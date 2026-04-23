// Package memfs implements an fs.FS whose entire contents live in memory
// and are atomically swappable. It exists so static handlers can serve
// Quartz output (or any other directory tree) without ever racing against
// a rebuild that is rewriting files on disk.
//
// A Snapshot is an immutable view of a directory tree at one moment. An
// FS holds a pointer to the current Snapshot and publishes replacements
// atomically via Store. Concurrent readers always see a consistent view —
// either the old snapshot or the new one, never a half-swap.
//
// The package is deliberately small: it owns the shape of in-memory data
// and the atomic-swap primitive. Loading (reading a directory off disk
// into a Snapshot) and watching (triggering reloads on change) live in
// sibling files so this one stays test-friendly in isolation.
package memfs

import (
	"bytes"
	"errors"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// Entry is one file in a Snapshot. Directories are represented with
// isDir=true and zero-length Data; their contents are discovered through
// the flat map using prefix matching in ReadDir.
type Entry struct {
	Data    []byte
	ModTime time.Time
	isDir   bool
}

// Snapshot is an immutable directory tree. Keys are forward-slash paths
// relative to the snapshot root, no leading slash. Directory keys end in
// "" (the empty string for root) or the directory name itself without a
// trailing slash. Example keys for a dir containing `index.html` and
// `docs/faq.html`:
//
//	""             → isDir=true  (implicit root)
//	"index.html"   → isDir=false
//	"docs"         → isDir=true
//	"docs/faq.html"→ isDir=false
type Snapshot struct {
	entries map[string]Entry
	// totalBytes is the sum of all Data lengths. Used by metrics so the
	// FS does not have to recompute it on every Stats() call.
	totalBytes int64
}

// NewSnapshot returns an empty snapshot containing only the root directory.
func NewSnapshot() *Snapshot {
	return &Snapshot{
		entries: map[string]Entry{
			"": {ModTime: time.Now().UTC(), isDir: true},
		},
	}
}

// AddFile records a file entry. name is a forward-slash path relative to
// the snapshot root, no leading slash. Intermediate directories are
// materialized automatically so ReadDir works without a separate step.
// Passing a name containing "." or ".." segments is a programmer error
// and returns an error; callers building snapshots from disk should
// filepath.Clean upstream.
func (s *Snapshot) AddFile(name string, data []byte, modTime time.Time) error {
	if strings.Contains(name, "\\") {
		return errors.New("memfs: name must use forward slashes")
	}
	if name == "" || name == "." {
		return errors.New("memfs: name must be non-empty")
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return errors.New("memfs: name contains disallowed segment")
		}
	}
	s.entries[name] = Entry{Data: data, ModTime: modTime, isDir: false}
	s.totalBytes += int64(len(data))
	// Materialize parent directories so ReadDir on a parent sees this
	// entry even if the parent was never added explicitly.
	for dir := path.Dir(name); dir != "." && dir != "/"; dir = path.Dir(dir) {
		if _, ok := s.entries[dir]; !ok {
			s.entries[dir] = Entry{ModTime: modTime, isDir: true}
		}
	}
	return nil
}

// Files returns the number of file entries (not counting directories).
func (s *Snapshot) Files() int {
	n := 0
	for _, e := range s.entries {
		if !e.isDir {
			n++
		}
	}
	return n
}

// Bytes returns the total size in bytes of all file entries.
func (s *Snapshot) Bytes() int64 { return s.totalBytes }

// FS is an fs.FS backed by a swappable Snapshot. The zero value is not
// useful — use New. Implements fs.FS and fs.ReadDirFS.
type FS struct {
	current atomic.Pointer[Snapshot]
}

// New returns an FS that initially serves the empty snapshot. Callers
// are expected to Store a real snapshot before letting requests through;
// until they do, Open returns fs.ErrNotExist for every path.
func New() *FS {
	f := &FS{}
	f.current.Store(NewSnapshot())
	return f
}

// Store publishes snap as the current snapshot. After Store returns, all
// subsequent Open calls see snap. In-flight reads against the previous
// snapshot complete uninterrupted because they hold their own *Snapshot
// reference captured at Open time.
func (f *FS) Store(snap *Snapshot) {
	if snap == nil {
		snap = NewSnapshot()
	}
	f.current.Store(snap)
}

// Snapshot returns the current snapshot pointer. Intended for metrics
// and tests; handlers go through Open.
func (f *FS) Snapshot() *Snapshot {
	return f.current.Load()
}

// Open implements fs.FS. name follows fs.FS semantics: forward-slash,
// no leading slash, "." means root. Returns an fs.File whose underlying
// byte slice is the snapshot's — safe to hold across an FS.Store because
// Entry values are never mutated after insertion.
func (f *FS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	snap := f.current.Load()
	key := name
	if key == "." {
		key = ""
	}
	e, ok := snap.entries[key]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	if e.isDir {
		return &memDir{snap: snap, name: key, modTime: e.ModTime}, nil
	}
	return &memFile{name: key, entry: e, r: bytes.NewReader(e.Data)}, nil
}

// ReadDir implements fs.ReadDirFS so range-over-directory works without
// first opening a fs.ReadDirFile. Entries are returned sorted by name for
// reproducibility.
func (f *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	return readDirFromSnapshot(f.current.Load(), name)
}

// readDirFromSnapshot is the shared implementation used by both FS.ReadDir
// and memDir.ReadDir. Kept as a package-level function so memDir does not
// need to reach through FS (which would require copying an atomic.Pointer
// and tripping vet).
func readDirFromSnapshot(snap *Snapshot, name string) ([]fs.DirEntry, error) {
	prefix := name
	if prefix == "." {
		prefix = ""
	}
	e, ok := snap.entries[prefix]
	if !ok || !e.isDir {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	search := prefix
	if search != "" {
		search += "/"
	}
	var out []fs.DirEntry
	for k, v := range snap.entries {
		if k == prefix {
			continue
		}
		if !strings.HasPrefix(k, search) {
			continue
		}
		rest := k[len(search):]
		if strings.Contains(rest, "/") {
			continue
		}
		out = append(out, &memDirEntry{name: rest, entry: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

// memFile adapts an Entry to fs.File. It also satisfies io.ReadSeeker so
// http.ServeContent can handle Range requests efficiently without copying.
type memFile struct {
	name   string
	entry  Entry
	r      *bytes.Reader
	closed bool
}

func (f *memFile) Stat() (fs.FileInfo, error) {
	if f.closed {
		return nil, fs.ErrClosed
	}
	return &memFileInfo{name: path.Base(f.name), size: int64(len(f.entry.Data)), modTime: f.entry.ModTime}, nil
}

func (f *memFile) Read(p []byte) (int, error) {
	if f.closed {
		return 0, fs.ErrClosed
	}
	return f.r.Read(p)
}

// Seek makes memFile an io.ReadSeeker, which is important: the existing
// static handler uses http.ServeContent which calls Seek for Range
// support. Without this, ServeContent would fall back to reading the
// whole body into memory on every request.
func (f *memFile) Seek(offset int64, whence int) (int64, error) {
	if f.closed {
		return 0, fs.ErrClosed
	}
	return f.r.Seek(offset, whence)
}

func (f *memFile) Close() error {
	f.closed = true
	return nil
}

// memDir represents a directory. Reads return EOF — fs.FS expects
// directories to be opened as files and then ReadDir'd via the
// fs.ReadDirFile interface.
type memDir struct {
	snap    *Snapshot
	name    string
	modTime time.Time
	idx     int
}

func (d *memDir) Stat() (fs.FileInfo, error) {
	base := path.Base(d.name)
	if d.name == "" {
		base = "."
	}
	return &memFileInfo{name: base, modTime: d.modTime, isDir: true}, nil
}

func (d *memDir) Read(_ []byte) (int, error) { return 0, errors.New("is a directory") }

func (d *memDir) Close() error { return nil }

// ReadDir satisfies fs.ReadDirFile.
func (d *memDir) ReadDir(n int) ([]fs.DirEntry, error) {
	name := d.name
	if name == "" {
		name = "."
	}
	all, err := readDirFromSnapshot(d.snap, name)
	if err != nil {
		return nil, err
	}
	if d.idx >= len(all) {
		if n <= 0 {
			return nil, nil
		}
		return nil, errors.New("EOF")
	}
	start := d.idx
	if n <= 0 {
		d.idx = len(all)
		return all[start:], nil
	}
	end := start + n
	if end > len(all) {
		end = len(all)
	}
	d.idx = end
	return all[start:end], nil
}

type memDirEntry struct {
	name  string
	entry Entry
}

func (e *memDirEntry) Name() string      { return e.name }
func (e *memDirEntry) IsDir() bool       { return e.entry.isDir }
func (e *memDirEntry) Type() fs.FileMode { return e.mode().Type() }
func (e *memDirEntry) Info() (fs.FileInfo, error) {
	return &memFileInfo{name: e.name, size: int64(len(e.entry.Data)), modTime: e.entry.ModTime, isDir: e.entry.isDir}, nil
}
func (e *memDirEntry) mode() fs.FileMode {
	if e.entry.isDir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}

type memFileInfo struct {
	name    string
	size    int64
	modTime time.Time
	isDir   bool
}

func (i *memFileInfo) Name() string       { return i.name }
func (i *memFileInfo) Size() int64        { return i.size }
func (i *memFileInfo) ModTime() time.Time { return i.modTime }
func (i *memFileInfo) IsDir() bool        { return i.isDir }
func (i *memFileInfo) Sys() any           { return nil }
func (i *memFileInfo) Mode() fs.FileMode {
	if i.isDir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}
