package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

// Logging logs each request with method, path, status, and duration.
// Place outside the Metrics middleware in the chain to avoid double-wrapping.
func Logging(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			lw := &logStatusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(lw, r)

			logger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", lw.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"remote", r.RemoteAddr,
			)
		})
	}
}

type logStatusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *logStatusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *logStatusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
