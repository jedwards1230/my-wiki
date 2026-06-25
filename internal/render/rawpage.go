package render

// rawpage.go renders raw/ content fallbacks. raw/ is a normal indexed folder:
// its markdown is promoted to first-class wiki pages and its directories get a
// standard generated index.md landing baked into the snapshot. The functions
// here are only fallbacks the /raw/ handler reaches for when the snapshot has no
// baked page:
//
//   - RenderRawPage renders a markdown leaf on-demand before the first build, so
//     a raw markdown URL never 404s spuriously. Agents and scripts still fetch
//     the verbatim bytes (Accept: !text/html or ?raw=1).
//   - RenderRawGallery renders an asset-only directory (no markdown, so no
//     meaningful generated index page) as a media gallery, keeping PDFs, images,
//     audio, and video visible. This is the original pre-folder-index behavior.

import (
	"html"
	"html/template"
	"strings"
	"time"

	"github.com/jedwards1230/my-wiki/internal/version"
)

// RawAsset is one non-markdown asset surfaced in a /raw/ directory gallery,
// passed by the server's raw handler to RenderRawGallery. Name is the immediate
// child filename (e.g. "photo.png", "talk.pdf").
type RawAsset struct {
	Name string
}

// RenderRawGallery renders a /raw/ directory as a media gallery landing wrapped
// in the wiki chrome — the fallback for an asset-only directory (no markdown, so
// the standard generated index.md has no meaningful page) or the pre-first-build
// window. urlDir is the directory URL with a trailing slash (e.g.
// "/raw/clippings/", or "/raw/" for the root); assets are the non-markdown
// children to surface. Returns ok=false before the first build, so the handler
// falls back to the plain autoindex.
func (b *Builder) RenderRawGallery(urlDir string, assets []RawAsset) ([]byte, bool) {
	b.mu.Lock()
	r := b.lastRenderer
	explorer := cloneExplorerTree(b.lastExplorer)
	b.mu.Unlock()
	if r == nil {
		return nil, false
	}
	markActiveByURL(explorer, urlDir)

	content, toc := buildRawGallery(urlDir, assets)
	p := &Page{
		Title:                      rawDirTitle(urlDir),
		Description:                rawIndexDescription(urlDir),
		DescriptionFromFrontmatter: true, // render the visible "Index of …" block, like normal folder indexes
		RelativeURL:                urlDir,
		BreadcrumbItems:            rawBreadcrumb(urlDir),
		ContentHTML:                content,
		TOC:                        toc,
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

// rawDirTitle is the page title for a /raw/ directory landing — the humanized
// last path segment, matching how wiki folder index pages are titled (e.g.
// "/raw/clippings/" → "Clippings", "/raw/" → "Raw").
func rawDirTitle(urlDir string) string {
	seg := strings.Trim(strings.TrimPrefix(urlDir, "/"), "/")
	if i := strings.LastIndexByte(seg, '/'); i >= 0 {
		seg = seg[i+1:]
	}
	return humanizeSegment(seg)
}

// rawIndexDescription is the article-description for a /raw/ directory landing,
// mirroring the normal folder index's "Index of <rel>" line (where <rel> is the
// vault-relative directory path, e.g. "raw/clippings"). The root /raw/ reads
// "Index of raw".
func rawIndexDescription(urlDir string) string {
	rel := strings.Trim(strings.TrimPrefix(urlDir, "/"), "/")
	if rel == "" {
		rel = "raw"
	}
	return "Index of " + rel
}

// isMediaFile reports whether a filename is renderable media (image, video,
// audio, or PDF) — the set the gallery surfaces with a thumbnail/badge tile.
func isMediaFile(name string) bool {
	return isImageExtension(name) || isVideoExtension(name) || isAudioExtension(name) || isPDFExtension(name)
}

// buildRawGallery renders a /raw/ directory's non-markdown assets as a gallery:
//
//   - "Gallery" (#gallery) — a thumbnail grid of recognized media (images, PDFs,
//     audio, video) with image thumbnails and a type badge for the rest.
//   - "Files" (#files) — any remaining non-media assets as a plain link list.
//
// Returns the body HTML plus TOC entries so the right-rail "On this page" lists
// the sections present. When the directory has no assets at all the body is an
// "empty" note (the handler reaches the gallery fallback only when there is no
// baked index page, so this stays informative rather than blank).
func buildRawGallery(urlDir string, assets []RawAsset) (template.HTML, []TOCEntry) {
	var media, files []RawAsset
	for _, a := range assets {
		if isMediaFile(a.Name) {
			media = append(media, a)
		} else {
			files = append(files, a)
		}
	}

	var b strings.Builder
	var toc []TOCEntry

	if len(media) == 0 && len(files) == 0 {
		b.WriteString(`<p><em>This directory is empty.</em></p>`)
		return template.HTML(b.String()), toc //nolint:gosec // static literal
	}

	if len(media) > 0 {
		b.WriteString(`<h2 id="gallery">Gallery</h2>`)
		toc = append(toc, TOCEntry{Depth: 2, Text: "Gallery", Anchor: "gallery"})
		b.WriteString(`<ul class="raw-gallery">`)
		for _, a := range media {
			href := html.EscapeString(urlDir + a.Name)
			escName := html.EscapeString(a.Name)
			if isImageExtension(a.Name) {
				b.WriteString(`<li class="raw-tile"><a href="` + href +
					`"><span class="raw-tile-thumb"><img loading="lazy" src="` + href + `" alt="` + escName +
					`"></span><span class="raw-tile-name">` + escName + `</span></a></li>`)
			} else {
				b.WriteString(`<li class="raw-tile"><a href="` + href +
					`"><span class="raw-tile-thumb raw-tile-badge">` + html.EscapeString(fileTypeLabel(a.Name)) +
					`</span><span class="raw-tile-name">` + escName + `</span></a></li>`)
			}
		}
		b.WriteString(`</ul>`)
	}

	if len(files) > 0 {
		b.WriteString(`<h2 id="files">Files</h2>`)
		toc = append(toc, TOCEntry{Depth: 2, Text: "Files", Anchor: "files"})
		b.WriteString(`<ul>`)
		for _, a := range files {
			href := html.EscapeString(urlDir + a.Name)
			escName := html.EscapeString(a.Name)
			b.WriteString(`<li><a class="internal" href="` + href + `">` + escName + `</a></li>`)
		}
		b.WriteString(`</ul>`)
	}

	return template.HTML(b.String()), toc //nolint:gosec // names/URLs escaped, dir built from clean path
}

// markActiveByURL marks the explorer node whose URL exactly matches url as
// active and opens its ancestor folders. Used for raw landings/pages, whose
// empty Slug can't be matched by the slug-based markActive. Returns true once
// matched.
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
