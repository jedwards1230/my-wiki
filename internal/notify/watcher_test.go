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

// TestWatcherDetectsFilesInAtomicallyMovedDirectory reproduces the inotify
// subdirectory race: when a populated directory tree is moved into the vault
// in a single operation (the pattern Obsidian Sync uses), fsnotify can
// deliver the inner file events before our watch is attached. The watcher
// must scan newly-watched directories after Add() to recover those events.
func TestWatcherDetectsFilesInAtomicallyMovedDirectory(t *testing.T) {
	dir := t.TempDir()
	staging := t.TempDir()

	// Build a populated subdirectory OUTSIDE the watched vault.
	stagedSub := filepath.Join(staging, "clippings")
	if err := os.MkdirAll(stagedSub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagedSub, "first.md"), []byte("# first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagedSub, "second.md"), []byte("# second"), 0o644); err != nil {
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

	vw, err := NewVaultWatcher(dir, notifier)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vw.Close() }()
	go vw.Run()
	time.Sleep(50 * time.Millisecond)

	// Atomically move the populated tree into the watched vault. The kernel
	// emits the Create event for the directory and the file inodes inside
	// it nearly simultaneously, so the inner-file events race with our
	// watcher.Add() call. Without the synthetic-event scan, these would be
	// silently lost (the bug observed when Obsidian Sync first created
	// inbox/clippings/).
	finalSub := filepath.Join(dir, "clippings")
	if err := os.Rename(stagedSub, finalSub); err != nil {
		t.Fatal(err)
	}

	time.Sleep(400 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	want := map[string]bool{
		filepath.Join(finalSub, "first.md"):  false,
		filepath.Join(finalSub, "second.md"): false,
	}
	for _, p := range flushed {
		if _, ok := want[p]; ok {
			want[p] = true
		}
	}
	for p, seen := range want {
		if !seen {
			t.Fatalf("missing synthetic create for %s; flushed=%v", p, flushed)
		}
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
