package api

import "net/http"

func (h *Handler) handleSearch(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotImplemented, "search is not yet implemented")
}
