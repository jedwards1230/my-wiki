package api

import (
	"encoding/json"
	"net/http"

	"github.com/jedwards1230/home-wiki/internal/service"
	"github.com/jedwards1230/home-wiki/internal/vault"
)

// Handler holds all API services and registers routes.
type Handler struct {
	lint     *service.LintService
	queue    *service.QueueService
	log      *service.LogService
	activity *service.ActivityService
	pages    *service.PageService
}

// NewHandler creates an API handler with services built from the given vault.
func NewHandler(v *vault.Vault) *Handler {
	return &Handler{
		lint:     service.NewLintService(v),
		queue:    service.NewQueueService(v),
		log:      service.NewLogService(v.Dir),
		activity: service.NewActivityService(v.Dir),
		pages:    service.NewPageService(v.Dir),
	}
}

// RegisterRoutes registers all API routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/lint", h.handleLint)
	mux.HandleFunc("GET /api/queue", h.handleQueueList)
	mux.HandleFunc("POST /api/queue/generate", h.handleQueueGenerate)
	mux.HandleFunc("GET /api/log", h.handleLogIndex)
	mux.HandleFunc("GET /api/log/lint", h.handleLogLint)
	mux.HandleFunc("GET /api/log/{date}", h.handleLogDay)
	mux.HandleFunc("POST /api/activity", h.handleActivityAppend)
	mux.HandleFunc("GET /api/pages/{path...}", h.handlePageRead)
	mux.HandleFunc("PUT /api/pages/{path...}", h.handlePageWrite)
	mux.HandleFunc("DELETE /api/pages/{path...}", h.handlePageDelete)
	mux.HandleFunc("GET /api/pages", h.handlePageList)
	mux.HandleFunc("GET /api/search", h.handleSearch)
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
