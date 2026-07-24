package freshness

import (
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
func scanTree(root string, excludeTopLevel []string) (scanResult, error) {
	var res scanResult
	if _, err := os.Stat(root); err != nil {
		return res, err
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			rel, relErr := filepath.Rel(root, p)
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
