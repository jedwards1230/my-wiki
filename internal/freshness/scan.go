package freshness

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// scanResult is one tree walk: the newest file mtime seen and how many files
// contributed to it.
type scanResult struct {
	newest time.Time // zero when files == 0
	files  int
}

// scanTree walks root and reports the newest file mtime and the file count.
//
// It walks the real filesystem rather than going through vault.Storage because
// the Storage abstraction cannot represent mtimes at all (MemStorage.Stat
// returns a FileInfo with no modtime). internal/cli's scanInboxSnapshot sets
// the same precedent for stat-oriented work, and this matches its error
// tolerance: per-entry failures are skipped so one unreadable file in a large
// vault cannot invalidate the whole observation. Only a failure to stat root
// itself is an error.
//
// Rules:
//   - Files only. Directory mtimes behave inconsistently across filesystems
//     (and change for reasons unrelated to content), so they are never counted.
//   - Top-level directories named in excludeTopLevel are pruned. For the vault
//     this is vault.DefaultExcludedDirs (".obsidian"), reusing the existing
//     exclusion list rather than inventing a third one.
//   - Every file counts, not just markdown: a raw/ attachment is real vault
//     content and a change there is a real change.
//   - A symlinked root is resolved first (see below); symlinks inside the tree
//     are not followed, so such a link counts as one file with its own mtime.
func scanTree(root string, excludeTopLevel []string) (scanResult, error) {
	var res scanResult
	fi, err := os.Stat(root)
	if err != nil {
		return res, err
	}
	if !fi.IsDir() {
		return res, fmt.Errorf("scan root %q is not a directory", root)
	}
	// os.Stat follows symlinks, but filepath.WalkDir lstats its root: handed a
	// symlinked directory it yields the link itself as a single non-dir entry
	// and descends no further. That reports one "file" whose mtime never
	// changes — a permanently frozen gauge, with no scan error to explain it,
	// which is exactly the false staleness alarm this package exists to
	// prevent. Resolve the root before walking so a symlinked vault behaves
	// like a real one. (Symlinks *inside* the tree are still not followed, so
	// there is no loop or escape risk.)
	walkRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return res, err
	}
	err = filepath.WalkDir(walkRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			rel, relErr := filepath.Rel(walkRoot, p)
			if relErr != nil {
				return nil
			}
			if slices.Contains(excludeTopLevel, filepath.ToSlash(rel)) {
				return fs.SkipDir
			}
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		res.files++
		if mt := info.ModTime(); mt.After(res.newest) {
			res.newest = mt
		}
		return nil
	})
	return res, err
}

// readSyncState reports the newest mtime at path, which may be either a single
// file or a directory.
//
// The file form exists for the split-volume deployment: when the sync process
// keeps its state on its own RWO volume, the server cannot read that directory
// at all, so the two sides instead agree on a heartbeat file the sync process
// touches every tick on a shared volume. The directory form is kept for
// deployments where the state directory is genuinely readable.
//
// The three return cases are deliberately distinct:
//
//   - (mtime, true, nil)  — read succeeded, there is a value to export.
//   - (zero, false, nil)  — the path does not exist. NOT an error: a sync
//     process that has not yet written its heartbeat is a legitimate state, and
//     conflating it with an I/O failure would make the error counter fire on
//     every fresh deployment. Signalled to consumers by metric absence.
//   - (zero, false, err)  — a real failure (permissions, I/O).
//
// An existing but empty directory returns (zero, false, nil) for the same
// reason: there is no mtime to report, and reporting 0 would mean the epoch.
func readSyncState(path string) (time.Time, bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, err
	}
	if !fi.IsDir() {
		return fi.ModTime(), true, nil
	}
	// No exclusions: the sync-state directory is opaque to us on purpose, so
	// this stays generic rather than encoding any particular tool's layout.
	res, err := scanTree(path, nil)
	if err != nil {
		return time.Time{}, false, err
	}
	if res.files == 0 {
		return time.Time{}, false, nil
	}
	return res.newest, true, nil
}
