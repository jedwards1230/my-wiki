package mcpserver

import (
	"context"

	"github.com/jedwards1230/home-wiki/internal/notify"
	"github.com/jedwards1230/home-wiki/internal/service"
	"github.com/jedwards1230/home-wiki/internal/vault"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Option configures optional MCP server behavior.
type Option func(*options)

type options struct {
	notifier *notify.RebuildNotifier
	pages    *service.PageService
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

// New creates a configured MCP server with all wiki tools registered.
// searchSvc may be nil if search is not configured.
func New(v *vault.Vault, searchSvc *service.SearchService, opts ...Option) *server.MCPServer {
	var cfg options
	for _, o := range opts {
		o(&cfg)
	}
	s := server.NewMCPServer(
		"home-wiki",
		"0.1.0",
		server.WithToolCapabilities(false),
		server.WithResourceCapabilities(false, false),
		server.WithLogging(),
		server.WithInstructions("Home wiki backed by an Obsidian vault. The meta/schema resource is available for context. Page create/update/delete/move mutations are auto-logged as compact audit entries — do NOT call activity for individual page changes. Use activity only for narrative summaries of multi-page work sessions or non-page activities (ingest, lint, note, migrate)."),
	)

	logSvc := service.NewLogService(v.Storage)
	lint := service.NewLintService(v, logSvc)
	directory := service.NewDirectoryService(v)
	activity := service.NewActivityService(v.Storage)
	recent := service.NewRecentService(v)

	var pages *service.PageService
	if cfg.pages != nil {
		pages = cfg.pages
	} else {
		pages = service.NewPageService(v.Storage)
	}

	registerResources(s, pages)
	registerTools(s, v.Dir, cfg.notifier, lint, directory, activity, pages, recent, searchSvc)

	return s
}

// registerResources exposes wiki content as MCP resources.
func registerResources(s *server.MCPServer, pages *service.PageService) {
	s.AddResource(
		mcp.NewResource(
			"wiki://schema",
			"Wiki Schema",
			mcp.WithResourceDescription("Operating manual for AI agents — page conventions, frontmatter rules, tag taxonomy, ingestion workflows, and activity logging format."),
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
	notifier *notify.RebuildNotifier,
	lint *service.LintService,
	directory *service.DirectoryService,
	activity *service.ActivityService,
	pages *service.PageService,
	recent *service.RecentService,
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
			mcp.WithDescription("List wiki pages (excludes raw/, private/, .obsidian/). With detail=false (default), returns JSON array of {path, title, has_meta}. With detail=true, returns rich entries with {path, title, description, tags} from frontmatter."),
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
			mcp.WithDescription("Delete a wiki page. Returns an error if the page does not exist."),
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

	// --- recent: Recently modified pages ---
	s.AddTool(
		mcp.NewTool("recent",
			mcp.WithTitleAnnotation("Recent Pages"),
			mcp.WithDescription("List recently modified wiki pages sorted by modification time. Returns JSON array of {path, title, modified}. Excludes activity log files."),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(true),
			mcp.WithOpenWorldHintAnnotation(false),
			mcp.WithNumber("limit",
				mcp.Description("Maximum pages to return. Default: 20."),
			),
		),
		recentHandler(recent),
	)

	// --- activity: Append to activity log ---
	s.AddTool(
		mcp.NewTool("activity",
			mcp.WithTitleAnnotation("Log Activity"),
			mcp.WithDescription("Append a narrative entry to today's activity log. Individual page mutations (create/edit/delete/move) are auto-logged — do NOT duplicate them here. Use this for summaries of multi-page work sessions or non-page activities like ingest, lint, or migrate."),
			mcp.WithReadOnlyHintAnnotation(false),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(false),
			mcp.WithOpenWorldHintAnnotation(false),
			mcp.WithString("type",
				mcp.Required(),
				mcp.Enum("ingest", "edit", "create", "delete", "lint", "note", "migrate", "move"),
				mcp.Description("Activity type: ingest (raw source processed), edit (page modified), create (new page), delete (page removed), lint (health check run), note (general observation), migrate (structural change), move (page relocated)."),
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
