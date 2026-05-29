# Renderer

`wiki-server` ships with two HTML renderers behind a single feature flag:
`--renderer` (env `WIKI_RENDERER`, Helm value `renderer:`). They share the
vault, the API, the MCP server, and the auth/middleware chain. Only the
HTML emission step differs.

| Mode | What runs | Source of HTML |
|------|-----------|----------------|
| `quartz` (default) | Quartz v4 + obsidian-headless | `/data/public/*` on disk |
| `native` | In-process Go renderer (`internal/render`) | atomic in-memory snapshot |

This is the operator runbook. See issue [#73] for design context and the
follow-up PR list.

[#73]: https://github.com/jedwards1230/my-wiki/issues/73

## How to flip it

### Helm

```yaml
# values.yaml
renderer: native   # or "quartz"
```

```bash
helm upgrade my-wiki oci://ghcr.io/jedwards1230/charts/my-wiki -f values.yaml
```

ArgoCD picks up the change in ~60–90 s. The pod is recreated (`strategy:
Recreate`) so there is a brief outage; on the cluster this is a few
seconds because the native renderer's initial Build runs synchronously
before `/readyz` flips to 200.

### docker-compose / local

```bash
WIKI_RENDERER=native docker compose up
```

### Binary

```bash
./wiki-server serve --renderer native --vault /path/to/vault
# or
WIKI_RENDERER=native ./wiki-server serve --vault /path/to/vault
```

## What changes per mode

### Init containers and sidecars

The `setup-quartz` init container and the `quartz` `--watch` sidecar are
both gated on `renderer == "quartz"`. In `native` mode the pod has:

- `setup-sync` init (Obsidian Sync handshake) — still runs
- `sync` sidecar (`ob sync --continuous`) — still runs
- `wiki-server` container — runs with `WIKI_RENDERER=native`

The Node + Quartz layers of the image are unused in `native` mode but
still ship. Removing them is a follow-up PR; one image keeps the
rollback story to "flip a single Helm value".

### Rebuild trigger

Both renderers debounce vault writes through `internal/notify.RebuildNotifier`
(2s window). On flush:

- `quartz`: re-runs `npx quartz build` against the live vault.
- `native`: calls `Builder.Build(ctx)` again and atomically swaps the
  `*memfs.FS` snapshot. No mid-rebuild 404 window — readers always see a
  consistent snapshot.

### URLs and behavior

| URL | quartz | native |
|-----|--------|--------|
| `/` (rendered HTML) | Quartz output | Go-rendered HTML |
| `/{path}.md` | vault markdown (text/plain) | same |
| `/raw/{path}` | native bytes from `vault/raw/` | same |
| `/api/*` | REST API | same + `/api/popover/{slug}` + `/api/backlinks` |
| `/_/static/*` | dormant (no client refs) | live (htmx + Alpine + KaTeX + Mermaid + fonts + wiki.css/js) |
| `HX-Request: true` headers | fall-through to full HTML (htmx hx-select) | content-only fragment from `RenderFragment` |

### Static assets

In both modes the embedded asset bundle is mounted under `/_/static/`:

```
/_/static/wiki.css
/_/static/wiki.js
/_/static/vendor/htmx.min.js
/_/static/vendor/alpine.min.js
/_/static/vendor/htmx-idiomorph-ext.min.js
/_/static/vendor/alpine-persist.min.js
/_/static/vendor/katex/...
/_/static/vendor/mermaid.min.js
/_/static/fonts/*.woff2
```

Pinned versions and sha256 hashes live in
`internal/server/assets/MANIFEST.txt`. Regenerate with
`scripts/vendor-assets.sh` after a version bump.

### Transclusion

The native renderer supports full Obsidian-style markdown transclusion
through three `![[…]]` forms:

| Form | Meaning |
|------|---------|
| `![[page]]` | Embed the entire body of `page`. |
| `![[page#Some Heading]]` | Embed only the section starting at `## Some Heading`, ending at the next heading of equal-or-lower depth (or end of file). |
| `![[page#^block-id]]` | Embed only the top-level block (paragraph, callout, list, blockquote, fenced code) bearing `^block-id`. |
| `![[image.png]]` | Image embed — unchanged, served from `/raw/`. |

Rendered output:

```html
<div class="transclude" data-source="{slug}">
  <a class="transclude-source-link" href="/{slug}/" hx-boost="false">From: {Title}</a>
  <div class="transclude-body">…rendered HTML…</div>
</div>
```

Error states are intentionally visible to readers:

- **Missing target** — `<a class="internal broken transclude-missing">[[target]]</a>`
- **Circular** (A transcludes B, B transcludes A) — `<div class="transclude transclude-cycle">circular: [[A]]</div>`
- **Depth limit** (default `MaxTranscludeDepth = 3`) — `<div class="transclude transclude-overflow">depth limit: [[X]]</div>`

The cache is built in `Builder.Build` as a pre-render pass over every
page (parse → AST cache → render), so transclusion targets always
resolve against the current vault snapshot — no stale state across
rebuilds. CSS lives in `internal/server/assets/wiki.css` under the
`/* ----------- Transclusion */` section.

## Rollback

Single value flip — `renderer: quartz` and `helm upgrade`. ArgoCD reverts
in ~60–90 s. The PVC is unchanged across renderer flips (the vault is
the only persistent state).

If `native` is broken for an entire vault (e.g. a goldmark parse error
crashes the build), the pod will fail its initial Build and stay
`NotReady`. The fix is the same flip — set `renderer: quartz` and
upgrade; the Quartz pipeline is unaffected.

## Known issues / follow-ups

These are intentionally cut from this PR to keep the diff reviewable. See
[#73] for the full follow-up list.

- **Graph view + `/api/graph` + cytoscape** — deferred. Follow-up PR #1.
- **Delete Quartz entirely** — follow-up PR #2. Drops Node from the image
  (~700 MB → ~50 MB) and removes the `setup-quartz` / `quartz` containers.
- **Extract obsidian-headless to its own Deployment** — follow-up PR #3.
  RWX PVC required; lets the wiki-server image shed Node.
- **Webfont fallback** — `scripts/vendor-assets.sh` fails hard if
  `gwfh.mranftl.com` is unreachable. If that ever becomes operationally
  painful, the fallback documented inline in the script is to bake bare
  Google Fonts CDN URLs into `wiki.css`. Not worth a runtime fallback;
  the script is hand-run, not in the hot path.
- **Visual regression CI** — follow-up PR #7. No playwright-diff baseline
  yet; cross-renderer visual review is currently manual.

## File map

```
internal/render/
  render.go        goldmark factory + template loader
  obsidian.go      callouts, ==highlight==, %%comment%%, $math$, ^blockref,
                   wikilink resolver, TOC extractor (one file)
  builder.go      Builder.Build → *memfs.Snapshot, parallel render via errgroup
  page.go         Page model + helpers
  backlinks.go    atomic.Pointer reverse index
  sitemap.go      sitemap.xml + index.xml (RSS)
  embed.go        //go:embed templates
  templates/      base.html.tmpl, list.html.tmpl, 404.html.tmpl,
                  sitemap.xml.tmpl, rss.xml.tmpl
  testdata/vault/ synthetic vault exercising every extension
internal/server/assets/
  wiki.css / wiki.js / vendor/ / fonts/ / MANIFEST.txt / assets.go
internal/cli/
  envvars.go       new EnvRenderer constant
  serve.go         --renderer flag + branch
  public_fs.go     new buildNativePublicFS helper
deploy/helm/my-wiki/
  values.yaml      new top-level renderer: quartz
  templates/deployment.yaml  setup-quartz init + quartz sidecar gated
docs/RENDERER.md   this file
```
