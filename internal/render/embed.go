package render

import "embed"

// embeddedTemplates holds the rendered HTML + XML templates the Renderer
// loads at init. Lives in the same package so loadTemplates() can iterate
// the tree at runtime without a separate FS argument.
//
//go:embed templates
var embeddedTemplates embed.FS
