package vault

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MemStorage is an in-memory Storage implementation for testing.
// All paths are treated as forward-slash separated relative paths.
type MemStorage struct {
	files map[string][]byte
}

// NewMemStorage creates an empty in-memory storage.
func NewMemStorage() *MemStorage {
	return &MemStorage{files: make(map[string][]byte)}
}

func (m *MemStorage) ReadFile(relPath string) ([]byte, error) {
	relPath = normPath(relPath)
	data, ok := m.files[relPath]
	if !ok {
		return nil, &os.PathError{Op: "open", Path: relPath, Err: os.ErrNotExist}
	}
	return append([]byte(nil), data...), nil // return copy
}

func (m *MemStorage) WriteFile(relPath string, data []byte, _ fs.FileMode) error {
	relPath = normPath(relPath)
	m.files[relPath] = append([]byte(nil), data...) // store copy
	return nil
}

func (m *MemStorage) Remove(relPath string) error {
	relPath = normPath(relPath)
	if _, ok := m.files[relPath]; !ok {
		return &os.PathError{Op: "remove", Path: relPath, Err: os.ErrNotExist}
	}
	delete(m.files, relPath)
	return nil
}

func (m *MemStorage) Stat(relPath string) (fs.FileInfo, error) {
	relPath = normPath(relPath)

	// Check for exact file match.
	if data, ok := m.files[relPath]; ok {
		return &memFileInfo{name: filepath.Base(relPath), size: int64(len(data))}, nil
	}

	// Check if it's a directory prefix.
	prefix := relPath + "/"
	if relPath == "." || relPath == "" {
		prefix = ""
	}
	for k := range m.files {
		if strings.HasPrefix(k, prefix) {
			return &memFileInfo{name: filepath.Base(relPath), dir: true}, nil
		}
	}

	return nil, &os.PathError{Op: "stat", Path: relPath, Err: os.ErrNotExist}
}

func (m *MemStorage) OpenFile(relPath string, _ int, _ fs.FileMode) (io.ReadWriteCloser, error) {
	relPath = normPath(relPath)
	data, ok := m.files[relPath]
	if !ok {
		return nil, &os.PathError{Op: "open", Path: relPath, Err: os.ErrNotExist}
	}
	return &memFile{buf: bytes.NewBuffer(append([]byte(nil), data...)), storage: m, path: relPath}, nil
}

func (m *MemStorage) MkdirAll(_ string, _ fs.FileMode) error {
	return nil // directories are implicit
}

func (m *MemStorage) ReadDir(relPath string) ([]fs.DirEntry, error) {
	relPath = normPath(relPath)
	prefix := relPath + "/"
	if relPath == "." || relPath == "" {
		prefix = ""
	}

	seen := make(map[string]bool)
	var entries []fs.DirEntry

	for k := range m.files {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		rest := strings.TrimPrefix(k, prefix)
		parts := strings.SplitN(rest, "/", 2)
		name := parts[0]
		if seen[name] {
			continue
		}
		seen[name] = true

		isDir := len(parts) > 1
		entries = append(entries, &memDirEntry{name: name, dir: isDir})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	return entries, nil
}

func (m *MemStorage) WalkDir(relPath string, fn fs.WalkDirFunc) error {
	relPath = normPath(relPath)

	// Collect all relative paths under the prefix.
	prefix := relPath + "/"
	if relPath == "." || relPath == "" {
		relPath = "."
		prefix = ""
	}

	// Walk the root directory entry first.
	rootEntry := &memDirEntry{name: filepath.Base(relPath), dir: true}
	if err := fn(relPath, rootEntry, nil); err != nil {
		if err == filepath.SkipDir {
			return nil
		}
		return err
	}

	// Gather and sort all matching file paths.
	var paths []string
	for k := range m.files {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			paths = append(paths, k)
		}
	}
	sort.Strings(paths)

	// Track which directories we've already visited and which are skipped.
	visitedDirs := make(map[string]bool)
	skippedDirs := make(map[string]bool)

	isSkipped := func(p string) bool {
		for sd := range skippedDirs {
			if strings.HasPrefix(p, sd+"/") || p == sd {
				return true
			}
		}
		return false
	}

	for _, p := range paths {
		if isSkipped(p) {
			continue
		}

		// Emit intermediate directory entries.
		rel := p
		if prefix != "" {
			rel = strings.TrimPrefix(p, prefix)
		}
		parts := strings.Split(rel, "/")

		dirSkipped := false
		for i := 0; i < len(parts)-1; i++ {
			var dirPath string
			if prefix == "" {
				dirPath = strings.Join(parts[:i+1], "/")
			} else {
				dirPath = relPath + "/" + strings.Join(parts[:i+1], "/")
			}
			if visitedDirs[dirPath] {
				if skippedDirs[dirPath] {
					dirSkipped = true
					break
				}
				continue
			}
			visitedDirs[dirPath] = true
			dirEntry := &memDirEntry{name: parts[i], dir: true}
			if err := fn(dirPath, dirEntry, nil); err != nil {
				if err == filepath.SkipDir {
					skippedDirs[dirPath] = true
					dirSkipped = true
					break
				}
				return err
			}
		}

		if dirSkipped {
			continue
		}

		// Emit the file entry.
		fileEntry := &memDirEntry{name: filepath.Base(p), dir: false}
		if err := fn(p, fileEntry, nil); err != nil {
			if err == filepath.SkipDir {
				continue
			}
			return err
		}
	}

	return nil
}

// normPath cleans a path and converts "." or "" to ".".
func normPath(p string) string {
	p = filepath.Clean(p)
	if p == "." || p == "" {
		return "."
	}
	return p
}

// memFile wraps a bytes.Buffer as a ReadWriteCloser that flushes on close.
type memFile struct {
	buf     *bytes.Buffer
	storage *MemStorage
	path    string
}

func (f *memFile) Read(p []byte) (int, error)  { return f.buf.Read(p) }
func (f *memFile) Write(p []byte) (int, error) { return f.buf.Write(p) }
func (f *memFile) Close() error {
	f.storage.files[f.path] = f.buf.Bytes()
	return nil
}

// memFileInfo implements fs.FileInfo for in-memory files.
type memFileInfo struct {
	name string
	size int64
	dir  bool
}

func (fi *memFileInfo) Name() string       { return fi.name }
func (fi *memFileInfo) Size() int64        { return fi.size }
func (fi *memFileInfo) Mode() fs.FileMode  { return 0o644 }
func (fi *memFileInfo) ModTime() time.Time { return time.Time{} }
func (fi *memFileInfo) IsDir() bool        { return fi.dir }
func (fi *memFileInfo) Sys() any           { return nil }

// memDirEntry implements fs.DirEntry for in-memory files.
type memDirEntry struct {
	name string
	dir  bool
}

func (de *memDirEntry) Name() string      { return de.name }
func (de *memDirEntry) IsDir() bool       { return de.dir }
func (de *memDirEntry) Type() fs.FileMode { return 0 }
func (de *memDirEntry) Info() (fs.FileInfo, error) {
	return &memFileInfo{name: de.name, dir: de.dir}, nil
}

// AddFile is a test helper to add a file to the in-memory storage.
func (m *MemStorage) AddFile(relPath string, content string) {
	m.files[normPath(relPath)] = []byte(content)
}

// String returns a debug representation of all stored files.
func (m *MemStorage) String() string {
	var keys []string
	for k := range m.files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return fmt.Sprintf("MemStorage{%s}", strings.Join(keys, ", "))
}
