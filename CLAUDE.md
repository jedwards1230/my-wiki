# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Home Wiki is a Go server (`wiki-server`) that serves an Obsidian vault as a website. It combines:
- **Quartz v4** static site generation (Node.js) for rendered HTML
- **Go HTTP server** for static serving, markdown delivery, REST API, and MCP protocol
- **obsidian-headless** for Obsidian Sync integration

The server exposes three content paths: `/path` (Quartz HTML), `/path.md` (plain markdown from vault), `/raw/path` (native source files with directory listing).

## Commands

```bash
# Build
go build -o wiki-server ./cmd/wiki-server

# Test (default — fast unit tests only)
go test ./...
go test -v -race -coverprofile=coverage.out ./...

# Test including the stdio subprocess integration test (slower, builds binary)
go test -tags=integration -v -race ./...

# Test a single package
go test -v ./internal/search/

# Lint
go vet ./...
golangci-lint run ./...

# Format
gofmt -w .

# Run locally (needs vault dir and Quartz public output)
./wiki-server serve --vault /path/to/vault --public-dir /path/to/quartz/public --port 8080

# Run MCP server standalone over HTTP (streamable-http transport)
./wiki-server serve mcp http --vault /path/to/vault --port 8081

# Run MCP server over stdio (for .mcp.json / Claude Code embedding)
# Logs are written to stderr; stdout is reserved for the JSON-RPC protocol.
# Skips HTTP, Quartz, OIDC, webhook dispatch, and the TF-IDF index.
./wiki-server serve mcp stdio --vault /path/to/vault --instance-name work-wiki

# Note: bare `serve mcp` (no transport) is a deprecated alias for `serve mcp http`.

# CLI subcommands (operate on vault directly)
./wiki-server lint --vault /path/to/vault
./wiki-server directory --vault /path/to/vault
./wiki-server log --vault /path/to/vault
./wiki-server activity --vault /path/to/vault

# macOS LaunchAgent for daily lint (work-laptop path)
./wiki-server --vault /path/to/vault --instance-name work-wiki launchd install
./wiki-server --instance-name work-wiki launchd status
./wiki-server --instance-name work-wiki launchd uninstall
```

For a high-level architecture overview see [docs/OVERVIEW.md](docs/OVERVIEW.md).
For the per-mode feature matrix see [docs/SERVER-MODES.md](docs/SERVER-MODES.md).

## Architecture

```
cmd/wiki-server/main.go    Entry point — delegates to cli package
internal/
  cli/                     Cobra command tree (serve, lint, directory, log, activity)
  server/                  HTTP server setup, static/markdown/raw handlers, middleware chain
  api/                     REST API handler — registers routes on /api/*, delegates to services
  mcpserver/               MCP server (mcp-go) — registers wiki_* tools, streamable-http transport
  service/                 Business logic layer — one service per domain (lint, pages, search, etc.)
  vault/                   Vault filesystem operations — page discovery, frontmatter parsing, wikilinks
  search/                  Search backends: Searcher interface, SubstringSearcher, IndexSearcher (TF-IDF)
  notify/                  Filesystem change debouncer — batches mutations and fires a callback
  middleware/              HTTP middleware: gzip, logging, metrics (Prometheus), cache headers
```

**Key patterns:**
- `vault.Vault` is the core abstraction — wraps a directory path and provides page discovery, frontmatter parsing, slug indexing, wikilink extraction
- `service/` types implement domain logic, consumed by both `api/` (REST) and `mcpserver/` (MCP tools)
- The MCP server and HTTP server can run together (`--mcp-port`) or independently (`serve mcp`)
- Search has a `Searcher` interface with two backends: substring (file walk) and index (inverted index with TF-IDF, auto-rebuilds every 5 minutes)

## Docker Image

Multi-stage build: Go binary built in `golang:1.25-alpine`, then copied into `node:24-alpine` which has Quartz and obsidian-headless. Custom Quartz config/components in `quartz/` are overlaid at build time.

## Helm Chart

Located at `deploy/helm/home-wiki/`. Published to `oci://ghcr.io/jedwards1230/charts/home-wiki`. Chart version is auto-bumped by the release workflow.

## CI/CD

- **CI** (`.github/workflows/ci.yml`): test (race detector + coverage), lint (go vet + golangci-lint + mod tidy check), build verification
- **Release** (`.github/workflows/release.yml`): auto-semver on push to main (PR labels `semver:patch/minor/major`), builds Docker image to GHCR, publishes Helm chart OCI artifact, creates GitHub release

## Environment Variables

| Variable | Default | Purpose |
|----------|---------|---------|
| `WIKI_VAULT_DIR` | `/data/vault` | Path to Obsidian vault |
| `WIKI_PORT` | `8080` | HTTP server port |
| `WIKI_PUBLIC_DIR` | `/data/public` | Quartz static output directory |
| `WIKI_MCP_PORT` | (disabled) | MCP server port (enables when non-zero) |
| `WIKI_INSTANCE_NAME` | (empty) | Human-readable instance identifier surfaced via the `whoami` MCP tool. Honored across all MCP transports (`serve mcp http`, `serve mcp stdio`, and the embedded MCP via `--mcp-port`). When empty, `whoami` omits the field. |
| `WIKI_IN_MEMORY_HTML` | `false` | When truthy, load `WIKI_PUBLIC_DIR` into an atomically-swappable in-memory `fs.FS` and serve from there; fsnotify drives debounced reloads on Quartz rebuilds. Eliminates the mid-rebuild 404 window. Adds the public tree's size to RSS. |
| `WIKI_AUTH_ISSUER` | (disabled) | OIDC issuer URL for JWT auth (e.g. Authentik); enables auth when set. Protects mutating REST API routes and MCP endpoint. |
| `WIKI_AUTH_AUDIENCE` | — | Expected JWT `aud` claim; required when `WIKI_AUTH_ISSUER` is set |
| `WIKI_AUTH_ALLOWED_GROUPS` | — | Comma-separated group names; token's `groups` claim must contain at least one. Required unless `WIKI_AUTH_ALLOW_ANY_USER=true`. |
| `WIKI_AUTH_ALLOW_ANY_USER` | `false` | Explicit opt-in to permit any authenticated user when `WIKI_AUTH_ALLOWED_GROUPS` is empty (fail-closed default). |
| `WIKI_AUTH_RESOURCE_METADATA_URL` | — | RFC 9728 Protected Resource Metadata URL; when set, 401 responses include `WWW-Authenticate` header for MCP OAuth discovery. |

## Vault Conventions

- `raw/` — source documents served natively, excluded from wiki page listing
- `private/` — excluded from sync and page listing
- `.obsidian/` — Obsidian config, excluded from page listing
- Pages have YAML frontmatter with `title`, `tags`, `date` fields
- Wikilinks (`[[target]]`) are parsed for link checking
