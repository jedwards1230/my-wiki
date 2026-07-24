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
		"Total scan cycles that failed because the vault root (or the configured sync-state directory) could not be read. Emitted from zero — 0 is a true and meaningful value.",
		nil, nil,
	)
	syncStateLastModifiedDesc = prometheus.NewDesc(
		"wiki_sync_state_last_modified_timestamp_seconds",
		"Unix timestamp of the newest file mtime under the configured sync-state directory (WIKI_SYNC_STATE_DIR). A sync process that touches its state on every tick keeps this fresh even when the vault is quiet, separating 'sync alive, vault quiet' from 'sync dead'. Only registered when the directory is configured.",
		nil, nil,
	)
)

// Config configures an Observer. VaultDir is required; everything else has a
// usable zero value.
type Config struct {
	// VaultDir is the vault root to walk.
	VaultDir string

	// SyncStateDir is an optional directory whose newest mtime is exported as
	// wiki_sync_state_last_modified_timestamp_seconds. Empty disables the
	// gauge entirely — the descriptor is not even registered, because a metric
	// we have no value for is worse than no metric.
	SyncStateDir string

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
	syncStateHasFiles     bool
}

// Observer periodically walks the vault and exports freshness metrics. It
// implements prometheus.Collector.
type Observer struct {
	vaultDir     string
	syncStateDir string
	interval     time.Duration
	logger       *slog.Logger

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

	mu         sync.Mutex
	snap       snapshot
	scanErrors float64
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
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}

	o := &Observer{
		vaultDir:     cfg.VaultDir,
		syncStateDir: cfg.SyncStateDir,
		interval:     cfg.Interval,
		logger:       cfg.Logger,
		now:          time.Now,
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
		syncStateHasFiles:     prev.syncStateHasFiles,
	}

	if o.syncStateDir != "" {
		// No exclusions: the sync-state directory is opaque to us on purpose,
		// so this stays generic instead of encoding obsidian-headless layout.
		syncRes, serr := scanTree(o.syncStateDir, nil)
		if serr != nil {
			o.recordScanError("freshness: sync-state scan failed, keeping previous sync-state snapshot", serr)
		} else {
			next.syncStateLastModified = syncRes.newest
			next.syncStateHasFiles = syncRes.files > 0
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

// Describe implements prometheus.Collector. The sync-state descriptor is
// advertised only when the directory is configured, so an unconfigured
// deployment does not carry a metric nobody can populate.
func (o *Observer) Describe(ch chan<- *prometheus.Desc) {
	ch <- lastModifiedDesc
	ch <- lastScanDesc
	ch <- filesDesc
	ch <- scanDurationDesc
	ch <- scanErrorsDesc
	if o.syncStateDir != "" {
		ch <- syncStateLastModifiedDesc
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
	o.mu.Unlock()

	ch <- prometheus.MustNewConstMetric(scanErrorsDesc, prometheus.CounterValue, scanErrors)

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
	if o.syncStateDir != "" && snap.syncStateHasFiles {
		ch <- prometheus.MustNewConstMetric(syncStateLastModifiedDesc, prometheus.GaugeValue, timestamp(snap.syncStateLastModified))
	}
}

// timestamp converts t to fractional Unix seconds, the Prometheus convention
// for *_timestamp_seconds metrics.
func timestamp(t time.Time) float64 {
	return float64(t.UnixNano()) / float64(time.Second)
}
