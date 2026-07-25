// Package cli — environment variable inventory.
//
// This file is the canonical inventory of every WIKI_* environment variable
// the server honors. Each constant's godoc describes the variable's purpose,
// default, and effect on behavior. CLAUDE.md and other docs point here
// instead of duplicating the list.
//
// Convention: every os.Getenv("WIKI_…") call site in this codebase MUST use
// a constant from this file. Adding a new variable is a single edit here
// plus the call site that consumes it. Searching for the constant name is
// equivalent to grepping for the literal string.
package cli

// ---------------------------------------------------------------------------
// Vault / instance identity (root persistent flags)
// ---------------------------------------------------------------------------

// EnvVaultDir is the path to the Obsidian vault directory.
//
// Default: "/data/vault" (applied in internal/cli/root.go when the variable
// is empty). Surfaced as the root persistent flag --vault; every subcommand
// resolves the vault directory from this single source.
const EnvVaultDir = "WIKI_VAULT_DIR"

// EnvInstanceName is a human-readable identifier for this wiki instance,
// surfaced via the `whoami` MCP tool.
//
// Default: empty — `whoami` omits the field when unset. Honored uniformly
// across every MCP-server surface (embedded via `serve --mcp-port`,
// standalone `serve mcp http`, and `serve mcp stdio`) because it is a root
// persistent flag.
const EnvInstanceName = "WIKI_INSTANCE_NAME"

// ---------------------------------------------------------------------------
// HTTP server
// ---------------------------------------------------------------------------

// EnvPort is the HTTP server listen port.
//
// Default: "8080". Used by `serve` and `serve http`.
const EnvPort = "WIKI_PORT"

// EnvBaseURL is the canonical external base URL of the site (e.g.
// "https://wiki.example.com"). The native renderer uses it for sitemap,
// RSS, and canonical/OpenGraph link generation.
//
// Default: empty (relative links only).
const EnvBaseURL = "WIKI_BASE_URL"

// EnvSiteTitle is the site name the native renderer uses for the header,
// page <title> suffix, and RSS/OpenGraph metadata.
//
// Default: "My Wiki".
const EnvSiteTitle = "WIKI_SITE_TITLE"

// ---------------------------------------------------------------------------
// Filesystem watcher
// ---------------------------------------------------------------------------

// EnvWatch toggles the filesystem watcher that detects external changes
// (Obsidian Sync, git pull, manual edits) and feeds them into the rebuild
// notifier and (if enabled) the webhook dispatch pipeline.
//
// Default: true. Set to "false" or "0" to disable.
const EnvWatch = "WIKI_WATCH"

// EnvWatchExcludeDirs is a comma-separated list of top-level vault
// subdirectories the filesystem watcher skips.
//
// Default: ".obsidian" (Obsidian editor metadata only). raw/ is watched like a
// normal folder. Whitespace or a lone comma disables exclusions entirely;
// empty/unset uses the default.
const EnvWatchExcludeDirs = "WIKI_WATCH_EXCLUDE_DIRS"

// EnvIndexExcludeDirs is a comma-separated list of vault directories that do
// NOT receive a generated index.md during `directory --generate`.
//
// Default (unset): empty — every non-vault-excluded directory gets an index,
// including meta/activity and raw/. Honored by `serve` and the `directory` CLI.
const EnvIndexExcludeDirs = "WIKI_INDEX_EXCLUDE_DIRS"

// EnvIndexNoRecentsDirs is a comma-separated list of vault directories whose
// pages are kept out of every "Recently Updated" section in generated indexes.
//
// Default (unset): "meta/activity" (append-heavy audit logs would otherwise
// dominate vault-wide recents and break Generate's idempotent write-skip).
// Set to whitespace or a lone comma to surface everything. Honored by `serve`
// and the `directory` CLI.
const EnvIndexNoRecentsDirs = "WIKI_INDEX_NORECENTS_DIRS"

// EnvRootDescription is the description written into the root index.md
// frontmatter by `directory --generate`.
//
// Default: "Shared knowledge base". Override to brand the generated index for
// a specific deployment (e.g. "Team knowledge base for Acme Corp").
const EnvRootDescription = "WIKI_ROOT_DESCRIPTION"

// ---------------------------------------------------------------------------
// MCP server
// ---------------------------------------------------------------------------

// EnvMCPPort is the MCP server listen port.
//
// In `serve` / `serve http`: default 0 (disabled). When non-zero, MCP runs
// alongside HTTP in the same process. In `serve mcp http`: default 8081.
const EnvMCPPort = "WIKI_MCP_PORT"

// ---------------------------------------------------------------------------
// OIDC / JWT auth
// ---------------------------------------------------------------------------

// EnvAuthDisabled, when truthy (1/true — case-insensitive), explicitly
// acknowledges running a network server (HTTP REST API / MCP HTTP) with NO
// authentication, exposing read/write/delete/move of the entire vault to
// any client that can reach the listener. Without either EnvAuthIssuer
// (enable auth) or this flag (acknowledge open), the network entry points
// refuse to start — auth fails closed by default.
//
// Default: false. Does not apply to `serve mcp stdio`, which is a local
// subprocess and intentionally runs without auth.
const EnvAuthDisabled = "WIKI_AUTH_DISABLED"

// EnvAuthIssuer is the OIDC issuer URL for JWT auth (e.g. Authentik). When
// set, auth is enabled and protects mutating REST API routes and the MCP
// endpoint.
//
// Default: empty (auth disabled).
const EnvAuthIssuer = "WIKI_AUTH_ISSUER"

// EnvAuthAudience is the expected JWT `aud` claim. Required when
// EnvAuthIssuer is set.
const EnvAuthAudience = "WIKI_AUTH_AUDIENCE"

// EnvAuthAllowedGroups is a comma-separated list of group names; the
// token's `groups` claim must contain at least one to be authorized.
// Required unless EnvAuthAllowAnyUser is "true" (fail-closed default).
const EnvAuthAllowedGroups = "WIKI_AUTH_ALLOWED_GROUPS"

// EnvAuthAllowAnyUser, when "true", permits any authenticated user even
// when EnvAuthAllowedGroups is empty. Explicit opt-in — without it, an
// empty allow-list rejects every request.
//
// Default: false.
const EnvAuthAllowAnyUser = "WIKI_AUTH_ALLOW_ANY_USER"

// EnvAuthResourceMetadataURL is the RFC 9728 Protected Resource Metadata
// URL. When set, 401 responses include a `WWW-Authenticate` header so MCP
// clients can discover the OAuth authorization server.
const EnvAuthResourceMetadataURL = "WIKI_AUTH_RESOURCE_METADATA_URL"

// EnvAuthReads, when "true", extends JWT auth to read-only REST API routes
// (default: writes only).
const EnvAuthReads = "WIKI_AUTH_READS"

// ---------------------------------------------------------------------------
// Webhook dispatch pipeline
// ---------------------------------------------------------------------------

// EnvWebhooksConfig is the path to the webhook dispatcher YAML config.
//
// Semantics:
//   - empty/unset → feature disabled
//   - path set but file missing/unreadable → startup fails (misconfiguration)
//   - valid config → dispatcher wired, inbox.changed events fire on
//     filesystem changes and API/MCP mutations
const EnvWebhooksConfig = "WIKI_WEBHOOKS_CONFIG"

// EnvInboxPollInterval is the cadence of the periodic inbox poll — a
// stat()/mtime fallback that dispatches inbox.changed events for filesystem
// writes the fsnotify watcher misses. This matters when wiki-server and the
// Obsidian-Sync writer run on different kernels sharing an NFS (RWX) volume:
// inotify delivers no cross-client events, but stat mtimes still propagate, so
// a periodic mtime diff catches clipper drops the watcher never sees.
//
// Format: a Go duration (e.g. "60s", "2m"). Default: 60s. A non-positive
// duration ("0", "-1s") disables polling. Only active when the webhook
// dispatch pipeline is enabled (WIKI_WEBHOOKS_CONFIG); without a dispatcher
// there is nothing to feed.
const EnvInboxPollInterval = "WIKI_INBOX_POLL_INTERVAL"

// ---------------------------------------------------------------------------
// Vault freshness observer (Prometheus dead-man's switch)
// ---------------------------------------------------------------------------

// EnvVaultFreshnessInterval is the cadence of the vault freshness scan, which
// exports wiki_vault_last_modified_timestamp_seconds and friends so a
// deployment can alert on "the vault stopped changing" — a sync path that dies
// is otherwise indistinguishable from a quiet week. Unlike the inbox poll this
// runs unconditionally, independent of the webhook dispatch pipeline: a default
// deployment must still emit the signal.
//
// Format: a Go duration (e.g. "5m", "15m"). Default: 5m — deliberately coarser
// than the inbox poll, because a dead-man's switch alerting on "N days" needs
// no finer granularity and a full vault walk over NFS is far more expensive
// than the inbox/-only scan. A non-positive duration ("0", "-1s") disables the
// observer entirely (no metrics registered). See docs/METRICS.md.
const EnvVaultFreshnessInterval = "WIKI_VAULT_FRESHNESS_INTERVAL"

// EnvSyncStatePath is an optional path the freshness observer stats to prove
// the sync process is alive. When set, it also exports
// wiki_sync_state_last_modified_timestamp_seconds — the newest mtime at that
// path. A sync process that touches it on every tick keeps that gauge fresh
// even when the vault itself is quiet, which is what separates "sync alive,
// vault quiet" from "sync dead". Without it, any write to the vault (including
// an agent/API write, which also touches meta/activity/) refreshes the vault
// mtime, so a dead sync can hide behind ordinary activity.
//
// May be either a file or a directory:
//
//   - A file — a heartbeat the sync process touches each tick. This is the form
//     that works when the sync process keeps its own state on a separate
//     ReadWriteOnce volume the server cannot mount: the two sides agree on a
//     heartbeat path on a shared volume instead. Deliberately placed outside
//     the vault, or it would be indexed as wiki content and synced back.
//   - A directory — newest mtime among the sync state DB files within
//     (state.db, state.db-wal), matched at any depth. Only usable where the
//     server can actually read the sync tool's state directory.
//
// The directory form deliberately considers ONLY those state DB files, not
// every file in the tree. obsidian-headless tees its log to sync.log including
// the errors it swallows on each failed poll, so a wedged sync keeps that file
// perpetually fresh; and state.db-shm is recreated on every crash. A gauge
// derived from either would stay fresh through exactly the outage it exists to
// catch. See freshness.DefaultSyncStateFiles.
//
// A path that does not exist is not an error: "the sync process has not written
// its heartbeat yet" is a legitimate state, reported by the gauge being absent
// rather than zero, and it does not increment
// wiki_sync_state_read_errors_total. Only a real read failure (permissions,
// I/O) does.
//
// Default: empty — the gauge is neither registered nor emitted.
const EnvSyncStatePath = "WIKI_SYNC_STATE_PATH"
