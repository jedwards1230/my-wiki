package cli

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/jedwards1230/my-wiki/internal/dispatch"
	"github.com/jedwards1230/my-wiki/internal/notify"
	"github.com/jedwards1230/my-wiki/internal/service"
	"github.com/prometheus/client_golang/prometheus"
)

// dispatchPipeline bundles the webhook dispatcher wiring so both serve modes
// can share a single construction path. When the feature is disabled,
// buildDispatchPipeline returns (nil, nil) and callers skip the wiring.
type dispatchPipeline struct {
	cfg        *dispatch.Config
	router     *dispatch.EventRouter
	dispatcher dispatch.Dispatcher
	sink       notify.Sink
	closer     func(context.Context) error
}

// buildDispatchPipeline constructs the webhook dispatcher from the
// WIKI_WEBHOOKS_CONFIG env var. Semantics:
//   - empty env var → return (nil, nil); feature disabled, caller skips.
//   - config path set but file missing or invalid → return an error; startup fails.
//   - valid config → return a ready pipeline; caller wires sink and closer.
//
// registerer is used for Prometheus metric registration; pass nil to use the
// default registry.
func buildDispatchPipeline(vaultDir string, logger *slog.Logger, registerer prometheus.Registerer) (*dispatchPipeline, error) {
	path := strings.TrimSpace(os.Getenv("WIKI_WEBHOOKS_CONFIG"))
	if path == "" {
		return nil, nil
	}
	// LoadConfig treats a missing file as disabled; for this pipeline a
	// path explicitly set to a nonexistent file is a misconfiguration —
	// surface it rather than silently ignoring.
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("WIKI_WEBHOOKS_CONFIG %q: %w", path, err)
	}
	cfg, err := dispatch.LoadConfig(path)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		// LoadConfig returned nil for an existing file — should not happen
		// after the Stat check above, but guard anyway.
		return nil, fmt.Errorf("WIKI_WEBHOOKS_CONFIG %q: unreadable", path)
	}

	httpDispatcher, err := dispatch.NewHTTPDispatcher(cfg, logger, registerer)
	if err != nil {
		return nil, fmt.Errorf("build dispatcher: %w", err)
	}
	router := dispatch.NewEventRouter(cfg, httpDispatcher, logger)

	p := &dispatchPipeline{
		cfg:        cfg,
		router:     router,
		dispatcher: httpDispatcher,
		sink:       newPipelineSink(vaultDir, router),
	}
	p.closer = func(ctx context.Context) error {
		// Stop the router first so in-flight debouncer callbacks don't race
		// with dispatcher close; then drain the dispatcher.
		_ = router.Close()
		return httpDispatcher.Close(ctx)
	}

	logger.Info("webhook dispatcher enabled",
		"config_path", path,
		"consumers", len(cfg.Consumers),
		"reconcile_on_start", cfg.ReconcileOnStart,
	)
	return p, nil
}

// pipelineSink implements notify.Sink by routing inbox/ filesystem events
// into the dispatch router. Paths arriving from VaultWatcher are absolute;
// we convert to vault-relative before handing off. Non-inbox paths are
// silently ignored here — the router applies its own event-specific
// filtering for future event types.
type pipelineSink struct {
	vaultDir string
	router   *dispatch.EventRouter
}

func newPipelineSink(vaultDir string, router *dispatch.EventRouter) notify.Sink {
	return &pipelineSink{vaultDir: vaultDir, router: router}
}

// MarkDirty converts an absolute filesystem path to a vault-relative path
// and forwards it to the router with its action. Paths outside vaultDir or
// above the inbox/ prefix are dropped.
func (s *pipelineSink) MarkDirty(absPath string, action notify.ChangeKind) {
	if s.router == nil {
		return
	}
	rel := toVaultRelative(s.vaultDir, absPath)
	if rel == "" {
		return
	}
	// Normalize to forward slashes for consistent prefix matching on all
	// platforms; the router and config both use forward-slash prefixes.
	rel = filepath.ToSlash(rel)
	if !strings.HasPrefix(rel, "inbox/") && rel != "inbox" {
		return
	}
	s.router.RecordInboxFSChange(rel, action)
}

// toVaultRelative returns the vault-relative path or "" if absPath is not
// within vaultDir. Accepts both absolute and already-relative paths (the
// latter returned verbatim) to remain tolerant of producers that supply
// either form.
func toVaultRelative(vaultDir, absPath string) string {
	if absPath == "" {
		return ""
	}
	if !filepath.IsAbs(absPath) {
		return absPath
	}
	cleaned := filepath.Clean(absPath)
	root := filepath.Clean(vaultDir)
	rel, err := filepath.Rel(root, cleaned)
	if err != nil {
		return ""
	}
	if rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	return rel
}

// mutationAdapter returns an OnMutation callback that routes service-layer
// mutations into the dispatch router in addition to whatever activity/
// notifier work the base callback performs.
func mutationAdapter(router *dispatch.EventRouter, base func(service.MutationEvent)) func(service.MutationEvent) {
	return func(evt service.MutationEvent) {
		if base != nil {
			base(evt)
		}
		if router == nil {
			return
		}
		router.RecordMutation(dispatch.MutationEvent{
			Kind: string(evt.Kind),
			Path: evt.Path,
			From: evt.From,
		})
	}
}

// scanInboxForReconcile walks vaultDir/inbox/ and returns the vault-relative
// paths of all .md files. Returns nil (no error) when the directory is
// missing. Errors from filesystem walks are logged and ignored so a partial
// scan still yields a useful reconcile event — the cost of missing one file
// in startup reconcile is cheap compared to failing server startup.
func scanInboxForReconcile(vaultDir string, logger *slog.Logger) []string {
	inbox := filepath.Join(vaultDir, "inbox")
	if _, err := os.Stat(inbox); err != nil {
		if !os.IsNotExist(err) {
			logger.Warn("reconcile: stat inbox failed", "error", err)
		}
		return nil
	}
	var paths []string
	err := filepath.WalkDir(inbox, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			logger.Warn("reconcile: walk entry error", "path", path, "error", walkErr)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}
		rel, err := filepath.Rel(vaultDir, path)
		if err != nil {
			return nil
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		logger.Warn("reconcile: walk inbox failed", "error", err)
	}
	return paths
}
