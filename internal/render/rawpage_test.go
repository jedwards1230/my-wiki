package render

import (
	"fmt"
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

func TestBuildRawIndex(t *testing.T) {
	t.Run("media + md children + recents → all sections", func(t *testing.T) {
		out, toc := buildRawIndex("/raw/clippings/", RawIndexData{
			Children: []RawDirEntry{
				{Name: "youtube", IsDir: true},
				{Name: "great-clip.md", Title: "Great Clip"},
				{Name: "untitled.md"}, // no resolved title → humanized filename
				{Name: "photo.png"},
				{Name: "clip.mp4"},
				{Name: "notes.txt"},
			},
			Recents: []RawRecentEntry{
				{RawURL: "/raw/clippings/youtube/clip/", Title: "Embedded Clip", ModTime: time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)},
				{RawURL: "/raw/clippings/great-clip/", Title: "Great Clip", ModTime: time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC)},
			},
		})
		s := string(out)

		// Recently Updated section: each descendant page links to its rendered URL.
		if !strings.Contains(s, `<h2 id="recently-updated">Recently Updated</h2>`) {
			t.Errorf("missing Recently Updated heading:\n%s", s)
		}
		if !strings.Contains(s, `<a class="internal" href="/raw/clippings/youtube/clip/">Embedded Clip</a> — 2026-06-20`) {
			t.Errorf("Recently Updated missing dated entry:\n%s", s)
		}

		// Directory section: a standard bulleted internal-link list. Markdown
		// children link to their rendered /raw/.../ page using the resolved title.
		if !strings.Contains(s, `<h2 id="directory">Directory</h2>`) {
			t.Errorf("missing Directory heading:\n%s", s)
		}
		for _, want := range []string{
			`<a class="internal" href="/raw/clippings/youtube/">youtube/</a>`,
			`<a class="internal" href="/raw/clippings/great-clip/">Great Clip</a>`,
			`<a class="internal" href="/raw/clippings/untitled/">Untitled</a>`, // humanized fallback
			`<a class="internal" href="/raw/clippings/photo.png">photo.png</a>`,
			`<a class="internal" href="/raw/clippings/notes.txt">notes.txt</a>`,
		} {
			if !strings.Contains(s, want) {
				t.Errorf("Directory missing %q:\n%s", want, s)
			}
		}
		if strings.Contains(s, "raw-list") || strings.Contains(s, "raw-row") {
			t.Errorf("Directory should use plain article-body list, not custom raw-list:\n%s", s)
		}

		// Gallery section: image thumbnail + non-image media badge; markdown is
		// NOT a gallery asset.
		if !strings.Contains(s, `<h2 id="gallery">Gallery</h2>`) {
			t.Errorf("missing Gallery heading:\n%s", s)
		}
		if !strings.Contains(s, `<img loading="lazy" src="/raw/clippings/photo.png"`) {
			t.Errorf("Gallery missing image thumbnail:\n%s", s)
		}
		if !strings.Contains(s, "MP4") {
			t.Errorf("Gallery missing video badge:\n%s", s)
		}

		// TOC lists all three sections, in order, for the right-rail "On this page".
		if len(toc) != 3 || toc[0].Anchor != "recently-updated" || toc[1].Anchor != "directory" || toc[2].Anchor != "gallery" {
			t.Errorf("toc = %+v, want [recently-updated, directory, gallery]", toc)
		}
	})

	t.Run("no media / no recents → Directory only", func(t *testing.T) {
		out, toc := buildRawIndex("/raw/", RawIndexData{
			Children: []RawDirEntry{
				{Name: "clippings", IsDir: true},
				{Name: "readme.txt", IsDir: false},
			},
		})
		s := string(out)
		if !strings.Contains(s, `<h2 id="directory">Directory</h2>`) {
			t.Errorf("missing Directory heading:\n%s", s)
		}
		if strings.Contains(s, "Recently Updated") {
			t.Errorf("no recents, but a Recently Updated section was rendered:\n%s", s)
		}
		if strings.Contains(s, "Gallery") || strings.Contains(s, "raw-gallery") {
			t.Errorf("no media, but a Gallery section was rendered:\n%s", s)
		}
		if len(toc) != 1 || toc[0].Anchor != "directory" {
			t.Errorf("toc = %+v, want [directory] only", toc)
		}
	})

	t.Run("empty directory", func(t *testing.T) {
		out, _ := buildRawIndex("/raw/empty/", RawIndexData{})
		if !strings.Contains(string(out), "empty") {
			t.Errorf("expected empty-directory note:\n%s", out)
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

func TestRawIndexTitle(t *testing.T) {
	if got := RawIndexTitle("great-clip.md", []byte("---\ntitle: Great Clip\n---\nbody")); got != "Great Clip" {
		t.Errorf("frontmatter title = %q, want Great Clip", got)
	}
	if got := RawIndexTitle("some-clip.md", []byte("no frontmatter\n")); got != "Some Clip" {
		t.Errorf("fallback title = %q, want Some Clip", got)
	}
}

func TestSelectRawRecents(t *testing.T) {
	mk := func(url string, day int) RawRecentEntry {
		return RawRecentEntry{RawURL: url, ModTime: time.Date(2026, 6, day, 0, 0, 0, 0, time.UTC)}
	}

	t.Run("non-root gated below minimum", func(t *testing.T) {
		small := []RawRecentEntry{mk("/raw/d/a/", 1), mk("/raw/d/b/", 2)}
		if got := SelectRawRecents(small, false); got != nil {
			t.Errorf("small non-root subtree should yield no recents, got %+v", got)
		}
	})

	t.Run("root always renders recents", func(t *testing.T) {
		small := []RawRecentEntry{mk("/raw/a/", 1), mk("/raw/b/", 2)}
		got := SelectRawRecents(small, true)
		if len(got) != 2 {
			t.Fatalf("root recents len = %d, want 2", len(got))
		}
		// Newest first.
		if got[0].RawURL != "/raw/b/" {
			t.Errorf("expected newest (b) first, got %q", got[0].RawURL)
		}
	})

	t.Run("sorted newest-first and capped", func(t *testing.T) {
		var many []RawRecentEntry
		for i := 1; i <= 12; i++ {
			many = append(many, mk(fmt.Sprintf("/raw/d/p%02d/", i), i))
		}
		got := SelectRawRecents(many, false)
		if len(got) != rawIndexRecentsLimit {
			t.Fatalf("recents len = %d, want %d", len(got), rawIndexRecentsLimit)
		}
		if got[0].RawURL != "/raw/d/p12/" {
			t.Errorf("expected newest p12 first, got %q", got[0].RawURL)
		}
	})
}

// markActiveByURL is still used by RenderRawIndex (the on-demand gallery) and
// the on-demand RenderRawPage fallback, so it keeps its own coverage even though
// the buildRawExplorerNode sidebar injection was removed (raw/ md now flows
// through the normal explorer tree as first-class pages).
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
