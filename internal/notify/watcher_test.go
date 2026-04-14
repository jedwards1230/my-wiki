package notify

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWatcherDetectsMDFileCreation(t *testing.T) {
	dir := t.TempDir()

	var mu sync.Mutex
	var flushed []string

	notifier := New(50*time.Millisecond, func(paths []string) {
		mu.Lock()
		flushed = append(flushed, paths...)
		mu.Unlock()
	})
	defer notifier.Close()

	vw, err := NewVaultWatcher(dir, notifier)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vw.Close() }()
	go vw.Run()

	// Give the watcher time to start.
	time.Sleep(50 * time.Millisecond)

	// Create a .md file — should trigger MarkDirty.
	target := filepath.Join(dir, "test.md")
	if err := os.WriteFile(target, []byte("# hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Wait for debounce + flush.
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(flushed) == 0 {
		t.Fatal("expected at least one flushed path for .md creation")
	}
	found := false
	for _, p := range flushed {
		if p == target {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected %s in flushed paths, got %v", target, flushed)
	}
}

func TestWatcherIgnoresNonMDFiles(t *testing.T) {
	dir := t.TempDir()

	var mu sync.Mutex
	var flushed []string

	notifier := New(50*time.Millisecond, func(paths []string) {
		mu.Lock()
		flushed = append(flushed, paths...)
		mu.Unlock()
	})
	defer notifier.Close()

	vw, err := NewVaultWatcher(dir, notifier)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vw.Close() }()
	go vw.Run()

	time.Sleep(50 * time.Millisecond)

	// Create a non-.md file — should NOT trigger MarkDirty.
	if err := os.WriteFile(filepath.Join(dir, "image.png"), []byte("fake png"), 0o644); err != nil {
		t.Fatal(err)
	}

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(flushed) != 0 {
		t.Fatalf("expected no flushed paths for non-.md file, got %v", flushed)
	}
}

func TestWatcherExcludesDirectories(t *testing.T) {
	dir := t.TempDir()

	// Pre-create excluded directory.
	obsDir := filepath.Join(dir, ".obsidian")
	if err := os.MkdirAll(obsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var flushed []string

	notifier := New(50*time.Millisecond, func(paths []string) {
		mu.Lock()
		flushed = append(flushed, paths...)
		mu.Unlock()
	})
	defer notifier.Close()

	vw, err := NewVaultWatcher(dir, notifier,
		WithExcludeDirs([]string{".obsidian"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vw.Close() }()
	go vw.Run()

	time.Sleep(50 * time.Millisecond)

	// Write to excluded directory — should NOT trigger.
	if err := os.WriteFile(filepath.Join(obsDir, "config.md"), []byte("config"), 0o644); err != nil {
		t.Fatal(err)
	}

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(flushed) != 0 {
		t.Fatalf("expected no flushed paths for excluded dir, got %v", flushed)
	}
}

func TestWatcherDetectsNewSubdirectoryFiles(t *testing.T) {
	dir := t.TempDir()

	var mu sync.Mutex
	var flushed []string

	notifier := New(50*time.Millisecond, func(paths []string) {
		mu.Lock()
		flushed = append(flushed, paths...)
		mu.Unlock()
	})
	defer notifier.Close()

	vw, err := NewVaultWatcher(dir, notifier)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vw.Close() }()
	go vw.Run()

	time.Sleep(50 * time.Millisecond)

	// Create a new subdirectory, then a file inside it.
	subDir := filepath.Join(dir, "notes")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Give watcher time to add the new directory.
	time.Sleep(100 * time.Millisecond)

	target := filepath.Join(subDir, "page.md")
	if err := os.WriteFile(target, []byte("# page"), 0o644); err != nil {
		t.Fatal(err)
	}

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, p := range flushed {
		if p == target {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected %s in flushed paths after subdir creation, got %v", target, flushed)
	}
}
