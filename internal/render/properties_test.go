package render

import (
	"testing"
	"time"

	yaml "gopkg.in/yaml.v2"
)

func TestBuildProperties(t *testing.T) {
	items := yaml.MapSlice{
		{Key: "title", Value: "My Title"},
		{Key: "date", Value: "2026-06-06"},
		{Key: "tags", Value: []interface{}{"clipping"}},
		{Key: "description", Value: "a summary"},
		{Key: "source", Value: "https://example.com/x"},
		{Key: "author", Value: "Hank Green"},
		{Key: "aliases", Value: []interface{}{"alpha", "beta"}},
		{Key: "rating", Value: 5},
		{Key: "read", Value: true},
		{Key: "empty", Value: ""},
		{Key: "blanklist", Value: []interface{}{"", nil}},
		// Computed markers from the directory generator must be hidden.
		{Key: "pages", Value: 3},
		{Key: "generated", Value: true},
	}

	got := buildProperties(items)

	// Order preserved; chrome/computed/empty fields dropped.
	wantKeys := []string{"source", "author", "aliases", "rating", "read"}
	if len(got) != len(wantKeys) {
		t.Fatalf("got %d properties, want %d: %+v", len(got), len(wantKeys), got)
	}
	for i, k := range wantKeys {
		if got[i].Key != k {
			t.Errorf("property[%d].Key = %q, want %q (order not preserved?)", i, got[i].Key, k)
		}
	}

	// source is a URL → linked.
	if src := got[0]; src.Label != "Source" || len(src.Values) != 1 || src.Values[0].Href != "https://example.com/x" {
		t.Errorf("source not rendered as link: %+v", src)
	}
	// author is plain text → no href.
	if a := got[1]; len(a.Values) != 1 || a.Values[0].Href != "" || a.Values[0].Text != "Hank Green" {
		t.Errorf("author rendered wrong: %+v", a)
	}
	// aliases is a 2-element list.
	if al := got[2]; len(al.Values) != 2 || al.Values[0].Text != "alpha" || al.Values[1].Text != "beta" {
		t.Errorf("aliases list rendered wrong: %+v", al)
	}
	// scalar non-strings stringify.
	if r := got[3]; r.Values[0].Text != "5" {
		t.Errorf("rating = %q, want \"5\"", r.Values[0].Text)
	}
	if r := got[4]; r.Values[0].Text != "true" {
		t.Errorf("read = %q, want \"true\"", r.Values[0].Text)
	}
}

func TestBuildPropertiesEmpty(t *testing.T) {
	// Only chrome fields → no properties table at all.
	items := yaml.MapSlice{
		{Key: "title", Value: "T"},
		{Key: "date", Value: "2026-01-01"},
		{Key: "tags", Value: []interface{}{"x"}},
	}
	if got := buildProperties(items); got != nil {
		t.Errorf("buildProperties = %+v, want nil", got)
	}
	if got := buildProperties(nil); got != nil {
		t.Errorf("buildProperties(nil) = %+v, want nil", got)
	}
}

func TestMetaScalarTime(t *testing.T) {
	ts := time.Date(2026, 6, 6, 10, 30, 0, 0, time.UTC)
	mv, ok := metaScalar(ts)
	if !ok || mv.Text != "2026-06-06" {
		t.Errorf("metaScalar(time) = (%+v, %v), want ({2026-06-06 }, true)", mv, ok)
	}
	if _, ok := metaScalar(nil); ok {
		t.Error("metaScalar(nil) should report ok=false")
	}
	if _, ok := metaScalar("   "); ok {
		t.Error("metaScalar(whitespace) should report ok=false after trim")
	}
}

func TestRenderPageDescriptionFromFrontmatter(t *testing.T) {
	r, err := NewRenderer(nil)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}

	withDesc := []byte("---\ntitle: T\ndate: 2026-01-01\ndescription: an authored summary\nstatus: complete\n---\n\nBody paragraph here.\n")
	p, err := r.RenderPage("a.md", withDesc, time.Now())
	if err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	if !p.DescriptionFromFrontmatter {
		t.Error("DescriptionFromFrontmatter = false, want true")
	}
	if p.Description != "an authored summary" {
		t.Errorf("Description = %q", p.Description)
	}
	if len(p.Properties) != 1 || p.Properties[0].Key != "status" {
		t.Errorf("Properties = %+v, want single status field", p.Properties)
	}

	// No frontmatter description → fallback fills Description but the flag
	// stays false so the page doesn't show an auto blurb as authored.
	noDesc := []byte("---\ntitle: T\ndate: 2026-01-01\n---\n\nFirst paragraph becomes the meta description fallback.\n")
	p2, err := r.RenderPage("b.md", noDesc, time.Now())
	if err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	if p2.DescriptionFromFrontmatter {
		t.Error("DescriptionFromFrontmatter = true for fallback description, want false")
	}
	if p2.Description == "" {
		t.Error("expected first-paragraph fallback Description, got empty")
	}
}
