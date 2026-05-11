# Server Modes

`wiki-server` exposes one binary across four runtime surfaces. They share the same vault, services, and MCP tool surface — they differ in which transports and background workers run.

## The four surfaces

| # | Invocation | Use case |
|---|------------|----------|
| 1 | `wiki-server serve` | HTTP + Quartz, no MCP. Browser-only deployment. |
| 2 | `wiki-server serve --mcp-port=N` | HTTP + Quartz **and** MCP-over-HTTP in one process. **Home K8s prod path.** |
| 3 | `wiki-server serve mcp http` | Standalone MCP-over-HTTP (no Quartz, no REST API). |
| 4 | `wiki-server serve mcp stdio` | Per-session MCP-over-stdio. **Work laptop path.** |

`serve mcp` (no transport) is a deprecated alias — cobra prints a deprecation message and shows help.

## Feature matrix

| Feature | (1) `serve` | (2) `serve --mcp-port` | (3) `serve mcp http` | (4) `serve mcp stdio` |
|---|---|---|---|---|
| HTTP listener (Quartz HTML) | ✅ | ✅ | ❌ | ❌ |
| REST API (`/api/*`) | ✅ | ✅ | ❌ | ❌ |
| Raw file serving (`/raw/*`) | ✅ | ✅ | ❌ | ❌ |
| MCP transport | ❌ | streamable-http | streamable-http | stdio |
| MCP tools (read/write/lint/...) | ❌ | ✅ | ✅ | ✅ |
| `whoami` `instance_name` field | n/a | ✅ | ✅ | ✅ |
| OIDC auth (Authentik) | ✅ | ✅ | ✅ | ❌ |
| Webhook dispatch | ✅ | ✅ | ✅ | ❌ |
| Filesystem watcher (fsnotify) | ✅ | ✅ | ✅ | ❌ |
| Quartz build pipeline | ✅ | ✅ | ❌ | ❌ |
| Search MCP tool | n/a | ✅ (TF-IDF + substring) | ✅ (substring) | ✅ (substring) |
| TF-IDF search index | ✅ | ✅ | ❌ | ❌ |
| Activity auto-logging on mutations | ✅ | ✅ | ✅ | ✅ |
| Prometheus `/metrics` | ✅ | ✅ | ❌ | ❌ |
| Logs to | stdout (JSON) | stdout (JSON) | stdout (JSON) | **stderr** (JSON) |
| Lifetime | long-lived | long-lived | long-lived | per-session |

Stdio routes logs to stderr because stdout is reserved for the MCP JSON-RPC framing — any log line on stdout corrupts the protocol stream.

## Flags by surface

| Flag | (1) | (2) | (3) | (4) |
|---|---|---|---|---|
| `--vault` (root) | ✅ | ✅ | ✅ | ✅ |
| `--instance-name` (root) | ✅ | ✅ | ✅ | ✅ |
| `--port` (HTTP) | ✅ | ✅ | ❌ | ❌ |
| `--public-dir` (Quartz output) | ✅ | ✅ | ❌ | ❌ |
| `--quartz-dir` (Quartz source) | ✅ | ✅ | ❌ | ❌ |
| `--mcp-port` (embed MCP) | ✅ | ✅ | ❌ | ❌ |
| `--port` (MCP-only HTTP) | ❌ | ❌ | ✅ | ❌ |
| `--watch` (fsnotify) | ✅ | ✅ | ✅ | ❌ |

`--instance-name` is honored by every MCP surface. It surfaces as `instance_name` in the `whoami` MCP tool response, letting agents distinguish my-wiki from work-wiki when both are connected.

## When to pick which

**Home K8s deployment (current production):** surface (2). The Helm chart invokes `wiki-server serve --mcp-port=8081` so a single pod serves the website, REST API, and MCP from one process.

**Work laptop:** surface (4). Register in `.mcp.json`:
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
Optional companion: `wiki-server launchd install` schedules a daily `lint` via macOS LaunchAgent.

**MCP-only browser-less server (rare):** surface (3). Useful if you want MCP access from outside the cluster without exposing the Quartz site, or for testing the MCP layer in isolation. Note: no TF-IDF index here either — substring search only.

**Browser-only (no MCP):** surface (1). The default `serve` invocation without `--mcp-port`. Useful if you're hosting the wiki for read-only human consumption and don't want any agent surface.

## Unified construction

The three MCP server surfaces share construction through two helpers in `internal/cli/mcp_runner.go`:

- `buildMCPServer(...)` — single source of truth for MCP option wiring. Surfaces (2), (3), and (4) all call it.
- `runMCP(ctx, vaultDir, cfg, logger)` — end-to-end runner for the two standalone surfaces. Drives the dependency graph from a small `mcpRunConfig` struct (`Transport`, `EnableWatcher`, `EnableDispatch`, `EnableAuth`, `EnableSearch`, `EnableSearchIndex`, `InstanceName`, `HTTPPort`).

`serve mcp http` and `serve mcp stdio` are now thin shims that pre-set the config and call `runMCP`. The embedded MCP path (surface (2)) keeps its inline construction because it shares its dependency graph with the REST API, but it still uses `buildMCPServer` so the MCP option set stays identical across all three surfaces.

Adding a new flag (e.g. webhook dispatch from stdio mutations) is now a config-struct field flip rather than a copy-paste between runners.
