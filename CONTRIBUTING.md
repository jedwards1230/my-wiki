# Contributing to my-wiki

`my-wiki` is a Go server that renders an Obsidian vault as a website, combining a native Go renderer (goldmark), an HTTP server, and an MCP interface for agent access.

## Prerequisites

- Go 1.25.6 or later (`go version`)
- `golangci-lint` (for linting — install via `go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest` or your package manager)

## Build, test & lint

```bash
# Build
go build -o wiki-server ./cmd/wiki-server

# Tests — fast unit tests for iteration
go test ./...

# Full test run (what CI runs) — the integration tag adds the stdio subprocess
# test (slower), with the race detector and coverage
go test -tags=integration -v -race -coverprofile=coverage.out -covermode=atomic ./...

# Vet + lint
go vet ./...
golangci-lint run ./...

# Format
gofmt -w .

# Tidy (CI checks this)
go mod tidy
```

## Documentation

Keep documentation current as part of the change, not as a follow-up — update the README and any affected docs in the same PR. A new environment variable belongs in its godoc in `internal/cli/envvars.go` (the canonical inventory); a new CLI subcommand or rendering feature should be reflected in the README and the relevant file under `docs/`.

## Before you open a PR

- Make sure all CI checks pass locally first — run the formatter, linter, and tests.
- For CSS, template, or rendered-output changes, verify visually before a PR: build, then `scripts/seed-vault.sh /tmp/wiki-test-vault` and `WIKI_AUTH_DISABLED=true ./wiki-server serve --vault /tmp/wiki-test-vault --port 9876`, and check key pages in light + dark.

## Branching & commits

- Branch off `main`; never commit directly to `main`.
- Use [Conventional Commits](https://www.conventionalcommits.org/) prefixes (`feat:`, `fix:`, `docs:`, `chore:`, `refactor:`, `test:`, …).
- Sign your commits where possible (`git commit -S`).
- Keep each PR focused; delete dead code rather than commenting it out.

## Pull requests

- Open the PR against `main`.
- Every PR runs CI and an automated code review. Resolve **all** review threads before the PR is merged.
- A PR can be merged once CI is green and all review threads are resolved.

## Releases

Every push to `main` is evaluated for a release automatically:

1. A `semver:major|minor|patch` label on the merged PR sets that bump; `semver:none` skips the release. A label always wins, even on a dev-only diff.
2. With no label, the PR releases as `patch` **unless every changed file is dev-only** (CI workflows, `.claude/`, `CLAUDE.md`/`AGENTS.md`, `CONTRIBUTING*`, `docs/*.md`, `scripts/`, tests and `testdata/`, linter configs). In that case no release is cut. The list is the `DEV_ONLY` array in the detect step of `.github/workflows/release.yml`; anything not listed (source, embedded assets/templates, `docs/openapi.*`, `deploy/`, `README.md`, `plugins/`, `compose.yaml`, dependency manifests) counts as product.
3. If there is no associated PR (direct push) or the changed-file list cannot be read, it releases as `patch` and logs a warning.

Each release publishes an immutable `vX.Y.Z` Docker image to GHCR, a Helm chart to `oci://ghcr.io/jedwards1230/charts/my-wiki`, and a GitHub Release with AI-generated notes.
