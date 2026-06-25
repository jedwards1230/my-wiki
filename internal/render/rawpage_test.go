package render

import (
	"strings"
	"testing"
	"time"
)

// TestRenderPageRawSourceFlag verifies that RenderPage marks pages compiled from
// raw/ markdown as verbatim-source imports (IsRawSource + SourceURL) so the base
// template can render the "Source" provenance badge, while leaving non-raw pages
// untouched.
func TestRenderPageRawSourceFlag(t *testing.T) {
	r, err := NewRenderer(nil)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}

	raw, err := r.RenderPage("raw/clippings/clip.md", []byte("---\ntitle: Clip\n---\nBody\n"), time.Time{})
	if err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	if !raw.IsRawSource {
		t.Error("raw/ page should have IsRawSource = true")
	}
	if raw.SourceURL != "/raw/clippings/clip.md" {
		t.Errorf("SourceURL = %q, want /raw/clippings/clip.md", raw.SourceURL)
	}
	if raw.RelativeURL != "/raw/clippings/clip/" {
		t.Errorf("RelativeURL = %q, want /raw/clippings/clip/", raw.RelativeURL)
	}

	notRaw, err := r.RenderPage("notes/note.md", []byte("# Note\n"), time.Time{})
	if err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	if notRaw.IsRawSource {
		t.Error("non-raw page should not be flagged as raw source")
	}
	if notRaw.SourceURL != "" {
		t.Errorf("non-raw SourceURL should be empty, got %q", notRaw.SourceURL)
	}

	// A generated index.md landing under raw/ is folder machinery, not an
	// authored source — it must NOT get the Source badge / view-source link,
	// so it reads like every other folder index.
	idx, err := r.RenderPage("raw/clippings/index.md", []byte("---\ntitle: Clippings\ngenerated: true\n---\nIndex of raw/clippings\n"), time.Time{})
	if err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	if idx.IsRawSource {
		t.Error("generated raw/ index.md should not be flagged as a raw source")
	}
	if idx.SourceURL != "" {
		t.Errorf("generated raw/ index SourceURL should be empty, got %q", idx.SourceURL)
	}
}

func TestFrontmatterScalar(t *testing.T) {
	src := []byte("---\ntitle: Sample\nsource: https://youtu.be/abc\ndate-added: 2026-06-06\n---\n\nBody here\n")
	cases := map[string]string{
		"title":      "Sample",
		"source":     "https://youtu.be/abc",
		"date-added": "2026-06-06",
		"missing":    "",
	}
	for key, want := range cases {
		if got := frontmatterScalar(src, key); got != want {
			t.Errorf("frontmatterScalar(%q) = %q, want %q", key, got, want)
		}
	}
	// No frontmatter at all.
	if got := frontmatterScalar([]byte("no frontmatter\nsource: x\n"), "source"); got != "" {
		t.Errorf("expected empty for body-only source line, got %q", got)
	}
	// Quoted value.
	if got := frontmatterScalar([]byte("---\nsource: \"https://x\"\n---\n"), "source"); got != "https://x" {
		t.Errorf("quoted value not unquoted: %q", got)
	}
}

func TestRawBreadcrumb(t *testing.T) {
	items := rawBreadcrumb("/raw/clippings/sample.md")
	if len(items) != 4 {
		t.Fatalf("expected 4 crumbs, got %d: %+v", len(items), items)
	}
	if items[0].Label != "Home" || items[0].URL != "/" {
		t.Errorf("first crumb should be Home→/, got %+v", items[0])
	}
	// Intermediate crumbs link to /raw/ directory listings (trailing slash).
	if items[1].URL != "/raw/" {
		t.Errorf("raw crumb URL = %q, want /raw/", items[1].URL)
	}
	if items[2].URL != "/raw/clippings/" {
		t.Errorf("clippings crumb URL = %q, want /raw/clippings/", items[2].URL)
	}
	last := items[len(items)-1]
	if !last.Last || last.Label != "sample.md" {
		t.Errorf("last crumb = %+v, want sample.md marked Last", last)
	}
}

func TestRawSourceBanner(t *testing.T) {
	// With an http source: includes the Original link and the View raw link.
	out := string(rawSourceBanner("/raw/clippings/x.md", "https://youtu.be/abc"))
	for _, want := range []string{`class="raw-source-banner"`, `https://youtu.be/abc`, `/raw/clippings/x.md?raw=1`, "View raw"} {
		if !strings.Contains(out, want) {
			t.Errorf("banner missing %q in:\n%s", want, out)
		}
	}
	// Non-URL source value must NOT be linked as Original.
	out = string(rawSourceBanner("/raw/x.md", "vault:some/path"))
	if strings.Contains(out, "Original") {
		t.Errorf("non-URL source should not produce an Original link:\n%s", out)
	}
	// Empty source: no Original link, still has View raw.
	out = string(rawSourceBanner("/raw/x.md", ""))
	if strings.Contains(out, "Original") {
		t.Errorf("empty source should not produce Original link:\n%s", out)
	}
	if !strings.Contains(out, "?raw=1") {
		t.Errorf("View raw link missing:\n%s", out)
	}
}

func TestIsHTTPURL(t *testing.T) {
	cases := map[string]bool{
		"https://x.com": true,
		"http://x.com":  true,
		"ftp://x":       false,
		"vault:path":    false,
		"/raw/local.md": false,
		"":              false,
	}
	for in, want := range cases {
		if got := isHTTPURL(in); got != want {
			t.Errorf("isHTTPURL(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestRawDirTitle(t *testing.T) {
	cases := map[string]string{
		"/raw/":           "Raw",
		"/raw/clippings/": "Clippings",
		"/raw/a/b-c/":     "B C",
		"/raw/gists":      "Gists",
	}
	for in, want := range cases {
		if got := rawDirTitle(in); got != want {
			t.Errorf("rawDirTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildRawGallery(t *testing.T) {
	t.Run("media + non-media assets → Gallery + Files", func(t *testing.T) {
		out, toc := buildRawGallery("/raw/clippings/", []RawAsset{
			{Name: "photo.png"},
			{Name: "clip.mp4"},
			{Name: "talk.pdf"},
			{Name: "notes.txt"},
			{Name: "archive.zip"},
		})
		s := string(out)

		// Gallery section: image thumbnail + non-image media badge.
		if !strings.Contains(s, `<h2 id="gallery">Gallery</h2>`) {
			t.Errorf("missing Gallery heading:\n%s", s)
		}
		if !strings.Contains(s, `<img loading="lazy" src="/raw/clippings/photo.png"`) {
			t.Errorf("Gallery missing image thumbnail:\n%s", s)
		}
		if !strings.Contains(s, "MP4") || !strings.Contains(s, "PDF") {
			t.Errorf("Gallery missing media badges:\n%s", s)
		}

		// Files section: non-media assets as a plain link list.
		if !strings.Contains(s, `<h2 id="files">Files</h2>`) {
			t.Errorf("missing Files heading:\n%s", s)
		}
		for _, want := range []string{
			`<a class="internal" href="/raw/clippings/notes.txt">notes.txt</a>`,
			`<a class="internal" href="/raw/clippings/archive.zip">archive.zip</a>`,
		} {
			if !strings.Contains(s, want) {
				t.Errorf("Files missing %q:\n%s", want, s)
			}
		}

		// TOC lists both sections, in order, for the right-rail "On this page".
		if len(toc) != 2 || toc[0].Anchor != "gallery" || toc[1].Anchor != "files" {
			t.Errorf("toc = %+v, want [gallery, files]", toc)
		}
	})

	t.Run("media only → Gallery only", func(t *testing.T) {
		out, toc := buildRawGallery("/raw/photos/", []RawAsset{{Name: "a.png"}, {Name: "b.jpg"}})
		s := string(out)
		if !strings.Contains(s, `<h2 id="gallery">Gallery</h2>`) {
			t.Errorf("missing Gallery heading:\n%s", s)
		}
		if strings.Contains(s, "Files") {
			t.Errorf("no non-media assets, but a Files section was rendered:\n%s", s)
		}
		if len(toc) != 1 || toc[0].Anchor != "gallery" {
			t.Errorf("toc = %+v, want [gallery] only", toc)
		}
	})

	t.Run("empty directory", func(t *testing.T) {
		out, toc := buildRawGallery("/raw/empty/", nil)
		if !strings.Contains(string(out), "empty") {
			t.Errorf("expected empty-directory note:\n%s", out)
		}
		if len(toc) != 0 {
			t.Errorf("toc = %+v, want empty", toc)
		}
	})
}

func TestRawIndexDescription(t *testing.T) {
	cases := map[string]string{
		"/raw/":           "Index of raw",
		"/raw/clippings/": "Index of raw/clippings",
		"/raw/a/b/":       "Index of raw/a/b",
	}
	for in, want := range cases {
		if got := rawIndexDescription(in); got != want {
			t.Errorf("rawIndexDescription(%q) = %q, want %q", in, got, want)
		}
	}
}

// markActiveByURL marks the active /raw/ landing/page node in the explorer tree;
// it backs both the RenderRawGallery fallback and the on-demand RenderRawPage
// fallback, whose pages carry an empty Slug that the slug-based markActive can't
// match.
func TestMarkActiveByURL(t *testing.T) {
	leaf := &ExplorerNode{Name: "clip.md", URL: "/raw/clippings/youtube/clip.md"}
	yt := &ExplorerNode{Name: "Youtube", URL: "/raw/clippings/youtube/", IsFolder: true, Children: []*ExplorerNode{leaf}}
	clippings := &ExplorerNode{Name: "Clippings", URL: "/raw/clippings/", IsFolder: true, Children: []*ExplorerNode{yt}}
	gists := &ExplorerNode{Name: "Gists", URL: "/raw/gists/", IsFolder: true}
	root := &ExplorerNode{Name: "Raw", URL: "/raw/", IsFolder: true, Children: []*ExplorerNode{clippings, gists}}

	roots := []*ExplorerNode{root}
	if !markActiveByURL(roots, "/raw/clippings/youtube/clip.md") {
		t.Fatal("expected markActiveByURL to match the clip")
	}
	if !root.IsOpen || !clippings.IsOpen || !yt.IsOpen {
		t.Error("ancestor folders should be open")
	}
	if !leaf.IsActive {
		t.Error("clip.md leaf should be active")
	}
	if gists.IsOpen {
		t.Error("sibling folder (Gists) should stay closed")
	}
}
