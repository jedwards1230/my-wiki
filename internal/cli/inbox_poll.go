package cli

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jedwards1230/my-wiki/internal/notify"
)

// defaultInboxPollInterval is the poll cadence when EnvInboxPollInterval is
// unset. 60s keeps clipper-drop latency low without meaningfully loading the
// NFS server (one shallow inbox/ walk per minute).
const defaultInboxPollInterval = 60 * time.Second

// inboxChangeRecorder is the slice of *dispatch.EventRouter the poller needs.
// Narrowing to an interface lets tests inject a recording fake instead of
// standing up a full router + debouncer + dispatcher.
type inboxChangeRecorder interface {
	RecordInboxFSChange(path string, action notify.ChangeKind)
}

// inboxPoller periodically walks the inbox/ tree and feeds mtime-based changes
// into the dispatch router. It exists because the fsnotify VaultWatcher is
// blind to writes made on a different kernel: the production deployment runs
// wiki-server and the obsidian-headless sync container in separate pods on
// separate nodes sharing one NFS (RWX) volume, and inotify events do not cross
// NFS clients. stat()/mtime, unlike inotify, DOES propagate over NFS, so a
// periodic mtime diff reliably catches the Obsidian-Sync clipper drops the
// watcher never sees.
//
// Diffing on a (mtime, size) signature rather than mere presence means an inbox
// file the agent leaves in place is dispatched exactly once — on first
// detection — so the poller cannot reintroduce the ghost-file re-dispatch loop.
// Changes funnel through the same router entry point as fsnotify
// (RecordInboxFSChange), so API-mutation dedupe, per-consumer path filters,
// debouncing, and skipAllDeletes suppression all apply unchanged.
type inboxPoller struct {
	vaultDir string
	router   inboxChangeRecorder
	interval time.Duration
	logger   *slog.Logger

	seen map[string]fileSig // vault-relative path -> last-seen signature
}

// fileSig is the change-detection signature for an inbox file. mtime alone is
// ambiguous on NFS (1s resolution): a delete-then-recreate within the same
// second keeps the same mtime, so a content swap could slip past unseen.
// Pairing mtime with size catches that case for any recreate that changes the
// byte count — which a real clipper re-drop almost always does. A replacement
// with identical mtime AND identical size is content-equivalent, so treating it
// as unchanged is correct, not a miss.
type fileSig struct {
	mtime time.Time
	size  int64
}

// newInboxPoller seeds its baseline from the current inbox state so the
// pre-existing backlog (already handled by reconcile-on-start) is not
// re-dispatched. Only changes observed after construction are emitted.
func newInboxPoller(vaultDir string, router inboxChangeRecorder, interval time.Duration, logger *slog.Logger) *inboxPoller {
	p := &inboxPoller{
		vaultDir: vaultDir,
		router:   router,
		interval: interval,
		logger:   logger,
		seen:     map[string]fileSig{},
	}
	if snap, err := scanInboxSnapshot(vaultDir); err == nil {
		p.seen = snap
	} else {
		logger.Warn("inbox poll: initial scan failed; starting from empty baseline", "error", err)
	}
	return p
}

// Run blocks until ctx is canceled, polling on the configured interval.
func (p *inboxPoller) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.poll()
		}
	}
}

// poll walks the inbox, diffs against the last snapshot, routes each change,
// then swaps in the fresh snapshot. A transient walk error keeps the previous
// snapshot so the next tick re-diffs cleanly rather than treating a failed
// scan as a mass deletion.
func (p *inboxPoller) poll() {
	current, err := scanInboxSnapshot(p.vaultDir)
	if err != nil {
		p.logger.Warn("inbox poll: scan failed, keeping previous snapshot", "error", err)
		return
	}
	var created, modified, deleted int
	for path, sig := range current {
		switch prev, ok := p.seen[path]; {
		case !ok:
			p.router.RecordInboxFSChange(path, notify.ChangeCreated)
			created++
		case sig.mtime.After(prev.mtime) || sig.size != prev.size:
			// Newer mtime is the common edit signal; a size change with an
			// equal (or even older, e.g. restore-from-backup) mtime catches a
			// same-second delete-recreate that mtime alone would miss.
			p.router.RecordInboxFSChange(path, notify.ChangeModified)
			modified++
		}
	}
	for path := range p.seen {
		if _, ok := current[path]; !ok {
			p.router.RecordInboxFSChange(path, notify.ChangeDeleted)
			deleted++
		}
	}
	p.seen = current
	if created+modified+deleted > 0 {
		p.logger.Info("inbox poll: detected changes",
			"created", created, "modified", modified, "deleted", deleted)
	}
}

// scanInboxSnapshot returns a map of vault-relative .md path -> (mtime, size)
// signature for every markdown file under inbox/. A missing inbox/ yields an
// empty map and no error. Per-entry walk errors are skipped (best-effort),
// matching scanInboxForReconcile's tolerance.
//
// It skips inbox/review-needed/ (human-curated, excluded from dispatch) and the
// generated index.md, mirroring normalizeInboxFilenames and the consumer's
// path-filter excludes. index.md is regenerated on every rebuild, so tracking
// its churning signature would emit a ChangeModified each tick that the router
// only drops at filter time — wasted work and noisy logs.
func scanInboxSnapshot(vaultDir string) (map[string]fileSig, error) {
	inbox := filepath.Join(vaultDir, "inbox")
	out := map[string]fileSig{}
	if _, err := os.Stat(inbox); err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	err := filepath.WalkDir(inbox, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "review-needed" {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(p) != ".md" || d.Name() == "index.md" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(vaultDir, p)
		if err != nil {
			return nil
		}
		out[filepath.ToSlash(rel)] = fileSig{mtime: info.ModTime(), size: info.Size()}
		return nil
	})
	return out, err
}

// inboxPollIntervalFromEnv resolves the poll interval from
// EnvInboxPollInterval. Unset/empty → defaultInboxPollInterval. A non-positive
// duration disables polling (returns 0). An unparseable value falls back to the
// default and is logged.
func inboxPollIntervalFromEnv(logger *slog.Logger) time.Duration {
	v := strings.TrimSpace(os.Getenv(EnvInboxPollInterval))
	if v == "" {
		return defaultInboxPollInterval
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		logger.Warn("invalid "+EnvInboxPollInterval+", using default",
			"value", v, "default", defaultInboxPollInterval.String(), "error", err)
		return defaultInboxPollInterval
	}
	if d <= 0 {
		return 0
	}
	return d
}
