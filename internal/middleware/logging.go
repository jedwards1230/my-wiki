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
			lw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
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
