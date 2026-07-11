package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jedwards1230/my-wiki/internal/middleware"
	"github.com/jedwards1230/my-wiki/internal/notify"
	"github.com/jedwards1230/my-wiki/internal/service"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Logging levels used by mcpLog. mcp.LoggingLevel has no exported constants
// in the go-sdk (it is a bare string type mirroring RFC-5424 syslog
// severities), so the small set this package actually emits is named here.
const (
	logLevelDebug     mcp.LoggingLevel = "debug"
	logLevelInfo      mcp.LoggingLevel = "info"
	logLevelWarning   mcp.LoggingLevel = "warning"
	logLevelError     mcp.LoggingLevel = "error"
	logLevelCritical  mcp.LoggingLevel = "critical"
	logLevelAlert     mcp.LoggingLevel = "alert"
	logLevelEmergency mcp.LoggingLevel = "emergency"
)

// mcpLog sends a structured log message to the current MCP client session and tees
// the same event to slog.Default() so there is a durable server-side audit trail
// regardless of whether the client is subscribed to MCP log notifications (the
// streamable-http transport is stateless and the client may not be listening).
//
// session is the requesting call's *mcp.ServerSession (req.Session); it is nil
// in a unit test that builds a bare *mcp.CallToolRequest with no live session —
// the client-facing notification is skipped in that case (slog still fires).
//
// Authenticated user identity (from the request context) is added to both sinks.
// MCP delivery errors are silently ignored since the slog tee already recorded it.
func mcpLog(ctx context.Context, session *mcp.ServerSession, level mcp.LoggingLevel, logger string, data map[string]any) {
	if user := middleware.UserFromContext(ctx); user != nil {
		if data == nil {
			data = map[string]any{}
		}
		if user.Subject != "" {
			data["user_sub"] = user.Subject
		}
		if user.Username != "" {
			data["user"] = user.Username
		}
	}

	// Tee to slog with component=logger and the data map as attributes so the
	// audit record survives even when no MCP client is subscribed to log events.
	attrs := make([]any, 0, 2+2*len(data))
	attrs = append(attrs, "component", logger)
	for k, v := range data {
		attrs = append(attrs, k, v)
	}
	msg := fmt.Sprintf("mcp %s", logger)
	switch level {
	case logLevelError, logLevelCritical, logLevelAlert, logLevelEmergency:
		slog.Default().Error(msg, attrs...)
	case logLevelWarning:
		slog.Default().Warn(msg, attrs...)
	case logLevelDebug:
		slog.Default().Debug(msg, attrs...)
	default:
		slog.Default().Info(msg, attrs...)
	}

	if session != nil {
		_ = session.Log(ctx, &mcp.LoggingMessageParams{Level: level, Logger: logger, Data: data})
	}
}

// unmarshalArgs decodes a tool call's raw JSON arguments into a map. A
// nil/empty payload decodes to a nil map (every lookup below misses, same as
// the pre-migration mcp-go GetArguments() default).
func unmarshalArgs(req *mcp.CallToolRequest) (map[string]any, error) {
	var raw map[string]any
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &raw); err != nil {
			return nil, err
		}
	}
	return raw, nil
}

func getStringArg(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getIntArg(args map[string]any, key string) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return 0
}

func getBoolArg(args map[string]any, key string) bool {
	if v, ok := args[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func getStringArrayArg(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// textResult wraps text in a successful CallToolResult.
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}

// structuredResult wraps a typed value as StructuredContent, with fallbackText
// as the TextContent fallback for non-structured-aware clients — mirroring
// mcp-go's NewToolResultStructured(structured, fallbackText).
func structuredResult(structured any, fallbackText string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: fallbackText}},
		StructuredContent: structured,
	}
}

// errorResult wraps an expected, user-facing failure (bad input, a service
// error) as a tool-level error: IsError with a text message the agent can
// read, per the MCP convention of surfacing tool failures as errors in the
// result rather than protocol-level errors.
func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}

// resultWithLintWarnings constructs a tool result with the given text and
// optional lint warnings. When warnings are present they are appended as a
// second text content block and emitted as an MCP log notification at warning
// level per the 2025-11-25 spec (notifications/message).
func resultWithLintWarnings(ctx context.Context, session *mcp.ServerSession, text string, warnings []service.LintIssue) *mcp.CallToolResult {
	result := textResult(text)
	if len(warnings) > 0 {
		result.Content = append(result.Content, &mcp.TextContent{
			Text: "Lint warnings:\n" + toJSON(warnings),
		})
		mcpLog(ctx, session, logLevelWarning, "lint", map[string]any{
			"issues": len(warnings),
		})
	}
	return result
}

func toJSON(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("error marshaling result: %v", err)
	}
	return string(data)
}

// --- Tool handlers ---

func lintHandler(lint *service.LintService) mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := unmarshalArgs(req)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		check := getStringArg(args, "check")
		if check == "" {
			check = "all"
		}

		report, err := lint.Run(check)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		return structuredResult(report, toJSON(report)), nil
	}
}

func tagsHandler(svc *service.TagService) mcp.ToolHandler {
	return func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		report, err := svc.List()
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return structuredResult(report, toJSON(report)), nil
	}
}

func whoamiHandler(vaultDir, instanceName string) mcp.ToolHandler {
	return func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var user *service.UserInfo
		if u := middleware.UserFromContext(ctx); u != nil {
			user = &service.UserInfo{
				Username: u.Username,
				Email:    u.Email,
				Name:     u.Name,
				Groups:   u.Groups,
			}
		}
		info := service.BuildServerInfo(vaultDir, instanceName, user)
		return structuredResult(info, toJSON(info)), nil
	}
}

func readHandler(svc *service.PageService) mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := unmarshalArgs(req)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		path := getStringArg(args, "path")
		if path == "" {
			return errorResult("path is required"), nil
		}

		content, err := svc.Read(path)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		return textResult(content), nil
	}
}

// sanitizeScalar strips newlines and trims whitespace to ensure a value
// stays on a single YAML line.
func sanitizeScalar(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	return strings.TrimSpace(s)
}

// reservedFrontmatterKeys are keys managed by structured params and must
// not appear in extra_frontmatter.
var reservedFrontmatterKeys = []string{"title", "tags", "date", "description"}

// validateExtraFrontmatter checks that extra_frontmatter does not contain
// a YAML document delimiter or override reserved keys.
func validateExtraFrontmatter(extra string) error {
	for _, line := range strings.Split(extra, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			return fmt.Errorf("extra_frontmatter must not contain YAML delimiter '---'")
		}
		for _, key := range reservedFrontmatterKeys {
			if strings.HasPrefix(trimmed, key+":") {
				return fmt.Errorf("extra_frontmatter must not redefine reserved key %q (use the dedicated parameter instead)", key)
			}
		}
	}
	return nil
}

// buildFrontmatter assembles YAML frontmatter from structured parameters.
func buildFrontmatter(title string, tags []string, date, description, extraFrontmatter string) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "title: %s\n", sanitizeScalar(title))
	b.WriteString("tags:\n")
	for _, tag := range tags {
		fmt.Fprintf(&b, "  - %s\n", sanitizeScalar(tag))
	}
	fmt.Fprintf(&b, "date: %s\n", sanitizeScalar(date))
	if description != "" {
		fmt.Fprintf(&b, "description: %s\n", sanitizeScalar(description))
	}
	if extraFrontmatter != "" {
		b.WriteString(extraFrontmatter)
		if !strings.HasSuffix(extraFrontmatter, "\n") {
			b.WriteString("\n")
		}
	}
	b.WriteString("---\n")
	return b.String()
}

func writeHandler(svc *service.PageService, lint *service.LintService, vaultDir string, notifier *notify.RebuildNotifier) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := unmarshalArgs(req)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		path := getStringArg(args, "path")
		title := getStringArg(args, "title")
		tags := getStringArrayArg(args, "tags")
		content := getStringArg(args, "content")
		date := getStringArg(args, "date")
		description := getStringArg(args, "description")
		extraFrontmatter := getStringArg(args, "extra_frontmatter")

		if path == "" {
			return errorResult("path is required"), nil
		}
		if title == "" {
			return errorResult("title is required"), nil
		}
		if len(tags) == 0 {
			return errorResult("tags is required and must not be empty"), nil
		}
		if content == "" {
			return errorResult("content is required"), nil
		}
		if date == "" {
			date = time.Now().Format("2006-01-02")
		}
		if extraFrontmatter != "" {
			if err := validateExtraFrontmatter(extraFrontmatter); err != nil {
				return errorResult(err.Error()), nil
			}
		}

		fullContent := buildFrontmatter(title, tags, date, description, extraFrontmatter) + "\n" + content

		if err := svc.Write(path, fullContent); err != nil {
			return errorResult(err.Error()), nil
		}

		notify.MarkDirtyRelative(notifier, vaultDir, path, notify.ChangeModified)
		mcpLog(ctx, req.Session, logLevelInfo, "vault", map[string]any{
			"action": "write", "path": path,
		})
		return resultWithLintWarnings(ctx, req.Session, fmt.Sprintf("Wrote page: %s", path), lint.LintPage(path)), nil
	}
}

func editHandler(svc *service.PageService, lint *service.LintService, vaultDir string, notifier *notify.RebuildNotifier) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := unmarshalArgs(req)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		path := getStringArg(args, "path")
		if path == "" {
			return errorResult("path is required"), nil
		}

		ops, err := getPatchOps(args)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		if len(ops) == 0 {
			return errorResult("operations is required and must not be empty"), nil
		}

		content, err := svc.Patch(path, ops)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		notify.MarkDirtyRelative(notifier, vaultDir, path, notify.ChangeModified)
		mcpLog(ctx, req.Session, logLevelInfo, "vault", map[string]any{
			"action": "edit", "path": path, "operations": len(ops),
		})
		return resultWithLintWarnings(ctx, req.Session, content, lint.LintPage(path)), nil
	}
}

// ListResponse is the structured response from the list tool. The MCP spec
// requires structuredContent to be a JSON object, so the two underlying array
// shapes are wrapped here. Exactly one of Pages (simple mode) or Entries
// (detail mode) is populated per call; the other is omitted. The fallback text
// block continues to carry the bare array for backwards compatibility.
type ListResponse struct {
	Detail  bool                     `json:"detail"`
	Pages   []service.PageInfo       `json:"pages,omitempty"`
	Entries []service.DirectoryEntry `json:"entries,omitempty"`
}

func listHandler(pageSvc *service.PageService, dirSvc *service.DirectoryService) mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := unmarshalArgs(req)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		prefix := getStringArg(args, "prefix")
		detail := getBoolArg(args, "detail")
		sortBy := getStringArg(args, "sort_by")
		limit := getIntArg(args, "limit")

		if detail {
			entries, err := dirSvc.List(prefix)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			// Fallback text keeps the bare array shape for back-compat.
			return structuredResult(
				ListResponse{Detail: true, Entries: entries},
				toJSON(entries),
			), nil
		}

		pages, err := pageSvc.List(service.ListOptions{
			Prefix: prefix,
			SortBy: sortBy,
			Limit:  limit,
		})
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return structuredResult(
			ListResponse{Detail: false, Pages: pages},
			toJSON(pages),
		), nil
	}
}

func searchHandler(svc *service.SearchService) mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := unmarshalArgs(req)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		query := getStringArg(args, "query")
		if query == "" {
			return errorResult("query is required"), nil
		}
		if len(query) < 2 {
			return errorResult("query must be at least 2 characters"), nil
		}

		limit := getIntArg(args, "limit")
		if limit <= 0 {
			limit = 20
		}

		engine := getStringArg(args, "engine")

		resp, err := svc.Search(query, limit, engine)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		return structuredResult(resp, toJSON(resp)), nil
	}
}

func deleteHandler(svc *service.PageService, lint *service.LintService, vaultDir string, notifier *notify.RebuildNotifier) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := unmarshalArgs(req)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		path := getStringArg(args, "path")
		if path == "" {
			return errorResult("path is required"), nil
		}

		if err := svc.Delete(path); err != nil {
			return errorResult(err.Error()), nil
		}

		notify.MarkDirtyRelative(notifier, vaultDir, path, notify.ChangeDeleted)
		mcpLog(ctx, req.Session, logLevelWarning, "vault", map[string]any{
			"action": "delete", "path": path,
		})
		return resultWithLintWarnings(ctx, req.Session, fmt.Sprintf("deleted: %s", path), lint.LintDelete(path)), nil
	}
}

func moveHandler(svc *service.PageService, lint *service.LintService, vaultDir string, notifier *notify.RebuildNotifier) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := unmarshalArgs(req)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		source := getStringArg(args, "source")
		destination := getStringArg(args, "destination")

		if source == "" {
			return errorResult("source is required"), nil
		}
		if destination == "" {
			return errorResult("destination is required"), nil
		}

		if err := svc.Move(source, destination); err != nil {
			return errorResult(err.Error()), nil
		}

		notify.MarkDirtyRelative(notifier, vaultDir, source, notify.ChangeDeleted)
		notify.MarkDirtyRelative(notifier, vaultDir, destination, notify.ChangeCreated)
		mcpLog(ctx, req.Session, logLevelInfo, "vault", map[string]any{
			"action": "move", "source": source, "destination": destination,
		})
		// Lint the source for broken inbound links caused by removing the old path.
		return resultWithLintWarnings(ctx, req.Session, fmt.Sprintf("moved: %s -> %s", source, destination), lint.LintDelete(source)), nil
	}
}

func activityHandler(svc *service.ActivityService, vaultDir string, notifier *notify.RebuildNotifier) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := unmarshalArgs(req)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		touched := getStringArrayArg(args, "touched")
		entry := service.ActivityEntry{
			Type:       getStringArg(args, "type"),
			Title:      getStringArg(args, "title"),
			Time:       getStringArg(args, "time"),
			Summary:    getStringArg(args, "summary"),
			Touched:    touched,
			DaySummary: getStringArg(args, "day_summary"),
		}

		if err := svc.Append(entry); err != nil {
			return errorResult(err.Error()), nil
		}

		for _, p := range svc.DirtyPaths() {
			notify.MarkDirtyRelative(notifier, vaultDir, p, notify.ChangeModified)
		}
		mcpLog(ctx, req.Session, logLevelInfo, "activity", map[string]any{
			"action": "log", "type": entry.Type, "title": entry.Title,
		})
		return textResult("Activity logged successfully"), nil
	}
}

func getPatchOps(args map[string]any) ([]service.PatchOp, error) {
	v, ok := args["operations"]
	if !ok {
		return nil, fmt.Errorf("operations is required")
	}

	arr, ok := v.([]interface{})
	if !ok {
		return nil, fmt.Errorf("operations must be an array")
	}

	ops := make([]service.PatchOp, 0, len(arr))
	for i, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("operation %d must be an object", i)
		}

		findValue, ok := m["find"]
		if !ok {
			return nil, fmt.Errorf("operation %d: find is required", i)
		}
		find, ok := findValue.(string)
		if !ok {
			return nil, fmt.Errorf("operation %d: find must be a string", i)
		}
		if find == "" {
			return nil, fmt.Errorf("operation %d: find must be non-empty", i)
		}

		replaceValue, ok := m["replace"]
		if !ok {
			return nil, fmt.Errorf("operation %d: replace is required", i)
		}
		replace, ok := replaceValue.(string)
		if !ok {
			return nil, fmt.Errorf("operation %d: replace must be a string", i)
		}

		ops = append(ops, service.PatchOp{Find: find, Replace: &replace})
	}

	return ops, nil
}
