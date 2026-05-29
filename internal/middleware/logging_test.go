package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLogging_EmitsExpectedFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	handler := Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	req := httptest.NewRequest("GET", "/test/path", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("failed to parse log entry: %v", err)
	}

	if entry["method"] != "GET" {
		t.Errorf("expected method GET, got %v", entry["method"])
	}
	if entry["path"] != "/test/path" {
		t.Errorf("expected path /test/path, got %v", entry["path"])
	}
	// JSON numbers decode as float64
	if status, ok := entry["status"].(float64); !ok || int(status) != 404 {
		t.Errorf("expected status 404, got %v", entry["status"])
	}
	if _, ok := entry["duration_ms"]; !ok {
		t.Error("expected duration_ms field")
	}
}

func TestLogging_CapturesImplicit200(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	handler := Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest("GET", "/", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("failed to parse log entry: %v", err)
	}

	if status, ok := entry["status"].(float64); !ok || int(status) != 200 {
		t.Errorf("expected status 200 for implicit write, got %v", entry["status"])
	}
}
