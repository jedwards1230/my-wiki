#!/bin/bash
# Stop hook: Block if golangci-lint reports issues on modified Go files.
set -euo pipefail

INPUT=$(cat)

# Prevent infinite loops
if [ "$(echo "$INPUT" | jq -r '.stop_hook_active')" = "true" ]; then
  exit 0
fi

cd "$(git rev-parse --show-toplevel)"

# Check for Go files modified in working tree, staged, or recent commits on branch
MODIFIED=$(
  {
    git diff --name-only 2>/dev/null
    git diff --name-only --cached 2>/dev/null
    git diff --name-only "$(git merge-base HEAD main 2>/dev/null || echo HEAD~1)" HEAD 2>/dev/null
  } | sort -u
)
[ -z "$MODIFIED" ] && exit 0

GO_CHANGED=$(echo "$MODIFIED" | grep '\.go$' | head -1 || true)
[ -z "$GO_CHANGED" ] && exit 0

# Find golangci-lint: check GOPATH/bin first, then PATH
GOLANGCI=""
GOPATH_DIR=$(go env GOPATH 2>/dev/null || true)
if [ -n "$GOPATH_DIR" ] && [ -x "$GOPATH_DIR/bin/golangci-lint" ]; then
  GOLANGCI="$GOPATH_DIR/bin/golangci-lint"
elif command -v golangci-lint &>/dev/null; then
  GOLANGCI=golangci-lint
else
  echo "WARNING: golangci-lint not found — skipping lint checks" >&2
  echo "Install: https://golangci-lint.run/welcome/install/" >&2
  exit 0
fi

# Build list of packages containing modified Go files
PACKAGES=$(echo "$MODIFIED" | grep '\.go$' | xargs -I{} dirname {} | sort -u | sed 's|^|./|' | tr '\n' ' ')

# shellcheck disable=SC2086
if $GOLANGCI run --timeout 60s $PACKAGES 2>/tmp/lint-out.txt; then
  exit 0
fi

# Handle v1/v2 config mismatch gracefully
if grep -q "configuration file for golangci-lint v2 with golangci-lint v1" /tmp/lint-out.txt 2>/dev/null; then
  echo "WARNING: golangci-lint v1 installed but config requires v2 — skipping" >&2
  exit 0
fi

echo "golangci-lint issues. Fix before finishing:" >&2
cat /tmp/lint-out.txt >&2
exit 2
