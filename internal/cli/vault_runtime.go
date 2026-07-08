package cli

import (
	"context"
	"log/slog"
	"time"

	"github.com/jedwards1230/my-wiki/internal/notify"
)

// closePipeline shuts a dispatch pipeline down with a bounded timeout, logging
// any close error. Safe to call with a nil pipeline. Both serve modes defer
// this so the shutdown wiring stays in one place.
func closePipeline(pipeline *dispatchPipeline, logger *slog.Logger) {
	if pipeline == nil {
		return
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if cerr := pipeline.closer(shutdownCtx); cerr != nil {
		logger.Warn("webhook dispatcher close", "error", cerr)
	}
}

// startVaultWatcher starts the filesystem watcher, fanning out to the dispatch
// sink when a pipeline is present. It returns a close func the caller must defer
// to stop the watcher on shutdown, or nil when the watcher failed to start (the
// failure is logged). Callers gate the call on their own watch-enabled flag.
func startVaultWatcher(vaultDir string, notifier *notify.RebuildNotifier, pipeline *dispatchPipeline, logger *slog.Logger) func() {
	var watcherSink notify.Sink = notifier
	if pipeline != nil {
		watcherSink = notify.NewFanoutSink(notifier, pipeline.sink)
	}
	vaultWatcher, watchErr := notify.NewVaultWatcher(vaultDir, watcherSink,
		notify.WithExcludeDirs(excludeDirsFromEnv()),
		notify.WithWatcherLogger(logger),
	)
	if watchErr != nil {
		logger.Warn("filesystem watcher failed to start", "error", watchErr)
		return nil
	}
	go vaultWatcher.Run()
	logger.Info("filesystem watcher started", "vaultDir", vaultDir)
	return func() { _ = vaultWatcher.Close() }
}

// reconcileInboxOnStart synthesizes inbox.changed events for any pending
// inbox/*.md files at boot, after normalizing unsafe filenames to their
// canonical slug. No-op unless the pipeline is enabled and configured for
// reconcile-on-start.
func reconcileInboxOnStart(vaultDir string, pipeline *dispatchPipeline, logger *slog.Logger) {
	if pipeline == nil || !pipeline.cfg.ReconcileOnStart {
		return
	}
	if n := normalizeInboxFilenames(vaultDir, logger); n > 0 {
		logger.Info("reconcile on start normalized inbox filenames", "renamed", n)
	}
	paths := scanInboxForReconcile(vaultDir, logger)
	if len(paths) > 0 {
		logger.Info("reconcile on start found pending inbox items", "count", len(paths))
		pipeline.router.RecordReconcile(paths)
	}
}

// startInboxPoller starts the periodic inbox stat/mtime poller — a fallback for
// inbox writes the fsnotify watcher misses (e.g. across an NFS volume). No-op
// unless the pipeline is enabled and a positive interval is configured. The
// poller runs until ctx is canceled.
func startInboxPoller(ctx context.Context, vaultDir string, pipeline *dispatchPipeline, logger *slog.Logger) {
	if pipeline == nil {
		return
	}
	if interval := inboxPollIntervalFromEnv(logger); interval > 0 {
		poller := newInboxPoller(vaultDir, pipeline.router, interval, logger)
		go poller.Run(ctx)
		logger.Info("inbox poller started", "interval", interval.String())
	}
}
