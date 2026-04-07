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
		server.WithToolCapabilities(false),
		server.WithInstructions("Home wiki backed by an Obsidian vault. Read meta/schema with wiki_read_page before making changes. Log all work with wiki_activity when done."),
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
		mcp.NewTool("wiki_queue",
			mcp.WithTitleAnnotation("List Queue"),
			mcp.WithDescription("List raw/ files missing the 'ingested' frontmatter key — source documents awaiting summarization into wiki pages. Returns JSON array of {path, title, date_added}."),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(true),
			mcp.WithOpenWorldHintAnnotation(false),
		),
		queueListHandler(queue),
	)

	s.AddTool(
		mcp.NewTool("wiki_queue_generate",
			mcp.WithTitleAnnotation("Generate Queue"),
			mcp.WithDescription("Write meta/ingest-queue.md with a table of unprocessed raw sources. Use wiki_queue to read the list without side effects. Returns {path, count}."),
			mcp.WithReadOnlyHintAnnotation(false),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(true),
			mcp.WithOpenWorldHintAnnotation(false),
		),
		queueGenerateHandler(queue),
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
		activityHandler(activity),
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
		createPageHandler(pages),
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
		updatePageHandler(pages),
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
}
