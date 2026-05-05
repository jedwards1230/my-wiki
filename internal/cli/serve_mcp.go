package cli

import (
	"context"
	"errors"
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
	mcpPageSvc := buildPageService(v, mcpNotifier, mcpDispatchRouter, logger)

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
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
