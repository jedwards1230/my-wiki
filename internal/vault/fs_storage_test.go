package vault

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesystemStorage_ReadWriteFile(t *testing.T) {
	dir := t.TempDir()
	s := NewFilesystemStorage(dir)

	data := []byte("hello world")
	if err := s.WriteFile("test.txt", data, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := s.ReadFile("test.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello world" {
		t.Errorf("expected 'hello world', got %q", string(got))
	}
}

func TestFilesystemStorage_WriteCreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	s := NewFilesystemStorage(dir)

	if err := s.WriteFile("deep/nested/file.txt", []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "deep", "nested", "file.txt")); err != nil {
		t.Fatal("expected nested file to exist")
	}
}

func TestFilesystemStorage_Remove(t *testing.T) {
	dir := t.TempDir()
	s := NewFilesystemStorage(dir)

	if err := s.WriteFile("deleteme.txt", []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("deleteme.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stat("deleteme.txt"); !os.IsNotExist(err) {
		t.Fatal("expected file to be removed")
	}
}

func TestFilesystemStorage_Stat(t *testing.T) {
	dir := t.TempDir()
	s := NewFilesystemStorage(dir)

	if err := s.WriteFile("exists.txt", []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := s.Stat("exists.txt")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 4 {
		t.Errorf("expected size 4, got %d", info.Size())
	}
}

func TestFilesystemStorage_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	s := NewFilesystemStorage(dir)

	_, err := s.ReadFile("../../etc/passwd")
	if err == nil {
		t.Fatal("expected path traversal error")
	}
}

func TestFilesystemStorage_MkdirAll(t *testing.T) {
	dir := t.TempDir()
	s := NewFilesystemStorage(dir)

	if err := s.MkdirAll("a/b/c", 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "a", "b", "c"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("expected directory")
	}
}

func TestFilesystemStorage_ReadDir(t *testing.T) {
	dir := t.TempDir()
	s := NewFilesystemStorage(dir)

	if err := s.WriteFile("file1.txt", []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteFile("file2.txt", []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := s.ReadDir("")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries))
	}
}

func TestFilesystemStorage_WalkDir(t *testing.T) {
	dir := t.TempDir()
	s := NewFilesystemStorage(dir)

	if err := s.WriteFile("a.txt", []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteFile("sub/b.txt", []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}

	var paths []string
	err := s.WalkDir("", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Errorf("expected 2 files, got %d: %v", len(paths), paths)
	}
}

func TestFilesystemStorage_Root(t *testing.T) {
	dir := t.TempDir()
	s := NewFilesystemStorage(dir)

	if s.Root() != dir {
		t.Errorf("expected root %q, got %q", dir, s.Root())
	}
}

func TestFilesystemStorage_Rename(t *testing.T) {
	dir := t.TempDir()
	s := NewFilesystemStorage(dir)

	if err := s.WriteFile("old.txt", []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := s.Rename("old.txt", "new.txt"); err != nil {
		t.Fatal(err)
	}

	// Old file should be gone
	if _, err := s.Stat("old.txt"); !os.IsNotExist(err) {
		t.Error("expected old file to be removed")
	}

	// New file should exist with same content
	got, err := s.ReadFile("new.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "data" {
		t.Errorf("expected 'data', got %q", string(got))
	}
}

func TestFilesystemStorage_RenameCreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	s := NewFilesystemStorage(dir)

	if err := s.WriteFile("src.txt", []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := s.Rename("src.txt", "deep/nested/dst.txt"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "deep", "nested", "dst.txt")); err != nil {
		t.Fatal("expected nested destination to exist")
	}
}

func TestFilesystemStorage_RenamePathTraversal(t *testing.T) {
	dir := t.TempDir()
	s := NewFilesystemStorage(dir)

	if err := s.WriteFile("src.txt", []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := s.Rename("src.txt", "../../etc/evil.txt")
	if err == nil {
		t.Fatal("expected path traversal error")
	}
}

func TestFilesystemStorage_OpenFile(t *testing.T) {
	dir := t.TempDir()
	s := NewFilesystemStorage(dir)

	if err := s.WriteFile("openme.txt", []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := s.OpenFile("openme.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, 7)
	n, err := f.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "content" {
		t.Errorf("expected 'content', got %q", string(buf[:n]))
	}
}
