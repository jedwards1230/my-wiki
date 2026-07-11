package mcpserver

import (
	"context"
	"net/http"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/jedwards1230/my-wiki/internal/notify"
	"github.com/jedwards1230/my-wiki/internal/service"
	"github.com/jedwards1230/my-wiki/internal/vault"
	"github.com/jedwards1230/my-wiki/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Option configures optional MCP server behavior.
type Option func(*options)

type options struct {
	notifier     *notify.RebuildNotifier
	pages        *service.PageService
	instanceName string
	baseURL      string
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

// WithBaseURL sets the canonical external base URL of the deployment (e.g.
// from WIKI_BASE_URL). When set, it is surfaced as the server's websiteUrl in
// the MCP initialize response. When empty (the default), the websiteUrl is
// omitted so no deployment-specific domain is baked in.
func WithBaseURL(url string) Option {
	return func(o *options) {
		o.baseURL = url
	}
}

// New creates a configured MCP server with all wiki tools registered.
// searchSvc may be nil if search is not configured.
func New(v *vault.Vault, searchSvc *service.SearchService, opts ...Option) *mcp.Server {
	var cfg options
	for _, o := range opts {
		o(&cfg)
	}
	// Server identity metadata surfaced to MCP clients on initialize. The
	// title and websiteUrl are deployment-specific, so they are sourced from
	// config (WIKI_INSTANCE_NAME / WIKI_BASE_URL, threaded in via Options) and
	// only included when non-empty — nothing deployment-specific is hardcoded.
	// Icons are omitted (no icon asset ships).
	impl := &mcp.Implementation{
		Name:    "my-wiki",
		Version: version.Value,
	}
	if cfg.instanceName != "" {
		impl.Title = cfg.instanceName
	}
	if cfg.baseURL != "" {
		impl.WebsiteURL = cfg.baseURL
	}

	// ServerOptions.Capabilities is left nil deliberately: the SDK
	// auto-derives capabilities from what's actually registered (logging is
	// always advertised by default; tools:{listChanged:true} and
	// resources:{listChanged:true} are added automatically once the tools and
	// the wiki://schema resource below are registered). Do NOT hand-set
	// Capabilities.Resources here — an explicit but empty
	// *mcp.ResourceCapabilities would serialize as a falsy `{}` that
	// ContextForge's federation (`if capabilities.get("resources"):`) treats
	// as absent. Leaving Capabilities nil is what makes it serialize truthy.
	//
	// The go-sdk's Implementation type has no Description field (unlike
	// mcp-go's nonstandard extension) — the description text is folded into
	// the leading sentence of Instructions instead, so it's still surfaced to
	// clients via the spec-sanctioned "instructions" field of the initialize
	// response, alongside the original operating-manual guidance.
	s := mcp.NewServer(impl, &mcp.ServerOptions{
		Instructions: "Read and edit a wiki backed by an Obsidian vault. The meta/schema resource is available for context. Page create/update/delete/move mutations are auto-logged as compact audit entries — do NOT call activity for individual page changes. Use activity only for narrative summaries of multi-page work sessions or non-page activities (lint, note, migrate).",
	})

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
func registerResources(s *mcp.Server, pages *service.PageService) {
	s.AddResource(
		&mcp.Resource{
			Name:        "Wiki Schema",
			URI:         "wiki://schema",
			Description: "Operating manual for AI agents — page conventions, frontmatter rules, and activity logging format.",
			MIMEType:    "text/markdown",
		},
		func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			content, err := pages.Read("meta/schema")
			if err != nil {
				return nil, err
			}
			return &mcp.ReadResourceResult{
				Contents: []*mcp.ResourceContents{
					{
						URI:      "wiki://schema",
						MIMEType: "text/markdown",
						Text:     content,
					},
				},
			}, nil
		},
	)
}

// NewStreamableHTTPServer creates a stateless streamable HTTP handler serving
// s. The tool set is process-wide: getServer always returns the same,
// already-configured *mcp.Server, matching the previous mcp-go
// WithStateLess(true) behavior.
func NewStreamableHTTPServer(s *mcp.Server) *mcp.StreamableHTTPHandler {
	return mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return s },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
}

// mustOutputSchema reflects T's output schema via jsonschema.For, the same
// reflector mcp-go's WithOutputSchema[T]() used — so the shape (property
// names, required lists, additionalProperties:false, nullable slices) is
// byte-for-byte the same as before. A reflection failure here is a coding
// error (an output type containing a schema-incompatible field), so it
// panics at startup rather than silently shipping a tool with no output
// schema.
func mustOutputSchema[T any]() *jsonschema.Schema {
	schema, err := jsonschema.For[T](&jsonschema.ForOptions{IgnoreInvalidTypes: true})
	if err != nil {
		panic(err)
	}
	return schema
}

func registerTools(
	s *mcp.Server,
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
	boolPtr := func(b bool) *bool { return &b }

	// --- read: Read a wiki page ---
	s.AddTool(
		&mcp.Tool{
			Name:        "read",
			Description: "Read a wiki page's full markdown content including frontmatter. The .md extension is added automatically if omitted.",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Read Page",
				ReadOnlyHint:    true,
				DestructiveHint: boolPtr(false),
				IdempotentHint:  true,
				OpenWorldHint:   boolPtr(false),
			},
			InputSchema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"path": {
						Type:        "string",
						Description: "Relative path within the vault (e.g., meta/schema or meta/schema.md).",
					},
				},
				Required: []string{"path"},
			},
		},
		readHandler(pages),
	)

	// --- write: Create or update a wiki page ---
	s.AddTool(
		&mcp.Tool{
			Name:        "write",
			Description: "Create or update a wiki page. Frontmatter is assembled from structured parameters — do NOT embed YAML frontmatter in the content field. If the page exists it is overwritten; if it does not exist it is created.",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Write Page",
				ReadOnlyHint:    false,
				DestructiveHint: boolPtr(true),
				IdempotentHint:  true,
				OpenWorldHint:   boolPtr(false),
			},
			InputSchema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"path": {
						Type:        "string",
						Description: "Relative path for the page (e.g., project/my-project). The .md extension is added if omitted. The server slugifies the filename segment (lowercase, hyphenated, smart-quotes/punctuation stripped) — provide the folder and an approximate name; the canonical on-disk path is server-assigned.",
					},
					"title": {
						Type:        "string",
						Description: "Page title for frontmatter.",
					},
					"tags": {
						Type:        "array",
						Description: "Page tags for frontmatter.",
						Items:       &jsonschema.Schema{Type: "string"},
					},
					"content": {
						Type:        "string",
						Description: "Body content in markdown. Do NOT include YAML frontmatter — it is generated from the other parameters.",
					},
					"date": {
						Type:        "string",
						Description: "Creation date in YYYY-MM-DD format. Defaults to today if omitted.",
					},
					"description": {
						Type:        "string",
						Description: "One-line summary for directory index. Omit if not needed.",
					},
					"extra_frontmatter": {
						Type:        "string",
						Description: "Raw YAML lines for arbitrary frontmatter fields (e.g., 'status: wip\\nsource: https://...'). Inserted before the closing ---. Omit if not needed.",
					},
				},
				Required: []string{"path", "title", "tags", "content"},
			},
		},
		writeHandler(pages, lint, vaultDir, notifier),
	)

	// --- edit: Surgical partial update ---
	s.AddTool(
		&mcp.Tool{
			Name:        "edit",
			Description: "Apply targeted find-and-replace edits to an existing wiki page without replacing the entire content. Each operation replaces the first occurrence of 'find' with 'replace'. If any find string is not found, the operation fails with no changes written.",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Edit Page",
				ReadOnlyHint:    false,
				DestructiveHint: boolPtr(true),
				IdempotentHint:  false,
				OpenWorldHint:   boolPtr(false),
			},
			InputSchema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"path": {
						Type:        "string",
						Description: "Relative path to the page to edit (e.g., project/my-project or project/my-project.md). The .md extension is added if omitted.",
					},
					"operations": {
						Type:        "array",
						Description: "Array of find-and-replace operations to apply in order.",
						Items: &jsonschema.Schema{
							Type: "object",
							Properties: map[string]*jsonschema.Schema{
								"find":    {Type: "string", Description: "Text to find in the page."},
								"replace": {Type: "string", Description: "Text to replace it with."},
							},
							Required: []string{"find", "replace"},
						},
					},
				},
				Required: []string{"path", "operations"},
			},
		},
		editHandler(pages, lint, vaultDir, notifier),
	)

	// --- list: List pages with optional detail ---
	s.AddTool(
		&mcp.Tool{
			Name:        "list",
			Description: "List wiki pages (excludes raw/, .obsidian/). With detail=false (default), returns {path, title, has_meta} per page. With detail=true, returns rich entries with {path, title, description, tags} from frontmatter. Use sort_by='modified' with limit to get recently changed pages.",
			Annotations: &mcp.ToolAnnotations{
				Title:           "List Pages",
				ReadOnlyHint:    true,
				DestructiveHint: boolPtr(false),
				IdempotentHint:  true,
				OpenWorldHint:   boolPtr(false),
			},
			InputSchema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"prefix": {
						Type:        "string",
						Description: "Filter to pages under this directory (e.g., 'project/' or 'meta/'). Default: list all pages.",
					},
					"detail": {
						Type:        "boolean",
						Description: "When true, return rich directory entries with description and tags from frontmatter. Default: false.",
					},
					"sort_by": {
						Type:        "string",
						Enum:        []any{"name", "modified"},
						Description: "Sort order: 'name' (default, alphabetical) or 'modified' (newest first, includes modified timestamp). When 'modified', activity log files are excluded.",
					},
					"limit": {
						Type:        "number",
						Description: "Maximum pages to return. Default: unlimited. Useful with sort_by='modified' to get recent pages.",
					},
				},
				Required: []string{},
			},
			OutputSchema: mustOutputSchema[ListResponse](),
		},
		listHandler(pages, directory),
	)

	// --- search: Full-text search ---
	if searchSvc != nil {
		s.AddTool(
			&mcp.Tool{
				Name:        "search",
				Description: "Full-text search across wiki pages. Matches against title, tags, and content. Returns results ranked by relevance with snippets and timing. Use engine='all' to compare search backends side-by-side.",
				Annotations: &mcp.ToolAnnotations{
					Title:           "Search Wiki",
					ReadOnlyHint:    true,
					DestructiveHint: boolPtr(false),
					IdempotentHint:  true,
					OpenWorldHint:   boolPtr(false),
				},
				InputSchema: &jsonschema.Schema{
					Type: "object",
					Properties: map[string]*jsonschema.Schema{
						"query": {
							Type:        "string",
							Description: "Search query (minimum 2 characters).",
						},
						"limit": {
							Type:        "number",
							Description: "Maximum results per engine. Default: 20.",
						},
						"engine": {
							Type:        "string",
							Enum:        []any{"substring", "index", "all"},
							Description: "Search engine: 'substring' (default, walks files), 'index' (inverted index with TF-IDF), 'all' (run both, compare timing).",
						},
					},
					Required: []string{"query"},
				},
				OutputSchema: mustOutputSchema[service.SearchResponse](),
			},
			searchHandler(searchSvc),
		)
	}

	// --- delete: Remove a page ---
	s.AddTool(
		&mcp.Tool{
			Name:        "delete",
			Description: "Delete a wiki page. Returns an error if the page does not exist. Returns lint warnings about broken wikilinks caused by the deletion.",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Delete Page",
				ReadOnlyHint:    false,
				DestructiveHint: boolPtr(true),
				IdempotentHint:  false,
				OpenWorldHint:   boolPtr(false),
			},
			InputSchema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"path": {
						Type:        "string",
						Description: "Relative path to the page to delete (e.g., project/old-page or project/old-page.md). The .md extension is added if omitted.",
					},
				},
				Required: []string{"path"},
			},
		},
		deleteHandler(pages, lint, vaultDir, notifier),
	)

	// --- move: Rename/relocate a page ---
	s.AddTool(
		&mcp.Tool{
			Name:        "move",
			Description: "Rename or relocate a wiki page. Fails if the source does not exist or the destination already exists. Returns lint warnings about broken wikilinks caused by the move.",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Move Page",
				ReadOnlyHint:    false,
				DestructiveHint: boolPtr(true),
				IdempotentHint:  false,
				OpenWorldHint:   boolPtr(false),
			},
			InputSchema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"source": {
						Type:        "string",
						Description: "Current relative path of the page (e.g., project/old-name). The .md extension is added if omitted.",
					},
					"destination": {
						Type:        "string",
						Description: "New relative path for the page (e.g., project/new-name). The .md extension is added if omitted. The filename segment is slugified server-side, so the final destination is canonical regardless of casing/punctuation you supply.",
					},
				},
				Required: []string{"source", "destination"},
			},
		},
		moveHandler(pages, lint, vaultDir, notifier),
	)

	// --- lint: Run vault health checks ---
	s.AddTool(
		&mcp.Tool{
			Name:        "lint",
			Description: "Run vault-wide mechanical health checks. Returns issues grouped by check. These are structural/metadata checks only — content-level issues (stale facts, contradictions, outdated references) require manual review or the semantic lint layer.\n\nChecks:\n- frontmatter: required fields (title, tags, date) on wiki pages; skips generated pages\n- tags: validates page tags against the taxonomy in meta/schema; flags unused domains and under-threshold tags\n- links: broken [[wikilinks]] — deduplicates by target, lists all source files per missing page\n- orphans: pages with no inbound wikilinks\n- size: pages exceeding 1000 words\n- log: hash mismatches between meta/log.md index and daily activity files",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Lint Vault",
				ReadOnlyHint:    true,
				DestructiveHint: boolPtr(false),
				IdempotentHint:  true,
				OpenWorldHint:   boolPtr(false),
			},
			InputSchema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"check": {
						Type:        "string",
						Description: "Which check to run. Default: all.",
						Enum:        []any{"all", "frontmatter", "tags", "links", "orphans", "size", "log"},
					},
				},
				Required: []string{},
			},
			OutputSchema: mustOutputSchema[service.LintReport](),
		},
		lintHandler(lint),
	)

	// --- tags: List all tags in use ---
	s.AddTool(
		&mcp.Tool{
			Name:        "tags",
			Description: "List all tags used across wiki pages with page counts, sorted by frequency. Use to discover existing tags before creating new pages.",
			Annotations: &mcp.ToolAnnotations{
				Title:           "List Tags",
				ReadOnlyHint:    true,
				DestructiveHint: boolPtr(false),
				IdempotentHint:  true,
				OpenWorldHint:   boolPtr(false),
			},
			InputSchema: &jsonschema.Schema{
				Type:       "object",
				Properties: map[string]*jsonschema.Schema{},
				Required:   []string{},
			},
			OutputSchema: mustOutputSchema[service.TagReport](),
		},
		tagsHandler(tags),
	)

	// --- whoami: Server identity ---
	s.AddTool(
		&mcp.Tool{
			Name:        "whoami",
			Description: "Returns server identity: name, version, vault directory, and Go runtime version. Useful for verifying which wiki instance you're connected to.",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Server Info",
				ReadOnlyHint:    true,
				DestructiveHint: boolPtr(false),
				IdempotentHint:  true,
				OpenWorldHint:   boolPtr(false),
			},
			InputSchema: &jsonschema.Schema{
				Type:       "object",
				Properties: map[string]*jsonschema.Schema{},
				Required:   []string{},
			},
			OutputSchema: mustOutputSchema[service.ServerInfo](),
		},
		whoamiHandler(vaultDir, instanceName),
	)

	// --- activity: Append to activity log ---
	s.AddTool(
		&mcp.Tool{
			Name:        "activity",
			Description: "Append a narrative entry to today's activity log. Individual page mutations (create/edit/delete/move) are auto-logged — do NOT duplicate them here. Use this for summaries of multi-page work sessions or non-page activities like lint, note, or migrate.",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Log Activity",
				ReadOnlyHint:    false,
				DestructiveHint: boolPtr(false),
				IdempotentHint:  false,
				OpenWorldHint:   boolPtr(false),
			},
			InputSchema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"type": {
						Type:        "string",
						Enum:        []any{"edit", "create", "delete", "lint", "note", "migrate", "move"},
						Description: "Activity type: edit (page modified), create (new page), delete (page removed), lint (health check run), note (general observation), migrate (structural change), move (page relocated).",
					},
					"title": {
						Type:        "string",
						Description: "Short title for the activity entry.",
					},
					"time": {
						Type:        "string",
						Description: "Override timestamp in HH:MM format. Default: current time.",
					},
					"summary": {
						Type:        "string",
						Description: "Optional description of what was done.",
					},
					"day_summary": {
						Type:        "string",
						Description: "Optional whole-day digest for the meta/log.md index line (e.g. \"Sapolsky neuroscience series + events collection\"). Set this once per work session to summarize the day's overall theme. Stored in the day file's frontmatter and used verbatim as the log index line, overriding the auto-computed digest. Leave empty on routine entries.",
					},
					"touched": {
						Type:        "array",
						Description: "Wiki pages related to this activity (e.g., project/foo). Optional.",
						Items:       &jsonschema.Schema{Type: "string"},
					},
				},
				Required: []string{"type", "title"},
			},
		},
		activityHandler(activity, vaultDir, notifier),
	)
}
