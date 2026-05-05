package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jedwards1230/home-wiki/internal/api"
	"github.com/jedwards1230/home-wiki/internal/mcpserver"
	"github.com/jedwards1230/home-wiki/internal/middleware"
	"github.com/jedwards1230/home-wiki/internal/notify"
	"github.com/jedwards1230/home-wiki/internal/search"
	"github.com/jedwards1230/home-wiki/internal/server"
	"github.com/jedwards1230/home-wiki/internal/service"
	"github.com/jedwards1230/home-wiki/internal/vault"
	"github.com/spf13/cobra"
)

func newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the wiki server",
		Long:  "Start the wiki HTTP server with static site, API, and optionally MCP transport.\n\nUse --mcp-port to start the MCP server alongside the HTTP server in the same process.\nThe 'serve mcp' subcommand is still available for running the MCP server standalone.",
	}

	// Add subcommands
	cmd.AddCommand(newServeHTTPCmd())
	cmd.AddCommand(newServeMCPCmd())
	cmd.AddCommand(newServeMCPStdioCmd())

	// Default to http if no subcommand given
	cmd.RunE = runServeHTTP

	// Flags shared with http subcommand
	cmd.Flags().String("port", envOr("WIKI_PORT", "8080"), "HTTP port (env: WIKI_PORT)")
	cmd.Flags().String("public-dir", envOr("WIKI_PUBLIC_DIR", "/data/public"), "path to Quartz public output (env: WIKI_PUBLIC_DIR)")
	cmd.Flags().Int("mcp-port", 0, "MCP server port; when non-zero, starts MCP alongside HTTP (env: WIKI_MCP_PORT)")
	cmd.Flags().String("quartz-dir", envOr("WIKI_QUARTZ_DIR", ""), "path to Quartz project directory; enables Quartz build triggering (env: WIKI_QUARTZ_DIR)")
	cmd.Flags().Bool("watch", envOrBool("WIKI_WATCH", true), "watch vault directory for filesystem changes (env: WIKI_WATCH)")

	return cmd
}

func newServeHTTPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "http",
		Short: "Start the HTTP server (static site + API)",
		RunE:  runServeHTTP,
	}

	cmd.Flags().String("port", envOr("WIKI_PORT", "8080"), "HTTP port (env: WIKI_PORT)")
	cmd.Flags().String("public-dir", envOr("WIKI_PUBLIC_DIR", "/data/public"), "path to Quartz public output (env: WIKI_PUBLIC_DIR)")
	cmd.Flags().Int("mcp-port", 0, "MCP server port; when non-zero, starts MCP alongside HTTP (env: WIKI_MCP_PORT)")
	cmd.Flags().String("quartz-dir", envOr("WIKI_QUARTZ_DIR", ""), "path to Quartz project directory; enables Quartz build triggering (env: WIKI_QUARTZ_DIR)")
	cmd.Flags().Bool("watch", envOrBool("WIKI_WATCH", true), "watch vault directory for filesystem changes (env: WIKI_WATCH)")

	return cmd
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envOrBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		return strings.EqualFold(v, "true") || v == "1"
	}
	return fallback
}

// authConfigFromEnv returns an AuthConfig if WIKI_AUTH_ISSUER is set, nil otherwise.
func authConfigFromEnv() *middleware.AuthConfig {
	issuer := os.Getenv("WIKI_AUTH_ISSUER")
	if issuer == "" {
		return nil
	}
	cfg := &middleware.AuthConfig{
		IssuerURL:           issuer,
		Audience:            os.Getenv("WIKI_AUTH_AUDIENCE"),
		AllowAnyUser:        strings.EqualFold(os.Getenv("WIKI_AUTH_ALLOW_ANY_USER"), "true"),
		ResourceMetadataURL: os.Getenv("WIKI_AUTH_RESOURCE_METADATA_URL"),
	}
	if groups := os.Getenv("WIKI_AUTH_ALLOWED_GROUPS"); groups != "" {
		for _, g := range strings.Split(groups, ",") {
			if g = strings.TrimSpace(g); g != "" {
				cfg.AllowedGroups = append(cfg.AllowedGroups, g)
			}
		}
	}
	return cfg
}

// wrapAuth wraps an http.Handler with the given auth middleware. When mw is nil
// it returns the handler unchanged (auth disabled).
func wrapAuth(handler http.Handler, mw func(http.Handler) http.Handler) http.Handler {
	if mw == nil {
		return handler
	}
	return mw(handler)
}

// authMiddlewares holds both text/plain (MCP) and JSON (REST API) auth middlewares.
type authMiddlewares struct {
	mcp func(http.Handler) http.Handler // text/plain 401/403 responses
	api func(http.Handler) http.Handler // JSON envelope 401/403 responses
}

// buildAuthMiddlewares constructs JWT middlewares for both MCP and REST API at startup.
// Returns nil when auth is disabled (cfg nil). On OIDC discovery failure the error is
// surfaced so the server fails fast rather than silently starting without auth.
//
// Two variants are built from the same OIDC config: NewAuth returns text/plain errors
// (matching MCP transport conventions), NewAuthJSON returns JSON envelope errors
// (matching the REST API's response format).
//
// OIDC discovery is bounded by a 30s timeout so a slow or unreachable provider cannot
// hang startup indefinitely (e.g. when Authentik is down during a rolling deploy).
func buildAuthMiddlewares(ctx context.Context, logger *slog.Logger, cfg *middleware.AuthConfig) (*authMiddlewares, error) {
	if cfg == nil {
		return nil, nil
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	mcpMW, err := middleware.NewAuth(discoveryCtx, *cfg)
	if err != nil {
		return nil, err
	}
	apiMW, err := middleware.NewAuthJSON(discoveryCtx, *cfg)
	if err != nil {
		return nil, err
	}
	logger.Info("auth enabled",
		"issuer", cfg.IssuerURL,
		"audience", cfg.Audience,
		"allowed_groups", cfg.AllowedGroups,
		"allow_any_user", cfg.AllowAnyUser,
		"resource_metadata_url", cfg.ResourceMetadataURL,
	)
	if cfg.AllowAnyUser && len(cfg.AllowedGroups) == 0 {
		logger.Warn("auth enabled with AllowAnyUser=true and no AllowedGroups; every authenticated token has full write access. Set WIKI_AUTH_ALLOWED_GROUPS to restrict.")
	}
	return &authMiddlewares{mcp: mcpMW, api: apiMW}, nil
}

// makeActivityCallback constructs a mutation callback that appends activity log
// entries and marks the relevant files dirty for Quartz rebuild. The returned
// callback is safe for concurrent use. If notifier is nil (e.g. stdio mode),
// dirty marking is skipped — activity entries still append to the vault.
func makeActivityCallback(activitySvc *service.ActivityService, notifier *notify.RebuildNotifier, vaultDir string, logger *slog.Logger) func(service.MutationEvent) {
	var mu sync.Mutex
	return func(evt service.MutationEvent) {
		pagePath := strings.TrimSuffix(evt.Path, ".md")
		entry := service.ActivityEntry{
			Type:       string(evt.Kind),
			Title:      fmt.Sprintf("[[%s]]", pagePath),
			AutoLogged: true,
		}
		mu.Lock()
		defer mu.Unlock()
		if err := activitySvc.Append(entry); err != nil {
			logger.Warn("auto-activity failed", "error", err, "path", evt.Path)
		}
		if notifier == nil {
			return
		}
		today := time.Now().Format("2006-01-02")
		notifier.MarkDirty(filepath.Join(vaultDir, "meta", "activity", today+".md"), notify.ChangeModified)
		notifier.MarkDirty(filepath.Join(vaultDir, "meta", "log.md"), notify.ChangeModified)
	}
}

func runServeHTTP(cmd *cobra.Command, _ []string) error {
	port, _ := cmd.Flags().GetString("port")
	publicDir, _ := cmd.Flags().GetString("public-dir")
	vaultDir, _ := cmd.Root().Flags().GetString("vault")
	mcpPort, _ := cmd.Flags().GetInt("mcp-port")
	quartzDir, _ := cmd.Flags().GetString("quartz-dir")
	watchEnabled, _ := cmd.Flags().GetBool("watch")

	// Support env var for mcp-port when flag is at default
	if mcpPort == 0 {
		if envVal := os.Getenv("WIKI_MCP_PORT"); envVal != "" {
			_, _ = fmt.Sscanf(envVal, "%d", &mcpPort)
		}
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	// Set as default so MCP handlers (and any future library code) emit
	// structured logs on the same JSON stream as the server.
	slog.SetDefault(logger)

	// Warn if TZ is set but timezone data is missing (Alpine without tzdata).
	if tz := os.Getenv("TZ"); tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			logger.Warn("TZ set but timezone data unavailable — timestamps will use UTC; install tzdata", "TZ", tz, "error", err)
		}
	}

	// Build auth middlewares early — they protect both the REST API and MCP server.
	authMWs, err := buildAuthMiddlewares(context.Background(), logger, authConfigFromEnv())
	if err != nil {
		return fmt.Errorf("auth setup: %w", err)
	}

	cfg := server.Config{
		PublicDir: publicDir,
		VaultDir:  vaultDir,
		Port:      port,
	}

	publicFS, closePublicFS, err := buildPublicFS(publicDir, logger)
	if err != nil {
		return fmt.Errorf("public fs setup: %w", err)
	}
	if closePublicFS != nil {
		defer func() { _ = closePublicFS() }()
	}
	vaultFS := os.DirFS(vaultDir)

	// Build API handler with search engines
	v := vault.New(vaultDir)

	sub := search.NewSubstringSearcher(v)
	engines := []search.Searcher{sub}

	idx := search.NewIndexSearcher(v)
	if err := idx.Build(); err != nil {
		logger.Warn("search index build failed, index engine not registered", "error", err)
	} else {
		logger.Info("search index built")
		engines = append(engines, idx)
	}
	searchSvc := service.NewSearchService(engines...)

	// Build services needed by the rebuild notifier
	directorySvc := service.NewDirectoryService(v)

	// Create Quartz builder if quartz-dir is configured. This replaces
	// Quartz's built-in --watch mode: the Go server triggers one-shot
	// builds after debounced filesystem changes.
	var quartzBuilder *notify.QuartzBuilder
	if quartzDir != "" {
		quartzBuilder = notify.NewQuartzBuilder(quartzDir, vaultDir, publicDir, logger)
	}

	// Debounced post-mutation hook: regenerate computed pages and trigger
	// a Quartz build when the quartz builder is configured.
	notifier := notify.New(2*time.Second, func(paths []string) {
		if _, _, err := directorySvc.Generate(); err != nil {
			logger.Warn("rebuild notifier: directory generate failed", "error", err)
		}
		if quartzBuilder != nil {
			quartzBuilder.Build()
		}
		logger.Info("rebuild notifier: flushed", "dirty_files", len(paths))
	})
	defer notifier.Close()

	// Build webhook dispatch pipeline (opt-in via WIKI_WEBHOOKS_CONFIG).
	// When disabled, pipeline is nil and subsequent checks skip dispatcher wiring.
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

	// Start filesystem watcher to detect external changes (Obsidian Sync,
	// git pull, manual edits) and feed them into the rebuild notifier.
	// When the dispatch pipeline is enabled, the watcher sink fans out to
	// both the rebuild notifier and the dispatch pipeline sink.
	if watchEnabled {
		var watcherSink notify.Sink = notifier
		if pipeline != nil {
			watcherSink = notify.NewFanoutSink(notifier, pipeline.sink)
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

	// Shared PageService with auto-activity logging on mutations. When the
	// dispatch pipeline is enabled, mutations also feed the router so
	// inbox.changed events fire on API/MCP edits.
	activitySvc := service.NewActivityService(v.Storage)
	onMutation := makeActivityCallback(activitySvc, notifier, vaultDir, logger)
	if pipeline != nil {
		onMutation = mutationAdapter(pipeline.router, onMutation)
	}
	pageSvc := service.NewPageService(v.Storage,
		service.WithExcludedDirs(v.ExcludedDirs),
		service.WithOnMutation(onMutation),
	)

	// Startup reconciliation: when enabled and the dispatcher is wired,
	// synthesize an inbox.changed event for any existing inbox/*.md files
	// so consumers pick up the backlog on boot.
	if pipeline != nil && pipeline.cfg.ReconcileOnStart {
		paths := scanInboxForReconcile(vaultDir, logger)
		if len(paths) > 0 {
			logger.Info("reconcile on start found pending inbox items", "count", len(paths))
			pipeline.router.RecordReconcile(paths)
		}
	}

	var apiOpts []api.HandlerOption
	if authMWs != nil {
		apiOpts = append(apiOpts, api.WithAuthMiddleware(authMWs.api))
	}
	if strings.EqualFold(os.Getenv("WIKI_AUTH_READS"), "true") {
		apiOpts = append(apiOpts, api.WithAuthReads(true))
	}
	apiOpts = append(apiOpts, api.WithRebuildNotifier(notifier))
	apiOpts = append(apiOpts, api.WithPageService(pageSvc))
	apiHandler := api.NewHandler(v, searchSvc, apiOpts...)

	srv := server.New(cfg, publicFS, vaultFS, logger,
		server.WithAPIRoutes(apiHandler.RegisterRoutes),
	)

	// Graceful shutdown on SIGTERM/SIGINT
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Start periodic search index rebuild (only if registered)
	if len(engines) > 1 {
		idx.StartAutoRebuild(ctx, 5*time.Minute)
	}

	// Poll for readiness with cancellation
	go func() {
		indexPath := publicDir + "/index.html"
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := os.Stat(indexPath); err == nil {
					srv.SetReady()
					logger.Info("server ready", "publicDir", publicDir)
					return
				}
			}
		}
	}()

	httpSrv := &http.Server{
		Addr:              ":" + port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Collect servers that need graceful shutdown
	servers := []*http.Server{httpSrv}

	// Start HTTP server
	errCh := make(chan error, 2)
	go func() {
		logger.Info("starting wiki-server", "version", version, "port", port, "publicDir", publicDir, "vaultDir", vaultDir)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("HTTP server failed: %w", err)
		}
	}()

	// Optionally start MCP server in the same process
	if mcpPort > 0 {
		mcpSrv := mcpserver.New(v, searchSvc, mcpserver.WithRebuildNotifier(notifier), mcpserver.WithPageService(pageSvc))
		httpTransport := mcpserver.NewStreamableHTTPServer(mcpSrv)

		mux := http.NewServeMux()
		var mcpAuthMW func(http.Handler) http.Handler
		if authMWs != nil {
			mcpAuthMW = authMWs.mcp
		}
		mux.Handle("/mcp", wrapAuth(httpTransport, mcpAuthMW))
		mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("ok"))
		})

		mcpHTTPSrv := &http.Server{
			Addr:              fmt.Sprintf(":%d", mcpPort),
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      60 * time.Second,
			IdleTimeout:       120 * time.Second,
		}
		servers = append(servers, mcpHTTPSrv)

		go func() {
			logger.Info("starting wiki MCP server", "port", mcpPort, "vaultDir", vaultDir)
			if err := mcpHTTPSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				errCh <- fmt.Errorf("MCP server failed: %w", err)
			}
		}()
	}

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	notifier.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Shut down all servers
	var firstErr error
	for _, s := range servers {
		if err := s.Shutdown(shutdownCtx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func newServeMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Start a standalone MCP server (streamable-http transport)",
		RunE:  runServeMCP,
	}

	cmd.Flags().String("port", envOr("WIKI_MCP_PORT", "8081"), "MCP server port (env: WIKI_MCP_PORT)")
	cmd.Flags().Bool("watch", envOrBool("WIKI_WATCH", true), "watch vault directory for filesystem changes (env: WIKI_WATCH)")

	return cmd
}

func runServeMCP(cmd *cobra.Command, _ []string) error {
	port, _ := cmd.Flags().GetString("port")
	vaultDir, _ := cmd.Root().Flags().GetString("vault")
	watchEnabled, _ := cmd.Flags().GetBool("watch")

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

	// Shared PageService with auto-activity logging on mutations. When the
	// dispatch pipeline is enabled, mutations also feed the router.
	mcpActivitySvc := service.NewActivityService(v.Storage)
	mcpOnMutation := makeActivityCallback(mcpActivitySvc, mcpNotifier, vaultDir, logger)
	if pipeline != nil {
		mcpOnMutation = mutationAdapter(pipeline.router, mcpOnMutation)
	}
	mcpPageSvc := service.NewPageService(v.Storage,
		service.WithExcludedDirs(v.ExcludedDirs),
		service.WithOnMutation(mcpOnMutation),
	)

	// Startup reconciliation.
	if pipeline != nil && pipeline.cfg.ReconcileOnStart {
		paths := scanInboxForReconcile(vaultDir, logger)
		if len(paths) > 0 {
			logger.Info("reconcile on start found pending inbox items", "count", len(paths))
			pipeline.router.RecordReconcile(paths)
		}
	}

	mcpSrv := mcpserver.New(v, nil, mcpserver.WithRebuildNotifier(mcpNotifier), mcpserver.WithPageService(mcpPageSvc))
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
		logger.Info("starting wiki MCP server", "port", port, "vaultDir", vaultDir)
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
