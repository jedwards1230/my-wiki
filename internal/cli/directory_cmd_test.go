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
	_ = os.MkdirAll(filepath.Join(dir, "guides/hosts"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "project"), 0o755)

	files := map[string]string{
		"guides/overview.md":       "---\ntitle: Guides Overview\ndescription: Infrastructure overview\ntags:\n  - guides\ndate: 2026-01-01\n---\n\nContent.\n",
		"guides/hosts/server-1.md": "---\ntitle: Server-1\ntags:\n  - guides/host\ndate: 2026-01-01\n---\n\nHost.\n",
		"project/alpha.md":         "---\ntitle: Alpha Project\ndescription: First project\ntags:\n  - project\ndate: 2026-02-01\n---\n\nProject.\n",
		"meta/schema.md":           "---\ntitle: Wiki Schema\ndescription: Operating manual for AI agents\ntags:\n  - meta\ndate: 2026-01-01\n---\n\nSchema.\n",
		"no-tags.md":               "---\ntitle: No Tags Page\ndate: 2026-01-01\n---\n\nNo tags.\n",
		"no-frontmatter.md":        "Just plain content.\n",
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

	if !strings.Contains(output, "6 wiki page(s)") {
		t.Errorf("expected '6 wiki page(s)', got:\n%s", output)
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

	indexFile := filepath.Join(dir, "index.md")
	data, err := os.ReadFile(indexFile)
	if err != nil {
		t.Fatalf("index.md not created: %v", err)
	}

	content := string(data)

	if !strings.Contains(content, "Home Wiki") {
		t.Error("missing title in generated index file")
	}
	if !strings.Contains(content, "date: ") {
		t.Error("missing date in generated index file")
	}
	if !strings.Contains(content, "## guides") {
		t.Error("missing guides group")
	}
	if !strings.Contains(content, "## project") {
		t.Error("missing project group")
	}
	if !strings.Contains(content, "## Uncategorized") {
		t.Error("missing Uncategorized group for pages without tags")
	}
	if !strings.Contains(content, "Guides Overview") {
		t.Error("missing page title in directory")
	}
	if !strings.Contains(content, "Infrastructure overview") {
		t.Error("missing page description in directory")
	}
	// Pages without description should show "—"
	if !strings.Contains(content, "—") {
		t.Error("missing em-dash for pages without description")
	}
}

func TestDirectoryGenerateGrouping(t *testing.T) {
	dir := setupDirectoryVault(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "directory", "--generate"})
	err := cmd.Execute()
	if err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "index.md"))
	content := string(data)

	// guides/host tag should group under "guides"
	guidesIdx := strings.Index(content, "## guides")
	projectIdx := strings.Index(content, "## project")

	if guidesIdx < 0 || projectIdx < 0 {
		t.Fatal("missing expected groups")
	}

	// guides section should contain server-1 (tagged guides/host)
	guidesSection := content[guidesIdx:projectIdx]
	if !strings.Contains(guidesSection, "Server-1") {
		t.Error("Server-1 should be in guides group (tag guides/host)")
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
