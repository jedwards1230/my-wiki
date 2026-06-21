package server

import (
	"bytes"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/jedwards1230/my-wiki/internal/render"
	"github.com/jedwards1230/my-wiki/internal/service"
)

// StaticHandler serves the rendered static site output with try_files logic:
// exact path → path.html → path/index.html → 404.html
type StaticHandler struct {
	fsys fs.FS
}

// NewStaticHandler creates a handler serving static files from the given filesystem.
func NewStaticHandler(fsys fs.FS) *StaticHandler {
	return &StaticHandler{fsys: fsys}
}

func (h *StaticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")

	// Try exact file
	if p != "" && h.serveFile(w, r, p) {
		return
	}

	// Try path.html (the renderer generates about.html, served at /about)
	if p != "" && h.serveFile(w, r, p+".html") {
		return
	}

	// Try path/index.html (directory index)
	indexPath := path.Join(p, "index.html")
	if h.serveFile(w, r, indexPath) {
		return
	}

	// Serve 404.html with 404 status
	h.serve404(w, r)
}

func (h *StaticHandler) serveFile(w http.ResponseWriter, r *http.Request, name string) bool {
	f, err := h.fsys.Open(name)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		return false
	}

	rs, ok := f.(io.ReadSeeker)
	if ok {
		http.ServeContent(w, r, name, stat.ModTime(), rs)
	} else {
		// Fallback for fs.FS implementations that don't support ReadSeeker
		data, err := io.ReadAll(f)
		if err != nil {
			return false
		}
		http.ServeContent(w, r, name, stat.ModTime(), bytes.NewReader(data))
	}
	return true
}

func (h *StaticHandler) serve404(w http.ResponseWriter, r *http.Request) {
	f, err := h.fsys.Open("404.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(f)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write(data)
}

// MarkdownHandler serves vault markdown files as text/plain.
type MarkdownHandler struct {
	fsys fs.FS
}

// NewMarkdownHandler creates a handler serving .md files from the vault filesystem.
func NewMarkdownHandler(fsys fs.FS) *MarkdownHandler {
	return &MarkdownHandler{fsys: fsys}
}

func (h *MarkdownHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")

	// Deny the editor-config directory (.obsidian/) on the markdown surface,
	// reusing the same predicate the page API enforces. 404 (not 403) so we
	// don't confirm existence. raw/ is intentionally NOT denied here — it is
	// served by the dedicated /raw/ handler.
	if service.IsAPIDenied(p) {
		http.NotFound(w, r)
		return
	}

	f, err := h.fsys.Open(p)
	if err != nil {
		// File not found: if path ends in .md, check if the base is a directory.
		// e.g., "homelab.md" → check if "homelab" is a directory → redirect to /homelab/
		if strings.HasSuffix(p, ".md") {
			dirPath := strings.TrimSuffix(p, ".md")
			if df, derr := h.fsys.Open(dirPath); derr == nil {
				defer func() { _ = df.Close() }()
				if ds, serr := df.Stat(); serr == nil && ds.IsDir() {
					http.Redirect(w, r, "/"+dirPath+"/", http.StatusMovedPermanently)
					return
				}
			}
		}
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		http.NotFound(w, r)
		return
	}

	data, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

// RawRenderer renders raw/ content into full HTML pages wrapped in the wiki
// chrome. The native renderer's Builder implements it. ok=false means no
// renderer is available (pre-first-build) and the caller serves bytes / the
// plain autoindex instead.
type RawRenderer interface {
	// RenderRawPage renders a markdown source. relPath is vault-relative
	// (e.g. "raw/clippings/x.md"); rawURL is the canonical /raw/ URL.
	RenderRawPage(relPath string, source []byte, modTime time.Time, rawURL string) ([]byte, bool)
	// RenderRawIndex renders a directory listing as a gallery. urlDir is the
	// directory URL with a trailing slash (e.g. "/raw/clippings/").
	RenderRawIndex(urlDir string, entries []render.RawDirEntry) ([]byte, bool)
}

// RawHandler serves raw source files with native MIME types and directory listing.
type RawHandler struct {
	fsys   fs.FS
	render RawRenderer // optional — when set, markdown is rendered for browsers
}

// NewRawHandler creates a handler serving raw files from the vault's raw/
// directory. render is optional: when non-nil, markdown files requested by a
// browser (Accept: text/html, no ?raw=1) are rendered as HTML pages instead of
// served as text/plain. Agents and scripts still get verbatim bytes.
func NewRawHandler(fsys fs.FS, render RawRenderer) *RawHandler {
	return &RawHandler{fsys: fsys, render: render}
}

// Custom MIME types matching the nginx config.
var rawMIMETypes = map[string]string{
	".md":     "text/plain",
	".txt":    "text/plain",
	".html":   "text/html",
	".htm":    "text/html",
	".css":    "text/css",
	".js":     "application/javascript",
	".mjs":    "application/javascript",
	".json":   "application/json",
	".canvas": "application/json",
	".base":   "application/json",
	".pdf":    "application/pdf",
	".png":    "image/png",
	".jpg":    "image/jpeg",
	".jpeg":   "image/jpeg",
	".gif":    "image/gif",
	".svg":    "image/svg+xml",
	".webp":   "image/webp",
	".mp4":    "video/mp4",
	".webm":   "video/webm",
	".mp3":    "audio/mpeg",
	".ogg":    "audio/ogg",
}

func (h *RawHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Strip /raw/ prefix
	p := strings.TrimPrefix(r.URL.Path, "/raw/")
	p = path.Clean(p)
	if p == "." {
		p = ""
	}

	// Reject path traversal
	if strings.HasPrefix(p, "..") {
		http.NotFound(w, r)
		return
	}

	// Try exact file
	if p != "" && h.serveRawFile(w, r, p) {
		return
	}

	// Try path.md fallback
	if p != "" && !strings.HasSuffix(p, ".md") && h.serveRawFile(w, r, p+".md") {
		return
	}

	// Try directory listing
	if h.serveAutoindex(w, r, p) {
		return
	}

	http.NotFound(w, r)
}

func (h *RawHandler) serveRawFile(w http.ResponseWriter, r *http.Request, name string) bool {
	f, err := h.fsys.Open(name)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		return false
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return false
	}

	// Render markdown source docs as HTML pages for browsers, keeping verbatim
	// bytes for agents/scripts (Accept without text/html) and explicit ?raw=1.
	if h.render != nil && strings.HasSuffix(name, ".md") && wantsRenderedHTML(r) {
		if out, ok := h.render.RenderRawPage("raw/"+name, data, stat.ModTime(), "/raw/"+name); ok {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(out)
			return true
		}
		// Render miss (e.g. pre-first-build) → fall through to plain bytes.
	}

	ext := path.Ext(name)
	ct, ok := rawMIMETypes[ext]
	if !ok {
		ct = "application/octet-stream"
	}

	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
	return true
}

// wantsRenderedHTML reports whether a raw request should be rendered to an HTML
// page (markdown → page, directory → gallery) rather than served as bytes / the
// plain autoindex. Browsers send Accept: text/html. htmx (hx-boost) navigations
// send HX-Request: true with Accept: */* — they MUST get the chrome page too, or
// the response lacks the #main element hx-select swaps into and the click lands
// on a blank pane. The explicit ?raw=1 escape hatch forces bytes for anyone.
func wantsRenderedHTML(r *http.Request) bool {
	if r.URL.Query().Get("raw") == "1" {
		return false
	}
	if r.Header.Get("HX-Request") == "true" {
		return true
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

func (h *RawHandler) serveAutoindex(w http.ResponseWriter, r *http.Request, dirPath string) bool {
	name := dirPath
	if name == "" {
		name = "."
	}

	entries, err := fs.ReadDir(h.fsys, name)
	if err != nil {
		return false
	}

	// Sort: directories first, then files, alphabetically
	sort.Slice(entries, func(i, j int) bool {
		di, dj := entries[i].IsDir(), entries[j].IsDir()
		if di != dj {
			return di
		}
		return entries[i].Name() < entries[j].Name()
	})

	urlDir := "/raw/"
	if dirPath != "" {
		urlDir = "/raw/" + dirPath
		if !strings.HasSuffix(urlDir, "/") {
			urlDir += "/"
		}
	}

	// Rendered gallery (wiki chrome + image thumbnails) for browsers. Agents,
	// curl, and ?raw=1 still get the plain autoindex below.
	if h.render != nil && wantsRenderedHTML(r) {
		gallery := make([]render.RawDirEntry, len(entries))
		for i, e := range entries {
			gallery[i] = render.RawDirEntry{Name: e.Name(), IsDir: e.IsDir()}
		}
		if out, ok := h.render.RenderRawIndex(urlDir, gallery); ok {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(out)
			return true
		}
		// Render miss → fall through to the plain autoindex.
	}

	escapedDir := html.EscapeString(urlDir)

	var buf bytes.Buffer
	fmt.Fprintf(&buf, `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Index of %s</title>
<style>
body { font-family: system-ui, sans-serif; max-width: 800px; margin: 2rem auto; padding: 0 1rem; }
h1 { font-size: 1.4rem; font-weight: 600; }
a { text-decoration: none; color: #2563eb; }
a:hover { text-decoration: underline; }
ul { list-style: none; padding: 0; }
li { padding: 0.3rem 0; }
li::before { content: "📁 "; }
li.file::before { content: "📄 "; }
hr { border: none; border-top: 1px solid #e5e7eb; }
</style></head>
<body><h1>Index of %s</h1><hr><ul>
`, escapedDir, escapedDir)

	if dirPath != "" {
		fmt.Fprintf(&buf, "<li><a href=\"%s\">../</a></li>\n", html.EscapeString(path.Dir(strings.TrimSuffix(urlDir, "/"))+"/"))
	}

	for _, entry := range entries {
		name := entry.Name()
		escapedName := html.EscapeString(name)
		if entry.IsDir() {
			fmt.Fprintf(&buf, "<li><a href=\"%s%s/\">%s/</a></li>\n", escapedDir, escapedName, escapedName)
		} else {
			fmt.Fprintf(&buf, "<li class=\"file\"><a href=\"%s%s\">%s</a></li>\n", escapedDir, escapedName, escapedName)
		}
	}

	fmt.Fprintf(&buf, "</ul><hr></body></html>")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(buf.Bytes())
	return true
}

// HealthHandler returns 200 "ok" for health checks.
func HealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok")
	}
}
