package api

import (
	"net/http"

	"github.com/jedwards1230/my-wiki/internal/middleware"
	"github.com/jedwards1230/my-wiki/internal/service"
)

func (h *Handler) handleWhoami(w http.ResponseWriter, r *http.Request) {
	var user *service.UserInfo
	if u := middleware.UserFromContext(r.Context()); u != nil {
		user = &service.UserInfo{
			Username: u.Username,
			Email:    u.Email,
			Name:     u.Name,
			Groups:   u.Groups,
		}
	}

	writeJSON(w, http.StatusOK, service.BuildServerInfo(h.vaultDir, h.instanceName, user))
}
