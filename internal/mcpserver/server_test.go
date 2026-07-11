package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestInitializeCapabilities drives a real MCP initialize handshake (over an
// in-memory transport pair) against the server New builds, and asserts the
// capabilities the SDK derives are ContextForge-federation-safe: truthy/
// non-empty resources and tools objects, never an explicit-but-empty `{}`.
//
// This locks in the ContextForge capability-parity requirement: leaving
// mcp.ServerOptions.Capabilities nil (server.go) makes the SDK auto-derive
// resources:{listChanged:true} from the one registered resource
// (wiki://schema) and tools:{listChanged:true} from the 11 registered tools —
// both serialize as truthy objects, which is what ContextForge's
// `if capabilities.get("resources"):` gate requires. Hand-setting an empty
// *mcp.ResourceCapabilities{} would reintroduce the falsy-`{}` trap.
func TestInitializeCapabilities(t *testing.T) {
	v := setupTestVault(t)
	srv := New(v, nil)

	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = clientSession.Close() }()

	result := clientSession.InitializeResult()
	if result == nil {
		t.Fatal("expected a non-nil InitializeResult after the handshake")
	}

	capsJSON, err := json.MarshalIndent(result.Capabilities, "", "  ")
	if err != nil {
		t.Fatalf("marshal capabilities: %v", err)
	}
	// Captured verbatim for the migration digest — see the PR description /
	// hand-off notes for the exact bytes produced by this assertion.
	t.Logf("initialize capabilities:\n%s", capsJSON)

	var caps map[string]any
	if err := json.Unmarshal(capsJSON, &caps); err != nil {
		t.Fatalf("unmarshal capabilities: %v", err)
	}

	resources, ok := caps["resources"].(map[string]any)
	if !ok || len(resources) == 0 {
		t.Fatalf("expected a truthy, non-empty resources capability, got: %#v", caps["resources"])
	}
	if lc, ok := resources["listChanged"].(bool); !ok || !lc {
		t.Errorf("expected resources.listChanged=true, got %v", resources["listChanged"])
	}

	tools, ok := caps["tools"].(map[string]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("expected a truthy, non-empty tools capability, got: %#v", caps["tools"])
	}
	if lc, ok := tools["listChanged"].(bool); !ok || !lc {
		t.Errorf("expected tools.listChanged=true, got %v", tools["listChanged"])
	}
}
