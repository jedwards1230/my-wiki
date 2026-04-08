package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "wiki_http_requests_total",
		Help: "Total HTTP requests by method, path pattern, and status code.",
	}, []string{"method", "pattern", "status"})

	httpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "wiki_http_request_duration_seconds",
		Help:    "HTTP request duration in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "pattern"})
)

// Metrics records request count and duration for each handler.
func Metrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)

		pattern := routePattern(r)
		method := r.Method
		status := strconv.Itoa(sw.status)

		httpRequestsTotal.WithLabelValues(method, pattern, status).Inc()
		httpRequestDuration.WithLabelValues(method, pattern).Observe(time.Since(start).Seconds())
	})
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.status = code
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.status = http.StatusOK
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// routePattern extracts the matched route pattern from Go 1.22+ ServeMux.
// Falls back to a generic label to avoid high-cardinality explosion.
func routePattern(r *http.Request) string {
	if pat := r.Pattern; pat != "" {
		return pat
	}
	return "unknown"
}
