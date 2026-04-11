package mcpserver

import (
	"context"

	"github.com/jedwards1230/home-wiki/internal/service"
	"github.com/jedwards1230/home-wiki/internal/vault"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// New creates a configured MCP server with all wiki tools registered.
// searchSvc may be nil if search is not configured.
func New(v *vault.Vault, searchSvc *service.SearchService) *server.MCPServer {
	s := server.NewMCPServer(
		"home-wiki",
		"0.1.0",
		server.WithToolCapabilities(false),
		server.WithResourceCapabilities(false, false),
		server.WithLogging(),
		server.WithInstructions("Home wiki backed by an Obsidian vault. The meta/schema resource is available for context. Log all work with wiki_activity when done."),
	)

	lint := service.NewLintService(v)
	ingest := service.NewIngestService(v)
	directory := service.NewDirectoryService(v)
	logSvc := service.NewLogService(v.Dir)
	activity := service.NewActivityService(v.Dir)
	pages := service.NewPageService(v.Dir)
	recent := service.NewRecentService(v)

	registerResources(s, pages)
	registerTools(s, lint, ingest, directory, logSvc, activity, pages, recent, searchSvc)

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
	lint *service.LintService,
	ingest *service.IngestService,
	directory *service.DirectoryService,
	logSvc *service.LogService,
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
				mcp.Enum("all", "frontmatter", "raw", "links", "orphans"),
				mcp.Description("Which check to run: frontmatter (required fields), raw (raw source metadata), links (broken wikilinks), orphans (no inbound links), all (everything). Default: all."),
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
		mcp.NewTool("wiki_log",
			mcp.WithTitleAnnotation("View Log Index"),
			mcp.WithDescription("List daily summaries from meta/log.md — each entry has date, change count, hash, and title. Use wiki_log_day to see a specific day's entries."),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(true),
			mcp.WithOpenWorldHintAnnotation(false),
			mcp.WithNumber("n",
				mcp.Description("Return only the last N days. Default (0): return all."),
			),
		),
		logIndexHandler(logSvc),
	)

	s.AddTool(
		mcp.NewTool("wiki_log_day",
			mcp.WithTitleAnnotation("View Day Log"),
			mcp.WithDescription("Get activity entries for a single day (type, time, title). Use wiki_log first to find dates with activity."),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(true),
			mcp.WithOpenWorldHintAnnotation(false),
			mcp.WithString("date",
				mcp.Required(),
				mcp.Description("Date in YYYY-MM-DD format."),
			),
			mcp.WithBoolean("detail",
				mcp.Description("Include full entry body/summary text. Default: false (headers only)."),
			),
		),
		logDayHandler(logSvc),
	)

	s.AddTool(
		mcp.NewTool("wiki_log_lint",
			mcp.WithTitleAnnotation("Lint Log"),
			mcp.WithDescription("Check activity log integrity: hash mismatches between index and daily files, orphaned files, missing frontmatter. Returns JSON array of {message}. For vault-wide checks, use wiki_lint instead."),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(true),
			mcp.WithOpenWorldHintAnnotation(false),
		),
		logLintHandler(logSvc),
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
				mcp.Enum("ingest", "edit", "create", "lint", "note", "migrate"),
				mcp.Description("Activity type: ingest (raw source processed), edit (page modified), create (new page), lint (health check run), note (general observation), migrate (structural change)."),
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
		activityHandler(s, activity),
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
		createPageHandler(s, pages),
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
		updatePageHandler(s, pages),
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
		deletePageHandler(s, pages),
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
		patchPageHandler(s, pages),
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
