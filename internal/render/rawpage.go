package render

// rawpage.go renders source documents under raw/ as first-class HTML pages.
// raw/ stays out of the build graph (no search index, no page directory, not
// a backlink node — see meta/schema "Raw Sources"), so these pages are
// rendered on demand by the /raw/ handler rather than baked into the snapshot.
// The point is presentation parity: a human clicking a raw markdown source
// gets the same chrome and rich-media embeds as any wiki page, while agents
// and scripts still fetch the verbatim bytes (Accept: !text/html or ?raw=1).

import (
	"html"
	"html/template"
	"strings"
	"time"

	"github.com/jedwards1230/my-wiki/internal/version"
)

// RenderRawPage renders a raw/ markdown source as a full HTML page. relPath is
// the vault-relative path (e.g. "raw/clippings/x.md"); rawURL is the canonical
// /raw/ URL it is served at. Returns ok=false when no renderer is available
// yet (pre-first-build) so the caller falls back to serving plain bytes.
//
// The page is deliberately framed as a source document, not a wiki page: it
// carries a banner, an empty Slug (which suppresses the graph, backlinks, and
// the default "View source" link in the template), and a canonical URL that
// points back at the /raw/ path rather than a wiki slug.
func (b *Builder) RenderRawPage(relPath string, source []byte, modTime time.Time, rawURL string) ([]byte, bool) {
	b.mu.Lock()
	r := b.lastRenderer
	explorer := b.lastExplorer
	b.mu.Unlock()
	if r == nil {
		return nil, false
	}

	p, err := r.RenderPage(relPath, source, modTime)
	if err != nil {
		return nil, false
	}

	// Reframe as a source document.
	p.ContentHTML = rawSourceBanner(rawURL, frontmatterScalar(source, "source")) + p.ContentHTML
	p.Slug = ""       // suppresses graph / backlinks / default view-source link
	p.Tags = nil      // raw docs aren't tag-graph members
	p.Backlinks = nil //
	p.RelativeURL = rawURL
	p.BreadcrumbItems = rawBreadcrumb(rawURL)

	td := TemplateData{
		Page:      p,
		SiteTitle: b.cfg.SiteTitle,
		Version:   version.Value,
		BaseURL:   b.cfg.BaseURL,
		Explorer:  explorer,
	}
	out, err := r.RenderToBytes(p, td)
	if err != nil {
		return nil, false
	}
	return out, true
}

// rawSourceBanner builds the "source document" notice prepended to the body.
// `source` is the optional `source:` frontmatter value (an origin URL).
func rawSourceBanner(rawURL, source string) template.HTML {
	var b strings.Builder
	b.WriteString(`<div class="raw-source-banner" role="note"><span class="raw-source-badge">Source</span><span class="raw-source-text">Verbatim source file in <code>raw/</code>, rendered for reading.</span><span class="raw-source-links">`)
	if source != "" && isHTTPURL(source) {
		b.WriteString(`<a href="`)
		b.WriteString(html.EscapeString(source))
		b.WriteString(`" rel="noopener noreferrer nofollow" target="_blank">Original&nbsp;&#8599;</a>`)
	}
	b.WriteString(`<a href="`)
	b.WriteString(html.EscapeString(rawURL))
	b.WriteString(`?raw=1" hx-boost="false">View raw</a></span></div>`)
	return template.HTML(b.String()) //nolint:gosec // inputs escaped above
}

// rawBreadcrumb builds a breadcrumb trail for a /raw/ URL: Home → raw → … →
// file. Each ancestor links to its /raw/ directory listing.
func rawBreadcrumb(rawURL string) []BreadcrumbItem {
	trimmed := strings.Trim(strings.TrimPrefix(rawURL, "/"), "/")
	parts := strings.Split(trimmed, "/")
	items := []BreadcrumbItem{{Label: "Home", URL: "/"}}
	cur := ""
	for i, seg := range parts {
		cur += "/" + seg
		last := i == len(parts)-1
		url := cur
		if !last {
			url += "/" // directory listing
		}
		items = append(items, BreadcrumbItem{Label: seg, URL: url, Last: last})
	}
	return items
}

// isHTTPURL reports whether s looks like an http(s) URL — guards the banner's
// "Original" link so a non-URL source value (e.g. a vault path) isn't linked.
func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// frontmatterScalar extracts a single top-level scalar value from a YAML
// frontmatter block by key. Returns "" when there's no frontmatter or the key
// is absent. Intentionally minimal — it only needs to read the flat string
// fields raw source docs use (`source`, `title`, `date-added`).
func frontmatterScalar(source []byte, key string) string {
	s := string(source)
	if !strings.HasPrefix(s, "---") {
		return ""
	}
	// Bound the scan to the frontmatter block.
	rest := s[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return ""
	}
	block := rest[:end]
	prefix := key + ":"
	for _, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		val := strings.TrimSpace(trimmed[len(prefix):])
		val = strings.Trim(val, `"'`)
		return val
	}
	return ""
}
