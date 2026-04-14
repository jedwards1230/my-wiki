package vault

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FilesystemStorage implements Storage using the local filesystem.
type FilesystemStorage struct {
	root string
}

// NewFilesystemStorage creates a FilesystemStorage rooted at the given directory.
func NewFilesystemStorage(root string) *FilesystemStorage {
	return &FilesystemStorage{root: root}
}

// Root returns the absolute root directory path.
func (f *FilesystemStorage) Root() string {
	return f.root
}

func (f *FilesystemStorage) ReadFile(relPath string) ([]byte, error) {
	abs, err := f.resolve(relPath)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(abs)
}

func (f *FilesystemStorage) WriteFile(relPath string, data []byte, perm fs.FileMode) error {
	abs, err := f.resolve(relPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, data, perm)
}

func (f *FilesystemStorage) Remove(relPath string) error {
	abs, err := f.resolve(relPath)
	if err != nil {
		return err
	}
	return os.Remove(abs)
}

func (f *FilesystemStorage) Stat(relPath string) (fs.FileInfo, error) {
	abs, err := f.resolve(relPath)
	if err != nil {
		return nil, err
	}
	return os.Stat(abs)
}

func (f *FilesystemStorage) OpenFile(relPath string, flag int, perm fs.FileMode) (*os.File, error) {
	abs, err := f.resolve(relPath)
	if err != nil {
		return nil, err
	}
	return os.OpenFile(abs, flag, perm)
}

func (f *FilesystemStorage) MkdirAll(relPath string, perm fs.FileMode) error {
	abs, err := f.resolve(relPath)
	if err != nil {
		return err
	}
	return os.MkdirAll(abs, perm)
}

func (f *FilesystemStorage) ReadDir(relPath string) ([]os.DirEntry, error) {
	abs, err := f.resolve(relPath)
	if err != nil {
		return nil, err
	}
	return os.ReadDir(abs)
}

func (f *FilesystemStorage) WalkDir(relPath string, fn fs.WalkDirFunc) error {
	var abs string
	if relPath == "" || relPath == "." {
		abs = f.root
	} else {
		var err error
		abs, err = f.resolve(relPath)
		if err != nil {
			return err
		}
	}
	return filepath.WalkDir(abs, func(p string, d fs.DirEntry, walkErr error) error {
		rel, err := filepath.Rel(f.root, p)
		if err != nil {
			return err
		}
		return fn(rel, d, walkErr)
	})
}

// resolve converts a relative path to an absolute path within the root,
// preventing path traversal attacks.
func (f *FilesystemStorage) resolve(relPath string) (string, error) {
	if relPath == "" || relPath == "." {
		return filepath.Clean(f.root), nil
	}
	abs := filepath.Clean(filepath.Join(f.root, relPath))
	prefix := filepath.Clean(f.root) + string(filepath.Separator)
	if !strings.HasPrefix(abs, prefix) && abs != filepath.Clean(f.root) {
		return "", fmt.Errorf("path traversal not allowed")
	}
	return abs, nil
}
