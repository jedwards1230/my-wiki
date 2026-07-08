package api

import (
	"os"
	"sort"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v2"
)

// specPath is the OpenAPI document, relative to this package directory.
const specPath = "../../docs/openapi.yaml"

// httpMethods are the OpenAPI operation keys treated as routes. Other keys under
// a path item (notably "parameters") are skipped.
var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"patch": true, "head": true, "options": true, "trace": true,
}

// canonicalPattern normalizes a route pattern so the code's Go 1.22 wildcard
// form ("{name...}") compares equal to the OpenAPI path parameter form
// ("{name}"). OpenAPI cannot express a multi-segment path parameter, so the
// trailing "..." is the only difference between the two spellings.
func canonicalPattern(p string) string {
	return strings.ReplaceAll(p, "...}", "}")
}

// TestOpenAPISync asserts docs/openapi.yaml documents exactly the routes
// registered in apiRoutes — no undocumented routes, no phantom spec paths.
// It is the drift guard: adding, removing, or renaming a route without updating
// the spec (or vice versa) fails this test.
func TestOpenAPISync(t *testing.T) {
	// Routes registered by the code.
	codeRoutes := make(map[string]bool, len(apiRoutes))
	for _, rt := range apiRoutes {
		codeRoutes[rt.Method+" "+canonicalPattern(rt.Pattern)] = true
	}

	// Operations documented by the spec.
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read %s: %v", specPath, err)
	}
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse %s: %v", specPath, err)
	}
	specRoutes := make(map[string]bool)
	for path, item := range spec.Paths {
		for key := range item {
			if !httpMethods[strings.ToLower(key)] {
				continue
			}
			specRoutes[strings.ToUpper(key)+" "+canonicalPattern(path)] = true
		}
	}

	if missing := diff(codeRoutes, specRoutes); len(missing) > 0 {
		t.Errorf("routes registered in code but missing from %s:\n  %s",
			specPath, strings.Join(missing, "\n  "))
	}
	if phantom := diff(specRoutes, codeRoutes); len(phantom) > 0 {
		t.Errorf("routes documented in %s but not registered in code:\n  %s",
			specPath, strings.Join(phantom, "\n  "))
	}
}

// diff returns the sorted keys present in a but not in b.
func diff(a, b map[string]bool) []string {
	var only []string
	for k := range a {
		if !b[k] {
			only = append(only, k)
		}
	}
	sort.Strings(only)
	return only
}
