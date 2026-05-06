package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jedwards1230/home-wiki/internal/middleware"
	"github.com/jedwards1230/home-wiki/internal/notify"
	"github.com/jedwards1230/home-wiki/internal/service"
	"github.com/jedwards1230/home-wiki/internal/version"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// markDirty notifies the rebuild notifier about a mutated vault path.
// relPath is relative to vaultDir; .md extension is added if missing.
// action tells downstream sinks what kind of mutation occurred (see
// notify.ChangeKind).
func markDirty(notifier *notify.RebuildNotifier, vaultDir, relPath string, action notify.ChangeKind) {
	if notifier == nil {
		return
	}
	if !strings.HasSuffix(relPath, ".md") {
		relPath += ".md"
	}
	notifier.MarkDirty(filepath.Clean(filepath.Join(vaultDir, relPath)), action)
}

// mcpLog sends a structured log message to the current MCP client session and tees
// the same event to slog.Default() so there is a durable server-side audit trail
// regardless of whether the client is subscribed to MCP log notifications (the
// streamable-http transport is stateless and the client may not be listening).
//
// Authenticated user identity (from the request context) is added to both sinks.
// MCP delivery errors are silently ignored since the slog tee already recorded it.
func mcpLog(ctx context.Context, s *server.MCPServer, level mcp.LoggingLevel, logger string, data map[string]any) {
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
	case mcp.LoggingLevelError, mcp.LoggingLevelCritical, mcp.LoggingLevelAlert, mcp.LoggingLevelEmergency:
		slog.Default().Error(msg, attrs...)
	case mcp.LoggingLevelWarning:
		slog.Default().Warn(msg, attrs...)
	case mcp.LoggingLevelDebug:
		slog.Default().Debug(msg, attrs...)
	default:
		slog.Default().Info(msg, attrs...)
	}

	_ = s.SendLogMessageToClient(ctx, mcp.NewLoggingMessageNotification(level, logger, data))
}

func getStringArg(req mcp.CallToolRequest, key string) string {
	args := req.GetArguments()
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getIntArg(req mcp.CallToolRequest, key string) int {
	args := req.GetArguments()
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

func getBoolArg(req mcp.CallToolRequest, key string) bool {
	args := req.GetArguments()
	if v, ok := args[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func getStringArrayArg(req mcp.CallToolRequest, key string) []string {
	args := req.GetArguments()
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

// resultWithLintWarnings constructs a tool result with the given text and
// optional lint warnings. When warnings are present they are appended as a
// second text content block and emitted as an MCP log notification at warning
// level per the 2025-11-25 spec (notifications/message).
func resultWithLintWarnings(ctx context.Context, s *server.MCPServer, text string, warnings []service.LintIssue) *mcp.CallToolResult {
	result := mcp.NewToolResultText(text)
	if len(warnings) > 0 {
		result.Content = append(result.Content, mcp.TextContent{
			Type: "text",
			Text: "Lint warnings:\n" + toJSON(warnings),
		})
		mcpLog(ctx, s, mcp.LoggingLevelWarning, "lint", map[string]any{
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

func lintHandler(lint *service.LintService) server.ToolHandlerFunc {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		check := getStringArg(req, "check")
		if check == "" {
			check = "all"
		}

		report, err := lint.Run(check)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(toJSON(report)), nil
	}
}

func tagsHandler(svc *service.TagService) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		report, err := svc.List()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(toJSON(report)), nil
	}
}

func whoamiHandler(vaultDir, instanceName string) server.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		info := map[string]any{
			"name":       "home-wiki",
			"version":    version.Value,
			"vault_dir":  filepath.Base(vaultDir),
			"go_version": runtime.Version(),
		}
		if instanceName != "" {
			info["instance_name"] = instanceName
		}
		if u := middleware.UserFromContext(ctx); u != nil {
			info["user"] = map[string]any{
				"username": u.Username,
				"email":    u.Email,
				"name":     u.Name,
				"groups":   u.Groups,
			}
		}
		return mcp.NewToolResultText(toJSON(info)), nil
	}
}

func readHandler(svc *service.PageService) server.ToolHandlerFunc {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		path := getStringArg(req, "path")
		if path == "" {
			return mcp.NewToolResultError("path is required"), nil
		}

		content, err := svc.Read(path)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(content), nil
	}
}

// buildFrontmatter assembles YAML frontmatter from structured parameters.
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

func writeHandler(s *server.MCPServer, svc *service.PageService, lint *service.LintService, vaultDir string, notifier *notify.RebuildNotifier) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		path := getStringArg(req, "path")
		title := getStringArg(req, "title")
		tags := getStringArrayArg(req, "tags")
		content := getStringArg(req, "content")
		date := getStringArg(req, "date")
		description := getStringArg(req, "description")
		extraFrontmatter := getStringArg(req, "extra_frontmatter")

		if path == "" {
			return mcp.NewToolResultError("path is required"), nil
		}
		if title == "" {
			return mcp.NewToolResultError("title is required"), nil
		}
		if len(tags) == 0 {
			return mcp.NewToolResultError("tags is required and must not be empty"), nil
		}
		if content == "" {
			return mcp.NewToolResultError("content is required"), nil
		}
		if date == "" {
			date = time.Now().Format("2006-01-02")
		}
		if extraFrontmatter != "" {
			if err := validateExtraFrontmatter(extraFrontmatter); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
		}

		fullContent := buildFrontmatter(title, tags, date, description, extraFrontmatter) + "\n" + content

		if err := svc.Write(path, fullContent); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		markDirty(notifier, vaultDir, path, notify.ChangeModified)
		mcpLog(ctx, s, mcp.LoggingLevelInfo, "vault", map[string]any{
			"action": "write", "path": path,
		})
		return resultWithLintWarnings(ctx, s, fmt.Sprintf("Wrote page: %s", path), lint.LintPage(path)), nil
	}
}

func editHandler(s *server.MCPServer, svc *service.PageService, lint *service.LintService, vaultDir string, notifier *notify.RebuildNotifier) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		path := getStringArg(req, "path")
		if path == "" {
			return mcp.NewToolResultError("path is required"), nil
		}

		ops, err := getPatchOps(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		if len(ops) == 0 {
			return mcp.NewToolResultError("operations is required and must not be empty"), nil
		}

		content, err := svc.Patch(path, ops)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		markDirty(notifier, vaultDir, path, notify.ChangeModified)
		mcpLog(ctx, s, mcp.LoggingLevelInfo, "vault", map[string]any{
			"action": "edit", "path": path, "operations": len(ops),
		})
		return resultWithLintWarnings(ctx, s, content, lint.LintPage(path)), nil
	}
}

func listHandler(pageSvc *service.PageService, dirSvc *service.DirectoryService) server.ToolHandlerFunc {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		prefix := getStringArg(req, "prefix")
		detail := getBoolArg(req, "detail")
		sortBy := getStringArg(req, "sort_by")
		limit := getIntArg(req, "limit")

		if detail {
			entries, err := dirSvc.List(prefix)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(toJSON(entries)), nil
		}

		pages, err := pageSvc.List(service.ListOptions{
			Prefix: prefix,
			SortBy: sortBy,
			Limit:  limit,
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(toJSON(pages)), nil
	}
}

func searchHandler(svc *service.SearchService) server.ToolHandlerFunc {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query := getStringArg(req, "query")
		if query == "" {
			return mcp.NewToolResultError("query is required"), nil
		}
		if len(query) < 2 {
			return mcp.NewToolResultError("query must be at least 2 characters"), nil
		}

		limit := getIntArg(req, "limit")
		if limit <= 0 {
			limit = 20
		}

		engine := getStringArg(req, "engine")

		resp, err := svc.Search(query, limit, engine)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(toJSON(resp)), nil
	}
}

func deleteHandler(s *server.MCPServer, svc *service.PageService, lint *service.LintService, vaultDir string, notifier *notify.RebuildNotifier) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		path := getStringArg(req, "path")
		if path == "" {
			return mcp.NewToolResultError("path is required"), nil
		}

		if err := svc.Delete(path); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		markDirty(notifier, vaultDir, path, notify.ChangeDeleted)
		mcpLog(ctx, s, mcp.LoggingLevelWarning, "vault", map[string]any{
			"action": "delete", "path": path,
		})
		return resultWithLintWarnings(ctx, s, fmt.Sprintf("deleted: %s", path), lint.LintDelete(path)), nil
	}
}

func moveHandler(s *server.MCPServer, svc *service.PageService, lint *service.LintService, vaultDir string, notifier *notify.RebuildNotifier) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		source := getStringArg(req, "source")
		destination := getStringArg(req, "destination")

		if source == "" {
			return mcp.NewToolResultError("source is required"), nil
		}
		if destination == "" {
			return mcp.NewToolResultError("destination is required"), nil
		}

		if err := svc.Move(source, destination); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		markDirty(notifier, vaultDir, source, notify.ChangeDeleted)
		markDirty(notifier, vaultDir, destination, notify.ChangeCreated)
		mcpLog(ctx, s, mcp.LoggingLevelInfo, "vault", map[string]any{
			"action": "move", "source": source, "destination": destination,
		})
		// Lint the source for broken inbound links caused by removing the old path.
		return resultWithLintWarnings(ctx, s, fmt.Sprintf("moved: %s -> %s", source, destination), lint.LintDelete(source)), nil
	}
}

func activityHandler(s *server.MCPServer, svc *service.ActivityService, vaultDir string, notifier *notify.RebuildNotifier) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		touched := getStringArrayArg(req, "touched")
		entry := service.ActivityEntry{
			Type:    getStringArg(req, "type"),
			Title:   getStringArg(req, "title"),
			Time:    getStringArg(req, "time"),
			Summary: getStringArg(req, "summary"),
			Touched: touched,
		}

		if err := svc.Append(entry); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		today := time.Now().Format("2006-01-02")
		markDirty(notifier, vaultDir, fmt.Sprintf("meta/activity/%s", today), notify.ChangeModified)
		markDirty(notifier, vaultDir, "meta/log", notify.ChangeModified)
		mcpLog(ctx, s, mcp.LoggingLevelInfo, "activity", map[string]any{
			"action": "log", "type": entry.Type, "title": entry.Title,
		})
		return mcp.NewToolResultText("Activity logged successfully"), nil
	}
}

func getPatchOps(req mcp.CallToolRequest) ([]service.PatchOp, error) {
	args := req.GetArguments()
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

		ops = append(ops, service.PatchOp{Find: find, Replace: replace})
	}

	return ops, nil
}
