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
		server.WithInstructions("Home wiki backed by an Obsidian vault. The meta/schema resource is available for context. Page create/update/delete operations are automatically logged. Use wiki_activity for non-page activities (ingest, lint, note, migrate)."),
	)

	logSvc := service.NewLogService(v.Storage)
	lint := service.NewLintService(v, logSvc)
	ingest := service.NewIngestService(v)
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
	registerTools(s, v.Dir, cfg.notifier, lint, ingest, directory, activity, pages, recent, searchSvc)

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
	ingest *service.IngestService,
	directory *service.DirectoryService,
	activity *service.ActivityService,
	pages *service.PageService,
	recent *service.RecentService,
	searchSvc *service.SearchService,
) {
	s.AddTool(
		mcp.NewTool("wiki_lint",
			mcp.WithTitleAnnotation("Lint Wiki"),
			mcp.WithDescription("Run vault health checks. Returns JSON with issues (file, check, level, message) and total count."),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(true),
			mcp.WithOpenWorldHintAnnotation(false),
			mcp.WithString("check",
				mcp.Enum("all", "frontmatter", "raw", "links", "orphans", "log"),
				mcp.Description("Which check to run: frontmatter (required fields), raw (raw source metadata), links (broken wikilinks), orphans (no inbound links), log (activity log integrity), all (everything). Default: all."),
			),
		),
		lintHandler(lint),
	)

	s.AddTool(
		mcp.NewTool("wiki_ingest",
			mcp.WithTitleAnnotation("List Ingest Queue"),
			mcp.WithDescription("List raw/ files missing the 'ingested' frontmatter key — source documents awaiting summarization into wiki pages. Returns JSON array of {path, title, date_added}."),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(true),
			mcp.WithOpenWorldHintAnnotation(false),
		),
		ingestListHandler(ingest),
	)

	s.AddTool(
		mcp.NewTool("wiki_directory",
			mcp.WithTitleAnnotation("List Page Directory"),
			mcp.WithDescription("List all wiki pages with title, description, and tags. Returns JSON array of {path, title, description, tags}. Use wiki_directory_generate to write a browsable markdown page."),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(true),
			mcp.WithOpenWorldHintAnnotation(false),
		),
		directoryListHandler(directory),
	)

	s.AddTool(
		mcp.NewTool("wiki_directory_generate",
			mcp.WithTitleAnnotation("Generate Page Directory"),
			mcp.WithDescription("Regenerate index.md with all wiki pages grouped by tag. Serves as both homepage and agent catalog. Use wiki_directory to read the list without side effects. Returns {path, count}."),
			mcp.WithReadOnlyHintAnnotation(false),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(true),
			mcp.WithOpenWorldHintAnnotation(false),
		),
		directoryGenerateHandler(s, directory),
	)

	s.AddTool(
		mcp.NewTool("wiki_ingest_generate",
			mcp.WithTitleAnnotation("Generate Ingest Queue"),
			mcp.WithDescription("Write meta/ingest-queue.md with a table of unprocessed raw sources. Use wiki_ingest to read the list without side effects. Returns {path, count}."),
			mcp.WithReadOnlyHintAnnotation(false),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(true),
			mcp.WithOpenWorldHintAnnotation(false),
		),
		ingestGenerateHandler(s, ingest),
	)

	s.AddTool(
		mcp.NewTool("wiki_activity",
			mcp.WithTitleAnnotation("Log Activity"),
			mcp.WithDescription("Append an entry to today's activity log and update meta/log.md index. Call after completing wiki work to maintain the audit trail."),
			mcp.WithReadOnlyHintAnnotation(false),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(false),
			mcp.WithOpenWorldHintAnnotation(false),
			mcp.WithString("type",
				mcp.Required(),
				mcp.Enum("ingest", "edit", "create", "delete", "lint", "note", "migrate"),
				mcp.Description("Activity type: ingest (raw source processed), edit (page modified), create (new page), delete (page removed), lint (health check run), note (general observation), migrate (structural change)."),
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

	s.AddTool(
		mcp.NewTool("wiki_read_page",
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
		readPageHandler(pages),
	)

	s.AddTool(
		mcp.NewTool("wiki_create_page",
			mcp.WithTitleAnnotation("Create Page"),
			mcp.WithDescription("Create a new wiki page. Fails if the page already exists — use wiki_update_page to modify existing pages."),
			mcp.WithReadOnlyHintAnnotation(false),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(false),
			mcp.WithOpenWorldHintAnnotation(false),
			mcp.WithString("path",
				mcp.Required(),
				mcp.Description("Relative path for the new page (e.g., project/my-project). The .md extension is added if omitted."),
			),
			mcp.WithString("content",
				mcp.Required(),
				mcp.Description("Full markdown content. Should include YAML frontmatter with title, tags, and date fields."),
			),
		),
		createPageHandler(s, pages, vaultDir, notifier),
	)

	s.AddTool(
		mcp.NewTool("wiki_update_page",
			mcp.WithTitleAnnotation("Update Page"),
			mcp.WithDescription("Overwrite an existing wiki page. Fails if the page does not exist — use wiki_create_page for new pages. Read the page first with wiki_read_page, then send the complete updated content."),
			mcp.WithReadOnlyHintAnnotation(false),
			mcp.WithDestructiveHintAnnotation(true),
			mcp.WithIdempotentHintAnnotation(true),
			mcp.WithOpenWorldHintAnnotation(false),
			mcp.WithString("path",
				mcp.Required(),
				mcp.Description("Relative path to the page."),
			),
			mcp.WithString("content",
				mcp.Required(),
				mcp.Description("Complete replacement markdown content including YAML frontmatter."),
			),
		),
		updatePageHandler(s, pages, vaultDir, notifier),
	)

	s.AddTool(
		mcp.NewTool("wiki_delete_page",
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
		deletePageHandler(s, pages, vaultDir, notifier),
	)

	s.AddTool(
		mcp.NewTool("wiki_patch_page",
			mcp.WithTitleAnnotation("Patch Page"),
			mcp.WithDescription("Apply targeted find-and-replace edits to an existing wiki page without replacing the entire content. Each operation replaces the first occurrence of 'find' with 'replace'. If any find string is not found, the operation fails with no changes written."),
			mcp.WithReadOnlyHintAnnotation(false),
			mcp.WithDestructiveHintAnnotation(true),
			mcp.WithIdempotentHintAnnotation(false),
			mcp.WithOpenWorldHintAnnotation(false),
			mcp.WithString("path",
				mcp.Required(),
				mcp.Description("Relative path to the page to patch (e.g., project/my-project or project/my-project.md). The .md extension is added if omitted."),
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
		patchPageHandler(s, pages, vaultDir, notifier),
	)

	s.AddTool(
		mcp.NewTool("wiki_list_pages",
			mcp.WithTitleAnnotation("List Pages"),
			mcp.WithDescription("List wiki pages (excludes raw/, private/, .obsidian/). Returns JSON array of {path, title, has_meta}."),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(true),
			mcp.WithOpenWorldHintAnnotation(false),
			mcp.WithString("prefix",
				mcp.Description("Filter to pages under this directory (e.g., 'project/' or 'meta/'). Default: list all pages."),
			),
		),
		listPagesHandler(pages),
	)

	s.AddTool(
		mcp.NewTool("wiki_recent",
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
		recentListHandler(recent),
	)

	if searchSvc != nil {
		s.AddTool(
			mcp.NewTool("wiki_search",
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
}
