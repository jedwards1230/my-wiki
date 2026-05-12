package render

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jedwards1230/my-wiki/internal/memfs"
	"github.com/jedwards1230/my-wiki/internal/vault"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

// renderMD spins up a minimal goldmark with just the obsidian extension
// and runs `src` through it. The wikilink resolver is wired with `slugs`
// so tests can exercise resolved + broken links.
func renderMD(t *testing.T, src string, slugs map[string]string) string {
	t.Helper()
	md := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			&obsidianExtension{},
			newWikilinkExtender(slugs, nil),
		),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(html.WithUnsafe()),
	)
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		t.Fatalf("convert: %v", err)
	}
	return buf.String()
}

func TestHighlight(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"basic", "Hello ==world==.", "<mark>world</mark>"},
		{"multi", "==a== and ==b==", "<mark>a</mark>"},
		{"missing close", "==unterminated", "==unterminated"},
		{"empty pair", "x ==aa== y", "<mark>aa</mark>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := renderMD(t, c.in, nil)
			if !strings.Contains(out, c.want) {
				t.Errorf("highlight: %q missing in %q", c.want, out)
			}
		})
	}
}

func TestComment(t *testing.T) {
	out := renderMD(t, "Visible %%hidden%% rest.", nil)
	if strings.Contains(out, "hidden") {
		t.Errorf("expected comment to be dropped, got %q", out)
	}
	if !strings.Contains(out, "Visible") || !strings.Contains(out, "rest") {
		t.Errorf("expected surrounding text retained, got %q", out)
	}
}

func TestInlineMath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"basic", "Pythagoras: $a^2 + b^2 = c^2$.", `<span class="math-inline">$a^2 + b^2 = c^2$</span>`},
		{"with escape", "$\\$ symbol$", `<span class="math-inline">`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := renderMD(t, c.in, nil)
			if !strings.Contains(out, c.want) {
				t.Errorf("inline math: %q missing in %q", c.want, out)
			}
		})
	}
}

func TestBlockMath(t *testing.T) {
	src := "$$\nE = mc^2\n$$\n"
	out := renderMD(t, src, nil)
	if !strings.Contains(out, `<div class="math-display">$$`) {
		t.Errorf("block math wrapper missing in %q", out)
	}
	if !strings.Contains(out, "E = mc^2") {
		t.Errorf("block math content missing in %q", out)
	}
}

func TestCallouts(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantKind string
		wantText string
	}{
		{"note", "> [!note] Hello\n> body\n", "callout-note", "Hello"},
		{"warning fold open", "> [!warning]- Title\n> body\n", "callout-warning", "Title"},
		{"warning fold collapsed", "> [!warning]+ Title\n> body\n", "is-collapsed", "Title"},
		{"unknown kind ignored", "> [!totally-bogus] x\n> body\n", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := renderMD(t, c.in, nil)
			if c.wantKind == "" {
				// Should remain a plain blockquote
				if !strings.Contains(out, "<blockquote>") {
					t.Errorf("expected plain blockquote, got %q", out)
				}
				return
			}
			if !strings.Contains(out, c.wantKind) {
				t.Errorf("callout class %q missing in %q", c.wantKind, out)
			}
			if c.wantText != "" && !strings.Contains(out, c.wantText) {
				t.Errorf("callout title %q missing in %q", c.wantText, out)
			}
		})
	}
}

func TestWikilinkResolution(t *testing.T) {
	slugs := map[string]string{
		"alpha":       "notes/alpha",
		"notes/alpha": "notes/alpha",
	}
	cases := []struct {
		name string
		in   string
		want string
		miss string // text expected absent
	}{
		{"resolved", "See [[alpha]].", `href="/notes/alpha/"`, ""},
		{"alias", "See [[alpha|Custom]].", `>Custom<`, ""},
		{"heading frag", "See [[alpha#Heading One]].", `href="/notes/alpha/#heading-one"`, ""},
		// broken link: resolver returns nil → contents render plain, the
		// abhg renderer drops the link tags entirely.
		{"broken", "See [[doesnotexist]].", "doesnotexist", `href="/doesnotexist`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := renderMD(t, c.in, slugs)
			if !strings.Contains(out, c.want) {
				t.Errorf("missing %q in %q", c.want, out)
			}
			if c.miss != "" && strings.Contains(out, c.miss) {
				t.Errorf("unexpected %q in %q", c.miss, out)
			}
		})
	}
}

// Wikilinks to the home page must resolve to "/" (not "/index/") so the
// site has a single canonical home URL that agrees with RenderPage and
// the server's index.html mount point.
func TestWikilinkHomePage(t *testing.T) {
	slugs := map[string]string{"index": "index"}
	out := renderMD(t, "Back to [[index]].", slugs)
	if !strings.Contains(out, `href="/"`) {
		t.Errorf("expected wikilink to home to render as href=\"/\" in %q", out)
	}
	if strings.Contains(out, `href="/index/"`) {
		t.Errorf("home wikilink should not use /index/ in %q", out)
	}
}

func TestEmbedImage(t *testing.T) {
	slugs := map[string]string{}
	out := renderMD(t, "![[fixtures/photo.png]]", slugs)
	if !strings.Contains(out, `/raw/fixtures/photo.png`) {
		t.Errorf("expected /raw/ image src in %q", out)
	}
}

func TestBlockRef(t *testing.T) {
	src := "Para text. ^my-anchor\n"
	out := renderMD(t, src, nil)
	if !strings.Contains(out, `id="my-anchor"`) {
		t.Errorf("block ref id missing in %q", out)
	}
	if strings.Contains(out, "^my-anchor") {
		t.Errorf("block ref marker not stripped from output: %q", out)
	}
}

// RenderPage must normalize frontmatter tags to the Page.Tags contract:
// lowercase + sorted + deduplicated. Also asserts the home-page URL
// override.
func TestRenderPageTagsAndHomeURL(t *testing.T) {
	r, err := NewRenderer(nil)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	src := []byte("---\ntitle: Home\ntags: [Zeta, alpha, Beta, alpha]\n---\nbody\n")
	p, err := r.RenderPage("index.md", src, time.Time{})
	if err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	wantTags := []string{"alpha", "beta", "zeta"}
	if len(p.Tags) != len(wantTags) {
		t.Fatalf("tags = %v, want %v", p.Tags, wantTags)
	}
	for i, w := range wantTags {
		if p.Tags[i] != w {
			t.Errorf("tags[%d] = %q, want %q (full: %v)", i, p.Tags[i], w, p.Tags)
		}
	}
	if p.RelativeURL != "/" {
		t.Errorf("home page RelativeURL = %q, want \"/\"", p.RelativeURL)
	}

	// Non-home page should keep the /{slug}/ form.
	q, err := r.RenderPage("notes/foo.md", []byte("# foo\n"), time.Time{})
	if err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	if q.RelativeURL != "/notes/foo/" {
		t.Errorf("nested page RelativeURL = %q, want \"/notes/foo/\"", q.RelativeURL)
	}
}

func TestSlugifyHeading(t *testing.T) {
	cases := map[string]string{
		"Heading One":    "heading-one",
		"With Numbers 1": "with-numbers-1",
		"  Spaces  ":     "spaces",
		"Hyphen-Already": "hyphen-already",
	}
	for in, want := range cases {
		if got := slugifyHeading(in); got != want {
			t.Errorf("slugifyHeading(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildEndToEnd(t *testing.T) {
	v := vault.New("testdata/vault")
	b := NewBuilder(BuilderConfig{
		Vault:     v,
		SiteTitle: "Test Wiki",
		BaseURL:   "https://wiki.test",
	})
	snap, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if snap == nil {
		t.Fatal("nil snapshot")
	}
	if snap.Files() == 0 {
		t.Fatal("snapshot has 0 files")
	}
	// Expect home page, two notes, schema, sitemap, rss.
	fs := memfs.New()
	fs.Store(snap)
	mustHave := []string{"index.html", "notes/alpha/index.html", "notes/beta/index.html", "meta/schema/index.html", "sitemap.xml", "index.xml", "404.html"}
	for _, key := range mustHave {
		f, err := fs.Open(key)
		if err != nil {
			t.Errorf("missing %s: %v", key, err)
			continue
		}
		_ = f.Close()
	}

	// Tag pages should exist for tag "example".
	if _, err := fs.Open("tags/example/index.html"); err != nil {
		t.Errorf("missing tags/example/index.html: %v", err)
	}

	// Backlinks: alpha is linked from beta + schema → at least one entry.
	bl := b.BacklinkIndex().Lookup("notes/alpha")
	if len(bl) == 0 {
		t.Errorf("expected backlinks for notes/alpha, got none")
	}

	// PageBySlug round-trip.
	if p := b.PageBySlug("notes/alpha"); p == nil {
		t.Errorf("PageBySlug returned nil for notes/alpha")
	} else if p.Title != "Alpha Note" {
		t.Errorf("title = %q, want %q", p.Title, "Alpha Note")
	}

	// Transclusion (added to fixture vault): index.md has ![[notes/alpha]]
	// which should expand into the alpha body inside a transclude wrapper.
	f, err := fs.Open("index.html")
	if err != nil {
		t.Fatalf("missing index.html: %v", err)
	}
	indexBody, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		t.Errorf("read index.html: %v", err)
	}
	body := string(indexBody)
	if !strings.Contains(body, `class="transclude"`) {
		t.Errorf("expected transclude wrapper in home page, got:\n%s", body)
	}
	if !strings.Contains(body, `data-source="notes/alpha"`) {
		t.Errorf("expected data-source=notes/alpha attribute, got:\n%s", body)
	}
	// alpha body contains "==highlighted==" — should appear inside
	// the transclude wrapper after recursive render.
	if !strings.Contains(body, "<mark>highlighted</mark>") {
		t.Errorf("expected transcluded alpha body content in home page")
	}
}

// --- Transclusion-specific table tests ---------------------------------------

// buildTranscludeRenderer returns a Renderer with a 2-page AST cache
// suitable for exercising transclusion paths. host is the page whose
// body holds the ![[…]] under test; target is the page being included.
//
// Returns the renderer plus a function that runs `host` through the
// render pipeline and returns the rendered HTML body so each test can
// pattern-match without re-implementing the parse+render dance.
func buildTranscludeRenderer(t *testing.T, host string, targets map[string]string) (*Renderer, func() string) {
	t.Helper()
	slugs := map[string]string{}
	for slug := range targets {
		slugs[strings.ToLower(slug)] = slug
	}
	slugs["host"] = "host"
	r, err := NewRenderer(slugs)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	cache := map[string]*ParsedPage{}
	titles := map[string]string{}
	for slug, src := range targets {
		pp, _, _ := r.ParsePage(slug+".md", []byte(src))
		key := strings.ToLower(slug)
		cache[key] = pp
		titles[key] = pp.Title
	}
	// host page parsed too — needed for cycle tests that include the
	// host's own slug in transclusions.
	hostParsed, _, _ := r.ParsePage("host.md", []byte(host))
	cache["host"] = hostParsed
	titles["host"] = hostParsed.Title
	r.WithTransclusion(cache, titles)
	return r, func() string {
		page, err := r.RenderPage("host.md", []byte(host), time.Time{})
		if err != nil {
			t.Fatalf("RenderPage host: %v", err)
		}
		return string(page.ContentHTML)
	}
}

func TestTransclude_FullPage(t *testing.T) {
	host := "Intro: ![[other]] tail."
	_, run := buildTranscludeRenderer(t, host, map[string]string{
		"other": "---\ntitle: Other Page\n---\n\nFull body with ==mark==.\n",
	})
	out := run()
	if !strings.Contains(out, `class="transclude"`) {
		t.Errorf("missing transclude wrapper: %s", out)
	}
	if !strings.Contains(out, `data-source="other"`) {
		t.Errorf("missing data-source: %s", out)
	}
	if !strings.Contains(out, "<mark>mark</mark>") {
		t.Errorf("missing transcluded body: %s", out)
	}
	if !strings.Contains(out, `From: Other Page`) {
		t.Errorf("missing source link label: %s", out)
	}
}

func TestTransclude_Section(t *testing.T) {
	host := "![[other#Second]]"
	target := `---
title: Other
---

## First

first body.

## Second

second body with ==mark==.

## Third

third body.
`
	_, run := buildTranscludeRenderer(t, host, map[string]string{"other": target})
	out := run()
	if !strings.Contains(out, "second body") {
		t.Errorf("section transclusion missed target section: %s", out)
	}
	if strings.Contains(out, "first body") {
		t.Errorf("section transclusion leaked prior section into output: %s", out)
	}
	if strings.Contains(out, "third body") {
		t.Errorf("section transclusion leaked following section into output: %s", out)
	}
	// Heading depth-preservation: the Second heading is at level 2,
	// rendered as <h2>; we don't assert exact level (Quartz parity is
	// "leave heading levels alone") but the word should appear in some
	// heading tag.
	if !strings.Contains(out, "Second") {
		t.Errorf("section heading text missing: %s", out)
	}
}

func TestTransclude_Block(t *testing.T) {
	host := "![[other#^my-block]]"
	target := `---
title: Other
---

unrelated paragraph.

paragraph with the right id. ^my-block

another unrelated paragraph.
`
	_, run := buildTranscludeRenderer(t, host, map[string]string{"other": target})
	out := run()
	if !strings.Contains(out, "paragraph with the right id") {
		t.Errorf("block transclusion missed target block: %s", out)
	}
	if strings.Contains(out, "unrelated paragraph") || strings.Contains(out, "another unrelated paragraph") {
		t.Errorf("block transclusion leaked siblings: %s", out)
	}
}

func TestTransclude_MissingTarget(t *testing.T) {
	host := "![[does-not-exist]]"
	_, run := buildTranscludeRenderer(t, host, nil)
	out := run()
	if !strings.Contains(out, "transclude-missing") {
		t.Errorf("expected transclude-missing marker for unknown target: %s", out)
	}
}

func TestTransclude_Cycle(t *testing.T) {
	// A.md includes B; B includes A. When B is rendered for inclusion
	// inside A, B's nested ![[A]] should hit the visited set and emit
	// the cycle marker.
	hostA := "A intro. ![[b]] A tail."
	bSource := "B body. ![[a]] B tail."
	r, err := NewRenderer(map[string]string{"a": "a", "b": "b"})
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	cache := map[string]*ParsedPage{}
	titles := map[string]string{}
	for slug, src := range map[string]string{"a": hostA, "b": bSource} {
		pp, _, _ := r.ParsePage(slug+".md", []byte(src))
		cache[slug] = pp
		titles[slug] = pp.Title
	}
	r.WithTransclusion(cache, titles)
	page, err := r.RenderPage("a.md", []byte(hostA), time.Time{})
	if err != nil {
		t.Fatalf("render A: %v", err)
	}
	body := string(page.ContentHTML)
	if !strings.Contains(body, "transclude-cycle") {
		t.Errorf("expected cycle marker rendering A → B → A: %s", body)
	}
}

func TestTransclude_DepthLimit(t *testing.T) {
	// Chain: A → B → C → D → E. Default MaxTranscludeDepth=3, so when
	// rendering A, D's ![[E]] should hit the depth limit (A=0, B=1,
	// C=2, D=3 → child render at depth 3 emits overflow marker).
	r, err := NewRenderer(map[string]string{"a": "a", "b": "b", "c": "c", "d": "d", "e": "e"})
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	sources := map[string]string{
		"a": "A. ![[b]]",
		"b": "B. ![[c]]",
		"c": "C. ![[d]]",
		"d": "D. ![[e]]",
		"e": "E leaf.",
	}
	cache := map[string]*ParsedPage{}
	titles := map[string]string{}
	for slug, src := range sources {
		pp, _, _ := r.ParsePage(slug+".md", []byte(src))
		cache[slug] = pp
		titles[slug] = pp.Title
	}
	r.WithTransclusion(cache, titles)
	page, err := r.RenderPage("a.md", []byte(sources["a"]), time.Time{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(page.ContentHTML)
	if !strings.Contains(body, "transclude-overflow") {
		t.Errorf("expected depth-limit marker, got:\n%s", body)
	}
	// E's body should NOT appear — depth limit cut off at D's inclusion of E.
	if strings.Contains(body, "E leaf") {
		t.Errorf("depth limit did not stop descent: %s", body)
	}
}
