package service

import (
	"os"
	"path/filepath"
	"testing"
)

func setupPagesVault(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	for _, d := range []string{"meta", "project", "raw", "private", ".obsidian"} {
		_ = os.MkdirAll(filepath.Join(dir, d), 0o755)
	}

	files := map[string]string{
		"index.md":         "---\ntitle: Home\ntags:\n  - root\ndate: 2026-01-01\n---\n\nWelcome.\n",
		"meta/schema.md":   "---\ntitle: Schema\ntags:\n  - meta\ndate: 2026-01-01\n---\n\nSchema content.\n",
		"project/alpha.md": "---\ntitle: Alpha\ntags:\n  - project\ndate: 2026-02-01\n---\n\nAlpha content.\n",
		"private/secret.md": "---\ntitle: Secret\n---\n\nPrivate.\n",
		"raw/source.md":     "---\ntitle: Source\n---\n\nRaw.\n",
	}

	for name, content := range files {
		_ = os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}

	return dir
}

func TestPageService_Read(t *testing.T) {
	dir := setupPagesVault(t)
	svc := NewPageService(dir)

	content, err := svc.Read("index.md")
	if err != nil {
		t.Fatal(err)
	}
	if content == "" {
		t.Fatal("expected content")
	}
}

func TestPageService_ReadWithoutExtension(t *testing.T) {
	dir := setupPagesVault(t)
	svc := NewPageService(dir)

	content, err := svc.Read("index")
	if err != nil {
		t.Fatal(err)
	}
	if content == "" {
		t.Fatal("expected content")
	}
}

func TestPageService_ReadNotFound(t *testing.T) {
	dir := setupPagesVault(t)
	svc := NewPageService(dir)

	_, err := svc.Read("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent page")
	}
}

func TestPageService_Write(t *testing.T) {
	dir := setupPagesVault(t)
	svc := NewPageService(dir)

	err := svc.Write("new-page.md", "---\ntitle: New\n---\n\nContent.\n")
	if err != nil {
		t.Fatal(err)
	}

	// Verify file exists
	data, err := os.ReadFile(filepath.Join(dir, "new-page.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" {
		t.Fatal("expected content in written file")
	}
}

func TestPageService_WriteNestedPath(t *testing.T) {
	dir := setupPagesVault(t)
	svc := NewPageService(dir)

	err := svc.Write("deep/nested/page.md", "content")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "deep", "nested", "page.md")); err != nil {
		t.Fatal("expected nested file to exist")
	}
}

func TestPageService_Delete(t *testing.T) {
	dir := setupPagesVault(t)
	svc := NewPageService(dir)

	err := svc.Delete("index.md")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "index.md")); !os.IsNotExist(err) {
		t.Fatal("expected file to be deleted")
	}
}

func TestPageService_DeleteNotFound(t *testing.T) {
	dir := setupPagesVault(t)
	svc := NewPageService(dir)

	err := svc.Delete("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent page")
	}
}

func TestPageService_List(t *testing.T) {
	dir := setupPagesVault(t)
	svc := NewPageService(dir)

	pages, err := svc.List("")
	if err != nil {
		t.Fatal(err)
	}

	// Should include wiki pages but not raw/ or private/ or .obsidian/
	paths := map[string]bool{}
	for _, p := range pages {
		paths[p.Path] = true
	}

	if !paths["index.md"] {
		t.Error("expected index.md")
	}
	if !paths["meta/schema.md"] {
		t.Error("expected meta/schema.md")
	}
	if paths["private/secret.md"] {
		t.Error("should not include private/")
	}
	if paths["raw/source.md"] {
		t.Error("should not include raw/")
	}
}

func TestPageService_ListPrefix(t *testing.T) {
	dir := setupPagesVault(t)
	svc := NewPageService(dir)

	pages, err := svc.List("project")
	if err != nil {
		t.Fatal(err)
	}

	if len(pages) != 1 {
		t.Fatalf("expected 1 page under project/, got %d", len(pages))
	}
	if pages[0].Path != "project/alpha.md" {
		t.Errorf("expected project/alpha.md, got %s", pages[0].Path)
	}
}

func TestPageService_PathTraversal(t *testing.T) {
	dir := setupPagesVault(t)
	svc := NewPageService(dir)

	_, err := svc.Read("../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}
