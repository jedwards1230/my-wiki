# my-wiki

A single Go binary (`wiki-server`) that serves an Obsidian vault as a website, a REST API, and an MCP server for AI agents. A native Go renderer turns markdown into HTML; the same binary handles static/REST delivery and MCP. It runs as a long-lived K8s deployment or an on-demand stdio MCP server.

## Quickstart

```bash
# Docker Compose — serves the rendered vault at http://localhost:8080
WIKI_VAULT=/path/to/obsidian-vault docker compose up --build
```

```bash
# Build from source
go build -o wiki-server ./cmd/wiki-server

# HTTP server (browser + REST). Add `--mcp-port 8081` to also start MCP in-process.
./wiki-server serve --vault /path/to/vault --port 8080

# MCP over HTTP (streamable-http)
./wiki-server serve mcp http --vault /path/to/vault --port 8081

# MCP over stdio (Claude Code / .mcp.json) — no HTTP or auth
./wiki-server serve mcp stdio --vault /path/to/vault
```

## Content paths

| Path        | Serves                                               |
| ----------- | ---------------------------------------------------- |
| `/path`     | Rendered HTML                                        |
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
./wiki-server --instance-name work-wiki launchd status
./wiki-server --instance-name work-wiki launchd uninstall
```

## Deployment

Helm chart: `oci://ghcr.io/jedwards1230/charts/my-wiki`. Images: `ghcr.io/jedwards1230/my-wiki`. Both released by `release.yml` on push to `main` (semver via `semver:patch|minor|major` PR labels).

Key Helm values (see `deploy/helm/my-wiki/values.yaml` for the full list): `instanceName` sets `WIKI_INSTANCE_NAME` and is surfaced by the `whoami` MCP tool so agents can distinguish multiple wiki instances. `obsidianSync.standalone: true` moves obsidian-headless to a separate Deployment (requires an RWX PVC).

## Documentation

- [docs/openapi.yaml](docs/openapi.yaml) — OpenAPI 3.1 spec for the REST API, kept in sync with the registered routes by `TestOpenAPISync` (`internal/api`); running instances also serve it at `GET /api/openapi.yaml`
- [docs/OVERVIEW.md](docs/OVERVIEW.md) — architecture and design choices
- [docs/SERVER-MODES.md](docs/SERVER-MODES.md) — per-mode feature matrix
- [docs/RENDERER.md](docs/RENDERER.md) — native renderer pipeline and assets
- [CONTRIBUTING.md](CONTRIBUTING.md) — contributor guide (prerequisites, build/test/lint, PR flow)
- `internal/cli/envvars.go` — canonical `WIKI_*` environment variable inventory
