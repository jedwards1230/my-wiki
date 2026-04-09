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
	_ = os.MkdirAll(filepath.Join(dir, "homelab/hosts"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "project"), 0o755)

	files := map[string]string{
		"homelab/overview.md":     "---\ntitle: Homelab Overview\ndescription: Infrastructure overview\ntags:\n  - homelab\ndate: 2026-01-01\n---\n\nContent.\n",
		"homelab/hosts/linux-1.md": "---\ntitle: Linux-1\ntags:\n  - homelab/host\ndate: 2026-01-01\n---\n\nHost.\n",
		"project/alpha.md":        "---\ntitle: Alpha Project\ndescription: First project\ntags:\n  - project\ndate: 2026-02-01\n---\n\nProject.\n",
		"meta/schema.md":          "---\ntitle: Wiki Schema\ndescription: Operating manual for AI agents\ntags:\n  - meta\ndate: 2026-01-01\n---\n\nSchema.\n",
		"no-tags.md":              "---\ntitle: No Tags Page\ndate: 2026-01-01\n---\n\nNo tags.\n",
		"no-frontmatter.md":       "Just plain content.\n",
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

	if !strings.Contains(output, "homelab/overview.md") {
		t.Errorf("expected homelab/overview.md in output, got:\n%s", output)
	}
	if !strings.Contains(output, "Homelab Overview") {
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
	if !strings.Contains(content, "date: 2026-04-06") {
		t.Error("missing fixed date in generated index file")
	}
	if !strings.Contains(content, "## homelab") {
		t.Error("missing homelab group")
	}
	if !strings.Contains(content, "## project") {
		t.Error("missing project group")
	}
	if !strings.Contains(content, "## Uncategorized") {
		t.Error("missing Uncategorized group for pages without tags")
	}
	if !strings.Contains(content, "Homelab Overview") {
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

	// homelab/host tag should group under "homelab"
	homelabIdx := strings.Index(content, "## homelab")
	projectIdx := strings.Index(content, "## project")

	if homelabIdx < 0 || projectIdx < 0 {
		t.Fatal("missing expected groups")
	}

	// homelab section should contain linux-1 (tagged homelab/host)
	homelabSection := content[homelabIdx:projectIdx]
	if !strings.Contains(homelabSection, "Linux-1") {
		t.Error("Linux-1 should be in homelab group (tag homelab/host)")
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
