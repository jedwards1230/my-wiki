package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jedwards1230/home-wiki/internal/mcpserver"
	"github.com/jedwards1230/home-wiki/internal/search"
	"github.com/jedwards1230/home-wiki/internal/service"
	"github.com/jedwards1230/home-wiki/internal/vault"
	"github.com/mark3labs/mcp-go/server"
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

func runServeMCPStdio(cmd *cobra.Command, _ []string) error {
	// CRITICAL: stdout is the MCP JSON-RPC pipe. All structured logs must go
	// to stderr or they will corrupt the protocol stream.
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	// --vault is a persistent flag on the root command (env: WIKI_VAULT_DIR).
	// --instance-name is a persistent flag on `serve mcp` (env: WIKI_INSTANCE_NAME).
	// Both surface through cmd.Flags() because cobra inherits persistent flags.
	vaultDir, _ := cmd.Flags().GetString("vault")
	if vaultDir == "" {
		return fmt.Errorf("--vault is required (or set WIKI_VAULT_DIR)")
	}
	instanceName, _ := cmd.Flags().GetString("instance-name")

	v := vault.New(vaultDir)
	searchSvc := service.NewSearchService(search.NewSubstringSearcher(v))
	pageSvc := buildMCPPageSvc(v, nil, nil, logger)

	mcpSrv := mcpserver.New(v, searchSvc,
		mcpserver.WithPageService(pageSvc),
		mcpserver.WithInstanceName(instanceName),
	)
	stdio := server.NewStdioServer(mcpSrv)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("starting wiki MCP stdio server",
		"version", version,
		"vaultDir", vaultDir,
		"instanceName", instanceName,
	)

	if err := stdio.Listen(ctx, os.Stdin, os.Stdout); err != nil && !isShutdownErr(ctx, err) {
		return fmt.Errorf("stdio server: %w", err)
	}

	logger.Info("wiki MCP stdio server stopped")
	return nil
}

// isShutdownErr returns true when err is a normal shutdown signal (context
// cancellation from SIGINT/SIGTERM). The mcp-go stdio Listen returns
// context.Canceled on signal-driven shutdown; treat that as success. We do
// NOT treat context.DeadlineExceeded as success — this command runs under
// signal.NotifyContext (no deadline), so a deadline error would indicate a
// real bug rather than a clean shutdown.
func isShutdownErr(ctx context.Context, err error) bool {
	return ctx.Err() != nil && errors.Is(err, context.Canceled)
}
