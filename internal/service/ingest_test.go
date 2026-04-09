package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jedwards1230/home-wiki/internal/vault"
)

func setupIngestVault(t *testing.T) *vault.Vault {
	t.Helper()
	dir := t.TempDir()

	_ = os.MkdirAll(filepath.Join(dir, "raw"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "meta"), 0o755)

	files := map[string]string{
		"raw/unprocessed.md": "---\ntitle: Unprocessed\nsource: https://example.com\ndate-added: 2026-01-15\n---\n\nContent.\n",
		"raw/processed.md":   "---\ntitle: Processed\nsource: https://example.com\ndate-added: 2026-01-10\ningested: true\n---\n\nDone.\n",
		"raw/also-new.md":    "---\ntitle: Also New\nsource: https://example.com/2\ndate-added: 2026-02-01\n---\n\nMore content.\n",
	}
	for name, content := range files {
		_ = os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}

	return vault.New(dir)
}

func TestIngestService_List(t *testing.T) {
	v := setupIngestVault(t)
	svc := NewIngestService(v)

	items, err := svc.List()
	if err != nil {
		t.Fatal(err)
	}

	if len(items) != 2 {
		t.Fatalf("expected 2 unprocessed items, got %d", len(items))
	}

	paths := map[string]bool{}
	for _, item := range items {
		paths[item.Path] = true
	}
	if !paths["raw/unprocessed.md"] {
		t.Error("expected unprocessed.md")
	}
	if !paths["raw/also-new.md"] {
		t.Error("expected also-new.md")
	}
	if paths["raw/processed.md"] {
		t.Error("should not include processed.md")
	}
}

func TestIngestService_ListEmpty(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "raw"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "raw/done.md"), []byte("---\ntitle: Done\ningested: true\n---\n"), 0o644)

	v := vault.New(dir)
	svc := NewIngestService(v)

	items, err := svc.List()
	if err != nil {
		t.Fatal(err)
	}

	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
}

func TestIngestService_Generate(t *testing.T) {
	v := setupIngestVault(t)
	svc := NewIngestService(v)

	path, count, err := svc.Generate()
	if err != nil {
		t.Fatal(err)
	}

	if count != 2 {
		t.Errorf("expected 2 items, got %d", count)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	content := string(data)
	if !strings.Contains(content, "Ingest Queue") {
		t.Error("missing title in generated queue file")
	}
	if !strings.Contains(content, "raw/unprocessed.md") {
		t.Error("missing unprocessed.md")
	}
	if strings.Contains(content, "raw/processed.md") {
		t.Error("should not contain processed.md")
	}
}
