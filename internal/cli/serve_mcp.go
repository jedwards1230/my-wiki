package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jedwards1230/home-wiki/internal/dispatch"
	"github.com/jedwards1230/home-wiki/internal/mcpserver"
	"github.com/jedwards1230/home-wiki/internal/notify"
	"github.com/jedwards1230/home-wiki/internal/service"
	"github.com/jedwards1230/home-wiki/internal/vault"
	"github.com/spf13/cobra"
)

// newServeMCPParentCmd is the parent command for `serve mcp <transport>`.
// It owns the --instance-name persistent flag (inherited by both transports)
// and provides a deprecated alias: `serve mcp` (no transport) routes to the
// http subcommand with a stderr deprecation warning.
func newServeMCPParentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Start an MCP server (Model Context Protocol)",
		Long: `Start a Model Context Protocol server.

Use 'serve mcp http' for the streamable-http transport (long-lived server,
suitable for K8s deployment) or 'serve mcp stdio' for an on-demand stdio
session (suitable for embedding in MCP clients like Claude Code).

Bare 'serve mcp' is a deprecated alias for 'serve mcp http'.`,
		RunE: runServeMCPDeprecated,
	}

	// --instance-name is shared by both transports: it identifies this wiki
	// instance in the whoami tool response so clients can distinguish between
	// multiple wiki servers (e.g. home-wiki vs work-wiki). Default is empty
	// (preserves existing whoami behavior); env override is WIKI_INSTANCE_NAME.
	cmd.PersistentFlags().String(
		"instance-name",
		envOr("WIKI_INSTANCE_NAME", ""),
		"human-readable identifier for this wiki instance, surfaced via the whoami MCP tool (env: WIKI_INSTANCE_NAME)",
	)

	// Flags shared by parent (deprecated path) and http subcommand. The http
	// subcommand redeclares them so it works standalone too.
	cmd.Flags().String("port", envOr("WIKI_MCP_PORT", "8081"), "MCP server port (env: WIKI_MCP_PORT)")
	cmd.Flags().Bool("watch", envOrBool("WIKI_WATCH", true), "watch vault directory for filesystem changes (env: WIKI_WATCH)")

	cmd.AddCommand(newServeMCPHTTPCmd())
	cmd.AddCommand(newServeMCPStdioCmd())

	return cmd
}

// runServeMCPDeprecated handles the bare `serve mcp` invocation by warning
// once on stderr and falling through to the http transport. Kept for one
// release of grace so existing scripts/configs don't break instantly.
func runServeMCPDeprecated(cmd *cobra.Command, args []string) error {
	fmt.Fprintln(os.Stderr, "warning: 'wiki-server serve mcp' is deprecated; use 'wiki-server serve mcp http' instead")
	return runServeMCPHTTP(cmd, args)
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

func runServeMCPHTTP(cmd *cobra.Command, _ []string) error {
	port, _ := cmd.Flags().GetString("port")
	vaultDir, _ := cmd.Root().Flags().GetString("vault")
	watchEnabled, _ := cmd.Flags().GetBool("watch")
	instanceName, _ := cmd.Flags().GetString("instance-name")

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	v := vault.New(vaultDir)

	directorySvc := service.NewDirectoryService(v)

	mcpNotifier := notify.New(2*time.Second, func(paths []string) {
		if _, _, err := directorySvc.Generate(); err != nil {
			logger.Warn("rebuild notifier: directory generate failed", "error", err)
		}
		logger.Info("rebuild notifier: flushed", "dirty_files", len(paths))
	})
	defer mcpNotifier.Close()

	// Build webhook dispatch pipeline (opt-in via WIKI_WEBHOOKS_CONFIG).
	pipeline, err := buildDispatchPipeline(vaultDir, logger, nil)
	if err != nil {
		return fmt.Errorf("webhook dispatcher setup: %w", err)
	}
	defer func() {
		if pipeline != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if cerr := pipeline.closer(shutdownCtx); cerr != nil {
				logger.Warn("webhook dispatcher close", "error", cerr)
			}
		}
	}()

	// Start filesystem watcher for standalone MCP mode (no Quartz builder).
	if watchEnabled {
		var watcherSink notify.Sink = mcpNotifier
		if pipeline != nil {
			watcherSink = notify.NewFanoutSink(mcpNotifier, pipeline.sink)
		}
		vaultWatcher, watchErr := notify.NewVaultWatcher(vaultDir, watcherSink,
			notify.WithExcludeDirs([]string{".obsidian", "raw", "private"}),
			notify.WithWatcherLogger(logger),
		)
		if watchErr != nil {
			logger.Warn("filesystem watcher failed to start", "error", watchErr)
		} else {
			go vaultWatcher.Run()
			defer func() { _ = vaultWatcher.Close() }()
			logger.Info("filesystem watcher started", "vaultDir", vaultDir)
		}
	}

	var mcpDispatchRouter *dispatch.EventRouter
	if pipeline != nil {
		mcpDispatchRouter = pipeline.router
	}
	mcpPageSvc := buildMCPPageSvc(v, mcpNotifier, mcpDispatchRouter, logger)

	// Startup reconciliation.
	if pipeline != nil && pipeline.cfg.ReconcileOnStart {
		paths := scanInboxForReconcile(vaultDir, logger)
		if len(paths) > 0 {
			logger.Info("reconcile on start found pending inbox items", "count", len(paths))
			pipeline.router.RecordReconcile(paths)
		}
	}

	mcpSrv := mcpserver.New(v, nil,
		mcpserver.WithRebuildNotifier(mcpNotifier),
		mcpserver.WithPageService(mcpPageSvc),
		mcpserver.WithInstanceName(instanceName),
	)
	httpTransport := mcpserver.NewStreamableHTTPServer(mcpSrv)

	authMWs, err := buildAuthMiddlewares(context.Background(), logger, authConfigFromEnv())
	if err != nil {
		return fmt.Errorf("MCP auth setup: %w", err)
	}

	var mcpAuthMW func(http.Handler) http.Handler
	if authMWs != nil {
		mcpAuthMW = authMWs.mcp
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", wrapAuth(httpTransport, mcpAuthMW))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})

	httpSrv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("starting wiki MCP server", "port", port, "vaultDir", vaultDir, "instanceName", instanceName)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("MCP server failed: %w", err)
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down MCP server")
	mcpNotifier.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}
