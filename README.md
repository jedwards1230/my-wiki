# my-wiki

A single Go binary (`wiki-server`) that serves an Obsidian vault as a website, a REST API, and an MCP server for AI agents. Quartz v4 renders markdown to HTML; the Go server handles static/REST delivery and MCP. The same binary runs as a long-lived K8s deployment or an on-demand stdio MCP server.

## Quickstart

```bash
# Docker Compose — serves the rendered vault at http://localhost:8080
WIKI_VAULT=/path/to/obsidian-vault docker compose up --build
```

```bash
# Build from source
go build -o wiki-server ./cmd/wiki-server

# HTTP server (browser + REST). --public-dir needs a built Quartz output
# (run `npx quartz build` first, or use Docker Compose which builds it).
# Add `--mcp-port 8081` to also start MCP in-process.
./wiki-server serve --vault /path/to/vault --public-dir /path/to/quartz/public --port 8080

# MCP over HTTP (streamable-http)
./wiki-server serve mcp http --vault /path/to/vault --port 8081

# MCP over stdio (Claude Code / .mcp.json) — no HTTP, Quartz, or auth
./wiki-server serve mcp stdio --vault /path/to/vault
```

## Content paths

| Path        | Serves                                               |
| ----------- | ---------------------------------------------------- |
| `/path`     | Quartz-rendered HTML                                 |
| `/path.md`  | Plain markdown from the vault                        |
| `/raw/path` | Native source files (PDFs, images) with dir listings |
| `/api/*`    | REST API (pages, search, lint, activity, ...)        |

## CLI

One-shot vault-maintenance commands sharing the `--vault` flag:

```bash
./wiki-server lint      [all|frontmatter|tags|links|orphans|size|clippings|stub|log]
./wiki-server directory                              # list all pages with metadata
./wiki-server log       [today|YYYY-MM-DD|lint]      # view / lint the activity log
./wiki-server activity  <type> <title> [--summary X] # append a structured log entry
```

A macOS LaunchAgent for daily lint (`--instance-name` is appended to the plist label so multiple vaults coexist):

```bash
./wiki-server --vault /path/to/vault --instance-name work-wiki launchd install
./wiki-server --instance-name work-wiki launchd {status|uninstall}
```

## Deployment

Helm chart: `oci://ghcr.io/jedwards1230/charts/my-wiki`. Images: `ghcr.io/jedwards1230/my-wiki`. Both released by `release.yml` on push to `main` (semver via `semver:patch|minor|major` PR labels).

## Documentation

- [docs/OVERVIEW.md](docs/OVERVIEW.md) — architecture and design choices
- [docs/SERVER-MODES.md](docs/SERVER-MODES.md) — per-mode feature matrix
- [docs/RENDERER.md](docs/RENDERER.md) — `WIKI_RENDERER=quartz|native` operator runbook
- [CLAUDE.md](CLAUDE.md) — agent repo guide (build commands, conventions, env vars)
- `internal/cli/envvars.go` — canonical `WIKI_*` environment variable inventory
