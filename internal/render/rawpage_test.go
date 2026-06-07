package render

import (
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
