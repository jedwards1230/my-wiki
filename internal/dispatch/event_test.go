package dispatch

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEnvelopeJSONRoundTrip(t *testing.T) {
	ts, err := time.Parse(time.RFC3339Nano, "2026-04-22T10:00:00.123456789Z")
	if err != nil {
		t.Fatal(err)
	}
	in := Envelope{
		DeliveryID: "abc123",
		Event:      EventInboxChanged,
		Timestamp:  ts,
		Source:     SourceAPI,
		Paths:      []string{"inbox/foo.md", "inbox/bar.md"},
		Prompt: PromptRef{
			Name: "inbox-manager",
			URL:  "https://wiki.example.com/meta/prompts/inbox-manager.md",
		},
		Wiki: WikiLocators{
			BaseURL: "https://wiki.example.com",
			MCPURL:  "https://wiki.example.com/mcp",
		},
	}

	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)

	// Spot-check the required JSON keys are present with their spec names.
	for _, key := range []string{
		`"delivery_id"`,
		`"event"`,
		`"timestamp"`,
		`"source"`,
		`"paths"`,
		`"prompt"`,
		`"wiki"`,
		`"base_url"`,
		`"mcp_url"`,
	} {
		if !strings.Contains(s, key) {
			t.Errorf("marshaled envelope missing key %s: %s", key, s)
		}
	}

	var out Envelope
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.DeliveryID != in.DeliveryID {
		t.Errorf("delivery_id: got %q, want %q", out.DeliveryID, in.DeliveryID)
	}
	if out.Event != in.Event {
		t.Errorf("event: got %q, want %q", out.Event, in.Event)
	}
	if !out.Timestamp.Equal(in.Timestamp) {
		t.Errorf("timestamp: got %v, want %v", out.Timestamp, in.Timestamp)
	}
	if out.Source != in.Source {
		t.Errorf("source: got %q, want %q", out.Source, in.Source)
	}
	if len(out.Paths) != len(in.Paths) {
		t.Errorf("paths: got %v, want %v", out.Paths, in.Paths)
	}
	if out.Prompt != in.Prompt {
		t.Errorf("prompt: got %+v, want %+v", out.Prompt, in.Prompt)
	}
	if out.Wiki != in.Wiki {
		t.Errorf("wiki: got %+v, want %+v", out.Wiki, in.Wiki)
	}
}

func TestEnvelopePathsOmitEmpty(t *testing.T) {
	env := Envelope{
		DeliveryID: "x",
		Event:      EventInboxChanged,
		Timestamp:  time.Unix(0, 0).UTC(),
		Source:     SourceReconcile,
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"paths"`) {
		t.Errorf("paths should be omitted when empty; got: %s", raw)
	}
}

func TestNewDeliveryIDUnique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id := NewDeliveryID()
		if id == "" {
			t.Fatal("empty delivery id")
		}
		if len(id) != 32 {
			t.Errorf("expected 32 hex chars, got %q (len=%d)", id, len(id))
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id after %d iterations: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}
