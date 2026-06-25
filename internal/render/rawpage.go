package render

// rawpage.go renders raw/ directory listings as galleries and provides an
// on-demand markdown render used only as a fallback.
//
// raw/ MARKDOWN is now promoted to first-class wiki pages: it flows through
// FindWikiPages and is baked into the snapshot at its raw/ slug, so the /raw/
// handler serves the full compiled page (backlinks, TOC, graph, nav) by
// delegating to the static snapshot. RenderRawPage here is the pre-first-build /
// snapshot-miss fallback so a raw markdown URL never 404s spuriously. Agents and
// scripts still fetch the verbatim bytes (Accept: !text/html or ?raw=1).
//
// raw/ ASSETS (PDFs, images, audio, video, .canvas) remain non-renderable source
// files served as-is, with RenderRawIndex providing the directory gallery view.

import (
	"html"
	"html/template"
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
	explorer := cloneExplorerTree(b.lastExplorer)
	b.mu.Unlock()
	if r == nil {
		return nil, false
	}
	markActiveByURL(explorer, urlDir)

	content, toc := buildRawIndex(urlDir, entries)
	p := &Page{
		Title:           rawDirTitle(urlDir),
		Description:     "Source files under " + urlDir,
		RelativeURL:     urlDir,
		BreadcrumbItems: rawBreadcrumb(urlDir),
		ContentHTML:     content,
		TOC:             toc,
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

// rawDirTitle is the page title for a /raw/ directory index — the humanized
// last path segment, matching how wiki folder index pages are titled (e.g.
// "/raw/clippings/" → "Clippings", "/raw/" → "Raw").
func rawDirTitle(urlDir string) string {
	seg := strings.Trim(strings.TrimPrefix(urlDir, "/"), "/")
	if i := strings.LastIndexByte(seg, '/'); i >= 0 {
		seg = seg[i+1:]
	}
	return humanizeSegment(seg)
}

// isMediaFile reports whether a filename is renderable media (image, video,
// audio, or PDF) — the set the Gallery section surfaces.
func isMediaFile(name string) bool {
	return isImageExtension(name) || isVideoExtension(name) || isAudioExtension(name) || isPDFExtension(name)
}

// buildRawIndex renders a /raw/ directory the same way the wiki renders every
// other folder index: a "Directory" section listing the children as a bulleted
// list of internal links. It then adds a "Gallery" section — a thumbnail grid
// of any media detected in the directory — analogous to the Directory/Tags
// sections on generated folder indexes. Returns the body HTML plus TOC entries
// so the right-rail "On this page" lists the sections like any other page.
func buildRawIndex(urlDir string, entries []RawDirEntry) (template.HTML, []TOCEntry) {
	var media []RawDirEntry
	for _, e := range entries {
		if !e.IsDir && isMediaFile(e.Name) {
			media = append(media, e)
		}
	}

	var b strings.Builder
	var toc []TOCEntry

	// Directory section — every child, as a bulleted internal-link list, the
	// same markup generated folder indexes produce.
	b.WriteString(`<h2 id="directory">Directory</h2>`)
	toc = append(toc, TOCEntry{Depth: 2, Text: "Directory", Anchor: "directory"})
	if len(entries) == 0 {
		b.WriteString(`<p><em>This directory is empty.</em></p>`)
	} else {
		b.WriteString(`<ul>`)
		for _, e := range entries {
			escName := html.EscapeString(e.Name)
			if e.IsDir {
				href := html.EscapeString(urlDir + e.Name + "/")
				b.WriteString(`<li><a class="internal" href="` + href + `">` + escName + `/</a></li>`)
			} else {
				href := html.EscapeString(urlDir + e.Name)
				b.WriteString(`<li><a class="internal" href="` + href + `">` + escName + `</a></li>`)
			}
		}
		b.WriteString(`</ul>`)
	}

	// Gallery section — thumbnails of detected media. Only rendered when the
	// directory actually contains media.
	if len(media) > 0 {
		b.WriteString(`<h2 id="gallery">Gallery</h2>`)
		toc = append(toc, TOCEntry{Depth: 2, Text: "Gallery", Anchor: "gallery"})
		b.WriteString(`<ul class="raw-gallery">`)
		for _, e := range media {
			href := html.EscapeString(urlDir + e.Name)
			escName := html.EscapeString(e.Name)
			if isImageExtension(e.Name) {
				b.WriteString(`<li class="raw-tile"><a href="` + href +
					`"><span class="raw-tile-thumb"><img loading="lazy" src="` + href + `" alt="` + escName +
					`"></span><span class="raw-tile-name">` + escName + `</span></a></li>`)
			} else {
				b.WriteString(`<li class="raw-tile"><a href="` + href +
					`"><span class="raw-tile-thumb raw-tile-badge">` + html.EscapeString(fileTypeLabel(e.Name)) +
					`</span><span class="raw-tile-name">` + escName + `</span></a></li>`)
			}
		}
		b.WriteString(`</ul>`)
	}

	return template.HTML(b.String()), toc //nolint:gosec // names escaped, URLs built from clean dir + name
}

// markActiveByURL marks the explorer node whose URL exactly matches url as
// active and opens its ancestor folders. Used for raw pages, whose empty Slug
// can't be matched by the slug-based markActive. Returns true once matched.
func markActiveByURL(nodes []*ExplorerNode, url string) bool {
	for _, n := range nodes {
		if n.URL == url {
			n.IsActive = true
			n.IsOpen = n.IsFolder
			return true
		}
		if markActiveByURL(n.Children, url) {
			n.IsOpen = true
			return true
		}
	}
	return false
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
	explorer := cloneExplorerTree(b.lastExplorer)
	b.mu.Unlock()
	if r == nil {
		return nil, false
	}
	markActiveByURL(explorer, rawURL)

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
