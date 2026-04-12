package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jedwards1230/home-wiki/internal/service"
	"github.com/jedwards1230/home-wiki/internal/vault"
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

func TestDirectoryListHandler(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewDirectoryService(v)
	handler := directoryListHandler(svc)

	result, err := handler(context.Background(), makeReq(nil))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if !strings.Contains(text, "index.md") {
		t.Errorf("expected index.md in result, got:\n%s", text)
	}
	if !strings.Contains(text, "Home") {
		t.Errorf("expected title in result, got:\n%s", text)
	}
}

func TestDirectoryGenerateHandler(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewDirectoryService(v)
	handler := directoryGenerateHandler(testServer(), svc)

	result, err := handler(context.Background(), makeReq(nil))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if !strings.Contains(text, "pages_indexed") {
		t.Errorf("expected pages_indexed in result, got:\n%s", text)
	}

	// Verify root index was created
	indexFile := filepath.Join(v.Dir, "index.md")
	if _, err := os.Stat(indexFile); os.IsNotExist(err) {
		t.Error("expected index.md to be created by directory generate")
	}
}

func TestIngestListHandler(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewIngestService(v)
	handler := ingestListHandler(svc)

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
	svc := service.NewActivityService(v.Dir)
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
	handler := createPageHandler(testServer(), svc, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"path":    "new-page.md",
		"content": "---\ntitle: New\ntags:\n  - test\ndate: 2026-01-15\n---\n\nContent.\n",
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
	handler := createPageHandler(testServer(), svc, v.Dir, nil)

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
	handler := createPageHandler(testServer(), svc, v.Dir, nil)

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
	handler := updatePageHandler(testServer(), svc, v.Dir, nil)

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
	handler := updatePageHandler(testServer(), svc, v.Dir, nil)

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

func TestDeletePageHandler(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Dir)
	handler := deletePageHandler(testServer(), svc, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{"path": "about.md"}))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)
	if !strings.Contains(text, "deleted") {
		t.Errorf("expected deleted message, got:\n%s", text)
	}

	// Verify the file is gone
	readHandler := readPageHandler(svc)
	result, err = readHandler(context.Background(), makeReq(map[string]any{"path": "about.md"}))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error reading deleted page")
	}
}

func TestDeletePageHandlerNotFound(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Dir)
	handler := deletePageHandler(testServer(), svc, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{"path": "nonexistent"}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error result for missing page")
	}
}

func TestDeletePageHandlerEmptyPath(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Dir)
	handler := deletePageHandler(testServer(), svc, v.Dir, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Error("expected error result for empty path")
	}
}

func TestPatchPageHandler(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Dir)
	handler := patchPageHandler(testServer(), svc, v.Dir, nil)

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
	readHandler := readPageHandler(svc)
	readResult, err := readHandler(context.Background(), makeReq(map[string]any{"path": "project/alpha"}))
	if err != nil {
		t.Fatal(err)
	}
	readText := getTextContent(readResult)
	if !strings.Contains(readText, "Updated content.") {
		t.Errorf("expected updated content on re-read, got:\n%s", readText)
	}
}

func TestPatchPageHandlerFindNotFound(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Dir)
	handler := patchPageHandler(testServer(), svc, v.Dir, nil)

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

func TestPatchPageHandlerEmptyPath(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Dir)
	handler := patchPageHandler(testServer(), svc, v.Dir, nil)

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

func TestPatchPageHandlerEmptyOperations(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Dir)
	handler := patchPageHandler(testServer(), svc, v.Dir, nil)

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

func TestPatchPageHandlerPageNotFound(t *testing.T) {
	v := setupTestVault(t)
	svc := service.NewPageService(v.Dir)
	handler := patchPageHandler(testServer(), svc, v.Dir, nil)

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

func TestRecentListHandler(t *testing.T) {
	v := setupTestVault(t)

	// Set distinct mtimes so we can verify sort order.
	// index.md = oldest, about.md = middle, project/alpha.md = newest
	now := time.Now()
	oldest := now.Add(-3 * time.Hour)
	middle := now.Add(-2 * time.Hour)
	newest := now.Add(-1 * time.Hour)

	_ = os.Chtimes(filepath.Join(v.Dir, "index.md"), oldest, oldest)
	_ = os.Chtimes(filepath.Join(v.Dir, "about.md"), middle, middle)
	_ = os.Chtimes(filepath.Join(v.Dir, "project", "alpha.md"), newest, newest)

	svc := service.NewRecentService(v)
	handler := recentListHandler(svc)

	// Test default (all pages returned)
	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}

	text := getTextContent(result)

	// Should contain pages
	if !strings.Contains(text, "index.md") {
		t.Errorf("expected index.md in result, got:\n%s", text)
	}
	if !strings.Contains(text, "project/alpha.md") {
		t.Errorf("expected project/alpha.md in result, got:\n%s", text)
	}

	// Verify sort order: alpha should appear before about, about before index
	alphaIdx := strings.Index(text, "project/alpha.md")
	aboutIdx := strings.Index(text, "about.md")
	indexIdx := strings.Index(text, "index.md")
	if alphaIdx > aboutIdx || aboutIdx > indexIdx {
		t.Errorf("expected newest-first order (alpha, about, index), got alpha@%d about@%d index@%d", alphaIdx, aboutIdx, indexIdx)
	}

	// meta/activity files should be excluded
	if strings.Contains(text, "meta/activity/") {
		t.Error("expected meta/activity/ files to be excluded")
	}

	// Test with limit
	result, err = handler(context.Background(), makeReq(map[string]any{"limit": float64(2)}))
	if err != nil {
		t.Fatal(err)
	}
	text = getTextContent(result)

	// Should have at most 2 entries — index.md (oldest) should be absent
	if strings.Contains(text, "index.md") {
		t.Errorf("expected index.md to be excluded with limit=2, got:\n%s", text)
	}
}

func TestNewCreatesServer(t *testing.T) {
	v := setupTestVault(t)
	s := New(v, nil)
	if s == nil {
		t.Fatal("expected non-nil MCP server")
	}
}
