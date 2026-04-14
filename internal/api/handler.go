package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/jedwards1230/home-wiki/internal/notify"
	"github.com/jedwards1230/home-wiki/internal/service"
	"github.com/jedwards1230/home-wiki/internal/vault"
)

// HandlerOption configures optional Handler behavior.
type HandlerOption func(*Handler)

// WithAuthMiddleware sets a middleware that protects mutating API routes.
// When nil or not provided, all routes are unauthenticated.
func WithAuthMiddleware(mw func(http.Handler) http.Handler) HandlerOption {
	return func(h *Handler) {
		h.authMW = mw
	}
}

// WithAuthReads enables authentication on read-only GET routes.
// When false (default), GET routes are publicly accessible.
func WithAuthReads(enabled bool) HandlerOption {
	return func(h *Handler) {
		h.authReads = enabled
	}
}

// WithRebuildNotifier sets a notifier that is called after successful vault
// mutations to trigger Quartz rebuilds.
func WithRebuildNotifier(n *notify.RebuildNotifier) HandlerOption {
	return func(h *Handler) {
		h.notifier = n
	}
}

// WithPageService provides a pre-configured PageService instead of constructing one internally.
func WithPageService(ps *service.PageService) HandlerOption {
	return func(h *Handler) {
		if ps != nil {
			h.pages = ps
		}
	}
}

// Handler holds all API services and registers routes.
type Handler struct {
	vaultDir  string
	lint      *service.LintService
	ingest    *service.IngestService
	directory *service.DirectoryService
	log       *service.LogService
	activity  *service.ActivityService
	pages     *service.PageService
	recent    *service.RecentService
	search    *service.SearchService
	authMW    func(http.Handler) http.Handler
	authReads bool
	notifier  *notify.RebuildNotifier
}

// NewHandler creates an API handler with services built from the given vault.
// searchSvc may be nil if search is not configured.
func NewHandler(v *vault.Vault, searchSvc *service.SearchService, opts ...HandlerOption) *Handler {
	h := &Handler{
		vaultDir:  v.Dir,
		lint:      service.NewLintService(v),
		ingest:    service.NewIngestService(v),
		directory: service.NewDirectoryService(v),
		log:       service.NewLogService(v.Storage),
		activity:  service.NewActivityService(v.Storage),
		pages:     service.NewPageService(v.Storage),
		recent:    service.NewRecentService(v),
		search:    searchSvc,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// RegisterRoutes registers all API routes on the given mux.
// Read-only GET routes are unauthenticated by default. When authReads is enabled,
// they are also wrapped with the auth middleware. Mutating routes (PUT, DELETE,
// PATCH, POST) are always wrapped with the auth middleware when configured.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Read-only routes — optionally auth-protected when read auth is enabled
	mux.Handle("GET /api/lint", h.wrapRead(http.HandlerFunc(h.handleLint)))
	mux.Handle("GET /api/ingest", h.wrapRead(http.HandlerFunc(h.handleIngestList)))
	mux.Handle("GET /api/directory", h.wrapRead(http.HandlerFunc(h.handleDirectoryList)))
	mux.Handle("GET /api/log", h.wrapRead(http.HandlerFunc(h.handleLogIndex)))
	mux.Handle("GET /api/log/lint", h.wrapRead(http.HandlerFunc(h.handleLogLint)))
	mux.Handle("GET /api/log/{date}", h.wrapRead(http.HandlerFunc(h.handleLogDay)))
	mux.Handle("GET /api/pages/{path...}", h.wrapRead(http.HandlerFunc(h.handlePageRead)))
	mux.Handle("GET /api/pages", h.wrapRead(http.HandlerFunc(h.handlePageList)))
	mux.Handle("GET /api/recent", h.wrapRead(http.HandlerFunc(h.handleRecentList)))
	mux.Handle("GET /api/search", h.wrapRead(http.HandlerFunc(h.handleSearch)))

	// Mutating routes — protected by auth middleware when configured
	mux.Handle("POST /api/ingest/generate", h.wrapMutating(http.HandlerFunc(h.handleIngestGenerate)))
	mux.Handle("POST /api/directory/generate", h.wrapMutating(http.HandlerFunc(h.handleDirectoryGenerate)))
	mux.Handle("POST /api/activity", h.wrapMutating(http.HandlerFunc(h.handleActivityAppend)))
	mux.Handle("PUT /api/pages/{path...}", h.wrapMutating(http.HandlerFunc(h.handlePageWrite)))
	mux.Handle("DELETE /api/pages/{path...}", h.wrapMutating(http.HandlerFunc(h.handlePageDelete)))
	mux.Handle("PATCH /api/pages/{path...}", h.wrapMutating(http.HandlerFunc(h.handlePagePatch)))
}

// wrapMutating wraps a handler with the auth middleware when configured.
// Returns the handler unchanged when auth is disabled.
func (h *Handler) wrapMutating(handler http.Handler) http.Handler {
	if h.authMW == nil {
		return handler
	}
	return h.authMW(handler)
}

// wrapRead wraps a handler with the auth middleware only when both auth and
// authReads are enabled. Returns the handler unchanged otherwise.
func (h *Handler) wrapRead(handler http.Handler) http.Handler {
	if h.authMW == nil || !h.authReads {
		return handler
	}
	return h.authMW(handler)
}

// markDirty notifies the rebuild notifier about a mutated vault path.
// path is a relative path within the vault; the .md extension is added if missing.
// No-op when the notifier is not configured.
func (h *Handler) markDirty(relPath string) {
	if h.notifier == nil {
		return
	}
	if !strings.HasSuffix(relPath, ".md") {
		relPath += ".md"
	}
	h.notifier.MarkDirty(filepath.Clean(filepath.Join(h.vaultDir, relPath)))
}

// response is the JSON envelope for API responses.
type response struct {
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response{Data: data})
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response{Error: msg})
}
