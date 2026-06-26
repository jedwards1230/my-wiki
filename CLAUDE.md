# CLAUDE.md

@CONTRIBUTING.md

Guidance for Claude Code when working in this repository.

## Project Overview

`wiki-server` is a Go server that serves an Obsidian vault as a website. It combines:
- **Native Go renderer** (`internal/render`, goldmark) for rendered HTML
- **Go HTTP server** for static serving, markdown delivery, REST API, and MCP
- **obsidian-headless** for Obsidian Sync

Three content paths: `/path` (rendered HTML), `/path.md` (vault markdown), `/raw/path` (native source files with directory listing).

## Commands

```bash
go test -v ./internal/search/                   # single package

# Run (needs vault dir)
./wiki-server serve --vault /path/to/vault --port 8080
./wiki-server serve mcp http --vault /path/to/vault --port 8081
./wiki-server serve mcp stdio --vault /path/to/vault --instance-name work-wiki
```

`serve mcp stdio` logs to stderr (stdout is reserved for JSON-RPC) and skips HTTP, OIDC, webhooks, and the TF-IDF index. Bare `serve mcp` (no transport) is deprecated — it prints a deprecation message and falls through to help, it does NOT start a server.

CLI subcommands (`lint`, `directory`, `log`, `activity`) and the macOS LaunchAgent are documented in [README.md](README.md).

See [docs/OVERVIEW.md](docs/OVERVIEW.md) (architecture), [docs/SERVER-MODES.md](docs/SERVER-MODES.md) (feature matrix), [docs/RENDERER.md](docs/RENDERER.md) (native renderer pipeline).

## Architecture

```
cmd/wiki-server/main.go    Entry point — delegates to cli package
internal/
  cli/         Cobra command tree (serve, lint, directory, log, activity)
  server/      HTTP setup, static/markdown/raw handlers, middleware chain
  api/         REST handler on /api/* — delegates to services
  mcpserver/   MCP server (mcp-go) — bare-name tools (read, write, edit, list, search, delete, move, lint, tags, whoami, activity), streamable-http
  service/     Business logic — one service per domain (lint, pages, search, ...)
  render/      Native Go renderer — goldmark + Obsidian extensions → site tree (HTML, listings, sitemap.xml, RSS, 404) into a memfs.Snapshot
  vault/       Vault filesystem ops — page discovery, frontmatter, wikilinks
  slug/        Deterministic, filesystem-safe slug derivation (server owns on-disk filenames)
  search/      Searcher interface; SubstringSearcher + IndexSearcher (TF-IDF)
  notify/      Filesystem change debouncer
  middleware/  gzip, logging, Prometheus metrics, cache headers
  dispatch/    Webhook pipeline — config loader, debouncer, EventRouter, dispatcher, sinks
  memfs/       Atomically-swappable in-memory fs.FS for native renderer snapshots
  version/     Build-time version string (-ldflags)
```

**Key patterns:**
- `vault.Vault` is the core abstraction — page discovery, frontmatter parsing, slug indexing, wikilink extraction
- `service/` types are consumed by both `api/` (REST) and `mcpserver/` (MCP) — agents and humans get parity
- MCP and HTTP servers run together (`--mcp-port`) or independently (`serve mcp`)
- Search index (TF-IDF) auto-rebuilds every 5 minutes; substring is the fallback backend

## Visual Verification

For CSS/template/rendered-output changes, verify visually before a PR (skip for backend-only changes):

```bash
go build -o wiki-server ./cmd/wiki-server
# Seed a vault that exercises every renderer feature (callouts, code, math,
# diagrams, wikilinks/backlinks, transclusion, raw/, …).
# Defaults to /tmp/wiki-test-vault; pass a path to override.
scripts/seed-vault.sh /tmp/wiki-test-vault
# WIKI_AUTH_DISABLED=true is required: the HTTP server fails closed and refuses
# to start without either an OIDC issuer or an explicit opt-out (see Auth, below).
WIKI_AUTH_DISABLED=true ./wiki-server serve --vault /tmp/wiki-test-vault --port 9876
# Use Playwright (MCP): navigate to http://localhost:9876/ and the feature pages
# (e.g. /rendering/callouts/, /transclusion/a/), screenshot light + dark.
# Save screenshots under .playwright-mcp/ (gitignored) so they don't get committed.
# Dark mode: document.documentElement.setAttribute('data-theme', 'dark')
```

`scripts/seed-vault.sh` is the canonical fixture for visual checks — each page
isolates a feature, so screenshots cover the whole render surface. Extend it
when you add a rendering feature.

The Playwright MCP server is declared in `.mcp.json` (`--browser firefox`). On
Claude Code on the web, `.claude/hooks/session-start.sh` installs the firefox
browser binary so `browser_navigate` works without manual setup; locally, run
`npx playwright install firefox` once.

## Native Renderer Frontend

The renderer uses goldmark + a single-page app with htmx + Alpine.js:

- **CSS**: `internal/server/assets/wiki.css` — all styles incl. dark mode (`html[data-theme="dark"]`, `prefers-color-scheme`)
- **JS**: `internal/server/assets/wiki.js` — theme toggle, search, code-copy, mermaid lazy-load
- **Templates**: `internal/render/templates/` — Go html/template (`base`, `list`, `404`)
- **Vendor**: `internal/server/assets/vendor/` — htmx, Alpine.js, KaTeX, Mermaid (versions pinned in `MANIFEST.txt`)
- **Fonts**: `internal/server/assets/fonts/` — self-hosted woff2
- **Syntax**: goldmark-highlighting + Chroma with CSS classes (light "github", dark "github-dark")

Assets are embedded via `//go:embed` in `internal/server/assets/assets.go`, served under `/_/static/`.

## Build & Release

- **Docker**: multi-stage — Go binary built in `golang:1.25.6-alpine`, copied into `node:24-alpine` (obsidian-headless).
- **Helm**: `deploy/helm/my-wiki/`, published to `oci://ghcr.io/jedwards1230/charts/my-wiki`. Chart version auto-bumped by the release workflow.
- **CI** (`.github/workflows/ci.yml`): test (race + coverage), lint (go vet + golangci-lint + mod tidy), build.

## Environment Variables

All `WIKI_*` vars are constants in [`internal/cli/envvars.go`](internal/cli/envvars.go) — the canonical inventory, with godoc describing each default and effect. Don't duplicate here; update the godoc and it propagates via `go doc`.

Auth fails closed: network entry points (REST API / MCP HTTP) refuse to start unless either `WIKI_AUTH_ISSUER` (OIDC, e.g. Authentik) is set or `WIKI_AUTH_DISABLED=true` explicitly acknowledges running with no auth. `serve mcp stdio` is local-only and always runs without auth.

## Vault Conventions

- `raw/` — a fully normal folder. It is watched, indexed, and searchable like any other directory: `Generate` writes a standard `index.md` landing into each `raw/` directory (baked into the snapshot and served at `/raw/<dir>/`, exactly like `/research/`). Its **markdown** is promoted to first-class compiled wiki pages (in Recently Updated / RSS / sitemap / backlink graph / nav, rendered as full pages at their `/raw/...` URLs); the verbatim source stays available via `/path.md` and `?raw=1`. Non-markdown **assets** (PDFs, images, audio, video, `.canvas`) are still served as-is by the `/raw/` handler. A browser hitting a `/raw/<dir>/` URL gets that directory's generated index landing from the snapshot; an asset-only directory with no meaningful baked index falls back to a media gallery so assets stay visible. Agents and `?raw=1` still get the plain autoindex. All raw paths are slug-indexed wikilink targets.
- `.obsidian/` — Obsidian config, excluded from page listing (and denied on the API/HTTP/MCP page surface)
- Pages have YAML frontmatter (`title`, `tags`, `date`); wikilinks (`[[target]]`) are parsed for link checking
- `meta/lint-config.yaml` (optional) — overrides schema-coupled lint values (clipping tag, raw-path prefix, stub cooldown). Missing/partial → `service.DefaultLintConfig()` defaults. Malformed → ERROR under both `clippings` and `stub` checks (shared config).
