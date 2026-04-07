package middleware

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func largeTextHandler(contentType string, size int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write([]byte(strings.Repeat("a", size)))
	}
}

func TestGzipCompressesLargeText(t *testing.T) {
	inner := largeTextHandler("text/plain", 2000)
	h := Gzip(inner)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if enc := w.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("expected Content-Encoding: gzip, got %q", enc)
	}

	// Verify body is valid gzip and decompresses to original
	gr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatalf("failed to create gzip reader: %v", err)
	}
	defer func() { _ = gr.Close() }()
	body, _ := io.ReadAll(gr)
	if len(body) != 2000 {
		t.Fatalf("expected 2000 bytes after decompress, got %d", len(body))
	}
}

func TestGzipSkipsSmallResponse(t *testing.T) {
	inner := largeTextHandler("text/plain", 500)
	h := Gzip(inner)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if enc := w.Header().Get("Content-Encoding"); enc == "gzip" {
		t.Fatal("should not gzip small responses")
	}
	if w.Body.Len() != 500 {
		t.Fatalf("expected 500 bytes, got %d", w.Body.Len())
	}
}

func TestGzipSkipsNonTextTypes(t *testing.T) {
	inner := largeTextHandler("image/png", 2000)
	h := Gzip(inner)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if enc := w.Header().Get("Content-Encoding"); enc == "gzip" {
		t.Fatal("should not gzip image/png")
	}
}

func TestGzipSkipsWhenNotAccepted(t *testing.T) {
	inner := largeTextHandler("text/plain", 2000)
	h := Gzip(inner)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	// No Accept-Encoding header
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if enc := w.Header().Get("Content-Encoding"); enc == "gzip" {
		t.Fatal("should not gzip when not accepted")
	}
}

func TestGzipSetsVaryHeader(t *testing.T) {
	inner := largeTextHandler("text/plain", 2000)
	h := Gzip(inner)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if v := w.Header().Get("Vary"); !strings.Contains(v, "Accept-Encoding") {
		t.Fatalf("expected Vary: Accept-Encoding, got %q", v)
	}
}

func TestGzipAllContentTypes(t *testing.T) {
	types := []string{
		"text/plain",
		"text/css",
		"text/html",
		"application/json",
		"application/javascript",
		"text/xml",
	}
	for _, ct := range types {
		t.Run(ct, func(t *testing.T) {
			inner := largeTextHandler(ct, 2000)
			h := Gzip(inner)

			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.Header.Set("Accept-Encoding", "gzip")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			if enc := w.Header().Get("Content-Encoding"); enc != "gzip" {
				t.Fatalf("expected gzip for %s, got %q", ct, enc)
			}
		})
	}
}
