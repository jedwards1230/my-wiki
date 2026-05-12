package render

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jedwards1230/my-wiki/internal/memfs"
	"github.com/jedwards1230/my-wiki/internal/vault"
	"github.com/jedwards1230/my-wiki/internal/version"
	"golang.org/x/sync/errgroup"
)

// BuilderConfig configures a Builder.
type BuilderConfig struct {
	// Vault is the source vault — read-only here; never mutated by Build.
	Vault *vault.Vault

	// SiteTitle is shown in the header and templates.
	SiteTitle string

	// BaseURL is the canonical site URL used for sitemap.xml/index.xml
	// (e.g. https://wiki.lilbro.cloud). Trailing slashes are stripped.
	BaseURL string

	// Logger is optional; nil falls back to slog.Default().
	Logger *slog.Logger
}

// Builder renders an entire vault into a memfs.Snapshot. One Builder per
// process; Build() can be called repeatedly (on filesystem-change rebuilds).
type Builder struct {
	cfg          BuilderConfig
	logger       *slog.Logger
	backlinkIdx  *BacklinkIndex
	mu           sync.Mutex // guards lastSnapshot / lastPages / lastRenderer
	lastSnapshot *memfs.Snapshot
	lastPages    map[string]*Page
	lastRenderer *Renderer
}

// NewBuilder constructs a Builder. BaseURL is normalized; SiteTitle
// defaults to "My Wiki" if empty.
func NewBuilder(cfg BuilderConfig) *Builder {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.SiteTitle == "" {
		cfg.SiteTitle = "My Wiki"
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &Builder{
		cfg:         cfg,
		logger:      cfg.Logger,
		backlinkIdx: NewBacklinkIndex(),
	}
}

// BacklinkIndex returns the index — exposed so the API handler can read
// backlinks without re-walking pages.
func (b *Builder) BacklinkIndex() *BacklinkIndex { return b.backlinkIdx }

// PageBySlug returns the cached *Page for a slug, or nil if missing.
// Used by the HX-Request fragment shim to re-execute the content template.
func (b *Builder) PageBySlug(slug string) *Page {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastPages[strings.ToLower(slug)]
}

// Snapshot returns the most recently built snapshot, or nil if Build has
// not yet been called.
func (b *Builder) Snapshot() *memfs.Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastSnapshot
}

// RenderFragment re-executes the content template for the page matching
// urlPath. Returns the rendered bytes and ok=true on hit; ok=false when
// the path doesn't resolve to a known slug (caller falls back to the
// static handler).
//
// urlPath is the request URL path, e.g. "/meta/schema/". Trailing
// slashes and the leading slash are stripped to derive the slug.
func (b *Builder) RenderFragment(urlPath string) ([]byte, bool) {
	slug := strings.Trim(urlPath, "/")
	if slug == "" {
		slug = "index"
	}
	b.mu.Lock()
	r := b.lastRenderer
	p := b.lastPages[strings.ToLower(slug)]
	allPages := make([]*Page, 0, len(b.lastPages))
	for _, pg := range b.lastPages {
		allPages = append(allPages, pg)
	}
	b.mu.Unlock()
	if r == nil || p == nil {
		return nil, false
	}
	// Build a per-request explorer tree with this page's branch active.
	// htmx fragment swaps don't replace the sidebar, so this is only used
	// when the user lands on a page directly (e.g. opening a deep URL in
	// a new tab) — once per session.
	explorer := cloneExplorerTree(BuildExplorerTree(allPages, ""))
	markActiveRoots(explorer, slug)
	td := TemplateData{Page: p, SiteTitle: b.cfg.SiteTitle, ActivePath: p.RelativeURL, Version: version.Value, BaseURL: b.cfg.BaseURL, Explorer: explorer}
	buf, err := r.RenderFragment(p, td)
	if err != nil {
		return nil, false
	}
	return buf, true
}

// Build walks the vault, renders every page in parallel, and returns a
// fresh memfs.Snapshot containing the full site tree:
//
//	/{slug}/index.html  for each markdown page
//	/sitemap.xml        full URL list
//	/index.xml          RSS feed
//	/404.html           static error page
//	/tags/{tag}/index.html  per-tag listings
//
// Build is safe to call concurrently with reads against the snapshot
// previously returned — the snapshot is replaced atomically by the caller
// (typically memfs.FS.Store).
func (b *Builder) Build(ctx context.Context) (*memfs.Snapshot, error) {
	start := time.Now()
	v := b.cfg.Vault

	// 1. Slug index for wikilink resolution.
	slugs, err := v.BuildSlugIndex()
	if err != nil {
		return nil, fmt.Errorf("build slug index: %w", err)
	}

	// 2. Renderer with the live slug map. We rebuild per Build() so
	// wikilink resolution always sees the current vault state.
	r, err := NewRenderer(slugs)
	if err != nil {
		return nil, fmt.Errorf("new renderer: %w", err)
	}

	// 3. Enumerate vault pages (filters out raw/ private/ .obsidian/).
	pages, err := v.FindWikiPages()
	if err != nil {
		return nil, fmt.Errorf("find wiki pages: %w", err)
	}

	// 4. PASS 1 — parse every page to AST. Transclusion needs every
	// target's AST available before any page's render runs, so we do a
	// full parse pass first and then a render pass. Both passes run in
	// parallel; the cache published between them is the join point.
	//
	// Per-page state (raw source + ModTime + relpath) is collected here
	// so pass 2 doesn't have to re-read the filesystem.
	type parsedInfo struct {
		relPath string
		source  []byte
		modTime time.Time
		parsed  *ParsedPage
		links   []string
	}
	parseResults := make([]parsedInfo, len(pages))

	pg, pgCtx := errgroup.WithContext(ctx)
	pg.SetLimit(runtime.GOMAXPROCS(0))
	for i, page := range pages {
		i, page := i, page
		pg.Go(func() error {
			if err := pgCtx.Err(); err != nil {
				return err
			}
			rel, err := filepath.Rel(v.Dir, page)
			if err != nil {
				return fmt.Errorf("relpath %s: %w", page, err)
			}
			rel = filepath.ToSlash(rel)
			data, err := os.ReadFile(page)
			if err != nil {
				return fmt.Errorf("read %s: %w", rel, err)
			}
			info, err := os.Stat(page)
			if err != nil {
				return fmt.Errorf("stat %s: %w", rel, err)
			}
			pp, _, _ := r.ParsePage(rel, data)
			links, _ := vault.ExtractWikilinks(page)
			parseResults[i] = parsedInfo{
				relPath: rel,
				source:  data,
				modTime: info.ModTime(),
				parsed:  pp,
				links:   links,
			}
			return nil
		})
	}
	if err := pg.Wait(); err != nil {
		return nil, fmt.Errorf("parse pass: %w", err)
	}

	// Publish the AST cache + slug titles so the render pass can resolve
	// transclusions.
	transcludeCache := make(map[string]*ParsedPage, len(parseResults))
	slugTitles := make(map[string]string, len(parseResults))
	for _, pi := range parseResults {
		if pi.parsed == nil {
			continue
		}
		key := strings.ToLower(pi.parsed.Slug)
		transcludeCache[key] = pi.parsed
		slugTitles[key] = pi.parsed.Title
	}
	r.WithTransclusion(transcludeCache, slugTitles)

	// 5. PASS 2 — render every page in parallel, capped at GOMAXPROCS.
	// Each render uses a per-page goldmark configured with a scoped
	// TranscludeSource so the visited set + depth are page-local.
	type rendered struct {
		page    *Page
		relPath string
		links   []string
	}
	results := make([]rendered, len(parseResults))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.GOMAXPROCS(0))
	for i, pi := range parseResults {
		i, pi := i, pi
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			p, err := r.RenderPage(pi.relPath, pi.source, pi.modTime)
			if err != nil {
				return fmt.Errorf("render %s: %w", pi.relPath, err)
			}
			results[i] = rendered{page: p, relPath: pi.relPath, links: pi.links}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	// 5. Backlink index — built from the full result set, replaces the
	// atomic pointer so /api/backlinks readers see the new map.
	all := make([]*Page, 0, len(results))
	linkMap := make(map[string][]string, len(results))
	pageMap := make(map[string]*Page, len(results))
	for _, r := range results {
		if r.page == nil {
			continue
		}
		all = append(all, r.page)
		linkMap[r.page.Slug] = r.links
		pageMap[strings.ToLower(r.page.Slug)] = r.page
	}
	backlinks := BuildBacklinks(all, linkMap, slugs)
	b.backlinkIdx.Replace(backlinks)
	// Stitch the backlinks back onto pages so the per-page sidebar has them.
	for _, p := range all {
		p.Backlinks = backlinks[p.Slug]
	}

	// 6. Build the snapshot — full-page HTML for each, plus aggregate
	// artifacts.
	snap := memfs.NewSnapshot()
	now := time.Now()

	// Build the explorer tree once from the full page set (sort + folder
	// reconstruction is the expensive part). Each page's TemplateData
	// gets a cheap deep-copy of the base tree with that page's ancestor
	// chain marked open / active — O(N) per page instead of re-running
	// the full O(N log N) build.
	explorerBase := BuildExplorerTree(all, "")

	for _, p := range all {
		// Per-page explorer tree with this page's branch marked active.
		explorer := cloneExplorerTree(explorerBase)
		markActiveRoots(explorer, p.Slug)
		td := TemplateData{
			Page:       p,
			SiteTitle:  b.cfg.SiteTitle,
			ActivePath: p.RelativeURL,
			BuildDate:  now.Format("2006-01-02"),
			Version:    version.Value,
			BaseURL:    b.cfg.BaseURL,
			Explorer:   explorer,
		}
		buf, err := r.RenderToBytes(p, td)
		if err != nil {
			return nil, fmt.Errorf("render page %s: %w", p.Slug, err)
		}
		key := p.Slug + "/index.html"
		if p.Slug == "index" {
			key = "index.html"
		}
		if err := snap.AddFile(key, buf, p.Modified); err != nil {
			return nil, fmt.Errorf("snapshot add %s: %w", key, err)
		}
	}

	// 7. Tag pages — collect tag → pages, render one listing per tag.
	tagPages := groupByTag(all)
	for tag, ps := range tagPages {
		listSlug := "tags/" + tag
		page := &Page{
			Title:           "Tag: " + tag,
			Slug:            listSlug,
			RelativeURL:     "/" + listSlug + "/",
			BreadcrumbItems: BuildBreadcrumb(listSlug),
			Description:     fmt.Sprintf("Pages tagged #%s", tag),
			IsListPage:      true,
			ListEntries:     pagesToEntries(ps),
		}
		td := TemplateData{
			Page:       page,
			SiteTitle:  b.cfg.SiteTitle,
			ActivePath: page.RelativeURL,
			Version:    version.Value,
			BaseURL:    b.cfg.BaseURL,
			Explorer:   explorerBase,
		}
		buf, err := r.RenderList(page, td)
		if err != nil {
			return nil, fmt.Errorf("render tag %s: %w", tag, err)
		}
		if err := snap.AddFile(listSlug+"/index.html", buf, now); err != nil {
			return nil, err
		}
	}

	// 8. Sitemap + RSS.
	if sm, err := BuildSitemap(all, b.cfg.BaseURL); err == nil {
		if err := snap.AddFile("sitemap.xml", sm, now); err != nil {
			return nil, err
		}
	} else {
		b.logger.Warn("sitemap render failed", "error", err)
	}
	if rss, err := BuildRSS(all, b.cfg.BaseURL, b.cfg.SiteTitle, "Recent updates"); err == nil {
		if err := snap.AddFile("index.xml", rss, now); err != nil {
			return nil, err
		}
	} else {
		b.logger.Warn("rss render failed", "error", err)
	}

	// 9. 404 page. Same chrome as a regular page so the user has a
	// sidebar/footer to navigate away from the dead URL.
	notFoundData := TemplateData{
		Page: &Page{
			Title:       "Not found",
			Slug:        "404",
			RelativeURL: "/404/",
			Description: "That page isn't here.",
			ContentHTML: template.HTML(`<p>That page isn't here. Try <a href="/">the home page</a> or search above.</p>`),
		},
		SiteTitle: b.cfg.SiteTitle,
		Version:   version.Value,
		BaseURL:   b.cfg.BaseURL,
		Explorer:  explorerBase,
	}
	if buf, err := r.Render404(notFoundData); err == nil {
		_ = snap.AddFile("404.html", buf, now)
	}

	// 10. Publish the page map + renderer for fragment re-exec.
	b.mu.Lock()
	b.lastSnapshot = snap
	b.lastPages = pageMap
	b.lastRenderer = r
	b.mu.Unlock()

	b.logger.Info("native renderer build complete",
		"pages", len(all),
		"tags", len(tagPages),
		"duration", time.Since(start),
		"bytes", snap.Bytes(),
	)
	return snap, nil
}

// BuildExplorerTree builds the collapsible file-tree for the left sidebar.
// It walks `all` pages, reconstructs the folder hierarchy, and marks the
// path from root to `activeSlug` as open. The tree is sorted folders-first
// then alphabetically within each group, mirroring Quartz's explorer.
func BuildExplorerTree(all []*Page, activeSlug string) []*ExplorerNode {
	// Collect slug → title from pages.
	titles := make(map[string]string, len(all))
	for _, p := range all {
		titles[p.Slug] = p.Title
	}

	// root is a virtual folder whose Children become the top-level nodes.
	root := &ExplorerNode{IsFolder: true}
	nodeMap := map[string]*ExplorerNode{"": root}

	// Ensure a folder node exists for every ancestor segment of `slug`.
	var ensureFolder func(slug string) *ExplorerNode
	ensureFolder = func(slug string) *ExplorerNode {
		if n, ok := nodeMap[slug]; ok {
			return n
		}
		parentSlug := ""
		name := slug
		if idx := strings.LastIndexByte(slug, '/'); idx >= 0 {
			parentSlug = slug[:idx]
			name = slug[idx+1:]
		}
		parent := ensureFolder(parentSlug)

		// Determine URL: if there's an index page (slug/index) use the
		// folder URL; otherwise the folder is virtual (no link).
		url := ""
		if _, ok := titles[slug+"/index"]; ok {
			url = "/" + slug + "/"
		}

		n := &ExplorerNode{
			Name:     humanizeSegment(name),
			Slug:     slug,
			URL:      url,
			IsFolder: true,
		}
		nodeMap[slug] = n
		parent.Children = append(parent.Children, n)
		return n
	}

	// Add a leaf node for each page. Folders (index pages) are represented
	// by the parent folder node rather than a separate leaf.
	for _, p := range all {
		// Skip folder index pages and the root index — they are represented
		// by the folder node itself.
		if p.Slug == "index" {
			continue
		}
		parts := strings.Split(p.Slug, "/")
		isIndex := parts[len(parts)-1] == "index"

		if isIndex {
			// A folder index page: ensure the folder node exists (ensureFolder
			// above will wire the URL). No separate leaf.
			folderSlug := strings.Join(parts[:len(parts)-1], "/")
			ensureFolder(folderSlug)
			continue
		}

		// Regular leaf page: ensure parent folder exists, then add leaf.
		parentSlug := ""
		if len(parts) > 1 {
			parentSlug = strings.Join(parts[:len(parts)-1], "/")
		}
		parent := ensureFolder(parentSlug)

		leaf := &ExplorerNode{
			Name: func() string {
				if t, ok := titles[p.Slug]; ok && t != "" {
					return t
				}
				return humanizeSegment(parts[len(parts)-1])
			}(),
			Slug:     p.Slug,
			URL:      p.RelativeURL,
			IsFolder: false,
		}
		parent.Children = append(parent.Children, leaf)
	}

	// Sort each level: folders first, then leaves; alphabetically within
	// each group by Name.
	var sortChildren func(node *ExplorerNode)
	sortChildren = func(node *ExplorerNode) {
		sort.Slice(node.Children, func(i, j int) bool {
			a, b := node.Children[i], node.Children[j]
			if a.IsFolder != b.IsFolder {
				return a.IsFolder
			}
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		})
		for _, child := range node.Children {
			if child.IsFolder {
				sortChildren(child)
			}
		}
	}
	sortChildren(root)

	// Mark the active node and open all ancestors.
	if activeSlug != "" && activeSlug != "index" {
		markActive(root, activeSlug)
	}

	return root.Children
}

// cloneExplorerTree returns a deep-copy of the given roots so per-page
// IsOpen / IsActive markers don't leak across pages when reusing the
// base tree.
func cloneExplorerTree(roots []*ExplorerNode) []*ExplorerNode {
	if len(roots) == 0 {
		return nil
	}
	out := make([]*ExplorerNode, len(roots))
	for i, n := range roots {
		c := *n
		c.IsOpen = false
		c.IsActive = false
		c.Children = cloneExplorerTree(n.Children)
		out[i] = &c
	}
	return out
}

// markActiveRoots is the top-level entry point for marking the active
// branch on a cloned tree. Walks each root subtree.
func markActiveRoots(roots []*ExplorerNode, activeSlug string) {
	for _, r := range roots {
		if markActive(r, activeSlug) {
			return
		}
	}
}

// markActive walks the tree and sets IsActive on the matching leaf/folder.
// Returns true when any child in this subtree is or contains the active slug,
// so the caller can set IsOpen on the parent. Folder-index pages (slug
// ending in "/index") match the parent folder node since the tree
// represents folder indexes as the folder node itself rather than a leaf.
func markActive(node *ExplorerNode, activeSlug string) bool {
	// Strip the trailing "/index" or bare "index" so folder-index pages
	// (rendered as the folder node) get marked correctly.
	folderSlug := strings.TrimSuffix(activeSlug, "/index")
	if folderSlug == "index" {
		folderSlug = ""
	}
	if node.Slug == activeSlug || node.Slug == folderSlug ||
		node.URL == "/"+activeSlug+"/" || node.URL == "/"+folderSlug+"/" {
		node.IsActive = true
		node.IsOpen = node.IsFolder
		return true
	}
	for _, child := range node.Children {
		if markActive(child, activeSlug) {
			node.IsOpen = true
			return true
		}
	}
	return false
}

// groupByTag returns tag → pages, lowercased. Pages with no tags are skipped.
func groupByTag(pages []*Page) map[string][]*Page {
	out := make(map[string][]*Page)
	for _, p := range pages {
		for _, t := range p.Tags {
			if t == "" {
				continue
			}
			out[t] = append(out[t], p)
		}
	}
	// Sort each tag's page list for reproducible output.
	for _, list := range out {
		sort.Slice(list, func(i, j int) bool { return list[i].Title < list[j].Title })
	}
	return out
}

// pagesToEntries converts pages to ListEntry rows for the listing template.
func pagesToEntries(pages []*Page) []ListEntry {
	out := make([]ListEntry, 0, len(pages))
	for _, p := range pages {
		out = append(out, ListEntry{
			Title:       p.Title,
			URL:         p.RelativeURL,
			Description: p.Description,
			Tags:        p.Tags,
		})
	}
	return out
}
