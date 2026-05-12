// Package render is the native Go markdown renderer. It replaces Quartz's
// HTML output with goldmark + a small set of Obsidian-flavored extensions,
// and emits a complete site tree (per-page HTML, folder + tag listings,
// sitemap.xml, index.xml RSS, 404.html) into a memfs.Snapshot.
//
// The renderer is gated behind WIKI_RENDERER=native and lives alongside the
// Quartz pipeline. See docs/RENDERER.md for the operator runbook.
package render

import (
	"html/template"
	"strings"
	"time"
	"unicode/utf8"
)

// Page is the canonical model the templates render against. Field names
// match the goldmark-style conventions: all data the template needs is
// pre-computed here so templates contain no logic beyond ranges and ifs.
type Page struct {
	// Title is the rendered page title — frontmatter `title:` or the first
	// H1 in the body or the slug, in that order.
	Title string

	// Slug is the canonical URL path (forward slashes, no leading slash,
	// no extension). For `meta/schema.md` this is `meta/schema`.
	Slug string

	// RelativeURL is the URL the user navigates to: `/{Slug}/`. Always has
	// a leading slash and trailing slash for content pages so the breadcrumb
	// + canonical link templates can concatenate without surgery. The home
	// page (Slug=="index") is the one exception: its RelativeURL is "/" so
	// the canonical URL, sitemap, and wikilinks all agree with how the
	// server actually serves index.html.
	RelativeURL string

	// ContentHTML is the goldmark-rendered body. Templates emit it via
	// `{{ .ContentHTML }}` — already trusted; no second escape pass.
	ContentHTML template.HTML

	// TOC is the heading outline for the right-rail Table of Contents. Only
	// headings at depth 2-6 are included; H1 is the page title.
	TOC []TOCEntry

	// Backlinks are pages that wikilink to this one. Populated by the
	// post-render backlinks pass (see backlinks.go).
	Backlinks []BacklinkEntry

	// BreadcrumbItems is the navigation trail from root to this page. Each
	// item's URL is already absolute (leading slash); the last item is the
	// current page and templates set aria-current="page" on it.
	BreadcrumbItems []BreadcrumbItem

	// Tags are the frontmatter tags, normalized to lowercase, sorted.
	Tags []string

	// Created / Modified come from frontmatter `date:` (created) and file
	// mtime (modified). Either may be zero — templates check IsZero().
	Created  time.Time
	Modified time.Time

	// HasMath is set when the body contains $…$ or $$…$$. Templates use it
	// to gate the KaTeX <script> tag — no math, no load.
	HasMath bool

	// HasMermaid is set when the body contains a ```mermaid``` fence. Same
	// gating as HasMath for mermaid.min.js.
	HasMermaid bool

	// Description is either the frontmatter `description:` or the first
	// non-empty paragraph of body (truncated to ~200 chars). Used for
	// <meta name="description"> and RSS item summaries.
	Description string

	// Aliases is the frontmatter `aliases:` list. Currently informational
	// only — the renderer does not yet emit alias redirects (follow-up).
	Aliases []string

	// WordCount + ReadingTime are computed from ContentHTML's text content.
	WordCount   int
	ReadingTime time.Duration

	// IsListPage is true for folder + tag listing pages. Templates use it
	// to skip TOC/backlinks/word count chrome that doesn't apply.
	IsListPage bool

	// ListEntries is populated for IsListPage=true pages — the items shown
	// on a folder or tag listing.
	ListEntries []ListEntry
}

// TOCEntry is one heading in the right-rail table of contents.
type TOCEntry struct {
	Depth  int    // 2..6 (H1 omitted; that's the page title)
	Text   string // visible heading text (already HTML-escaped)
	Anchor string // fragment id (slugified heading text)
}

// BacklinkEntry is one inbound wikilink shown in the right-rail backlinks
// list. Snippet is the surrounding sentence; URL is the linking page's
// canonical URL.
type BacklinkEntry struct {
	Title   string
	URL     string
	Snippet string
}

// BreadcrumbItem is one segment in the breadcrumb nav at the top of every
// content page.
type BreadcrumbItem struct {
	Label string
	URL   string
	Last  bool // true for the trailing (current page) item
}

// ListEntry is one item on a folder or tag listing page.
type ListEntry struct {
	Title       string
	URL         string
	Description string
	Tags        []string
}

// BuildBreadcrumb returns the breadcrumb trail for a slug. The first item
// is always "Home" → "/"; subsequent items are derived from the slug's
// path segments.
func BuildBreadcrumb(slug string) []BreadcrumbItem {
	out := []BreadcrumbItem{{Label: "Home", URL: "/"}}
	if slug == "" || slug == "index" {
		out[len(out)-1].Last = true
		return out
	}
	parts := strings.Split(slug, "/")
	var prefix strings.Builder
	for i, part := range parts {
		prefix.WriteByte('/')
		prefix.WriteString(part)
		item := BreadcrumbItem{
			Label: humanizeSegment(part),
			URL:   prefix.String() + "/",
		}
		if i == len(parts)-1 {
			item.Last = true
		}
		out = append(out, item)
	}
	return out
}

// humanizeSegment turns "homelab-services" into "Homelab Services" for
// breadcrumbs and folder listings. It's intentionally simple: replace
// hyphens with spaces, uppercase the first letter of each word.
func humanizeSegment(s string) string {
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, "-", " ")
	s = strings.ReplaceAll(s, "_", " ")
	var b strings.Builder
	upper := true
	for _, r := range s {
		if r == ' ' {
			b.WriteRune(r)
			upper = true
			continue
		}
		if upper {
			b.WriteString(strings.ToUpper(string(r)))
			upper = false
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// computeWordCount counts whitespace-separated words in the HTML body
// after a coarse tag strip. Good enough for "X-minute read" — exact
// counts aren't load-bearing.
func computeWordCount(htmlBody string) int {
	var b strings.Builder
	b.Grow(len(htmlBody))
	inTag := false
	for _, r := range htmlBody {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
			b.WriteByte(' ')
		default:
			if !inTag {
				b.WriteRune(r)
			}
		}
	}
	words := 0
	inWord := false
	for _, r := range b.String() {
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			inWord = false
			continue
		}
		if !inWord {
			words++
			inWord = true
		}
	}
	return words
}

// computeReadingTime is a 200 WPM heuristic, rounded up to the next minute.
func computeReadingTime(words int) time.Duration {
	if words <= 0 {
		return 0
	}
	mins := (words + 199) / 200
	return time.Duration(mins) * time.Minute
}

// firstParagraph extracts the first paragraph of plain text from rendered
// HTML for the description fallback. Truncates to ~200 chars on a rune
// boundary and adds "…" if truncated.
func firstParagraph(htmlBody string) string {
	const maxLen = 200
	// Find first <p>...</p>. Goldmark always wraps body text in <p>.
	start := strings.Index(htmlBody, "<p>")
	if start < 0 {
		return ""
	}
	rest := htmlBody[start+3:]
	end := strings.Index(rest, "</p>")
	if end < 0 {
		end = len(rest)
	}
	para := rest[:end]
	// Strip inline tags coarsely.
	var b strings.Builder
	inTag := false
	for _, r := range para {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				b.WriteRune(r)
			}
		}
	}
	text := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(text) <= maxLen {
		return text
	}
	// Truncate to a rune boundary at or just past maxLen — adds "…" so
	// readers see the truncation.
	var seen int
	for i := range text {
		if seen >= maxLen {
			return strings.TrimRight(text[:i], " ,.;:") + "…"
		}
		seen++
	}
	return text
}
