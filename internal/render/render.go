package render

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	emoji "github.com/yuin/goldmark-emoji"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	meta "github.com/yuin/goldmark-meta"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"go.abhg.dev/goldmark/mermaid"
)

// Renderer is the compiled goldmark instance + parsed templates. One per
// process — Build() calls share the renderer across goroutines.
//
// The renderer holds three goldmark instances:
//   - parseMD: used for the parse-only pass that populates the AST cache
//     (one shared instance, called concurrently).
//   - md: the default render-pipeline instance for callers that don't
//     supply per-page transclusion state (e.g. unit tests via renderMD).
//     This instance is also what Renderer.RenderPage uses when no
//     TranscludeCache is configured.
//   - per-page: when transclusion is wired, Builder.Build builds a fresh
//     goldmark per render via newMarkdown so the transcludeRenderer's
//     visited set and current-slug are page-scoped.
type Renderer struct {
	parseMD   goldmark.Markdown
	md        goldmark.Markdown
	templates *template.Template
	slugs     map[string]string

	// transcludeCache + slugTitles are populated when the Renderer is
	// being driven by a Builder that pre-parsed the vault. nil when
	// transclusion isn't available (unit tests, single-doc renders).
	transcludeCache map[string]*ParsedPage
	slugTitles      map[string]string
}

// NewRenderer compiles a Renderer with the wikilink slug index. Returns
// an error only if template parsing fails — goldmark wiring is straightline.
//
// Callers in long-running mode should call NewRenderer once and reuse it
// across Build()s; the slug map is rebuilt and the renderer recreated only
// when wikilink targets change.
func NewRenderer(slugs map[string]string) (*Renderer, error) {
	parseMD := newMarkdown(slugs, nil)
	defaultMD := newMarkdown(slugs, nil)

	tmpl, err := loadTemplates()
	if err != nil {
		return nil, fmt.Errorf("load templates: %w", err)
	}
	return &Renderer{
		parseMD:   parseMD,
		md:        defaultMD,
		templates: tmpl,
		slugs:     slugs,
	}, nil
}

// WithTransclusion attaches a parsed-page cache and slug → title map so
// subsequent RenderPage calls can resolve full transclusions. Called by
// Builder.Build between pass 1 (parse) and pass 2 (render).
func (r *Renderer) WithTransclusion(cache map[string]*ParsedPage, titles map[string]string) {
	r.transcludeCache = cache
	r.slugTitles = titles
}

// newMarkdown returns a goldmark.Markdown configured with every extension
// the renderer needs. If transcludes is non-nil, the wikilink renderer
// is the transcluding variant; otherwise it falls back to plain wikilink
// rendering with no transclusion behavior.
//
// The transcludeRenderer's MD pointer is set to the returned instance
// after construction so it can render subtrees of cached ASTs back to
// HTML using the same configuration that produced them.
func newMarkdown(slugs map[string]string, transcludes *TranscludeSource) goldmark.Markdown {
	md := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			meta.New(meta.WithStoresInDocument()),
			extension.Footnote,
			extension.DefinitionList,
			// NOTE: extension.Typographer is intentionally NOT enabled
			// because its `-` trigger splits inline text segments, which
			// breaks blockRefTransformer's ^block-id detection (the
			// trailing `-foo` segment loses the `^` prefix to a
			// preceding sibling). The visual loss (em-dashes etc) is
			// minor; block refs are load-bearing for transclusion.
			emoji.Emoji,
			highlighting.NewHighlighting(
				highlighting.WithStyle("github"),
			),
			&obsidianExtension{},
			newWikilinkExtender(slugs, transcludes),
			// Mermaid: client-side passthrough — ```mermaid``` blocks are
			// emitted as <pre class="mermaid"> for mermaid.min.js to pick
			// up at runtime. NoScript=true keeps the extension from
			// injecting its own <script> tag; wiki.js detects pre.mermaid
			// in the DOM and lazy-loads mermaid.min.js on both the initial
			// paint and after every htmx swap (Page.HasMermaid is no longer
			// gating script delivery — see base.html.tmpl).
			&mermaid.Extender{RenderMode: mermaid.RenderModeClient, NoScript: true},
		),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
			parser.WithAttribute(),
		),
		goldmark.WithRendererOptions(
			html.WithUnsafe(),
		),
	)
	if transcludes != nil {
		transcludes.MD = md
	}
	return md
}

// loadTemplates parses the embedded template tree into a single template
// set so {{ template "x" }} cross-references resolve. Used by both the
// builder (full-page render) and the server (HX-Request fragment re-exec).
func loadTemplates() (*template.Template, error) {
	root := template.New("base").Funcs(templateFuncs())
	entries, err := fs.ReadDir(embeddedTemplates, "templates")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		data, err := fs.ReadFile(embeddedTemplates, "templates/"+name)
		if err != nil {
			return nil, err
		}
		if _, err := root.New(name).Parse(string(data)); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
	}
	return root, nil
}

// templateFuncs are the helpers available inside templates.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"formatDate": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.Format("2006-01-02")
		},
		"formatDateLong": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.Format("January 2, 2006")
		},
		"readingTime": func(d time.Duration) string {
			if d == 0 {
				return ""
			}
			mins := int(d / time.Minute)
			if mins <= 1 {
				return "1 min read"
			}
			return fmt.Sprintf("%d min read", mins)
		},
		"isMD": func(s string) bool { return strings.HasSuffix(s, ".md") },
		// renderExplorer recursively renders the explorer tree to HTML.
		// Go templates cannot call themselves recursively, so the rendering
		// is done in Go and returned as template.HTML.
		"renderExplorer": func(nodes []*ExplorerNode) template.HTML {
			var buf strings.Builder
			renderExplorerNodes(&buf, nodes)
			return template.HTML(buf.String())
		},
	}
}

// renderExplorerNodes recursively writes the explorer tree HTML into buf.
// Each folder becomes a <details>/<summary> pair; leaves are plain <a> tags.
func renderExplorerNodes(buf *strings.Builder, nodes []*ExplorerNode) {
	if len(nodes) == 0 {
		return
	}
	buf.WriteString(`<ul class="explorer-ul">`)
	for _, n := range nodes {
		if n.IsFolder {
			buf.WriteString(`<li class="explorer-folder">`)
			openAttr := ""
			if n.IsOpen {
				openAttr = " open"
			}
			buf.WriteString(`<details` + openAttr + `>`)
			buf.WriteString(`<summary class="explorer-folder-title`)
			if n.IsActive {
				buf.WriteString(` is-active`)
			}
			buf.WriteString(`">`)
			buf.WriteString(`<svg class="folder-icon" xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="5 8 14 8" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polyline points="6 9 12 15 18 9"></polyline></svg>`)
			if n.URL != "" {
				buf.WriteString(`<a href="` + template.HTMLEscapeString(n.URL) + `" class="folder-link">`)
				buf.WriteString(template.HTMLEscapeString(n.Name))
				buf.WriteString(`</a>`)
			} else {
				buf.WriteString(`<span class="folder-name">`)
				buf.WriteString(template.HTMLEscapeString(n.Name))
				buf.WriteString(`</span>`)
			}
			buf.WriteString(`</summary>`)
			renderExplorerNodes(buf, n.Children)
			buf.WriteString(`</details>`)
			buf.WriteString(`</li>`)
		} else {
			buf.WriteString(`<li class="explorer-file">`)
			classes := "explorer-link"
			if n.IsActive {
				classes += " is-active"
			}
			buf.WriteString(`<a href="` + template.HTMLEscapeString(n.URL) + `" class="` + classes + `"`)
			if n.IsActive {
				buf.WriteString(` aria-current="page"`)
			}
			buf.WriteString(`>`)
			buf.WriteString(template.HTMLEscapeString(n.Name))
			buf.WriteString(`</a>`)
			buf.WriteString(`</li>`)
		}
	}
	buf.WriteString(`</ul>`)
}

// TemplateData is the top-level data passed to templates. Embedding *Page
// gives templates direct access to page fields plus surrounding context
// (site title, active path for navigation highlighting).
type TemplateData struct {
	*Page
	SiteTitle  string
	ActivePath string // for explorer active-state — same as Page.RelativeURL
	BuildDate  string
	Version    string
	// BaseURL is the canonical site origin (no trailing slash), e.g.
	// "https://wiki.lilbro.cloud". Empty when the deployment doesn't
	// publish a public origin; templates must tolerate that.
	BaseURL string
	// Explorer is the site's folder tree for the left-sidebar. Built once
	// per Build() and carried through TemplateData so partial templates can
	// render it without a separate data fetch.
	Explorer []*ExplorerNode
}

// ParsePage parses a page's source to AST without rendering. Used by
// Builder.Build's pass 1 to populate the transclusion cache.
//
// Pulled out from RenderPage because transclusion needs every page's AST
// before any page is rendered — a page can transclude another page that
// hasn't been visited yet in the render pass.
func (r *Renderer) ParsePage(path string, source []byte) (*ParsedPage, parser.Context, ast.Node) {
	source = escapePipesInsideWikilinks(source)
	ctx := parser.NewContext()
	reader := text.NewReader(source)
	doc := r.parseMD.Parser().Parse(reader, parser.WithContext(ctx))

	slug := slugFromPath(path)
	title := ""
	if metaMap := meta.Get(ctx); metaMap != nil {
		if v, ok := metaMap["title"].(string); ok {
			title = v
		}
	}
	if title == "" {
		title = firstHeadingText(doc, source)
	}
	if title == "" {
		title = humanizeSegment(filepath.Base(slug))
	}
	return &ParsedPage{
		Slug:   slug,
		Title:  title,
		Source: source,
		Doc:    doc,
	}, ctx, doc
}

// RenderPage runs goldmark over the page's raw markdown bytes, populates
// every metadata field on *Page, and stores the rendered HTML.
//
// `path` is the relative path inside the vault (e.g. `meta/schema.md`).
// `source` is the raw file content. `modTime` is the file's mtime —
// surfaces in the "Modified" footer.
//
// When the Renderer has a transclude cache attached (via WithTransclusion)
// each RenderPage call constructs a per-page goldmark with a fresh
// TranscludeSource so the visited-set + depth are page-scoped. Without
// the cache, the shared default goldmark is used.
func (r *Renderer) RenderPage(path string, source []byte, modTime time.Time) (*Page, error) {
	source = escapePipesInsideWikilinks(source)
	p := &Page{Modified: modTime}

	slug := slugFromPath(path)

	var (
		md  goldmark.Markdown
		ctx parser.Context
		doc ast.Node
	)
	if r.transcludeCache != nil {
		// Per-page goldmark so the transcludeRenderer's visited set is
		// scoped to this render. Seed Visited with the current slug so a
		// page can't ![[transclude itself]].
		ts := &TranscludeSource{
			Cache:       r.transcludeCache,
			SlugTitles:  r.slugTitles,
			CurrentSlug: slug,
			Visited:     map[string]struct{}{strings.ToLower(slug): {}},
			Depth:       0,
		}
		md = newMarkdown(r.slugs, ts)
		ctx = parser.NewContext()
		doc = md.Parser().Parse(text.NewReader(source), parser.WithContext(ctx))
	} else {
		md = r.md
		ctx = parser.NewContext()
		doc = md.Parser().Parse(text.NewReader(source), parser.WithContext(ctx))
	}

	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, source, doc); err != nil {
		return nil, fmt.Errorf("render markdown: %w", err)
	}

	// Pull metadata from frontmatter.
	if metaMap := meta.Get(ctx); metaMap != nil {
		if v, ok := metaMap["title"].(string); ok && v != "" {
			p.Title = v
		}
		if v, ok := metaMap["description"].(string); ok {
			p.Description = v
		}
		if v, ok := metaMap["date"].(string); ok {
			if t, err := parseDate(v); err == nil {
				p.Created = t
			}
		}
		switch tv := metaMap["tags"].(type) {
		case string:
			for _, t := range splitCommaOrSpace(tv) {
				p.Tags = append(p.Tags, strings.ToLower(strings.TrimSpace(t)))
			}
		case []interface{}:
			for _, x := range tv {
				if s, ok := x.(string); ok {
					p.Tags = append(p.Tags, strings.ToLower(strings.TrimSpace(s)))
				}
			}
		}
		switch av := metaMap["aliases"].(type) {
		case string:
			p.Aliases = []string{av}
		case []interface{}:
			for _, x := range av {
				if s, ok := x.(string); ok {
					p.Aliases = append(p.Aliases, s)
				}
			}
		}
	}

	// Normalize tags per the Page.Tags contract: lowercased above, here
	// sort + dedupe so output is deterministic and downstream consumers
	// (sitemap, tag pages, search index) can rely on stable ordering.
	if len(p.Tags) > 1 {
		sort.Strings(p.Tags)
		dedup := p.Tags[:1]
		for _, t := range p.Tags[1:] {
			if t != dedup[len(dedup)-1] {
				dedup = append(dedup, t)
			}
		}
		p.Tags = dedup
	}

	p.Slug = slug
	// Folder-index pages (root "index" or any "<dir>/index") collapse
	// their trailing /index segment so the canonical URL matches how the
	// HTTP server serves them. Without this, sitemap entries, breadcrumbs,
	// tag-page links, and the wikilink resolver would disagree on the URL
	// — e.g. /home/index/ in the sitemap vs /home/ from the resolver.
	switch {
	case slug == "index":
		p.RelativeURL = "/"
	case strings.HasSuffix(slug, "/index"):
		p.RelativeURL = "/" + strings.TrimSuffix(slug, "/index") + "/"
	default:
		p.RelativeURL = "/" + slug + "/"
	}
	if p.Title == "" {
		p.Title = firstHeadingText(doc, source)
	}
	if p.Title == "" {
		p.Title = humanizeSegment(filepath.Base(slug))
	}

	// Heuristic gating for client-side runtimes.
	src := string(source)
	p.HasMath = strings.Contains(src, "$$") || hasInlineMath(src)
	p.HasMermaid = strings.Contains(src, "```mermaid")

	p.ContentHTML = template.HTML(buf.String())
	p.TOC = extractTOC(doc, source)
	p.BreadcrumbItems = BuildBreadcrumb(slug)
	if p.Description == "" {
		p.Description = firstParagraph(buf.String())
	}
	p.WordCount = computeWordCount(buf.String())
	p.ReadingTime = computeReadingTime(p.WordCount)
	return p, nil
}

// RenderToBytes runs the full template chain (base.html.tmpl) for a page
// and returns the bytes. Used by the builder to write per-page index.html
// into the snapshot.
func (r *Renderer) RenderToBytes(_ *Page, data TemplateData) ([]byte, error) {
	var buf bytes.Buffer
	if err := r.templates.ExecuteTemplate(&buf, "base.html.tmpl", data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RenderFragment re-executes just the `content` block — used by the
// HX-Request: true branch to swap #main without re-shipping the full
// shell. The `content` block emits the inner article; we wrap it in
// the same `<main id="main">` element the base template uses so that
// htmx's `hx-select="#main" hx-swap="outerHTML"` finds a #main in the
// response and the new element keeps the hx-boost / hx-target wiring
// for subsequent navigations. Without the wrapper htmx selects
// nothing — the main area renders empty until the user refreshes.
//
// The fragment also includes the `page-tools` block (graph / TOC /
// backlinks) carrying `hx-swap-oob="outerHTML"`. htmx pulls that out
// of the response as an out-of-band swap and replaces the existing
// #page-tools in the right rail, keeping the rail in sync with the
// active page — base.html.tmpl renders only the surrounding shell,
// not this block, on htmx requests.
func (r *Renderer) RenderFragment(_ *Page, data TemplateData) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(`<main id="main" tabindex="-1" hx-boost="true" hx-target="#main" hx-select="#main, #page-tools" hx-swap="outerHTML show:window:top">`)
	if err := r.templates.ExecuteTemplate(&buf, "content", data); err != nil {
		return nil, err
	}
	buf.WriteString(`</main>`)
	if err := r.templates.ExecuteTemplate(&buf, "page-tools", data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RenderList builds a folder/tag listing page using list.html.tmpl.
func (r *Renderer) RenderList(_ *Page, data TemplateData) ([]byte, error) {
	var buf bytes.Buffer
	if err := r.templates.ExecuteTemplate(&buf, "list.html.tmpl", data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Render404 renders the static 404 page used by the static handler when
// a path doesn't resolve.
func (r *Renderer) Render404(data TemplateData) ([]byte, error) {
	var buf bytes.Buffer
	if err := r.templates.ExecuteTemplate(&buf, "404.html.tmpl", data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Templates returns the parsed template set (exposed for the server
// fragment shim).
func (r *Renderer) Templates() *template.Template { return r.templates }

// slugFromPath turns `meta/schema.md` → `meta/schema`. Filepath.ToSlash
// normalizes Windows separators.
func slugFromPath(p string) string {
	p = strings.TrimSuffix(p, ".md")
	return filepath.ToSlash(p)
}

// escapePipesInsideWikilinks rewrites `[[target|alias]]` → `[[target\|alias]]`
// in the source before goldmark sees it. The GFM table parser runs before
// inline parsers, so an unescaped pipe inside a wikilink located in a table
// cell is consumed as a column separator — cell splits in half and the
// wikilink never resolves. Quartz handles this by running its wikilink
// transformer pre-parse; we accomplish the same with a tiny scan that only
// touches text inside `[[...]]` segments.
//
// Already-escaped pipes (`\|`) and pipes outside any `[[...]]` are
// untouched, so this is safe on documents authored either way.
func escapePipesInsideWikilinks(src []byte) []byte {
	if len(src) < 4 {
		return src
	}
	var out []byte
	inLink := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if !inLink {
			if c == '[' && i+1 < len(src) && src[i+1] == '[' {
				inLink = true
				if out == nil {
					out = make([]byte, 0, len(src)+16)
					out = append(out, src[:i]...)
				}
				out = append(out, '[', '[')
				i++
				continue
			}
			if out != nil {
				out = append(out, c)
			}
			continue
		}
		// inside [[...]]
		if c == ']' && i+1 < len(src) && src[i+1] == ']' {
			inLink = false
			out = append(out, ']', ']')
			i++
			continue
		}
		if c == '\n' {
			// Wikilinks don't span lines in Obsidian — bail out so a
			// stray `[[` doesn't accidentally swallow the rest of the
			// document.
			inLink = false
			out = append(out, c)
			continue
		}
		if c == '|' && (i == 0 || src[i-1] != '\\') {
			out = append(out, '\\', '|')
			continue
		}
		out = append(out, c)
	}
	if out == nil {
		return src
	}
	return out
}

// parseDate accepts the common frontmatter date shapes: ISO 8601, RFC 3339,
// and bare `YYYY-MM-DD`.
func parseDate(s string) (time.Time, error) {
	layouts := []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized date: %q", s)
}

// splitCommaOrSpace handles tag strings of either shape:
//
//	"homelab, guide" → ["homelab", "guide"]
//	"homelab guide"   → ["homelab", "guide"]
func splitCommaOrSpace(s string) []string {
	if strings.ContainsRune(s, ',') {
		return strings.Split(s, ",")
	}
	return strings.Fields(s)
}

// hasInlineMath is a coarse `$x$` detection. False positives on dollar
// signs in prose are acceptable — we only use this to gate KaTeX loading.
func hasInlineMath(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			continue
		}
		first := strings.IndexByte(line, '$')
		if first < 0 {
			continue
		}
		second := strings.IndexByte(line[first+1:], '$')
		if second >= 0 {
			return true
		}
	}
	return false
}

// firstHeadingText returns the first H1 text in the document, or "" if
// there isn't one. Used as a title fallback after frontmatter.
func firstHeadingText(doc ast.Node, source []byte) string {
	for c := doc.FirstChild(); c != nil; c = c.NextSibling() {
		if h, ok := c.(*ast.Heading); ok && h.Level == 1 {
			return string(h.Text(source)) //nolint:staticcheck
		}
	}
	return ""
}
