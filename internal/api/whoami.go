package api

import (
	"net/http"
	"path/filepath"
	"runtime"

	"github.com/jedwards1230/home-wiki/internal/version"
)

// ServerInfo describes the running wiki server instance.
type ServerInfo struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	VaultDir  string `json:"vault_dir"`
	GoVersion string `json:"go_version"`
}

func (h *Handler) handleWhoami(w http.ResponseWriter, _ *http.Request) {
	info := ServerInfo{
		Name:      "home-wiki",
		Version:   version.Value,
		VaultDir:  filepath.Base(h.vaultDir),
		GoVersion: runtime.Version(),
	}
	writeJSON(w, http.StatusOK, info)
}
