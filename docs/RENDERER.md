# Renderer

`wiki-server` renders HTML in-process with a native Go renderer (`internal/render`). It shares the vault, API, MCP server, and auth/middleware chain — only HTML emission lives here. Rendered output is held in an atomic in-memory snapshot, never written to disk.

Design context: issue [#73].

[#73]: https://github.com/jedwards1230/my-wiki/issues/73

## Pipeline

goldmark parses markdown → `html/template` wraps it in the page shell → Chroma (via goldmark-highlighting) handles syntax highlighting with CSS classes (light "github", dark "github-dark").

`Builder.Build(ctx)` renders every page in parallel (errgroup) into a `*memfs.Snapshot` and atomically swaps the live `*memfs.FS`. Vault writes are debounced by `internal/notify.RebuildNotifier` (2s window); on flush the builder re-renders and swaps the snapshot — no mid-rebuild 404, readers always see a consistent view.

`WIKI_BASE_URL` (Helm `baseURL`) feeds sitemap, RSS, and canonical/OpenGraph link generation. Unset → relative links only.

## URLs

| URL | Serves |
|-----|--------|
| `/` and `/{path}/` | Go-rendered HTML |
| `/{path}.md` | vault markdown (text/plain) |
| `/raw/{path}` | native bytes from `vault/raw/` |
| `/api/*` | REST API (incl. `/api/popover/{slug}`, `/api/backlinks`, `/api/graph.json`) |
| `/_/static/*` | embedded asset bundle (htmx + Alpine + KaTeX + Mermaid + fonts + wiki.css/js) |
| `HX-Request: true` | content-only fragment from `RenderFragment` |

## Static assets

The embedded bundle mounts under `/_/static/`:

```
/_/static/wiki.css, wiki.js
/_/static/vendor/{htmx,alpine,htmx-idiomorph-ext,alpine-persist}.min.js
/_/static/vendor/katex/..., /_/static/vendor/mermaid.min.js
/_/static/fonts/*.woff2
```

Assets are embedded via `//go:embed` in `internal/server/assets/assets.go`. Pinned versions + sha256 hashes: `internal/server/assets/MANIFEST.txt`. Regenerate with `scripts/vendor-assets.sh` after a version bump.

The graph view ships as a lightweight canvas widget in `wiki.js` (no d3/cytoscape); it reads node/link data from `/api/graph.json` (`internal/api/graph.go`).

## Transclusion

Full Obsidian-style `![[…]]` transclusion:

| Form | Meaning |
|------|---------|
| `![[page]]` | Embed entire body of `page`. |
| `![[page#Some Heading]]` | Embed the section under `## Some Heading`, ending at the next equal-or-lower heading (or EOF). |
| `![[page#^block-id]]` | Embed the top-level block (paragraph, callout, list, blockquote, fenced code) bearing `^block-id`. |
| `![[image.png]]` | Image embed — served from `/raw/`. |

Rendered output:
```html
<div class="transclude" data-source="{slug}">
  <a class="transclude-source-link" href="/{slug}/" hx-boost="false">From: {Title}</a>
  <div class="transclude-body">…rendered HTML…</div>
</div>
```

Error states are intentionally visible:
- **Missing target** — `<a class="internal broken transclude-missing">[[target]]</a>`
- **Circular** — `<div class="transclude transclude-cycle">circular: [[A]]</div>`
- **Depth limit** (default `MaxTranscludeDepth = 3`) — `<div class="transclude transclude-overflow">depth limit: [[X]]</div>`

The cache is built in `Builder.Build` as a pre-render pass over every page (parse → AST cache → render), so targets resolve against the current snapshot. CSS: `wiki.css` under `/* ----------- Transclusion */`.

## File map

```
internal/render/
  render.go        goldmark factory + template loader
  obsidian.go      callouts, ==highlight==, %%comment%%, $math$, ^blockref, wikilinks, TOC
  builder.go       Builder.Build → *memfs.Snapshot, parallel render via errgroup
  page.go          Page model + helpers
  backlinks.go     atomic.Pointer reverse index
  sitemap.go       sitemap.xml + index.xml (RSS)
  embed.go         //go:embed templates
  templates/       base/list/404 .html.tmpl, sitemap.xml.tmpl, rss.xml.tmpl
  testdata/vault/  synthetic vault exercising every extension
internal/api/  graph.go (GET /api/graph.json)
internal/server/assets/  wiki.css / wiki.js / vendor/ / fonts/ / MANIFEST.txt / assets.go
internal/cli/  public_fs.go (buildNativePublicFS)
deploy/helm/my-wiki/  values.yaml (baseURL:), templates/deployment.yaml
docs/RENDERER.md  this file
```
