package cli

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

func newServeMCPStdioCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stdio",
		Short: "Start an MCP server over stdio transport (per-session)",
		Long: `Start a Model Context Protocol server over stdin/stdout for direct
embedding in MCP clients (Claude Code, .mcp.json entries, etc).

This mode skips everything stdio doesn't need: no HTTP listener, no Quartz
build pipeline, no obsidian-headless sync, no OIDC auth, no webhook dispatch.
Substring search is the only search backend (no TF-IDF index).

All logs are written to stderr — stdout is reserved for the JSON-RPC protocol.

The --instance-name flag is inherited from the parent 'serve mcp' command.`,
		RunE: runServeMCPStdio,
	}
}

// runServeMCPStdio is a thin shim that pre-sets the config for the per-session
// stdio transport: every optional feature is off. Substring search stays on so
// the historic stdio behavior — substring `search` tool available, no TF-IDF —
// is preserved.
func runServeMCPStdio(cmd *cobra.Command, _ []string) error {
	// CRITICAL: stdout is the MCP JSON-RPC pipe. All structured logs must go
	// to stderr or they will corrupt the protocol stream.
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	// --vault is a persistent flag on the root command (env: WIKI_VAULT_DIR).
	// --instance-name is a persistent flag on the root command
	// (env: WIKI_INSTANCE_NAME). Both surface through cmd.Flags() because
	// cobra inherits persistent flags.
	vaultDir, _ := cmd.Flags().GetString("vault")
	instanceName, _ := cmd.Flags().GetString("instance-name")

	ctx, stop := signal.NotifyContext(cmdContext(cmd), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return runMCP(ctx, vaultDir, mcpRunConfig{
		Transport:         mcpTransportStdio,
		EnableWatcher:     false,
		EnableDispatch:    false,
		EnableAuth:        false,
		EnableSearch:      true,  // substring backend only
		EnableSearchIndex: false, // no TF-IDF
		InstanceName:      instanceName,
	}, logger)
}
