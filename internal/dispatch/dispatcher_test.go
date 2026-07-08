package dispatch

import (
	"testing"
)

func TestSanitizeURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "https://n8n.example.com/hook", "https://n8n.example.com/hook"},
		{"userinfo stripped", "https://u:p@h.example.com/x", "https://h.example.com/x"},
		{"query stripped", "https://h.example.com/x?token=abc", "https://h.example.com/x"},
		{"fragment stripped", "https://h.example.com/x#y", "https://h.example.com/x"},
		{"all three stripped", "https://u:p@h.example.com/x?k=v#f", "https://h.example.com/x"},
		{"no path", "https://h.example.com", "https://h.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeURL(tc.in); got != tc.want {
				t.Errorf("SanitizeURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Unparseable URLs should round-trip verbatim — better to see the literal
// than to swallow it silently.
func TestSanitizeURL_UnparseableFallsBack(t *testing.T) {
	// url.Parse is famously lenient, but control characters fail it.
	bad := "http://\x00/bad"
	got := SanitizeURL(bad)
	if got != bad {
		t.Errorf("SanitizeURL unparseable: got %q, want %q", got, bad)
	}
}
