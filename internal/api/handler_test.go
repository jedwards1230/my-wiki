package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jedwards1230/home-wiki/internal/vault"
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
		"raw/source.md":      "---\ntitle: Source\nsource: https://example.com\ndate-added: 2026-01-15\n---\n\nRaw content.\n",
		"raw/unprocessed.md": "---\ntitle: Unprocessed\nsource: https://example.com/2\ndate-added: 2026-02-01\n---\n\nNot ingested.\n",
	}
	for name, content := range files {
		_ = os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}

	// Create log index
	logContent := "---\ntitle: Activity Log\n---\n\n## [2026-04-06] 1 changes | abcdef | Test | [[meta/activity/2026-04-06]]\n"
	_ = os.WriteFile(filepath.Join(dir, "meta", "log.md"), []byte(logContent), 0o644)

	// Create activity file
	actContent := "---\ntitle: \"2026-04-06\"\ntags:\n  - meta/activity\ndate: 2026-04-06\n---\n\n### 10:00 | create | First thing\nCreated a page.\n"
	_ = os.WriteFile(filepath.Join(dir, "meta", "activity", "2026-04-06.md"), []byte(actContent), 0o644)

	return vault.New(dir)
}

func setupTestMux(t *testing.T) (*http.ServeMux, *vault.Vault) {
	t.Helper()
	v := setupTestVault(t)
	h := NewHandler(v)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux, v
}

func TestLintEndpoint(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/lint", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp response
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
}

func TestLintEndpointWithCheck(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/lint?check=frontmatter", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestLintEndpointInvalidCheck(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/lint?check=invalid", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestQueueEndpoint(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/queue", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp response
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
}

func TestQueueGenerateEndpoint(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodPost, "/api/queue/generate", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestLogIndexEndpoint(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/log", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestLogIndexEndpointWithN(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/log?n=1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestLogDayEndpoint(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/log/2026-04-06", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestLogDayEndpointNotFound(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/log/2099-01-01", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestLogLintEndpoint(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/log/lint", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestActivityEndpoint(t *testing.T) {
	mux, _ := setupTestMux(t)

	body := `{"type":"note","title":"Test Note","time":"15:00"}`
	r := httptest.NewRequest(http.MethodPost, "/api/activity", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusCreated {
		b, _ := io.ReadAll(w.Body)
		t.Fatalf("expected 201, got %d: %s", w.Code, string(b))
	}
}

func TestActivityEndpointInvalidType(t *testing.T) {
	mux, _ := setupTestMux(t)

	body := `{"type":"invalid","title":"Test"}`
	r := httptest.NewRequest(http.MethodPost, "/api/activity", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestActivityEndpointInvalidJSON(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodPost, "/api/activity", strings.NewReader("not json"))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestPageReadEndpoint(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/pages/index.md", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestPageReadEndpointNotFound(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/pages/nonexistent.md", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestPageWriteEndpoint(t *testing.T) {
	mux, _ := setupTestMux(t)

	body := "---\ntitle: New Page\n---\n\nContent.\n"
	r := httptest.NewRequest(http.MethodPut, "/api/pages/new-page.md", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		b, _ := io.ReadAll(w.Body)
		t.Fatalf("expected 200, got %d: %s", w.Code, string(b))
	}

	// Verify we can read it back
	r2 := httptest.NewRequest(http.MethodGet, "/api/pages/new-page.md", nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, r2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 on read-back, got %d", w2.Code)
	}
}

func TestPageDeleteEndpoint(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodDelete, "/api/pages/about.md", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Verify it's gone
	r2 := httptest.NewRequest(http.MethodGet, "/api/pages/about.md", nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, r2)

	if w2.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", w2.Code)
	}
}

func TestPageListEndpoint(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/pages", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestPageListEndpointWithPrefix(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/pages?prefix=project", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestSearchEndpoint(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/search?q=test", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", w.Code)
	}
}

func TestAPIBypassesReadinessGate(t *testing.T) {
	// Verify in the server test that /api/ routes work when not ready
	// This is a documentation test - the actual bypass is in server.go
}
