package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jedwards1230/home-wiki/internal/notify"
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

	today := time.Now().Format("2006-01-02")
	h.markDirty(fmt.Sprintf("meta/activity/%s", today), notify.ChangeModified)
	h.markDirty("meta/log", notify.ChangeModified)
	writeJSON(w, http.StatusCreated, map[string]string{"status": "ok"})
}
