package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jedwards1230/my-wiki/internal/api"
	"github.com/jedwards1230/my-wiki/internal/dispatch"
	"github.com/jedwards1230/my-wiki/internal/mcpserver"
	"github.com/jedwards1230/my-wiki/internal/memfs"
	"github.com/jedwards1230/my-wiki/internal/middleware"
	"github.com/jedwards1230/my-wiki/internal/notify"
	"github.com/jedwards1230/my-wiki/internal/render"
	"github.com/jedwards1230/my-wiki/internal/search"
	"github.com/jedwards1230/my-wiki/internal/server"
	"github.com/jedwards1230/my-wiki/internal/service"
	"github.com/jedwards1230/my-wiki/internal/vault"
	"github.com/spf13/cobra"
)

func newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the wiki server",
		Long:  "Start the wiki HTTP server with static site, API, and optionally MCP transport.\n\nUse --mcp-port to start the MCP server alongside the HTTP server in the same process.\nFor MCP-only deployments, use 'serve mcp http' (long-lived, K8s) or 'serve mcp stdio'\n(per-session, embedded in MCP clients like Claude Code).",
	}

	// Add subcommands
	cmd.AddCommand(newServeHTTPCmd())
	cmd.AddCommand(newServeMCPParentCmd())

	// Default to http if no subcommand given
	cmd.RunE = runServeHTTP

	// Flags shared with http subcommand
	cmd.Flags().String("port", envOr(EnvPort, "8080"), "HTTP port (env: "+EnvPort+")")
	cmd.Flags().Int("mcp-port", 0, "MCP server port; when non-zero, starts MCP alongside HTTP (env: "+EnvMCPPort+")")
	cmd.Flags().Bool("watch", envOrBool(EnvWatch, true), "watch vault directory for filesystem changes (env: "+EnvWatch+")")

	return cmd
}

func newServeHTTPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "http",
		Short: "Start the HTTP server (static site + API)",
		RunE:  runServeHTTP,
	}

	cmd.Flags().String("port", envOr(EnvPort, "8080"), "HTTP port (env: "+EnvPort+")")
	cmd.Flags().Int("mcp-port", 0, "MCP server port; when non-zero, starts MCP alongside HTTP (env: "+EnvMCPPort+")")
	cmd.Flags().Bool("watch", envOrBool(EnvWatch, true), "watch vault directory for filesystem changes (env: "+EnvWatch+")")

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

// defaultWatchExcludeDirs lists vault subdirectories the filesystem watcher
// skips by default — only Obsidian editor metadata. raw/ is a normal indexed
// folder now: it is watched like every other directory so new clippings trigger
// a rebuild/reindex (and regenerate raw/'s index.md landings).
var defaultWatchExcludeDirs = []string{".obsidian"}

// excludeDirsFromEnv returns the watcher exclude list, honoring
// EnvWatchExcludeDirs as a comma-separated override. Unset or empty
// falls back to defaultWatchExcludeDirs (matches envOr semantics). To
// disable all exclusions entirely, set the var to whitespace or a lone
// comma — anything that produces no non-empty entries after splitting.
func excludeDirsFromEnv() []string {
	v := os.Getenv(EnvWatchExcludeDirs)
	if v == "" {
		return defaultWatchExcludeDirs
	}
	out := make([]string, 0, strings.Count(v, ",")+1)
	for _, d := range strings.Split(v, ",") {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// directoryOptionsFromEnv builds DirectoryService options from the index env
// vars. Each var is honored only when set (LookupEnv): an unset var keeps the
// service default, while setting it — even to whitespace — overrides. This lets
// EnvIndexNoRecentsDirs be cleared to surface every directory in recents.
//
// raw/ is a normal indexed folder: it is NOT force-excluded from index
// generation. Generate writes an index.md landing into each raw/ directory just
// like any other folder, baked into the snapshot and searchable. Only
// EnvIndexExcludeDirs (when set) adds to the exclude set.
func directoryOptionsFromEnv() []service.DirectoryOption {
	var opts []service.DirectoryOption
	var excludeDirs []string
	if v, ok := os.LookupEnv(EnvIndexExcludeDirs); ok {
		excludeDirs = append(excludeDirs, splitCSV(v)...)
	}
	opts = append(opts, service.WithIndexExcludeDirs(excludeDirs))
	if v, ok := os.LookupEnv(EnvIndexNoRecentsDirs); ok {
		opts = append(opts, service.WithNoRecentsDirs(splitCSV(v)))
	}
	return opts
}

// splitCSV splits a comma-separated list, trimming whitespace and dropping
// empty entries. A non-nil-but-empty result is intentional — it signals an
// explicit "none" override rather than "use the default".
func splitCSV(v string) []string {
	out := make([]string, 0, strings.Count(v, ",")+1)
	for _, d := range strings.Split(v, ",") {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// authConfigFromEnv returns an AuthConfig if EnvAuthIssuer is set, nil otherwise.
func authConfigFromEnv() *middleware.AuthConfig {
	issuer := os.Getenv(EnvAuthIssuer)
	if issuer == "" {
		return nil
	}
	cfg := &middleware.AuthConfig{
		IssuerURL:           issuer,
		Audience:            os.Getenv(EnvAuthAudience),
		AllowAnyUser:        strings.EqualFold(os.Getenv(EnvAuthAllowAnyUser), "true"),
		ResourceMetadataURL: os.Getenv(EnvAuthResourceMetadataURL),
	}
	if groups := os.Getenv(EnvAuthAllowedGroups); groups != "" {
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

// requireAuthOrAck enforces fail-closed auth for network-facing surfaces. When
// auth IS configured (authConfigured true) it is a no-op. When auth is NOT
// configured it requires the operator to explicitly acknowledge running open
// via EnvAuthDisabled: if set truthy, it logs a prominent warning that the
// surface is unauthenticated and the whole vault is exposed, and returns nil;
// otherwise it returns an error refusing to start. surface names the listener
// (e.g. "HTTP REST API", "MCP HTTP server") for the message.
//
// EnvAuthDisabled is parsed with the same truthiness rule as envOrBool
// (case-insensitive "true" or "1").
func requireAuthOrAck(logger *slog.Logger, authConfigured bool, surface string) error {
	if authConfigured {
		return nil
	}
	v := os.Getenv(EnvAuthDisabled)
	if strings.EqualFold(v, "true") || v == "1" {
		logger.Warn("authentication DISABLED — "+surface+" is running with NO authentication; the entire vault is exposed for unauthenticated read, write, delete, and move",
			"surface", surface,
			EnvAuthDisabled, v,
		)
		return nil
	}
	return fmt.Errorf("%s refusing to start without authentication: set %s to an OIDC issuer URL to enable auth, or set %s=true to explicitly run open (exposes the entire vault unauthenticated)", surface, EnvAuthIssuer, EnvAuthDisabled)
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
		logger.Warn("auth enabled with AllowAnyUser=true and no AllowedGroups; every authenticated token has full write access. Set " + EnvAuthAllowedGroups + " to restrict.")
	}
	return &authMiddlewares{mcp: mcpMW, api: apiMW}, nil
}

// makeActivityCallback constructs a mutation callback that appends activity log
// entries and marks the relevant files dirty for renderer rebuild. The returned
// callback is safe for concurrent use. If notifier is nil (e.g. stdio mode),
// dirty marking is skipped — activity entries still append to the vault.
func makeActivityCallback(activitySvc *service.ActivityService, notifier *notify.RebuildNotifier, vaultDir string, logger *slog.Logger) func(service.MutationEvent) {
	var mu sync.Mutex
	return func(evt service.MutationEvent) {
		pagePath := strings.TrimSuffix(evt.Path, ".md")
		// Every mutation links its page via a wikilink. An unresolved target
		// (e.g. a delete, or an inbox staging path) renders muted via the
		// renderer's class="broken" anchor — no per-kind special-casing here.
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
		for _, p := range activitySvc.DirtyPaths() {
			notify.MarkDirtyRelative(notifier, vaultDir, p, notify.ChangeModified)
		}
	}
}

func runServeHTTP(cmd *cobra.Command, _ []string) error {
	port, _ := cmd.Flags().GetString("port")
	vaultDir, _ := cmd.Root().Flags().GetString("vault")
	mcpPort, _ := cmd.Flags().GetInt("mcp-port")
	watchEnabled, _ := cmd.Flags().GetBool("watch")

	// Support env var for mcp-port when flag is at default
	if mcpPort == 0 {
		if envVal := os.Getenv(EnvMCPPort); envVal != "" {
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
	// Fail closed: the HTTP server always registers the REST API write routes
	// (api.NewHandler), so when no auth middleware was built the entire vault
	// is exposed for unauthenticated read/write/delete/move. Refuse to start
	// unless the operator explicitly acknowledges via EnvAuthDisabled. This
	// also covers the in-process MCP listener below (same process, same
	// authMWs), so no separate guard is needed there.
	if err := requireAuthOrAck(logger, authMWs != nil, "HTTP REST API"); err != nil {
		return err
	}

	cfg := server.Config{
		VaultDir: vaultDir,
		Port:     port,
	}

	// Vault instance used by both renderer wiring and the API handler.
	v := vault.New(vaultDir)

	// publicFS is the source of HTML for the static + markdown handlers:
	// a memfs the native renderer populates with the rendered site tree.
	publicFS, _, nativeBuilder, err := buildNativePublicFS(v, logger)
	if err != nil {
		return fmt.Errorf("native renderer setup: %w", err)
	}
	cfg.FragmentRenderer = nativeBuilder
	cfg.RawRenderer = nativeBuilder
	vaultFS := os.DirFS(vaultDir)

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
	directorySvc := service.NewDirectoryService(v, directoryOptionsFromEnv()...)

	// nativeRebuildFS is the *memfs.FS the native builder writes new
	// snapshots into. Captured from publicFS so the rebuild callback can
	// swap snapshots atomically.
	nativeRebuildFS, _ := publicFS.(*memfs.FS)

	// Debounced post-mutation hook: regenerate computed pages and re-render
	// the vault via the native builder into a fresh memfs snapshot.
	notifier := notify.New(2*time.Second, func(paths []string) {
		if _, _, err := directorySvc.Generate(); err != nil {
			logger.Warn("rebuild notifier: directory generate failed", "error", err)
		}
		if nativeRebuildFS != nil {
			snap, err := nativeBuilder.Build(context.Background())
			if err != nil {
				logger.Warn("native rebuild failed", "error", err)
				return
			}
			nativeRebuildFS.Store(snap)
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
	defer closePipeline(pipeline, logger)

	// Start filesystem watcher to detect external changes (Obsidian Sync,
	// git pull, manual edits) and feed them into the rebuild notifier.
	// When the dispatch pipeline is enabled, the watcher sink fans out to
	// both the rebuild notifier and the dispatch pipeline sink.
	if watchEnabled {
		if stopWatcher := startVaultWatcher(vaultDir, notifier, pipeline, logger); stopWatcher != nil {
			defer stopWatcher()
		}
	}

	// Shared PageService with auto-activity logging on mutations. When the
	// dispatch pipeline is enabled, mutations also feed the router so
	// inbox.changed events fire on API/MCP edits.
	var dispatchRouter *dispatch.EventRouter
	if pipeline != nil {
		dispatchRouter = pipeline.router
	}
	pageSvc := buildPageService(v, notifier, dispatchRouter, logger)

	// Startup reconciliation: when enabled and the dispatcher is wired,
	// synthesize an inbox.changed event for any existing inbox/*.md files
	// so consumers pick up the backlog on boot.
	reconcileInboxOnStart(vaultDir, pipeline, logger)

	instanceName, _ := cmd.Flags().GetString("instance-name")

	var apiOpts []api.HandlerOption
	if authMWs != nil {
		apiOpts = append(apiOpts, api.WithAuthMiddleware(authMWs.api))
	}
	if instanceName != "" {
		apiOpts = append(apiOpts, api.WithInstanceName(instanceName))
	}
	if strings.EqualFold(os.Getenv(EnvAuthReads), "true") {
		apiOpts = append(apiOpts, api.WithAuthReads(true))
	}
	apiOpts = append(apiOpts, api.WithRebuildNotifier(notifier))
	apiOpts = append(apiOpts, api.WithPageService(pageSvc))
	if nativeBuilder != nil {
		// Adapt the renderer surface to the small interfaces the API
		// package declares so we don't drag internal/render into
		// internal/api (preserves the api → service → vault layering).
		apiOpts = append(apiOpts, api.WithRenderEndpoints(
			nativeRendererPages{b: nativeBuilder},
			nativeRendererBacklinks{b: nativeBuilder},
		))
	}
	apiHandler := api.NewHandler(v, searchSvc, apiOpts...)

	srv := server.New(cfg, publicFS, vaultFS, logger,
		server.WithAPIRoutes(apiHandler.RegisterRoutes),
	)

	// Graceful shutdown on SIGTERM/SIGINT
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Periodic inbox poll: a stat()/mtime fallback that catches inbox writes
	// the fsnotify watcher misses. Required because the Obsidian-Sync writer
	// and wiki-server run in separate pods on separate nodes over an NFS (RWX)
	// volume, where inotify is blind; stat mtimes propagate over NFS, so a
	// periodic diff reliably sees clipper drops. Gated on the dispatcher being
	// enabled — without it there is nothing to feed.
	startInboxPoller(ctx, vaultDir, pipeline, logger)

	// Start periodic search index rebuild (only if registered)
	if len(engines) > 1 {
		idx.StartAutoRebuild(ctx, 5*time.Minute)
	}

	// Readiness gate: run an initial Build() synchronously and flip ready
	// as soon as it returns.
	snap, err := nativeBuilder.Build(ctx)
	if err != nil {
		return fmt.Errorf("native renderer initial build: %w", err)
	}
	nativeRebuildFS.Store(snap)
	srv.SetReady()
	logger.Info("native renderer ready", "pages", snap.Files(), "bytes", snap.Bytes())

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
		logger.Info("starting wiki-server", "version", version, "port", port, "vaultDir", vaultDir)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("HTTP server failed: %w", err)
		}
	}()

	// Optionally start MCP server in the same process. --instance-name is a
	// root persistent flag (env: WIKI_INSTANCE_NAME) so it surfaces here the
	// same way it does in `serve mcp http` and `serve mcp stdio`. The MCP
	// server itself is built via the shared helper so option wiring stays
	// identical across all three transports.
	if mcpPort > 0 {
		mcpSrv := buildMCPServer(v, searchSvc, notifier, pageSvc, instanceName)
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
			if err := mcpHTTPSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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

// nativeRendererPages adapts *render.Builder to api.RenderPage so the
// internal/api package does not need to import internal/render.
type nativeRendererPages struct{ b *render.Builder }

func (a nativeRendererPages) PageBySlug(slug string) (title, description string, ok bool) {
	p := a.b.PageBySlug(slug)
	if p == nil {
		return "", "", false
	}
	return p.Title, p.Description, true
}

// nativeRendererBacklinks adapts *render.Builder to api.RenderBacklinks.
type nativeRendererBacklinks struct{ b *render.Builder }

func (a nativeRendererBacklinks) Lookup(slug string) []api.RenderBacklinkEntry {
	src := a.b.BacklinkIndex().Lookup(slug)
	if len(src) == 0 {
		return nil
	}
	out := make([]api.RenderBacklinkEntry, len(src))
	for i, e := range src {
		out[i] = api.RenderBacklinkEntry{Title: e.Title, URL: e.URL}
	}
	return out
}
