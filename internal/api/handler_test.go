package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jedwards1230/home-wiki/internal/search"
	"github.com/jedwards1230/home-wiki/internal/service"
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
	logContent := "---\ntitle: Activity Log\n---\n\n## [[meta/activity/2026-04-06|2026-04-06]] 1 changes | abcdef | Test\n"
	_ = os.WriteFile(filepath.Join(dir, "meta", "log.md"), []byte(logContent), 0o644)

	// Create activity file
	actContent := "---\ntitle: \"2026-04-06\"\ntags:\n  - meta/activity\ndate: 2026-04-06\n---\n\n### 10:00 | create | First thing\nCreated a page.\n"
	_ = os.WriteFile(filepath.Join(dir, "meta", "activity", "2026-04-06.md"), []byte(actContent), 0o644)

	return vault.New(dir)
}

func setupTestMux(t *testing.T) (*http.ServeMux, *vault.Vault) {
	t.Helper()
	v := setupTestVault(t)
	sub := search.NewSubstringSearcher(v)
	idx := search.NewIndexSearcher(v)
	_ = idx.Build()
	searchSvc := service.NewSearchService(sub, idx)
	h := NewHandler(v, searchSvc)
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

func TestIngestEndpoint(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/ingest", nil)
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

func TestIngestGenerateEndpoint(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodPost, "/api/ingest/generate", nil)
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

	body := "---\ntitle: New Page\ntags:\n  - test\ndate: 2026-01-15\n---\n\nContent.\n"
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

func TestPagePatchEndpoint(t *testing.T) {
	mux, _ := setupTestMux(t)

	body := `{"operations":[{"find":"Content.","replace":"Updated content."}]}`
	r := httptest.NewRequest(http.MethodPatch, "/api/pages/project/alpha.md", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		b, _ := io.ReadAll(w.Body)
		t.Fatalf("expected 200, got %d: %s", w.Code, string(b))
	}

	// Verify the content was patched
	r2 := httptest.NewRequest(http.MethodGet, "/api/pages/project/alpha.md", nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, r2)

	b, _ := io.ReadAll(w2.Body)
	if !strings.Contains(string(b), "Updated content.") {
		t.Fatalf("expected patched content, got: %s", string(b))
	}
}

func TestPagePatchEndpointNotFound(t *testing.T) {
	mux, _ := setupTestMux(t)

	body := `{"operations":[{"find":"foo","replace":"bar"}]}`
	r := httptest.NewRequest(http.MethodPatch, "/api/pages/nonexistent.md", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestPagePatchEndpointEmptyOps(t *testing.T) {
	mux, _ := setupTestMux(t)

	body := `{"operations":[]}`
	r := httptest.NewRequest(http.MethodPatch, "/api/pages/project/alpha.md", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestPagePatchEndpointEmptyFind(t *testing.T) {
	mux, _ := setupTestMux(t)

	body := `{"operations":[{"find":"","replace":"bar"}]}`
	r := httptest.NewRequest(http.MethodPatch, "/api/pages/project/alpha.md", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestPagePatchEndpointFindNotFound(t *testing.T) {
	mux, _ := setupTestMux(t)

	body := `{"operations":[{"find":"nonexistent text","replace":"bar"}]}`
	r := httptest.NewRequest(http.MethodPatch, "/api/pages/project/alpha.md", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", w.Code)
	}
}

func TestDirectoryEndpoint(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/directory", nil)
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

func TestDirectoryGenerateEndpoint(t *testing.T) {
	mux, v := setupTestMux(t)

	r := httptest.NewRequest(http.MethodPost, "/api/directory/generate", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		b, _ := io.ReadAll(w.Body)
		t.Fatalf("expected 200, got %d: %s", w.Code, string(b))
	}

	// Verify file was created
	indexFile := filepath.Join(v.Dir, "index.md")
	if _, err := os.Stat(indexFile); os.IsNotExist(err) {
		t.Fatal("index.md not created by directory generate")
	}
}

func TestRecentEndpoint(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/recent", nil)
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

func TestRecentEndpointWithLimit(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/recent?limit=1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestPageWriteValidationEndpoint(t *testing.T) {
	mux, _ := setupTestMux(t)

	// Missing tags — should return 400
	body := "---\ntitle: Bad Page\ndate: 2026-01-01\n---\n\nContent.\n"
	r := httptest.NewRequest(http.MethodPut, "/api/pages/bad-page.md", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		b, _ := io.ReadAll(w.Body)
		t.Fatalf("expected 400 for invalid frontmatter, got %d: %s", w.Code, string(b))
	}
}

func TestSearchEndpoint(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/search?q=Alpha", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Data struct {
			Results []struct {
				Path   string  `json:"path"`
				Title  string  `json:"title"`
				Score  float64 `json:"score"`
				Engine string  `json:"engine"`
			} `json:"results"`
			Engines   []string           `json:"engines"`
			ElapsedMs map[string]float64 `json:"elapsed_ms"`
		} `json:"data"`
	}

	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.Data.Results) == 0 {
		t.Fatal("expected search results")
	}

	if len(resp.Data.Engines) == 0 {
		t.Fatal("expected engines list")
	}

	if len(resp.Data.ElapsedMs) == 0 {
		t.Fatal("expected elapsed_ms timing")
	}
}

func TestSearchEndpointMissingQuery(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/search", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestSearchEndpointShortQuery(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/search?q=a", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestSearchEndpointWithEngine(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/search?q=Alpha&engine=index", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestSearchEndpointAllEngines(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/search?q=Alpha&engine=all", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Data struct {
			Engines   []string           `json:"engines"`
			ElapsedMs map[string]float64 `json:"elapsed_ms"`
		} `json:"data"`
	}

	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	if len(resp.Data.Engines) < 2 {
		t.Fatalf("expected at least 2 engines, got %d", len(resp.Data.Engines))
	}

	if len(resp.Data.ElapsedMs) < 2 {
		t.Fatalf("expected timing for at least 2 engines, got %d", len(resp.Data.ElapsedMs))
	}
}

func TestSearchEndpointUnknownEngine(t *testing.T) {
	mux, _ := setupTestMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/search?q=test&engine=bogus", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown engine, got %d", w.Code)
	}
}

func TestAPIBypassesReadinessGate(t *testing.T) {
	// Verify in the server test that /api/ routes work when not ready
	// This is a documentation test - the actual bypass is in server.go
}

// --- Auth middleware integration tests ---

// fakeAuthMW returns a middleware that rejects requests without a specific header.
// This simulates JWT auth without needing a real OIDC provider.
func fakeAuthMW() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "" {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = fmt.Fprintln(w, `{"error":"unauthorized"}`)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func setupTestMuxWithAuth(t *testing.T) *http.ServeMux {
	t.Helper()
	v := setupTestVault(t)
	sub := search.NewSubstringSearcher(v)
	idx := search.NewIndexSearcher(v)
	_ = idx.Build()
	searchSvc := service.NewSearchService(sub, idx)
	h := NewHandler(v, searchSvc, WithAuthMiddleware(fakeAuthMW()))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux
}

func TestAuthMutatingRoutesRequireAuth(t *testing.T) {
	mux := setupTestMuxWithAuth(t)

	mutatingRequests := []struct {
		method string
		path   string
		body   string
	}{
		{"PUT", "/api/pages/test-page.md", "---\ntitle: Test\ntags:\n  - test\ndate: 2026-01-01\n---\n\nContent.\n"},
		{"DELETE", "/api/pages/about.md", ""},
		{"PATCH", "/api/pages/project/alpha.md", `{"operations":[{"find":"Content.","replace":"New."}]}`},
		{"POST", "/api/activity", `{"type":"note","title":"Test","time":"15:00"}`},
		{"POST", "/api/ingest/generate", ""},
		{"POST", "/api/directory/generate", ""},
	}

	for _, tc := range mutatingRequests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			var body *strings.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			} else {
				body = strings.NewReader("")
			}
			r := httptest.NewRequest(tc.method, tc.path, body)
			if tc.body != "" && (tc.method == "PATCH" || tc.method == "POST") {
				r.Header.Set("Content-Type", "application/json")
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)

			if w.Code != http.StatusUnauthorized {
				t.Errorf("expected 401 without auth, got %d", w.Code)
			}
			ct := w.Header().Get("Content-Type")
			if !strings.Contains(ct, "application/json") {
				t.Errorf("expected JSON content-type on 401, got %q", ct)
			}
		})
	}
}

func TestAuthReadRoutesRemainOpen(t *testing.T) {
	mux := setupTestMuxWithAuth(t)

	readRequests := []struct {
		path string
	}{
		{"/api/pages/index.md"},
		{"/api/pages"},
		{"/api/lint"},
		{"/api/ingest"},
		{"/api/directory"},
		{"/api/recent"},
		{"/api/search?q=Alpha"},
	}

	for _, tc := range readRequests {
		t.Run("GET "+tc.path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)

			if w.Code == http.StatusUnauthorized {
				t.Errorf("GET %s should not require auth, got 401", tc.path)
			}
		})
	}
}

func TestAuthReadsProtectsGetRoutes(t *testing.T) {
	v := setupTestVault(t)
	sub := search.NewSubstringSearcher(v)
	idx := search.NewIndexSearcher(v)
	_ = idx.Build()
	searchSvc := service.NewSearchService(sub, idx)
	h := NewHandler(v, searchSvc, WithAuthMiddleware(fakeAuthMW()), WithAuthReads(true))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	readPaths := []string{
		"/api/pages/index.md",
		"/api/pages",
		"/api/lint",
		"/api/ingest",
		"/api/directory",
		"/api/recent",
		"/api/search?q=Alpha",
	}

	for _, path := range readPaths {
		t.Run("GET "+path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)

			if w.Code != http.StatusUnauthorized {
				t.Errorf("expected 401 without auth when authReads enabled, got %d", w.Code)
			}
		})
	}

	// With auth header, reads should succeed
	r := httptest.NewRequest(http.MethodGet, "/api/pages/index.md", nil)
	r.Header.Set("Authorization", "Bearer fake-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code == http.StatusUnauthorized {
		t.Errorf("GET with auth should not get 401, got %d", w.Code)
	}
}

func TestAuthMutatingRoutesPassWithAuth(t *testing.T) {
	mux := setupTestMuxWithAuth(t)

	// PUT with valid auth header should succeed
	body := "---\ntitle: Auth Test\ntags:\n  - test\ndate: 2026-01-01\n---\n\nContent.\n"
	r := httptest.NewRequest(http.MethodPut, "/api/pages/auth-test.md", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer fake-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code == http.StatusUnauthorized {
		t.Errorf("PUT with auth should not get 401, got %d", w.Code)
	}
}
