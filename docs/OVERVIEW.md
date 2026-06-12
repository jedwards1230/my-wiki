# my-wiki Overview

A single Go binary (`wiki-server`) that turns an Obsidian vault into an agent-accessible knowledge base. The same binary runs as a 24/7 K8s service *and* as an on-demand stdio MCP server, with the same vault conventions either way.

## What it does

- **Serves an Obsidian vault** as a website (native Go renderer → HTML).
- **REST API** for vault operations (`/api/pages`, `/api/search`, ...).
- **MCP server** so AI agents can read/write/lint the vault as a tool surface.
- **Vault-maintenance CLIs** (`lint`, `directory`, `log`, `activity`).

## Two deployment shapes

The binary doesn't know which shape it runs in — it's all flag/subcommand selection. See [SERVER-MODES.md](SERVER-MODES.md) for the per-mode feature matrix.

- **Home (K8s, long-lived):** `serve --mcp-port=8081` — HTTP, REST, embedded MCP, OIDC auth, webhooks, and the TF-IDF index all enabled. Deployed via Helm. wiki-server and obsidian-headless (Obsidian Sync) run as **separate Deployments sharing one NFS (RWX) PVC** — not co-located. Because they can land on different nodes, the fsnotify watcher in wiki-server cannot see the sync container's writes (inotify does not cross NFS clients), so inbox-change dispatch relies on a periodic mtime poll (`WIKI_INBOX_POLL_INTERVAL`, default 60s) in addition to fsnotify.
- **Work (laptop, on-demand):** `serve mcp stdio`, invoked per session by an MCP client (Claude Code) against a local vault. No HTTP or auth — just markdown reads/writes over the same MCP tools. A LaunchAgent runs `lint` daily.

## Design choices

- **In-process renderer.** HTML is rendered by a native Go renderer (`internal/render`) — no external build step. See [RENDERER.md](RENDERER.md).
- **Obsidian as source of truth.** Markdown on disk with YAML frontmatter. The server never owns content — it renders, indexes, and lints. Obsidian's own client edits the vault; `wiki-server` is one of several consumers.
- **MCP is the agent contract.** Every vault op is an MCP tool (`read`, `write`, `edit`, `list`, `search`, `lint`, ...); the REST API is a thin re-skin of the same service layer.
- **Stripping by transport, not config.** Stdio skips watchers, rendering, auth, and webhooks because the per-session lifetime makes them pointless (issue #65 plans a config-driven refactor).
- **Activity logging lives in the vault.** Mutations auto-append to `meta/activity/YYYY-MM-DD.md` and `meta/log.md` — searchable, syncable, no separate database.
- **Schema is markdown.** `meta/schema.md` is canonical, exposed as MCP resource `wiki://schema`. Lint rules are hard-coded (kebab-case tags, required frontmatter, link integrity); schema *content* is per-vault.

## See also

- [SERVER-MODES.md](SERVER-MODES.md) — feature matrix across the MCP-server surfaces
- [../CLAUDE.md](../CLAUDE.md) — agent repo guide (build commands, env vars, conventions)
- [../README.md](../README.md) — install and quickstart
