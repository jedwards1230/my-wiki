#!/bin/bash
# Stop hook: Block if Go files were modified but go vet or go test fails.
set -euo pipefail

INPUT=$(cat)

# Prevent infinite loops
if [ "$(echo "$INPUT" | jq -r '.stop_hook_active')" = "true" ]; then
  exit 0
fi

# Always run from the git root so relative paths and go tooling work correctly
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

# go vet first (fast, catches type errors and suspicious constructs)
if ! VET_RESULT=$(go vet ./... 2>&1); then
  echo "go vet failed. Fix these issues before finishing:" >&2
  echo "$VET_RESULT" >&2
  exit 2
fi

# go test (with timeout to prevent hangs)
if ! TEST_RESULT=$(go test -timeout 120s -count=1 ./... 2>&1); then
  echo "go test failed. Fix failing tests before finishing:" >&2
  echo "$TEST_RESULT" >&2
  exit 2
fi

exit 0
