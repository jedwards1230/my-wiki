package api

import "net/http"

// apiRoute describes one REST route. The apiRoutes table is the single source of
// truth for both RegisterRoutes and the OpenAPI sync test (openapi_sync_test.go),
// so docs/openapi.yaml cannot silently drift from the registered surface.
type apiRoute struct {
	Method  string
	Pattern string
	// mutating selects the auth wrapper: true → wrapMutating (gated whenever auth
	// is configured), false → wrapRead (gated only when WIKI_AUTH_READS=true).
	mutating bool
	// handler is the method expression for the route's handler.
	handler func(*Handler, http.ResponseWriter, *http.Request)
	// guard, when non-nil, gates registration on a Handler field being wired.
	// The renderer fragment routes use it (they exist only in native-renderer
	// mode); they remain part of the documented contract regardless.
	guard func(*Handler) bool
}

// apiRoutes is the complete REST surface. Keep it in sync with docs/openapi.yaml
// — TestOpenAPISync enforces that both directions match exactly.
var apiRoutes = []apiRoute{
	// Read-only routes — auth-gated only when WIKI_AUTH_READS=true.
	{Method: "GET", Pattern: "/api/lint", handler: (*Handler).handleLint},
	{Method: "GET", Pattern: "/api/directory", handler: (*Handler).handleDirectoryList},
	{Method: "GET", Pattern: "/api/pages/{path...}", handler: (*Handler).handlePageRead},
	{Method: "GET", Pattern: "/api/pages", handler: (*Handler).handlePageList},
	{Method: "GET", Pattern: "/api/recent", handler: (*Handler).handleRecentList},
	{Method: "GET", Pattern: "/api/search", handler: (*Handler).handleSearch},
	{Method: "GET", Pattern: "/api/graph.json", handler: (*Handler).handleGraph},
	{Method: "GET", Pattern: "/api/tags", handler: (*Handler).handleTags},
	{Method: "GET", Pattern: "/api/whoami", handler: (*Handler).handleWhoami},
	{
		Method:  "GET",
		Pattern: "/api/popover/{slug...}",
		handler: (*Handler).handlePopover,
		guard:   func(h *Handler) bool { return h.renderPages != nil },
	},
	{
		Method:  "GET",
		Pattern: "/api/backlinks",
		handler: (*Handler).handleBacklinks,
		guard:   func(h *Handler) bool { return h.renderBacklinks != nil },
	},

	// Mutating routes — always auth-gated when auth is configured.
	{Method: "POST", Pattern: "/api/directory/generate", mutating: true, handler: (*Handler).handleDirectoryGenerate},
	{Method: "POST", Pattern: "/api/activity", mutating: true, handler: (*Handler).handleActivityAppend},
	{Method: "PUT", Pattern: "/api/pages/{path...}", mutating: true, handler: (*Handler).handlePageWrite},
	{Method: "DELETE", Pattern: "/api/pages/{path...}", mutating: true, handler: (*Handler).handlePageDelete},
	{Method: "PATCH", Pattern: "/api/pages/{path...}", mutating: true, handler: (*Handler).handlePagePatch},
}

// RegisterRoutes registers every route in apiRoutes on mux. Read-only routes are
// unauthenticated by default (wrapRead; also gated when authReads is enabled);
// mutating routes are wrapped with the auth middleware when configured
// (wrapMutating). Guarded routes register only when their renderer hook is wired.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	for _, rt := range apiRoutes {
		if rt.guard != nil && !rt.guard(h) {
			continue
		}
		fn := rt.handler
		var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fn(h, w, r)
		})
		if rt.mutating {
			handler = h.wrapMutating(handler)
		} else {
			handler = h.wrapRead(handler)
		}
		mux.Handle(rt.Method+" "+rt.Pattern, handler)
	}
}
