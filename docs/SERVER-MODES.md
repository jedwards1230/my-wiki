# Server Modes

One binary, four runtime surfaces. They share the same vault, services, and MCP tools — they differ in which transports and background workers run.

| # | Invocation | Use case |
|---|------------|----------|
| 1 | `wiki-server serve` | HTTP, no MCP. Browser-only. |
| 2 | `wiki-server serve --mcp-port=N` | HTTP + MCP-over-HTTP in one process. **Home K8s prod.** |
| 3 | `wiki-server serve mcp http` | Standalone MCP-over-HTTP (no REST). |
| 4 | `wiki-server serve mcp stdio` | Per-session MCP-over-stdio. **Work laptop.** |

`serve mcp` (no transport) is a deprecated alias — prints a deprecation message and shows help.

## Feature matrix

| Feature | (1) `serve` | (2) `--mcp-port` | (3) `mcp http` | (4) `mcp stdio` |
|---|---|---|---|---|
| HTTP listener (rendered HTML) | ✅ | ✅ | ❌ | ❌ |
| REST API (`/api/*`) | ✅ | ✅ | ❌ | ❌ |
| Raw file serving (`/raw/*`) | ✅ | ✅ | ❌ | ❌ |
| MCP transport | ❌ | streamable-http | streamable-http | stdio |
| MCP tools (read/write/lint/...) | ❌ | ✅ | ✅ | ✅ |
| `whoami` `instance_name` field | n/a | ✅ | ✅ | ✅ |
| OIDC auth (Authentik) | ✅ | ✅ | ✅ | ❌ |
| Webhook dispatch | ✅ | ✅ | ✅ | ❌ |
| Filesystem watcher (fsnotify) | ✅ | ✅ | ✅ | ❌ |
| Inbox poll (mtime fallback) | ✅ | ✅ | ✅ | ❌ |
| Search MCP tool | n/a | ✅ (TF-IDF + substring) | ✅ (substring) | ✅ (substring) |
| TF-IDF search index | ✅ | ✅ | ❌ | ❌ |
| Activity auto-logging on mutations | ✅ | ✅ | ✅ | ✅ |
| Prometheus `/metrics` (unauthenticated) | ✅ | ✅ | ❌ | ❌ |
| Logs to | stdout (JSON) | stdout (JSON) | stdout (JSON) | **stderr** (JSON) |
| Lifetime | long-lived | long-lived | long-lived | per-session |

Stdio logs to stderr because stdout carries the MCP JSON-RPC framing — any stdout log line corrupts the protocol stream.

## MCP protocol revision

The MCP layer is `github.com/modelcontextprotocol/go-sdk` (**not** mcp-go). Every MCP surface serves protocol revision **`2026-07-28`** (SEP-2575), and still negotiates down to `2025-11-25`, `2025-06-18`, `2025-03-26`, and `2024-11-05` for older clients.

| Transport | Revisions served | Precondition |
|---|---|---|
| streamable-http — surfaces (2), (3) | `2026-07-28` … `2024-11-05` | **stateless mode is mandatory for `2026-07-28`** |
| stdio — surface (4) | `2026-07-28` … `2024-11-05` | none |

**HTTP requires stateless mode.** `NewStreamableHTTPServer` passes `mcp.StreamableHTTPOptions{Stateless: true}`; the SDK's `StreamableServerTransport.SupportsProtocolVersion` withholds `2026-07-28` from a stateful handler. Dropping that option does **not** produce an error — the revision simply disappears from `server/discover`'s `supportedVersions` and clients silently downgrade. `TestDiscoverAdvertisesNewProtocolOverHTTP` (`internal/mcpserver/protocol_test.go`) exists to catch exactly that; it has been mutation-checked to fail when `Stateless` is flipped to `false`.

In stateless mode only `POST` is served: `DELETE /mcp` (session teardown) and a standalone `GET /mcp` (the legacy SSE stream, replaced by `subscriptions/listen`) both answer `405` with `Allow: POST`.

Implemented by the SDK transport, not by this repo: the mandatory `server/discover` RPC, `resultType` on results, `Mcp-Method`/`Mcp-Name`/`MCP-Protocol-Version` header validation, and the removal of `ping` / `logging/setLevel`.

This repo's contribution to the revision is three things, two of which are choices rather than code — and all three are load-bearing:

1. **The stateless precondition** — `Stateless: true` in `NewStreamableHTTPServer`. Without it the revision is silently withheld (above).
2. **The federation invariant** — leaving `mcp.ServerOptions.Capabilities` nil (below).
3. **The `ttlMs`/`cacheScope` cache hints** — the only added code (`cacheHintMiddleware` in `internal/mcpserver/server.go`).

### ContextForge federation invariant — do not "clean up"

`mcp.ServerOptions.Capabilities` is left **nil** in `internal/mcpserver/server.go`, on purpose. The SDK then auto-derives `resources:{listChanged:true}` from the registered `wiki://schema` resource, which serializes as a truthy object. ContextForge gates resource federation on `if capabilities.get("resources"):` — an explicit-but-empty `&mcp.ResourceCapabilities{}` serializes as `{}`, which is **falsy in Python** and silently kills federation.

In go-sdk v1.7.0 both `initialize` and the new `server/discover` build their capabilities from the same `Server.capabilities()`, so this holds on both surfaces. `TestLegacyInitializeKeepsFederationCapabilities` and `TestDiscoverAdvertisesFederationSafeCapabilities` assert it on the wire. The advertised `logging: {}` capability is likewise left alone.

## Flags by surface

| Flag | (1) | (2) | (3) | (4) |
|---|---|---|---|---|
| `--vault` (root) | ✅ | ✅ | ✅ | ✅ |
| `--instance-name` (root) | ✅ | ✅ | ✅ | ✅ |
| `--port` (HTTP) | ✅ | ✅ | ❌ | ❌ |
| `--mcp-port` (embed MCP) | ✅ | ✅ | ❌ | ❌ |
| `--port` (MCP-only HTTP) | ❌ | ❌ | ✅ | ❌ |
| `--watch` (fsnotify) | ✅ | ✅ | ✅ | ❌ |

`--instance-name` surfaces as `instance_name` in the `whoami` MCP tool, letting agents distinguish my-wiki from work-wiki when both are connected.

## When to pick which

- **(2)** — Home K8s production. Helm invokes `serve --mcp-port=8081`; one pod serves website, REST, and MCP.
- **(4)** — Work laptop. Register in `.mcp.json` (below). Optional: `wiki-server launchd install` for a daily `lint`.
- **(3)** — MCP-only, no browser. For MCP access without exposing the site, or testing the MCP layer. Substring search only (no TF-IDF).
- **(1)** — Browser-only (default `serve` without `--mcp-port`). Read-only human consumption, no agent surface.

`.mcp.json` for surface (4):
```json
{
  "mcpServers": {
    "work-wiki": {
      "command": "wiki-server",
      "args": ["--vault", "/path/to/your/vault",
               "--instance-name", "work-wiki",
               "serve", "mcp", "stdio"]
    }
  }
}
```

## Unified construction

The three MCP surfaces share two helpers in `internal/cli/mcp_runner.go`:

- `buildMCPServer(...)` — single source of truth for MCP option wiring. Surfaces (2), (3), (4) all call it.
- `runMCP(ctx, vaultDir, cfg, logger)` — end-to-end runner for the standalone surfaces, driven by an `mcpRunConfig` struct (`Transport`, `EnableWatcher`, `EnableDispatch`, `EnableAuth`, `EnableSearch`, `EnableSearchIndex`, `InstanceName`, `HTTPPort`).

`serve mcp http`/`stdio` are thin shims that pre-set the config and call `runMCP`. Surface (2) keeps inline construction (it shares its dependency graph with the REST API) but still uses `buildMCPServer` for an identical MCP option set. Adding a flag is a config-field flip, not a copy-paste.
