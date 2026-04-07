package middleware

import (
	"net/http"
	"path"
	"strings"
	"time"
)

var cachedExtensions = map[string]bool{
	".js": true, ".css": true, ".png": true, ".jpg": true,
	".jpeg": true, ".gif": true, ".ico": true, ".svg": true,
	".woff": true, ".woff2": true,
}

// CacheHeaders adds Cache-Control and Expires headers for static assets.
func CacheHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ext := strings.ToLower(path.Ext(r.URL.Path))
		if cachedExtensions[ext] {
			w.Header().Set("Cache-Control", "public, immutable")
			w.Header().Set("Expires", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
		}
		next.ServeHTTP(w, r)
	})
}
