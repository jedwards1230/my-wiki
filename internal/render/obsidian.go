package render

// obsidian.go holds every Obsidian-flavored markdown extension the
// renderer ships: callouts, ==highlight==, %%comment%%, $math$ inline +
// $$math$$ block, ^block-id refs, the wikilink resolver, and the TOC
// extractor. Kept in one file to match Quartz's "OFM as a single
// transformer" shape and the slimmed plan's "ONE obsidian.go" mandate.

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	"go.abhg.dev/goldmark/wikilink"
)

// =============================================================================
// Wikilink resolver
// =============================================================================

// SlugResolver implements wikilink.Resolver against a slug → relative-path
// map built by vault.BuildSlugIndex. It also drives the [[page|alias]],
// [[page#heading]], and ![[image.ext]] forms.
//
// The map is read-only after construction — the renderer rebuilds it
// per Build() call to avoid sharing mutable state across goroutines.
type SlugResolver struct {
	Slugs map[string]string
}

// ResolveWikilink returns the URL bytes for a wikilink target. Returns
// (nil, nil) to render the link as plain text when the target is missing
// — matches Quartz's behavior for broken wikilinks.
func (r *SlugResolver) ResolveWikilink(n *wikilink.Node) ([]byte, error) {
	target := string(n.Target)
	frag := string(n.Fragment)

	// Image / embed: [[foo.png]] — the abhg renderer handles embeds via the
	// Embed flag and emits <img>. We resolve such links to /raw/{target}
	// because images live in the raw/ tree.
	if n.Embed && hasFileExtension(target) {
		// Linkable assets are always served from /raw/
		out := "/raw/" + target
		return []byte(out), nil
	}

	if target == "" && frag != "" {
		// Same-page anchor: [[#heading]] → #heading
		return []byte("#" + slugifyHeading(frag)), nil
	}

	key := strings.ToLower(target)
	canonical, ok := r.Slugs[key]
	if !ok {
		// Broken link — leave as plain text. The abhg renderer wraps it
		// in a class="broken" span when destination is nil; we instead
		// return an empty href that the template's CSS styles distinctly.
		// Returning (nil, nil) renders the contents only — which is what
		// Quartz does for broken links.
		return nil, nil
	}
	url := "/" + canonical + "/"
	if frag != "" {
		url += "#" + slugifyHeading(frag)
	}
	return []byte(url), nil
}

// hasFileExtension is a small heuristic: a target with a "." that isn't
// at the start and isn't followed by a wikilink fragment is treated as a
// filename. Used so `![[image.png]]` resolves to /raw/image.png while
// `[[page]]` resolves to a wiki page URL.
func hasFileExtension(s string) bool {
	i := strings.LastIndexByte(s, '.')
	return i > 0 && i < len(s)-1
}

// slugifyHeading mirrors goldmark's default heading anchor scheme so
// `[[page#My Heading]]` resolves to `#my-heading`.
func slugifyHeading(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
	prevDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case r == ' ' || r == '-' || r == '_':
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// =============================================================================
// Highlight (==text==) — inline parser → <mark>
// =============================================================================

// highlightNode is the AST node for `==text==`.
type highlightNode struct {
	ast.BaseInline
}

var kindHighlight = ast.NewNodeKind("ObsidianHighlight")

func (n *highlightNode) Kind() ast.NodeKind         { return kindHighlight }
func (n *highlightNode) Dump(src []byte, level int) { ast.DumpHelper(n, src, level, nil, nil) }

type highlightParser struct{}

func (p *highlightParser) Trigger() []byte { return []byte{'='} }

func (p *highlightParser) Parse(parent ast.Node, block text.Reader, _ parser.Context) ast.Node {
	line, segment := block.PeekLine()
	if len(line) < 4 || line[0] != '=' || line[1] != '=' {
		return nil
	}
	// Find closing ==. Scan within the same line; multi-line highlights
	// are rare and Obsidian doesn't support them either.
	end := bytes.Index(line[2:], []byte("=="))
	if end < 0 {
		return nil
	}
	contentStart := segment.Start + 2
	contentEnd := segment.Start + 2 + end

	n := &highlightNode{}
	n.AppendChild(n, ast.NewTextSegment(text.NewSegment(contentStart, contentEnd)))
	block.Advance(end + 4) // ==content==
	return n
}

type highlightRenderer struct{}

func (h *highlightRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindHighlight, h.render)
}

func (h *highlightRenderer) render(w util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString("<mark>")
	} else {
		_, _ = w.WriteString("</mark>")
	}
	return ast.WalkContinue, nil
}

// =============================================================================
// Comment (%%text%%) — drop content
// =============================================================================

// commentNode is the AST node for `%%text%%`; renderer drops the entire
// subtree (and its child text) so authors can leave inline TODO notes
// invisible to readers.
type commentNode struct{ ast.BaseInline }

var kindComment = ast.NewNodeKind("ObsidianComment")

func (n *commentNode) Kind() ast.NodeKind         { return kindComment }
func (n *commentNode) Dump(src []byte, level int) { ast.DumpHelper(n, src, level, nil, nil) }

type commentParser struct{}

func (p *commentParser) Trigger() []byte { return []byte{'%'} }

func (p *commentParser) Parse(_ ast.Node, block text.Reader, _ parser.Context) ast.Node {
	line, _ := block.PeekLine()
	if len(line) < 4 || line[0] != '%' || line[1] != '%' {
		return nil
	}
	end := bytes.Index(line[2:], []byte("%%"))
	if end < 0 {
		return nil
	}
	block.Advance(end + 4)
	return &commentNode{}
}

type commentRenderer struct{}

func (c *commentRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindComment, c.render)
}

func (c *commentRenderer) render(_ util.BufWriter, _ []byte, _ ast.Node, _ bool) (ast.WalkStatus, error) {
	// Drop the entire node and its children — comments are invisible.
	return ast.WalkSkipChildren, nil
}

// =============================================================================
// Math — inline $x$ and display $$x$$ (delimiters preserved for KaTeX
// auto-render at runtime)
// =============================================================================

type inlineMathNode struct{ ast.BaseInline }

var kindInlineMath = ast.NewNodeKind("InlineMath")

func (n *inlineMathNode) Kind() ast.NodeKind         { return kindInlineMath }
func (n *inlineMathNode) Dump(src []byte, level int) { ast.DumpHelper(n, src, level, nil, nil) }

// blockMathNode stores the raw TeX expression on the node itself rather
// than as a child so the parser doesn't have to materialize text segments
// at parse time (and the walker doesn't trip on an inline child of a
// block node).
type blockMathNode struct {
	ast.BaseBlock
	expr []byte
}

var kindBlockMath = ast.NewNodeKind("BlockMath")

func (n *blockMathNode) Kind() ast.NodeKind         { return kindBlockMath }
func (n *blockMathNode) Dump(src []byte, level int) { ast.DumpHelper(n, src, level, nil, nil) }
func (n *blockMathNode) IsRaw() bool                { return true }

type mathInlineParser struct{}

func (p *mathInlineParser) Trigger() []byte { return []byte{'$'} }

func (p *mathInlineParser) Parse(_ ast.Node, block text.Reader, _ parser.Context) ast.Node {
	line, segment := block.PeekLine()
	if len(line) < 3 || line[0] != '$' {
		return nil
	}
	// $$ is the block form — let the block parser handle it. Inline parsers
	// run after blocks, but we still defensively bail if we see $$ here.
	if line[1] == '$' {
		return nil
	}
	// Find the next un-escaped `$`. Math expressions can contain `\$`
	// (TeX literal) so we honor that.
	end := -1
	for i := 1; i < len(line); i++ {
		if line[i] == '$' && line[i-1] != '\\' {
			end = i
			break
		}
	}
	if end < 1 {
		return nil
	}
	contentStart := segment.Start + 1
	contentEnd := segment.Start + end

	n := &inlineMathNode{}
	n.AppendChild(n, ast.NewTextSegment(text.NewSegment(contentStart, contentEnd)))
	block.Advance(end + 1)
	return n
}

type mathBlockParser struct{}

func (p *mathBlockParser) Trigger() []byte { return []byte{'$'} }

func (p *mathBlockParser) Open(_ ast.Node, reader text.Reader, _ parser.Context) (ast.Node, parser.State) {
	line, _ := reader.PeekLine()
	if len(line) < 2 || line[0] != '$' || line[1] != '$' {
		return nil, parser.NoChildren
	}
	trimmed := bytes.TrimRight(line, "\n")
	// Single-line shape: $$ expression $$
	if len(trimmed) > 4 && bytes.HasSuffix(trimmed, []byte("$$")) {
		n := &blockMathNode{expr: bytes.TrimSpace(trimmed[2 : len(trimmed)-2])}
		reader.Advance(len(line))
		return n, parser.NoChildren | parser.Close
	}
	// Multi-line: consume body lines until closing $$.
	reader.Advance(len(line))
	var buf bytes.Buffer
	for {
		l, _ := reader.PeekLine()
		if l == nil {
			break
		}
		if bytes.HasPrefix(bytes.TrimSpace(l), []byte("$$")) {
			reader.Advance(len(l))
			break
		}
		buf.Write(l)
		reader.Advance(len(l))
	}
	n := &blockMathNode{expr: bytes.TrimSpace(buf.Bytes())}
	return n, parser.NoChildren | parser.Close
}

func (p *mathBlockParser) Continue(_ ast.Node, _ text.Reader, _ parser.Context) parser.State {
	return parser.Close
}
func (p *mathBlockParser) Close(_ ast.Node, _ text.Reader, _ parser.Context) {}
func (p *mathBlockParser) CanInterruptParagraph() bool                       { return true }
func (p *mathBlockParser) CanAcceptIndentedLine() bool                       { return false }

type mathRenderer struct{}

func (m *mathRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindInlineMath, m.renderInline)
	reg.Register(kindBlockMath, m.renderBlock)
}

func (m *mathRenderer) renderInline(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	// Emit `<span class="math-inline">$expr$</span>` — KaTeX auto-render
	// scans for delimiters at the DOM level so we keep them intact.
	_, _ = w.WriteString(`<span class="math-inline">$`)
	for c := node.FirstChild(); c != nil; c = c.NextSibling() {
		if seg, ok := c.(*ast.Text); ok {
			_, _ = w.Write(util.EscapeHTML(seg.Segment.Value(source)))
		}
	}
	_, _ = w.WriteString(`$</span>`)
	return ast.WalkSkipChildren, nil
}

func (m *mathRenderer) renderBlock(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*blockMathNode)
	_, _ = w.WriteString("<div class=\"math-display\">$$")
	_, _ = w.Write(util.EscapeHTML(n.expr))
	_, _ = w.WriteString("$$</div>\n")
	return ast.WalkSkipChildren, nil
}

// =============================================================================
// Callouts — > [!kind] Title\n> body
// =============================================================================

// validCalloutKinds matches Obsidian's 13 built-in callout kinds.
var validCalloutKinds = map[string]struct{}{
	"note": {}, "info": {}, "tip": {}, "warning": {}, "danger": {},
	"example": {}, "quote": {}, "abstract": {}, "todo": {}, "success": {},
	"question": {}, "failure": {}, "bug": {},
}

// calloutHeader matches `[!kind]` or `[!kind]+`/`[!kind]-` at the start of
// a blockquote's first text. Capture groups: kind, fold marker, title.
var calloutHeader = regexp.MustCompile(`^\[!([a-zA-Z]+)\]([+-]?)\s*(.*)`)

// calloutTransformer walks Blockquote nodes and rewrites those whose
// first text starts with `[!kind]` into a styled <div> in the renderer.
//
// We tag the AST with attributes (callout-kind, callout-title, callout-fold)
// and intercept rendering in calloutRenderer. The original blockquote
// node is still emitted — its rendering is replaced.
type calloutTransformer struct{}

func (t *calloutTransformer) Transform(node *ast.Document, reader text.Reader, _ parser.Context) {
	source := reader.Source()
	_ = ast.Walk(node, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		bq, ok := n.(*ast.Blockquote)
		if !ok {
			return ast.WalkContinue, nil
		}
		para, ok := bq.FirstChild().(*ast.Paragraph)
		if !ok {
			return ast.WalkContinue, nil
		}
		// Paragraph.Lines() returns the original source line segments —
		// the inline children have been split across delimiters and don't
		// give us back the raw text reliably.
		lines := para.Lines()
		if lines.Len() == 0 {
			return ast.WalkContinue, nil
		}
		firstSegV := lines.At(0)
		firstLine := strings.TrimRight(string(firstSegV.Value(source)), "\n\r")
		m := calloutHeader.FindStringSubmatch(firstLine)
		if m == nil {
			return ast.WalkContinue, nil
		}
		kind := strings.ToLower(m[1])
		if _, ok := validCalloutKinds[kind]; !ok {
			return ast.WalkContinue, nil
		}
		fold := m[2]
		title := strings.TrimSpace(m[3])
		if title == "" {
			title = humanizeSegment(kind)
		}
		bq.SetAttributeString("data-callout-kind", []byte(kind))
		bq.SetAttributeString("data-callout-title", []byte(title))
		bq.SetAttributeString("data-callout-fold", []byte(fold))
		// Strip the header line: walk inline children dropping any text
		// nodes whose segment falls within the first source line.
		firstSeg := firstSegV
		var toRemove []ast.Node
		for c := para.FirstChild(); c != nil; c = c.NextSibling() {
			t, ok := c.(*ast.Text)
			if !ok {
				continue
			}
			if t.Segment.Start < firstSeg.Stop {
				toRemove = append(toRemove, c)
				continue
			}
			break
		}
		for _, c := range toRemove {
			para.RemoveChild(para, c)
		}
		if para.ChildCount() == 0 {
			bq.RemoveChild(bq, para)
		}
		return ast.WalkSkipChildren, nil
	})
}

// calloutRenderer intercepts blockquote rendering. When the blockquote
// has a `data-callout-kind` attribute it emits a styled <div>; otherwise
// falls back to the default blockquote rendering.
type calloutRenderer struct {
	html.Config
}

func newCalloutRenderer(opts ...html.Option) renderer.NodeRenderer {
	r := &calloutRenderer{Config: html.NewConfig()}
	for _, o := range opts {
		o.SetHTMLOption(&r.Config)
	}
	return r
}

func (r *calloutRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindBlockquote, r.render)
}

func (r *calloutRenderer) render(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	bq := node.(*ast.Blockquote)
	kindAttr, hasKind := bq.AttributeString("data-callout-kind")
	if !hasKind {
		// Plain blockquote.
		if entering {
			_, _ = w.WriteString("<blockquote>\n")
		} else {
			_, _ = w.WriteString("</blockquote>\n")
		}
		return ast.WalkContinue, nil
	}
	if !entering {
		_, _ = w.WriteString("</div></div>\n")
		return ast.WalkContinue, nil
	}
	kind := string(kindAttr.([]byte))
	titleAttr, _ := bq.AttributeString("data-callout-title")
	foldAttr, _ := bq.AttributeString("data-callout-fold")
	title := string(titleAttr.([]byte))
	fold := string(foldAttr.([]byte))

	classes := "callout callout-" + kind
	switch fold {
	case "+":
		classes += " is-collapsible is-collapsed"
	case "-":
		classes += " is-collapsible"
	}
	_, _ = w.WriteString(`<div class="`)
	_, _ = w.WriteString(classes)
	_, _ = w.WriteString(`" data-callout="`)
	_, _ = w.WriteString(kind)
	_, _ = w.WriteString(`"><div class="callout-title"><span class="callout-icon" aria-hidden="true"></span><span class="callout-title-inner">`)
	_, _ = w.Write(util.EscapeHTML([]byte(title)))
	_, _ = w.WriteString(`</span></div><div class="callout-content">`)
	return ast.WalkContinue, nil
}

// =============================================================================
// Block reference (`^block-id` at end of a paragraph/heading)
// =============================================================================

// blockRefRe matches `^block-id` at the end of a paragraph/heading's text.
var blockRefRe = regexp.MustCompile(`\s*\^([a-zA-Z0-9_-]+)\s*$`)

type blockRefTransformer struct{}

func (t *blockRefTransformer) Transform(node *ast.Document, reader text.Reader, _ parser.Context) {
	source := reader.Source()
	_ = ast.Walk(node, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		// Only check trailing text of paragraphs/headings.
		var lastText *ast.Text
		switch n.(type) {
		case *ast.Paragraph, *ast.Heading:
			c := n.LastChild()
			for c != nil {
				if t, ok := c.(*ast.Text); ok {
					lastText = t
					break
				}
				c = c.PreviousSibling()
			}
		}
		if lastText == nil {
			return ast.WalkContinue, nil
		}
		raw := lastText.Segment.Value(source)
		m := blockRefRe.FindSubmatchIndex(raw)
		if m == nil {
			return ast.WalkContinue, nil
		}
		// m[2:4] is the block-id capture range within `raw`.
		blockID := string(raw[m[2]:m[3]])
		n.SetAttributeString("id", []byte(blockID))
		// Trim the matched suffix from the text segment so the visible
		// text no longer shows `^id`.
		trimmedEnd := lastText.Segment.Start + m[0]
		lastText.Segment = text.NewSegment(lastText.Segment.Start, trimmedEnd)
		return ast.WalkContinue, nil
	})
}

// =============================================================================
// TOC extractor — populates Page.TOC during a walk
// =============================================================================

// extractTOC walks the AST and returns heading entries at depth 2-6.
// Anchor is the slugified heading text — matches heading IDs generated
// by goldmark's auto-heading-ids extension.
func extractTOC(doc ast.Node, source []byte) []TOCEntry {
	var out []TOCEntry
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		h, ok := n.(*ast.Heading)
		if !ok || h.Level < 2 || h.Level > 6 {
			return ast.WalkContinue, nil
		}
		text := string(h.Text(source)) //nolint:staticcheck // ast.Node.Text is the right tool for plain extraction.
		// Use the heading's existing id attribute if goldmark auto-id'd it;
		// otherwise compute one.
		var anchor string
		if v, ok := h.AttributeString("id"); ok {
			anchor = string(v.([]byte))
		} else {
			anchor = slugifyHeading(text)
		}
		out = append(out, TOCEntry{Depth: h.Level, Text: text, Anchor: anchor})
		return ast.WalkContinue, nil
	})
	return out
}

// =============================================================================
// Extension assembly
// =============================================================================

// obsidianExtension bundles every inline/block/transformer/renderer pair
// into one goldmark.Extender. Plugged into render.New(). The wikilink
// resolver is wired separately via newWikilinkExtender so the slug map
// can be threaded in at render time without rebuilding this extension.
type obsidianExtension struct{}

// Extend implements goldmark.Extender.
func (e *obsidianExtension) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(
		parser.WithInlineParsers(
			util.Prioritized(&highlightParser{}, 200),
			util.Prioritized(&commentParser{}, 199),
			util.Prioritized(&mathInlineParser{}, 198),
		),
		parser.WithBlockParsers(
			util.Prioritized(&mathBlockParser{}, 200),
		),
		parser.WithASTTransformers(
			util.Prioritized(&calloutTransformer{}, 100),
			util.Prioritized(&blockRefTransformer{}, 101),
		),
	)
	m.Renderer().AddOptions(
		renderer.WithNodeRenderers(
			util.Prioritized(&highlightRenderer{}, 200),
			util.Prioritized(&commentRenderer{}, 199),
			util.Prioritized(&mathRenderer{}, 198),
			util.Prioritized(newCalloutRenderer(), 197),
		),
	)
}

// newWikilinkExtender wires the abhg wikilink extension with our slug-aware
// resolver.
func newWikilinkExtender(slugs map[string]string) goldmark.Extender {
	return &wikilink.Extender{Resolver: &SlugResolver{Slugs: slugs}}
}
