package render

// obsidian.go holds every Obsidian-flavored markdown extension the
// renderer ships: callouts, ==highlight==, %%comment%%, $math$ inline +
// $$math$$ block, ^block-id refs, the wikilink resolver, and the TOC
// extractor. Kept in one file so all Obsidian-flavored markdown handling
// lives in a single place.

import (
	"bytes"
	"regexp"
	"strings"
	"sync"

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
// — broken wikilinks degrade to their literal text.
func (r *SlugResolver) ResolveWikilink(n *wikilink.Node) ([]byte, error) {
	target := string(n.Target)
	frag := string(n.Fragment)

	// Obsidian stores aliased wikilinks as [[target\|alias]] in raw markdown.
	// The abhg wikilink parser does not treat \| as an escape sequence, so the
	// target arrives with a trailing backslash. Strip it so the slug lookup
	// against "home/index" (not "home/index\") resolves correctly.
	target = strings.TrimSuffix(target, `\`)

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
		// Returning (nil, nil) renders the contents only — broken links
		// show as plain text.
		return nil, nil
	}
	// "index" is the home page — emit "/" instead of "/index/".
	// "foo/index" is a folder index — emit "/foo/" (the folder URL) so
	// wikilinks like [[home/index|home/]] resolve to the canonical folder URL.
	var url string
	switch {
	case canonical == "index":
		url = "/"
	case strings.HasSuffix(canonical, "/index"):
		url = "/" + strings.TrimSuffix(canonical, "/index") + "/"
	default:
		url = "/" + canonical + "/"
	}
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
// Inline #tag parser → <a class="internal tag-link" href="/tags/{tag}/">
// =============================================================================

// inlineTagNode is the AST node for `#tag`, `#tag/sub`, `#tag/sub/sub`.
type inlineTagNode struct {
	ast.BaseInline
	tag string // normalized tag slug (lowercase, no leading #)
}

var kindInlineTag = ast.NewNodeKind("ObsidianInlineTag")

func (n *inlineTagNode) Kind() ast.NodeKind         { return kindInlineTag }
func (n *inlineTagNode) Dump(src []byte, level int) { ast.DumpHelper(n, src, level, nil, nil) }

// inlineTagParser matches `#tag`, `#tag/sub`, `#tag/sub/sub` in body text.
// False-positive guards (enforced in Parse):
//   - Goldmark handles `[link](#anchor)` as a link destination before
//     inline parsers run — the `#` inside `()` is never seen by us.
//   - `# Heading` is a block-level ATX heading and never reaches inline
//     parsing.
//   - Inline code spans are already consumed by goldmark's code-span
//     parser (priority 100) before we run (priority 97), so `#` inside
//     backtick spans is invisible to us.
//   - We reject `#` immediately preceded by an alphanumeric, `_`, `-`,
//     `(`, or `[` in the source line to avoid matching URL fragments
//     and dash-joined identifiers (e.g. `commit-#1234`, `[link](#x)`).
type inlineTagParser struct{}

func (p *inlineTagParser) Trigger() []byte { return []byte{'#'} }

// isTagStart returns true when b is a valid first character after `#`.
// Restricted to lowercase ASCII letters so bare `#`, numeric `#1234`
// commit refs, and `/`/`-` lead-ins don't match. The continuation rule
// (isTagContinue) is broader and permits digits, slashes, hyphens, and
// underscores for nested-tag paths like `#meta/claude-code`.
func isTagStart(b byte) bool {
	return b >= 'a' && b <= 'z'
}

// isTagContinue returns true for chars that can follow the opening letter.
func isTagContinue(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '/' || b == '-' || b == '_'
}

func (p *inlineTagParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, segment := block.PeekLine()
	if len(line) < 2 || line[0] != '#' {
		return nil
	}

	// Guard: reject `#` preceded by a word char, `(`, or `[` — those
	// indicate a URL fragment, issue-number, or markdown link destination.
	// block.Position() returns the absolute offset into the source;
	// segment.Start is the byte offset of the current line's start.
	// The character immediately before the `#` is at segment.Start - 1
	// relative to the source — but we can read it from the raw source
	// via the reader's Source() method if available. Simpler: check
	// whether the column before the trigger is a word/bracket char by
	// reading back from the segment start + current column offset.
	//
	// goldmark advances segment.Start on each Advance call, so the
	// current position within the line is (current offset - segment.Start).
	// The character before the `#` on this line (if any) can be found by
	// checking the last byte consumed before we were called.
	//
	// Practical approach: read the source and look at the byte before this
	// `#`. If the reader doesn't expose the full source we skip this check
	// (safe to proceed; most false-positives are caught by the block parser).
	src := block.Source()
	if segment.Start > 0 {
		prev := src[segment.Start-1]
		if (prev >= 'a' && prev <= 'z') || (prev >= 'A' && prev <= 'Z') ||
			(prev >= '0' && prev <= '9') || prev == '_' || prev == '-' ||
			prev == '(' || prev == '[' {
			return nil
		}
	}

	// The first char after '#' must be a lowercase letter.
	if !isTagStart(line[1]) {
		return nil
	}

	end := 1
	for end < len(line) && isTagContinue(line[end]) {
		end++
	}
	// Tag must end at a word boundary: next char must not be a tag-continue
	// char (or we've reached end of line). This is automatically satisfied
	// by the loop above.
	if end < 2 {
		return nil
	}

	tag := strings.ToLower(string(line[1:end]))
	n := &inlineTagNode{tag: tag}
	block.Advance(end) // consume `#tag...`
	_ = pc
	return n
}

type inlineTagRenderer struct{}

func (r *inlineTagRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindInlineTag, r.render)
}

func (r *inlineTagRenderer) render(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	n := node.(*inlineTagNode)
	_, _ = w.WriteString(`<a class="internal tag-link" href="/tags/`)
	_, _ = w.WriteString(n.tag)
	_, _ = w.WriteString(`/">`)
	_, _ = w.WriteString(n.tag)
	_, _ = w.WriteString(`</a>`)
	return ast.WalkSkipChildren, nil
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
			// inlineTagParser runs after code-span (100) so #tag inside
			// backtick spans is never seen here (already consumed).
			util.Prioritized(&inlineTagParser{}, 97),
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
			util.Prioritized(&inlineTagRenderer{}, 97),
		),
	)
}

// newWikilinkExtender wires the abhg wikilink *parser* with a custom
// renderer that handles both plain wikilinks and full transclusion
// (![[page]], ![[page#heading]], ![[page#^block-id]]).
//
// The custom renderer replaces abhg's Renderer at the same priority — it
// installs after the abhg parser so the parse layer is identical, but the
// rendering side delegates to transcludeRenderer for embeds that target
// other vault pages (not image files).
func newWikilinkExtender(slugs map[string]string, transcludes *TranscludeSource) goldmark.Extender {
	return &customWikilinkExtender{
		resolver:    &SlugResolver{Slugs: slugs},
		transcludes: transcludes,
	}
}

// customWikilinkExtender installs abhg's wikilink parser and our custom
// renderer. Mirrors wikilink.Extender's parser priority (199) so the "["
// trigger still wins over goldmark's built-in link parser (priority 200).
type customWikilinkExtender struct {
	resolver    *SlugResolver
	transcludes *TranscludeSource
}

func (e *customWikilinkExtender) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(
		parser.WithInlineParsers(
			util.Prioritized(&wikilink.Parser{}, 199),
		),
	)
	m.Renderer().AddOptions(
		renderer.WithNodeRenderers(
			util.Prioritized(&transcludeRenderer{
				resolver:    e.resolver,
				transcludes: e.transcludes,
			}, 199),
		),
	)
}

// =============================================================================
// Transclusion
// =============================================================================

// MaxTranscludeDepth bounds recursive transclusion. Beyond this depth the
// renderer emits a `transclude-overflow` marker instead of descending
// further. Three is enough for "page A includes B includes C includes D"
// patterns without runaway recursion.
const MaxTranscludeDepth = 3

// TranscludeSource gives the wikilink renderer the data it needs to
// resolve a transclusion: the parsed AST of every other page in the
// vault, the same goldmark.Markdown that produced those ASTs (so we can
// render subtrees back to HTML), and the current page's slug + visited
// set for cycle detection.
//
// One TranscludeSource is constructed per page render. The Builder owns
// the immutable bits (Cache, MD) and clones the source with a fresh
// visited set and current slug for each RenderPage call.
type TranscludeSource struct {
	// Cache maps lowercase slug → the parsed source for that page. nil
	// when transclusion is disabled (e.g. in unit tests that don't
	// pre-parse — broken targets simply render as broken links).
	Cache map[string]*ParsedPage

	// MD is the goldmark instance used to render transcluded subtrees.
	// MUST be the same instance that produced the cached ASTs.
	MD goldmark.Markdown

	// SlugTitles maps lowercase slug → display title for the "From: X"
	// source link in the transclusion wrapper.
	SlugTitles map[string]string

	// CurrentSlug is the slug of the page currently being rendered.
	// Seeded into the visited set so a page can't transclude itself.
	CurrentSlug string

	// Visited tracks slugs already on the transclusion stack. Lookups
	// use strings.ToLower(slug). The map is per-page; do not share
	// across goroutines.
	Visited map[string]struct{}

	// Depth is the current nesting depth. Starts at 0 for top-level
	// rendering; each recursive transclusion increments it.
	Depth int
}

// child returns a TranscludeSource one level deeper, with `target` added
// to the visited set. The returned struct is safe to mutate — the
// visited map is cloned so the caller's state is unaffected.
func (t *TranscludeSource) child(target string) *TranscludeSource {
	visited := make(map[string]struct{}, len(t.Visited)+1)
	for k := range t.Visited {
		visited[k] = struct{}{}
	}
	visited[strings.ToLower(target)] = struct{}{}
	return &TranscludeSource{
		Cache:       t.Cache,
		MD:          t.MD,
		SlugTitles:  t.SlugTitles,
		CurrentSlug: target,
		Visited:     visited,
		Depth:       t.Depth + 1,
	}
}

// ParsedPage is one entry in the AST cache: the parsed root document
// plus the source bytes needed to render its subtrees.
type ParsedPage struct {
	Slug   string
	Title  string
	Source []byte
	Doc    ast.Node
}

// transcludeRenderer renders wikilink.Node nodes. For non-Embed links
// and for image embeds, it mirrors abhg's renderer output. For non-image
// embeds it triggers the transclusion path.
type transcludeRenderer struct {
	resolver    *SlugResolver
	transcludes *TranscludeSource
}

func (r *transcludeRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(wikilink.Kind, r.render)
}

// hasDest tracks whether we wrote an opening <a> for a given node so the
// exit pass knows whether to close it. Keyed by *wikilink.Node pointer.
var hasDestKeys sync.Map // *wikilink.Node → struct{}

func (r *transcludeRenderer) render(w util.BufWriter, src []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n, ok := node.(*wikilink.Node)
	if !ok {
		return ast.WalkStop, nil
	}

	if !entering {
		if _, ok := hasDestKeys.LoadAndDelete(n); ok {
			_, _ = w.WriteString("</a>")
		}
		return ast.WalkContinue, nil
	}

	target := string(n.Target)
	frag := string(n.Fragment)

	// Image embed — mirror abhg's <img> emission so [[foo.png]] still works.
	if n.Embed && isImageExtension(target) {
		dest, err := r.resolver.ResolveWikilink(n)
		if err != nil || len(dest) == 0 {
			return ast.WalkContinue, nil
		}
		_, _ = w.WriteString(`<img src="`)
		_, _ = w.Write(util.URLEscape(dest, true))
		// Alt text — match abhg's policy: use the label when explicitly
		// set (i.e. node has children whose text differs from the target).
		if n.ChildCount() == 1 {
			label := nodeText(src, n.FirstChild())
			if !bytes.Equal(label, n.Target) {
				_, _ = w.WriteString(`" alt="`)
				_, _ = w.Write(util.EscapeHTML(label))
			}
		}
		_, _ = w.WriteString(`">`)
		return ast.WalkSkipChildren, nil
	}

	// Media file embeds — video/audio/PDF live in raw/ like images. Resolve
	// to /raw/ and emit the matching native element instead of falling
	// through to page transclusion (which would render a broken embed).
	if n.Embed && (isVideoExtension(target) || isAudioExtension(target) || isPDFExtension(target)) {
		dest, err := r.resolver.ResolveWikilink(n)
		if err != nil || len(dest) == 0 {
			return ast.WalkContinue, nil
		}
		writeFileEmbed(w, target, dest)
		return ast.WalkSkipChildren, nil
	}

	// Non-image embed → transclusion of another vault page.
	if n.Embed && target != "" {
		return r.renderTransclude(w, target, frag)
	}

	// Plain link — same shape abhg emits.
	dest, err := r.resolver.ResolveWikilink(n)
	if err != nil {
		return ast.WalkStop, err
	}
	if len(dest) == 0 {
		// Broken link — render contents only (abhg behavior).
		return ast.WalkContinue, nil
	}
	hasDestKeys.Store(n, struct{}{})
	_, _ = w.WriteString(`<a href="`)
	_, _ = w.Write(util.URLEscape(dest, true))
	_, _ = w.WriteString(`" class="internal">`)
	return ast.WalkContinue, nil
}

// renderTransclude emits the transclusion wrapper for `target`. Handles
// cycle / depth / missing-target cases inline; otherwise extracts the
// requested subtree from the target's cached AST and renders it.
func (r *transcludeRenderer) renderTransclude(w util.BufWriter, target, frag string) (ast.WalkStatus, error) {
	if r.transcludes == nil || r.transcludes.Cache == nil {
		// No cache available (e.g. in unit tests using renderMD directly).
		// Render as a styled link so missing transclusion is visible.
		writeBrokenTransclude(w, target, frag)
		return ast.WalkSkipChildren, nil
	}
	key := strings.ToLower(target)

	// Depth limit — emit the marker without descending.
	if r.transcludes.Depth >= MaxTranscludeDepth {
		writeOverflowTransclude(w, target, frag)
		return ast.WalkSkipChildren, nil
	}

	// Cycle — emit the marker and stop.
	if _, cycling := r.transcludes.Visited[key]; cycling {
		writeCycleTransclude(w, target, frag)
		return ast.WalkSkipChildren, nil
	}

	parsed, ok := r.transcludes.Cache[key]
	if !ok {
		writeBrokenTransclude(w, target, frag)
		return ast.WalkSkipChildren, nil
	}

	// Resolve subset: full page, section, or block.
	nodes, ok := selectSubtree(parsed.Doc, parsed.Source, frag)
	if !ok || len(nodes) == 0 {
		// Frag specified but not found in target — broken link.
		writeBrokenTransclude(w, target, frag)
		return ast.WalkSkipChildren, nil
	}

	// Recursive render: build a fresh source with a child Visited set so
	// nested ![[...]] inside the transcluded section can still resolve.
	childCtx := r.transcludes.child(target)
	// Swap our state for the duration of the child render. transcludeRenderer
	// instances are short-lived (one per top-level RenderPage), so we
	// stack-save and restore.
	savedTranscludes := r.transcludes
	r.transcludes = childCtx
	defer func() { r.transcludes = savedTranscludes }()

	title := parsed.Title
	if title == "" {
		title = parsed.Slug
	}
	canonicalURL := "/" + parsed.Slug + "/"

	_, _ = w.WriteString(`<div class="transclude" data-source="`)
	_, _ = w.Write(util.EscapeHTML([]byte(parsed.Slug)))
	_, _ = w.WriteString(`"><a class="transclude-source-link" href="`)
	_, _ = w.Write(util.EscapeHTML([]byte(canonicalURL)))
	_, _ = w.WriteString(`" hx-boost="false">From: `)
	_, _ = w.Write(util.EscapeHTML([]byte(title)))
	_, _ = w.WriteString(`</a><div class="transclude-body">`)

	for _, node := range nodes {
		if err := r.transcludes.MD.Renderer().Render(w, parsed.Source, node); err != nil {
			return ast.WalkStop, err
		}
	}
	_, _ = w.WriteString(`</div></div>`)

	return ast.WalkSkipChildren, nil
}

// isImageExtension matches the same set abhg's renderer treats as images.
// Kept here so we can switch from "is this a file" to "is this an image"
// without duplicating logic across the two embed branches.
func isImageExtension(s string) bool {
	idx := strings.LastIndexByte(s, '.')
	if idx < 0 || idx == len(s)-1 {
		return false
	}
	switch strings.ToLower(s[idx:]) {
	case ".apng", ".avif", ".gif", ".jpg", ".jpeg", ".jfif", ".pjpeg", ".pjp", ".png", ".svg", ".webp":
		return true
	}
	return false
}

// nodeText extracts plain text from a wikilink Node's children. Used for
// alt-text generation on image embeds.
func nodeText(src []byte, n ast.Node) []byte {
	var buf bytes.Buffer
	writeNodeText(src, &buf, n)
	return buf.Bytes()
}

func writeNodeText(src []byte, dst *bytes.Buffer, n ast.Node) {
	switch n := n.(type) {
	case *ast.Text:
		_, _ = dst.Write(n.Segment.Value(src))
	case *ast.String:
		_, _ = dst.Write(n.Value)
	default:
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			writeNodeText(src, dst, c)
		}
	}
}

// writeBrokenTransclude / writeCycleTransclude / writeOverflowTransclude
// emit the three error states. Each is a self-contained inline-block so
// templates and CSS can style them uniformly.

func writeBrokenTransclude(w util.BufWriter, target, frag string) {
	_, _ = w.WriteString(`<a class="internal broken transclude-missing">[[`)
	_, _ = w.Write(util.EscapeHTML([]byte(target)))
	if frag != "" {
		_, _ = w.WriteString(`#`)
		_, _ = w.Write(util.EscapeHTML([]byte(frag)))
	}
	_, _ = w.WriteString(`]]</a>`)
}

func writeCycleTransclude(w util.BufWriter, target, frag string) {
	_, _ = w.WriteString(`<div class="transclude transclude-cycle">circular: [[`)
	_, _ = w.Write(util.EscapeHTML([]byte(target)))
	if frag != "" {
		_, _ = w.WriteString(`#`)
		_, _ = w.Write(util.EscapeHTML([]byte(frag)))
	}
	_, _ = w.WriteString(`]]</div>`)
}

func writeOverflowTransclude(w util.BufWriter, target, frag string) {
	_, _ = w.WriteString(`<div class="transclude transclude-overflow">depth limit: [[`)
	_, _ = w.Write(util.EscapeHTML([]byte(target)))
	if frag != "" {
		_, _ = w.WriteString(`#`)
		_, _ = w.Write(util.EscapeHTML([]byte(frag)))
	}
	_, _ = w.WriteString(`]]</div>`)
}

// =============================================================================
// Subtree selection for transclusion
// =============================================================================

// selectSubtree returns the nodes the renderer should emit for a given
// fragment spec.
//
//   - frag == "":          the entire page body (all top-level children of doc)
//   - frag == "Some Heading": a section — the matching heading and every
//     following sibling up to (but not including) the next heading of
//     equal-or-lower depth.
//   - frag == "^block-id": the single block (paragraph, callout, list,
//     blockquote, fenced code, etc.) whose id attribute matches.
//
// Returns ok=false when a non-empty frag is specified but no match is
// found, so the caller can emit a broken-target marker instead of an
// empty transclusion.
func selectSubtree(doc ast.Node, source []byte, frag string) ([]ast.Node, bool) {
	if frag == "" {
		var out []ast.Node
		for c := doc.FirstChild(); c != nil; c = c.NextSibling() {
			out = append(out, c)
		}
		return out, true
	}
	if strings.HasPrefix(frag, "^") {
		return selectBlock(doc, frag[1:])
	}
	return selectSection(doc, source, frag)
}

// selectSection finds the heading whose slugified text matches the
// fragment, then returns it plus subsequent siblings up to the next
// heading of equal-or-lower depth (or end of document).
func selectSection(doc ast.Node, source []byte, frag string) ([]ast.Node, bool) {
	want := slugifyHeading(frag)
	var start *ast.Heading
	for c := doc.FirstChild(); c != nil; c = c.NextSibling() {
		h, ok := c.(*ast.Heading)
		if !ok {
			continue
		}
		// Prefer the auto-id attribute (set by parser.WithAutoHeadingID)
		// for consistency with anchor links.
		var anchor string
		if v, ok := h.AttributeString("id"); ok {
			anchor = string(v.([]byte))
		} else {
			anchor = slugifyHeading(string(h.Text(source))) //nolint:staticcheck
		}
		if anchor == want {
			start = h
			break
		}
	}
	if start == nil {
		return nil, false
	}
	out := []ast.Node{start}
	for c := start.NextSibling(); c != nil; c = c.NextSibling() {
		if h, ok := c.(*ast.Heading); ok && h.Level <= start.Level {
			break
		}
		out = append(out, c)
	}
	return out, true
}

// selectBlock finds the top-level block carrying the given id (set by
// blockRefTransformer). Returns just that single block.
func selectBlock(doc ast.Node, blockID string) ([]ast.Node, bool) {
	for c := doc.FirstChild(); c != nil; c = c.NextSibling() {
		if v, ok := c.AttributeString("id"); ok {
			if string(v.([]byte)) == blockID {
				return []ast.Node{c}, true
			}
		}
		// Block refs also attach to headings (see blockRefTransformer) —
		// those are covered by the top-level walk above. Anything nested
		// (inside a list item, for instance) is intentionally not
		// matched: a "block" in transclusion semantics is a top-level
		// content unit.
	}
	return nil, false
}
