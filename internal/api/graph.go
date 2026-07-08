package api

import (
	"net/http"
	"path/filepath"
	"strings"

	"github.com/jedwards1230/my-wiki/internal/vault"
)

// graphResponse is the JSON shape consumed by the right-rail graph
// widget in wiki.js. Nodes and links use slug strings as identifiers;
// the canonical slug (no leading slash, no trailing /index) is also the
// page's permalink minus surrounding slashes.
type graphResponse struct {
	Nodes []graphNode `json:"nodes"`
	Links []graphLink `json:"links"`
}

type graphNode struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url"`
}

type graphLink struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

// handleGraph emits {nodes, links} computed from vault pages +
// wikilinks. Used by the right-rail graph widget. Resolves wikilink
// targets through the slug index so aliased and folder-index forms
// land on the same node as the page they refer to.
func (h *Handler) handleGraph(w http.ResponseWriter, _ *http.Request) {
	if h.vault == nil {
		writeError(w, http.StatusNotImplemented, "vault is not configured")
		return
	}

	pages, err := h.vault.FindWikiPages()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list pages: "+err.Error())
		return
	}
	slugs, err := h.vault.BuildSlugIndex()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "build slug index: "+err.Error())
		return
	}

	// nodes — one per page that ended up in the slug index. The slug
	// index already collapses folder-index pages and lowercases keys,
	// so deduping by canonical slug is just a map.
	type nodeInfo struct {
		title string
	}
	known := make(map[string]nodeInfo)
	for _, canonical := range slugs {
		// `slugs` is keyed by aliases AND by canonical paths; iterating
		// over values gives canonical paths possibly with duplicates,
		// which the map collapses for us.
		if _, ok := known[canonical]; ok {
			continue
		}
		known[canonical] = nodeInfo{}
	}

	// resolve page → canonical slug + try to find a title via
	// frontmatter (cheap; only the first few lines are read).
	pageCanonical := make(map[string]string, len(pages))
	for _, p := range pages {
		rel := h.relSlug(p)
		canonical := slugs[strings.ToLower(rel)]
		if canonical == "" {
			canonical = rel // page not in slug index — skip linking, but keep node
		}
		pageCanonical[p] = canonical
		info := known[canonical]
		if info.title == "" {
			info.title = titleFor(h.vault, p)
			known[canonical] = info
		}
	}

	// links — for each page, extract wikilinks and resolve targets.
	var links []graphLink
	for _, p := range pages {
		src := pageCanonical[p]
		if src == "" {
			continue
		}
		wls, err := h.vault.ExtractWikilinks(p)
		if err != nil {
			continue // skip unreadable pages, don't fail the request
		}
		for _, target := range wls {
			tgt := slugs[strings.ToLower(target)]
			if tgt == "" || tgt == src {
				continue
			}
			links = append(links, graphLink{Source: src, Target: tgt})
		}
	}

	// Stable output ordering would be nice but isn't required — the
	// client doesn't depend on it. Skipping a sort keeps this handler
	// inexpensive on large vaults.
	resp := graphResponse{
		Nodes: make([]graphNode, 0, len(known)),
		Links: links,
	}
	for slug, info := range known {
		resp.Nodes = append(resp.Nodes, graphNode{
			ID:    slug,
			Title: info.title,
			URL:   urlFromSlug(slug),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// relSlug returns a page's slug-style path from a storage-relative page path:
// forward slashes, no `.md` extension, lowercased.
func (h *Handler) relSlug(rel string) string {
	rel = filepath.ToSlash(rel)
	rel = strings.TrimSuffix(rel, ".md")
	return strings.ToLower(rel)
}

// titleFor pulls the title from frontmatter; falls back to the slug
// basename. rel is a storage-relative page path read through the vault.
func titleFor(v *vault.Vault, rel string) string {
	fm, err := v.ParseFrontmatter(rel)
	if err == nil {
		if t, ok := fm["title"]; ok && t != "" {
			return t
		}
	}
	base := filepath.Base(rel)
	return strings.TrimSuffix(base, ".md")
}

// urlFromSlug mirrors render.RenderPage's URL logic so graph node URLs
// match canonical wiki URLs (folder-index → `/dir/`, root → `/`).
func urlFromSlug(slug string) string {
	switch {
	case slug == "index":
		return "/"
	case strings.HasSuffix(slug, "/index"):
		return "/" + strings.TrimSuffix(slug, "/index") + "/"
	default:
		return "/" + slug + "/"
	}
}
