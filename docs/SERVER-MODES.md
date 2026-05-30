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
| Search MCP tool | n/a | ✅ (TF-IDF + substring) | ✅ (substring) | ✅ (substring) |
| TF-IDF search index | ✅ | ✅ | ❌ | ❌ |
| Activity auto-logging on mutations | ✅ | ✅ | ✅ | ✅ |
| Prometheus `/metrics` (unauthenticated) | ✅ | ✅ | ❌ | ❌ |
| Logs to | stdout (JSON) | stdout (JSON) | stdout (JSON) | **stderr** (JSON) |
| Lifetime | long-lived | long-lived | long-lived | per-session |

Stdio logs to stderr because stdout carries the MCP JSON-RPC framing — any stdout log line corrupts the protocol stream.

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
