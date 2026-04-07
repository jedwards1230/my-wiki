package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func echoHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}
}

func TestCacheControlForStaticAssets(t *testing.T) {
	extensions := []string{
		"/app.js", "/style.css", "/logo.png", "/photo.jpg",
		"/photo.jpeg", "/icon.gif", "/favicon.ico", "/icon.svg",
		"/font.woff", "/font.woff2",
	}
	h := CacheHeaders(echoHandler())

	for _, path := range extensions {
		t.Run(path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			cc := w.Header().Get("Cache-Control")
			if !strings.Contains(cc, "public") || !strings.Contains(cc, "immutable") {
				t.Fatalf("expected Cache-Control: public, immutable for %s, got %q", path, cc)
			}
			if exp := w.Header().Get("Expires"); exp == "" {
				t.Fatalf("expected Expires header for %s", path)
			}
		})
	}
}

func TestNoCacheForHTML(t *testing.T) {
	h := CacheHeaders(echoHandler())
	r := httptest.NewRequest(http.MethodGet, "/about.html", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if cc := w.Header().Get("Cache-Control"); cc != "" {
		t.Fatalf("expected no Cache-Control for .html, got %q", cc)
	}
}

func TestNoCacheForMarkdown(t *testing.T) {
	h := CacheHeaders(echoHandler())
	r := httptest.NewRequest(http.MethodGet, "/notes/hello.md", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if cc := w.Header().Get("Cache-Control"); cc != "" {
		t.Fatalf("expected no Cache-Control for .md, got %q", cc)
	}
}
