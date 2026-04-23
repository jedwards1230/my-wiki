package memfs

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ReloadCallback is invoked after every reload attempt. duration is the
// walk+read wall-clock time; err is non-nil if the reload failed (in
// which case the FS still points at the previous snapshot). Handlers
// typically publish metrics from this hook.
type ReloadCallback func(files int, bytes int64, duration time.Duration, err error)

// WatcherOptions configure a Watcher.
type WatcherOptions struct {
	// Debounce is how long the watcher waits after the last filesystem
	// event before triggering a reload. Smaller = faster convergence,
	// more reload work during a burst. 250ms is a reasonable default
	// for SSG output: most rebuilds write tens-to-hundreds of files in
	// a single sub-second burst.
	Debounce time.Duration

	// Loader overrides control how the source directory is loaded.
	Loader LoaderOptions

	// OnReload is optional; see ReloadCallback.
	OnReload ReloadCallback

	// Logger receives informational and warning events. nil → slog.Default.
	Logger *slog.Logger
}

// Watcher continuously reloads an FS in response to filesystem changes
// under sourceDir. The FS's snapshot pointer is swapped atomically on
// every successful reload; readers never see a partial update.
type Watcher struct {
	sourceDir string
	fs        *FS
	opts      WatcherOptions
	logger    *slog.Logger

	w *fsnotify.Watcher

	mu     sync.Mutex
	timer  *time.Timer
	gen    uint64
	closed bool
	stopCh chan struct{}
	doneCh chan struct{}
}

// NewWatcher constructs a Watcher, performs one initial synchronous
// Load, and returns. Call Start() to begin processing filesystem events
// in a background goroutine. Close() stops the watcher and waits for
// the goroutine to exit.
//
// The initial Load is synchronous so serve.go can fail fast if the
// source directory is wrong. After Start, reload errors are logged but
// do not return; the existing snapshot remains in effect.
func NewWatcher(sourceDir string, f *FS, opts WatcherOptions) (*Watcher, error) {
	if f == nil {
		return nil, errors.New("memfs: FS is required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if opts.Debounce <= 0 {
		opts.Debounce = 250 * time.Millisecond
	}

	initial, err := Load(sourceDir, opts.Loader)
	if err != nil {
		return nil, err
	}
	f.Store(initial)
	if opts.OnReload != nil {
		opts.OnReload(initial.Files(), initial.Bytes(), 0, nil)
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	if err := addRecursive(fsw, sourceDir); err != nil {
		_ = fsw.Close()
		return nil, err
	}

	return &Watcher{
		sourceDir: sourceDir,
		fs:        f,
		opts:      opts,
		logger:    logger,
		w:         fsw,
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}, nil
}

// Start begins processing filesystem events. Non-blocking; returns
// immediately after launching the goroutine.
func (w *Watcher) Start() {
	go w.run()
}

// Close stops the watcher, releases fsnotify resources, and waits for
// the event-processing goroutine to exit. Safe to call multiple times.
func (w *Watcher) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	close(w.stopCh)
	w.mu.Unlock()

	err := w.w.Close()
	<-w.doneCh
	return err
}

func (w *Watcher) run() {
	defer close(w.doneCh)
	for {
		select {
		case <-w.stopCh:
			return
		case ev, ok := <-w.w.Events:
			if !ok {
				return
			}
			// New directories need to be watched or we'll miss future
			// events within them.
			if ev.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					if err := addRecursive(w.w, ev.Name); err != nil {
						w.logger.Warn("memfs: watch new dir failed", "path", ev.Name, "error", err)
					}
				}
			}
			w.scheduleReload()
		case err, ok := <-w.w.Errors:
			if !ok {
				return
			}
			w.logger.Warn("memfs: fsnotify error", "error", err)
		}
	}
}

// scheduleReload (re)arms the debounce timer. The generation counter
// protects against a stale timer callback firing after a newer event
// has already rearmed the timer.
func (w *Watcher) scheduleReload() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.gen++
	gen := w.gen
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(w.opts.Debounce, func() {
		w.reloadIfCurrent(gen)
	})
}

func (w *Watcher) reloadIfCurrent(gen uint64) {
	w.mu.Lock()
	if w.closed || w.gen != gen {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()

	start := time.Now()
	snap, err := Load(w.sourceDir, w.opts.Loader)
	dur := time.Since(start)
	if err != nil {
		w.logger.Warn("memfs: reload failed; keeping previous snapshot",
			"error", err, "duration", dur)
		if w.opts.OnReload != nil {
			w.opts.OnReload(0, 0, dur, err)
		}
		return
	}
	w.fs.Store(snap)
	w.logger.Debug("memfs: reloaded", "files", snap.Files(), "bytes", snap.Bytes(), "duration", dur)
	if w.opts.OnReload != nil {
		w.opts.OnReload(snap.Files(), snap.Bytes(), dur, nil)
	}
}

// addRecursive adds every non-hidden directory under root to the
// fsnotify watcher. fsnotify's per-directory watches do not recurse, so
// we do it manually once; new directories created later are added on
// the fly in the event loop.
func addRecursive(w *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		return w.Add(p)
	})
}
