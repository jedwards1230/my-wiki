package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLoggingDispatcher_EmitsExpectedFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	d := NewLoggingDispatcher(logger)

	consumer := Consumer{
		Name: "n8n-primary",
		URL:  "https://n8n.example.com/webhook/home-wiki",
	}
	env := Envelope{
		DeliveryID: "did-abc",
		Event:      EventInboxChanged,
		Timestamp:  time.Unix(0, 0).UTC(),
		Source:     SourceAPI,
		Paths:      []string{"inbox/a.md", "inbox/b.md"},
	}

	if err := d.Dispatch(context.Background(), consumer, env); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// Should be a single log line.
	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 1 {
		t.Fatalf("expected 1 log line, got %d: %s", len(lines), buf.String())
	}

	var fields map[string]any
	if err := json.Unmarshal(lines[0], &fields); err != nil {
		t.Fatalf("unmarshal log: %v\nline: %s", err, lines[0])
	}
	wantKeys := []string{"consumer_name", "consumer_url", "event", "delivery_id", "paths_count", "source"}
	for _, k := range wantKeys {
		if _, ok := fields[k]; !ok {
			t.Errorf("log line missing field %q: %s", k, lines[0])
		}
	}
	if fields["consumer_name"] != "n8n-primary" {
		t.Errorf("consumer_name: got %v", fields["consumer_name"])
	}
	if fields["delivery_id"] != "did-abc" {
		t.Errorf("delivery_id: got %v", fields["delivery_id"])
	}
	if fields["event"] != string(EventInboxChanged) {
		t.Errorf("event: got %v", fields["event"])
	}
	// paths_count is serialized as a JSON number
	if n, ok := fields["paths_count"].(float64); !ok || n != 2 {
		t.Errorf("paths_count: got %v", fields["paths_count"])
	}
}

func TestLoggingDispatcher_NilLoggerUsesDefault(t *testing.T) {
	d := NewLoggingDispatcher(nil)
	// Just confirm it doesn't panic; can't easily assert on slog.Default output.
	if err := d.Dispatch(context.Background(), Consumer{Name: "x", URL: "http://x"}, Envelope{Event: EventInboxChanged}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Guard: ensure the log message is the stable dispatch.stub key downstream
// tooling may grep for.
func TestLoggingDispatcher_LogKey(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	d := NewLoggingDispatcher(logger)
	_ = d.Dispatch(context.Background(), Consumer{Name: "n", URL: "http://x"}, Envelope{Event: EventInboxChanged})
	if !strings.Contains(buf.String(), `"msg":"dispatch.stub"`) {
		t.Errorf("expected dispatch.stub log msg, got: %s", buf.String())
	}
}

// TestLoggingDispatcher_RedactsSensitiveURLParts confirms userinfo, query,
// and fragment components are stripped from consumer URLs before logging
// so secrets embedded in URLs don't leak into log sinks.
func TestLoggingDispatcher_RedactsSensitiveURLParts(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	d := NewLoggingDispatcher(logger)

	raw := "https://user:s3cret@n8n.example.com/webhook/home-wiki?token=abc123#x"
	consumer := Consumer{Name: "n8n-primary", URL: raw}
	env := Envelope{Event: EventInboxChanged, DeliveryID: "d"}

	if err := d.Dispatch(context.Background(), consumer, env); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	logged := buf.String()
	// Positive: the safe form must appear.
	if !strings.Contains(logged, "https://n8n.example.com/webhook/home-wiki") {
		t.Errorf("expected safe URL in log, got: %s", logged)
	}
	// Negative: secrets must not.
	for _, forbidden := range []string{"s3cret", "user:s3cret", "token=abc123", "#x"} {
		if strings.Contains(logged, forbidden) {
			t.Errorf("log contains sensitive substring %q: %s", forbidden, logged)
		}
	}
}

func TestSanitizeURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "https://n8n.example.com/hook", "https://n8n.example.com/hook"},
		{"userinfo stripped", "https://u:p@h.example.com/x", "https://h.example.com/x"},
		{"query stripped", "https://h.example.com/x?token=abc", "https://h.example.com/x"},
		{"fragment stripped", "https://h.example.com/x#y", "https://h.example.com/x"},
		{"all three stripped", "https://u:p@h.example.com/x?k=v#f", "https://h.example.com/x"},
		{"no path", "https://h.example.com", "https://h.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeURL(tc.in); got != tc.want {
				t.Errorf("SanitizeURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Unparseable URLs should round-trip verbatim — better to see the literal
// than to swallow it silently.
func TestSanitizeURL_UnparseableFallsBack(t *testing.T) {
	// url.Parse is famously lenient, but control characters fail it.
	bad := "http://\x00/bad"
	got := SanitizeURL(bad)
	if got != bad {
		t.Errorf("SanitizeURL unparseable: got %q, want %q", got, bad)
	}
}
