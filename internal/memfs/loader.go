package memfs

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// LoaderOptions configure Load.
type LoaderOptions struct {
	// MaxFileBytes caps the size of any single file read into the
	// snapshot. Files larger than the cap are skipped and a warning is
	// logged — keeps one oversize asset from blowing the process memory
	// budget. Zero means unlimited.
	MaxFileBytes int64

	// MaxTotalBytes caps the total bytes across all files. Load returns
	// an error once the running total would exceed the cap. Zero means
	// unlimited.
	MaxTotalBytes int64

	// Logger receives skip warnings. If nil, slog.Default is used.
	Logger *slog.Logger
}

// Load walks sourceDir and returns a fresh Snapshot containing every
// regular file. Directory entries are materialized implicitly by the
// Snapshot itself (see Snapshot.AddFile). Paths in the snapshot are
// forward-slash-relative to sourceDir.
//
// Load does not follow symlinks. Any symlink encountered while walking
// sourceDir is skipped with a warning. This guards against malformed
// SSG output or operator error creating unbounded walks via cycles or
// links that resolve outside the tree. If symlink support is ever
// required, add a LoaderOption and validate target paths explicitly.
func Load(sourceDir string, opts LoaderOptions) (*Snapshot, error) {
	if sourceDir == "" {
		return nil, errors.New("memfs: sourceDir is required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	root, err := filepath.Abs(sourceDir)
	if err != nil {
		return nil, fmt.Errorf("memfs: resolve sourceDir: %w", err)
	}

	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("memfs: stat sourceDir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("memfs: sourceDir %q is not a directory", root)
	}

	snap := NewSnapshot()

	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			logger.Warn("memfs: walk entry error", "path", p, "error", walkErr)
			return nil
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		if d.IsDir() {
			// Snapshot.AddFile materializes parent dirs implicitly; we
			// still record empty directories explicitly so ReadDir on an
			// empty directory returns nil rather than fs.ErrNotExist.
			snap.addDir(rel)
			return nil
		}
		// Reject symlinks: they are rare in SSG output and make the
		// cap-enforcement story messy (target size isn't known without
		// stat). If we ever need them, add a flag.
		if d.Type()&fs.ModeSymlink != 0 {
			logger.Warn("memfs: skipping symlink", "path", rel)
			return nil
		}

		fi, err := d.Info()
		if err != nil {
			logger.Warn("memfs: stat failed", "path", rel, "error", err)
			return nil
		}
		// Early cap check on stat-reported size to avoid reading files
		// we're going to throw away.
		if opts.MaxFileBytes > 0 && fi.Size() > opts.MaxFileBytes {
			logger.Warn("memfs: file exceeds MaxFileBytes; skipping",
				"path", rel, "size", fi.Size(), "limit", opts.MaxFileBytes)
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			logger.Warn("memfs: read file failed", "path", rel, "error", err)
			return nil
		}
		// Re-check against len(data) — a file that grew between stat
		// and read (common during SSG rebuild bursts) could now exceed
		// the limit. We use the actual byte count that will live in
		// the snapshot, which is the only source of truth for Bytes().
		dataSize := int64(len(data))
		if opts.MaxFileBytes > 0 && dataSize > opts.MaxFileBytes {
			logger.Warn("memfs: file exceeds MaxFileBytes after read; skipping",
				"path", rel, "size", dataSize, "limit", opts.MaxFileBytes)
			return nil
		}
		if opts.MaxTotalBytes > 0 && snap.totalBytes+dataSize > opts.MaxTotalBytes {
			return fmt.Errorf("memfs: total size would exceed MaxTotalBytes (%d); aborting at %q", opts.MaxTotalBytes, rel)
		}
		if err := snap.AddFile(rel, data, fi.ModTime()); err != nil {
			logger.Warn("memfs: add file failed", "path", rel, "error", err)
			return nil
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return snap, nil
}

// addDir is a private helper used by the loader to explicitly record an
// (possibly empty) directory so ReadDir returns a valid empty list for
// it. AddFile already materializes parent directories for files, so this
// only matters for directories that contain no files.
func (s *Snapshot) addDir(name string) {
	if name == "" || name == "." {
		return
	}
	if strings.ContainsAny(name, "\\") {
		// Loader normalizes to forward slashes upstream, so this is only a
		// belt-and-braces check.
		return
	}
	if _, ok := s.entries[name]; ok {
		return
	}
	s.entries[name] = Entry{isDir: true}
}
