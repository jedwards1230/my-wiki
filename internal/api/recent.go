package api

import (
	"net/http"
	"strconv"

	"github.com/jedwards1230/home-wiki/internal/service"
)

// handleRecentList is a convenience alias for GET /api/pages?sort_by=modified&limit=20.
func (h *Handler) handleRecentList(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
		}
	}

	pages, err := h.pages.List(service.ListOptions{
		SortBy: "modified",
		Limit:  limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, pages)
}
