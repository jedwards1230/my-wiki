package memfs

import (
	"io"
	"os"
	"path/filepath"
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
