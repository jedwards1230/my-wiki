package server

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/jedwards1230/my-wiki/internal/middleware"
	"github.com/jedwards1230/my-wiki/internal/server/assets"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Config holds server configuration.
type Config struct {
	VaultDir string
	Port     string

	// FragmentRenderer is consulted on HX-Request: true requests. The
	// native renderer wires the Builder; when nil, the catch-all falls
	// back to full-page HTML (htmx then extracts #main via hx-select on
	// the client).
	FragmentRenderer FragmentRenderer

	// RawRenderer renders raw/ markdown sources as HTML pages for browsers.
	// The native renderer wires the Builder; when nil, raw markdown is always
	// served as text/plain.
	RawRenderer RawRenderer
}

// Server is the wiki HTTP server.
type Server struct {
	handler http.Handler
	ready   atomic.Bool
	config  Config
}

// FragmentRenderer renders a content fragment for a given URL path. The
// native renderer implements this; when nil, the catch-all returns full
// HTML for HX-Request swaps (htmx falls back to extracting `#main` from
// the full body via hx-select).
type FragmentRenderer interface {
	RenderFragment(urlPath string) ([]byte, bool)
}

// Option configures the server.
type Option func(mux *http.ServeMux)

// WithAPIRoutes registers API routes on the server mux.
func WithAPIRoutes(register func(mux *http.ServeMux)) Option {
	return func(mux *http.ServeMux) {
		register(mux)
	}
}

// New creates a new Server with the given config, filesystems, and optional logger.
func New(cfg Config, publicFS, vaultFS fs.FS, logger *slog.Logger, opts ...Option) *Server {
	s := &Server{config: cfg}

	staticHandler := NewStaticHandler(publicFS)
	mdHandler := NewMarkdownHandler(vaultFS)

	// Build raw FS from vault's raw/ subdirectory
	rawFS, err := fs.Sub(vaultFS, "raw")
	var rawHandler *RawHandler
	if err == nil {
		rawHandler = NewRawHandler(rawFS, cfg.RawRenderer)
	}

	mux := http.NewServeMux()
	// Native-renderer static assets — htmx/Alpine/KaTeX/Mermaid/fonts plus
	// our wiki.css/wiki.js. Mounted under /_/static/ (leading underscore
	// guarantees no vault-slug collision), ahead of the catch-all.
	mux.Handle("GET /_/static/", http.StripPrefix("/_/static/", assets.Handler()))

	healthHandler := HealthHandler()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		healthHandler.ServeHTTP(w, r)
	})
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !s.ready.Load() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		healthHandler.ServeHTTP(w, r)
	})

	// Apply options (API routes, etc.) before static catch-all
	for _, opt := range opts {
		opt(mux)
	}

	if rawHandler != nil {
		mux.HandleFunc("GET /raw", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/raw/", http.StatusMovedPermanently)
		})
		mux.Handle("GET /raw/", rawHandler)
	}
	// Catch-all: route .md to markdown handler, everything else to static.
	// HX-Request: true requests get a content-only fragment when a
	// FragmentRenderer is wired (native mode); otherwise fall through to
	// full HTML and let htmx hx-select="#main" do the extraction.
	mux.HandleFunc("GET /{path...}", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".md") {
			mdHandler.ServeHTTP(w, r)
			return
		}
		if cfg.FragmentRenderer != nil && r.Header.Get("HX-Request") == "true" {
			if body, ok := cfg.FragmentRenderer.RenderFragment(r.URL.Path); ok {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				// Add (not Set) so we don't clobber Vary: Accept-Encoding
				// added by the gzip middleware downstream.
				w.Header().Add("Vary", "HX-Request")
				_, _ = w.Write(body)
				return
			}
			// Fall through to full HTML when fragment lookup misses.
		}
		staticHandler.ServeHTTP(w, r)
	})

	// Wrap with middleware (outermost first):
	// request → readiness → logging → metrics → gzip → cache headers → mux
	var handler http.Handler = mux
	handler = middleware.CacheHeaders(handler)
	handler = middleware.Gzip(handler)
	handler = middleware.Metrics(handler)
	if logger != nil {
		handler = middleware.Logging(logger)(handler)
	}
	handler = s.readinessMiddleware(handler)

	s.handler = handler
	return s
}

func (s *Server) readinessMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /healthz, /readyz, /metrics, and /api/ bypass readiness gate
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/metrics" || strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if !s.ready.Load() {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// SetReady marks the server as ready to serve content.
func (s *Server) SetReady() {
	s.ready.Store(true)
}

// IsReady returns whether the server is ready to serve content.
func (s *Server) IsReady() bool {
	return s.ready.Load()
}

// Handler returns the server's HTTP handler.
func (s *Server) Handler() http.Handler {
	return s.handler
}
