package render

import (
	"strings"
	"sync/atomic"
)

// BacklinkIndex is the read-mostly reverse index: for a given target slug,
// which pages link to it. Stored behind an atomic.Pointer so the builder
// can swap a fresh map at the end of a rebuild without blocking readers.
//
// Lifetime: built once during Builder.Build, swapped via Replace, read by
// the per-page Backlinks pass and by the /api/backlinks endpoint.
type BacklinkIndex struct {
	m atomic.Pointer[map[string][]BacklinkEntry]
}

// NewBacklinkIndex returns an index containing an empty map.
func NewBacklinkIndex() *BacklinkIndex {
	idx := &BacklinkIndex{}
	empty := map[string][]BacklinkEntry{}
	idx.m.Store(&empty)
	return idx
}

// Replace atomically publishes the new backlink map.
func (b *BacklinkIndex) Replace(m map[string][]BacklinkEntry) {
	b.m.Store(&m)
}

// Lookup returns the backlinks for a target slug. Returns a nil slice when
// the target has no inbound links (which is the common case for new pages).
func (b *BacklinkIndex) Lookup(slug string) []BacklinkEntry {
	m := b.m.Load()
	if m == nil {
		return nil
	}
	return (*m)[strings.ToLower(slug)]
}

// BuildBacklinks walks rendered pages and builds the reverse-index map.
// The slugs argument is the same canonical slug → relative-path table the
// renderer uses; it lets the indexer ignore wikilinks whose targets don't
// resolve to a real page.
//
// Each backlink entry stores the source page's title and canonical URL.
func BuildBacklinks(pages []*Page, allLinks map[string][]string, slugs map[string]string) map[string][]BacklinkEntry {
	out := make(map[string][]BacklinkEntry)
	for _, src := range pages {
		links := allLinks[src.Slug]
		seen := make(map[string]struct{}, len(links))
		for _, target := range links {
			key := strings.ToLower(target)
			canonical, ok := slugs[key]
			if !ok {
				continue
			}
			if _, dup := seen[canonical]; dup {
				continue
			}
			seen[canonical] = struct{}{}
			out[canonical] = append(out[canonical], BacklinkEntry{
				Title: src.Title,
				URL:   src.RelativeURL,
			})
		}
	}
	return out
}
