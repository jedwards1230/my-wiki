package notify

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"
)

// VaultWatcher watches a vault directory for filesystem changes and feeds
// them into a RebuildNotifier. It recursively watches all subdirectories
// and only forwards .md file changes (Create, Write, Remove, Rename).
type VaultWatcher struct {
	watcher     *fsnotify.Watcher
	notifier    *RebuildNotifier
	vaultDir    string
	excludeDirs []string // top-level dirs to skip, e.g. [".obsidian", "raw", "private"]
	logger      *slog.Logger
}

// WatcherOption configures a VaultWatcher.
type WatcherOption func(*VaultWatcher)

// WithExcludeDirs sets top-level directories to exclude from watching.
func WithExcludeDirs(dirs []string) WatcherOption {
	return func(w *VaultWatcher) { w.excludeDirs = dirs }
}

// WithWatcherLogger sets the logger for the watcher.
func WithWatcherLogger(logger *slog.Logger) WatcherOption {
	return func(w *VaultWatcher) { w.logger = logger }
}

// NewVaultWatcher creates a watcher that recursively monitors vaultDir and
// calls notifier.MarkDirty for every .md file change. The caller must invoke
// Run() in a goroutine and Close() when done.
func NewVaultWatcher(vaultDir string, notifier *RebuildNotifier, opts ...WatcherOption) (*VaultWatcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	vw := &VaultWatcher{
		watcher:  fsw,
		notifier: notifier,
		vaultDir: vaultDir,
		logger:   slog.Default(),
	}
	for _, o := range opts {
		o(vw)
	}

	if err := vw.addRecursive(vaultDir); err != nil {
		_ = fsw.Close()
		return nil, err
	}

	return vw, nil
}

// Run processes filesystem events until the watcher is closed. It should be
// called in its own goroutine.
func (vw *VaultWatcher) Run() {
	for {
		select {
		case event, ok := <-vw.watcher.Events:
			if !ok {
				return
			}

			rel, _ := filepath.Rel(vw.vaultDir, event.Name)
			if vw.isExcluded(rel) {
				continue
			}

			// New directory — add to watcher so we catch files created inside it.
			if event.Has(fsnotify.Create) {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					_ = vw.watcher.Add(event.Name)
					continue
				}
			}

			// Only forward .md file changes to the notifier.
			if filepath.Ext(event.Name) != ".md" {
				continue
			}

			if event.Has(fsnotify.Create) || event.Has(fsnotify.Write) || event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
				vw.logger.Debug("vault file changed", "path", rel, "op", event.Op.String())
				vw.notifier.MarkDirty(event.Name)
			}

		case err, ok := <-vw.watcher.Errors:
			if !ok {
				return
			}
			vw.logger.Warn("fsnotify error", "error", err)
		}
	}
}

// Close stops the filesystem watcher and releases resources.
func (vw *VaultWatcher) Close() error {
	return vw.watcher.Close()
}

// isExcluded returns true if the relative path starts with an excluded directory.
func (vw *VaultWatcher) isExcluded(rel string) bool {
	parts := strings.SplitN(filepath.ToSlash(rel), "/", 2)
	if len(parts) == 0 {
		return false
	}
	for _, d := range vw.excludeDirs {
		if parts[0] == d {
			return true
		}
	}
	return false
}

// addRecursive walks dir and adds every non-excluded subdirectory to the watcher.
func (vw *VaultWatcher) addRecursive(dir string) error {
	return filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(vw.vaultDir, p)
		if vw.isExcluded(rel) {
			return filepath.SkipDir
		}
		return vw.watcher.Add(p)
	})
}
