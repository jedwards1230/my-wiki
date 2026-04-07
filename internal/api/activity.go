package api

import (
	"encoding/json"
	"net/http"

	"github.com/jedwards1230/home-wiki/internal/service"
)

func (h *Handler) handleActivityAppend(w http.ResponseWriter, r *http.Request) {
	var entry service.ActivityEntry
	if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if err := h.activity.Append(entry); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"status": "ok"})
}
