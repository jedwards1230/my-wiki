package mcpserver

import (
	"github.com/jedwards1230/home-wiki/internal/service"
	"github.com/jedwards1230/home-wiki/internal/vault"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// New creates a configured MCP server with all wiki tools registered.
func New(v *vault.Vault) *server.MCPServer {
	s := server.NewMCPServer(
		"home-wiki",
		"0.1.0",
		server.WithToolCapabilities(true),
	)

	lint := service.NewLintService(v)
	queue := service.NewQueueService(v)
	logSvc := service.NewLogService(v.Dir)
	activity := service.NewActivityService(v.Dir)
	pages := service.NewPageService(v.Dir)

	registerTools(s, lint, queue, logSvc, activity, pages)

	return s
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
	queue *service.QueueService,
	logSvc *service.LogService,
	activity *service.ActivityService,
	pages *service.PageService,
) {
	s.AddTool(
		mcp.NewTool("wiki_lint",
			mcp.WithDescription("Run health checks on the wiki vault (frontmatter, links, orphans, raw sources)"),
			mcp.WithString("check",
				mcp.Enum("all", "frontmatter", "raw", "links", "orphans"),
				mcp.Description("Which check to run (default: all)"),
			),
		),
		lintHandler(lint),
	)

	s.AddTool(
		mcp.NewTool("wiki_queue",
			mcp.WithDescription("List unprocessed raw source files that need ingestion"),
		),
		queueListHandler(queue),
	)

	s.AddTool(
		mcp.NewTool("wiki_queue_generate",
			mcp.WithDescription("Regenerate the meta/ingest-queue.md file from unprocessed raw sources"),
		),
		queueGenerateHandler(queue),
	)

	s.AddTool(
		mcp.NewTool("wiki_log",
			mcp.WithDescription("View the wiki activity log index"),
			mcp.WithNumber("n",
				mcp.Description("Show last N entries (0 for all)"),
			),
		),
		logIndexHandler(logSvc),
	)

	s.AddTool(
		mcp.NewTool("wiki_log_day",
			mcp.WithDescription("View activity entries for a specific date"),
			mcp.WithString("date",
				mcp.Required(),
				mcp.Description("Date in YYYY-MM-DD format"),
			),
			mcp.WithBoolean("detail",
				mcp.Description("Include full entry content (default: false)"),
			),
		),
		logDayHandler(logSvc),
	)

	s.AddTool(
		mcp.NewTool("wiki_log_lint",
			mcp.WithDescription("Lint the activity log for issues (hash mismatches, missing files)"),
		),
		logLintHandler(logSvc),
	)

	s.AddTool(
		mcp.NewTool("wiki_activity",
			mcp.WithDescription("Append a structured entry to today's activity log"),
			mcp.WithString("type",
				mcp.Required(),
				mcp.Enum("ingest", "edit", "create", "lint", "note", "migrate"),
				mcp.Description("Activity type"),
			),
			mcp.WithString("title",
				mcp.Required(),
				mcp.Description("Title of the activity"),
			),
			mcp.WithString("time",
				mcp.Description("Override timestamp (HH:MM format, default: current time)"),
			),
			mcp.WithString("summary",
				mcp.Description("Description of what was done"),
			),
		),
		activityHandler(activity),
	)

	s.AddTool(
		mcp.NewTool("wiki_read_page",
			mcp.WithDescription("Read the content of a wiki page"),
			mcp.WithString("path",
				mcp.Required(),
				mcp.Description("Relative path to the page (e.g., meta/schema.md)"),
			),
		),
		readPageHandler(pages),
	)

	s.AddTool(
		mcp.NewTool("wiki_create_page",
			mcp.WithDescription("Create a new wiki page"),
			mcp.WithString("path",
				mcp.Required(),
				mcp.Description("Relative path for the new page"),
			),
			mcp.WithString("content",
				mcp.Required(),
				mcp.Description("Full markdown content including frontmatter"),
			),
		),
		createPageHandler(pages),
	)

	s.AddTool(
		mcp.NewTool("wiki_update_page",
			mcp.WithDescription("Update an existing wiki page"),
			mcp.WithString("path",
				mcp.Required(),
				mcp.Description("Relative path to the page"),
			),
			mcp.WithString("content",
				mcp.Required(),
				mcp.Description("New full markdown content including frontmatter"),
			),
		),
		updatePageHandler(pages),
	)

	s.AddTool(
		mcp.NewTool("wiki_list_pages",
			mcp.WithDescription("List wiki pages, optionally filtered by path prefix"),
			mcp.WithString("prefix",
				mcp.Description("Path prefix to filter by (e.g., project/)"),
			),
		),
		listPagesHandler(pages),
	)

	s.AddTool(
		mcp.NewTool("wiki_search",
			mcp.WithDescription("Search wiki pages (not yet implemented)"),
			mcp.WithString("query",
				mcp.Required(),
				mcp.Description("Search query"),
			),
		),
		searchHandler(),
	)
}
