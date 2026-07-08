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

	"github.com/jedwards1230/my-wiki/internal/dispatch"
	"github.com/jedwards1230/my-wiki/internal/mcpserver"
	"github.com/jedwards1230/my-wiki/internal/notify"
	"github.com/jedwards1230/my-wiki/internal/search"
	"github.com/jedwards1230/my-wiki/internal/service"
	"github.com/jedwards1230/my-wiki/internal/vault"
	"github.com/mark3labs/mcp-go/server"
)

// mcpTransport identifies the wire transport for a standalone MCP runner.
type mcpTransport string

const (
	mcpTransportHTTP  mcpTransport = "http"
	mcpTransportStdio mcpTransport = "stdio"
)

// mcpRunConfig drives runMCP. Feature flags let a single helper express
// every standalone MCP runner: full-featured HTTP, lean stdio, and any
// future combinations. The embedded MCP path (serve --mcp-port) does NOT
// use runMCP — it shares its dependency graph with the HTTP API — but it
// does use buildMCPServer below to construct the MCP server itself.
type mcpRunConfig struct {
	Transport         mcpTransport
	HTTPPort          string // ignored when Transport == mcpTransportStdio
	EnableWatcher     bool
	EnableDispatch    bool
	EnableAuth        bool
	EnableSearchIndex bool // when false, search uses substring only (or no search at all)
	EnableSearch      bool // when false, no search service is wired at all
	InstanceName      string
}

// buildMCPServer constructs the MCP server with the standard set of options
// every transport wires. Centralizing the option list means a new MCP option
// only needs to be added here, not in three runners. searchSvc may be nil
// (no search backend wired); notifier may be nil (no rebuild signaling);
// pages may be nil (mcpserver.New constructs its own default).
func buildMCPServer(
	v *vault.Vault,
	searchSvc *service.SearchService,
	notifier *notify.RebuildNotifier,
	pages *service.PageService,
	instanceName string,
) *server.MCPServer {
	opts := []mcpserver.Option{
		mcpserver.WithInstanceName(instanceName),
		mcpserver.WithBaseURL(os.Getenv(EnvBaseURL)),
	}
	if notifier != nil {
		opts = append(opts, mcpserver.WithRebuildNotifier(notifier))
	}
	if pages != nil {
		opts = append(opts, mcpserver.WithPageService(pages))
	}
	return mcpserver.New(v, searchSvc, opts...)
}

// runMCP runs a standalone MCP server end-to-end: builds the dependency
// graph based on cfg, constructs the MCP server, mounts it on the configured
// transport, and blocks until shutdown. It is the single entry point for
// both `serve mcp http` and `serve mcp stdio`.
//
// The embedded MCP path (serve --mcp-port) is NOT routed through runMCP —
// its dependencies are shared with the REST API and built inline in
// runServeHTTP. That path still uses buildMCPServer to keep MCP option
// wiring identical across all three surfaces.
func runMCP(ctx context.Context, vaultDir string, cfg mcpRunConfig, logger *slog.Logger) error {
	if vaultDir == "" {
		return fmt.Errorf("--vault is required (or set WIKI_VAULT_DIR)")
	}

	v := vault.New(vaultDir)

	// Search: optional, and when enabled may or may not include the TF-IDF
	// index. The standalone HTTP runner historically passed nil; stdio
	// historically wired substring-only. Both behaviors are reproduced via
	// EnableSearch / EnableSearchIndex.
	var searchSvc *service.SearchService
	if cfg.EnableSearch {
		engines := []search.Searcher{search.NewSubstringSearcher(v)}
		if cfg.EnableSearchIndex {
			idx := search.NewIndexSearcher(v)
			if err := idx.Build(); err != nil {
				logger.Warn("search index build failed, index engine not registered", "error", err)
			} else {
				logger.Info("search index built")
				engines = append(engines, idx)
			}
		}
		searchSvc = service.NewSearchService(engines...)
	}

	// Rebuild notifier: only wired when we have something for it to do
	// (a watcher feeding it, or dispatch wanting to fan out from it).
	// Standalone MCP has no HTML renderer; the flush only regenerates
	// the directory index so MCP `list` results stay current after edits.
	var notifier *notify.RebuildNotifier
	if cfg.EnableWatcher || cfg.EnableDispatch {
		directorySvc := service.NewDirectoryService(v, directoryOptionsFromEnv()...)
		notifier = notify.New(2*time.Second, func(paths []string) {
			if _, _, err := directorySvc.Generate(); err != nil {
				logger.Warn("rebuild notifier: directory generate failed", "error", err)
			}
			logger.Info("rebuild notifier: flushed", "dirty_files", len(paths))
		})
		defer notifier.Close()
	}

	// Webhook dispatch pipeline (opt-in via WIKI_WEBHOOKS_CONFIG). The env
	// var is consulted only when the transport opts in; stdio explicitly
	// skips it to keep per-session startup cheap.
	var pipeline *dispatchPipeline
	if cfg.EnableDispatch {
		p, err := buildDispatchPipeline(vaultDir, logger, nil)
		if err != nil {
			return fmt.Errorf("webhook dispatcher setup: %w", err)
		}
		pipeline = p
		defer closePipeline(pipeline, logger)
	}

	// Filesystem watcher — fan out to notifier and dispatch sink when both
	// are present. Exclusions go through excludeDirsFromEnv() so the
	// WIKI_WATCH_EXCLUDE_DIRS override applies uniformly to every runner.
	if cfg.EnableWatcher {
		if stopWatcher := startVaultWatcher(vaultDir, notifier, pipeline, logger); stopWatcher != nil {
			defer stopWatcher()
		}
	}

	// Shared PageService with auto-activity logging and (optional) dispatch
	// routing on mutations. notifier may be nil — buildPageService handles
	// that by suppressing dirty marking in the activity callback.
	var dispatchRouter *dispatch.EventRouter
	if pipeline != nil {
		dispatchRouter = pipeline.router
	}
	pageSvc := buildPageService(v, notifier, dispatchRouter, logger)

	// Startup reconciliation: when dispatch is enabled and configured for
	// reconcile-on-start, synthesize inbox.changed events for any pending
	// inbox/*.md files so consumers see the backlog.
	reconcileInboxOnStart(vaultDir, pipeline, logger)

	// Periodic inbox poll: a stat()/mtime fallback that catches inbox writes
	// the fsnotify watcher misses across an NFS (RWX) volume, where inotify is
	// blind to writes made on another node. Gated on the dispatcher being
	// enabled — without it there is nothing to feed.
	startInboxPoller(ctx, vaultDir, pipeline, logger)

	mcpSrv := buildMCPServer(v, searchSvc, notifier, pageSvc, cfg.InstanceName)

	switch cfg.Transport {
	case mcpTransportHTTP:
		return runMCPHTTP(ctx, mcpSrv, cfg, vaultDir, logger)
	case mcpTransportStdio:
		return runMCPStdio(ctx, mcpSrv, vaultDir, cfg.InstanceName, logger)
	default:
		return fmt.Errorf("unknown MCP transport %q", cfg.Transport)
	}
}

// runMCPHTTP mounts mcpSrv on a streamable-http listener, optionally wraps
// the handler in JWT auth, and blocks until ctx is canceled or the listener
// fails.
func runMCPHTTP(ctx context.Context, mcpSrv *server.MCPServer, cfg mcpRunConfig, vaultDir string, logger *slog.Logger) error {
	httpTransport := mcpserver.NewStreamableHTTPServer(mcpSrv)

	var mcpAuthMW func(http.Handler) http.Handler
	if cfg.EnableAuth {
		authMWs, err := buildAuthMiddlewares(ctx, logger, authConfigFromEnv())
		if err != nil {
			return fmt.Errorf("MCP auth setup: %w", err)
		}
		if authMWs != nil {
			mcpAuthMW = authMWs.mcp
		}
	}

	// Fail closed: the MCP HTTP listener exposes the full read/write/delete/move
	// tool surface over the network. When no auth middleware was built, refuse to
	// start unless the operator explicitly acknowledges via EnvAuthDisabled.
	// (serve mcp stdio is a local subprocess and never reaches this path.)
	if err := requireAuthOrAck(logger, mcpAuthMW != nil, "MCP HTTP server"); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", wrapAuth(httpTransport, mcpAuthMW))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})

	httpSrv := &http.Server{
		Addr:              ":" + cfg.HTTPPort,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("starting wiki MCP server", "port", cfg.HTTPPort, "vaultDir", vaultDir, "instanceName", cfg.InstanceName)
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
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}

// runMCPStdio drives mcpSrv over stdin/stdout. Logs are already routed to
// stderr by the caller (runServeMCPStdio) so this helper only manages the
// mcp-go stdio Listen lifecycle and surfaces clean-shutdown errors as nil.
func runMCPStdio(ctx context.Context, mcpSrv *server.MCPServer, vaultDir, instanceName string, logger *slog.Logger) error {
	stdio := server.NewStdioServer(mcpSrv)

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
// NOT treat context.DeadlineExceeded as success — these helpers run under
// signal.NotifyContext (no deadline), so a deadline error would indicate a
// real bug rather than a clean shutdown.
func isShutdownErr(ctx context.Context, err error) bool {
	return ctx.Err() != nil && errors.Is(err, context.Canceled)
}

// notifyContextWithSignals returns a context canceled on SIGINT/SIGTERM.
// Wrapped in a helper so callers don't repeat the import-heavy signal
// boilerplate.
func notifyContextWithSignals(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, syscall.SIGTERM, syscall.SIGINT)
}
