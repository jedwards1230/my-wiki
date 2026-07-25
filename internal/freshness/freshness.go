// Package freshness observes how recently the vault changed and exports that
// observation as Prometheus metrics, so a deployment can alert on "the vault
// stopped changing" — a dead-man's switch for the sync path.
//
// The failure it exists to catch is one where nothing observable happens: when
// the Obsidian-Sync sidecar dies, new clippings simply stop arriving, which is
// indistinguishable from a quiet week. Downstream automation only fires on
// change, so there is no positive signal to miss. The fix is to emit a positive
// signal unconditionally: the observer runs wherever /metrics is served
// (`serve` / `serve http`), independent of the webhook dispatch pipeline, so a
// default deployment still gets it. The MCP-only entry points do not start it —
// they expose no /metrics endpoint to scrape.
//
// Two gauges make the distinction alertable:
//
//   - wiki_vault_last_modified_timestamp_seconds is stale ⇒ nothing has been
//     written to the vault lately (dead sync, or a genuinely quiet week).
//   - wiki_vault_last_scan_timestamp_seconds is stale or absent ⇒ the observer
//     itself is dead or its scans are failing, so the first gauge means nothing.
//
// Alerting rules live in the deploying cluster's monitoring stack; this package
// only emits the signal. See docs/METRICS.md.
//
// The Observer is a custom prometheus.Collector rather than a set of registered
// gauges on purpose. A registered-but-never-Set gauge reports 0, which reads as
// "last modified at the Unix epoch" — ~56 years stale — and would fire a false
// alarm on every startup, permanently so for a CrashLooping pod. Holding the
// snapshot behind a `valid` flag and emitting nothing until the first
// successful scan removes that failure mode entirely.
package freshness

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/jedwards1230/my-wiki/internal/vault"
)

// DefaultInterval is the scan cadence when Config.Interval is non-positive.
// Deliberately much longer than the 60s inbox poll: a dead-man's switch that
// alerts on "N days" needs no finer granularity, and a full vault walk over NFS
// is far more expensive than the inbox/-only scan.
const DefaultInterval = 5 * time.Minute

// DefaultSyncStateFiles are the basenames treated as evidence that a sync
// process is doing work, when SyncStatePath names a directory. Matched at any
// depth, since obsidian-headless nests state under a per-vault-id directory.
//
// This is an allowlist because a blocklist fails open — see newestMatching.
// Two exclusions are deliberate and worth stating, because both would silently
// defeat the whole metric:
//
//   - sync.log — obsidian-headless tees every log line here, INCLUDING the
//     errors it catches and swallows on each failed poll. A wedged, error-
//     looping sync keeps this file perpetually fresh, so a gauge derived from
//     it stays fresh through exactly the outage it is supposed to catch. (The
//     production incident left a 113MB sync.log against a 22MB vault.) Log
//     rotation that truncates in place bumps its mtime too.
//   - state.db-shm — the WAL shared-memory file is a 3-byte stub recreated on
//     every crash. Its freshness proves the process is CRASHING, not syncing;
//     during the incident it was the one file being touched constantly.
//
// state.db (checkpoints) and state.db-wal (sync writes) both move only when
// real sync work happens. During the incident the WAL sat dirty and never
// checkpointed — i.e. correctly frozen, which is the signal we want.
var DefaultSyncStateFiles = []string{"state.db", "state.db-wal"}

// Metric descriptors. Package-level so Describe and Collect cannot disagree.
var (
	lastModifiedDesc = prometheus.NewDesc(
		"wiki_vault_last_modified_timestamp_seconds",
		"Unix timestamp of the newest file mtime observed in the vault. Staleness means the vault is not receiving writes — a dead sync path, or a genuinely quiet week. Only meaningful when wiki_vault_last_scan_timestamp_seconds is fresh. Absent until the first successful scan, and absent while the vault holds no files.",
		nil, nil,
	)
	lastScanDesc = prometheus.NewDesc(
		"wiki_vault_last_scan_timestamp_seconds",
		"Unix timestamp at which the last successful vault scan completed. This is the dead-man's switch: staleness or absence means the observer (or the whole server) is dead or every scan is failing, so wiki_vault_last_modified_timestamp_seconds cannot be trusted. Absent until the first successful scan.",
		nil, nil,
	)
	// Named without a _total suffix: this is a Gauge (a current count that can
	// go down), and Prometheus reserves _total for counters. promlint flags the
	// mismatch.
	filesDesc = prometheus.NewDesc(
		"wiki_vault_files",
		"Number of files counted by the last successful vault scan, excluding directories and excluded top-level directories. Counts all files, not just markdown. Absent until the first successful scan.",
		nil, nil,
	)
	scanDurationDesc = prometheus.NewDesc(
		"wiki_vault_scan_duration_seconds",
		"Wall-clock duration of the last successful vault scan in seconds. Absent until the first successful scan.",
		nil, nil,
	)
	scanErrorsDesc = prometheus.NewDesc(
		"wiki_vault_scan_errors_total",
		"Total scan cycles that failed because the vault root could not be read. Counts vault failures only — sync-state read failures have their own counter, so a climbing value here unambiguously means the vault is unreadable. Emitted from zero — 0 is a true and meaningful value.",
		nil, nil,
	)
	syncStateLastModifiedDesc = prometheus.NewDesc(
		"wiki_sync_state_last_modified_timestamp_seconds",
		"Unix timestamp of the newest mtime at the configured sync-state path (WIKI_SYNC_STATE_PATH) — a heartbeat file the sync process touches every tick, or a directory. A sync process that touches it on every tick keeps this fresh even when the vault is quiet, which is what separates 'sync alive, vault quiet' from 'sync dead'. Only registered when the path is configured; absent until the path first exists, because a sync process that has never run is not the same as one at the Unix epoch.",
		nil, nil,
	)
	syncStateErrorsDesc = prometheus.NewDesc(
		"wiki_sync_state_read_errors_total",
		"Total sync-state reads that failed for a reason other than the path not existing (permissions, I/O). A missing path is NOT counted here: 'the sync process has not written its heartbeat yet' is a legitimate state, signalled by the absence of wiki_sync_state_last_modified_timestamp_seconds. Only registered when the path is configured.",
		nil, nil,
	)
)

// Config configures an Observer. VaultDir is required; everything else has a
// usable zero value.
type Config struct {
	// VaultDir is the vault root to walk.
	VaultDir string

	// SyncStatePath is an optional path whose newest mtime is exported as
	// wiki_sync_state_last_modified_timestamp_seconds. Empty disables the
	// gauge entirely — the descriptor is not even registered, because a metric
	// we have no value for is worse than no metric.
	//
	// It may be either a single file (a heartbeat the sync process touches
	// every tick) or a directory (newest mtime within). The file form is what
	// works in the split-volume deployment: when the sync process keeps its
	// state on its own RWO volume, the server cannot read that directory at
	// all, so the two sides agree on a heartbeat file placed on the shared
	// volume instead. Kept generic — this package encodes no knowledge of
	// obsidian-headless's layout.
	SyncStatePath string

	// SyncStateFiles are the basenames considered when SyncStatePath is a
	// directory. Nil uses DefaultSyncStateFiles. Ignored for the file form,
	// where the named file IS the signal.
	SyncStateFiles []string

	// Interval is the scan cadence. Non-positive uses DefaultInterval;
	// disabling the observer is the caller's decision, not this package's.
	Interval time.Duration

	// Logger defaults to slog.Default when nil.
	Logger *slog.Logger
}

// snapshot is the result of the last successful scan. valid gates every
// snapshot-derived metric: until the first scan succeeds, Collect emits none of
// them, so no consumer ever sees a zero-value "epoch" timestamp.
type snapshot struct {
	valid        bool
	lastModified time.Time // zero when the vault holds no files
	hasFiles     bool
	files        int
	scanDuration time.Duration
	lastScan     time.Time

	syncStateLastModified time.Time
	syncStateHasValue     bool
}

// Observer periodically walks the vault and exports freshness metrics. It
// implements prometheus.Collector.
type Observer struct {
	vaultDir       string
	syncStatePath  string
	syncStateFiles []string
	interval       time.Duration
	logger         *slog.Logger

	// now is time.Now, overridable in tests.
	now func() time.Time

	// scanMu serializes whole scan cycles. mu alone is not enough: scan reads
	// the previous snapshot, walks the tree without holding a lock (a walk can
	// take seconds on NFS and must never block a scrape), then writes back. Two
	// concurrent scans could interleave those halves and let the slower one
	// write back a staler sync-state observation — a lost update, which the
	// race detector cannot see because every individual access is synchronized.
	// Held across the walk; uncontended in production, where only one Run
	// goroutine scans.
	scanMu sync.Mutex

	mu              sync.Mutex
	snap            snapshot
	scanErrors      float64
	syncStateErrors float64
}

var _ prometheus.Collector = (*Observer)(nil)

// New constructs an Observer and registers it. registerer may be nil, in which
// case metrics are registered on prometheus.DefaultRegisterer — tests should
// pass a fresh prometheus.NewRegistry() to avoid collisions.
//
// Registration failure is returned rather than panicking (no promauto here):
// package-level promauto vars cannot be tested without global-registry
// collisions.
func New(cfg Config, registerer prometheus.Registerer) (*Observer, error) {
	if cfg.VaultDir == "" {
		return nil, errors.New("freshness: VaultDir must not be empty")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.SyncStateFiles == nil {
		cfg.SyncStateFiles = DefaultSyncStateFiles
	}
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}

	o := &Observer{
		vaultDir:       cfg.VaultDir,
		syncStatePath:  cfg.SyncStatePath,
		syncStateFiles: cfg.SyncStateFiles,
		interval:       cfg.Interval,
		logger:         cfg.Logger,
		now:            time.Now,
	}
	if err := registerer.Register(o); err != nil {
		return nil, fmt.Errorf("freshness: register collector: %w", err)
	}
	return o, nil
}

// Interval reports the resolved scan cadence, for startup logging.
func (o *Observer) Interval() time.Duration { return o.interval }

// Run scans immediately, then on every tick until ctx is canceled. The
// immediate scan means the metrics become valid at startup rather than after a
// full interval — a pod that restarts every few minutes still publishes a
// timestamp instead of looking permanently unobserved.
//
// A scan already in flight when ctx is canceled runs to completion and writes
// its result before Run returns; scans are not interrupted mid-walk. Since a
// scan only reads the filesystem and updates in-memory gauges, letting it
// finish is harmless and keeps the last observation as fresh as possible.
//
// Concurrent use is safe: Collect may be called from any goroutine, and scanMu
// serializes scan cycles, so calling Run more than once is sound (each just
// scans on its own schedule).
func (o *Observer) Run(ctx context.Context) {
	o.scan()
	ticker := time.NewTicker(o.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.scan()
		}
	}
}

// scan walks the vault (and the sync-state directory when configured) and
// swaps in a fresh snapshot. A failed scan increments the error counter and
// keeps the previous snapshot: a transient NFS hiccup must not regress a good
// observation to "never scanned", which would look identical to a dead
// observer. This mirrors inboxPoller.poll's keep-previous-snapshot behavior.
func (o *Observer) scan() {
	// Serialize whole cycles so a slow scan cannot have its result clobbered by
	// a faster one that started later. Deliberately NOT o.mu: that one is held
	// only for the snapshot read/write, so a scrape never waits on the walk.
	o.scanMu.Lock()
	defer o.scanMu.Unlock()

	start := o.now()

	o.mu.Lock()
	prev := o.snap
	o.mu.Unlock()

	vaultRes, err := scanTree(o.vaultDir, vault.DefaultExcludedDirs)
	if err != nil {
		o.recordScanError("freshness: vault scan failed, keeping previous snapshot", err)
		return
	}
	end := o.now()

	next := snapshot{
		valid:        true,
		lastModified: vaultRes.newest,
		hasFiles:     vaultRes.files > 0,
		files:        vaultRes.files,
		lastScan:     end,
		scanDuration: end.Sub(start),

		// Carried forward so a sync-state read failure below cannot silently
		// erase the last known-good sync-state observation.
		syncStateLastModified: prev.syncStateLastModified,
		syncStateHasValue:     prev.syncStateHasValue,
	}

	if o.syncStatePath != "" {
		switch mtime, present, serr := readSyncState(o.syncStatePath, o.syncStateFiles); {
		case serr != nil:
			// A real read failure (permissions, I/O). Keep the previous value
			// and count it on the sync-state counter, not the vault one.
			o.recordSyncStateError(serr)
		case present:
			next.syncStateLastModified = mtime
			next.syncStateHasValue = true
		default:
			// The path does not exist yet. That is a legitimate state — the
			// sync process has not written its heartbeat — not an error. Report
			// it by absence: clear the value rather than carrying forward a
			// stale one, so a heartbeat file that gets deleted stops looking
			// alive. No counter moves.
			next.syncStateLastModified = time.Time{}
			next.syncStateHasValue = false
		}
	}

	o.mu.Lock()
	o.snap = next
	o.mu.Unlock()
}

func (o *Observer) recordScanError(msg string, err error) {
	o.mu.Lock()
	o.scanErrors++
	o.mu.Unlock()
	o.logger.Warn(msg, "error", err)
}

func (o *Observer) recordSyncStateError(err error) {
	o.mu.Lock()
	o.syncStateErrors++
	o.mu.Unlock()
	o.logger.Warn("freshness: sync-state read failed, keeping previous value",
		"path", o.syncStatePath, "error", err)
}

// Describe implements prometheus.Collector. The sync-state descriptor is
// advertised only when the directory is configured, so an unconfigured
// deployment does not carry a metric nobody can populate.
func (o *Observer) Describe(ch chan<- *prometheus.Desc) {
	ch <- lastModifiedDesc
	ch <- lastScanDesc
	ch <- filesDesc
	ch <- scanDurationDesc
	ch <- scanErrorsDesc
	if o.syncStatePath != "" {
		ch <- syncStateLastModifiedDesc
		ch <- syncStateErrorsDesc
	}
}

// Collect implements prometheus.Collector. Everything derived from a scan is
// gated on snap.valid — before the first successful scan the only metric
// emitted is the error counter, for which zero is a true value. Emitting an
// unset gauge would publish 0, i.e. "last modified at the Unix epoch", and fire
// a false staleness alarm on every startup.
func (o *Observer) Collect(ch chan<- prometheus.Metric) {
	o.mu.Lock()
	snap := o.snap
	scanErrors := o.scanErrors
	syncStateErrors := o.syncStateErrors
	o.mu.Unlock()

	ch <- prometheus.MustNewConstMetric(scanErrorsDesc, prometheus.CounterValue, scanErrors)
	if o.syncStatePath != "" {
		ch <- prometheus.MustNewConstMetric(syncStateErrorsDesc, prometheus.CounterValue, syncStateErrors)
	}

	if !snap.valid {
		return
	}

	ch <- prometheus.MustNewConstMetric(lastScanDesc, prometheus.GaugeValue, timestamp(snap.lastScan))
	ch <- prometheus.MustNewConstMetric(filesDesc, prometheus.GaugeValue, float64(snap.files))
	ch <- prometheus.MustNewConstMetric(scanDurationDesc, prometheus.GaugeValue, snap.scanDuration.Seconds())

	// An empty vault has no newest mtime. Reporting 0 would be the same epoch
	// trap, so the gauge stays absent until there is a file to report.
	if snap.hasFiles {
		ch <- prometheus.MustNewConstMetric(lastModifiedDesc, prometheus.GaugeValue, timestamp(snap.lastModified))
	}
	if o.syncStatePath != "" && snap.syncStateHasValue {
		ch <- prometheus.MustNewConstMetric(syncStateLastModifiedDesc, prometheus.GaugeValue, timestamp(snap.syncStateLastModified))
	}
}

// timestamp converts t to fractional Unix seconds, the Prometheus convention
// for *_timestamp_seconds metrics.
func timestamp(t time.Time) float64 {
	return float64(t.UnixNano()) / float64(time.Second)
}
