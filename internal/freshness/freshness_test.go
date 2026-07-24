package freshness

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

const (
	mLastModified   = "wiki_vault_last_modified_timestamp_seconds"
	mLastScan       = "wiki_vault_last_scan_timestamp_seconds"
	mFiles          = "wiki_vault_files"
	mScanDuration   = "wiki_vault_scan_duration_seconds"
	mScanErrors     = "wiki_vault_scan_errors_total"
	mSyncStateMtime = "wiki_sync_state_last_modified_timestamp_seconds"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// writeFile writes content at dir/rel (creating parents) and stamps its mtime.
func writeFile(t *testing.T, dir, rel string, mtime time.Time) {
	t.Helper()
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(abs, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	if err := os.Chtimes(abs, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", rel, err)
	}
}

// newTestObserver builds an Observer on a private registry so tests never touch
// the default one.
func newTestObserver(t *testing.T, cfg Config) (*Observer, *prometheus.Registry) {
	t.Helper()
	if cfg.Logger == nil {
		cfg.Logger = testLogger()
	}
	reg := prometheus.NewRegistry()
	o, err := New(cfg, reg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return o, reg
}

// gaugeValue returns the sample value of a single-series metric family from
// reg, plus whether the family was exported at all. Presence is the assertion
// that matters for the zero-value trap; the value matters for the rest.
func gaugeValue(t *testing.T, reg *prometheus.Registry, name string) (float64, bool) {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, fam := range families {
		if fam.GetName() != name {
			continue
		}
		metrics := fam.GetMetric()
		if len(metrics) != 1 {
			t.Fatalf("%s: want exactly 1 series, got %d", name, len(metrics))
		}
		return sampleValue(t, name, metrics[0]), true
	}
	return 0, false
}

func sampleValue(t *testing.T, name string, m *dto.Metric) float64 {
	t.Helper()
	switch {
	case m.GetGauge() != nil:
		return m.GetGauge().GetValue()
	case m.GetCounter() != nil:
		return m.GetCounter().GetValue()
	default:
		t.Fatalf("%s: unsupported metric type %v", name, m)
		return 0
	}
}

func mustAbsent(t *testing.T, reg *prometheus.Registry, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, ok := gaugeValue(t, reg, name); ok {
			t.Errorf("%s: want absent, but it was exported", name)
		}
	}
}

func mustValue(t *testing.T, reg *prometheus.Registry, name string, want float64) {
	t.Helper()
	got, ok := gaugeValue(t, reg, name)
	if !ok {
		t.Fatalf("%s: want exported, but it was absent", name)
	}
	if got != want {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

// TestCollectBeforeFirstScanEmitsNoSnapshotMetrics is the zero-value-trap test,
// and the most important one in this file.
//
// A registered-but-never-Set gauge reports 0, which a consumer reads as "last
// modified at the Unix epoch" — ~56 years stale. That would fire a false
// staleness alarm on every startup and fire it permanently for a CrashLooping
// pod: strictly worse than having no metric. So before the first successful
// scan, Collect must emit NOTHING snapshot-derived. Only the error counter is
// exported, because 0 scan errors is a true and meaningful value.
//
// If someone later replaces the const-metric collector with plain registered
// gauges, or drops the `valid` guard in Collect, this test goes red.
func TestCollectBeforeFirstScanEmitsNoSnapshotMetrics(t *testing.T) {
	vaultDir := t.TempDir()
	writeFile(t, vaultDir, "note.md", time.Unix(1_700_000_000, 0))

	o, reg := newTestObserver(t, Config{VaultDir: vaultDir, SyncStateDir: t.TempDir()})

	// No scan has run: every snapshot-derived metric must be absent.
	mustAbsent(t, reg, mLastModified, mLastScan, mFiles, mScanDuration, mSyncStateMtime)

	// The error counter is exported from zero — its zero value is honest.
	mustValue(t, reg, mScanErrors, 0)
	if got := testutil.CollectAndCount(o); got != 1 {
		t.Fatalf("collected %d metrics before first scan, want exactly 1 (the error counter)", got)
	}

	// Sanity: the very same collector does export them once a scan succeeds,
	// so the assertions above are about scan state and not about the metric
	// names being misspelled.
	o.scan()
	for _, name := range []string{mLastModified, mLastScan, mFiles, mScanDuration} {
		if _, ok := gaugeValue(t, reg, name); !ok {
			t.Errorf("%s: absent after a successful scan", name)
		}
	}
}

func TestScanExportsFreshness(t *testing.T) {
	vaultDir := t.TempDir()
	oldest := time.Unix(1_700_000_000, 0)
	newest := oldest.Add(48 * time.Hour)

	writeFile(t, vaultDir, "note.md", oldest)
	writeFile(t, vaultDir, "deep/nested/clip.md", newest) // newest wins across subdirs
	writeFile(t, vaultDir, "raw/scan.pdf", oldest)        // non-markdown counts too

	o, reg := newTestObserver(t, Config{VaultDir: vaultDir})
	o.scan()

	mustValue(t, reg, mLastModified, float64(newest.UnixNano())/float64(time.Second))
	mustValue(t, reg, mFiles, 3)
	mustValue(t, reg, mScanErrors, 0)

	lastScan, ok := gaugeValue(t, reg, mLastScan)
	if !ok {
		t.Fatal(mLastScan + " absent after a successful scan")
	}
	if delta := time.Since(time.Unix(0, int64(lastScan*float64(time.Second)))); delta > time.Minute || delta < 0 {
		t.Errorf("%s is %v away from now, want ~0", mLastScan, delta)
	}
	if _, ok := gaugeValue(t, reg, mScanDuration); !ok {
		t.Error(mScanDuration + " absent after a successful scan")
	}
}

// TestScanExcludesObsidianDir pins the two exclusion rules that keep the gauge
// honest: .obsidian/ churns on every editor action (it would mask a dead sync
// by keeping last_modified fresh), and directory mtimes are inconsistent across
// filesystems so they never contribute.
func TestScanExcludesObsidianDir(t *testing.T) {
	vaultDir := t.TempDir()
	real := time.Unix(1_700_000_000, 0)
	churn := real.Add(72 * time.Hour)

	writeFile(t, vaultDir, "note.md", real)
	writeFile(t, vaultDir, ".obsidian/workspace.json", churn)
	writeFile(t, vaultDir, ".obsidian/plugins/x/data.json", churn)

	// A directory stamped newer than every file must not contribute.
	dir := filepath.Join(vaultDir, "empty-dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chtimes(dir, churn, churn); err != nil {
		t.Fatalf("chtimes dir: %v", err)
	}

	o, reg := newTestObserver(t, Config{VaultDir: vaultDir})
	o.scan()

	mustValue(t, reg, mLastModified, float64(real.UnixNano())/float64(time.Second))
	mustValue(t, reg, mFiles, 1)
}

// TestScanEmptyVault documents the empty-vault contract: the scan succeeded, so
// the heartbeat and the file count are exported (files = 0), but there is no
// newest mtime to report and reporting 0 would be the same epoch trap — so
// last_modified stays absent.
func TestScanEmptyVault(t *testing.T) {
	o, reg := newTestObserver(t, Config{VaultDir: t.TempDir()})
	o.scan()

	mustValue(t, reg, mFiles, 0)
	mustValue(t, reg, mScanErrors, 0)
	if _, ok := gaugeValue(t, reg, mLastScan); !ok {
		t.Error(mLastScan + " absent after a successful scan of an empty vault")
	}
	mustAbsent(t, reg, mLastModified)
}

// TestScanErrorKeepsPreviousSnapshot: a transient read failure must not regress
// a good observation to "never scanned" — that would look identical to a dead
// observer and defeat the dead-man's switch. Mirrors inboxPoller.poll.
func TestScanErrorKeepsPreviousSnapshot(t *testing.T) {
	parent := t.TempDir()
	vaultDir := filepath.Join(parent, "vault")
	mtime := time.Unix(1_700_000_000, 0)
	writeFile(t, vaultDir, "note.md", mtime)

	o, reg := newTestObserver(t, Config{VaultDir: vaultDir})
	o.scan()
	wantModified := float64(mtime.UnixNano()) / float64(time.Second)
	mustValue(t, reg, mLastModified, wantModified)
	lastScan, _ := gaugeValue(t, reg, mLastScan)

	if err := os.RemoveAll(vaultDir); err != nil {
		t.Fatalf("remove vault: %v", err)
	}
	o.scan()

	mustValue(t, reg, mScanErrors, 1)
	mustValue(t, reg, mLastModified, wantModified)
	mustValue(t, reg, mFiles, 1)
	if got, _ := gaugeValue(t, reg, mLastScan); got != lastScan {
		t.Errorf("%s advanced on a failed scan: got %v, want %v", mLastScan, got, lastScan)
	}
}

func TestSyncStateGauge(t *testing.T) {
	syncMtime := time.Unix(1_700_100_000, 0)

	t.Run("absent when unconfigured", func(t *testing.T) {
		vaultDir := t.TempDir()
		writeFile(t, vaultDir, "note.md", time.Unix(1_700_000_000, 0))

		o, reg := newTestObserver(t, Config{VaultDir: vaultDir})
		o.scan()

		mustAbsent(t, reg, mSyncStateMtime)
		descs := make(chan *prometheus.Desc, 16)
		o.Describe(descs)
		close(descs)
		for d := range descs {
			if d == syncStateLastModifiedDesc {
				t.Error("sync-state descriptor advertised while the directory is unconfigured")
			}
		}
	})

	t.Run("present when configured", func(t *testing.T) {
		vaultDir := t.TempDir()
		writeFile(t, vaultDir, "note.md", time.Unix(1_700_000_000, 0))
		syncDir := t.TempDir()
		writeFile(t, syncDir, "state/sync.json", syncMtime)

		o, reg := newTestObserver(t, Config{VaultDir: vaultDir, SyncStateDir: syncDir})
		o.scan()

		mustValue(t, reg, mSyncStateMtime, float64(syncMtime.UnixNano())/float64(time.Second))
	})

	t.Run("empty sync dir exports nothing", func(t *testing.T) {
		vaultDir := t.TempDir()
		writeFile(t, vaultDir, "note.md", time.Unix(1_700_000_000, 0))

		o, reg := newTestObserver(t, Config{VaultDir: vaultDir, SyncStateDir: t.TempDir()})
		o.scan()

		mustAbsent(t, reg, mSyncStateMtime)
	})

	t.Run("unreadable sync dir keeps the previous value", func(t *testing.T) {
		vaultDir := t.TempDir()
		writeFile(t, vaultDir, "note.md", time.Unix(1_700_000_000, 0))
		parent := t.TempDir()
		syncDir := filepath.Join(parent, "state")
		writeFile(t, syncDir, "sync.json", syncMtime)

		o, reg := newTestObserver(t, Config{VaultDir: vaultDir, SyncStateDir: syncDir})
		o.scan()
		want := float64(syncMtime.UnixNano()) / float64(time.Second)
		mustValue(t, reg, mSyncStateMtime, want)

		if err := os.RemoveAll(syncDir); err != nil {
			t.Fatalf("remove sync dir: %v", err)
		}
		o.scan()

		mustValue(t, reg, mScanErrors, 1)
		mustValue(t, reg, mSyncStateMtime, want)
	})
}

// TestRunScansImmediately covers the startup contract: the first scan happens on
// Run entry, not one interval later, so a pod that restarts every few minutes
// still publishes a heartbeat instead of looking permanently unobserved. It also
// covers ctx cancellation.
func TestRunScansImmediately(t *testing.T) {
	vaultDir := t.TempDir()
	writeFile(t, vaultDir, "note.md", time.Unix(1_700_000_000, 0))

	o, reg := newTestObserver(t, Config{VaultDir: vaultDir, Interval: time.Hour})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		o.Run(ctx)
	}()

	deadline := time.After(5 * time.Second)
	for {
		if _, ok := gaugeValue(t, reg, mLastScan); ok {
			break
		}
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatal("no scan observed within 5s of Run; the immediate first scan is missing")
		case <-time.After(5 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after ctx cancellation")
	}
}

func TestNewValidatesConfig(t *testing.T) {
	if _, err := New(Config{}, prometheus.NewRegistry()); err == nil {
		t.Fatal("New with an empty VaultDir: want error, got nil")
	}

	reg := prometheus.NewRegistry()
	o, err := New(Config{VaultDir: t.TempDir(), Logger: testLogger()}, reg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if o.Interval() != DefaultInterval {
		t.Errorf("Interval() = %v, want the %v default", o.Interval(), DefaultInterval)
	}
	if _, err := New(Config{VaultDir: t.TempDir(), Logger: testLogger()}, reg); err == nil {
		t.Error("re-registering on the same registry: want a collision error, got nil")
	}
}
