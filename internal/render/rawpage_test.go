package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	t.Run("media present → Directory + Gallery sections", func(t *testing.T) {
		out, toc := buildRawIndex("/raw/clippings/", []RawDirEntry{
			{Name: "youtube", IsDir: true},
			{Name: "photo.png", IsDir: false},
			{Name: "clip.mp4", IsDir: false},
			{Name: "notes.txt", IsDir: false},
		})
		s := string(out)
		// Directory section: a standard bulleted internal-link list (no custom
		// raw-list/icons), listing every child including media.
		if !strings.Contains(s, `<h2 id="directory">Directory</h2>`) {
			t.Errorf("missing Directory heading:\n%s", s)
		}
		for _, want := range []string{
			`<a class="internal" href="/raw/clippings/youtube/">youtube/</a>`,
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
		// Gallery section: image thumbnail + non-image media badge.
		if !strings.Contains(s, `<h2 id="gallery">Gallery</h2>`) {
			t.Errorf("missing Gallery heading:\n%s", s)
		}
		if !strings.Contains(s, `<img loading="lazy" src="/raw/clippings/photo.png"`) {
			t.Errorf("Gallery missing image thumbnail:\n%s", s)
		}
		if !strings.Contains(s, "MP4") {
			t.Errorf("Gallery missing video badge:\n%s", s)
		}
		// TOC lists both sections for the right-rail "On this page".
		if len(toc) != 2 || toc[0].Anchor != "directory" || toc[1].Anchor != "gallery" {
			t.Errorf("toc = %+v, want [directory, gallery]", toc)
		}
	})

	t.Run("no media → Directory only, no Gallery", func(t *testing.T) {
		out, toc := buildRawIndex("/raw/", []RawDirEntry{
			{Name: "clippings", IsDir: true},
			{Name: "readme.txt", IsDir: false},
		})
		s := string(out)
		if !strings.Contains(s, `<h2 id="directory">Directory</h2>`) {
			t.Errorf("missing Directory heading:\n%s", s)
		}
		if strings.Contains(s, "Gallery") || strings.Contains(s, "raw-gallery") {
			t.Errorf("no media, but a Gallery section was rendered:\n%s", s)
		}
		if len(toc) != 1 || toc[0].Anchor != "directory" {
			t.Errorf("toc = %+v, want [directory] only", toc)
		}
	})

	t.Run("empty directory", func(t *testing.T) {
		out, _ := buildRawIndex("/raw/empty/", nil)
		if !strings.Contains(string(out), "empty") {
			t.Errorf("expected empty-directory note:\n%s", out)
		}
	})
}

func TestBuildRawExplorerNode(t *testing.T) {
	dir := t.TempDir()
	if buildRawExplorerNode(dir) != nil {
		t.Fatal("expected nil when vault has no raw/ dir")
	}
	mk := func(parts ...string) string {
		p := filepath.Join(append([]string{dir}, parts...)...)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	mk("raw", "clippings", "youtube", "clip.md")
	mk("raw", "gists", "snippet.txt")
	mk("raw", "top.png")

	node := buildRawExplorerNode(dir)
	if node == nil || node.Name != "Raw" || node.URL != "/raw/" || !node.IsFolder {
		t.Fatalf("unexpected root node: %+v", node)
	}
	// Folders sort before files: Clippings, Gists, then top.png.
	if len(node.Children) != 3 {
		t.Fatalf("want 3 children, got %d", len(node.Children))
	}
	if node.Children[0].Name != "Clippings" || node.Children[0].URL != "/raw/clippings/" {
		t.Errorf("child[0] = %+v, want Clippings /raw/clippings/", node.Children[0])
	}
	if node.Children[2].Name != "top.png" || node.Children[2].URL != "/raw/top.png" || node.Children[2].IsFolder {
		t.Errorf("child[2] = %+v, want file top.png", node.Children[2])
	}
	// Deep file URL.
	yt := node.Children[0].Children[0]
	if yt.Name != "Youtube" || yt.Children[0].URL != "/raw/clippings/youtube/clip.md" {
		t.Errorf("deep node wrong: %+v / %+v", yt, yt.Children[0])
	}

	// markActiveByURL marks the leaf and opens its ancestors.
	roots := []*ExplorerNode{node}
	if !markActiveByURL(roots, "/raw/clippings/youtube/clip.md") {
		t.Fatal("expected markActiveByURL to match the clip")
	}
	if !node.IsOpen || !node.Children[0].IsOpen || !yt.IsOpen {
		t.Error("ancestor folders should be open")
	}
	if !yt.Children[0].IsActive {
		t.Error("clip.md leaf should be active")
	}
	if node.Children[1].IsOpen {
		t.Error("sibling folder (Gists) should stay closed")
	}
}
