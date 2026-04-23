package memfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_BasicTree(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "index.html"), "<html>root</html>")
	writeFile(t, filepath.Join(root, "docs", "page.html"), "<html>page</html>")
	writeFile(t, filepath.Join(root, "assets", "style.css"), "body{}")
	_ = os.MkdirAll(filepath.Join(root, "empty-dir"), 0o755)

	snap, err := Load(root, LoaderOptions{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := snap.Files(); got != 3 {
		t.Errorf("Files: got %d want 3", got)
	}
	if _, ok := snap.entries["empty-dir"]; !ok {
		t.Error("empty-dir should be present so ReadDir returns nil, not ErrNotExist")
	}
	if e, ok := snap.entries["docs/page.html"]; !ok || string(e.Data) != "<html>page</html>" {
		t.Errorf("docs/page.html missing or wrong content: %v", e)
	}
}

func TestLoad_SkipsOversizedFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "small.html"), "ok")
	// 2 KiB file; MaxFileBytes is 1 KiB.
	writeFile(t, filepath.Join(root, "big.html"), string(make([]byte, 2048)))

	snap, err := Load(root, LoaderOptions{MaxFileBytes: 1024})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := snap.entries["big.html"]; ok {
		t.Error("big.html should have been skipped by MaxFileBytes")
	}
	if _, ok := snap.entries["small.html"]; !ok {
		t.Error("small.html should still be loaded")
	}
}

func TestLoad_AbortsWhenTotalExceeded(t *testing.T) {
	root := t.TempDir()
	// Two files of 1 KiB each; cap at 1.5 KiB so the second one pushes
	// the running total over the limit.
	writeFile(t, filepath.Join(root, "a.html"), string(make([]byte, 1024)))
	writeFile(t, filepath.Join(root, "b.html"), string(make([]byte, 1024)))

	_, err := Load(root, LoaderOptions{MaxTotalBytes: 1536})
	if err == nil {
		t.Fatal("expected error when MaxTotalBytes would be exceeded")
	}
}

func TestLoad_MissingDirErrors(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "does-not-exist"), LoaderOptions{})
	if err == nil || errors.Is(err, nil) {
		t.Fatal("expected error for missing sourceDir")
	}
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
