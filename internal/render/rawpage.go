package render

// rawpage.go renders raw/ directory listings as folder-index landing pages and
// provides an on-demand markdown render used only as a fallback.
//
// raw/ MARKDOWN is now promoted to first-class wiki pages: it flows through
// FindWikiPages and is baked into the snapshot at its raw/ slug, so the /raw/
// handler serves the full compiled page (backlinks, TOC, graph, nav) by
// delegating to the static snapshot. RenderRawPage here is the pre-first-build /
// snapshot-miss fallback so a raw markdown URL never 404s spuriously. Agents and
// scripts still fetch the verbatim bytes (Accept: !text/html or ?raw=1).
//
// raw/ DIRECTORIES (/raw/, /raw/<dir>/) render an "Index of …" landing page
// matching the normal folder-index look: a "Recently Updated" section of the
// most recently modified descendant pages, a "Directory" section listing the
// immediate children, and a "Gallery" section of any non-markdown media assets
// (PDFs, images, audio, video) directly in that directory. The landing is
// generated on-demand from the filesystem by RenderRawIndex — nothing is
// persisted into the vault (Generate never writes index.md into raw/).

import (
	"html"
	"html/template"
	"sort"
	"strings"
	"time"

	"github.com/jedwards1230/my-wiki/internal/version"
)

// RawDirEntry is one entry in a /raw/ directory listing, passed by the server's
// raw handler to RenderRawIndex. Name + IsDir drives classification; Title and
// ModTime are populated for markdown entries so the Directory and Recently
// Updated sections can show a real page title and order by mtime.
type RawDirEntry struct {
	// Name is the immediate child name (e.g. "clip.md", "youtube", "photo.png").
	Name string
	// IsDir reports whether the entry is a subdirectory.
	IsDir bool
	// Title is the resolved page title for a markdown entry (frontmatter
	// title, falling back to a humanized filename). Empty for non-markdown
	// entries and subdirectories.
	Title string
	// ModTime is the file mtime for a markdown entry, used for ordering the
	// Recently Updated section. Zero for entries that don't carry one.
	ModTime time.Time
}

// RawRecentEntry is one descendant markdown page surfaced in a raw landing's
// "Recently Updated" section. RawURL is the rendered page URL (e.g.
// "/raw/clippings/youtube/clip/"); Title is its display title; ModTime orders
// the list newest-first.
type RawRecentEntry struct {
	RawURL  string
	Title   string
	ModTime time.Time
}

// RawIndexData is the filesystem-derived input the raw handler hands to
// RenderRawIndex to render a /raw/ directory landing. Children are the immediate
// entries (for the Directory + Gallery sections); Recents are the most recently
// modified descendant markdown pages (for the Recently Updated section), already
// sorted newest-first and capped by the handler.
type RawIndexData struct {
	Children []RawDirEntry
	Recents  []RawRecentEntry
}

// isMarkdownName reports whether a filename is a markdown source (the set
// promoted to first-class wiki pages, linked to their rendered /raw/ URL).
func isMarkdownName(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), ".md")
}

// RenderRawIndex renders a /raw/ directory as a folder-index landing page
// wrapped in the wiki chrome — structurally consistent with the wiki's normal
// folder index pages (same "Index of …" description, #recently-updated and
// #directory sections, "On this page" TOC) — plus a Gallery section for any
// non-markdown media in the directory. urlDir is the directory URL with a
// trailing slash (e.g. "/raw/clippings/", or "/raw/" for the root). Returns
// ok=false before the first build, so the handler falls back to the plain
// autoindex.
func (b *Builder) RenderRawIndex(urlDir string, data RawIndexData) ([]byte, bool) {
	b.mu.Lock()
	r := b.lastRenderer
	explorer := cloneExplorerTree(b.lastExplorer)
	b.mu.Unlock()
	if r == nil {
		return nil, false
	}
	markActiveByURL(explorer, urlDir)

	content, toc := buildRawIndex(urlDir, data)
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
// audio, or PDF) — the set the Gallery section surfaces.
func isMediaFile(name string) bool {
	return isImageExtension(name) || isVideoExtension(name) || isAudioExtension(name) || isPDFExtension(name)
}

// buildRawIndex renders a /raw/ directory as a folder-index landing matching the
// wiki's normal generated folder indexes:
//
//   - "Recently Updated" (#recently-updated) — the most recently modified
//     descendant markdown pages, newest first. Mirrors the normal index's
//     recents: the handler caps the list at recentsLimit and applies the
//     non-root recentsMinPages gate, so this section only renders the slice it
//     was handed.
//   - "Directory" (#directory) — the immediate children as a bulleted
//     internal-link list (subdirs → /raw/<dir>/sub/, markdown → its rendered
//     /raw/.../ page using the resolved title, other assets → the raw URL).
//   - "Gallery" (#gallery) — a thumbnail grid of any non-markdown media (images,
//     PDFs, audio, video) directly in this directory, so assets stay visible.
//     Only rendered when such media exist.
//
// Returns the body HTML plus TOC entries so the right-rail "On this page" lists
// the sections present, like any other page.
func buildRawIndex(urlDir string, data RawIndexData) (template.HTML, []TOCEntry) {
	var media []RawDirEntry
	for _, e := range data.Children {
		if !e.IsDir && isMediaFile(e.Name) {
			media = append(media, e)
		}
	}

	var b strings.Builder
	var toc []TOCEntry

	// Recently Updated — most recently modified descendant pages, newest first.
	// The handler pre-sorts/caps/gates this list, so we render exactly what we
	// were given.
	if len(data.Recents) > 0 {
		b.WriteString(`<h2 id="recently-updated">Recently Updated</h2>`)
		toc = append(toc, TOCEntry{Depth: 2, Text: "Recently Updated", Anchor: "recently-updated"})
		b.WriteString(`<ul>`)
		for _, e := range data.Recents {
			href := html.EscapeString(e.RawURL)
			label := html.EscapeString(e.Title)
			if !e.ModTime.IsZero() {
				b.WriteString(`<li><a class="internal" href="` + href + `">` + label + `</a> — ` +
					html.EscapeString(e.ModTime.Format("2006-01-02")) + `</li>`)
			} else {
				b.WriteString(`<li><a class="internal" href="` + href + `">` + label + `</a></li>`)
			}
		}
		b.WriteString(`</ul>`)
	}

	// Directory section — every immediate child, as a bulleted internal-link
	// list, the same markup generated folder indexes produce.
	b.WriteString(`<h2 id="directory">Directory</h2>`)
	toc = append(toc, TOCEntry{Depth: 2, Text: "Directory", Anchor: "directory"})
	if len(data.Children) == 0 {
		b.WriteString(`<p><em>This directory is empty.</em></p>`)
	} else {
		b.WriteString(`<ul>`)
		for _, e := range data.Children {
			escName := html.EscapeString(e.Name)
			switch {
			case e.IsDir:
				href := html.EscapeString(urlDir + e.Name + "/")
				b.WriteString(`<li><a class="internal" href="` + href + `">` + escName + `/</a></li>`)
			case isMarkdownName(e.Name):
				// Markdown children are first-class pages — link to the rendered
				// /raw/.../ page (extension-less + trailing slash) using the
				// resolved page title, falling back to a humanized filename.
				slug := strings.TrimSuffix(e.Name, ".md")
				href := html.EscapeString(urlDir + slug + "/")
				label := e.Title
				if label == "" {
					label = humanizeSegment(slug)
				}
				b.WriteString(`<li><a class="internal" href="` + href + `">` + html.EscapeString(label) + `</a></li>`)
			default:
				href := html.EscapeString(urlDir + e.Name)
				b.WriteString(`<li><a class="internal" href="` + href + `">` + escName + `</a></li>`)
			}
		}
		b.WriteString(`</ul>`)
	}

	// Gallery section — thumbnails of detected media. Only rendered when the
	// directory actually contains non-markdown media assets.
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

	return template.HTML(b.String()), toc //nolint:gosec // names/titles/URLs escaped, dir built from clean path
}

// RawIndexTitle resolves a markdown source's display title: its frontmatter
// `title:` when present, otherwise a humanized filename (sans ".md"). Shared by
// the handler so Directory + Recently Updated entries read like real pages.
func RawIndexTitle(name string, source []byte) string {
	if t := frontmatterScalar(source, "title"); t != "" {
		return t
	}
	return humanizeSegment(strings.TrimSuffix(name, ".md"))
}

// rawIndexRecentsLimit caps how many descendant pages a raw landing's "Recently
// Updated" section lists, matching the normal folder index's recentsLimit.
const rawIndexRecentsLimit = 10

// rawIndexRecentsMinPages gates the "Recently Updated" section on non-root raw
// landings: a subtree this small or smaller just echoes its own directory
// listing, so the section is skipped there — matching the normal index's
// recentsMinPages.
const rawIndexRecentsMinPages = 8

// SelectRawRecents orders a /raw/ landing's descendant pages newest-first,
// applies the non-root recentsMinPages gate, and caps the result at
// recentsLimit — mirroring the normal folder index's "Recently Updated"
// semantics. isRoot is true only for the /raw/ root landing (which always shows
// recents, regardless of count). The input slice is not mutated.
func SelectRawRecents(recents []RawRecentEntry, isRoot bool) []RawRecentEntry {
	if !isRoot && len(recents) <= rawIndexRecentsMinPages {
		return nil
	}
	sorted := make([]RawRecentEntry, len(recents))
	copy(sorted, recents)
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].ModTime.Equal(sorted[j].ModTime) {
			return sorted[i].ModTime.After(sorted[j].ModTime)
		}
		return sorted[i].RawURL < sorted[j].RawURL
	})
	if len(sorted) > rawIndexRecentsLimit {
		sorted = sorted[:rawIndexRecentsLimit]
	}
	return sorted
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
