package api

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
)

// RenderBacklinks exposes the per-slug backlinks list as a slug-keyed
// lookup, satisfied by *render.Builder. Surfaces without a renderer (e.g.
// standalone MCP) leave it nil and the endpoints return 404.
type RenderBacklinks interface {
	Lookup(slug string) []RenderBacklinkEntry
}

// RenderBacklinkEntry mirrors render.BacklinkEntry. Duplicated here to
// avoid importing internal/render from internal/api (the layering
// convention is api → service → vault; importing render would invert it).
type RenderBacklinkEntry struct {
	Title string
	URL   string
}

// RenderPage is the minimal contract the popover endpoint needs from the
// renderer's cached page map. Same layering rationale as RenderBacklinks.
type RenderPage interface {
	PageBySlug(slug string) (title, description string, ok bool)
}

// WithRenderEndpoints wires /api/popover/{slug...} and /api/backlinks
// to the renderer-provided lookups. Both return short HTML fragments
// (matching htmx's swap semantics) — content-type is text/html.
func WithRenderEndpoints(pages RenderPage, backlinks RenderBacklinks) HandlerOption {
	return func(h *Handler) {
		h.renderPages = pages
		h.renderBacklinks = backlinks
	}
}

// The renderer fragment routes (popover + backlinks) are registered from the
// central apiRoutes table in routes.go, guarded on renderPages / renderBacklinks
// being wired by WithRenderEndpoints (native-renderer mode only).

func (h *Handler) handlePopover(w http.ResponseWriter, r *http.Request) {
	slug := strings.Trim(r.PathValue("slug"), "/")
	title, desc, ok := h.renderPages.PageBySlug(slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, fmt.Sprintf(
		`<div class="popover"><strong>%s</strong>%s</div>`,
		html.EscapeString(title),
		descriptionFragment(desc),
	))
}

func descriptionFragment(desc string) string {
	if desc == "" {
		return ""
	}
	return `<p>` + html.EscapeString(desc) + `</p>`
}

func (h *Handler) handleBacklinks(w http.ResponseWriter, r *http.Request) {
	slug := strings.Trim(r.URL.Query().Get("slug"), "/")
	if slug == "" {
		http.Error(w, "slug required", http.StatusBadRequest)
		return
	}
	entries := h.renderBacklinks.Lookup(slug)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if len(entries) == 0 {
		_, _ = io.WriteString(w, `<p class="empty">No backlinks yet.</p>`)
		return
	}
	var b strings.Builder
	b.WriteString(`<ul class="backlinks-list">`)
	for _, e := range entries {
		b.WriteString(`<li><a href="`)
		b.WriteString(html.EscapeString(e.URL))
		b.WriteString(`">`)
		b.WriteString(html.EscapeString(e.Title))
		b.WriteString(`</a></li>`)
	}
	b.WriteString(`</ul>`)
	_, _ = io.WriteString(w, b.String())
}
