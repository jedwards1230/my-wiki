#!/bin/bash
# Stop hook: Block if golangci-lint reports issues on modified Go files.
set -euo pipefail

INPUT=$(cat)

# Prevent infinite loops
if [ "$(echo "$INPUT" | jq -r '.stop_hook_active')" = "true" ]; then
  exit 0
fi

# Skip lint hook inside worktrees — worktree branches are validated by GitHub CI.
TOPLEVEL="$(git rev-parse --show-toplevel 2>/dev/null)" || exit 0
GIT_COMMON="$(git rev-parse --git-common-dir 2>/dev/null)" || exit 0
MAIN_ROOT="$(cd "$(dirname "$GIT_COMMON")" 2>/dev/null && pwd)" || exit 0
if [ "$TOPLEVEL" != "$MAIN_ROOT" ]; then
  exit 0
fi
cd "$TOPLEVEL" || exit 0

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

# Resolve via go env — works on macOS and Linux regardless of PATH during hook execution
GOLANGCI=$(go env GOPATH 2>/dev/null)/bin/golangci-lint
if [ ! -x "$GOLANGCI" ]; then
  exit 0
fi

# Build list of packages containing modified Go files
PACKAGES=$(echo "$MODIFIED" | grep '\.go$' | xargs -I{} dirname {} | sort -u | sed 's|^|./|' | tr '\n' ' ')

# shellcheck disable=SC2086
if $GOLANGCI run --timeout 60s $PACKAGES 2>/tmp/wiki-lint-out.txt; then
  exit 0
fi
if grep -q "configuration file for golangci-lint v2 with golangci-lint v1" /tmp/wiki-lint-out.txt 2>/dev/null; then
  exit 0
fi
echo "golangci-lint issues. Fix before finishing:" >&2
cat /tmp/wiki-lint-out.txt >&2
exit 2
