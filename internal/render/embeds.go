package render

// embeds.go holds rich-media embedding: YouTube/Vimeo iframe embeds derived
// from standard markdown image syntax (`![](https://youtu.be/ID)`, the form
// Obsidian auto-embeds), plus the file-extension classifiers used by the
// wikilink renderer to emit <video>/<audio>/<iframe> for `![[clip.mp4]]`
// style embeds. Kept separate from obsidian.go so the media layer is easy to
// find and extend with new providers.

import (
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// =============================================================================
// File-type classifiers (shared by the wikilink embed renderer)
// =============================================================================

// isVideoExtension reports whether s ends in a browser-playable video
// container. Used so `![[clip.mp4]]` renders a <video> element.
func isVideoExtension(s string) bool {
	switch fileExt(s) {
	case ".mp4", ".webm", ".ogv", ".mov", ".m4v":
		return true
	}
	return false
}

// isAudioExtension reports whether s ends in a browser-playable audio file.
func isAudioExtension(s string) bool {
	switch fileExt(s) {
	case ".mp3", ".ogg", ".oga", ".wav", ".flac", ".m4a", ".aac":
		return true
	}
	return false
}

// isPDFExtension reports whether s is a PDF, embedded via an inline <iframe>
// viewer rather than a download link.
func isPDFExtension(s string) bool {
	return fileExt(s) == ".pdf"
}

// fileExt returns the lowercased extension (including the dot) of s, or ""
// when there is none. Mirrors the guard isImageExtension uses.
func fileExt(s string) string {
	idx := strings.LastIndexByte(s, '.')
	if idx < 0 || idx == len(s)-1 {
		return ""
	}
	return strings.ToLower(s[idx:])
}

// =============================================================================
// YouTube / Vimeo embeds (markdown image syntax → iframe)
// =============================================================================

// youtubeRe captures the 11-char video id from the common YouTube URL shapes:
// watch?v=, youtu.be/, embed/, v/, shorts/.
var youtubeRe = regexp.MustCompile(`(?i)^(?:https?://)?(?:www\.|m\.)?(?:youtube\.com/(?:watch\?(?:[^ ]*&)?v=|embed/|v/|shorts/|live/)|youtu\.be/)([A-Za-z0-9_-]{11})`)

// vimeoRe captures the numeric id from a Vimeo URL.
var vimeoRe = regexp.MustCompile(`(?i)^(?:https?://)?(?:www\.)?vimeo\.com/(?:video/)?(\d+)`)

// startParamRe pulls a start offset (?t=90, ?start=90, &t=1m30s) out of a
// YouTube URL so deep-links keep their timestamp.
var startParamRe = regexp.MustCompile(`(?i)[?&](?:t|start)=([0-9hms]+)`)

// embedFromURL maps a media URL to (iframe src, provider kind). Returns
// ok=false when the URL isn't a recognized embeddable provider.
func embedFromURL(raw string) (src, kind string, ok bool) {
	if m := youtubeRe.FindStringSubmatch(raw); m != nil {
		id := m[1]
		src = "https://www.youtube-nocookie.com/embed/" + id
		if ts := startParamRe.FindStringSubmatch(raw); ts != nil {
			if secs := parseYouTubeStart(ts[1]); secs != "" {
				src += "?start=" + secs
			}
		}
		return src, "youtube", true
	}
	if m := vimeoRe.FindStringSubmatch(raw); m != nil {
		return "https://player.vimeo.com/video/" + m[1], "vimeo", true
	}
	return "", "", false
}

// parseYouTubeStart normalizes a YouTube start offset to a plain seconds
// string. Accepts bare seconds ("90") and the 1h2m3s shorthand. Returns ""
// when the value is empty or unparseable, so the caller omits ?start.
func parseYouTubeStart(v string) string {
	if v == "" {
		return ""
	}
	// Bare seconds.
	if !strings.ContainsAny(v, "hms") {
		return v
	}
	var total, cur int
	for _, c := range v {
		switch {
		case c >= '0' && c <= '9':
			cur = cur*10 + int(c-'0')
		case c == 'h':
			total += cur * 3600
			cur = 0
		case c == 'm':
			total += cur * 60
			cur = 0
		case c == 's':
			total += cur
			cur = 0
		}
	}
	total += cur
	if total == 0 {
		return ""
	}
	return itoa(total)
}

// itoa avoids pulling strconv in for a single small positive int.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// writeFileEmbed emits the native HTML element for a file embed targeting a
// raw/ asset. `target` classifies the kind; `dest` is the resolved /raw/ URL.
func writeFileEmbed(w util.BufWriter, target string, dest []byte) {
	esc := util.URLEscape(dest, true)
	switch {
	case isVideoExtension(target):
		_, _ = w.WriteString(`<span class="embed embed-file embed-video"><video class="embed-frame" controls preload="metadata" src="`)
		_, _ = w.Write(esc)
		_, _ = w.WriteString(`"></video></span>`)
	case isAudioExtension(target):
		_, _ = w.WriteString(`<span class="embed embed-file embed-audio"><audio class="embed-frame" controls preload="metadata" src="`)
		_, _ = w.Write(esc)
		_, _ = w.WriteString(`"></audio></span>`)
	case isPDFExtension(target):
		_, _ = w.WriteString(`<span class="embed embed-file embed-pdf"><iframe class="embed-frame" src="`)
		_, _ = w.Write(esc)
		_, _ = w.WriteString(`" title="`)
		_, _ = w.Write(util.EscapeHTML([]byte(target)))
		_, _ = w.WriteString(`" loading="lazy"></iframe></span>`)
	}
}

// mediaEmbedNode is the AST node for a provider video embed. Inline so it can
// stand in for the *ast.Image it replaces without breaking the surrounding
// paragraph's HTML validity (the renderer emits a <span> wrapper).
type mediaEmbedNode struct {
	ast.BaseInline
	src   string
	kind  string
	title string
}

var kindMediaEmbed = ast.NewNodeKind("MediaEmbed")

func (n *mediaEmbedNode) Kind() ast.NodeKind         { return kindMediaEmbed }
func (n *mediaEmbedNode) Dump(src []byte, level int) { ast.DumpHelper(n, src, level, nil, nil) }

// mediaEmbedTransformer rewrites markdown images whose destination is a
// recognized video provider URL into mediaEmbedNode. This matches Obsidian's
// behavior of auto-embedding `![](youtube-url)`.
type mediaEmbedTransformer struct{}

func (t *mediaEmbedTransformer) Transform(node *ast.Document, reader text.Reader, _ parser.Context) {
	source := reader.Source()
	type repl struct {
		parent ast.Node
		old    ast.Node
		new    ast.Node
	}
	var repls []repl
	_ = ast.Walk(node, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		img, ok := n.(*ast.Image)
		if !ok {
			return ast.WalkContinue, nil
		}
		src, kind, ok := embedFromURL(string(img.Destination))
		if !ok {
			return ast.WalkContinue, nil
		}
		title := string(img.Text(source)) //nolint:staticcheck // plain alt-text extraction
		repls = append(repls, repl{parent: n.Parent(), old: n, new: &mediaEmbedNode{src: src, kind: kind, title: title}})
		return ast.WalkSkipChildren, nil
	})
	for _, r := range repls {
		if r.parent != nil {
			r.parent.ReplaceChild(r.parent, r.old, r.new)
		}
	}
}

// mediaEmbedRenderer emits a responsive iframe wrapper. The <span> wrapper
// keeps the output valid inside a paragraph; CSS promotes it to a block with
// a 16:9 aspect ratio.
type mediaEmbedRenderer struct{}

func (r *mediaEmbedRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindMediaEmbed, r.render)
}

func (r *mediaEmbedRenderer) render(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	n := node.(*mediaEmbedNode)
	_, _ = w.WriteString(`<span class="embed embed-video embed-`)
	_, _ = w.WriteString(n.kind)
	_, _ = w.WriteString(`"><iframe class="embed-frame" src="`)
	_, _ = w.Write(util.EscapeHTML([]byte(n.src)))
	_, _ = w.WriteString(`" title="`)
	if n.title != "" {
		_, _ = w.Write(util.EscapeHTML([]byte(n.title)))
	} else {
		_, _ = w.WriteString("Embedded video")
	}
	_, _ = w.WriteString(`" loading="lazy" referrerpolicy="strict-origin-when-cross-origin" allow="accelerometer; autoplay; clipboard-write; encrypted-media; gyroscope; picture-in-picture; web-share" allowfullscreen></iframe></span>`)
	return ast.WalkSkipChildren, nil
}

// mediaEmbedExtension registers the provider-embed transformer + renderer.
// Wired into newMarkdown alongside obsidianExtension.
type mediaEmbedExtension struct{}

func (e *mediaEmbedExtension) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(
		parser.WithASTTransformers(
			util.Prioritized(&mediaEmbedTransformer{}, 99),
		),
	)
	m.Renderer().AddOptions(
		renderer.WithNodeRenderers(
			util.Prioritized(&mediaEmbedRenderer{}, 99),
		),
	)
}
