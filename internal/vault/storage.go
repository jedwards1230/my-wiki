package vault

import (
	"io"
	"io/fs"
)

// Storage abstracts file-level operations on the vault, enabling alternative
// backends (git-sync, Syncthing, in-memory for tests) without changing service code.
// All paths are relative to the vault root.
type Storage interface {
	// ReadFile returns the content of a file at the given relative path.
	ReadFile(relPath string) ([]byte, error)

	// WriteFile writes content to a file at the given relative path,
	// creating parent directories as needed.
	WriteFile(relPath string, data []byte, perm fs.FileMode) error

	// Remove deletes a file at the given relative path.
	Remove(relPath string) error

	// Stat returns file info for the given relative path.
	Stat(relPath string) (fs.FileInfo, error)

	// OpenFile opens a file with the given flags and permissions.
	// The caller is responsible for closing the returned file.
	OpenFile(relPath string, flag int, perm fs.FileMode) (io.ReadWriteCloser, error)

	// MkdirAll creates a directory path and all parents.
	MkdirAll(relPath string, perm fs.FileMode) error

	// ReadDir reads the named directory.
	ReadDir(relPath string) ([]fs.DirEntry, error)

	// WalkDir walks the directory tree rooted at relPath, calling fn for each entry.
	// Paths passed to fn are relative to the storage root.
	WalkDir(relPath string, fn fs.WalkDirFunc) error
}
