package api

import "net/http"

func (h *Handler) handleTags(w http.ResponseWriter, _ *http.Request) {
	report, err := h.tags.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, report)
}
