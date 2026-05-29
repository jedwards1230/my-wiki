package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jedwards1230/my-wiki/internal/vault"
)

func setupActivityVault(t *testing.T) (vault.Storage, string) {
	t.Helper()
	dir := t.TempDir()

	_ = os.MkdirAll(filepath.Join(dir, "meta", "activity"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "meta", "log.md"), []byte("---\ntitle: Activity Log\n---\n"), 0o644)

	return vault.NewFilesystemStorage(dir), dir
}

func TestActivityService_Append(t *testing.T) {
	storage, dir := setupActivityVault(t)
	svc := NewActivityService(storage)

	err := svc.Append(ActivityEntry{
		Type:  "create",
		Title: "Test Page",
		Time:  "10:30",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Check daily file was created
	entries, err := os.ReadDir(filepath.Join(dir, "meta", "activity"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no activity file created")
	}

	data, err := os.ReadFile(filepath.Join(dir, "meta", "activity", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(data), "### 10:30 | create | Test Page") {
		t.Errorf("expected entry, got:\n%s", string(data))
	}
}

func TestActivityService_AppendWithTouched(t *testing.T) {
	storage, dir := setupActivityVault(t)
	svc := NewActivityService(storage)

	err := svc.Append(ActivityEntry{
		Type:    "edit",
		Title:   "Update Schema",
		Time:    "14:00",
		Touched: []string{"meta/schema", "project/alpha"},
	})
	if err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(filepath.Join(dir, "meta", "activity"))
	data, _ := os.ReadFile(filepath.Join(dir, "meta", "activity", entries[0].Name()))
	content := string(data)

	if !strings.Contains(content, "[[meta/schema]]") {
		t.Error("expected wikilink for meta/schema")
	}
	if !strings.Contains(content, "[[project/alpha]]") {
		t.Error("expected wikilink for project/alpha")
	}
}

func TestActivityService_AppendUpdatesIndex(t *testing.T) {
	storage, dir := setupActivityVault(t)
	svc := NewActivityService(storage)

	err := svc.Append(ActivityEntry{
		Type:  "note",
		Title: "First Note",
		Time:  "11:00",
	})
	if err != nil {
		t.Fatal(err)
	}

	logData, err := os.ReadFile(filepath.Join(dir, "meta", "log.md"))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(logData), "1 changes") {
		t.Error("expected '1 changes' in log index")
	}
}

func TestActivityService_AutoLoggedCompact(t *testing.T) {
	storage, dir := setupActivityVault(t)
	svc := NewActivityService(storage)

	// Auto-logged entry should have no description body
	err := svc.Append(ActivityEntry{
		Type:       "create",
		Title:      "[[home/plants/pothos]]",
		Time:       "11:39",
		AutoLogged: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Manual entry should have description body
	err = svc.Append(ActivityEntry{
		Type:    "create",
		Title:   "Scaffolded all plant pages",
		Time:    "11:40",
		Summary: "Created 7 individual plant pages.",
		Touched: []string{"home/plants/overview", "home/plants/pothos"},
	})
	if err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "meta", "activity"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one activity file")
	}
	data, err := os.ReadFile(filepath.Join(dir, "meta", "activity", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	// Auto entry: header only, no "Updated" or "Touched" line
	if !strings.Contains(content, "### 11:39 | create | [[home/plants/pothos]]") {
		t.Error("expected auto-logged header")
	}
	// The auto entry should NOT have a description line after it
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.Contains(line, "11:39") && strings.Contains(line, "pothos") {
			// Next non-empty line should be the manual entry header, not a description
			for j := i + 1; j < len(lines); j++ {
				if strings.TrimSpace(lines[j]) == "" {
					continue
				}
				if !strings.HasPrefix(lines[j], "### ") {
					t.Errorf("expected next non-empty line after auto entry to be a header, got: %q", lines[j])
				}
				break
			}
			break
		}
	}

	// Manual entry: header + description with touched links
	if !strings.Contains(content, "### 11:40 | create | Scaffolded all plant pages") {
		t.Error("expected manual entry header")
	}
	if !strings.Contains(content, "[[home/plants/overview]]") {
		t.Error("expected touched wikilinks in manual entry description")
	}
}

func TestActivityService_InvalidType(t *testing.T) {
	storage, _ := setupActivityVault(t)
	svc := NewActivityService(storage)

	err := svc.Append(ActivityEntry{
		Type:  "invalid",
		Title: "Test",
		Time:  "10:00",
	})
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
}

func TestActivityService_InvalidTime(t *testing.T) {
	storage, _ := setupActivityVault(t)
	svc := NewActivityService(storage)

	err := svc.Append(ActivityEntry{
		Type:  "note",
		Title: "Test",
		Time:  "invalid",
	})
	if err == nil {
		t.Fatal("expected error for invalid time")
	}
}

func TestSanitize(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"normal text", "normal text"},
		{"has | pipe", "has pipe"},
		{"has ` backtick", "has backtick"},
		{"  extra   spaces  ", "extra spaces"},
	}

	for _, tt := range tests {
		got := Sanitize(tt.input)
		if got != tt.want {
			t.Errorf("Sanitize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBuildDescription(t *testing.T) {
	tests := []struct {
		name    string
		summary string
		touched []string
		want    string
	}{
		{"summary only", "Did stuff", nil, "Did stuff"},
		{"touched only", "", []string{"meta/schema"}, "Updated 1 page(s). Touched: [[meta/schema]]."},
		{"both", "Updated schema", []string{"meta/schema", "index"}, "Updated schema Touched: [[meta/schema]], [[index]]."},
		{"neither", "", nil, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildDescription(tt.summary, tt.touched)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
