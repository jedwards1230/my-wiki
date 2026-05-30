# Renderer

`wiki-server` ships two HTML renderers behind one flag: `--renderer` (env `WIKI_RENDERER`, Helm `renderer:`). They share the vault, API, MCP server, and auth/middleware chain — only HTML emission differs.

| Mode | What runs | Source of HTML |
|------|-----------|----------------|
| `quartz` (default) | Quartz v4 + obsidian-headless | `/data/public/*` on disk |
| `native` | In-process Go renderer (`internal/render`) | atomic in-memory snapshot |

Design context: issue [#73].

[#73]: https://github.com/jedwards1230/my-wiki/issues/73

## How to flip it

```yaml
# Helm values.yaml
renderer: native   # or "quartz"
```

```bash
helm upgrade my-wiki oci://ghcr.io/jedwards1230/charts/my-wiki -f values.yaml
# or locally:
WIKI_RENDERER=native docker compose up
./wiki-server serve --renderer native --vault /path/to/vault
```

ArgoCD picks up the change in ~60–90s. The pod is recreated (`strategy: Recreate`), so there's a brief outage — a few seconds, since the native renderer's initial Build runs synchronously before `/readyz` flips to 200.

## What changes per mode

### Init containers and sidecars

The `setup-quartz` init and the `quartz` `--watch` sidecar are gated on `renderer == "quartz"`. In `native` mode the pod runs: `setup-sync` init + `sync` sidecar (`ob sync --continuous`) + `wiki-server` with `WIKI_RENDERER=native`. The Node/Quartz image layers are unused in `native` mode but still ship (removing them is a follow-up; keeping them makes rollback a single Helm value flip).

### Rebuild trigger

Both renderers debounce vault writes via `internal/notify.RebuildNotifier` (2s window). On flush:
- `quartz`: re-runs `npx quartz build` against the live vault.
- `native`: calls `Builder.Build(ctx)` and atomically swaps the `*memfs.FS` snapshot — no mid-rebuild 404; readers always see a consistent snapshot.

### URLs and behavior

| URL | quartz | native |
|-----|--------|--------|
| `/` (rendered HTML) | Quartz output | Go-rendered HTML |
| `/{path}.md` | vault markdown (text/plain) | same |
| `/raw/{path}` | native bytes from `vault/raw/` | same |
| `/api/*` | REST API | same + `/api/popover/{slug}` + `/api/backlinks` |
| `/_/static/*` | dormant (no client refs) | live (htmx + Alpine + KaTeX + Mermaid + fonts + wiki.css/js) |
| `HX-Request: true` | fall-through to full HTML | content-only fragment from `RenderFragment` |

### Static assets

Both modes mount the embedded bundle under `/_/static/`:

```
/_/static/wiki.css, wiki.js
/_/static/vendor/{htmx,alpine,htmx-idiomorph-ext,alpine-persist}.min.js
/_/static/vendor/katex/..., /_/static/vendor/mermaid.min.js
/_/static/fonts/*.woff2
```

Pinned versions + sha256 hashes: `internal/server/assets/MANIFEST.txt`. Regenerate with `scripts/vendor-assets.sh` after a version bump.

### Transclusion (native only)

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

## Rollback

Single value flip — set `renderer: quartz` and `helm upgrade`; ArgoCD reverts in ~60–90s. The PVC is unchanged across flips (the vault is the only persistent state). If `native` breaks an entire vault (e.g. a goldmark parse error fails the initial Build, leaving the pod `NotReady`), the fix is the same flip — the Quartz pipeline is unaffected.

## Known issues / follow-ups

Cut from this PR to keep the diff reviewable; full list in [#73].

- **Graph view + `/api/graph` + cytoscape** — deferred (PR #1).
- **Delete Quartz entirely** (PR #2) — drops Node from the image (~700 MB → ~50 MB), removes `setup-quartz`/`quartz` containers.
- **Extract obsidian-headless to its own Deployment** (PR #3) — needs RWX PVC; lets wiki-server shed Node.
- **Webfont fallback** — `scripts/vendor-assets.sh` fails hard if `gwfh.mranftl.com` is unreachable. Inline fallback: bake Google Fonts CDN URLs into `wiki.css`. Hand-run script, not the hot path.
- **Visual regression CI** (PR #7) — no playwright-diff baseline yet; cross-renderer review is manual.

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
internal/server/assets/  wiki.css / wiki.js / vendor/ / fonts/ / MANIFEST.txt / assets.go
internal/cli/  envvars.go (EnvRenderer), serve.go (--renderer flag), public_fs.go (buildNativePublicFS)
deploy/helm/my-wiki/  values.yaml (renderer:), templates/deployment.yaml (gated init/sidecar)
docs/RENDERER.md  this file
```
