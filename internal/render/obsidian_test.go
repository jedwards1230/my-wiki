package render

import (
	"bytes"
	"context"
	"strings"
	"testing"

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
			newWikilinkExtender(slugs),
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
}
