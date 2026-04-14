package api

import (
	"net/http"
	"path/filepath"
	"runtime"
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
		Version:   "0.1.0",
		VaultDir:  filepath.Base(h.vaultDir),
		GoVersion: runtime.Version(),
	}
	writeJSON(w, http.StatusOK, info)
}
