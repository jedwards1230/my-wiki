package slug

import "testing"

func TestMake(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "grafana", "grafana"},
		{"spaces", "Staff archetypes", "staff-archetypes"},
		{"curly apostrophe joined", "Thinking Machines’ Murati on AI’s Next Chapter", "thinking-machines-murati-on-ais-next-chapter"},
		{"straight apostrophe joined", "The body's gut", "the-bodys-gut"},
		{"double space collapses", "gut health  DW Documentary", "gut-health-dw-documentary"},
		{"pipe and punctuation", "The intestine - The body | DW", "the-intestine-the-body-dw"},
		{"leading/trailing separators", "  --hello--  ", "hello"},
		{"unicode dropped to separator", "café del mar", "caf-del-mar"},
		{"already a slug is idempotent", "staff-archetypes-staffeng", "staff-archetypes-staffeng"},
		{"digits and dates", "2026-06-11", "2026-06-11"},
		{"all punctuation falls back", "—’|’—", "untitled"},
		{"empty falls back", "", "untitled"},
		{"uppercase lowered", "NODE_LABELS", "node-labels"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Make(tc.in); got != tc.want {
				t.Errorf("Make(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMakeIdempotent(t *testing.T) {
	inputs := []string{
		"Thinking Machines’ Murati on AI’s Next Chapter",
		"gut health  DW Documentary",
		"Staff archetypes",
		"café del mar",
		"",
	}
	for _, in := range inputs {
		once := Make(in)
		twice := Make(once)
		if once != twice {
			t.Errorf("Make not idempotent for %q: %q -> %q", in, once, twice)
		}
	}
}

func TestMakeLengthCap(t *testing.T) {
	long := "this is an extremely long clipping title that goes well beyond the eighty character cap we enforce so it should be truncated on a hyphen boundary"
	got := Make(long)
	if len(got) > maxLen {
		t.Errorf("Make produced slug longer than cap: len=%d %q", len(got), got)
	}
	if got[len(got)-1] == '-' {
		t.Errorf("Make produced trailing hyphen after truncation: %q", got)
	}
}

func TestNormalizePath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"research/clippings/Thinking Machines’ Murati.md", "research/clippings/thinking-machines-murati.md"},
		{"home/homelab/services/grafana.md", "home/homelab/services/grafana.md"},
		{"inbox/clippings/Staff archetypes.md", "inbox/clippings/staff-archetypes.md"},
		{"2026-06-11.md", "2026-06-11.md"},
		{"inbox/clippings/gut health  DW Documentary.md", "inbox/clippings/gut-health-dw-documentary.md"},
		{"NoExtension", "noextension"},
		{"UPPER.MD", "upper.md"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := NormalizePath(tc.in); got != tc.want {
				t.Errorf("NormalizePath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizePathIdempotent(t *testing.T) {
	inputs := []string{
		"research/clippings/Thinking Machines’ Murati.md",
		"home/homelab/services/grafana.md",
		"inbox/clippings/gut health  DW Documentary.md",
	}
	for _, in := range inputs {
		once := NormalizePath(in)
		twice := NormalizePath(once)
		if once != twice {
			t.Errorf("NormalizePath not idempotent for %q: %q -> %q", in, once, twice)
		}
	}
}

func TestIsNormalized(t *testing.T) {
	if !IsNormalized("home/homelab/services/grafana.md") {
		t.Error("expected already-clean path to be normalized")
	}
	if IsNormalized("inbox/clippings/Staff archetypes.md") {
		t.Error("expected unsafe path to be reported not-normalized")
	}
}
