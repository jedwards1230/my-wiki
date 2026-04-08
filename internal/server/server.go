package server

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/jedwards1230/home-wiki/internal/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Config holds server configuration.
type Config struct {
	PublicDir string
	VaultDir  string
	Port      string
}

// Server is the wiki HTTP server.
type Server struct {
	handler http.Handler
	ready   atomic.Bool
	config  Config
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
		rawHandler = NewRawHandler(rawFS)
	}

	mux := http.NewServeMux()
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
	// Catch-all: route .md to markdown handler, everything else to static
	mux.HandleFunc("GET /{path...}", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".md") {
			mdHandler.ServeHTTP(w, r)
			return
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
