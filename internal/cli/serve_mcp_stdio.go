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

// defaultInstanceName is used when neither --instance-name nor
// WIKI_INSTANCE_NAME is set.
const defaultInstanceName = "work-wiki"

func newServeMCPStdioCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp-stdio",
		Short: "Start a per-session MCP server over stdio (no HTTP, no Quartz)",
		Long: `Start a Model Context Protocol server over stdin/stdout for direct
embedding in MCP clients (Claude Code, .mcp.json entries, etc).

This mode skips everything stdio doesn't need: no HTTP listener, no Quartz
build pipeline, no obsidian-headless sync, no OIDC auth, no webhook dispatch.
Substring search is the only search backend (no TF-IDF index).

All logs are written to stderr — stdout is reserved for the JSON-RPC protocol.`,
		RunE: runServeMCPStdio,
	}

	// --vault is a persistent flag on the root command (env: WIKI_VAULT_DIR).
	cmd.Flags().String("instance-name", envOr("WIKI_INSTANCE_NAME", defaultInstanceName), "human-readable identifier for this wiki instance, surfaced via the whoami MCP tool (env: WIKI_INSTANCE_NAME)")

	return cmd
}

func runServeMCPStdio(cmd *cobra.Command, _ []string) error {
	// CRITICAL: stdout is the MCP JSON-RPC pipe. All structured logs must go
	// to stderr or they will corrupt the protocol stream.
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	// Read --vault from the root persistent flag (env: WIKI_VAULT_DIR).
	vaultDir, _ := cmd.Root().PersistentFlags().GetString("vault")
	if vaultDir == "" {
		return fmt.Errorf("--vault is required (or set WIKI_VAULT_DIR)")
	}
	instanceName, _ := cmd.Flags().GetString("instance-name")

	v := vault.New(vaultDir)
	searchSvc := service.NewSearchService(search.NewSubstringSearcher(v))

	activitySvc := service.NewActivityService(v.Storage)
	onMutation := makeActivityCallback(activitySvc, nil, vaultDir, logger)
	pageSvc := service.NewPageService(v.Storage,
		service.WithExcludedDirs(v.ExcludedDirs),
		service.WithOnMutation(onMutation),
	)

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
