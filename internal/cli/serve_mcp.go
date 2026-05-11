package cli

import (
	"context"
	"log/slog"
	"os"

	"github.com/spf13/cobra"
)

// newServeMCPParentCmd is the parent command for `serve mcp <transport>`.
// It groups the http and stdio subcommands. Bare `serve mcp` (no transport)
// is marked Deprecated via cobra: cobra prints a deprecation message and
// falls through to help (since the parent has no RunE), so users discover
// the correct invocation rather than starting a server unexpectedly.
//
// --instance-name is a root persistent flag — see internal/cli/root.go.
// All four MCP-server surfaces (embedded via serve --mcp-port, serve mcp
// http, serve mcp stdio, and the deprecated alias's help output) read it
// uniformly without scope-shifting.
func newServeMCPParentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Start an MCP server (Model Context Protocol)",
		Long: `Start a Model Context Protocol server.

Use 'serve mcp http' for the streamable-http transport (long-lived server,
suitable for K8s deployment) or 'serve mcp stdio' for an on-demand stdio
session (suitable for embedding in MCP clients like Claude Code).`,
		Deprecated: "use 'serve mcp http' or 'serve mcp stdio' explicitly. Bare 'serve mcp' will be removed in a future release.",
	}

	cmd.AddCommand(newServeMCPHTTPCmd())
	cmd.AddCommand(newServeMCPStdioCmd())

	return cmd
}

func newServeMCPHTTPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "http",
		Short: "Start an MCP server over streamable-http transport",
		RunE:  runServeMCPHTTP,
	}

	cmd.Flags().String("port", envOr("WIKI_MCP_PORT", "8081"), "MCP server port (env: WIKI_MCP_PORT)")
	cmd.Flags().Bool("watch", envOrBool("WIKI_WATCH", true), "watch vault directory for filesystem changes (env: WIKI_WATCH)")

	return cmd
}

// runServeMCPHTTP is a thin shim that pre-sets the config for the long-lived
// streamable-http transport: all features on except the TF-IDF search index
// (which the standalone runner has historically omitted — substring search
// only). The HTTP API runs separately under `serve http`, so the only search
// backend exposed here is whatever this runner wires.
func runServeMCPHTTP(cmd *cobra.Command, _ []string) error {
	port, _ := cmd.Flags().GetString("port")
	vaultDir, _ := cmd.Root().Flags().GetString("vault")
	watchEnabled, _ := cmd.Flags().GetBool("watch")
	instanceName, _ := cmd.Flags().GetString("instance-name")

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	ctx, stop := notifyContextWithSignals(cmdContext(cmd))
	defer stop()

	return runMCP(ctx, vaultDir, mcpRunConfig{
		Transport:         mcpTransportHTTP,
		HTTPPort:          port,
		EnableWatcher:     watchEnabled,
		EnableDispatch:    true,
		EnableAuth:        true,
		EnableSearch:      false, // historical: standalone HTTP passes nil search
		EnableSearchIndex: false,
		InstanceName:      instanceName,
	}, logger)
}

// cmdContext returns cmd.Context() when set, otherwise a fresh
// context.Background(). Cobra populates Context only when ExecuteContext is
// used; the wiki-server entry point uses Execute() so this falls back to
// Background, matching the pre-refactor behavior.
func cmdContext(cmd *cobra.Command) context.Context {
	if c := cmd.Context(); c != nil {
		return c
	}
	return context.Background()
}
