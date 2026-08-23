# my-wiki — PRD

> **Status:** draft v1 · 2026-08-22 · owner: justin
> **Repo:** [`jedwards1230/my-wiki`](https://github.com/jedwards1230/my-wiki) · **Live:** <https://wiki.lilbro.cloud> · **Shipped:** `v0.12.3` (2026-08-04)
> **Scope:** the engine. Vault *content* — page conventions, the taxonomy, the migration backlog — is governed by [`meta/schema.md`](https://wiki.lilbro.cloud/meta/schema) on the wiki itself and is explicitly out of scope here.

## 1. What it is

`wiki-server` is a single Go binary that turns a directory of Obsidian-flavored markdown into three
coordinated interfaces: an MCP server for AI agents, a REST API, and a rendered HTML site. It owns
no content. The vault is the source of truth on disk; the server renders, indexes, lints, and
serves it, and every write it accepts lands back as markdown in that same directory.

The distinction is load-bearing. **This repo is the engine.** The homelab instance's vault —
536 pages at wiki.lilbro.cloud, of which 477 are authored and 59 are generated `index.md` files —
is a separate Obsidian vault replicated in by Obsidian Sync. Pointing `--vault` at any other
Obsidian vault must work with zero migration and zero setup, because the server keeps no state a
vault couldn't reconstruct.

**The boundary test for this document:** *would this change a `.go` file, the Helm chart, or the
Dockerfile?* If yes it belongs here. If it only changes a page in the vault, it belongs in
`meta/schema.md`. Page counts, tag taxonomies, folder conventions, and which folders a given
deployment chooses to sync are all content or deployment policy, not product surface.

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
| Schema vs lint authority | **The schema is authoritative.** A mechanical check it advertises must exist; a lint rule with no schema clause is a bug | It ships as an MCP resource. An agent that trusts it and finds it wrong stops trusting all of it. |
| Write concurrency | **Optimistic concurrency, opt-in** — `read` returns a version token, `write`/PUT accept `If-Match`, mismatch is 409 — plus **atomic temp-file-and-rename** on every write | Detection and durability are different problems: rename fixes torn files, `If-Match` fixes lost updates. Opt-in means an agent that omits the token keeps today's behavior. |
| Sync backend | **Pluggable: `sync.backend: obsidian \| git \| none`**, surfaced to the server as `WIKI_SYNC_BACKEND` | Obsidian Sync is the homelab's choice, not the product's. `none` makes the chart installable by anyone; `git` serves deployments that already have a repo. Each backend owes its own durability answer (§8). |
| Version history | **Optional `GitTracker`, constructed when `.git` exists**; tools named `history` / `diff` / `revert`. Its *write* leg arms only when `WIKI_SYNC_BACKEND != git` | Git on disk is still markdown on a filesystem, so "no database" survives. It gives agents an undo, which is what makes aggressive agent writes safe to encourage. Bare names, per the tool-naming convention. `.git` presence alone can't gate it — under the `git` backend `.git` always exists (§7). |
| Multi-vault ("Spaces") | Declared direction; **no epic, nothing built** until a second vault exists. The only standing cost is a design constraint: pass the vault as a parameter, never a process global | Keeping the option open is nearly free; building for a tenant that does not exist is not. |
| `.obsidian/` | Excluded and denied on every surface | Editor state is not content. Denial returns 404, not 403, so the API doesn't confirm existence. |

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
| `lint` | Check the vault | `check` (`all`\|`frontmatter`\|`tags`\|`links`\|`orphans`\|`size`\|`log`) | Structured `LintReport`. The enum exposes six of the eight checks — `clippings` and `stub` are unreachable from MCP though REST accepts them (§6.5). |
| `tags` | Tag census | — | Every tag with page count, sorted by frequency. The "check before inventing a tag" call. |
| `whoami` | Instance identity | — | `{name, version, vault_dir, go_version, instance_name?, user?}` — lets an agent tell which wiki it is connected to. |
| `activity` | Narrative log entry | `type`* (`edit`\|`create`\|`delete`\|`lint`\|`note`\|`migrate`\|`move`), `title`*, `time`, `summary`, `day_summary`, `touched` | **For session summaries only** — individual page mutations are auto-logged by the server; duplicating them here is a defect. |
| `history` | Commit log for one page | `path`*, `limit` | *End state, not built.* Only when `GitTracker` is active. |
| `diff` | Change between two revisions | `path`*, `from`, `to` | *End state, not built.* Read-only; safe under every sync backend. |
| `revert` | Restore a page to a revision | `path`*, `hash`* | *End state, not built.* Writes a new commit, never rewrites history. Refused under `sync.backend: git` (§7). |

**Version tokens (end state, not built).** `read` returns an opaque version token for the page.
`write` and `edit` accept it as an optional precondition; a stale token is a conflict, not an
overwrite. Omitting it preserves today's last-write-wins behavior, so no existing caller breaks.

*Annotations:* `read`/`list`/`search`/`lint`/`tags`/`whoami` are read-only + idempotent; `write`/`edit`/
`delete`/`move` are marked destructive. **There is no dry-run parameter on any tool.**

**Resources:** exactly one — `wiki://schema` (`text/markdown`), the agent operating manual, backed by
`meta/schema.md`. No prompts, no resource templates. `ServerOptions.Capabilities` is left **nil** on
purpose so the SDK auto-derives a truthy `resources` capability; an explicit empty struct serializes
as `{}`, which is falsy in Python and silently kills ContextForge resource federation.

**Protocol:** revision `2026-07-28` (`modelcontextprotocol/go-sdk v1.7.0`), negotiating down to
`2025-11-25` / `2025-06-18` / `2025-03-26` / `2024-11-05`. SEP-2575 cache hints stamped on listings
(`ttlMs 300000`) and `resources/read` (`ttlMs 60000`). MCP logging removed per SEP-2577; audit events
go to `slog` enriched with the caller's OIDC subject. (`jedwards1230/my-wiki#184`'s prose calls
the cache-hint SEP "SEP-2549"; the source of truth is the code — `internal/mcpserver/server.go` and
`docs/SERVER-MODES.md` both say **SEP-2575**, and the issue text is the outlier.)

### 6.2 HTTP API

17 operations under `/api`, envelope `{"data":…}` / `{"error":…}`, plus a `warnings` array of lint
issues on mutations. `docs/openapi.yaml` is the single source of truth and is held in **exact** sync
with the registered route table by `TestOpenAPISync`; the spec is served verbatim from the running
instance.

| Method + path | Notes |
|---|---|
| `GET /api/pages/{path...}` | `{path, content}`; 404 on miss or denied prefix |
| `GET /api/pages` | `?prefix`, `?sort_by`, `?limit` |
| `PUT /api/pages/{path...}` | Body = raw markdown, **10 MB cap** (413). 400 on frontmatter validation failure. *End state:* honors `If-Match`, **409** on a stale token |
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
| `/healthz`, `/readyz`, `/metrics` | Liveness; readiness (503 until first render completes); Prometheus. All three bypass the readiness gate — **and so does all of `/api/*`**, so the entire REST surface answers before the first render finishes. Only the rendered-HTML routes are gated. |
| `/mcp` | **Separate port**, never on the main mux. `--mcp-port` defaults to **0 (off)** in `serve`; `serve mcp http` listens on 8081. Production passes `--mcp-port=8081` explicitly. Stateless: `POST` only; `GET`/`DELETE` return 405. |

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

Eight checks. `errors` counts everything except `INFO`; a non-zero count fails the CLI. Scoped
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

**The schema governs this table.** Two checks `meta/schema.md` advertises — stale `inbox/` files
past N days, and `note` activity entries worth promoting to pages — are not implemented and must be.
Conversely the MCP `lint` enum lists only six of the eight checks above (`clippings` and `stub` are
missing) while REST and the CLI accept all eight, so the agent surface is narrower than the human
one. The arithmetic is worth stating because it has been miscounted: the MCP enum carries **seven**
values and `LintService.Run` accepts **nine**, but one value on each side is `all`, which is not a
check — six checks versus eight, a gap of exactly two.

**The `lint` tool description advertises a tier that does not exist.** It tells agents that
content-level issues "require manual review or the semantic lint layer"
(`internal/mcpserver/server.go`). There is no semantic lint layer, anywhere — not in the source, not
in this document. Since the tool description *is* the agent-facing contract, either the tier gets
claimed as end state or the clause comes out: `jedwards1230/my-wiki#200`.

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
| `WIKI_SYNC_BACKEND` | `none` | *End state, not built.* `obsidian` \| `git` \| `none`. Rendered by the chart from `sync.backend`; the only thing that tells the server whether another process owns `.git` (§7) |

### 6.7 Metrics

`GET /metrics`, HTTP modes only, unauthenticated, bypasses gzip and the readiness gate.

- **HTTP:** `wiki_http_requests_total{method,pattern,status}`, `wiki_http_request_duration_seconds{method,pattern}` — `pattern` is the ServeMux route, falling back to `unknown` to bound cardinality.
- **Freshness (dead-man's switch):** `wiki_vault_last_modified_timestamp_seconds`, `wiki_vault_last_scan_timestamp_seconds`, `wiki_vault_files`, `wiki_vault_scan_duration_seconds`, `wiki_vault_scan_errors_total`, plus `wiki_sync_state_last_modified_timestamp_seconds` and `wiki_sync_state_read_errors_total` when a sync path is configured.
- **Webhooks:** `wiki_webhook_dispatch_total{event,consumer,outcome}`, `wiki_webhook_dispatch_duration_seconds`, `wiki_webhook_retry_total{consumer}`, `wiki_webhook_queue_depth{consumer}`.

**Absence is deliberate.** Snapshot gauges are emitted from a custom collector and stay *absent*
until the first successful scan — a never-`Set` gauge reads as the epoch and would alert forever on
a CrashLooping pod. The one exception is a configured-but-missing sync path, which reports sentinel
`0` so a sync that never started still fires. Every staleness alert must be paired with `absent()`.

### 6.8 Where the end state stops

Three capabilities are named here because leaving them implicit is how a defect list gets mistaken
for a destination. Each states what is in, what is permanently out, and why.

**Retrieval.** §2's bet is that agents *build and maintain* a structured wiki rather than retrieving
raw documents per question. That makes search the most load-bearing surface in the product, and
"whatever TF-IDF happens to return" is not a specification. The end state is:

- **In:** stemming and a stopword list in the tokenizer; quoted phrase queries; and an `engine=all`
  that *merges, dedupes, and re-ranks* instead of concatenating two result lists. All three are
  in-memory, add no dependency, and survive a restart by being rebuilt — so "no database, ever"
  (§4) is untouched.
- **Permanently out: embedding / vector search inside this engine.** It needs either a persisted
  index or a live model service, and both violate a locked decision — the first "no database,
  ever", the second "no external runtime dependency beyond the sync backend" (§4). Semantic
  retrieval is real and useful; it belongs to the *agent* calling `search` and reading `/path.md`,
  or to a separate service that consumes this one. It does not belong in `wiki-server`.

That line is the point: the engine's job is to be fast, exact, and always-fresh over markdown on
disk. Anything that has to be trained, embedded, or persisted is somebody else's tier.

**Authentication.** The ideal end state runs **authenticated**, not unauthenticated behind a LAN
middleware. Two rows in §10 already presuppose it — provenance from JWT claims
(`jedwards1230/my-wiki#198`) is meaningless without a subject, and per-agent identity has no other
source — so the position should be stated rather than inferred. End state: OIDC on, `WIKI_AUTH_READS=true`
so reads are gated too, and each agent issued its own client credentials so subjects are distinct.
Network position becomes defense in depth. `WIKI_AUTH_DISABLED` stays as the explicit, noisy escape
hatch it already is; today's production value of `true` is a deployment debt (§10), not the design.

**Write volume.** A product whose thesis is a fleet of agents writing concurrently currently has no
quota, no backpressure, and no rate limit — the only bound anywhere is the 10 MB per-request body
cap. End state: a per-subject token bucket on the mutating routes (in-memory, single replica, no new
state), and coalescing rather than queueing on the rebuild path so a write storm produces one
rebuild instead of a queue of them. This is the natural companion to authentication: a token bucket
needs a subject to bucket by, which is another reason the two land together.

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

**Today there is no write-conflict detection.** No ETag, no `If-Match`, no CAS, no per-path lock.
Two concurrent writes to one page race at `os.WriteFile`; `write` overwrites unconditionally.
`edit`'s find/replace is the *de facto* mitigation — it fails loudly if the text it expected has
moved — but it is not a guarantee, and the write itself is not atomic (no temp-file + rename).

**End state: optimistic concurrency plus atomic writes.** `read` returns a version token derived
from the file's mtime and size. `write` and PUT accept it as an optional `If-Match` precondition and
answer a stale token with 409 rather than an overwrite; `edit` accepts it the same way, on top of
the find/replace guard it already has. Every write goes through a temp file in the destination
directory followed by `os.Rename`, so a reader never sees a half-written page and a crash mid-write
leaves the previous version intact. The precondition is opt-in — a caller that omits the token gets
today's behavior — so nothing existing breaks, but the schema will tell agents to send it.

Note what this does and does not cover: it makes *server-mediated* writes safe against each other.
A race between the server and the sync process writing the same file from outside is still resolved
by whichever wrote last, because the server does not own that file's lifecycle.

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

**Version history and the sync backend must not both own `.git`.** With `sync.backend` pluggable
(§8) and `GitTracker` enabled whenever `.git` exists, the two can collide: a sync sidecar pulling on
an interval and a server auto-committing on every mutation are two writers to one working tree, and
the server has no merge strategy and no human to escalate to.

**The server has to be told which backend it is running under.** `.git` presence cannot decide this
— under `sync.backend: git` a `.git` directory *always* exists, so presence-detection would enable
auto-commit in exactly the case that must disable it. The carrier is a new environment variable,
`WIKI_SYNC_BACKEND` (`obsidian` | `git` | `none`, default `none`), declared in
`internal/cli/envvars.go` like every other (§6.6) and rendered by the chart from `sync.backend` so
the two cannot disagree. `GitTracker` then constructs whenever `.git` exists, but its *write* leg —
auto-commit and `revert` — arms only when `WIKI_SYNC_BACKEND != git`:

| `sync.backend` = `WIKI_SYNC_BACKEND` | `GitTracker` auto-commit | `history` / `diff` | `revert` |
|---|---|---|---|
| `none` | **enabled** (the intended pairing — git is the history store, not the transport) | yes | yes |
| `obsidian` | **enabled** — Obsidian Sync moves files, git only observes them. A sidecar's incoming edit arrives as an ordinary file change and is committed like any other, attributed to `wiki-server`, which is exactly how a remote edit should enter history | yes | yes |
| `git` | **disabled** — the sync process owns the repo | yes, read from the working tree the sidecar maintains | **refused** at runtime |

`history` and `diff` only read `.git`, so they stay available under every backend. Only the two
operations that *write* commits are gated. Under `sync.backend: git` a revert would race the next
pull, so `revert` returns an `IsError` text result naming the backend and pointing at the remote —
the same failure shape every other tool uses (§6.1), not a silent queue and not a panic.

**What enforces what.** The runtime gate above is the enforcement; a Helm `fail` cannot express
"this tool returns an error when called," because `fail` aborts `helm template` and every one of the
three backends is a shippable configuration. The chart's two jobs are narrower and both are real:
render `WIKI_SYNC_BACKEND` from `sync.backend` so drift between them is unrepresentable, and carry
one `fail` for the genuinely unshippable *values* combination — an operator explicitly setting
`sync.git.autoCommit: true` alongside `sync.backend: git`, i.e. asking in values for the pairing the
runtime refuses. That is a values combination, which is what the chart's existing guard-rails (§8)
are for — the chart already carries fifteen of them (§8). Everything else about this exclusion
lives in Go.

Two caveats this creates, both accepted. A `git-sync` sidecar commonly clones shallow and resets a
detached HEAD on each pull, so under `sync.backend: git` `history` may see a truncated log — that is
the sync deployment's configuration to widen, not the engine's to work around. And `history`/`diff`
read whatever tree the sidecar last wrote, so they are as fresh as the pull interval, no fresher.

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
**Sync is a pluggable backend** (`sync.backend`), not a fixed dependency. `obsidian` runs
obsidian-headless as today; `git` runs a `git-sync` sidecar against a remote; `none` omits the sync
workload entirely and treats the vault as a volume somebody else fills.

Most of this is chart and Dockerfile work — a conditional init container, a conditional sidecar, a
`sync.git` block (`repo`, `branch`, `interval`, `sshSecretName`), and making the `obsidian-headless`
install conditional in the image — because the Go server's *serving* path is genuinely indifferent
to how bytes arrive in the vault directory. **It is not purely chart work, and the epic should not
be scoped as though it were.** One bounded Go change comes with it: the server must read
`WIKI_SYNC_BACKEND` and use it to arm or disarm `GitTracker`'s write leg (§7). That is a new env
constant, a config field, and one branch — small, but it is Go, and pretending otherwise is what
made the earlier version of this section incoherent.

`none` is what makes the chart installable by anyone without an Obsidian subscription, and it also
lets those images drop the amd64 pin.

- **`wiki-sync`** (`sync.backend: obsidian`) — `obsidian-headless` running `ob sync --continuous`, with a `setup-sync` init
  container that logs in and pulls the vault. Probes are plain filesystem checks on the sync **lock**
  age (readiness 180 s, liveness 300 s) and deliberately never invoke `ob`; the readiness window
  stays below liveness so a pod goes NotReady before a restart fires. Both pinned to `amd64` —
  obsidian-headless SIGBUSes on arm64 during sync reconnect.

**Storage.** The vault is a 5 Gi **RWX PVC on `nfs-dynamic`**, shared by both pods at `/data`. The
sync process's SQLite state lives on a *separate* 2 Gi **RWO Longhorn** volume, because SQLite WAL
shared memory cannot be created on NFS — and RWX always means NFS, Longhorn RWX included. That
split, and the requirement to set `storageClass` explicitly rather than inherit the cluster's
`local-path` default, are the direct output of an 8-day silent outage (`SQLITE_IOERR_SHMSIZE`, 1611
restarts). The Helm chart encodes **fifteen** `fail` preconditions so those configurations can't be
shipped again — eleven in `deployment.yaml` (auth/discovery coherence, MCP ingress exposure, the
standalone-sync RWX requirement, the `homePersistence` must-not-be-RWX rule, and the multi-replica
RWX rule), one in `networkpolicy.yaml`, and three in `webhooks-configmap.yaml`. Counted chart-wide;
the deployment-only figure is eleven.

**Backups and durability.** Today, none specific to this service: the vault's real durability comes
from Obsidian Sync holding an E2E-encrypted replica plus every synced device's local copy, so the
PVC is a cache of that. No Velero annotations are set.

**That argument is load-bearing on one backend, and making all three shippable breaks it.** It has
to be re-answered per backend, and the end state does:

| `sync.backend` | Where the durable copy lives | What the end state owes |
|---|---|---|
| `obsidian` | The E2E-encrypted Obsidian replica + every synced device. The PVC is a cache | Nothing new — today's answer, and it is a good one |
| `git` | The remote, **only if the deployment pushes**. A stock `git-sync` sidecar pulls; it does not push. Server-side writes would then live nowhere but the PVC | Either the sidecar pushes, or the deployment treats the vault as read-mostly and externally authored. The chart must say which, and default to the safe reading. This is a second, independent reason `git` disables auto-commit (§7): commits nobody pushes are not history, they are landfill |
| `none` | **The PVC, and nothing else.** No replica, no device copies, no remote | The chart annotates the vault PVC for Velero by default under `none`, and `helm` says so at install time. "Somebody else fills the volume" is not a backup story, and `none` is precisely the mode a stranger installing the chart will land on |

The through-line: "no database, ever" (§4) buys portability, not durability. Every byte still lives
in exactly one directory, and each backend answers "what else has a copy of that directory?"
differently. Shipping a backend without answering it is shipping a data-loss default.

**Excluded folders are a deployment choice, not a product decision.** The chart's
`obsidian.excludedFolders` defaults to **empty**, and its own comment recommends keeping private
content in a separate unsynced vault rather than a folder inside this one. The homelab instance
sets `excludedFolders: "private"` so that folder stays personal-device-only; that is one operator's
content policy, and the engine has no `private/` concept anywhere in its source. Everything the
vault does contain is LAN-visible and agent-visible by design. (Contrast `.obsidian/`, which *is*
engine behavior — denied at the service layer and excluded from the watcher by default, §5.)

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
| MCP tool surface | Tools at parity with REST, current protocol | shipped | `jedwards1230/my-wiki#184` (ttlMs leg) |
| REST API + self-served OpenAPI | Machine-readable, drift-proof incl. parameter schemas | partial | `jedwards1230/my-wiki#197` |
| Search — consistency | Fast ranked default, graceful fallback, same default on every surface | partial | `jedwards1230/my-wiki#196` |
| Lint — mechanical checks | Exactly the machine-checkable subset of `meta/schema.md`, reachable from both surfaces | partial | `jedwards1230/my-wiki#193`, `#195`; the two schema-advertised checks (stale `inbox/`, promotable `note`s) unfiled |
| Lint — signal quality | `size` skips the `raw/` source tier, whose transcripts are meant to be long | not started | unfiled |
| Lint — advertised vs real surface | The `lint` tool description promises no tier the product doesn't ship | not started | `jedwards1230/my-wiki#200` |
| Freshness alerting | Dead-man's switch on vault + sync | shipped | — |
| Webhook dispatch | Signed, debounced, circuit-broken | shipped | — |
| OIDC auth | Fails closed, group-gated, RFC 9728 | shipped but **not enabled in prod** | deployment task, not engine |
| Multi-agent write safety | Opt-in `If-Match` + 409, atomic temp-file-and-rename | **not started** | `jedwards1230/my-wiki#194` |
| Change provenance | Author/agent on every activity entry, from JWT claims | not started | `jedwards1230/my-wiki#198` |
| Sync backend abstraction | `sync.backend: obsidian \| git \| none`, all three shippable, each with a stated durability answer (§8) | not started | unfiled |
| Version history | Optional `GitTracker` + `history`/`diff`/`revert`, write leg gated by `WIKI_SYNC_BACKEND` (§7) | not started | unfiled |
| Backup posture per backend | Velero on the vault PVC by default under `none`; a stated push/read-mostly answer under `git` | not started | unfiled |
| Search quality | Stemming, stopwords, phrase queries, and a merged re-ranked `engine=all`. Embedding search stays **out** (§6.8) | not started | unfiled |
| Authenticated by default | OIDC on, `WIKI_AUTH_READS=true`, per-agent subjects — LAN position becomes defense in depth, not the only control | not started | unfiled |
| Write-volume control | Per-subject token bucket on mutating routes; rebuild coalescing under write storms | not started | unfiled |
| Page/folder conflict auto-relocation | `foo.md` → `foo/index.md` on demand, instead of an agent-side workaround | not started | unfiled |
| Directory-level wikilinks | `[[research/neuroscience]]` resolves to the directory index | not started | unfiled (SCHEMA-004 in the audits) |
| Literal `$` in prose | Does not silently become KaTeX math on the rendered site | not started | unfiled |
| Renderer golden tests | Per-template regression coverage | not started | `jedwards1230/my-wiki#76` |
| Backlink snippets | Context excerpt per backlink | not started | `jedwards1230/my-wiki#77` |
| Webfont CDN fallback | Vendor script degrades instead of failing | not started | `jedwards1230/my-wiki#78` |
| Init-sync failure diagnosability | Permanent vs transient distinguishable *and observable* | partial | `jedwards1230/my-wiki#185`, `jedwards1230/homelab-k8s#1089` |
| OTLP traces | API/MCP spans to Tempo | not started | unfiled |
| Spaces (multi-vault) | Vaults per OIDC group | direction only, deliberately unbuilt (§5) | none by design |

## 11. Risks & accepted limits

- **Concurrent writes can silently lose an edit** until `#194` lands. The decision is made (§5, §7);
  the gap is real today and scales worst with agent count. Even afterwards, a race between the
  server and the sync process is resolved by whoever wrote last — the server does not own that
  file's lifecycle, and no `If-Match` can change that.
- **Full re-render on every change.** No incremental invalidation; the production vault rebuilds
  all **536** rendered pages every 2 s debounce window. (536 is the figure that matters here —
  everything the server renders, including the 59 generated `index.md` pages; the 477 in §1 counts
  authored pages only.) Fine now, linear in vault size, and it gets worse in exactly the direction
  this product is heading.
- **The single-vault assumption is everywhere.** Deepening it makes Spaces more expensive later.
- **Statelessness stops at the sync dependency.** obsidian-headless needs a SQLite DB on RWO block
  storage, and it segfaults on arm64 — the one piece of the stack that is neither stateless nor
  portable.
- **Obsidian Sync is a paid third-party dependency** with no self-hosted fallback shipped.
- **Two of the three end-state sync backends have no backup story yet** (§8). Under `none` the PVC
  is the only copy of the vault and no Velero annotation is set; under `git` a stock pull-only
  `git-sync` sidecar never pushes, so anything the server writes stays on the PVC too. Shipping
  either without its durability answer ships a data-loss default.
- **A fresh sync heartbeat proves liveness, not success.** The "heartbeat fresh + vault mtime stale"
  signature degrades toward useless as agent write traffic rises — precisely the direction this
  product is heading.
- **Prod runs unauthenticated behind a LAN-only middleware.** Anything on the LAN can write to the
  wiki; anything in the cluster can reach the MCP port (no NetworkPolicy). This is deployment debt,
  not the design: the end state runs authenticated (§6.8), and per-agent provenance (`#198`) cannot
  work until it does.
- **No write-volume control of any kind** (§6.8) — no quota, no rate limit, no backpressure — for a
  product whose stated thesis is a fleet of agents writing concurrently.
- **Metrics are unauthenticated** on the same port as the site.
- **Init-container exit codes are not observable in Prometheus** — kube-state-metrics exports no
  init-container exit-code series, so the documented "alert on exit 3 vs 4" is not actionable as
  written (`jedwards1230/my-wiki#185`).
- **Literal `$` in prose renders as math.** Obsidian shows it fine, so the breakage only appears on
  the rendered site — a foot-gun that is currently a documented authoring rule rather than a fix.
- **Search has no stemming, stopwords, or phrase support**, all three of which are in the end state
  (§6.8), and `engine=all` concatenates results
  from both engines without merging, deduping, or re-ranking.
- **The `orphans` check is structurally defeated** (`#193`): it harvests link targets from every
  page including the auto-generated `index.md` files, which link everything, so it reports 0 on a
  536-page vault and always will. Verified live: `GET /api/lint?check=orphans` returns
  `{"total":0,"errors":0}`. `size` has the inverse problem — it flags `raw/clippings/`
  verbatim transcripts, which are supposed to be long, drowning the real splitting candidates.
- **Enabling `sync.backend: git` costs the vault its `revert`** (§7). Two writers to one `.git` is
  not a configuration to tune, so the chart refuses it; deployments that want page-level undo
  should choose `none` or `obsidian`.

## 12. Decisions taken

All five forks that were open when this document was drafted are settled. They are reflected in the
locked-decision table (§5) and in the sections named below; this table is the record of *what was
chosen and why*, not an open question list.

| # | Question | Decision | Where the end state is described |
|---|---|---|---|
| 1 | Write-conflict semantics | Optimistic concurrency — a version token from `read`, optional `If-Match` on `write`/`edit`/PUT, 409 on mismatch — **plus** atomic temp-file-and-rename on every write. Opt-in, so omitting the token preserves today's behavior | §6.1, §6.2, §7 · `jedwards1230/my-wiki#194` |
| 2 | Does lint own the schema, or the schema own lint? | **The schema is authoritative.** The two checks `meta/schema.md` advertises but does not implement (stale `inbox/` files, promotable `note` entries) must be built; any lint rule with no schema clause is a bug | §6.5 · `jedwards1230/my-wiki#193`, `#195` |
| 3 | Is Obsidian Sync permanent? | **No — the full backend abstraction is the end state:** `sync.backend: obsidian \| git \| none`, all three shippable, each owing its own durability answer. Mostly chart and Dockerfile work, **plus one bounded Go change**: the server reads `WIKI_SYNC_BACKEND` to gate `GitTracker`'s write leg | §8 · §7 |
| 4 | Git version history | **Optional**, constructed when `.git` exists, exposed as `history` / `diff` / `revert` — bare names, matching every other tool. The write leg is gated on the sync backend, not on `.git` presence | §6.1, §7 |
| 5 | Spaces (multi-vault) | **Keep the constraint, build nothing.** Pass the vault as a parameter rather than a process global; file no epic until a second vault exists | §5 |

Decisions 3 and 4 interact — a `git-sync` sidecar and a `GitTracker` auto-committer are two writers
to one working tree. That interaction is resolved explicitly in §7: auto-commit and `revert` are
disabled under `sync.backend: git`, while `history` and `diff` stay available under every backend
because they only read.

**The enforcement point is the server, not the chart**, and an earlier draft of this document had it
backwards. `.git` presence cannot gate the exclusion, because under the `git` backend `.git` always
exists; the server has to be *told*, via `WIKI_SYNC_BACKEND` rendered from `sync.backend`. And a
Helm `fail` cannot express a runtime refusal — it aborts `helm template`, while all three backends
are shippable configurations with different capability sets. The chart's real jobs are to keep
`WIKI_SYNC_BACKEND` and `sync.backend` from drifting apart, and to `fail` on the one genuinely
unshippable *values* combination: an explicit `sync.git.autoCommit: true` under
`sync.backend: git`.

The correction is worth recording rather than quietly fixing, because the original wording made the
epic look like pure chart work and would have shipped a `GitTracker` that auto-commits into a
sidecar's working tree.
