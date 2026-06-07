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
	"path"
	"strings"
	"time"

	"github.com/jedwards1230/my-wiki/internal/version"
)

// RawDirEntry is one entry in a /raw/ directory listing, passed by the server's
// raw handler to RenderRawIndex. Kept minimal — name + whether it's a folder is
// all the gallery needs (image-vs-file is derived from the extension).
type RawDirEntry struct {
	Name  string
	IsDir bool
}

// RenderRawIndex renders a /raw/ directory listing as a gallery page wrapped in
// the wiki chrome: image entries get lazy-loaded thumbnails, other files get an
// extension badge, folders get a folder icon. urlDir is the directory URL with
// a trailing slash (e.g. "/raw/clippings/", or "/raw/" for the root). Returns
// ok=false before the first build, so the handler falls back to the plain
// autoindex.
func (b *Builder) RenderRawIndex(urlDir string, entries []RawDirEntry) ([]byte, bool) {
	b.mu.Lock()
	r := b.lastRenderer
	explorer := b.lastExplorer
	b.mu.Unlock()
	if r == nil {
		return nil, false
	}

	p := &Page{
		Title:           "Index of " + strings.TrimSuffix(strings.TrimPrefix(urlDir, "/"), "/"),
		Description:     "Source files under " + urlDir,
		RelativeURL:     urlDir,
		BreadcrumbItems: rawBreadcrumb(urlDir),
		ContentHTML:     buildRawListing(urlDir, entries),
		// Slug stays empty — suppresses graph / backlinks / view-source.
	}
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

// buildRawListing renders a /raw/ directory as an autoindex: a file/folder
// list (the default everywhere), preceded by an image thumbnail grid ONLY when
// the directory actually contains images. No images → just the list. All names
// are HTML-escaped; URLs are built from the already-clean urlDir + name.
func buildRawListing(urlDir string, entries []RawDirEntry) template.HTML {
	// Split images out so they can lead with a thumbnail grid; folders and
	// non-image files stay in the list below.
	var images, rest []RawDirEntry
	for _, e := range entries {
		if !e.IsDir && isImageExtension(e.Name) {
			images = append(images, e)
		} else {
			rest = append(rest, e)
		}
	}

	var b strings.Builder

	// Image gallery section — conditional on images being present.
	if len(images) > 0 {
		b.WriteString(`<section class="raw-images"><ul class="raw-gallery">`)
		for _, e := range images {
			href := html.EscapeString(urlDir + e.Name)
			escName := html.EscapeString(e.Name)
			b.WriteString(`<li class="raw-tile raw-tile-img"><a href="` + href +
				`"><span class="raw-tile-thumb"><img loading="lazy" src="` + href + `" alt="` + escName +
				`"></span><span class="raw-tile-name">` + escName + `</span></a></li>`)
		}
		b.WriteString(`</ul></section>`)
	}

	// File/folder list — the autoindex proper.
	b.WriteString(`<ul class="raw-list">`)
	if urlDir != "/raw/" {
		parent := path.Dir(strings.TrimSuffix(urlDir, "/"))
		if !strings.HasSuffix(parent, "/") {
			parent += "/"
		}
		b.WriteString(`<li class="raw-row raw-row-dir"><a href="` + html.EscapeString(parent) +
			`"><span class="raw-row-icon" aria-hidden="true">&#8617;</span><span class="raw-row-name">..</span></a></li>`)
	}
	for _, e := range rest {
		escName := html.EscapeString(e.Name)
		if e.IsDir {
			href := html.EscapeString(urlDir + e.Name + "/")
			b.WriteString(`<li class="raw-row raw-row-dir"><a href="` + href +
				`"><span class="raw-row-icon" aria-hidden="true">&#128193;</span><span class="raw-row-name">` + escName + `/</span></a></li>`)
		} else {
			href := html.EscapeString(urlDir + e.Name)
			b.WriteString(`<li class="raw-row raw-row-file"><a href="` + href +
				`"><span class="raw-row-ext" aria-hidden="true">` + html.EscapeString(fileTypeLabel(e.Name)) +
				`</span><span class="raw-row-name">` + escName + `</span></a></li>`)
		}
	}
	b.WriteString(`</ul>`)
	return template.HTML(b.String()) //nolint:gosec // names escaped, URLs built from clean dir + name
}

// fileTypeLabel returns a short uppercase extension badge for a filename, e.g.
// "PDF", "MP4", or "FILE" when there's no extension.
func fileTypeLabel(name string) string {
	ext := fileExt(name)
	if ext == "" {
		return "FILE"
	}
	return strings.ToUpper(strings.TrimPrefix(ext, "."))
}

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
