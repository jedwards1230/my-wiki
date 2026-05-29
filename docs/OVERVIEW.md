# my-wiki Overview

A single Go binary (`wiki-server`) that turns an Obsidian vault into a queryable, agent-accessible knowledge base. Same binary runs as a 24/7 service on a homelab K8s cluster *and* as an on-demand stdio MCP server on a laptop, with the same vault conventions either way.

## What it does

- **Serves an Obsidian vault** as a website (Quartz v4 rendering markdown to static HTML).
- **Exposes a REST API** for vault operations (`/api/pages`, `/api/search`, etc).
- **Speaks MCP** (Model Context Protocol) so AI agents can read/write/lint the vault as a tool surface.
- **Runs vault-maintenance CLIs** (`lint`, `directory`, `log`, `activity`) for one-shot operations.

## Two deployment shapes

**Home (K8s, long-lived).** A single pod runs `wiki-server serve --mcp-port=8081` alongside `obsidian-headless` (Obsidian Sync) and Quartz. HTTP server handles browser traffic, REST API handles UI, embedded MCP handles agent calls. OIDC auth, webhook dispatch, and TF-IDF search index are all enabled. Backed by Longhorn PVC; deployed via Helm.

**Work (macOS laptop, on-demand).** No homelab connection. `wiki-server serve mcp stdio` is invoked by an MCP client (Claude Code) per session and exits when the session ends. Points at a local Obsidian vault directory. No HTTP, no Quartz, no auth — just markdown reads/writes through the same MCP tool surface. A LaunchAgent installed via `wiki-server launchd install` runs `lint` daily.

The binary doesn't know which shape it's running in — that's all flag/subcommand selection. See [SERVER-MODES.md](SERVER-MODES.md) for the per-mode feature matrix.

## Design choices

- **Pluggable renderer.** `WIKI_RENDERER` (Helm value `renderer:`) selects between Quartz v4 (Node) and a native Go renderer (`internal/render`). Quartz is the default; the native renderer is opt-in per deployment and trivially reversible. See [RENDERER.md](RENDERER.md).
- **Obsidian as source of truth.** Markdown files on disk with YAML frontmatter (`title`, `tags`, `date`). The server never owns content — it just renders, indexes, and lints it. This is what makes the home/work split work: Obsidian's own client edits the vault directly, and `wiki-server` is one of several consumers.
- **MCP is the agent contract.** Every vault operation is exposed as an MCP tool (`read`, `write`, `edit`, `list`, `search`, `lint`, ...). The REST API is a thin re-skinning of the same service layer for browser use. Agents and humans get parity.
- **Stripping by transport, not by config.** Stdio mode skips watchers, Quartz, auth, and webhooks because the per-session lifetime makes them pointless — not because they're configurable off. (See issue #65 for a planned refactor that makes the stripping config-driven, so stdio can grow features without copy-paste.)
- **Activity logging is part of the vault.** Every mutation auto-appends to `meta/activity/YYYY-MM-DD.md` and `meta/log.md`. The audit trail lives where the rest of the content does — searchable, syncable, no separate database.
- **Schema is markdown, not code.** `meta/schema.md` is the canonical schema, exposed as an MCP resource (`wiki://schema`). Lint rules are hard-coded (kebab-case tags, required frontmatter, link integrity), but the schema *content* is authored per-vault.

## Where to go next

- [SERVER-MODES.md](SERVER-MODES.md) — feature matrix across the four MCP-server surfaces.
- [../CLAUDE.md](../CLAUDE.md) — agent-facing repo guide (build commands, env vars, conventions).
- [../README.md](../README.md) — install and quickstart.
