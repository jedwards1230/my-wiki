package search

import "testing"

func TestStripFrontmatter(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "with frontmatter",
			content: "---\ntitle: Test\ntags:\n  - foo\n---\n\nBody text here.",
			want:    "Body text here.",
		},
		{
			name:    "no frontmatter",
			content: "Just plain text.",
			want:    "Just plain text.",
		},
		{
			name:    "frontmatter only",
			content: "---\ntitle: Test\n---",
			want:    "",
		},
		{
			name:    "empty string",
			content: "",
			want:    "",
		},
		{
			name:    "frontmatter with extra whitespace",
			content: "---\ntitle: Test\n---\n\n\n  Body.  \n",
			want:    "Body.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripFrontmatter(tt.content)
			if got != tt.want {
				t.Errorf("StripFrontmatter() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractSnippet(t *testing.T) {
	content := "The quick brown fox jumps over the lazy dog near the riverbank"

	tests := []struct {
		name         string
		content      string
		query        string
		windowBefore int
		windowAfter  int
		wantContains string
		wantPrefix   string
	}{
		{
			name:         "match in middle",
			content:      content,
			query:        "fox",
			windowBefore: 10,
			windowAfter:  20,
			wantContains: "fox",
		},
		{
			name:         "match at start",
			content:      content,
			query:        "The",
			windowBefore: 10,
			windowAfter:  20,
			wantContains: "The",
		},
		{
			name:         "case insensitive",
			content:      content,
			query:        "FOX",
			windowBefore: 10,
			windowAfter:  20,
			wantContains: "fox",
		},
		{
			name:         "no match returns beginning",
			content:      content,
			query:        "zebra",
			windowBefore: 10,
			windowAfter:  20,
			wantContains: "quick",
		},
		{
			name:         "empty content",
			content:      "",
			query:        "test",
			windowBefore: 10,
			windowAfter:  20,
			wantContains: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractSnippet(tt.content, tt.query, tt.windowBefore, tt.windowAfter)
			if tt.wantContains != "" && len(got) > 0 {
				if !containsCI(got, tt.wantContains) {
					t.Errorf("ExtractSnippet() = %q, want it to contain %q", got, tt.wantContains)
				}
			}
		})
	}
}

func containsCI(s, sub string) bool {
	return len(s) >= len(sub) && (sub == "" ||
		len(s) > 0 && contains(lower(s), lower(sub)))
}

func lower(s string) string {
	b := make([]byte, len(s))
	for i := range s {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
