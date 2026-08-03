package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// protocolRevision is the MCP protocol revision this server is migrating to
// (SEP-2575). It is asserted verbatim rather than read back from the SDK so
// that an SDK-side rename or default change cannot make these tests pass
// vacuously.
const protocolRevision = "2026-07-28"

// legacyRevision is the revision ContextForge federates with today. The
// federation lifeline must keep working on it.
const legacyRevision = "2025-06-18"

// --- wire helpers -----------------------------------------------------------

// rpcRequest is a JSON-RPC 2.0 request with an optional `_meta` block.
type rpcRequest struct {
	method string
	params map[string]any
	// protocolVersion, when non-empty, is sent BOTH in the
	// MCP-Protocol-Version header and in params._meta under
	// io.modelcontextprotocol/protocolVersion. The SDK rejects the request
	// (CodeHeaderMismatch / -32020) if only one of the two is present, so
	// they are set together on purpose.
	protocolVersion string
}

// post sends req to the handler at ts and returns the HTTP status, the raw
// body, and the decoded JSON-RPC response envelope (nil when the body is not a
// JSON-RPC envelope, e.g. a plain-text 4xx).
func post(t *testing.T, ts *httptest.Server, req rpcRequest) (int, string, map[string]any) {
	t.Helper()

	params := req.params
	if params == nil {
		params = map[string]any{}
	}
	if req.protocolVersion != "" {
		params["_meta"] = map[string]any{
			mcp.MetaKeyProtocolVersion: req.protocolVersion,
			// Required alongside protocolVersion: the SDK's
			// validateRequestMeta fails with -32602 without it.
			mcp.MetaKeyClientCapabilities: map[string]any{},
			mcp.MetaKeyClientInfo: map[string]any{
				"name": "my-wiki-protocol-test", "version": "0.0.0",
			},
		}
	}

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  req.method,
		"params":  params,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	httpReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	if req.protocolVersion != "" {
		httpReq.Header.Set("MCP-Protocol-Version", req.protocolVersion)
		// Required from 2026-07-28 on: every request must echo its method
		// in Mcp-Method.
		httpReq.Header.Set("Mcp-Method", req.method)
		// Mcp-Name is required for exactly three methods — tools/call,
		// resources/read, prompts/get — and it is the same header for all
		// three; there is no separate Mcp-Uri. For resources/read the "name"
		// IS the resource URI: the SDK's extractName unmarshals
		// ReadResourceParams and compares the header against p.URI
		// (streamable_headers.go extractName + validateRequestHeaders), so a
		// mismatch is rejected with -32020 HeaderMismatch.
		if name, ok := params["name"].(string); ok {
			httpReq.Header.Set("Mcp-Name", name)
		}
		if uri, ok := params["uri"].(string); ok {
			httpReq.Header.Set("Mcp-Name", uri)
		}
	}

	resp, err := ts.Client().Do(httpReq)
	if err != nil {
		t.Fatalf("POST %s: %v", req.method, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(raw), decodeEnvelope(t, resp.Header.Get("Content-Type"), raw)
}

// decodeEnvelope decodes a streamable-HTTP response body. The handler answers
// with an SSE stream by default (JSONResponse is off), so the JSON-RPC
// envelope arrives as the payload of a `data:` line; a plain application/json
// body is decoded directly.
func decodeEnvelope(t *testing.T, contentType string, raw []byte) map[string]any {
	t.Helper()

	decode := func(b []byte) map[string]any {
		var env map[string]any
		if err := json.Unmarshal(b, &env); err != nil {
			return nil
		}
		return env
	}

	if !strings.HasPrefix(contentType, "text/event-stream") {
		return decode(raw)
	}

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		if env := decode([]byte(strings.TrimSpace(data))); env != nil {
			if _, isRPC := env["jsonrpc"]; isRPC {
				return env
			}
		}
	}
	return nil
}

// resultOf extracts the "result" object from a JSON-RPC envelope, failing the
// test when the envelope carries an error instead.
func resultOf(t *testing.T, env map[string]any, raw string) map[string]any {
	t.Helper()
	if env == nil {
		t.Fatalf("no JSON-RPC envelope in response body: %s", raw)
	}
	if rpcErr, ok := env["error"]; ok {
		t.Fatalf("JSON-RPC error in response: %#v (body: %s)", rpcErr, raw)
	}
	result, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected an object result, got %#v (body: %s)", env["result"], raw)
	}
	return result
}

// newTestHTTPServer stands up the real streamable-HTTP handler (the same one
// `serve mcp http` mounts) over a hermetic temp-dir vault. meta/schema.md is
// seeded so the wiki://schema resource resolves.
func newTestHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()
	v := setupTestVault(t)
	schema := "---\ntitle: Schema\ntags:\n  - meta\ndate: 2026-01-01\n---\n\nOperating manual.\n"
	if err := os.WriteFile(filepath.Join(v.Dir, "meta", "schema.md"), []byte(schema), 0o644); err != nil {
		t.Fatalf("seed meta/schema.md: %v", err)
	}
	ts := httptest.NewServer(NewStreamableHTTPServer(New(v, nil)))
	t.Cleanup(ts.Close)
	return ts
}

// assertTruthyResourcesCapability asserts the ContextForge federation
// invariant on a serialized capabilities object: `resources` must be a
// non-empty object (ContextForge gates federation on
// `if capabilities.get("resources"):`, which a falsy `{}` fails) carrying
// listChanged:true.
func assertTruthyResourcesCapability(t *testing.T, caps map[string]any, where string) {
	t.Helper()
	resources, ok := caps["resources"].(map[string]any)
	if !ok || len(resources) == 0 {
		t.Fatalf("%s: expected a truthy, non-empty capabilities.resources object, got %#v", where, caps["resources"])
	}
	if lc, ok := resources["listChanged"].(bool); !ok || !lc {
		t.Errorf("%s: expected capabilities.resources.listChanged=true, got %#v", where, resources["listChanged"])
	}
}

// --- negotiation ------------------------------------------------------------

// TestDiscoverAdvertisesNewProtocolOverHTTP is the anti-silent-downgrade test.
//
// go-sdk v1.7.0 serves 2026-07-28 over streamable HTTP ONLY when the handler
// is built with StreamableHTTPOptions{Stateless: true}
// (StreamableServerTransport.SupportsProtocolVersion). Without it the version
// is simply absent from server/discover's supportedVersions — no error, no
// warning, a silent downgrade to 2025-11-25. This test fails loudly in that
// case: it asserts the exact string "2026-07-28" is present in the advertised
// list.
//
// MUTATION-CHECKED: flipping Stateless to false turns this test (and
// TestExplicitNewProtocolRequestIsAccepted, TestStatelessMethodNotAllowed,
// TestCacheHintsOnListTools) red. If you weaken this assertion, re-run that
// mutation — a negotiation test that cannot fail is worse than none, because
// it reads as coverage.
func TestDiscoverAdvertisesNewProtocolOverHTTP(t *testing.T) {
	ts := newTestHTTPServer(t)

	status, raw, env := post(t, ts, rpcRequest{method: "server/discover", protocolVersion: protocolRevision})
	if status != http.StatusOK {
		t.Fatalf("server/discover: status = %d, want 200 (body: %s)", status, raw)
	}
	result := resultOf(t, env, raw)

	versionsAny, ok := result["supportedVersions"].([]any)
	if !ok {
		t.Fatalf("expected supportedVersions array in DiscoverResult, got %#v (body: %s)", result["supportedVersions"], raw)
	}
	versions := make([]string, 0, len(versionsAny))
	for _, v := range versionsAny {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("non-string entry in supportedVersions: %#v", v)
		}
		versions = append(versions, s)
	}
	t.Logf("server/discover supportedVersions = %v", versions)

	if !slices.Contains(versions, protocolRevision) {
		t.Fatalf("server/discover did not advertise %q; got %v.\n"+
			"This is the silent-downgrade failure mode: go-sdk v1.7.0 only serves %s over "+
			"streamable HTTP when NewStreamableHTTPServer passes "+
			"StreamableHTTPOptions{Stateless: true}.",
			protocolRevision, versions, protocolRevision)
	}
}

// TestDiscoverAdvertisesFederationSafeCapabilities asserts the ContextForge
// invariant on the NEW discovery surface. server/discover builds its
// capabilities from the same Server.capabilities() that initialize uses, so a
// hand-set (falsy) mcp.ServerOptions.Capabilities would break federation here
// too — this is the assertion that could not be made before 2026-07-28
// existed.
func TestDiscoverAdvertisesFederationSafeCapabilities(t *testing.T) {
	ts := newTestHTTPServer(t)

	status, raw, env := post(t, ts, rpcRequest{method: "server/discover", protocolVersion: protocolRevision})
	if status != http.StatusOK {
		t.Fatalf("server/discover: status = %d, want 200 (body: %s)", status, raw)
	}
	result := resultOf(t, env, raw)

	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("expected a capabilities object in DiscoverResult, got %#v (body: %s)", result["capabilities"], raw)
	}
	assertTruthyResourcesCapability(t, caps, "server/discover")

	tools, ok := caps["tools"].(map[string]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("server/discover: expected a truthy, non-empty capabilities.tools object, got %#v", caps["tools"])
	}
}

// TestExplicitNewProtocolRequestIsAccepted drives a request that explicitly
// declares 2026-07-28 (MCP-Protocol-Version header + the _meta
// protocolVersion/clientCapabilities block the SDK requires) and asserts it is
// served.
//
// A non-stateless handler answers this with HTTP 400 "protocol version
// \"2026-07-28\" is only supported on stateless HTTP servers", so a 200 here
// is direct evidence the stateless precondition holds end to end.
func TestExplicitNewProtocolRequestIsAccepted(t *testing.T) {
	ts := newTestHTTPServer(t)

	status, raw, env := post(t, ts, rpcRequest{
		method:          "tools/list",
		protocolVersion: protocolRevision,
	})
	if status != http.StatusOK {
		t.Fatalf("tools/list at %s: status = %d, want 200 (body: %s)", protocolRevision, status, raw)
	}
	result := resultOf(t, env, raw)

	toolsAny, ok := result["tools"].([]any)
	if !ok || len(toolsAny) == 0 {
		t.Fatalf("expected a non-empty tools array, got %#v (body: %s)", result["tools"], raw)
	}

	// SEP-2575 results carry a discriminator. Verified empirically here
	// rather than implemented: the SDK transport populates it.
	if rt, ok := result["resultType"].(string); !ok || rt == "" {
		t.Errorf("expected a non-empty resultType on a %s result, got %#v", protocolRevision, result["resultType"])
	} else {
		t.Logf("tools/list resultType = %q", rt)
	}
}

// TestLegacyInitializeKeepsFederationCapabilities is the ContextForge
// lifeline: ContextForge still speaks 2025-06-18, and it federates this
// server's resources only when the initialize response advertises a truthy
// `capabilities.resources`. TestInitializeCapabilities asserts the same
// invariant through the in-memory transport; this asserts it on the wire,
// through the real HTTP handler, at the legacy revision.
func TestLegacyInitializeKeepsFederationCapabilities(t *testing.T) {
	ts := newTestHTTPServer(t)

	status, raw, env := post(t, ts, rpcRequest{
		method: "initialize",
		params: map[string]any{
			"protocolVersion": legacyRevision,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "contextforge-stand-in", "version": "0.0.0"},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("initialize at %s: status = %d, want 200 (body: %s)", legacyRevision, status, raw)
	}
	result := resultOf(t, env, raw)

	if got, ok := result["protocolVersion"].(string); !ok || got != legacyRevision {
		t.Errorf("initialize negotiated protocolVersion = %#v, want %q", result["protocolVersion"], legacyRevision)
	}

	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("expected a capabilities object in InitializeResult, got %#v (body: %s)", result["capabilities"], raw)
	}
	assertTruthyResourcesCapability(t, caps, "initialize@"+legacyRevision)
}

// TestStatelessMethodNotAllowed locks in the stateless transport's shape: only
// POST is served. DELETE (session teardown) and a standalone GET (the legacy
// SSE stream, replaced by subscriptions/listen in 2026-07-28) both answer 405
// with an Allow header.
func TestStatelessMethodNotAllowed(t *testing.T) {
	ts := newTestHTTPServer(t)

	for _, method := range []string{http.MethodDelete, http.MethodGet} {
		t.Run(method, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), method, ts.URL, nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req.Header.Set("Accept", "application/json, text/event-stream")
			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatalf("%s: %v", method, err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("%s /: status = %d, want 405", method, resp.StatusCode)
			}
			if allow := resp.Header.Get("Allow"); allow != "POST" {
				t.Errorf("%s /: Allow = %q, want %q", method, allow, "POST")
			}
		})
	}
}

// --- cache hints ------------------------------------------------------------

// TestCacheHintsOnListTools asserts the SEP-2575 ttlMs/cacheScope hints
// cacheHintMiddleware stamps onto tools/list. They can only be set from
// receiving middleware — see the comment on cacheHintMiddleware.
func TestCacheHintsOnListTools(t *testing.T) {
	ts := newTestHTTPServer(t)

	status, raw, env := post(t, ts, rpcRequest{method: "tools/list", protocolVersion: protocolRevision})
	if status != http.StatusOK {
		t.Fatalf("tools/list: status = %d, want 200 (body: %s)", status, raw)
	}
	result := resultOf(t, env, raw)

	ttl, ok := result["ttlMs"].(float64)
	if !ok || ttl <= 0 {
		t.Errorf("tools/list ttlMs = %#v, want a positive number", result["ttlMs"])
	}
	if got := int(ttl); got != listTTLMs {
		t.Errorf("tools/list ttlMs = %d, want %d", got, listTTLMs)
	}
	if scope, ok := result["cacheScope"].(string); !ok || scope == "" {
		t.Errorf("tools/list cacheScope = %#v, want a non-empty string", result["cacheScope"])
	} else if scope != "public" {
		t.Errorf("tools/list cacheScope = %q, want %q", scope, "public")
	}
}

// TestCacheHintsOnReadResource asserts the same for resources/read of
// wiki://schema. The SDK overwrites CacheScope with "public" AFTER the read
// handler returns, so a handler cannot set these — receiving middleware runs
// after that clobber and does control both fields. This test is what proves
// the ttlMs survives it.
func TestCacheHintsOnReadResource(t *testing.T) {
	ts := newTestHTTPServer(t)

	status, raw, env := post(t, ts, rpcRequest{
		method:          "resources/read",
		params:          map[string]any{"uri": "wiki://schema"},
		protocolVersion: protocolRevision,
	})
	if status != http.StatusOK {
		t.Fatalf("resources/read: status = %d, want 200 (body: %s)", status, raw)
	}
	result := resultOf(t, env, raw)

	ttl, ok := result["ttlMs"].(float64)
	if !ok || int(ttl) != readResourceTTLMs {
		t.Errorf("resources/read ttlMs = %#v, want %d", result["ttlMs"], readResourceTTLMs)
	}
	if scope, ok := result["cacheScope"].(string); !ok || scope == "" {
		t.Errorf("resources/read cacheScope = %#v, want a non-empty string", result["cacheScope"])
	}
}

// --- stdio ------------------------------------------------------------------

// TestNonHTTPTransportNegotiatesNewProtocol covers the stdio leg.
//
// In go-sdk v1.7.0, ProtocolVersionSupporter (the interface that can withhold
// a protocol version) is implemented by exactly one transport:
// *StreamableServerTransport. Neither StdioTransport nor the in-memory pair
// implements it, so Server.filterSupportedVersions falls through to "every
// SDK-supported version" for both — the stateless gate is an HTTP-only
// concern, and stdio serves 2026-07-28 unconditionally.
//
// NOTE: the in-memory transport pair used here therefore does NOT gate on
// statelessness. This test is stdio-shaped coverage of the same code path
// (Server.discover over a non-HTTP transport); it is deliberately NOT a
// substitute for TestDiscoverAdvertisesNewProtocolOverHTTP, which is the only
// test that can catch a lost Stateless:true.
func TestNonHTTPTransportNegotiatesNewProtocol(t *testing.T) {
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

	// mcp.Client.Connect probes server/discover first at latestProtocolVersion
	// and only falls back to the legacy initialize handshake if the server
	// does not offer a >= 2026-07-28 version. So a negotiated ProtocolVersion
	// of 2026-07-28 here is proof the discover path won: the legacy fallback
	// caps at 2025-11-25 and could never produce this value.
	res := clientSession.InitializeResult()
	if res == nil {
		t.Fatal("expected a non-nil InitializeResult after connecting")
	}
	t.Logf("in-memory negotiated protocolVersion = %q", res.ProtocolVersion)
	if res.ProtocolVersion != protocolRevision {
		t.Fatalf("non-HTTP transport negotiated %q, want %q", res.ProtocolVersion, protocolRevision)
	}

	assertTruthyResourcesCapabilityTyped(t, res.Capabilities)
}

// assertTruthyResourcesCapabilityTyped round-trips typed capabilities through
// JSON before asserting, because the invariant is about the serialized shape
// ContextForge sees, not the Go value.
func assertTruthyResourcesCapabilityTyped(t *testing.T, caps *mcp.ServerCapabilities) {
	t.Helper()
	raw, err := json.Marshal(caps)
	if err != nil {
		t.Fatalf("marshal capabilities: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal capabilities: %v", err)
	}
	assertTruthyResourcesCapability(t, decoded, "in-memory server/discover")
}
