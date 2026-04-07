package api

import "net/http"

func (h *Handler) handleQueueList(w http.ResponseWriter, r *http.Request) {
	items, err := h.queue.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, items)
}

func (h *Handler) handleQueueGenerate(w http.ResponseWriter, r *http.Request) {
	path, count, err := h.queue.Generate()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"path":  path,
		"count": count,
	})
}
