# my-wiki — PRD

> **Status:** draft v1 · 2026-08-22 · owner: justin
> **Repo:** [`jedwards1230/my-wiki`](https://github.com/jedwards1230/my-wiki) · **Live:** <https://wiki.lilbro.cloud> · **Shipped:** `v0.12.3` (2026-08-04)
> **Scope:** the engine. Vault *content* — page conventions, the taxonomy, the migration backlog — is governed by [`meta/schema.md`](https://wiki.lilbro.cloud/meta/schema) on the wiki itself and is explicitly out of scope here.

## 1. What it is

`wiki-server` is a single Go binary that turns a directory of Obsidian-flavored markdown into three
coordinated interfaces: an MCP server for AI agents, a REST API, and a rendered HTML site. It owns
no content. The vault is the source of truth on disk; the server renders, indexes, lints, and
serves it, and every write it accepts lands back as markdown in that same directory.

The distinction is load-bearing. **This repo is the engine.** The homelab instance's vault — 477
pages at wiki.lilbro.cloud — is a separate Obsidian vault replicated in by Obsidian Sync. Pointing
`--vault` at any other Obsidian vault must work with zero migration and zero setup, because the
server keeps no state a vault couldn't reconstruct.

Its primary consumer is not a person. Agents read, write, search, and lint through MCP tools; the
web UI exists so a human can see what the agents did.

## 2. Problem

Plain Obsidian is single-player and offline: an agent can't reach the vault, and neither can a
family member on a phone browser. A static site generator (the original Quartz deployment, ripped
out in `jedwards1230/my-wiki#73`) renders for humans but offers agents nothing writable and
requires an external build step per change. A hosted wiki (Notion, Confluence, Outline) inverts the
ownership: content becomes rows in someone's database, unreadable by the editor you actually use,
and un-greppable by an agent with filesystem tools.

What was missing is a server where **markdown on disk stays the source of truth** while an agent
gets a first-class mutation API over it — one that understands Obsidian's dialect (wikilinks,
transclusion, callouts, block refs), keeps a link graph and a search index current on write, and
lints its own conventions so a fleet of agents writing concurrently doesn't degrade the vault.

The deeper bet, stated on the service's own wiki page: rather than retrieving raw documents per
question (the RAG pattern), agents *incrementally build and maintain* a structured wiki —
synthesizing sources into interlinked pages and compounding knowledge over time. That only works if
writing is as cheap and as safe as reading.

## 3. Users & core workflows

### AI agents (primary)

Reached over MCP — federated into ContextForge as the `wiki` upstream (hence the `wiki-*` tool names
agents see; the server registers them under bare names, see §7) — or over REST, or by fetching
`/path.md`.

| Journey | Path |
|---|---|
| **Orient** | `read` `wiki://schema` resource (or `meta/schema.md`) → `list` / read `index.md` → `tags` to discover the taxonomy before inventing one |
| **Answer from the wiki** | `search` (TF-IDF, ranked, snippets) → `read` the hits → cite by URL |
| **Capture knowledge** | `search` first to avoid duplicates → `write` with structured frontmatter → server auto-logs the mutation to `meta/activity/YYYY-MM-DD.md` → lint warnings return in the same response |
| **Amend** | `edit` with ordered find/replace ops — all-or-nothing, no full-content rewrite, no clobbering a concurrent edit elsewhere in the file |
| **Reorganize** | `move` (fails if destination exists; returns the wikilinks the move broke) / `delete` (same) |
| **Self-maintain** | `lint` → fix → `activity` to log a narrative summary of a multi-page session |
| **React to a drop** | a file lands in `inbox/` → debounced `inbox.changed` webhook carries a prompt URL → an agent wakes, classifies, and deletes the entry |

The design rule behind the tool surface: **it mirrors Claude Code's filesystem model** (`read`/`write`/
`edit`/`list`/`search`/`delete`/`move` ≈ Read/Write/Edit/Glob/Grep/rm/mv), so an agent already fluent
with files needs no new mental model.

### Humans (first-class, second priority)

Browse `wiki.lilbro.cloud` — an htmx + Alpine site with an explorer sidebar, backlinks rail, link
popovers, a canvas graph view, tag pages, full-text search, dark mode, KaTeX, Mermaid, and syntax
highlighting. Curate sources and direct analysis; the agents do the writing. Edit natively in
Obsidian on any device — those edits sync in and appear within seconds. Read `/path.md` when you
want the bytes.

The stated tie-break, from `CLAUDE.md`: *"when interfaces compete for design effort, the agent
surface wins."*

## 4. Goals / non-goals

**Goals**

1. **Markdown on disk is the only durable state.** No database, ever. Every derived structure —
   render snapshot, search index, slug index, backlink graph — is recomputed from files and held in
   memory. Generated pages (`index.md`, activity logs) persist *as markdown*.
2. **Agent surface and human surface stay at parity through one service layer.** `internal/api` and
   `internal/mcpserver` both delegate to `internal/service`; a capability added to one lands in the
   other.
3. **Writes are safe for a fleet.** Concurrent agents, an Obsidian sync process, and a human editor
   share one directory without clobbering each other.
4. **The vault's conventions are machine-checkable.** Lint catches structural drift; the write path
   returns warnings inline so an agent self-corrects before the next call.
5. **Operational trust that the vault is current.** Stale content that *looks* fine is the failure
   mode that actually happened (twice); freshness is instrumented as a dead-man's switch.
6. **Self-describing contract.** A deployed instance serves its own OpenAPI spec and its own agent
   operating manual.

**Non-goals**

- Being a general CMS, an authoring UI, or a WYSIWYG editor. Obsidian is the editor.
- Owning content. The server never authors a page except generated indexes and activity logs.
- A database, a job queue, or any external runtime dependency beyond the sync backend.
- Built-in password auth as a default path (OIDC only; local auth acceptable only as a clearly
  separated add-on for public deployments).
- Multi-tenancy today — see "Spaces" in §12.

## 5. Locked product decisions

| Decision | Choice | Why |
|---|---|---|
| Source of truth | Markdown files on a filesystem | Portable, greppable, editable in Obsidian, survives the server. No migration to adopt, none to leave. |
| Persistence | **No database, ever** | Any state that "needs" a datastore must persist as markdown or be recomputable. A feature that can't is the wrong feature. |
| Rendered output | In-memory `memfs` snapshot, atomically swapped | Never written to disk. No build step, no partial-build 404 window. |
| Renderer | In-process goldmark, not Quartz | One binary replaces nginx + an external SSG and shares logic with the CLI/API/MCP (`#73`). |
| Primary agent contract | **MCP**; REST is a thin re-skin of the same service layer | One place to add a capability. |
| MCP tool names | Bare — `read`, `write`, … | Deliberately mirrors Claude Code's filesystem verbs. Namespacing is the federation gateway's job. |
| MCP transport | Streamable HTTP (**stateless**) + stdio | Protocol revision `2026-07-28` requires `Stateless: true` — without it the SDK silently withholds the revision and clients downgrade. |
| `/path` vs `/path.md` vs `/raw/path` | Three deliberate representations | `/path` = rendered HTML for humans; `/path.md` = verbatim vault bytes, the token-efficient **primary agent read path**; `/raw/path` = native sources (PDFs, images, `.canvas`) with directory browsing. |
| Raw markdown | Compiled as first-class pages | `raw/*.md` gets rendered, searched, indexed, and graphed with a "Source" badge; verbatim bytes remain at `/path.md` or `?raw=1`. Non-markdown assets stay a pure source tier. |
| Search model | Two pluggable engines — in-memory TF-IDF `index` (default) + `substring` fallback; `all` runs both | Index is fast and never touches disk at query time; substring always works and is the fallback if an index build fails. |
| Sync | Obsidian Sync via `obsidian-headless`, not git | E2E encryption means family content is private *even from the server*; offline-first mobile; no merge conflicts. Cost: a subscription and a Node sidecar. |
| Auth | OIDC bearer JWT only, **fails closed** | The network entry points refuse to start without an issuer unless `WIKI_AUTH_DISABLED` is explicitly set. No API keys, no password store. |
| Vault schema | Lives *in* the vault (`meta/schema.md`), exposed as MCP resource `wiki://schema` | The operating manual is content, co-evolved by humans and agents; only its mechanically-checkable subset is compiled into lint. |
| `.obsidian/` | Excluded and denied on every surface | Editor state is not content. Denial returns 404, not 403, so the API doesn't confirm existence. |
| `private/` | Excluded from server sync entirely | Personal-device-only via Obsidian Sync. Everything else is LAN-visible and agent-visible by design. |

## 6. Product surface

### 6.1 MCP tools

Registered under **bare names** in `internal/mcpserver/server.go`. The `wiki-*` names agents see in
the homelab come from ContextForge's federation prefix, not from this server. Every failure is an
`IsError` text result, not a JSON-RPC error. `search` is registered only when a search service is wired.

| Tool | Intent | Parameters | Semantics |
|---|---|---|---|
| `read` | Fetch a page verbatim | `path`* | `.md` appended if omitted. Returns raw file text incl. frontmatter. |
| `write` | Create or overwrite | `path`*, `title`*, `tags`*, `content`*, `date`, `description`, `extra_frontmatter` | Frontmatter is **assembled from structured params** — agents must not embed YAML in `content`. Filename slugified server-side. No existence check: last write wins. Returns lint warnings for the page. |
| `edit` | Targeted find/replace | `path`*, `operations`* `[{find*, replace*}]` | All-or-nothing: a missing `find` aborts before any write. Replaces first occurrence per op. Returns the full new content + lint warnings. |
| `list` | Enumerate pages | `prefix`, `detail`, `sort_by` (`name`\|`modified`), `limit` | `detail:false` → `{path,title,has_meta}`; `detail:true` → `{path,title,description,tags}`. `sort_by=modified` + `limit` is the "recently changed" idiom. |
| `search` | Ranked full-text | `query`* (≥2 chars), `limit` (20), `engine` (`index`\|`substring`\|`all`) | Matches title, tags, content. Returns score, snippet, match class, and per-engine timings. |
| `delete` | Remove a page | `path`* | Errors if absent. Returns the wikilinks the deletion broke. |
| `move` | Rename/relocate | `source`*, `destination`* | Fails if source missing or destination exists. Destination slugified. Returns newly-broken wikilinks. |
| `lint` | Check the vault | `check` (`all`\|`frontmatter`\|`tags`\|`links`\|`orphans`\|`size`\|`log`) | Structured `LintReport`. See §6.5 — the enum here is narrower than the service supports. |
| `tags` | Tag census | — | Every tag with page count, sorted by frequency. The "check before inventing a tag" call. |
| `whoami` | Instance identity | — | `{name, version, vault_dir, go_version, instance_name?, user?}` — lets an agent tell which wiki it is connected to. |
| `activity` | Narrative log entry | `type`* (`edit`\|`create`\|`delete`\|`lint`\|`note`\|`migrate`\|`move`), `title`*, `time`, `summary`, `day_summary`, `touched` | **For session summaries only** — individual page mutations are auto-logged by the server; duplicating them here is a defect. |

*Annotations:* `read`/`list`/`search`/`lint`/`tags`/`whoami` are read-only + idempotent; `write`/`edit`/
`delete`/`move` are marked destructive. **There is no dry-run parameter on any tool.**

**Resources:** exactly one — `wiki://schema` (`text/markdown`), the agent operating manual, backed by
`meta/schema.md`. No prompts, no resource templates. `ServerOptions.Capabilities` is left **nil** on
purpose so the SDK auto-derives a truthy `resources` capability; an explicit empty struct serializes
as `{}`, which is falsy in Python and silently kills ContextForge resource federation.

**Protocol:** revision `2026-07-28` (`modelcontextprotocol/go-sdk v1.7.0`), negotiating down to
`2025-11-25` / `2025-06-18` / `2025-03-26` / `2024-11-05`. SEP-2549 cache hints stamped on listings
(`ttlMs 300000`) and `resources/read` (`ttlMs 60000`). MCP logging removed per SEP-2577; audit events
go to `slog` enriched with the caller's OIDC subject.

### 6.2 HTTP API

17 operations under `/api`, envelope `{"data":…}` / `{"error":…}`, plus a `warnings` array of lint
issues on mutations. `docs/openapi.yaml` is the single source of truth and is held in **exact** sync
with the registered route table by `TestOpenAPISync`; the spec is served verbatim from the running
instance.

| Method + path | Notes |
|---|---|
| `GET /api/pages/{path...}` | `{path, content}`; 404 on miss or denied prefix |
| `GET /api/pages` | `?prefix`, `?sort_by`, `?limit` |
| `PUT /api/pages/{path...}` | Body = raw markdown, **10 MB cap** (413). 400 on frontmatter validation failure |
| `PATCH /api/pages/{path...}` | `{operations:[{find,replace}]}`; **422** when a `find` misses |
| `DELETE /api/pages/{path...}` | |
| `GET /api/search` | `q` (≥2), `limit` (20), `engine`; **501** if search unconfigured |
| `GET /api/lint` | `?check=` — accepts `clippings` and `stub` in addition to the MCP enum |
| `GET /api/tags`, `GET /api/directory`, `GET /api/recent` | `recent` = `pages?sort_by=modified&limit=20` |
| `GET /api/graph.json` | `{nodes:[{id,title,url}], links:[{source,target}]}` for the graph widget |
| `GET /api/whoami` | Mirrors the MCP tool, incl. caller claims when authenticated |
| `POST /api/directory/generate` | Regenerates `index.md` pages (also automatic post-mutation) |
| `POST /api/activity` | Narrative entry; 201 |
| `GET /api/openapi.yaml` | The spec itself, `application/yaml`, not enveloped |
| `GET /api/popover/{slug...}`, `GET /api/backlinks?slug=` | **HTML fragments**, htmx-only, native-renderer mode only |

### 6.3 URL scheme

| Prefix | Serves |
|---|---|
| `/{path}` | Rendered HTML (try_files: exact → `.html` → `/index.html` → `404.html`). `HX-Request: true` gets a content fragment with `Vary: HX-Request`. |
| `/{path}.md` | Verbatim vault bytes, `text/plain`, `nosniff`. The primary agent read path. |
| `/raw/{path}` | Markdown → compiled page in a browser, verbatim on `?raw=1` or a non-HTML `Accept`. Assets native MIME. Directories → generated index, media gallery, or plain autoindex for agents. |
| `/api/*` | REST (§6.2) |
| `/_/static/*` | Embedded CSS/JS/fonts/vendor bundle, pinned by sha256 in `MANIFEST.txt` |
| `/healthz`, `/readyz`, `/metrics` | Liveness; readiness (503 until first render completes); Prometheus. All bypass the readiness gate. |
| `/mcp` | **Separate port** (`--mcp-port`, default 8081), never on the main mux. Stateless: `POST` only; `GET`/`DELETE` return 405. |

### 6.4 Server modes

| Mode | HTTP+REST | MCP | Renderer | Search | Watcher | Webhooks | Auth | Metrics |
|---|---|---|---|---|---|---|---|---|
| `serve` | yes | — | yes | index + substring | yes | yes | yes | yes |
| `serve --mcp-port=N` **(prod)** | yes | streamable HTTP | yes | index + substring | yes | yes | yes | yes |
| `serve mcp http` | — | streamable HTTP | — | substring only | yes | yes | yes | — |
| `serve mcp stdio` **(laptop)** | — | stdio | — | substring only | — | — | never | — |

Behavior is stripped **by transport, not by config flags**. `serve mcp` with no transport is a
deprecated no-op alias. One-shot CLIs share `--vault`: `lint [check]`, `directory [--generate]`,
`log [today|YYYY-MM-DD|lint]`, `activity <type> <title>`, and `launchd install|status|uninstall`
(macOS LaunchAgent for a daily lint, label suffixed with `--instance-name` so vaults coexist).

### 6.5 Lint

Nine checks. `errors` counts everything except `INFO`; a non-zero count fails the CLI. Scoped
variants (`LintPage`, `LintDelete`) run on the write path so mutations return warnings inline.

| Check | Catches | Level |
|---|---|---|
| `frontmatter` | Invalid YAML; missing frontmatter; missing `title`/`tags`/`date`. `raw/**` and `generated: true` exempt | FAIL / WARN |
| `tags` | Non-kebab-case tag segments; single-page tags; sub-tags whose parent domain has no pages | WARN / INFO |
| `links` | Broken `[[wikilinks]]`, deduped by target with the linking sources listed | WARN |
| `orphans` | Pages with no inbound wikilink | WARN |
| `size` | Body over 1000 words — "consider splitting" | WARN |
| `clippings` | A `clipping`-tagged descriptor with no markdown URL into `raw/clippings/` (missing `## Sources`) | WARN |
| `stub` | Empty vault-root `.md` idle past a cooldown — the file Obsidian creates when you click a broken wikilink | WARN |
| `log` | Hash mismatch between `meta/log.md` and a daily activity file — i.e. unprocessed changes | WARN |

Only `clippings.tag`, `clippings.raw_path_prefix`, and `stub.min_idle_seconds` are configurable, via
`<vault>/meta/lint-config.yaml`; a malformed file surfaces as an ERROR issue rather than failing
startup. Config is read once — **changes require a restart**.

### 6.6 Configuration

All `WIKI_*` variables are declared as constants in `internal/cli/envvars.go`, which is the canonical
inventory; a new one must be documented there in the same PR.

| Variable | Default | Purpose |
|---|---|---|
| `WIKI_VAULT_DIR` | `/data/vault` | Vault root (`--vault`) |
| `WIKI_INSTANCE_NAME` | — | Human label surfaced by `whoami` |
| `WIKI_PORT` / `WIKI_MCP_PORT` | `8080` / `0` (off) | Listeners |
| `WIKI_BASE_URL` | — | Absolute links in sitemap/RSS/canonical/OpenGraph; unset ⇒ relative only |
| `WIKI_SITE_TITLE`, `WIKI_ROOT_DESCRIPTION` | `My Wiki`, `Shared knowledge base` | Chrome |
| `WIKI_WATCH` | `true` | fsnotify watcher |
| `WIKI_WATCH_EXCLUDE_DIRS` / `WIKI_INDEX_EXCLUDE_DIRS` / `WIKI_INDEX_NORECENTS_DIRS` | `.obsidian` / — / `meta/activity` | Scope control |
| `WIKI_AUTH_ISSUER`, `_AUDIENCE`, `_ALLOWED_GROUPS`, `_ALLOW_ANY_USER`, `_RESOURCE_METADATA_URL` | — | OIDC |
| `WIKI_AUTH_READS` | `false` | Gate GETs too, not just mutations |
| `WIKI_AUTH_DISABLED` | `false` | Explicit ack required to start unauthenticated |
| `WIKI_WEBHOOKS_CONFIG` | — | Path to the dispatch YAML; unset ⇒ feature off |
| `WIKI_INBOX_POLL_INTERVAL` | `60s` | mtime-poll fallback where inotify can't cross NFS |
| `WIKI_VAULT_FRESHNESS_INTERVAL` | `5m` | Freshness scan cadence; non-positive disables |
| `WIKI_SYNC_STATE_PATH` | — | Sync heartbeat file/dir; unset ⇒ that gauge is not registered |

### 6.7 Metrics

`GET /metrics`, HTTP modes only, unauthenticated, bypasses gzip and the readiness gate.

- **HTTP:** `wiki_http_requests_total{method,pattern,status}`, `wiki_http_request_duration_seconds{method,pattern}` — `pattern` is the ServeMux route, falling back to `unknown` to bound cardinality.
- **Freshness (dead-man's switch):** `wiki_vault_last_modified_timestamp_seconds`, `wiki_vault_last_scan_timestamp_seconds`, `wiki_vault_files`, `wiki_vault_scan_duration_seconds`, `wiki_vault_scan_errors_total`, plus `wiki_sync_state_last_modified_timestamp_seconds` and `wiki_sync_state_read_errors_total` when a sync path is configured.
- **Webhooks:** `wiki_webhook_dispatch_total{event,consumer,outcome}`, `wiki_webhook_dispatch_duration_seconds`, `wiki_webhook_retry_total{consumer}`, `wiki_webhook_queue_depth{consumer}`.

**Absence is deliberate.** Snapshot gauges are emitted from a custom collector and stay *absent*
until the first successful scan — a never-`Set` gauge reads as the epoch and would alert forever on
a CrashLooping pod. The one exception is a configured-but-missing sync path, which reports sentinel
`0` so a sync that never started still fires. Every staleness alert must be paired with `absent()`.

## 7. Architecture

```
cmd/wiki-server → internal/cli  (mode wiring, flags, env, one-shot commands)
                       │
   ┌───────────────────┼───────────────────┐
internal/api      internal/mcpserver   internal/server   (transports)
   └───────────────────┼───────────────────┘
              internal/service          (the shared contract: pages, search, lint,
                       │                 tags, directory, activity, log, serverinfo)
              internal/vault            (Storage seam over the filesystem)
                       │
   render · search · notify · dispatch · freshness · memfs · slug · middleware
```

Every capability lives in `internal/service` and is exposed twice. That is what keeps the REST and
MCP surfaces from drifting.

**Render pipeline.** `Builder.Build` runs two parallel passes over the whole vault (`errgroup`
bounded by `GOMAXPROCS`): pass 1 parses every page to an AST and extracts wikilinks; pass 2 renders
with a transclusion cache populated from pass 1, because `![[page#^block]]` needs every target's AST
before any page renders. Results assemble into an immutable `memfs.Snapshot` that replaces the live
one via an atomic pointer swap — no mid-rebuild 404, and in-flight readers keep their snapshot. Tag
pages, `sitemap.xml`, `index.xml` (RSS, 50 most-recent), and `404.html` are produced in the same
pass. Rendered HTML is **never written to disk**.

Goldmark carries GFM, footnotes, definition lists, emoji, Chroma highlighting, Mermaid, and a custom
Obsidian extension: `==highlight==`, `%%comment%%`, `$…$`/`$$…$$` math (delimiters preserved for
client-side KaTeX), `#tag` inline links, 13 callout kinds with fold markers, `^block-id` anchors,
wikilinks with alias/anchor forms, and transclusion to depth 3 with *visible* failure states
(missing / cycle / overflow). Typographer is deliberately off — its `-` trigger breaks block-id
detection. An unresolvable wikilink renders as `class="broken"` with no href, on purpose: broken
links are how the wiki surfaces gaps.

**Search index lifecycle.** The TF-IDF index is built synchronously at startup before the listener
binds, rebuilt on the 2-second post-write debounce, and rebuilt on a 5-minute ticker. It is fully
in-memory and never persisted; a restart rebuilds from scratch. Title tokens are weighted 5×, tags
3×. If the initial build fails the server registers substring only and logs a warning — search
degrades, it never disappears.

**Write path and concurrency.** `PageService` denies `.obsidian` prefixes (404, not 403), validates
frontmatter (`title` + non-empty `tags` + ISO `date`; `raw/**` exempt), slugifies the filename
server-side, writes, then fires a mutation callback that feeds the rebuild notifier, the activity
log, and the webhook router. Path traversal is blocked at the `Storage` seam.

**There is no write-conflict detection.** No ETag, no `If-Match`, no CAS, no per-path lock. Two
concurrent writes to one page race at `os.WriteFile`; `write` overwrites unconditionally. `edit`'s
find/replace is the *de facto* mitigation — it fails loudly if the text it expected has moved — but
it is not a guarantee, and the file write itself is not atomic (no temp-file + rename). This is
tracked as "multi-agent write safety" on the wiki's engine backlog and is the largest correctness
gap in the product (§12).

**Invalidation.** fsnotify (`.md` only, recursive, with a synthetic-create walk to close the
atomic-`mv` race) and API/MCP mutations both feed a `FanoutSink`: one leg debounces 2 s and triggers
directory regeneration → render rebuild → search rebuild; the other debounces 90 s and dispatches
`inbox.changed` webhooks. Where the sync process runs in a different pod across NFS, inotify can't
see its writes, so a 60-second mtime poller covers the inbox path.

**Webhook dispatch.** Opt-in signed outbound events. Per-consumer goroutine and bounded queue,
HMAC-SHA256 over `timestamp.body`, exponential backoff with full jitter, per-consumer circuit
breaker, path include/exclude filters, and `skip_all_deletes` to stop an agent's own cleanup from
re-triggering itself. An empty secret is a non-retriable refusal to send. The envelope carries a URL
to a prompt page in the vault, which the consumer fetches as the agent's system message.

**Identity.** OIDC bearer JWT only, verified for signature, issuer, audience, and expiry, then a
group allowlist. `NewAuth` refuses to construct with an empty allowlist unless `AllowAnyUser` is
explicitly set, and `serve` / `serve mcp http` refuse to *start* without an issuer unless
`WIKI_AUTH_DISABLED` acknowledges it. The whole `/mcp` handler is gated; REST mutations are always
gated when auth is configured, GETs only under `WIKI_AUTH_READS`. Claims land in the request context
and surface via `whoami` and in the audit log. **There is no per-agent identity** — every agent
sharing a token is indistinguishable, and activity entries carry no author field.

## 8. Deployment & operations

Two Deployments in `web-services`, delivered by ArgoCD (auto-sync, prune, self-heal) through the
Helmfile CMP; chart `oci://ghcr.io/jedwards1230/charts/my-wiki`, image `ghcr.io/jedwards1230/my-wiki`.

- **`wiki`** — one replica of `wiki-server serve --mcp-port=8081`. Non-root uid 1001, all caps
  dropped, `readOnlyRootFilesystem: true`. Liveness `/healthz`, readiness `/readyz`.
- **`wiki-sync`** — `obsidian-headless` running `ob sync --continuous`, with a `setup-sync` init
  container that logs in and pulls the vault. Probes are plain filesystem checks on the sync **lock**
  age (readiness 180 s, liveness 300 s) and deliberately never invoke `ob`; the readiness window
  stays below liveness so a pod goes NotReady before a restart fires. Both pinned to `amd64` —
  obsidian-headless SIGBUSes on arm64 during sync reconnect.

**Storage.** The vault is a 5 Gi **RWX PVC on `nfs-dynamic`**, shared by both pods at `/data`. The
sync process's SQLite state lives on a *separate* 2 Gi **RWO Longhorn** volume, because SQLite WAL
shared memory cannot be created on NFS — and RWX always means NFS, Longhorn RWX included. That
split, and the requirement to set `storageClass` explicitly rather than inherit the cluster's
`local-path` default, are the direct output of an 8-day silent outage (`SQLITE_IOERR_SHMSIZE`, 1611
restarts). The Helm chart encodes ten `fail` preconditions so those configurations can't be shipped
again.

**Backups.** None specific to this service. The vault's real durability comes from Obsidian Sync
holding an E2E-encrypted replica plus every synced device's local copy; the PVC is a cache of that.
No Velero annotations are set.

**Access.** `wiki.lilbro.cloud` over Traefik with a LAN-only middleware and a Let's Encrypt cert.
The MCP port is ClusterIP-only (no ingress, no NetworkPolicy) and reached via ContextForge at
`http://wiki.web-services:8081/mcp`. **App-level auth is off in production today**
(`WIKI_AUTH_DISABLED=true`); network position is the only control. Secrets (`wiki-obsidian`,
`wiki-webhooks`) come from 1Password via `OnePasswordItem`.

**Observability.** ServiceMonitor scrapes `/metrics` every 30 s. Logs are `slog` JSON to stdout →
Alloy → Loki. A chart-generated PrometheusRule covers webhook failure rate, drops, and queue depth;
a Grafana ConfigMap carries `WikiSyncHeartbeatStale` (>1 h against a ~200–300 s steady state) and its
mandatory `absent()` companion.

**Upgrade path.** Every push to `main` releases: CI (race tests, vet, golangci-lint, `go mod tidy`
check) → multi-arch image + Helm chart to GHCR → GitHub Release with AI-generated notes. Semver from
PR labels, defaulting to patch. The image tag is auto-bumped into the homelab helmfile; the **chart
version is not tracked**, which is why the deployment currently runs chart `0.11.25` against image
`0.12.3`.

## 9. Quality bar

- **CI is the gate.** `go test -tags=integration -race -coverprofile` (the tag adds a real stdio
  subprocess test), `go vet`, `golangci-lint` (errcheck, govet, staticcheck, unused), and a
  `go mod tidy` diff check. 55 test files against ~34 kLOC.
- **Contract tests are mandatory, not optional.** `TestOpenAPISync` fails on drift in either
  direction between `docs/openapi.yaml` and the route table. `TestDiscoverAdvertisesNewProtocolOverHTTP`
  and the two federation-capability tests are mutation-checked, because both failure modes are
  *silent* — a client quietly downgrades, or ContextForge quietly stops federating resources.
- **Docs ship in the same PR.** A new `WIKI_*` variable requires godoc in `envvars.go`; a new flag or
  port must be exposed in the Helm chart (the automated PR review checks for code↔chart drift
  specifically).
- **Every PR gets an automated review and all threads must be resolved before merge.**
- **Visual verification** for renderer work: seed a deterministic throwaway vault exercising all 13
  callout kinds, transclusion depth limits, math, Mermaid, embeds and wikilinks, then screenshot
  light and dark via Playwright.

**Acceptance criteria for the product as a whole**

1. `wiki-server serve --vault <any Obsidian vault>` renders and serves with zero setup.
2. An agent that has read only `wiki://schema` can find, write, and correctly cross-link a page
   without a second round of instruction.
3. A write is visible in search within ~2 s and in the rendered site with no 404 window.
4. Killing the process loses nothing but caches.
5. Lint's mechanical checks are the exact machine-checkable subset of `meta/schema.md` — no rule in
   one that contradicts the other.
6. Concurrent writes from two agents plus an Obsidian sync never silently lose an edit.
7. A vault that has stopped updating raises an alert before a human notices stale content.

Criteria 5 and 6 are not met today.

## 10. End state vs today

| Capability | End-state intent | Status | Tracking |
|---|---|---|---|
| Render + serve an Obsidian vault | Full dialect, in-process, no build step | shipped | — |
| MCP tool surface | 11 tools at parity with REST, current protocol | shipped | `jedwards1230/my-wiki#184` (ttlMs leg) |
| REST API + self-served OpenAPI | Machine-readable, drift-proof | shipped | — |
| Search | Fast ranked default, graceful fallback | shipped | semantic/vector search unfiled |
| Lint | Machine-checkable subset of the schema, inline on write | partial | MCP enum gap, defeated `orphans`, noisy `size` — all unfiled |
| Freshness alerting | Dead-man's switch on vault + sync | shipped | — |
| Webhook dispatch | Signed, debounced, circuit-broken | shipped | — |
| OIDC auth | Fails closed, group-gated, RFC 9728 | shipped but **not enabled in prod** | deployment task, not engine |
| Multi-agent write safety | No silent lost update | **not started** | unfiled |
| Change provenance (author/agent in activity) | Attribution from JWT claims | not started | unfiled |
| Page/folder conflict auto-relocation | `foo.md` → `foo/index.md` on demand | not started | unfiled |
| Renderer golden tests | Per-template regression coverage | not started | `jedwards1230/my-wiki#76` |
| Backlink snippets | Context excerpt per backlink | not started | `jedwards1230/my-wiki#77` |
| Webfont CDN fallback | Vendor script degrades instead of failing | not started | `jedwards1230/my-wiki#78` |
| Init-sync failure diagnosability | Permanent vs transient distinguishable *and observable* | partial | `jedwards1230/my-wiki#185`, `jedwards1230/homelab-k8s#1089` |
| Sync backend abstraction (`obsidian`\|`git`\|`none`) | Deployable without an Obsidian subscription | not started | unfiled (§12 Q3) |
| Git version history (`history`/`diff`/`revert`) | Page-level time travel | not started | unfiled (§12 Q4) |
| OTLP traces | API/MCP spans to Tempo | not started | unfiled |
| Spaces (multi-vault) | Vaults per OIDC group | declared direction only | §12 |

## 11. Risks & accepted limits

- **Concurrent writes can silently lose an edit.** Accepted so far because write volume is low and
  `edit` fails loudly on drift, but it is the correctness gap that scales worst with agent count.
- **Full re-render on every change.** No incremental invalidation; a 477-page vault rebuilds in
  full every 2 s debounce window. Fine now, linear in vault size.
- **The single-vault assumption is everywhere.** Deepening it makes Spaces more expensive later.
- **Statelessness stops at the sync dependency.** obsidian-headless needs a SQLite DB on RWO block
  storage, and it segfaults on arm64 — the one piece of the stack that is neither stateless nor
  portable.
- **Obsidian Sync is a paid third-party dependency** with no self-hosted fallback shipped.
- **A fresh sync heartbeat proves liveness, not success.** The "heartbeat fresh + vault mtime stale"
  signature degrades toward useless as agent write traffic rises — precisely the direction this
  product is heading.
- **Prod runs unauthenticated behind a LAN-only middleware.** Anything on the LAN can write to the
  wiki; anything in the cluster can reach the MCP port (no NetworkPolicy).
- **Metrics are unauthenticated** on the same port as the site.
- **Init-container exit codes are not observable in Prometheus** — kube-state-metrics exports no
  init-container exit-code series, so the documented "alert on exit 3 vs 4" is not actionable as
  written (`jedwards1230/my-wiki#185`).
- **Literal `$` in prose renders as math.** Obsidian shows it fine, so the breakage only appears on
  the rendered site — a foot-gun that is currently a documented authoring rule rather than a fix.
- **Search has no stemming, stopwords, or phrase support**, and `engine=all` concatenates results
  from both engines without merging, deduping, or re-ranking.
- **The `orphans` check is structurally defeated.** It harvests link targets from every page
  including the auto-generated `index.md` files, which link everything — so it reports 0 on a
  477-page vault and always will. `size` has the inverse problem: it flags `raw/clippings/`
  verbatim transcripts, which are supposed to be long, drowning the real splitting candidates.

## 12. Open decisions

**Q1 — Write-conflict semantics.** Nothing prevents two agents from silently overwriting each other.
What is the contract?

- **(a)** Accept last-write-wins; rely on `edit` failing loudly. Zero work, zero guarantee.
- **(b)** Optimistic concurrency: return an mtime/ETag from `read`, accept `If-Match` on `write`
  and PUT, reject with 409 on mismatch. Opt-in — an agent that omits it keeps today's behavior.
- **(c)** Per-path mutex + atomic temp-file-and-rename writes. Fixes torn writes and same-process
  races, but not a race against the Obsidian sync process.

*Recommendation:* **(b) plus the atomic-write half of (c).** (c) alone gives durability without
detection; (b) gives the fleet a way to be correct, and atomic rename removes the torn-file case for
free. *Consequence:* one new optional field on `read`/`write`/`edit` in both surfaces, and agents
must be told to use it — a schema/prompt change, not just code.

**Q2 — Does lint own the schema, or does the schema own lint?** `meta/schema.md` advertises
mechanical checks the binary doesn't implement (stale `inbox/` files, `note` entries worth
promoting), and lint enforces rules the schema doesn't state. Two ways out:

- **(a)** Schema is authoritative: implement the missing checks, and treat any lint rule with no
  schema clause as a bug.
- **(b)** Lint is authoritative for the mechanical layer: trim the schema's claims to what actually
  runs and keep the rest as prose guidance for LLM audits.

*Recommendation:* **(a).** The schema is the agent-facing contract and is shipped as an MCP resource;
an agent that trusts it and finds it wrong loses trust in all of it. *Consequence:* two new lint
checks, and an ongoing obligation to keep them in step — which is exactly what criterion 5 in §9 asks
for.

**Q3 — Is Obsidian Sync permanent, or is the sync backend pluggable?** The wiki's engine backlog
carries a large `sync.backend: obsidian|git|none` epic, and notes the Go server is already fully
decoupled — it is entirely chart and Dockerfile work.

- **(a)** Obsidian-only. Simplest; keeps a paid dependency and a Node runtime in the image forever.
- **(b)** Ship `none` only — the vault is a volume someone else fills. Small, and it's what the
  work-laptop and any OSS adopter already need.
- **(c)** Full epic: `none` + `git-sync` + `obsidian`, conditional init container and sidecar.

*Recommendation:* **(b) now, (c) as a later increment.** `none` is a conditional template and a
Dockerfile branch; it makes the chart installable by anyone without an Obsidian subscription, which
is the main thing blocking the repo from being usable as the OSS project it is published as.
*Consequence:* the image can drop `obsidian-headless` for non-Obsidian installs, which also removes
the arm64 pin.

**Q4 — Git version history: in scope, or does it violate "no database, ever"?** The backlog proposes
a `GitTracker` auto-committing on mutation plus `history` / `diff` / `revert` on both surfaces.

- **(a)** Out of scope. Obsidian Sync already keeps version history; the wiki is current state.
- **(b)** In scope as an optional feature, enabled when `.git` exists. Git on disk is still
  markdown on a filesystem, so the principle survives.
- **(c)** In scope and default-on.

*Recommendation:* **(b).** It gives agents an undo for their own mistakes — the thing that makes
aggressive agent writes safe to encourage — without becoming a required dependency. Note the backlog
drafts these MCP tools as `wiki_history`/`wiki_diff`/`wiki_revert`, which contradicts the bare-name
convention; they should be `history`/`diff`/`revert`. *Consequence:* a `go-git` dependency, and a
real question about how auto-commits interleave with a concurrent `git-sync` pull under Q3(c).

**Q5 — Spaces (multi-vault): committed direction or aspiration?** `CLAUDE.md` declares it as
direction and asks that single-vault coupling not be deepened, but nothing is built and no issue
tracks it.

- **(a)** Commit: file the epic, and treat "does this deepen single-vault coupling?" as a review
  question on every PR.
- **(b)** Park it: delete the constraint from `CLAUDE.md` and let the code assume one vault until a
  second one actually exists.

*Recommendation:* **(a), narrowly** — keep the constraint (it costs nothing; it is just "pass the
vault as a parameter"), but file no epic and build nothing until there is a second vault to serve.
*Consequence:* a standing, cheap design tax in exchange for keeping the option open.
