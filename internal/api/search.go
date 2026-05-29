package api

import (
	"net/http"
	"strconv"
)

func (h *Handler) handleSearch(w http.ResponseWriter, r *http.Request) {
	if h.search == nil {
		writeError(w, http.StatusNotImplemented, "search is not configured")
		return
	}

	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(w, http.StatusBadRequest, "q parameter is required")
		return
	}
	if len(q) < 2 {
		writeError(w, http.StatusBadRequest, "query must be at least 2 characters")
		return
	}

	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	engine := r.URL.Query().Get("engine")

	resp, err := h.search.Search(q, limit, engine)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, resp)
}
