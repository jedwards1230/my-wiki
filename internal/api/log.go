package api

import (
	"net/http"
	"strconv"
)

func (h *Handler) handleLogIndex(w http.ResponseWriter, r *http.Request) {
	nStr := r.URL.Query().Get("n")
	n := 0
	if nStr != "" {
		var err error
		n, err = strconv.Atoi(nStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid n parameter")
			return
		}
	}

	entries, err := h.log.Index(n)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, entries)
}

func (h *Handler) handleLogDay(w http.ResponseWriter, r *http.Request) {
	date := r.PathValue("date")
	detail := r.URL.Query().Get("detail") == "true"

	dayLog, err := h.log.Day(date, detail)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, dayLog)
}

func (h *Handler) handleLogLint(w http.ResponseWriter, r *http.Request) {
	issues, err := h.log.Lint()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, issues)
}
