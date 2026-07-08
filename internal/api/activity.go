package api

import (
	"encoding/json"
	"net/http"

	"github.com/jedwards1230/my-wiki/internal/notify"
	"github.com/jedwards1230/my-wiki/internal/service"
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

	for _, p := range h.activity.DirtyPaths() {
		notify.MarkDirtyRelative(h.notifier, h.vaultDir, p, notify.ChangeModified)
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "ok"})
}
