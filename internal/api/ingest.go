package api

import "net/http"

func (h *Handler) handleIngestList(w http.ResponseWriter, r *http.Request) {
	items, err := h.ingest.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, items)
}

func (h *Handler) handleIngestGenerate(w http.ResponseWriter, r *http.Request) {
	path, count, err := h.ingest.Generate()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"path":  path,
		"count": count,
	})
}
