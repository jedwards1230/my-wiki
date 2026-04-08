package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jedwards1230/home-wiki/internal/service"
	"github.com/jedwards1230/home-wiki/internal/vault"
	"github.com/mark3labs/mcp-go/mcp"
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

func TestLintHandler(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewLintService(v)
	handler := lintHandler(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{"check": "all"}))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if text == "" {
		t.Fatal("expected non-empty result")
	}
	if !strings.Contains(text, "total") {
		t.Errorf("expected JSON with total field, got:\n%s", text)
	}
}

func TestLintHandlerInvalidCheck(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewLintService(v)
	handler := lintHandler(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{"check": "invalid"}))
	if err != nil {
		t.Fatal(err)
	}

	// Should return an error result, not a Go error
	if !result.IsError {
		t.Error("expected error result for invalid check")
	}
}

func TestQueueListHandler(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewQueueService(v)
	handler := queueListHandler(svc)

	result, err := handler(context.Background(), makeReq(nil))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if !strings.Contains(text, "unprocessed") {
		t.Errorf("expected unprocessed in result, got:\n%s", text)
	}
}

func TestLogIndexHandler(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewLogService(v.Dir)
	handler := logIndexHandler(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if !strings.Contains(text, "2026-04-06") {
		t.Errorf("expected date in result, got:\n%s", text)
	}
}

func TestLogDayHandler(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewLogService(v.Dir)
	handler := logDayHandler(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"date":   "2026-04-06",
		"detail": true,
	}))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if !strings.Contains(text, "First thing") {
		t.Errorf("expected entry in result, got:\n%s", text)
	}
}

func TestLogDayHandlerMissing(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewLogService(v.Dir)
	handler := logDayHandler(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{"date": "2099-01-01"}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error result for missing day")
	}
}

func TestActivityHandler(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewActivityService(v.Dir)
	handler := activityHandler(svc)

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
	svc := service.NewActivityService(v.Dir)
	handler := activityHandler(svc)

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

func TestReadPageHandler(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Dir)
	handler := readPageHandler(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{"path": "index.md"}))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if !strings.Contains(text, "Home") {
		t.Errorf("expected page content, got:\n%s", text)
	}
}

func TestReadPageHandlerNotFound(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Dir)
	handler := readPageHandler(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{"path": "nonexistent"}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error result for missing page")
	}
}

func TestCreatePageHandler(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Dir)
	handler := createPageHandler(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"path":    "new-page.md",
		"content": "---\ntitle: New\n---\n\nContent.\n",
	}))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if !strings.Contains(text, "Created") {
		t.Errorf("expected created message, got:\n%s", text)
	}
}

func TestCreatePageHandlerAlreadyExists(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Dir)
	handler := createPageHandler(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"path":    "index.md",
		"content": "new content",
	}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error result for existing page")
	}
}

func TestCreatePageHandlerEmptyContent(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Dir)
	handler := createPageHandler(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"path":    "empty-page.md",
		"content": "",
	}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error result for empty content")
	}
}

func TestUpdatePageHandlerNonExistent(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Dir)
	handler := updatePageHandler(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"path":    "does-not-exist.md",
		"content": "---\ntitle: Ghost\n---\n\nContent.\n",
	}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error result for non-existent page")
	}
	text := getTextContent(result)
	if !strings.Contains(text, "page not found") {
		t.Errorf("expected 'page not found' message, got:\n%s", text)
	}
}

func TestUpdatePageHandlerEmptyContent(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Dir)
	handler := updatePageHandler(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"path":    "index.md",
		"content": "",
	}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error result for empty content")
	}
}

func TestListPagesHandler(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Dir)
	handler := listPagesHandler(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if !strings.Contains(text, "index.md") {
		t.Errorf("expected index.md in result, got:\n%s", text)
	}
}

func TestNewCreatesServer(t *testing.T) {
	v := setupTestVault(t)
	s := New(v)
	if s == nil {
		t.Fatal("expected non-nil MCP server")
	}
}
