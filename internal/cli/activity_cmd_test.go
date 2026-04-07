package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupActivityVault(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	os.MkdirAll(filepath.Join(dir, "meta", "activity"), 0o755)

	// Create empty log index
	os.WriteFile(filepath.Join(dir, "meta", "log.md"), []byte("---\ntitle: Activity Log\n---\n"), 0o644)

	return dir
}

func TestActivityCreate(t *testing.T) {
	dir := setupActivityVault(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "activity", "create", "Test Page", "--time", "10:30"})
	err := cmd.Execute()
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

	// Read the daily file
	data, err := os.ReadFile(filepath.Join(dir, "meta", "activity", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if !strings.Contains(content, "### 10:30 | create | Test Page") {
		t.Errorf("expected entry in activity file, got:\n%s", content)
	}
}

func TestActivityWithTouched(t *testing.T) {
	dir := setupActivityVault(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "activity", "edit", "Update Schema", "--time", "14:00", "--touched", "meta/schema,project/alpha"})
	err := cmd.Execute()
	if err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(filepath.Join(dir, "meta", "activity"))
	data, _ := os.ReadFile(filepath.Join(dir, "meta", "activity", entries[0].Name()))
	content := string(data)

	if !strings.Contains(content, "[[meta/schema]]") {
		t.Errorf("expected wikilink for meta/schema, got:\n%s", content)
	}
	if !strings.Contains(content, "[[project/alpha]]") {
		t.Errorf("expected wikilink for project/alpha, got:\n%s", content)
	}
}

func TestActivityWithSummary(t *testing.T) {
	dir := setupActivityVault(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "activity", "ingest", "Article", "--time", "09:00", "--summary", "Ingested and summarized"})
	err := cmd.Execute()
	if err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(filepath.Join(dir, "meta", "activity"))
	data, _ := os.ReadFile(filepath.Join(dir, "meta", "activity", entries[0].Name()))
	content := string(data)

	if !strings.Contains(content, "Ingested and summarized") {
		t.Errorf("expected summary in output, got:\n%s", content)
	}
}

func TestActivityUpdatesLogIndex(t *testing.T) {
	dir := setupActivityVault(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "activity", "note", "First Note", "--time", "11:00"})
	err := cmd.Execute()
	if err != nil {
		t.Fatal(err)
	}

	logData, err := os.ReadFile(filepath.Join(dir, "meta", "log.md"))
	if err != nil {
		t.Fatal(err)
	}
	logContent := string(logData)

	if !strings.Contains(logContent, "1 changes") {
		t.Errorf("expected '1 changes' in log index, got:\n%s", logContent)
	}
	if !strings.Contains(logContent, "First Note") {
		t.Errorf("expected 'First Note' in log index, got:\n%s", logContent)
	}
}

func TestActivityMultipleEntries(t *testing.T) {
	dir := setupActivityVault(t)

	// First entry
	cmd1 := NewRootCmd()
	cmd1.SetArgs([]string{"--vault", dir, "activity", "note", "Entry One", "--time", "10:00"})
	if err := cmd1.Execute(); err != nil {
		t.Fatal(err)
	}

	// Second entry (same day)
	cmd2 := NewRootCmd()
	cmd2.SetArgs([]string{"--vault", dir, "activity", "edit", "Entry Two", "--time", "11:00"})
	if err := cmd2.Execute(); err != nil {
		t.Fatal(err)
	}

	// Check log index shows 2 changes
	logData, _ := os.ReadFile(filepath.Join(dir, "meta", "log.md"))
	logContent := string(logData)

	if !strings.Contains(logContent, "2 changes") {
		t.Errorf("expected '2 changes' in log index, got:\n%s", logContent)
	}
}

func TestActivityInvalidType(t *testing.T) {
	dir := setupActivityVault(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "activity", "invalid", "Title"})
	err := cmd.Execute()

	if err == nil {
		t.Fatal("expected error for invalid type, got nil")
	}
}

func TestActivitySanitizeTitle(t *testing.T) {
	dir := setupActivityVault(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "activity", "note", "Title with | pipes and ` backticks", "--time", "12:00"})
	err := cmd.Execute()
	if err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(filepath.Join(dir, "meta", "activity"))
	data, _ := os.ReadFile(filepath.Join(dir, "meta", "activity", entries[0].Name()))
	content := string(data)

	if strings.Contains(content, "|  pipes") || strings.Contains(content, "`") {
		// The entry line itself uses | as separator, but the title should be sanitized
		// Check that title part doesn't have backticks
		for _, line := range strings.Split(content, "\n") {
			if strings.HasPrefix(line, "### 12:00") {
				parts := strings.SplitN(line, " | ", 3)
				if len(parts) == 3 && strings.Contains(parts[2], "`") {
					t.Errorf("title should not contain backticks: %s", parts[2])
				}
			}
		}
	}
}

func TestActivityInvalidTime(t *testing.T) {
	dir := setupActivityVault(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "activity", "note", "Title", "--time", "invalid"})
	err := cmd.Execute()

	if err == nil {
		t.Fatal("expected error for invalid time, got nil")
	}
}

func TestBuildDescription(t *testing.T) {
	tests := []struct {
		name    string
		summary string
		touched []string
		want    string
	}{
		{
			name:    "summary only",
			summary: "Did stuff",
			want:    "Did stuff",
		},
		{
			name:    "touched only",
			touched: []string{"meta/schema"},
			want:    "Updated 1 page(s). Touched: [[meta/schema]].",
		},
		{
			name:    "both",
			summary: "Updated schema",
			touched: []string{"meta/schema", "index"},
			want:    "Updated schema Touched: [[meta/schema]], [[index]].",
		},
		{
			name: "neither",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildDescription(tt.summary, tt.touched)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
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
		got := sanitize(tt.input)
		if got != tt.want {
			t.Errorf("sanitize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
