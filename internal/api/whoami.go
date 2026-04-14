package api

import (
	"net/http"
	"path/filepath"
	"runtime"

	"github.com/jedwards1230/home-wiki/internal/middleware"
	"github.com/jedwards1230/home-wiki/internal/version"
)

// ServerInfo describes the running wiki server instance.
type ServerInfo struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	VaultDir  string    `json:"vault_dir"`
	GoVersion string    `json:"go_version"`
	User      *UserInfo `json:"user,omitempty"`
}

// UserInfo is the subset of auth claims returned by whoami.
type UserInfo struct {
	Username string   `json:"username"`
	Email    string   `json:"email,omitempty"`
	Name     string   `json:"name,omitempty"`
	Groups   []string `json:"groups,omitempty"`
}

func (h *Handler) handleWhoami(w http.ResponseWriter, r *http.Request) {
	info := ServerInfo{
		Name:      "home-wiki",
		Version:   version.Value,
		VaultDir:  filepath.Base(h.vaultDir),
		GoVersion: runtime.Version(),
	}

	if u := middleware.UserFromContext(r.Context()); u != nil {
		info.User = &UserInfo{
			Username: u.Username,
			Email:    u.Email,
			Name:     u.Name,
			Groups:   u.Groups,
		}
	}

	writeJSON(w, http.StatusOK, info)
}
