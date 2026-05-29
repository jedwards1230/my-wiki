package mcpserver

import (
	"context"

	"github.com/jedwards1230/my-wiki/internal/notify"
	"github.com/jedwards1230/my-wiki/internal/service"
	"github.com/jedwards1230/my-wiki/internal/vault"
	"github.com/jedwards1230/my-wiki/internal/version"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Option configures optional MCP server behavior.
type Option func(*options)

type options struct {
	notifier     *notify.RebuildNotifier
	pages        *service.PageService
	instanceName string
}

// WithRebuildNotifier sets a notifier called after successful vault mutations.
func WithRebuildNotifier(n *notify.RebuildNotifier) Option {
	return func(o *options) {
		o.notifier = n
	}
}

// WithPageService provides a pre-configured PageService.
func WithPageService(ps *service.PageService) Option {
	return func(o *options) {
		o.pages = ps
	}
}

// WithInstanceName sets a human-readable identifier for this wiki instance
// (e.g. "work-wiki", "my-wiki"). When set, it is included in the whoami
// tool response so clients can distinguish between multiple wiki instances.
// When empty (the default), whoami omits the field for backwards compatibility.
func WithInstanceName(name string) Option {
	return func(o *options) {
		o.instanceName = name
	}
}

// New creates a configured MCP server with all wiki tools registered.
// searchSvc may be nil if search is not configured.
func New(v *vault.Vault, searchSvc *service.SearchService, opts ...Option) *server.MCPServer {
	var cfg options
	for _, o := range opts {
		o(&cfg)
	}
	s := server.NewMCPServer(
		"my-wiki",
		version.Value,
		server.WithToolCapabilities(false),
		server.WithResourceCapabilities(false, false),
		server.WithLogging(),
		server.WithInstructions("Home wiki backed by an Obsidian vault. The meta/schema resource is available for context. Page create/update/delete/move mutations are auto-logged as compact audit entries — do NOT call activity for individual page changes. Use activity only for narrative summaries of multi-page work sessions or non-page activities (lint, note, migrate)."),
	)

	logSvc := service.NewLogService(v.Storage)
	lint := service.NewLintService(v, logSvc)
	directory := service.NewDirectoryService(v)
	activity := service.NewActivityService(v.Storage)
	tags := service.NewTagService(v)

	var pages *service.PageService
	if cfg.pages != nil {
		pages = cfg.pages
	} else {
		pages = service.NewPageService(v.Storage)
	}

	registerResources(s, pages)
	registerTools(s, v.Dir, cfg.instanceName, cfg.notifier, lint, directory, activity, pages, tags, searchSvc)

	return s
}

// registerResources exposes wiki content as MCP resources.
func registerResources(s *server.MCPServer, pages *service.PageService) {
	s.AddResource(
		mcp.NewResource(
			"wiki://schema",
			"Wiki Schema",
			mcp.WithResourceDescription("Operating manual for AI agents — page conventions, frontmatter rules, and activity logging format."),
			mcp.WithMIMEType("text/markdown"),
		),
		func(_ context.Context, _ mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			content, err := pages.Read("meta/schema")
			if err != nil {
				return nil, err
			}
			return []mcp.ResourceContents{
				mcp.TextResourceContents{
					URI:      "wiki://schema",
					MIMEType: "text/markdown",
					Text:     content,
				},
			}, nil
		},
	)
}

// NewStreamableHTTPServer creates a stateless streamable HTTP transport.
func NewStreamableHTTPServer(s *server.MCPServer) *server.StreamableHTTPServer {
	return server.NewStreamableHTTPServer(s,
		server.WithStateLess(true),
	)
}

func registerTools(
	s *server.MCPServer,
	vaultDir string,
	instanceName string,
	notifier *notify.RebuildNotifier,
	lint *service.LintService,
	directory *service.DirectoryService,
	activity *service.ActivityService,
	pages *service.PageService,
	tags *service.TagService,
	searchSvc *service.SearchService,
) {
	// --- read: Read a wiki page ---
	s.AddTool(
		mcp.NewTool("read",
			mcp.WithTitleAnnotation("Read Page"),
			mcp.WithDescription("Read a wiki page's full markdown content including frontmatter. The .md extension is added automatically if omitted."),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(true),
			mcp.WithOpenWorldHintAnnotation(false),
			mcp.WithString("path",
				mcp.Required(),
				mcp.Description("Relative path within the vault (e.g., meta/schema or meta/schema.md)."),
			),
		),
		readHandler(pages),
	)

	// --- write: Create or update a wiki page ---
	s.AddTool(
		mcp.NewTool("write",
			mcp.WithTitleAnnotation("Write Page"),
			mcp.WithDescription("Create or update a wiki page. Frontmatter is assembled from structured parameters — do NOT embed YAML frontmatter in the content field. If the page exists it is overwritten; if it does not exist it is created."),
			mcp.WithReadOnlyHintAnnotation(false),
			mcp.WithDestructiveHintAnnotation(true),
			mcp.WithIdempotentHintAnnotation(true),
			mcp.WithOpenWorldHintAnnotation(false),
			mcp.WithString("path",
				mcp.Required(),
				mcp.Description("Relative path for the page (e.g., project/my-project). The .md extension is added if omitted."),
			),
			mcp.WithString("title",
				mcp.Required(),
				mcp.Description("Page title for frontmatter."),
			),
			mcp.WithArray("tags",
				mcp.Required(),
				mcp.Description("Page tags for frontmatter."),
				mcp.Items(map[string]any{"type": "string"}),
			),
			mcp.WithString("content",
				mcp.Required(),
				mcp.Description("Body content in markdown. Do NOT include YAML frontmatter — it is generated from the other parameters."),
			),
			mcp.WithString("date",
				mcp.Description("Creation date in YYYY-MM-DD format. Defaults to today if omitted."),
			),
			mcp.WithString("description",
				mcp.Description("One-line summary for directory index. Omit if not needed."),
			),
			mcp.WithString("extra_frontmatter",
				mcp.Description("Raw YAML lines for arbitrary frontmatter fields (e.g., 'status: wip\\nsource: https://...'). Inserted before the closing ---. Omit if not needed."),
			),
		),
		writeHandler(s, pages, lint, vaultDir, notifier),
	)

	// --- edit: Surgical partial update ---
	s.AddTool(
		mcp.NewTool("edit",
			mcp.WithTitleAnnotation("Edit Page"),
			mcp.WithDescription("Apply targeted find-and-replace edits to an existing wiki page without replacing the entire content. Each operation replaces the first occurrence of 'find' with 'replace'. If any find string is not found, the operation fails with no changes written."),
			mcp.WithReadOnlyHintAnnotation(false),
			mcp.WithDestructiveHintAnnotation(true),
			mcp.WithIdempotentHintAnnotation(false),
			mcp.WithOpenWorldHintAnnotation(false),
			mcp.WithString("path",
				mcp.Required(),
				mcp.Description("Relative path to the page to edit (e.g., project/my-project or project/my-project.md). The .md extension is added if omitted."),
			),
			mcp.WithArray("operations",
				mcp.Required(),
				mcp.Description("Array of find-and-replace operations to apply in order."),
				mcp.Items(map[string]any{
					"type": "object",
					"properties": map[string]any{
						"find":    map[string]any{"type": "string", "description": "Text to find in the page."},
						"replace": map[string]any{"type": "string", "description": "Text to replace it with."},
					},
					"required": []string{"find", "replace"},
				}),
			),
		),
		editHandler(s, pages, lint, vaultDir, notifier),
	)

	// --- list: List pages with optional detail ---
	s.AddTool(
		mcp.NewTool("list",
			mcp.WithTitleAnnotation("List Pages"),
			mcp.WithDescription("List wiki pages (excludes raw/, private/, .obsidian/). With detail=false (default), returns {path, title, has_meta} per page. With detail=true, returns rich entries with {path, title, description, tags} from frontmatter. Use sort_by='modified' with limit to get recently changed pages."),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(true),
			mcp.WithOpenWorldHintAnnotation(false),
			mcp.WithString("prefix",
				mcp.Description("Filter to pages under this directory (e.g., 'project/' or 'meta/'). Default: list all pages."),
			),
			mcp.WithBoolean("detail",
				mcp.Description("When true, return rich directory entries with description and tags from frontmatter. Default: false."),
			),
			mcp.WithString("sort_by",
				mcp.Enum("name", "modified"),
				mcp.Description("Sort order: 'name' (default, alphabetical) or 'modified' (newest first, includes modified timestamp). When 'modified', activity log files are excluded."),
			),
			mcp.WithNumber("limit",
				mcp.Description("Maximum pages to return. Default: unlimited. Useful with sort_by='modified' to get recent pages."),
			),
		),
		listHandler(pages, directory),
	)

	// --- search: Full-text search ---
	if searchSvc != nil {
		s.AddTool(
			mcp.NewTool("search",
				mcp.WithTitleAnnotation("Search Wiki"),
				mcp.WithDescription("Full-text search across wiki pages. Matches against title, tags, and content. Returns results ranked by relevance with snippets and timing. Use engine='all' to compare search backends side-by-side."),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("query",
					mcp.Required(),
					mcp.Description("Search query (minimum 2 characters)."),
				),
				mcp.WithNumber("limit",
					mcp.Description("Maximum results per engine. Default: 20."),
				),
				mcp.WithString("engine",
					mcp.Enum("substring", "index", "all"),
					mcp.Description("Search engine: 'substring' (default, walks files), 'index' (inverted index with TF-IDF), 'all' (run both, compare timing)."),
				),
			),
			searchHandler(searchSvc),
		)
	}

	// --- delete: Remove a page ---
	s.AddTool(
		mcp.NewTool("delete",
			mcp.WithTitleAnnotation("Delete Page"),
			mcp.WithDescription("Delete a wiki page. Returns an error if the page does not exist. Returns lint warnings about broken wikilinks caused by the deletion."),
			mcp.WithReadOnlyHintAnnotation(false),
			mcp.WithDestructiveHintAnnotation(true),
			mcp.WithIdempotentHintAnnotation(false),
			mcp.WithOpenWorldHintAnnotation(false),
			mcp.WithString("path",
				mcp.Required(),
				mcp.Description("Relative path to the page to delete (e.g., project/old-page or project/old-page.md). The .md extension is added if omitted."),
			),
		),
		deleteHandler(s, pages, lint, vaultDir, notifier),
	)

	// --- move: Rename/relocate a page ---
	s.AddTool(
		mcp.NewTool("move",
			mcp.WithTitleAnnotation("Move Page"),
			mcp.WithDescription("Rename or relocate a wiki page. Fails if the source does not exist or the destination already exists. Returns lint warnings about broken wikilinks caused by the move."),
			mcp.WithReadOnlyHintAnnotation(false),
			mcp.WithDestructiveHintAnnotation(true),
			mcp.WithIdempotentHintAnnotation(false),
			mcp.WithOpenWorldHintAnnotation(false),
			mcp.WithString("source",
				mcp.Required(),
				mcp.Description("Current relative path of the page (e.g., project/old-name). The .md extension is added if omitted."),
			),
			mcp.WithString("destination",
				mcp.Required(),
				mcp.Description("New relative path for the page (e.g., project/new-name). The .md extension is added if omitted."),
			),
		),
		moveHandler(s, pages, lint, vaultDir, notifier),
	)

	// --- lint: Run vault health checks ---
	s.AddTool(
		mcp.NewTool("lint",
			mcp.WithTitleAnnotation("Lint Vault"),
			mcp.WithDescription("Run vault-wide mechanical health checks. Returns issues grouped by check. These are structural/metadata checks only — content-level issues (stale facts, contradictions, outdated references) require manual review or the semantic lint layer.\n\nChecks:\n- frontmatter: required fields (title, tags, date) on wiki pages; skips generated pages\n- tags: validates page tags against the taxonomy in meta/schema; flags unused domains and under-threshold tags\n- links: broken [[wikilinks]] — deduplicates by target, lists all source files per missing page\n- orphans: pages with no inbound wikilinks\n- size: pages exceeding 1000 words\n- log: hash mismatches between meta/log.md index and daily activity files"),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(true),
			mcp.WithOpenWorldHintAnnotation(false),
			mcp.WithString("check",
				mcp.Description("Which check to run. Default: all."),
				mcp.Enum("all", "frontmatter", "tags", "links", "orphans", "size", "log"),
			),
		),
		lintHandler(lint),
	)

	// --- tags: List all tags in use ---
	s.AddTool(
		mcp.NewTool("tags",
			mcp.WithTitleAnnotation("List Tags"),
			mcp.WithDescription("List all tags used across wiki pages with page counts, sorted by frequency. Use to discover existing tags before creating new pages."),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(true),
			mcp.WithOpenWorldHintAnnotation(false),
		),
		tagsHandler(tags),
	)

	// --- whoami: Server identity ---
	s.AddTool(
		mcp.NewTool("whoami",
			mcp.WithTitleAnnotation("Server Info"),
			mcp.WithDescription("Returns server identity: name, version, vault directory, and Go runtime version. Useful for verifying which wiki instance you're connected to."),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(true),
			mcp.WithOpenWorldHintAnnotation(false),
		),
		whoamiHandler(vaultDir, instanceName),
	)

	// --- activity: Append to activity log ---
	s.AddTool(
		mcp.NewTool("activity",
			mcp.WithTitleAnnotation("Log Activity"),
			mcp.WithDescription("Append a narrative entry to today's activity log. Individual page mutations (create/edit/delete/move) are auto-logged — do NOT duplicate them here. Use this for summaries of multi-page work sessions or non-page activities like lint, note, or migrate."),
			mcp.WithReadOnlyHintAnnotation(false),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(false),
			mcp.WithOpenWorldHintAnnotation(false),
			mcp.WithString("type",
				mcp.Required(),
				mcp.Enum("edit", "create", "delete", "lint", "note", "migrate", "move"),
				mcp.Description("Activity type: edit (page modified), create (new page), delete (page removed), lint (health check run), note (general observation), migrate (structural change), move (page relocated)."),
			),
			mcp.WithString("title",
				mcp.Required(),
				mcp.Description("Short title for the activity entry."),
			),
			mcp.WithString("time",
				mcp.Description("Override timestamp in HH:MM format. Default: current time."),
			),
			mcp.WithString("summary",
				mcp.Description("Optional description of what was done."),
			),
			mcp.WithArray("touched",
				mcp.Description("Wiki pages related to this activity (e.g., project/foo). Optional."),
				mcp.Items(map[string]any{"type": "string"}),
			),
		),
		activityHandler(s, activity, vaultDir, notifier),
	)
}
