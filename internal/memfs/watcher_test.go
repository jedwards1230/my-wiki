package memfs

import (
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// waitForContent polls f.Open(name) until the content matches want or
// the deadline passes. Returns what it last saw.
func waitForContent(t *testing.T, f *FS, name, want string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		file, err := f.Open(name)
		if err == nil {
			b, _ := io.ReadAll(file)
			_ = file.Close()
			last = string(b)
			if last == want {
				return last
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return last
}

func TestWatcher_ReloadsOnWrite(t *testing.T) {
	root := t.TempDir()
	page := filepath.Join(root, "index.html")
	writeFile(t, page, "v1")

	f := New()
	w, err := NewWatcher(root, f, WatcherOptions{Debounce: 30 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()
	w.Start()

	if got := waitForContent(t, f, "index.html", "v1", time.Second); got != "v1" {
		t.Fatalf("initial load: got %q want v1", got)
	}

	// Rewrite the file; watcher must observe and swap in a fresh snapshot.
	if err := os.WriteFile(page, []byte("v2"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if got := waitForContent(t, f, "index.html", "v2", 2*time.Second); got != "v2" {
		t.Fatalf("after write: got %q want v2", got)
	}
}

func TestWatcher_PicksUpNewDirectories(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "index.html"), "root")

	f := New()
	w, err := NewWatcher(root, f, WatcherOptions{Debounce: 30 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()
	w.Start()

	// Create a new subdirectory + file after the watcher is running. The
	// fsnotify watch on root fires Create(newdir); the watcher must add
	// newdir to the watch list so the subsequent write is picked up.
	newDir := filepath.Join(root, "later")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Give the watcher a moment to register the new directory before
	// writing inside it. In practice this is <10ms; 100ms leaves comfort
	// room without slowing the suite.
	time.Sleep(100 * time.Millisecond)
	writeFile(t, filepath.Join(newDir, "page.html"), "new page")

	if got := waitForContent(t, f, "later/page.html", "new page", 2*time.Second); got != "new page" {
		t.Fatalf("after create-dir+write: got %q want %q", got, "new page")
	}
}

// TestWatcher_RecoversFromDestructiveRebuild simulates a producer
// (like Quartz) that wipes sourceDir and rewrites it from scratch. The
// original inotify watches die with the old inodes; without the resync
// + re-add-watches fix, the watcher would silently stop observing the
// tree even though sourceDir once again exists.
func TestWatcher_RecoversFromDestructiveRebuild(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "index.html"), "v1")

	f := New()
	w, err := NewWatcher(root, f, WatcherOptions{
		Debounce:       20 * time.Millisecond,
		ResyncInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()
	w.Start()

	if got := waitForContent(t, f, "index.html", "v1", time.Second); got != "v1" {
		t.Fatalf("initial load: got %q want v1", got)
	}

	// Destructive rebuild: remove the whole tree, then recreate with
	// different content. fsnotify watches on the old inodes are now
	// stale — only the resync ticker + re-add on reload can recover.
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeFile(t, filepath.Join(root, "index.html"), "v2")
	writeFile(t, filepath.Join(root, "fresh.html"), "new file")

	// Wait up to a handful of resync intervals for the recovery path
	// to swap in the new snapshot. Generous so CI noise doesn't flake.
	if got := waitForContent(t, f, "index.html", "v2", 2*time.Second); got != "v2" {
		t.Fatalf("after destructive rebuild: got %q want v2 — watcher did not recover", got)
	}
	if got := waitForContent(t, f, "fresh.html", "new file", 2*time.Second); got != "new file" {
		t.Fatalf("new file not visible after recovery: got %q", got)
	}

	// A subsequent in-place write should flow through normally now that
	// watches have been re-established on the new inodes.
	writeFile(t, filepath.Join(root, "index.html"), "v3")
	if got := waitForContent(t, f, "index.html", "v3", 2*time.Second); got != "v3" {
		t.Fatalf("post-recovery fsnotify event: got %q want v3", got)
	}
}

// TestWatcher_ResyncFiresWithoutEvents pins the periodic-safety-net
// behavior: even in the absence of new fsnotify activity between
// ticks, the watcher reloads and refreshes the snapshot. We prove
// this by comparing reload counts before and after a ticker interval
// passes with no producer activity — OnReload must have been called
// at least once beyond the initial synchronous load.
func TestWatcher_ResyncFiresWithoutEvents(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "index.html"), "v1")

	// Count reload callbacks so we can assert "more than just the
	// initial load fired" after one resync interval passes.
	var reloads int32
	cb := func(_ int, _ int64, _ time.Duration, err error) {
		if err == nil {
			atomic.AddInt32(&reloads, 1)
		}
	}

	f := New()
	w, err := NewWatcher(root, f, WatcherOptions{
		Debounce:       10 * time.Millisecond,
		ResyncInterval: 40 * time.Millisecond,
		OnReload:       cb,
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()
	w.Start()

	// Wait several ticker intervals with no file activity at all. The
	// initial synchronous load fires the callback once (reloads == 1).
	// Each subsequent tick fires it again.
	time.Sleep(250 * time.Millisecond)

	if got := atomic.LoadInt32(&reloads); got < 2 {
		t.Fatalf("resync did not fire without events: reloads=%d want >=2", got)
	}
}

func TestWatcher_CallbackReceivesMetrics(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.html"), "aa")
	writeFile(t, filepath.Join(root, "b.html"), "bbbb")

	type reload struct {
		files int
		bytes int64
	}
	var mu = make(chan reload, 4)

	f := New()
	w, err := NewWatcher(root, f, WatcherOptions{
		Debounce: 30 * time.Millisecond,
		OnReload: func(files int, bytes int64, _ time.Duration, err error) {
			if err == nil {
				select {
				case mu <- reload{files: files, bytes: bytes}:
				default:
				}
			}
		},
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()
	w.Start()

	// Initial synchronous load should have fired the callback once with
	// the two files totaling 6 bytes.
	select {
	case r := <-mu:
		if r.files != 2 || r.bytes != 6 {
			t.Fatalf("initial callback: files=%d bytes=%d; want 2/6", r.files, r.bytes)
		}
	case <-time.After(time.Second):
		t.Fatal("initial OnReload never fired")
	}
}
