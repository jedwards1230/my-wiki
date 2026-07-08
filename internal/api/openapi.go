package api

import (
	"net/http"

	"github.com/jedwards1230/my-wiki/docs"
)

// handleOpenAPISpec serves the embedded OpenAPI document so a deployed instance
// exposes its own REST contract. The spec is returned verbatim (not envelope-
// wrapped) as YAML; docs/openapi.yaml is its single source of truth.
func (h *Handler) handleOpenAPISpec(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(docs.OpenAPISpec)
}
