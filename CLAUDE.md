# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

My Wiki is a Go server (`wiki-server`) that serves an Obsidian vault as a website. It combines:
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

# Note: bare `serve mcp` (no transport) is deprecated. Cobra prints a deprecation
# message and falls through to help — it does NOT start a server. Always specify
# `serve mcp http` or `serve mcp stdio` explicitly.

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
  mcpserver/               MCP server (mcp-go) — registers bare-name tools (read, write, edit, list, search, delete, move, lint, tags, whoami, activity), streamable-http transport
  service/                 Business logic layer — one service per domain (lint, pages, search, etc.)
  vault/                   Vault filesystem operations — page discovery, frontmatter parsing, wikilinks
  search/                  Search backends: Searcher interface, SubstringSearcher, IndexSearcher (TF-IDF)
  notify/                  Filesystem change debouncer — batches mutations and fires a callback
  middleware/              HTTP middleware: gzip, logging, metrics (Prometheus), cache headers
  dispatch/                Webhook dispatch pipeline — config loader, debouncer, EventRouter, HTTP dispatcher, sinks
  memfs/                   Atomically-swappable in-memory `fs.FS` for `WIKI_IN_MEMORY_HTML`
  version/                 Build-time version string injected via -ldflags
```

**Key patterns:**
- `vault.Vault` is the core abstraction — wraps a directory path and provides page discovery, frontmatter parsing, slug indexing, wikilink extraction
- `service/` types implement domain logic, consumed by both `api/` (REST) and `mcpserver/` (MCP tools)
- The MCP server and HTTP server can run together (`--mcp-port`) or independently (`serve mcp`)
- Search has a `Searcher` interface with two backends: substring (file walk) and index (inverted index with TF-IDF, auto-rebuilds every 5 minutes)

## Docker Image

Multi-stage build: Go binary built in `golang:1.25-alpine`, then copied into `node:24-alpine` which has Quartz and obsidian-headless. Custom Quartz config/components in `quartz/` are overlaid at build time.

## Helm Chart

Located at `deploy/helm/my-wiki/`. Published to `oci://ghcr.io/jedwards1230/charts/my-wiki`. Chart version is auto-bumped by the release workflow.

## CI/CD

- **CI** (`.github/workflows/ci.yml`): test (race detector + coverage), lint (go vet + golangci-lint + mod tidy check), build verification
- **Release** (`.github/workflows/release.yml`): auto-semver on push to main (PR labels `semver:patch/minor/major`), builds Docker image to GHCR, publishes Helm chart OCI artifact, creates GitHub release

## Environment Variables

All `WIKI_*` environment variables are defined as constants in [`internal/cli/envvars.go`](internal/cli/envvars.go) — that's the canonical inventory. Each constant's godoc describes the variable, its default, and its effect on behavior. Don't duplicate them here; update the godoc in `envvars.go` and the change propagates everywhere via `go doc`.

## Vault Conventions

- `raw/` — source documents served natively, excluded from the page directory and the page-listing API. Files inside ARE valid wikilink targets (slug-indexed).
- `private/` — excluded from sync and page listing
- `.obsidian/` — Obsidian config, excluded from page listing
- Pages have YAML frontmatter with `title`, `tags`, `date` fields
- Wikilinks (`[[target]]`) are parsed for link checking
- `meta/lint-config.yaml` (optional) — overrides schema-coupled lint values (clipping tag name, raw-path prefix, stub idle cooldown). Missing or partial → defaults from `service.DefaultLintConfig()`. Malformed → surfaced as an ERROR under both the `clippings` and `stub` checks (they share the same loaded config).
