package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jedwards1230/my-wiki/internal/render"
)

// --- Static handler tests ---

func newStaticFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":       {Data: []byte("<html>home</html>")},
		"about.html":       {Data: []byte("<html>about</html>")},
		"docs/index.html":  {Data: []byte("<html>docs</html>")},
		"static/style.css": {Data: []byte("body{}")},
		"404.html":         {Data: []byte("<html>not found</html>")},
	}
}

func TestStaticExactFile(t *testing.T) {
	h := NewStaticHandler(newStaticFS())
	r := httptest.NewRequest(http.MethodGet, "/static/style.css", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := w.Body.String(); body != "body{}" {
		t.Fatalf("expected body{}, got %q", body)
	}
}

func TestStaticHTMLFallback(t *testing.T) {
	h := NewStaticHandler(newStaticFS())
	r := httptest.NewRequest(http.MethodGet, "/about", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "about") {
		t.Fatalf("expected about content, got %q", body)
	}
}

func TestStaticDirectoryIndex(t *testing.T) {
	h := NewStaticHandler(newStaticFS())
	r := httptest.NewRequest(http.MethodGet, "/docs/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "docs") {
		t.Fatalf("expected docs content, got %q", body)
	}
}

func TestStaticRootIndex(t *testing.T) {
	h := NewStaticHandler(newStaticFS())
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "home") {
		t.Fatalf("expected home content, got %q", body)
	}
}

func TestStatic404Page(t *testing.T) {
	h := NewStaticHandler(newStaticFS())
	r := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "not found") {
		t.Fatalf("expected 404.html content, got %q", body)
	}
}

func TestStaticPathTraversal(t *testing.T) {
	h := NewStaticHandler(newStaticFS())
	r := httptest.NewRequest(http.MethodGet, "/../../etc/passwd", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for traversal, got %d", w.Code)
	}
}

func TestStaticContentType(t *testing.T) {
	h := NewStaticHandler(newStaticFS())
	r := httptest.NewRequest(http.MethodGet, "/static/style.css", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/css") {
		t.Fatalf("expected text/css content type, got %q", ct)
	}
}

// --- Markdown handler tests ---

func newVaultFS() fstest.MapFS {
	return fstest.MapFS{
		"notes/hello.md":           {Data: []byte("# Hello\nWorld")},
		"deep/path/note.md":        {Data: []byte("# Deep\nNote")},
		"meta/schema.md":           {Data: []byte("# Schema\nContent")},
		"guides/overview.md":       {Data: []byte("# Guides Overview")},
		"guides/hosts/server-1.md": {Data: []byte("# Server 1")},
		"private/secret.md":        {Data: []byte("# Secret\nConfidential")},
		".obsidian/workspace.json": {Data: []byte("{}")},
	}
}

// TestMarkdownDeniesObsidian verifies the markdown surface refuses to serve
// editor-config (.obsidian/) content, returning 404 (not 403, to avoid
// confirming existence). raw/ is intentionally not denied here — it has its
// own handler. private/ is no longer special and IS served.
func TestMarkdownDeniesObsidian(t *testing.T) {
	h := NewMarkdownHandler(newVaultFS())

	r := httptest.NewRequest(http.MethodGet, "/.obsidian/workspace.json", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /.obsidian/workspace.json: expected 404, got %d", w.Code)
	}

	// private/ is a normal directory now — it is served like any other page.
	r = httptest.NewRequest(http.MethodGet, "/private/secret.md", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("GET /private/secret.md: expected 200, got %d", w.Code)
	}
}

func TestMarkdownServes(t *testing.T) {
	h := NewMarkdownHandler(newVaultFS())
	r := httptest.NewRequest(http.MethodGet, "/notes/hello.md", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body, _ := io.ReadAll(w.Body)
	if string(body) != "# Hello\nWorld" {
		t.Fatalf("expected markdown content, got %q", string(body))
	}
}

func TestMarkdownContentType(t *testing.T) {
	h := NewMarkdownHandler(newVaultFS())
	r := httptest.NewRequest(http.MethodGet, "/notes/hello.md", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	ct := w.Header().Get("Content-Type")
	if ct != "text/plain; charset=utf-8" {
		t.Fatalf("expected text/plain; charset=utf-8, got %q", ct)
	}
}

func TestMarkdownNosniff(t *testing.T) {
	h := NewMarkdownHandler(newVaultFS())
	r := httptest.NewRequest(http.MethodGet, "/notes/hello.md", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if v := w.Header().Get("X-Content-Type-Options"); v != "nosniff" {
		t.Fatalf("expected nosniff, got %q", v)
	}
}

func TestMarkdownNestedPath(t *testing.T) {
	h := NewMarkdownHandler(newVaultFS())
	r := httptest.NewRequest(http.MethodGet, "/deep/path/note.md", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body, _ := io.ReadAll(w.Body)
	if string(body) != "# Deep\nNote" {
		t.Fatalf("expected deep note content, got %q", string(body))
	}
}

func TestMarkdownNotFound(t *testing.T) {
	h := NewMarkdownHandler(newVaultFS())
	r := httptest.NewRequest(http.MethodGet, "/nonexistent.md", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestMarkdownDirectoryRedirect(t *testing.T) {
	h := NewMarkdownHandler(newVaultFS())
	// "guides.md" doesn't exist as a file, but "guides/" is a directory
	r := httptest.NewRequest(http.MethodGet, "/guides.md", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("expected 301, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if loc != "/guides/" {
		t.Fatalf("expected redirect to /guides/, got %q", loc)
	}
}

func TestMarkdownFileInDirectoryStillServes(t *testing.T) {
	h := NewMarkdownHandler(newVaultFS())
	// File inside a directory should still serve normally
	r := httptest.NewRequest(http.MethodGet, "/guides/overview.md", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body, _ := io.ReadAll(w.Body)
	if string(body) != "# Guides Overview" {
		t.Fatalf("expected guides overview content, got %q", string(body))
	}
}

// --- Raw handler tests ---

func newRawFS() fstest.MapFS {
	return fstest.MapFS{
		"doc.pdf":           {Data: []byte("%PDF-1.4")},
		"notes/hello.md":    {Data: []byte("# Hello")},
		"somedir/file1.txt": {Data: []byte("file1")},
		"somedir/file2.txt": {Data: []byte("file2")},
		"data.canvas":       {Data: []byte("{}")},
		"data.base":         {Data: []byte("{}")},
		"video.mp4":         {Data: []byte("fake-mp4")},
		"image.png":         {Data: []byte("fake-png")},
	}
}

func TestRawExactFile(t *testing.T) {
	h := NewRawHandler(newRawFS(), nil)
	r := httptest.NewRequest(http.MethodGet, "/raw/doc.pdf", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/pdf") {
		t.Fatalf("expected application/pdf, got %q", ct)
	}
}

func TestRawMdFallback(t *testing.T) {
	h := NewRawHandler(newRawFS(), nil)
	// Request without .md extension, should find notes/hello.md
	r := httptest.NewRequest(http.MethodGet, "/raw/notes/hello", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body, _ := io.ReadAll(w.Body)
	if string(body) != "# Hello" {
		t.Fatalf("expected markdown content, got %q", string(body))
	}
}

// stubRawRenderer records the args it was called with and returns a sentinel
// HTML body so tests can assert the render path was taken. When miss is set,
// both methods report ok=false so the handler falls back to bytes / autoindex.
type stubRawRenderer struct {
	miss        bool
	called      bool
	indexCalled bool
	gotRel      string
	gotURL      string
	gotIndexURL string
	gotBytes    []byte
}

func (s *stubRawRenderer) RenderRawPage(relPath string, source []byte, _ time.Time, rawURL string) ([]byte, bool) {
	s.called = true
	s.gotRel = relPath
	s.gotURL = rawURL
	s.gotBytes = source
	if s.miss {
		return nil, false
	}
	return []byte("<html>RENDERED</html>"), true
}

func (s *stubRawRenderer) RenderRawIndex(urlDir string, _ []render.RawDirEntry) ([]byte, bool) {
	s.indexCalled = true
	s.gotIndexURL = urlDir
	if s.miss {
		return nil, false
	}
	return []byte("<html>GALLERY</html>"), true
}

func TestRawMarkdownContentNegotiation(t *testing.T) {
	cases := []struct {
		name       string
		url        string
		accept     string
		hxRequest  bool
		wantRender bool
		wantCType  string
	}{
		{"browser renders", "/raw/notes/hello.md", "text/html,*/*", false, true, "text/html"},
		{"agent gets bytes", "/raw/notes/hello.md", "", false, false, "text/plain"},
		{"raw=1 forces bytes", "/raw/notes/hello.md?raw=1", "text/html", false, false, "text/plain"},
		{"non-md untouched", "/raw/image.png", "text/html", false, false, "image/png"},
		// htmx (hx-boost) sends HX-Request but Accept: */* — must still render
		// the chrome page so the swap target #main exists (else blank pane).
		{"htmx renders", "/raw/notes/hello.md", "*/*", true, true, "text/html"},
		{"htmx but raw=1 → bytes", "/raw/notes/hello.md?raw=1", "*/*", true, false, "text/plain"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := &stubRawRenderer{}
			h := NewRawHandler(newRawFS(), stub)
			r := httptest.NewRequest(http.MethodGet, c.url, nil)
			if c.accept != "" {
				r.Header.Set("Accept", c.accept)
			}
			if c.hxRequest {
				r.Header.Set("HX-Request", "true")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", w.Code)
			}
			if stub.called != c.wantRender {
				t.Fatalf("render called = %v, want %v", stub.called, c.wantRender)
			}
			if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, c.wantCType) {
				t.Fatalf("content-type %q, want %q", ct, c.wantCType)
			}
			if c.wantRender {
				if !strings.Contains(w.Body.String(), "RENDERED") {
					t.Fatalf("expected rendered body, got %q", w.Body.String())
				}
				if stub.gotRel != "raw/notes/hello.md" {
					t.Fatalf("relPath = %q, want raw/notes/hello.md", stub.gotRel)
				}
				if stub.gotURL != "/raw/notes/hello.md" {
					t.Fatalf("rawURL = %q, want /raw/notes/hello.md", stub.gotURL)
				}
			}
		})
	}
}

// When the renderer reports a miss (ok=false), the handler must fall back to
// serving verbatim bytes rather than erroring.
func TestRawMarkdownRenderMissFallsBackToBytes(t *testing.T) {
	h := NewRawHandler(newRawFS(), &stubRawRenderer{miss: true})
	r := httptest.NewRequest(http.MethodGet, "/raw/notes/hello.md", nil)
	r.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("expected text/plain fallback, got %q", ct)
	}
	if w.Body.String() != "# Hello" {
		t.Fatalf("expected verbatim bytes, got %q", w.Body.String())
	}
}

// A browser browsing a /raw/ directory gets the rendered gallery; agents and
// ?raw=1 get the plain autoindex.
func TestRawIndexGalleryNegotiation(t *testing.T) {
	t.Run("browser gets gallery", func(t *testing.T) {
		stub := &stubRawRenderer{}
		h := NewRawHandler(newRawFS(), stub)
		r := httptest.NewRequest(http.MethodGet, "/raw/somedir/", nil)
		r.Header.Set("Accept", "text/html")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if !stub.indexCalled {
			t.Fatal("expected gallery render path")
		}
		if stub.gotIndexURL != "/raw/somedir/" {
			t.Fatalf("urlDir = %q, want /raw/somedir/", stub.gotIndexURL)
		}
		if !strings.Contains(w.Body.String(), "GALLERY") {
			t.Fatalf("expected gallery body, got %q", w.Body.String())
		}
	})
	t.Run("htmx click gets gallery (chrome with #main)", func(t *testing.T) {
		// hx-boost: HX-Request true, Accept */* — must render so the swap
		// response carries #main. This is the blank-pane regression.
		stub := &stubRawRenderer{}
		h := NewRawHandler(newRawFS(), stub)
		r := httptest.NewRequest(http.MethodGet, "/raw/somedir/", nil)
		r.Header.Set("Accept", "*/*")
		r.Header.Set("HX-Request", "true")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if !stub.indexCalled {
			t.Fatal("htmx navigation must render the gallery, not the plain autoindex")
		}
	})
	t.Run("agent gets plain autoindex", func(t *testing.T) {
		stub := &stubRawRenderer{}
		h := NewRawHandler(newRawFS(), stub)
		r := httptest.NewRequest(http.MethodGet, "/raw/somedir/", nil) // no Accept
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if stub.indexCalled {
			t.Fatal("agent should get plain autoindex, not gallery")
		}
		if !strings.Contains(w.Body.String(), "file1.txt") {
			t.Fatalf("expected plain autoindex, got %q", w.Body.String())
		}
	})
}

func TestRawAutoindex(t *testing.T) {
	h := NewRawHandler(newRawFS(), nil)
	r := httptest.NewRequest(http.MethodGet, "/raw/somedir/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "file1.txt") || !strings.Contains(body, "file2.txt") {
		t.Fatalf("expected directory listing with file1.txt and file2.txt, got %q", body)
	}
	if !strings.Contains(body, "<ul>") {
		t.Fatalf("expected styled list markup, got %q", body)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Fatalf("expected text/html for autoindex, got %q", ct)
	}
}

func TestRawCustomMimeTypes(t *testing.T) {
	tests := []struct {
		path     string
		wantMIME string
	}{
		{"/raw/data.canvas", "application/json"},
		{"/raw/data.base", "application/json"},
		{"/raw/video.mp4", "video/mp4"},
		{"/raw/image.png", "image/png"},
	}
	h := NewRawHandler(newRawFS(), nil)
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tt.path, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", w.Code)
			}
			ct := w.Header().Get("Content-Type")
			if !strings.Contains(ct, tt.wantMIME) {
				t.Fatalf("expected %s, got %q", tt.wantMIME, ct)
			}
		})
	}
}

func TestRawNosniff(t *testing.T) {
	h := NewRawHandler(newRawFS(), nil)
	r := httptest.NewRequest(http.MethodGet, "/raw/doc.pdf", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if v := w.Header().Get("X-Content-Type-Options"); v != "nosniff" {
		t.Fatalf("expected nosniff, got %q", v)
	}
}

func TestRawNotFound(t *testing.T) {
	h := NewRawHandler(newRawFS(), nil)
	r := httptest.NewRequest(http.MethodGet, "/raw/nonexistent", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// --- Health handler tests ---

func TestHealthReturns200(t *testing.T) {
	h := HealthHandler()
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := w.Body.String(); body != "ok" {
		t.Fatalf("expected 'ok', got %q", body)
	}
}

func TestHealthContentType(t *testing.T) {
	h := HealthHandler()
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Fatalf("expected text/plain, got %q", ct)
	}
}
