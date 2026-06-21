package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jedwards1230/my-wiki/internal/vault"
)

func setupPagesVault(t *testing.T) (vault.Storage, string) {
	t.Helper()
	dir := t.TempDir()

	for _, d := range []string{"meta", "project", "raw", "private", ".obsidian"} {
		_ = os.MkdirAll(filepath.Join(dir, d), 0o755)
	}

	files := map[string]string{
		"index.md":                 "---\ntitle: Home\ntags:\n  - root\ndate: 2026-01-01\n---\n\nWelcome.\n",
		"meta/schema.md":           "---\ntitle: Schema\ntags:\n  - meta\ndate: 2026-01-01\n---\n\nSchema content.\n",
		"project/alpha.md":         "---\ntitle: Alpha\ntags:\n  - project\ndate: 2026-02-01\n---\n\nAlpha content.\n",
		"private/secret.md":        "---\ntitle: Secret\n---\n\nPrivate.\n",
		"raw/source.md":            "---\ntitle: Source\n---\n\nRaw.\n",
		".obsidian/workspace.json": "{}\n",
	}

	for name, content := range files {
		_ = os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}

	return vault.NewFilesystemStorage(dir), dir
}

func TestPageService_Read(t *testing.T) {
	storage, _ := setupPagesVault(t)
	svc := NewPageService(storage)

	content, err := svc.Read("index.md")
	if err != nil {
		t.Fatal(err)
	}
	if content == "" {
		t.Fatal("expected content")
	}
}

func TestPageService_ReadWithoutExtension(t *testing.T) {
	storage, _ := setupPagesVault(t)
	svc := NewPageService(storage)

	content, err := svc.Read("index")
	if err != nil {
		t.Fatal(err)
	}
	if content == "" {
		t.Fatal("expected content")
	}
}

func TestPageService_ReadNotFound(t *testing.T) {
	storage, _ := setupPagesVault(t)
	svc := NewPageService(storage)

	_, err := svc.Read("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent page")
	}
	if !strings.Contains(err.Error(), "page not found") {
		t.Errorf("expected 'page not found' error, got: %s", err)
	}
}

func TestPageService_ReadDirectory(t *testing.T) {
	storage, _ := setupPagesVault(t)
	svc := NewPageService(storage)

	// "meta" is a directory, not a page
	_, err := svc.Read("meta")
	if err == nil {
		t.Fatal("expected error reading a directory path")
	}
	if !strings.Contains(err.Error(), "is a directory, not a page") {
		t.Errorf("expected directory error, got: %s", err)
	}
}

func TestPageService_Write(t *testing.T) {
	storage, dir := setupPagesVault(t)
	svc := NewPageService(storage)

	err := svc.Write("new-page.md", "---\ntitle: New\ntags:\n  - test\ndate: 2026-01-15\n---\n\nContent.\n")
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
	storage, dir := setupPagesVault(t)
	svc := NewPageService(storage)

	err := svc.Write("deep/nested/page.md", "---\ntitle: Nested\ntags:\n  - test\ndate: 2026-03-01\n---\n\nContent.\n")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "deep", "nested", "page.md")); err != nil {
		t.Fatal("expected nested file to exist")
	}
}

func TestPageService_Delete(t *testing.T) {
	storage, dir := setupPagesVault(t)
	svc := NewPageService(storage)

	err := svc.Delete("index.md")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "index.md")); !os.IsNotExist(err) {
		t.Fatal("expected file to be deleted")
	}
}

func TestPageService_DeleteNotFound(t *testing.T) {
	storage, _ := setupPagesVault(t)
	svc := NewPageService(storage)

	err := svc.Delete("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent page")
	}
}

func TestPageService_List(t *testing.T) {
	storage, _ := setupPagesVault(t)
	svc := NewPageService(storage)

	pages, err := svc.List(ListOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Should include wiki pages (including private/) but not raw/ or .obsidian/
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
	if !paths["private/secret.md"] {
		t.Error("expected private/secret.md — private/ is no longer special")
	}
	if paths["raw/source.md"] {
		t.Error("should not include raw/")
	}
}

func TestPageService_ListPrefix(t *testing.T) {
	storage, _ := setupPagesVault(t)
	svc := NewPageService(storage)

	pages, err := svc.List(ListOptions{Prefix: "project"})
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
	storage, _ := setupPagesVault(t)
	svc := NewPageService(storage)

	_, err := svc.Read("../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

func TestPageService_WriteValidation(t *testing.T) {
	storage, _ := setupPagesVault(t)
	svc := NewPageService(storage)

	tests := []struct {
		name    string
		path    string
		content string
		wantErr string
	}{
		{
			name:    "valid wiki page",
			path:    "valid.md",
			content: "---\ntitle: Valid Page\ntags:\n  - test\n  - wiki\ndate: 2026-04-01\n---\n\nBody text.\n",
			wantErr: "",
		},
		{
			name:    "missing title",
			path:    "bad.md",
			content: "---\ntags:\n  - test\ndate: 2026-04-01\n---\n\nBody.\n",
			wantErr: "missing required frontmatter field: title",
		},
		{
			name:    "missing tags",
			path:    "bad.md",
			content: "---\ntitle: No Tags\ndate: 2026-04-01\n---\n\nBody.\n",
			wantErr: "missing required frontmatter field: tags (must have at least one tag)",
		},
		{
			name:    "empty tags list",
			path:    "bad.md",
			content: "---\ntitle: Empty Tags\ntags:\ndate: 2026-04-01\n---\n\nBody.\n",
			wantErr: "missing required frontmatter field: tags (must have at least one tag)",
		},
		{
			name:    "missing date",
			path:    "bad.md",
			content: "---\ntitle: No Date\ntags:\n  - test\n---\n\nBody.\n",
			wantErr: "missing required frontmatter field: date",
		},
		{
			name:    "invalid date format",
			path:    "bad.md",
			content: "---\ntitle: Bad Date\ntags:\n  - test\ndate: 2026\n---\n\nBody.\n",
			wantErr: "invalid date format: expected YYYY-MM-DD, got",
		},
		{
			name:    "no frontmatter block",
			path:    "bad.md",
			content: "Just plain text without frontmatter.\n",
			wantErr: "missing frontmatter block",
		},
		{
			name:    "raw file skips validation",
			path:    "raw/anything.md",
			content: "no frontmatter, no rules.\n",
			wantErr: "",
		},
		{
			name:    "raw path traversal does not skip validation",
			path:    "raw/../somepage.md",
			content: "Just plain text without frontmatter.\n",
			wantErr: "missing frontmatter block",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.Write(tc.path, tc.content)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got: %s", err)
				}
			} else {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("expected error containing %q, got: %s", tc.wantErr, err)
				}
			}
		})
	}
}

func TestPageService_Move(t *testing.T) {
	storage, dir := setupPagesVault(t)
	svc := NewPageService(storage)

	err := svc.Move("index", "index-moved")
	if err != nil {
		t.Fatal(err)
	}

	// Source should be gone
	if _, err := os.Stat(filepath.Join(dir, "index.md")); !os.IsNotExist(err) {
		t.Error("expected source to be removed")
	}
	// Destination should exist
	if _, err := os.Stat(filepath.Join(dir, "index-moved.md")); err != nil {
		t.Error("expected destination to exist")
	}
}

func TestPageService_MoveSourceNotFound(t *testing.T) {
	storage, _ := setupPagesVault(t)
	svc := NewPageService(storage)

	err := svc.Move("nonexistent", "somewhere")
	if err == nil {
		t.Fatal("expected error for nonexistent source")
	}
	if !strings.Contains(err.Error(), "source page not found") {
		t.Errorf("expected 'source page not found', got: %s", err)
	}
}

func TestPageService_MoveDestinationExists(t *testing.T) {
	storage, _ := setupPagesVault(t)
	svc := NewPageService(storage)

	err := svc.Move("index", "meta/schema")
	if err == nil {
		t.Fatal("expected error for existing destination")
	}
	if !strings.Contains(err.Error(), "destination already exists") {
		t.Errorf("expected 'destination already exists', got: %s", err)
	}
}

func TestPageService_MoveToNestedPath(t *testing.T) {
	storage, dir := setupPagesVault(t)
	svc := NewPageService(storage)

	err := svc.Move("index", "deep/nested/moved")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "deep", "nested", "moved.md")); err != nil {
		t.Error("expected nested destination to exist")
	}
}

func TestPageService_MoveMutationCallback(t *testing.T) {
	storage, _ := setupPagesVault(t)
	var got *MutationEvent
	svc := NewPageService(storage, WithOnMutation(func(evt MutationEvent) {
		got = &evt
	}))

	err := svc.Move("index", "index-moved")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected mutation callback to fire")
	}
	if got.Kind != MutationMove {
		t.Errorf("expected MutationMove, got %s", got.Kind)
	}
	if !strings.HasSuffix(got.Path, "index-moved.md") {
		t.Errorf("expected path ending in index-moved.md, got %s", got.Path)
	}
}

func TestPageService_PatchValidContent(t *testing.T) {
	storage, _ := setupPagesVault(t)
	svc := NewPageService(storage)

	// Patch index.md (which has valid frontmatter) — result should still be valid
	result, err := svc.Patch("index.md", []PatchOp{
		{Find: "Welcome.", Replace: "Welcome to the wiki."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Welcome to the wiki.") {
		t.Error("expected patched content")
	}
}

func TestPageService_MutationCallbackCreate(t *testing.T) {
	storage, _ := setupPagesVault(t)
	var got *MutationEvent
	svc := NewPageService(storage, WithOnMutation(func(evt MutationEvent) {
		got = &evt
	}))

	err := svc.Write("new-page.md", "---\ntitle: New\ntags:\n  - test\ndate: 2026-01-15\n---\n\nContent.\n")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected mutation callback to fire")
	}
	if got.Kind != MutationCreate {
		t.Errorf("expected MutationCreate, got %s", got.Kind)
	}
	if !strings.HasSuffix(got.Path, "new-page.md") {
		t.Errorf("expected path ending in new-page.md, got %s", got.Path)
	}
}

func TestPageService_MutationCallbackEdit(t *testing.T) {
	storage, _ := setupPagesVault(t)
	var got *MutationEvent
	svc := NewPageService(storage, WithOnMutation(func(evt MutationEvent) {
		got = &evt
	}))

	// index.md already exists
	err := svc.Write("index.md", "---\ntitle: Updated\ntags:\n  - root\ndate: 2026-01-01\n---\n\nUpdated.\n")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected mutation callback to fire")
	}
	if got.Kind != MutationEdit {
		t.Errorf("expected MutationEdit, got %s", got.Kind)
	}
}

func TestPageService_MutationCallbackDelete(t *testing.T) {
	storage, _ := setupPagesVault(t)
	var got *MutationEvent
	svc := NewPageService(storage, WithOnMutation(func(evt MutationEvent) {
		got = &evt
	}))

	err := svc.Delete("index.md")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected mutation callback to fire")
	}
	if got.Kind != MutationDelete {
		t.Errorf("expected MutationDelete, got %s", got.Kind)
	}
}

func TestPageService_MutationCallbackPatch(t *testing.T) {
	storage, _ := setupPagesVault(t)
	var callCount int
	svc := NewPageService(storage, WithOnMutation(func(evt MutationEvent) {
		callCount++
	}))

	_, err := svc.Patch("index.md", []PatchOp{
		{Find: "Welcome.", Replace: "Hello."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if callCount != 1 {
		t.Errorf("expected callback called once (via Write), got %d", callCount)
	}
}

func TestPageService_NoCallbackNoPanic(t *testing.T) {
	storage, _ := setupPagesVault(t)
	svc := NewPageService(storage) // no callback

	err := svc.Write("safe.md", "---\ntitle: Safe\ntags:\n  - test\ndate: 2026-01-15\n---\n\nContent.\n")
	if err != nil {
		t.Fatal(err)
	}

	err = svc.Delete("safe.md")
	if err != nil {
		t.Fatal(err)
	}
}

func TestPageService_CallbackNotCalledOnError(t *testing.T) {
	storage, _ := setupPagesVault(t)
	called := false
	svc := NewPageService(storage, WithOnMutation(func(evt MutationEvent) {
		called = true
	}))

	// Invalid frontmatter should fail validation
	_ = svc.Write("bad.md", "no frontmatter")
	if called {
		t.Error("callback should not fire on validation error")
	}
}

// --- Security: API/HTTP/MCP denylist (.obsidian/) ---

func TestIsAPIDenied(t *testing.T) {
	denied := []string{
		".obsidian", ".obsidian/workspace.json", ".obsidian/nested/deep.json",
		"./.obsidian/workspace.json", ".obsidian/../.obsidian/workspace.json",
	}
	for _, p := range denied {
		if !IsAPIDenied(p) {
			t.Errorf("IsAPIDenied(%q) = false, want true", p)
		}
	}
	// private/ is no longer special — it is a normal, served directory.
	allowed := []string{
		"index.md", "meta/schema.md", "project/alpha.md",
		"private", "private/secret", "private/secret.md", "private/nested/deep.md",
		"raw", "raw/source.md",
	}
	for _, p := range allowed {
		if IsAPIDenied(p) {
			t.Errorf("IsAPIDenied(%q) = true, want false", p)
		}
	}
}

func TestPageService_ReadDeniedReturnsSentinel(t *testing.T) {
	storage, _ := setupPagesVault(t)
	svc := NewPageService(storage)

	for _, p := range []string{".obsidian/workspace.json", ".obsidian"} {
		if _, err := svc.Read(p); !errors.Is(err, ErrPathDenied) {
			t.Errorf("Read(%q) err = %v, want ErrPathDenied", p, err)
		}
	}
}

func TestPageService_ReadRawStillAllowed(t *testing.T) {
	storage, _ := setupPagesVault(t)
	svc := NewPageService(storage)

	// raw/ must remain readable — it is served intentionally and is NOT denied.
	if _, err := svc.Read("raw/source.md"); err != nil {
		t.Errorf("Read(raw/source.md) err = %v, want nil", err)
	}
}

func TestPageService_ReadPrivateNowAllowed(t *testing.T) {
	storage, _ := setupPagesVault(t)
	svc := NewPageService(storage)

	// private/ is a normal directory now — it must be readable.
	if _, err := svc.Read("private/secret.md"); err != nil {
		t.Errorf("Read(private/secret.md) err = %v, want nil", err)
	}
}

func TestPageService_WriteDeniedReturnsSentinel(t *testing.T) {
	storage, dir := setupPagesVault(t)
	svc := NewPageService(storage)

	content := "---\ntitle: X\ntags:\n  - t\ndate: 2026-01-01\n---\n\nbody\n"
	if err := svc.Write(".obsidian/new.md", content); !errors.Is(err, ErrPathDenied) {
		t.Fatalf("Write(.obsidian/new.md) err = %v, want ErrPathDenied", err)
	}
	// Ensure no file was actually written.
	if _, err := os.Stat(filepath.Join(dir, ".obsidian", "new.md")); err == nil {
		t.Error(".obsidian/new.md was written despite denial")
	}
}

func TestPageService_DeleteDeniedReturnsSentinel(t *testing.T) {
	storage, dir := setupPagesVault(t)
	svc := NewPageService(storage)

	if err := svc.Delete(".obsidian/workspace.json"); !errors.Is(err, ErrPathDenied) {
		t.Fatalf("Delete(.obsidian/workspace.json) err = %v, want ErrPathDenied", err)
	}
	// File must still exist.
	if _, err := os.Stat(filepath.Join(dir, ".obsidian", "workspace.json")); err != nil {
		t.Error(".obsidian/workspace.json was deleted despite denial")
	}
}

func TestPageService_MoveDeniedBothEnds(t *testing.T) {
	storage, _ := setupPagesVault(t)
	svc := NewPageService(storage)

	// Denied source.
	if err := svc.Move(".obsidian/workspace.json", "project/leaked.md"); !errors.Is(err, ErrPathDenied) {
		t.Errorf("Move(.obsidian src) err = %v, want ErrPathDenied", err)
	}
	// Denied destination.
	if err := svc.Move("index.md", ".obsidian/exfil.md"); !errors.Is(err, ErrPathDenied) {
		t.Errorf("Move(.obsidian dst) err = %v, want ErrPathDenied", err)
	}
}

func TestPageService_PatchDeniedReturnsSentinel(t *testing.T) {
	storage, _ := setupPagesVault(t)
	svc := NewPageService(storage)

	if _, err := svc.Patch(".obsidian/workspace.json", []PatchOp{{Find: "{}", Replace: "[]"}}); !errors.Is(err, ErrPathDenied) {
		t.Errorf("Patch(.obsidian) err = %v, want ErrPathDenied", err)
	}
}

func TestPageService_WriteSlugifiesDestination(t *testing.T) {
	storage, dir := setupPagesVault(t)
	svc := NewPageService(storage)

	// An agent-supplied path with smart punctuation and spaces must land on a
	// server-assigned slug, not the raw name.
	content := "---\ntitle: Murati\ntags:\n  - research\ndate: 2026-06-11\n---\n\nBody.\n"
	if err := svc.Write("research/clippings/Thinking Machines’ Murati on AI’s Next Chapter.md", content); err != nil {
		t.Fatal(err)
	}

	wantRel := "research/clippings/thinking-machines-murati-on-ais-next-chapter.md"
	if _, err := os.Stat(filepath.Join(dir, wantRel)); err != nil {
		t.Fatalf("expected slugified file at %s: %v", wantRel, err)
	}
	// The raw name must not exist on disk.
	if _, err := os.Stat(filepath.Join(dir, "research/clippings/Thinking Machines’ Murati on AI’s Next Chapter.md")); !os.IsNotExist(err) {
		t.Fatalf("raw filename should not exist, got err=%v", err)
	}
	// And it is addressable by the slug.
	if _, err := svc.Read(wantRel); err != nil {
		t.Fatalf("slugified page not readable: %v", err)
	}
}

func TestPageService_MoveSlugifiesDestination(t *testing.T) {
	storage, dir := setupPagesVault(t)
	svc := NewPageService(storage)

	if err := svc.Move("project/alpha.md", "research/clippings/Staff Archetypes.md"); err != nil {
		t.Fatal(err)
	}

	wantRel := "research/clippings/staff-archetypes.md"
	if _, err := os.Stat(filepath.Join(dir, wantRel)); err != nil {
		t.Fatalf("expected slugified destination at %s: %v", wantRel, err)
	}
}
