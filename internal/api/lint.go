package api

import "net/http"

func (h *Handler) handleLint(w http.ResponseWriter, r *http.Request) {
	check := r.URL.Query().Get("check")
	if check == "" {
		check = "all"
	}

	report, err := h.lint.Run(check)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, report)
}
