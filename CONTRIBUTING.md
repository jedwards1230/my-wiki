# Contributing to my-wiki

`my-wiki` is a Go server that renders an Obsidian vault as a website, combining a native Go renderer (goldmark), an HTTP server, and an MCP interface for agent access.

## Prerequisites

- Go 1.25.6 or later (`go version`)
- `golangci-lint` (for linting — install via `go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest` or your package manager)

## Build, test & lint

```bash
# Build
go build -o wiki-server ./cmd/wiki-server

# Tests — the integration tag includes the stdio subprocess test (slower)
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

Every push to `main` triggers a release automatically. If the merged PR carries a `semver:major` or `semver:minor` label, that bump type is used; otherwise it **defaults to `patch`** — an unlabeled merge always produces a patch release. Each release publishes an immutable `vX.Y.Z` Docker image to GHCR, a Helm chart to `oci://ghcr.io/jedwards1230/charts/my-wiki`, and a GitHub Release with AI-generated notes.
