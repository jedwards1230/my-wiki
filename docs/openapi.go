// Package docs embeds documentation assets that the wiki-server binary serves
// at runtime, so a deployed instance can expose its own machine-readable
// contract rather than pointing callers at a repo file.
package docs

import _ "embed"

// OpenAPISpec is the OpenAPI 3 contract for the REST API, served verbatim at
// GET /api/openapi.yaml. docs/openapi.yaml is its single source of truth;
// TestOpenAPISync (internal/api) keeps that file in exact sync with the
// registered route table.
//
//go:embed openapi.yaml
var OpenAPISpec []byte
