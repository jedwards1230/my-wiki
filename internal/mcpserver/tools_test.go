package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jedwards1230/my-wiki/internal/middleware"
	"github.com/jedwards1230/my-wiki/internal/service"
	"github.com/jedwards1230/my-wiki/internal/vault"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func setupTestVault(t *testing.T) *vault.Vault {
	t.Helper()
	dir := t.TempDir()

	for _, d := range []string{"raw", "meta", "meta/activity", "project", "private", ".obsidian"} {
		_ = os.MkdirAll(filepath.Join(dir, d), 0o755)
	}

	files := map[string]string{
		"index.md":           "---\ntitle: Home\ntags:\n  - root\ndate: 2026-01-01\n---\n\n[[about]]\n",
		"about.md":           "---\ntitle: About\ntags:\n  - info\ndate: 2026-01-01\n---\n\n[[index]]\n",
		"project/alpha.md":   "---\ntitle: Alpha\ntags:\n  - project\ndate: 2026-02-01\n---\n\nContent.\n",
		"raw/unprocessed.md": "---\ntitle: Unprocessed\nsource: https://example.com\ndate-added: 2026-01-15\n---\n\nContent.\n",
	}
	for name, content := range files {
		_ = os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}

	logContent := "---\ntitle: Activity Log\n---\n\n## [[meta/activity/2026-04-06|2026-04-06]] 1 changes | abcdef | Test\n"
	_ = os.WriteFile(filepath.Join(dir, "meta", "log.md"), []byte(logContent), 0o644)

	actContent := "---\ntitle: \"2026-04-06\"\ntags:\n  - meta/activity\ndate: 2026-04-06\n---\n\n### 10:00 | create | First thing\nCreated a page.\n"
	_ = os.WriteFile(filepath.Join(dir, "meta", "activity", "2026-04-06.md"), []byte(actContent), 0o644)

	return vault.New(dir)
}

func makeReq(args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: args,
		},
	}
}

func testServer() *server.MCPServer {
	return server.NewMCPServer("test", "0.0.0", server.WithLogging())
}

func getTextContent(result *mcp.CallToolResult) string {
	if result == nil || len(result.Content) == 0 {
		return ""
	}
	tc, ok := mcp.AsTextContent(result.Content[0])
	if !ok {
		return ""
	}
	return tc.Text
}

// --- read ---

func TestReadHandler(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	handler := readHandler(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{"path": "index.md"}))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if !strings.Contains(text, "Home") {
		t.Errorf("expected page content, got:\n%s", text)
	}
}

func TestReadHandlerNotFound(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	handler := readHandler(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{"path": "nonexistent"}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error result for missing page")
	}
}

func TestReadHandlerEmptyPath(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	handler := readHandler(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error result for empty path")
	}
}

// --- write ---

func TestWriteHandlerCreateNew(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := writeHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"path":    "new-page.md",
		"title":   "New",
		"tags":    []interface{}{"test"},
		"date":    "2026-01-15",
		"content": "Content.\n",
	}))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if !strings.Contains(text, "Wrote") {
		t.Errorf("expected wrote message, got:\n%s", text)
	}

	// Verify file exists and has assembled frontmatter
	data, err := os.ReadFile(filepath.Join(v.Dir, "new-page.md"))
	if err != nil {
		t.Fatal("expected file to exist after write")
	}
	content := string(data)
	if !strings.Contains(content, "title: New") {
		t.Error("expected assembled frontmatter with title")
	}
	if !strings.Contains(content, "  - test") {
		t.Error("expected assembled frontmatter with tags")
	}
	if !strings.Contains(content, "date: 2026-01-15") {
		t.Error("expected assembled frontmatter with date")
	}
	if !strings.Contains(content, "Content.\n") {
		t.Error("expected body content after frontmatter")
	}
}

func TestWriteHandlerDateDefaultsToToday(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := writeHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"path":    "no-date.md",
		"title":   "No Date",
		"tags":    []interface{}{"test"},
		"content": "Body.\n",
	}))
	if err != nil {
		t.Fatal(err)
	}

	if result.IsError {
		t.Errorf("expected success, got error: %s", getTextContent(result))
	}

	data, err := os.ReadFile(filepath.Join(v.Dir, "no-date.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	today := time.Now().Format("2006-01-02")
	if !strings.Contains(string(data), "date: "+today) {
		t.Errorf("expected date to default to today (%s), got:\n%s", today, string(data))
	}
}

func TestWriteHandlerWithDescription(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := writeHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"path":        "with-desc.md",
		"title":       "Described",
		"tags":        []interface{}{"test"},
		"date":        "2026-03-01",
		"description": "A short summary.",
		"content":     "Body.\n",
	}))
	if err != nil {
		t.Fatal(err)
	}

	if result.IsError {
		t.Errorf("expected success, got error: %s", getTextContent(result))
	}

	data, err := os.ReadFile(filepath.Join(v.Dir, "with-desc.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "description: A short summary.") {
		t.Errorf("expected description in frontmatter, got:\n%s", string(data))
	}
}

func TestWriteHandlerWithExtraFrontmatter(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := writeHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"path":              "with-extra.md",
		"title":             "Extra",
		"tags":              []interface{}{"test"},
		"date":              "2026-03-01",
		"extra_frontmatter": "status: wip\nsource: https://example.com",
		"content":           "Body.\n",
	}))
	if err != nil {
		t.Fatal(err)
	}

	if result.IsError {
		t.Errorf("expected success, got error: %s", getTextContent(result))
	}

	data, err := os.ReadFile(filepath.Join(v.Dir, "with-extra.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "status: wip") {
		t.Errorf("expected extra_frontmatter 'status: wip', got:\n%s", content)
	}
	if !strings.Contains(content, "source: https://example.com") {
		t.Errorf("expected extra_frontmatter 'source' line, got:\n%s", content)
	}
}

func TestWriteHandlerAllOptionalFields(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := writeHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"path":              "full-page.md",
		"title":             "Full Page",
		"tags":              []interface{}{"project", "go"},
		"date":              "2026-04-14",
		"description":       "Everything included.",
		"extra_frontmatter": "status: active\npriority: high",
		"content":           "All fields present.\n",
	}))
	if err != nil {
		t.Fatal(err)
	}

	if result.IsError {
		t.Errorf("expected success, got error: %s", getTextContent(result))
	}

	data, err := os.ReadFile(filepath.Join(v.Dir, "full-page.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)

	for _, want := range []string{
		"title: Full Page",
		"  - project",
		"  - go",
		"date: 2026-04-14",
		"description: Everything included.",
		"status: active",
		"priority: high",
		"All fields present.",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("expected %q in output, got:\n%s", want, content)
		}
	}

	// Verify frontmatter structure: starts with --- and has closing ---
	if !strings.HasPrefix(content, "---\n") {
		t.Fatal("expected content to start with ---")
		return
	}
	rest := content[4:]
	if !strings.Contains(rest, "---\n") {
		t.Error("expected closing --- in frontmatter")
	}
}

func TestWriteHandlerOverwriteExisting(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := writeHandler(testServer(), svc, lint, v.Dir, nil)

	// Overwrite existing index.md — should succeed (no existence check)
	result, err := handler(context.Background(), makeReq(map[string]any{
		"path":    "index.md",
		"title":   "Updated Home",
		"tags":    []interface{}{"root"},
		"date":    "2026-01-01",
		"content": "Updated.\n",
	}))
	if err != nil {
		t.Fatal(err)
	}

	if result.IsError {
		t.Error("expected success when overwriting existing page")
	}

	// Verify content was updated
	data, err := os.ReadFile(filepath.Join(v.Dir, "index.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "Updated Home") {
		t.Error("expected content to be overwritten")
	}
}

func TestWriteHandlerEmptyContent(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := writeHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"path":    "empty.md",
		"title":   "Empty",
		"tags":    []interface{}{"test"},
		"content": "",
	}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error result for empty content")
	}
}

func TestWriteHandlerEmptyPath(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := writeHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"title":   "X",
		"tags":    []interface{}{"t"},
		"content": "Body.\n",
	}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error result for empty path")
	}
}

func TestWriteHandlerEmptyTitle(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := writeHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"path":    "no-title.md",
		"tags":    []interface{}{"t"},
		"content": "Body.\n",
	}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error result for empty title")
	}
}

func TestWriteHandlerEmptyTags(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := writeHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"path":    "no-tags.md",
		"title":   "No Tags",
		"content": "Body.\n",
	}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error result for empty tags")
	}
}

func TestWriteHandlerExtraFrontmatterDelimiter(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := writeHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"path":              "bad-extra.md",
		"title":             "Bad",
		"tags":              []interface{}{"test"},
		"content":           "Body.\n",
		"extra_frontmatter": "status: wip\n---\ninjected: true",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error when extra_frontmatter contains ---")
	}
}

func TestWriteHandlerExtraFrontmatterReservedKey(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := writeHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"path":              "bad-extra2.md",
		"title":             "Bad",
		"tags":              []interface{}{"test"},
		"content":           "Body.\n",
		"extra_frontmatter": "title: Override",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error when extra_frontmatter redefines reserved key")
	}
}

func TestWriteHandlerLintWarnings(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := writeHandler(testServer(), svc, lint, v.Dir, nil)

	// Create a page with a broken wikilink.
	result, err := handler(context.Background(), makeReq(map[string]any{
		"path":    "broken.md",
		"title":   "Broken",
		"tags":    []interface{}{"test"},
		"date":    "2026-01-15",
		"content": "[[nonexistent-target]]\n",
	}))
	if err != nil {
		t.Fatal(err)
	}

	if result.IsError {
		t.Error("expected successful result")
	}

	if len(result.Content) < 2 {
		t.Fatalf("expected at least 2 content items (result + warnings), got %d", len(result.Content))
	}

	tc, ok := mcp.AsTextContent(result.Content[1])
	if !ok {
		t.Fatal("expected second content item to be TextContent")
	}
	if !strings.Contains(tc.Text, "nonexistent-target") {
		t.Errorf("expected warning about [[nonexistent-target]], got:\n%s", tc.Text)
	}
}

// --- edit ---

func TestEditHandler(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := editHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"path": "project/alpha",
		"operations": []interface{}{
			map[string]interface{}{"find": "Content.", "replace": "Updated content."},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if !strings.Contains(text, "Updated content.") {
		t.Errorf("expected patched content, got:\n%s", text)
	}

	// Verify the file was actually updated
	rh := readHandler(svc)
	readResult, err := rh(context.Background(), makeReq(map[string]any{"path": "project/alpha"}))
	if err != nil {
		t.Fatal(err)
	}
	readText := getTextContent(readResult)
	if !strings.Contains(readText, "Updated content.") {
		t.Errorf("expected updated content on re-read, got:\n%s", readText)
	}
}

func TestEditHandlerFindNotFound(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := editHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"path": "project/alpha",
		"operations": []interface{}{
			map[string]interface{}{"find": "nonexistent text", "replace": "replacement"},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error result for missing find string")
	}
}

func TestEditHandlerEmptyPath(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := editHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"operations": []interface{}{
			map[string]interface{}{"find": "x", "replace": "y"},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error result for empty path")
	}
}

func TestEditHandlerEmptyOperations(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := editHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"path":       "project/alpha",
		"operations": []interface{}{},
	}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error result for empty operations")
	}
}

func TestEditHandlerPageNotFound(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := editHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"path": "nonexistent",
		"operations": []interface{}{
			map[string]interface{}{"find": "x", "replace": "y"},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error result for nonexistent page")
	}
}

func TestEditHandlerInvalidYAMLWarning(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := editHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"path": "index.md",
		"operations": []any{
			map[string]any{
				"find":    "  - root",
				"replace": "  - [unclosed bracket",
			},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}

	if result.IsError {
		t.Errorf("expected successful result, got error: %s", getTextContent(result))
	}

	if len(result.Content) < 2 {
		t.Fatalf("expected at least 2 content items (result + warnings), got %d", len(result.Content))
	}

	tc, ok := mcp.AsTextContent(result.Content[1])
	if !ok {
		t.Fatal("expected second content item to be TextContent")
	}
	if !strings.Contains(tc.Text, "invalid YAML") {
		t.Errorf("expected 'invalid YAML' warning, got:\n%s", tc.Text)
	}
}

// --- list ---

func TestListHandlerSimple(t *testing.T) {
	v := setupTestVault(t)
	pageSvc := service.NewPageService(v.Storage)
	dirSvc := service.NewDirectoryService(v)
	handler := listHandler(pageSvc, dirSvc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if !strings.Contains(text, "index.md") {
		t.Errorf("expected index.md in result, got:\n%s", text)
	}
	// Simple mode should have has_meta field
	if !strings.Contains(text, "has_meta") {
		t.Errorf("expected has_meta field in simple listing, got:\n%s", text)
	}
}

func TestListHandlerWithPrefix(t *testing.T) {
	v := setupTestVault(t)
	pageSvc := service.NewPageService(v.Storage)
	dirSvc := service.NewDirectoryService(v)
	handler := listHandler(pageSvc, dirSvc)

	result, err := handler(context.Background(), makeReq(map[string]any{"prefix": "project"}))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if !strings.Contains(text, "project/alpha.md") {
		t.Errorf("expected project/alpha.md in result, got:\n%s", text)
	}
	if strings.Contains(text, "index.md") {
		t.Errorf("should not contain index.md with project prefix, got:\n%s", text)
	}
}

func TestListHandlerDetail(t *testing.T) {
	v := setupTestVault(t)
	pageSvc := service.NewPageService(v.Storage)
	dirSvc := service.NewDirectoryService(v)
	handler := listHandler(pageSvc, dirSvc)

	result, err := handler(context.Background(), makeReq(map[string]any{"detail": true}))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if !strings.Contains(text, "index.md") {
		t.Errorf("expected index.md in detail result, got:\n%s", text)
	}
	if !strings.Contains(text, "Home") {
		t.Errorf("expected title in detail result, got:\n%s", text)
	}
}

func TestListHandlerDetailWithPrefix(t *testing.T) {
	v := setupTestVault(t)
	pageSvc := service.NewPageService(v.Storage)
	dirSvc := service.NewDirectoryService(v)
	handler := listHandler(pageSvc, dirSvc)

	result, err := handler(context.Background(), makeReq(map[string]any{"prefix": "project", "detail": true}))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if !strings.Contains(text, "project/alpha.md") {
		t.Errorf("expected project/alpha.md in result, got:\n%s", text)
	}
	if strings.Contains(text, "index.md") {
		t.Errorf("should not contain index.md with project prefix, got:\n%s", text)
	}
}

// --- delete ---

func TestDeleteHandler(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := deleteHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{"path": "about.md"}))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if !strings.Contains(text, "deleted") {
		t.Errorf("expected deleted message, got:\n%s", text)
	}

	// Verify the file is gone
	rh := readHandler(svc)
	result, err = rh(context.Background(), makeReq(map[string]any{"path": "about.md"}))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error reading deleted page")
	}
}

func TestDeleteHandlerNotFound(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := deleteHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{"path": "nonexistent"}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error result for missing page")
	}
}

func TestDeleteHandlerEmptyPath(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := deleteHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error result for empty path")
	}
}

func TestDeleteHandlerLintWarnings(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := deleteHandler(testServer(), svc, lint, v.Dir, nil)

	// about.md is linked from index.md — deleting it should produce warnings.
	result, err := handler(context.Background(), makeReq(map[string]any{"path": "about.md"}))
	if err != nil {
		t.Fatal(err)
	}

	if result.IsError {
		t.Error("expected successful result")
	}

	if len(result.Content) < 2 {
		t.Fatalf("expected at least 2 content items (result + warnings), got %d", len(result.Content))
	}

	tc, ok := mcp.AsTextContent(result.Content[1])
	if !ok {
		t.Fatal("expected second content item to be TextContent")
	}
	if !strings.Contains(tc.Text, "about") {
		t.Errorf("expected warning about broken link to about, got:\n%s", tc.Text)
	}
}

// --- move ---

func TestMoveHandler(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := moveHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"source":      "project/alpha",
		"destination": "project/beta",
	}))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if !strings.Contains(text, "moved") {
		t.Errorf("expected moved message, got:\n%s", text)
	}

	// Verify source is gone
	if _, err := os.Stat(filepath.Join(v.Dir, "project", "alpha.md")); !os.IsNotExist(err) {
		t.Error("expected source file to be removed")
	}
	// Verify destination exists
	if _, err := os.Stat(filepath.Join(v.Dir, "project", "beta.md")); err != nil {
		t.Error("expected destination file to exist")
	}
}

func TestMoveHandlerSourceNotFound(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := moveHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"source":      "nonexistent",
		"destination": "project/beta",
	}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error for nonexistent source")
	}
	text := getTextContent(result)
	if !strings.Contains(text, "source page not found") {
		t.Errorf("expected 'source page not found', got:\n%s", text)
	}
}

func TestMoveHandlerDestinationExists(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := moveHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"source":      "about",
		"destination": "index",
	}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error for existing destination")
	}
	text := getTextContent(result)
	if !strings.Contains(text, "destination already exists") {
		t.Errorf("expected 'destination already exists', got:\n%s", text)
	}
}

func TestMoveHandlerEmptySource(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := moveHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"destination": "project/beta",
	}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error for empty source")
	}
}

func TestMoveHandlerEmptyDestination(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Storage)
	lint := service.NewLintService(v, nil)
	handler := moveHandler(testServer(), svc, lint, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"source": "about",
	}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error for empty destination")
	}
}

// --- recent ---

func TestListHandler_SortByModified(t *testing.T) {
	v := setupTestVault(t)

	now := time.Now()
	oldest := now.Add(-3 * time.Hour)
	middle := now.Add(-2 * time.Hour)
	newest := now.Add(-1 * time.Hour)

	_ = os.Chtimes(filepath.Join(v.Dir, "index.md"), oldest, oldest)
	_ = os.Chtimes(filepath.Join(v.Dir, "about.md"), middle, middle)
	_ = os.Chtimes(filepath.Join(v.Dir, "project", "alpha.md"), newest, newest)

	pageSvc := service.NewPageService(v.Storage)
	dirSvc := service.NewDirectoryService(v)
	handler := listHandler(pageSvc, dirSvc)

	result, err := handler(context.Background(), makeReq(map[string]any{"sort_by": "modified"}))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if !strings.Contains(text, "index.md") {
		t.Errorf("expected index.md in result, got:\n%s", text)
	}
	if !strings.Contains(text, "project/alpha.md") {
		t.Errorf("expected project/alpha.md in result, got:\n%s", text)
	}

	alphaIdx := strings.Index(text, "project/alpha.md")
	aboutIdx := strings.Index(text, "about.md")
	indexIdx := strings.Index(text, "index.md")
	if alphaIdx > aboutIdx || aboutIdx > indexIdx {
		t.Errorf("expected newest-first order (alpha, about, index), got alpha@%d about@%d index@%d", alphaIdx, aboutIdx, indexIdx)
	}

	if strings.Contains(text, "meta/activity/") {
		t.Error("expected meta/activity/ files to be excluded")
	}

	// Test with limit
	result, err = handler(context.Background(), makeReq(map[string]any{"sort_by": "modified", "limit": float64(2)}))
	if err != nil {
		t.Fatal(err)
	}
	text = getTextContent(result)
	if strings.Contains(text, "index.md") {
		t.Errorf("expected index.md to be excluded with limit=2, got:\n%s", text)
	}
}

// --- activity ---

func TestActivityHandler(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewActivityService(v.Storage)
	handler := activityHandler(testServer(), svc, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"type":  "note",
		"title": "Test Note",
		"time":  "15:00",
	}))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if !strings.Contains(text, "successfully") {
		t.Errorf("expected success message, got:\n%s", text)
	}
}

func TestActivityHandlerInvalidType(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewActivityService(v.Storage)
	handler := activityHandler(testServer(), svc, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"type":  "invalid",
		"title": "Test",
	}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error result for invalid type")
	}
}

// --- search ---

func TestSearchHandlerEmptyQuery(t *testing.T) {
	handler := searchHandler(&service.SearchService{})

	result, err := handler(context.Background(), makeReq(map[string]any{"query": ""}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error for empty query")
	}
}

func TestSearchHandlerShortQuery(t *testing.T) {
	handler := searchHandler(&service.SearchService{})

	result, err := handler(context.Background(), makeReq(map[string]any{"query": "a"}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error for single-character query")
	}
}

// --- New creates server ---

func TestWhoamiHandler(t *testing.T) {
	v := setupTestVault(t)
	handler := whoamiHandler(v.Dir, "")

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if !strings.Contains(text, `"name": "home-wiki"`) {
		t.Errorf("expected name field, got:\n%s", text)
	}
	if !strings.Contains(text, `"version"`) {
		t.Errorf("expected version field, got:\n%s", text)
	}
	// No auth context — user should be absent.
	if strings.Contains(text, `"user"`) {
		t.Errorf("expected no user field without auth, got:\n%s", text)
	}
}

func TestWhoamiHandler_WithUser(t *testing.T) {
	v := setupTestVault(t)
	handler := whoamiHandler(v.Dir, "")

	ctx := middleware.WithUser(context.Background(), &middleware.UserInfo{
		Username: "agent",
		Email:    "agent@example.com",
		Name:     "Agent Smith",
		Groups:   []string{"editors"},
	})

	result, err := handler(ctx, makeReq(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if !strings.Contains(text, `"username": "agent"`) {
		t.Errorf("expected username in user field, got:\n%s", text)
	}
	if !strings.Contains(text, `"email": "agent@example.com"`) {
		t.Errorf("expected email in user field, got:\n%s", text)
	}
}

func TestNewCreatesServer(t *testing.T) {
	v := setupTestVault(t)
	s := New(v, nil)
	if s == nil {
		t.Fatal("expected non-nil MCP server")
	}
}

// TestWhoamiHandler_WithInstanceName verifies that the instance_name field
// is included when set, supporting per-vault identity for stdio mode.
func TestWhoamiHandler_WithInstanceName(t *testing.T) {
	v := setupTestVault(t)
	handler := whoamiHandler(v.Dir, "work-wiki")

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if !strings.Contains(text, `"instance_name": "work-wiki"`) {
		t.Errorf("expected instance_name field, got:\n%s", text)
	}
}

// TestWhoamiHandler_EmptyInstanceNameOmitted verifies backwards compatibility:
// when instance_name is empty (HTTP serve path), the field is omitted entirely.
func TestWhoamiHandler_EmptyInstanceNameOmitted(t *testing.T) {
	v := setupTestVault(t)
	handler := whoamiHandler(v.Dir, "")

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if strings.Contains(text, "instance_name") {
		t.Errorf("expected instance_name to be omitted when empty, got:\n%s", text)
	}
}

// TestNewCreatesServer_WithInstanceName verifies the option threads through
// New into whoami end-to-end.
func TestNewCreatesServer_WithInstanceName(t *testing.T) {
	v := setupTestVault(t)
	s := New(v, nil, WithInstanceName("custom-instance"))
	if s == nil {
		t.Fatal("expected non-nil MCP server")
	}
	// The exported MCPServer doesn't expose registered handlers directly,
	// so the handler-level test above (TestWhoamiHandler_WithInstanceName)
	// is the round-trip. This case asserts construction succeeds with the
	// option set.
}
