package api

import "net/http"

func (h *Handler) handleDirectoryList(w http.ResponseWriter, r *http.Request) {
	entries, err := h.directory.List("")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, entries)
}

func (h *Handler) handleDirectoryGenerate(w http.ResponseWriter, r *http.Request) {
	_, count, err := h.directory.Generate()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"pages_indexed": count,
	})
}
