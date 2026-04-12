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

// WithRebuildNotifier sets a notifier that is called after successful vault
// mutations to trigger Quartz rebuilds.
func WithRebuildNotifier(n *notify.RebuildNotifier) HandlerOption {
	return func(h *Handler) {
		h.notifier = n
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
		log:       service.NewLogService(v.Dir),
		activity:  service.NewActivityService(v.Dir),
		pages:     service.NewPageService(v.Dir),
		recent:    service.NewRecentService(v),
		search:    searchSvc,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// RegisterRoutes registers all API routes on the given mux.
// Read-only GET routes are always unauthenticated. Mutating routes (PUT, DELETE,
// PATCH, POST) are wrapped with the auth middleware when configured.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Read-only routes — no auth required
	mux.HandleFunc("GET /api/lint", h.handleLint)
	mux.HandleFunc("GET /api/ingest", h.handleIngestList)
	mux.HandleFunc("GET /api/directory", h.handleDirectoryList)
	mux.HandleFunc("GET /api/log", h.handleLogIndex)
	mux.HandleFunc("GET /api/log/lint", h.handleLogLint)
	mux.HandleFunc("GET /api/log/{date}", h.handleLogDay)
	mux.HandleFunc("GET /api/pages/{path...}", h.handlePageRead)
	mux.HandleFunc("GET /api/pages", h.handlePageList)
	mux.HandleFunc("GET /api/recent", h.handleRecentList)
	mux.HandleFunc("GET /api/search", h.handleSearch)

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
