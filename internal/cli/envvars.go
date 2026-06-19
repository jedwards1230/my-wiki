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
// Default: ".obsidian,raw,private" (Obsidian metadata, raw byte storage,
// device-only private folder). Whitespace or a lone comma disables
// exclusions entirely; empty/unset uses the default.
const EnvWatchExcludeDirs = "WIKI_WATCH_EXCLUDE_DIRS"

// EnvIndexExcludeDirs is a comma-separated list of vault directories that do
// NOT receive a generated index.md during `directory --generate`.
//
// Default (unset): empty — every non-vault-excluded directory gets an index,
// including meta/activity. Honored by `serve` and the `directory` CLI.
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
// Admin panel (/_/admin) — browser OIDC login
// ---------------------------------------------------------------------------

// EnvAuthAdminGroups is a comma-separated list of group names authorized to
// use the admin panel (/_/admin). It is a SEPARATE, narrower allow-list from
// EnvAuthAllowedGroups: a token's `groups` claim must contain at least one of
// these to reach any admin route.
//
// Required when the admin login gate is active (i.e. EnvAuthIssuer is set).
// Empty → the admin panel is not registered and every /_/admin/* path 404s.
const EnvAuthAdminGroups = "WIKI_AUTH_ADMIN_GROUPS"

// EnvAuthClientID is the OAuth2 client ID of the confidential WEB client used
// by the admin panel's browser login (Authorization Code + PKCE). It is the
// expected `aud` of the ID token minted for the browser session.
//
// This is distinct from EnvAuthAudience, which is the access-token audience
// validated for the Bearer-authenticated REST API and MCP surfaces. If you
// reuse a single OAuth client for both, set this to that client's ID.
//
// Required when the admin login gate is active. Default: empty.
const EnvAuthClientID = "WIKI_AUTH_CLIENT_ID"

// EnvAuthClientSecret is the OAuth2 client secret paired with EnvAuthClientID,
// used to exchange the authorization code for tokens at the IdP's token
// endpoint. Required when the admin login gate is active. Default: empty.
const EnvAuthClientSecret = "WIKI_AUTH_CLIENT_SECRET"

// EnvAdminSessionKey is the secret used to authenticate-and-encrypt the admin
// session cookie (AES-256-GCM; the AES key is SHA-256(secret)). Each key must
// be at least 32 bytes.
//
// To rotate without logging everyone out, provide a comma-separated list:
// new cookies are signed with the FIRST key, and decryption is attempted
// against every key in order. Required when the admin login gate is active;
// an empty or too-short key fails closed at startup. Must be identical across
// replicas (put it in the shared Helm secret).
const EnvAdminSessionKey = "WIKI_ADMIN_SESSION_KEY"

// EnvAdminDevInsecure, when truthy (1/true — case-insensitive), registers the
// admin panel WITHOUT the OIDC login gate, but ONLY when auth is otherwise
// disabled (EnvAuthIssuer unset). It synthesizes a local "dev-admin" identity
// so the panel renders for local development and visual verification without
// standing up an IdP, and logs a prominent warning at startup.
//
// Truth table (auth = EnvAuthIssuer set; opt-in = this var truthy):
//   - auth on,  opt-in ignored → admin registered WITH OIDC login gate
//   - auth off, opt-in absent  → admin NOT registered (all /_/admin/* 404)
//   - auth off, opt-in present → admin registered WITHOUT gate (dev only)
//
// Never set this on a network-facing deployment. Default: false.
const EnvAdminDevInsecure = "WIKI_ADMIN_DEV_INSECURE"

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
