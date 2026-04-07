package server

import (
	"io/fs"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/jedwards1230/home-wiki/internal/middleware"
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

// New creates a new Server with the given config and filesystems.
func New(cfg Config, publicFS, vaultFS fs.FS) *Server {
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
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		HealthHandler().ServeHTTP(w, r)
	})
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

	// Wrap with middleware: readiness → cache headers → gzip → mux
	var handler http.Handler = mux
	handler = middleware.CacheHeaders(handler)
	handler = middleware.Gzip(handler)
	handler = s.readinessMiddleware(handler)

	s.handler = handler
	return s
}

func (s *Server) readinessMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
