# my-wiki

A single Go binary (`wiki-server`) that turns an Obsidian vault into a website, a REST API, and an MCP server for AI agents.

Quartz v4 renders markdown to HTML, the Go server handles static delivery and REST, and the MCP server exposes vault operations as tools. The same binary runs as a long-lived K8s deployment or an on-demand stdio MCP server on a laptop.

## Quickstart

### Docker Compose

```bash
WIKI_VAULT=/path/to/obsidian-vault docker compose up --build
```

Serves the rendered vault at <http://localhost:8080>.

### Build from source

```bash
go build -o wiki-server ./cmd/wiki-server

# HTTP server (browser + REST + embedded MCP).
# --public-dir must point at a built Quartz output (run `npx quartz build` first,
# or use the Docker Compose flow which builds it for you).
./wiki-server serve \
  --vault /path/to/vault \
  --public-dir /path/to/quartz/public \
  --port 8080

# MCP server over HTTP (streamable-http)
./wiki-server serve mcp http --vault /path/to/vault --port 8081

# MCP server over stdio (for Claude Code / .mcp.json) — no HTTP, no Quartz, no auth
./wiki-server serve mcp stdio --vault /path/to/vault
```

## Content paths

| Path        | Serves                                                |
| ----------- | ----------------------------------------------------- |
| `/path`     | Quartz-rendered HTML                                  |
| `/path.md`  | Plain markdown from the vault                         |
| `/raw/path` | Native source files (PDFs, images) with dir listings  |
| `/api/*`    | REST API (pages, search, lint, activity, ...)         |

## CLI

One-shot vault-maintenance commands that share the same `--vault` flag as `serve`:

```bash
./wiki-server lint      [all|frontmatter|tags|links|orphans|size|clippings|stub|log]
./wiki-server directory                              # list all pages with metadata
./wiki-server log       [today|YYYY-MM-DD|lint]      # view / lint the activity log
./wiki-server activity  <type> <title> [--summary X] # append a structured log entry
```

A macOS LaunchAgent for daily lint is available via `wiki-server launchd install`.

## Deployment

A Helm chart is published to `oci://ghcr.io/jedwards1230/charts/my-wiki`. Container images are published to `ghcr.io/jedwards1230/my-wiki`. Both are released together by the `release.yml` workflow on push to `main` (semver controlled via `semver:patch|minor|major` PR labels).

## Documentation

- [docs/OVERVIEW.md](docs/OVERVIEW.md) — architecture and design choices
- [docs/SERVER-MODES.md](docs/SERVER-MODES.md) — per-mode feature matrix
- [CLAUDE.md](CLAUDE.md) — repo guide for agents (build commands, conventions, env vars)
- `internal/cli/envvars.go` — canonical `WIKI_*` environment variable inventory
