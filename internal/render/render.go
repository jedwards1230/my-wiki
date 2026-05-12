package render

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"path/filepath"
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
type Renderer struct {
	md        goldmark.Markdown
	templates *template.Template
	slugs     map[string]string
}

// NewRenderer compiles a Renderer with the wikilink slug index. Returns
// an error only if template parsing fails — goldmark wiring is straightline.
//
// Callers in long-running mode should call NewRenderer once and reuse it
// across Build()s; the slug map is rebuilt and the renderer recreated only
// when wikilink targets change.
func NewRenderer(slugs map[string]string) (*Renderer, error) {
	md := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			meta.New(meta.WithStoresInDocument()),
			extension.Footnote,
			extension.DefinitionList,
			extension.Typographer,
			emoji.Emoji,
			highlighting.NewHighlighting(
				highlighting.WithStyle("github"),
			),
			&obsidianExtension{},
			newWikilinkExtender(slugs),
			// Mermaid: client-side passthrough — ```mermaid``` blocks are
			// emitted as <pre class="mermaid"> for mermaid.min.js to pick
			// up at runtime. NoScript=true keeps the extension from
			// injecting its own <script> tag (we control loading via
			// wiki.js based on Page.HasMermaid).
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

	tmpl, err := loadTemplates()
	if err != nil {
		return nil, fmt.Errorf("load templates: %w", err)
	}
	return &Renderer{md: md, templates: tmpl, slugs: slugs}, nil
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
	}
}

// TemplateData is the top-level data passed to templates. Embedding *Page
// gives templates direct access to page fields plus surrounding context
// (site title, active path for navigation highlighting).
type TemplateData struct {
	*Page
	SiteTitle  string
	ActivePath string // for explorer active-state — same as Page.RelativeURL
	BuildDate  string
}

// RenderPage runs goldmark over the page's raw markdown bytes, populates
// every metadata field on *Page, and stores the rendered HTML.
//
// `path` is the relative path inside the vault (e.g. `meta/schema.md`).
// `source` is the raw file content. `modTime` is the file's mtime —
// surfaces in the "Modified" footer.
func (r *Renderer) RenderPage(path string, source []byte, modTime time.Time) (*Page, error) {
	p := &Page{Modified: modTime}

	ctx := parser.NewContext()
	reader := text.NewReader(source)
	doc := r.md.Parser().Parse(reader, parser.WithContext(ctx))

	var buf bytes.Buffer
	if err := r.md.Renderer().Render(&buf, source, doc); err != nil {
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

	slug := slugFromPath(path)
	p.Slug = slug
	p.RelativeURL = "/" + slug + "/"
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
// shell. Same TemplateData; the template's `{{ define "content" }}`
// block is executed in isolation.
func (r *Renderer) RenderFragment(_ *Page, data TemplateData) ([]byte, error) {
	var buf bytes.Buffer
	if err := r.templates.ExecuteTemplate(&buf, "content", data); err != nil {
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
