package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupDirectoryVault(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	_ = os.MkdirAll(filepath.Join(dir, "meta"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "meta/activity"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "guides/hosts"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "project"), 0o755)

	files := map[string]string{
		"guides/overview.md":          "---\ntitle: Guides Overview\ndescription: Infrastructure overview\ntags:\n  - guides\ndate: 2026-01-01\n---\n\nContent.\n",
		"guides/hosts/server-1.md":    "---\ntitle: Server-1\ntags:\n  - guides/host\ndate: 2026-01-01\n---\n\nHost.\n",
		"project/alpha.md":            "---\ntitle: Alpha Project\ndescription: First project\ntags:\n  - project\ndate: 2026-02-01\n---\n\nProject.\n",
		"meta/schema.md":              "---\ntitle: Wiki Schema\ndescription: Operating manual for AI agents\ntags:\n  - meta\ndate: 2026-01-01\n---\n\nSchema.\n",
		"no-tags.md":                  "---\ntitle: No Tags Page\ndate: 2026-01-01\n---\n\nNo tags.\n",
		"no-frontmatter.md":           "Just plain content.\n",
		"meta/activity/2026-04-06.md": "---\ntitle: \"2026-04-06\"\ntags:\n  - meta/activity\ndate: 2026-04-06\n---\n\n### 10:00 | create | Test\n",
	}
	for name, content := range files {
		_ = os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}

	return dir
}

func TestDirectoryList(t *testing.T) {
	dir := setupDirectoryVault(t)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "directory"})
	err := cmd.Execute()

	_ = w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatal(err)
	}

	var buf [8192]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	if !strings.Contains(output, "guides/overview.md") {
		t.Errorf("expected guides/overview.md in output, got:\n%s", output)
	}
	if !strings.Contains(output, "Guides Overview") {
		t.Errorf("expected title in output, got:\n%s", output)
	}
	if !strings.Contains(output, "Infrastructure overview") {
		t.Errorf("expected description in output, got:\n%s", output)
	}
}

func TestDirectoryCount(t *testing.T) {
	dir := setupDirectoryVault(t)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "directory", "--count"})
	err := cmd.Execute()

	_ = w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatal(err)
	}

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	// 7 total: 6 wiki pages + 1 activity log (List includes all)
	if !strings.Contains(output, "7 wiki page(s)") {
		t.Errorf("expected '7 wiki page(s)', got:\n%s", output)
	}
}

func TestDirectoryGenerate(t *testing.T) {
	dir := setupDirectoryVault(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "directory", "--generate"})
	err := cmd.Execute()
	if err != nil {
		t.Fatal(err)
	}

	// Root index should exist
	data, err := os.ReadFile(filepath.Join(dir, "index.md"))
	if err != nil {
		t.Fatalf("index.md not created: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "Home Wiki") {
		t.Error("missing title in root index")
	}
	if !strings.Contains(content, "## Directory") {
		t.Error("missing Directory section in root index")
	}
	if !strings.Contains(content, "## Tags") {
		t.Error("missing Tags section in root index")
	}
	if !strings.Contains(content, "guides/") {
		t.Error("missing guides/ in directory tree")
	}

	// Leaf index should exist
	guidesHostsIndex := filepath.Join(dir, "guides", "hosts", "index.md")
	data, err = os.ReadFile(guidesHostsIndex)
	if err != nil {
		t.Fatalf("guides/hosts/index.md not created: %v", err)
	}
	hostContent := string(data)

	if !strings.Contains(hostContent, "Server-1") {
		t.Error("missing Server-1 in guides/hosts/index.md")
	}
	if !strings.Contains(hostContent, "## Directory") {
		t.Error("missing Directory section in guides/hosts/index.md")
	}

	// Mid-level index should exist
	guidesIndex := filepath.Join(dir, "guides", "index.md")
	data, err = os.ReadFile(guidesIndex)
	if err != nil {
		t.Fatalf("guides/index.md not created: %v", err)
	}
	guidesContent := string(data)

	if !strings.Contains(guidesContent, "Guides Overview") {
		t.Error("missing Guides Overview in guides/index.md")
	}
	if !strings.Contains(guidesContent, "hosts/") {
		t.Error("missing hosts/ subdirectory reference in guides/index.md")
	}

	// Project index should exist
	projectIndex := filepath.Join(dir, "project", "index.md")
	data, err = os.ReadFile(projectIndex)
	if err != nil {
		t.Fatalf("project/index.md not created: %v", err)
	}
	projectContent := string(data)

	if !strings.Contains(projectContent, "Alpha Project") {
		t.Error("missing Alpha Project in project/index.md")
	}

	// Subdirectory indexes must use bullet lists, not tables
	for _, sub := range []struct {
		name    string
		content string
	}{
		{"guides/index.md", guidesContent},
		{"guides/hosts/index.md", hostContent},
		{"project/index.md", projectContent},
	} {
		if strings.Contains(sub.content, "|---") {
			t.Errorf("%s should not contain table markup, got table separators", sub.name)
		}
		if !strings.Contains(sub.content, "- [[") {
			t.Errorf("%s should use bullet list format with wikilinks", sub.name)
		}
	}
}

func TestDirectoryGenerateExcludesActivity(t *testing.T) {
	dir := setupDirectoryVault(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "directory", "--generate"})
	err := cmd.Execute()
	if err != nil {
		t.Fatal(err)
	}

	// meta/activity/ should NOT get an index
	activityIndex := filepath.Join(dir, "meta", "activity", "index.md")
	if _, err := os.Stat(activityIndex); err == nil {
		t.Error("meta/activity/index.md should NOT be generated")
	}

	// Root index should not contain activity log entries
	data, _ := os.ReadFile(filepath.Join(dir, "index.md"))
	content := string(data)
	if strings.Contains(content, "2026-04-06") {
		t.Error("activity log entries should be excluded from root index")
	}
}

func TestDirectoryGenerateMultipleIndexFiles(t *testing.T) {
	dir := setupDirectoryVault(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "directory", "--generate"})
	err := cmd.Execute()
	if err != nil {
		t.Fatal(err)
	}

	// Count generated index files
	expected := []string{
		"index.md",
		"guides/index.md",
		"guides/hosts/index.md",
		"project/index.md",
		"meta/index.md",
	}

	for _, path := range expected {
		full := filepath.Join(dir, path)
		if _, err := os.Stat(full); os.IsNotExist(err) {
			t.Errorf("expected %s to be generated", path)
		}
	}
}

func TestDirectoryEmpty(t *testing.T) {
	dir := t.TempDir()

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "directory"})
	err := cmd.Execute()

	_ = w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatal(err)
	}

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	if !strings.Contains(output, "No wiki pages found") {
		t.Errorf("expected empty message, got:\n%s", output)
	}
}
