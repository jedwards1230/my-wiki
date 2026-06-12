package cli

import (
	"testing"

	"github.com/jedwards1230/my-wiki/internal/service"
)

func TestActivityTitle(t *testing.T) {
	const path = "inbox/clippings/staff-archetypes"
	tests := []struct {
		kind service.MutationKind
		want string
	}{
		{service.MutationCreate, "[[" + path + "]]"},
		{service.MutationEdit, "[[" + path + "]]"},
		{service.MutationMove, "[[" + path + "]]"},
		{service.MutationDelete, "~~" + path + "~~"},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			if got := activityTitle(tt.kind, path); got != tt.want {
				t.Fatalf("activityTitle(%q) = %q, want %q", tt.kind, got, tt.want)
			}
		})
	}
}

// TestActivityTitleDeleteSurvivesSanitize guards the reason strikethrough was
// chosen over inline code: Sanitize strips '|' and '`' but not '~', so the
// delete marker must reach the rendered log intact.
func TestActivityTitleDeleteSurvivesSanitize(t *testing.T) {
	title := activityTitle(service.MutationDelete, "inbox/clippings/staff-archetypes")
	if got := service.Sanitize(title); got != title {
		t.Fatalf("Sanitize mangled delete title: %q -> %q", title, got)
	}
}
