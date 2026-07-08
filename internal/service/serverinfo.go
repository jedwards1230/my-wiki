package service

import (
	"path/filepath"
	"runtime"

	"github.com/jedwards1230/my-wiki/internal/version"
)

// ServerInfo describes the running wiki server instance. It is the shared
// response shape for the REST /api/whoami endpoint and the MCP whoami tool so
// both surfaces report identical identity (parity). It is also the type the MCP
// whoami tool's outputSchema is generated from. InstanceName and User are
// optional and omitted when empty/nil.
type ServerInfo struct {
	Name         string    `json:"name"`
	Version      string    `json:"version"`
	VaultDir     string    `json:"vault_dir"`
	GoVersion    string    `json:"go_version"`
	InstanceName string    `json:"instance_name,omitempty"`
	User         *UserInfo `json:"user,omitempty"`
}

// UserInfo is the authenticated-caller portion of ServerInfo, populated only
// when the request carries authenticated identity. Optional claims are omitted
// when empty, matching the documented /api/whoami contract.
type UserInfo struct {
	Username string   `json:"username"`
	Email    string   `json:"email,omitempty"`
	Name     string   `json:"name,omitempty"`
	Groups   []string `json:"groups,omitempty"`
}

// BuildServerInfo assembles a ServerInfo. vaultDir is the full vault path (only
// its base name is exposed). instanceName is optional (omitted when empty); user
// is optional (omitted when nil).
func BuildServerInfo(vaultDir, instanceName string, user *UserInfo) ServerInfo {
	return ServerInfo{
		Name:         "my-wiki",
		Version:      version.Value,
		VaultDir:     filepath.Base(vaultDir),
		GoVersion:    runtime.Version(),
		InstanceName: instanceName,
		User:         user,
	}
}
