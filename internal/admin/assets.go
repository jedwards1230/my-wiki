package admin

import (
	"embed"
	"html/template"
)

//go:embed templates/*.html.tmpl
var templateFS embed.FS

// parseTemplates parses the admin template set. The pages reference shared
// "head"/"foot" partials, so all files are parsed into one set.
func parseTemplates() (*template.Template, error) {
	return template.New("admin").ParseFS(templateFS, "templates/*.html.tmpl")
}
