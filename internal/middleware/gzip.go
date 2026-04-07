package middleware

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

const gzipMinLength = 1000

var compressibleTypes = map[string]bool{
	"text/plain":                true,
	"text/css":                  true,
	"text/html":                 true,
	"application/json":          true,
	"application/javascript":    true,
	"text/xml":                  true,
}

var gzipWriterPool = sync.Pool{
	New: func() any {
		return gzip.NewWriter(io.Discard)
	},
}

// Gzip wraps a handler to compress responses for supported content types
// when the response body is at least 1000 bytes.
func Gzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}

		grw := &gzipResponseWriter{
			ResponseWriter: w,
			buf:            make([]byte, 0, gzipMinLength),
		}
		next.ServeHTTP(grw, r)
		grw.finish()
	})
}

type gzipResponseWriter struct {
	http.ResponseWriter
	buf        []byte       // buffer until we decide whether to compress
	gw         *gzip.Writer // set once we commit to compressing
	committed  bool         // whether we've decided to compress or pass through
	compressed bool         // whether we're compressing
	statusCode int          // buffered status code
	wroteHeader bool
}

func (w *gzipResponseWriter) WriteHeader(code int) {
	w.statusCode = code
	w.wroteHeader = true
	// Don't write header yet — we need to decide on compression first
}

func (w *gzipResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.statusCode = http.StatusOK
		w.wroteHeader = true
	}

	if w.committed {
		if w.compressed {
			return w.gw.Write(p)
		}
		return w.ResponseWriter.Write(p)
	}

	// Buffer data
	w.buf = append(w.buf, p...)

	// If we've accumulated enough, decide now
	if len(w.buf) >= gzipMinLength {
		w.commit()
	}

	return len(p), nil
}

func (w *gzipResponseWriter) commit() {
	if w.committed {
		return
	}
	w.committed = true

	ct := w.Header().Get("Content-Type")
	// Strip parameters (e.g., "text/html; charset=utf-8" → "text/html")
	if idx := strings.Index(ct, ";"); idx != -1 {
		ct = strings.TrimSpace(ct[:idx])
	}

	if len(w.buf) >= gzipMinLength && compressibleTypes[ct] {
		// Compress
		w.compressed = true
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
		w.Header().Del("Content-Length")
		w.ResponseWriter.WriteHeader(w.statusCode)

		gz := gzipWriterPool.Get().(*gzip.Writer)
		gz.Reset(w.ResponseWriter)
		w.gw = gz

		w.gw.Write(w.buf)
	} else {
		// Pass through
		w.Header().Set("Vary", "Accept-Encoding")
		w.ResponseWriter.WriteHeader(w.statusCode)
		w.ResponseWriter.Write(w.buf)
	}
}

func (w *gzipResponseWriter) finish() {
	if !w.committed {
		w.commit()
	}
	if w.compressed && w.gw != nil {
		w.gw.Close()
		gzipWriterPool.Put(w.gw)
	}
}
