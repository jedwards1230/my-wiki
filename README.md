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

# HTTP server (browser + REST + embedded MCP)
./wiki-server serve --vault /path/to/vault --port 8080

# MCP server over HTTP (streamable-http)
./wiki-server serve mcp http --vault /path/to/vault --port 8081

# MCP server over stdio (for Claude Code / .mcp.json)
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

```bash
./wiki-server lint       --vault /path/to/vault    # frontmatter + link checks
./wiki-server directory  --vault /path/to/vault    # generate page directory
./wiki-server log        --vault /path/to/vault    # vault activity log
./wiki-server activity   --vault /path/to/vault    # recent mutations
```

A macOS LaunchAgent for daily lint is available via `wiki-server launchd install`.

## Deployment

A Helm chart is published to `oci://ghcr.io/jedwards1230/charts/my-wiki`. Container images are published to `ghcr.io/jedwards1230/my-wiki`. Both are released together by the `release.yml` workflow on push to `main` (semver controlled via `semver:patch|minor|major` PR labels).

## Documentation

- [docs/OVERVIEW.md](docs/OVERVIEW.md) — architecture and design choices
- [docs/SERVER-MODES.md](docs/SERVER-MODES.md) — per-mode feature matrix
- [CLAUDE.md](CLAUDE.md) — repo guide for agents (build commands, conventions, env vars)
- `internal/cli/envvars.go` — canonical `WIKI_*` environment variable inventory
