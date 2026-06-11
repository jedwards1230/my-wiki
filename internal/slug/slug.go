// Package slug derives deterministic, filesystem-safe page slugs from
// arbitrary titles or paths. It exists so the server — not a caller (human or
// agent) — owns the final on-disk filename for every page CRUD operation.
//
// Callers that round-trip a filename through a lossy channel (an LLM agent
// retyping a path it read from `list`, a clipper naming a file after a page
// title) routinely mangle smart punctuation (curly apostrophes), collapse
// double spaces, or change case. When the mangled path no longer matches the
// byte sequence on disk, reads/moves/deletes silently 404. Normalizing every
// filename to a stable ASCII slug removes that whole failure class: there is
// one canonical name, and it survives a round-trip unchanged.
package slug

import (
	"path"
	"strings"
)

// maxLen caps a generated slug so titles that are whole sentences don't
// produce absurd filenames. Truncation lands on a hyphen boundary.
const maxLen = 80

// Make converts an arbitrary string into a filesystem-safe slug:
//   - lower-cased ASCII alphanumerics are kept verbatim
//   - apostrophes and quotation marks (straight and curly) are dropped so
//     "AI's" becomes "ais" rather than "ai-s"
//   - every other run of non-alphanumeric characters collapses to a single "-"
//   - leading/trailing hyphens are trimmed and the result is length-capped
//
// An input that slugifies to nothing (all punctuation) returns "untitled" so
// callers always get a usable, addressable name. Make is idempotent:
// Make(Make(s)) == Make(s).
func Make(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))

	var b strings.Builder
	prevHyphen := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevHyphen = false
		case isApostrophe(r):
			// Drop: join the surrounding letters instead of splitting them.
		default:
			// Any other rune (space, hyphen, punctuation, unicode) is a
			// separator. Collapse consecutive separators into one hyphen and
			// never emit a leading hyphen.
			if !prevHyphen && b.Len() > 0 {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}

	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "untitled"
	}
	if len(out) > maxLen {
		out = out[:maxLen]
		if i := strings.LastIndexByte(out, '-'); i > 0 {
			out = out[:i]
		}
		out = strings.Trim(out, "-")
		if out == "" {
			return "untitled"
		}
	}
	return out
}

// isApostrophe reports whether r is an apostrophe or quotation mark that
// should be elided (rather than treated as a word separator).
func isApostrophe(r rune) bool {
	switch r {
	case '\'', '`', '"',
		'‘', '’', // ' '  curly single quotes
		'“', '”': // " "  curly double quotes
		return true
	default:
		return false
	}
}

// NormalizePath returns relPath with only its final filename segment slugified;
// directory segments and the file extension are preserved. The path is
// returned with forward slashes.
//
// Examples:
//
//	"research/clippings/Thinking Machines’ Murati.md" -> "research/clippings/thinking-machines-murati.md"
//	"home/homelab/services/grafana.md"                -> "home/homelab/services/grafana.md" (idempotent)
//	"2026-06-11.md"                                    -> "2026-06-11.md"
//
// NormalizePath is idempotent for already-conformant paths, so applying it to
// every write/move is a no-op on the existing vault and only rewrites the
// unsafe names it is meant to fix.
func NormalizePath(relPath string) string {
	relPath = path.Clean(strings.ReplaceAll(relPath, "\\", "/"))
	if relPath == "." || relPath == "/" {
		return relPath
	}

	dir, base := path.Split(relPath)
	ext := path.Ext(base)
	name := strings.TrimSuffix(base, ext)

	out := Make(name) + strings.ToLower(ext)
	if dir != "" {
		out = strings.TrimSuffix(dir, "/") + "/" + out
	}
	return out
}

// IsNormalized reports whether the final filename segment of relPath is already
// its canonical slug — i.e. NormalizePath would not change it. Inbox
// normalization uses this to skip files that need no rename.
func IsNormalized(relPath string) bool {
	return NormalizePath(relPath) == path.Clean(strings.ReplaceAll(relPath, "\\", "/"))
}
