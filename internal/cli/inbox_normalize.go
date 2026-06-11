package cli

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/jedwards1230/my-wiki/internal/slug"
)

// normalizeInboxFilenames renames inbox files whose filename is not already its
// canonical slug, so downstream consumers (the inbox-manager agent) only ever
// see stable, addressable names. Clippers and sync clients drop files named
// after human titles ("Thinking Machines’ Murati on AI’s Next Chapter.md");
// those smart-punctuation / double-space names don't survive an agent
// round-trip and become unreachable, which loops the dispatch. Normalizing on
// the server removes that whole failure class. Returns the number of files
// renamed.
//
// Skips inbox/review-needed/ (human-curated), index.md (generated directory
// pages), and any non-.md file. Renames are collision-safe: if the target slug
// already exists, a numeric suffix is appended rather than overwriting.
func normalizeInboxFilenames(vaultDir string, logger *slog.Logger) int {
	inbox := filepath.Join(vaultDir, "inbox")
	if _, err := os.Stat(inbox); err != nil {
		return 0
	}

	// Collect first, then rename — renaming during the walk could revisit or
	// skip entries depending on directory-read ordering.
	type rename struct{ absSrc, relSrc string }
	var pending []rename

	_ = filepath.WalkDir(inbox, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "review-needed" {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(p) != ".md" || d.Name() == "index.md" {
			return nil
		}
		rel, err := filepath.Rel(vaultDir, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if slug.IsNormalized(rel) {
			return nil
		}
		pending = append(pending, rename{absSrc: p, relSrc: rel})
		return nil
	})

	renamed := 0
	for _, r := range pending {
		target := uniqueInboxTarget(vaultDir, slug.NormalizePath(r.relSrc))
		absTarget := filepath.Join(vaultDir, filepath.FromSlash(target))
		if err := os.Rename(r.absSrc, absTarget); err != nil {
			logger.Warn("inbox normalize: rename failed", "from", r.relSrc, "to", target, "error", err)
			continue
		}
		logger.Info("inbox normalize: renamed", "from", r.relSrc, "to", target)
		renamed++
	}
	return renamed
}

// uniqueInboxTarget returns target, or target with a "-N" suffix before the
// extension if target already exists on disk (a genuine duplicate clip).
func uniqueInboxTarget(vaultDir, target string) string {
	if _, err := os.Stat(filepath.Join(vaultDir, filepath.FromSlash(target))); os.IsNotExist(err) {
		return target
	}
	ext := path.Ext(target)
	base := strings.TrimSuffix(target, ext)
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d%s", base, i, ext)
		if _, err := os.Stat(filepath.Join(vaultDir, filepath.FromSlash(cand))); os.IsNotExist(err) {
			return cand
		}
	}
}
