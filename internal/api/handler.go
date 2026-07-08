package api

import (
	"encoding/json"
	"net/http"

	"github.com/jedwards1230/my-wiki/internal/notify"
	"github.com/jedwards1230/my-wiki/internal/service"
	"github.com/jedwards1230/my-wiki/internal/vault"
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
// mutations to trigger renderer rebuilds.
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

// WithInstanceName sets the human-readable instance identifier reported by
// /api/whoami (parity with the MCP whoami tool). Empty (the default) omits the
// field.
func WithInstanceName(name string) HandlerOption {
	return func(h *Handler) {
		h.instanceName = name
	}
}

// Handler holds all API services and registers routes.
type Handler struct {
	vault           *vault.Vault
	vaultDir        string
	instanceName    string
	lint            *service.LintService
	directory       *service.DirectoryService
	activity        *service.ActivityService
	pages           *service.PageService
	tags            *service.TagService
	search          *service.SearchService
	authMW          func(http.Handler) http.Handler
	authReads       bool
	notifier        *notify.RebuildNotifier
	renderPages     RenderPage
	renderBacklinks RenderBacklinks
}

// NewHandler creates an API handler with services built from the given vault.
// searchSvc may be nil if search is not configured.
func NewHandler(v *vault.Vault, searchSvc *service.SearchService, opts ...HandlerOption) *Handler {
	logSvc := service.NewLogService(v.Storage)
	h := &Handler{
		vault:     v,
		vaultDir:  v.Dir,
		lint:      service.NewLintService(v, logSvc),
		directory: service.NewDirectoryService(v),
		activity:  service.NewActivityService(v.Storage),
		pages:     service.NewPageService(v.Storage),
		tags:      service.NewTagService(v),
		search:    searchSvc,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
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

// response is the JSON envelope for API responses.
type response struct {
	Data     any                 `json:"data,omitempty"`
	Error    string              `json:"error,omitempty"`
	Warnings []service.LintIssue `json:"warnings,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response{Data: data})
}

// writeJSONWithWarnings writes a JSON response with optional lint warnings.
func writeJSONWithWarnings(w http.ResponseWriter, status int, data any, warnings []service.LintIssue) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response{Data: data, Warnings: warnings})
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response{Error: msg})
}
