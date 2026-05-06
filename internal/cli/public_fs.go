package cli

import (
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jedwards1230/my-wiki/internal/memfs"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// memfsMetrics are lazily initialized on the first call to buildPublicFS
// with the in-memory flag on. Lazy so tests (and disabled-by-default
// deployments) don't register unused series on the default registerer.
var (
	memfsMetricsOnce   sync.Once
	memfsFilesGauge    prometheus.Gauge
	memfsBytesGauge    prometheus.Gauge
	memfsReloadTotal   *prometheus.CounterVec
	memfsReloadSeconds prometheus.Histogram
)

func initMemfsMetrics() {
	memfsMetricsOnce.Do(func() {
		memfsFilesGauge = promauto.NewGauge(prometheus.GaugeOpts{
			Name: "wiki_memfs_files",
			Help: "Number of files held in the in-memory public fs snapshot.",
		})
		memfsBytesGauge = promauto.NewGauge(prometheus.GaugeOpts{
			Name: "wiki_memfs_bytes",
			Help: "Total bytes held in the in-memory public fs snapshot.",
		})
		memfsReloadTotal = promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "wiki_memfs_reload_total",
			Help: "Count of in-memory public fs reload attempts by outcome.",
		}, []string{"outcome"}) // success | error
		memfsReloadSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "wiki_memfs_reload_duration_seconds",
			Help:    "Wall-clock duration of a single in-memory public fs reload.",
			Buckets: prometheus.ExponentialBuckets(0.005, 2, 10), // 5ms .. ~5s
		})
	})
}

// buildPublicFS returns an fs.FS for the rendered public directory
// together with an optional cleanup function.
//
// When WIKI_IN_MEMORY_HTML is truthy (1/true/yes — case-insensitive)
// the returned fs.FS is an atomically-swappable in-memory copy kept in
// sync with publicDir via fsnotify. Writes to publicDir (typically
// Quartz rebuilds) produce a full reload into a fresh snapshot that is
// published with a single atomic pointer swap, so requests arriving
// during a rebuild never observe a half-written file.
//
// When the flag is off (default) the previous behavior is preserved:
// fs.FS is os.DirFS and reads go straight to disk. This is the fallback
// path if the in-memory mode ever misbehaves in production — operators
// can flip it off without redeploying by restarting with the env unset.
func buildPublicFS(publicDir string, logger *slog.Logger) (fs.FS, func() error, error) {
	if !envBool("WIKI_IN_MEMORY_HTML") {
		return os.DirFS(publicDir), nil, nil
	}

	initMemfsMetrics()
	mf := memfs.New()
	w, err := memfs.NewWatcher(publicDir, mf, memfs.WatcherOptions{
		Debounce: 250 * time.Millisecond,
		Logger:   logger,
		OnReload: func(files int, bytes int64, d time.Duration, err error) {
			outcome := "success"
			if err != nil {
				outcome = "error"
			} else {
				memfsFilesGauge.Set(float64(files))
				memfsBytesGauge.Set(float64(bytes))
			}
			memfsReloadTotal.WithLabelValues(outcome).Inc()
			// Skip the initial synchronous load (duration 0) from the
			// duration histogram — it measures the operational steady
			// state, not the one-off startup cost.
			if d > 0 {
				memfsReloadSeconds.Observe(d.Seconds())
			}
		},
	})
	if err != nil {
		return nil, nil, err
	}
	w.Start()

	snap := mf.Snapshot()
	logger.Info("in-memory html serve enabled",
		"publicDir", publicDir,
		"files", snap.Files(),
		"bytes", snap.Bytes(),
		"debounce", "250ms",
	)
	return mf, w.Close, nil
}

// envBool parses a boolean-ish env var. Empty/unset → false.
func envBool(name string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
