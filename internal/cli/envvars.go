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

// EnvPublicDir is the path to the Quartz static output directory served
// at "/".
//
// Default: "/data/public". The directory is watched (or loaded into memory
// when EnvInMemoryHTML is truthy) for changes triggered by Quartz rebuilds.
const EnvPublicDir = "WIKI_PUBLIC_DIR"

// EnvQuartzDir is the path to the Quartz project directory. When set, the
// Go server triggers one-shot Quartz builds after debounced filesystem
// changes (replacing Quartz's built-in --watch mode).
//
// Default: empty — Quartz build triggering is disabled and the server only
// serves whatever already exists under EnvPublicDir.
const EnvQuartzDir = "WIKI_QUARTZ_DIR"

// EnvInMemoryHTML, when truthy (1/true/yes — case-insensitive), causes the
// server to load EnvPublicDir into an atomically-swappable in-memory fs.FS
// and serve from there; fsnotify drives debounced reloads on Quartz
// rebuilds. Eliminates the mid-rebuild 404 window at the cost of adding the
// public tree's size to RSS.
//
// Default: false (serve directly from disk via os.DirFS).
const EnvInMemoryHTML = "WIKI_IN_MEMORY_HTML"

// EnvRenderer selects the HTML renderer used for the served wiki:
//
//   - "quartz" (default): Quartz v4 produces static HTML on disk under
//     EnvPublicDir; the Go server serves those files unchanged. This is
//     the production-stable path.
//   - "native": the in-process Go renderer (internal/render) compiles the
//     vault into an in-memory snapshot on startup and after each debounced
//     vault change. EnvPublicDir is unused (memory-only).
//
// Surfaced as --renderer on `serve` and `serve http`. The Helm chart
// drives this via .Values.renderer; flipping the value is the documented
// rollback for the native renderer. See docs/RENDERER.md.
const EnvRenderer = "WIKI_RENDERER"

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
