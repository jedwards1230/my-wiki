package notify

import (
	"path/filepath"
	"strings"
)

// MarkDirtyRelative marks a vault-relative path dirty on n. relPath is relative
// to vaultDir; a missing .md extension is added. The joined path is cleaned to
// an absolute vault path before notifying, matching what the filesystem watcher
// emits. No-op when n is nil (e.g. stdio mode with no renderer to rebuild).
func MarkDirtyRelative(n *RebuildNotifier, vaultDir, relPath string, action ChangeKind) {
	if n == nil {
		return
	}
	if !strings.HasSuffix(relPath, ".md") {
		relPath += ".md"
	}
	n.MarkDirty(filepath.Clean(filepath.Join(vaultDir, relPath)), action)
}
