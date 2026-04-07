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
)

// StaticHandler serves Quartz static site output with try_files logic:
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

	// Try path.html (Quartz generates about.html, served at /about)
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

	f, err := h.fsys.Open(p)
	if err != nil {
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

// RawHandler serves raw source files with native MIME types and directory listing.
type RawHandler struct {
	fsys fs.FS
}

// NewRawHandler creates a handler serving raw files from the vault's raw/ directory.
func NewRawHandler(fsys fs.FS) *RawHandler {
	return &RawHandler{fsys: fsys}
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
