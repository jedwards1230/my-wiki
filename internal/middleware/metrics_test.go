package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMetrics_RecordsRequestCount(t *testing.T) {
	handler := Metrics(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/healthz", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if count := testutil.ToFloat64(httpRequestsTotal.WithLabelValues("GET", "unknown", "200")); count < 1 {
		t.Errorf("expected at least 1 request counted, got %f", count)
	}
}

func TestMetrics_RecordsDuration(t *testing.T) {
	handler := Metrics(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	// Histogram should have produced metric families
	count := testutil.CollectAndCount(httpRequestDuration)
	if count == 0 {
		t.Error("expected histogram to have observations")
	}
}

func TestMetrics_CapturesStatusCode(t *testing.T) {
	handler := Metrics(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	req := httptest.NewRequest("GET", "/missing", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	count := testutil.ToFloat64(httpRequestsTotal.WithLabelValues("GET", "unknown", "404"))
	if count < 1 {
		t.Errorf("expected 404 request counted, got %f", count)
	}
}
