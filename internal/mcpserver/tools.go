package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jedwards1230/home-wiki/internal/service"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

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

func toJSON(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("error marshaling result: %v", err)
	}
	return string(data)
}

func lintHandler(svc *service.LintService) server.ToolHandlerFunc {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		check := getStringArg(req, "check")
		if check == "" {
			check = "all"
		}

		report, err := svc.Run(check)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(toJSON(report)), nil
	}
}

func queueListHandler(svc *service.QueueService) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		items, err := svc.List()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(toJSON(items)), nil
	}
}

func queueGenerateHandler(svc *service.QueueService) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		path, count, err := svc.Generate()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		result := map[string]any{"path": path, "count": count}
		return mcp.NewToolResultText(toJSON(result)), nil
	}
}

func logIndexHandler(svc *service.LogService) server.ToolHandlerFunc {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		n := getIntArg(req, "n")

		entries, err := svc.Index(n)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(toJSON(entries)), nil
	}
}

func logDayHandler(svc *service.LogService) server.ToolHandlerFunc {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		date := getStringArg(req, "date")
		detail := getBoolArg(req, "detail")

		if date == "" {
			return mcp.NewToolResultError("date is required"), nil
		}

		dayLog, err := svc.Day(date, detail)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(toJSON(dayLog)), nil
	}
}

func logLintHandler(svc *service.LogService) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		issues, err := svc.Lint()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(toJSON(issues)), nil
	}
}

func activityHandler(svc *service.ActivityService) server.ToolHandlerFunc {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		entry := service.ActivityEntry{
			Type:    getStringArg(req, "type"),
			Title:   getStringArg(req, "title"),
			Time:    getStringArg(req, "time"),
			Summary: getStringArg(req, "summary"),
		}

		if err := svc.Append(entry); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText("Activity logged successfully"), nil
	}
}

func readPageHandler(svc *service.PageService) server.ToolHandlerFunc {
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

func createPageHandler(svc *service.PageService) server.ToolHandlerFunc {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		path := getStringArg(req, "path")
		content := getStringArg(req, "content")

		if path == "" {
			return mcp.NewToolResultError("path is required"), nil
		}

		// Check if page already exists
		if _, err := svc.Read(path); err == nil {
			return mcp.NewToolResultError(fmt.Sprintf("page already exists: %s (use wiki_update_page to modify)", path)), nil
		}

		if err := svc.Write(path, content); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(fmt.Sprintf("Created page: %s", path)), nil
	}
}

func updatePageHandler(svc *service.PageService) server.ToolHandlerFunc {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		path := getStringArg(req, "path")
		content := getStringArg(req, "content")

		if path == "" {
			return mcp.NewToolResultError("path is required"), nil
		}

		if err := svc.Write(path, content); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(fmt.Sprintf("Updated page: %s", path)), nil
	}
}

func listPagesHandler(svc *service.PageService) server.ToolHandlerFunc {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		prefix := getStringArg(req, "prefix")

		pages, err := svc.List(prefix)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(toJSON(pages)), nil
	}
}

func searchHandler() server.ToolHandlerFunc {
	return func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultError("search is not yet implemented"), nil
	}
}
