#!/bin/bash
# Hook: SessionStart
# In Claude Code Web (ephemeral containers), installs required tools.
# In local devcontainers, tools are pre-installed.

set +e

if [ "${CLAUDE_CODE_REMOTE:-}" = "true" ]; then
  echo "[session-start] Running in Claude Code Web" >&2

  export PATH="/usr/local/go/bin:/root/go/bin:${PATH}"

  if ! command -v jq &>/dev/null; then
    echo "[session-start] Installing jq..." >&2
    apt-get update >/dev/null 2>&1 && apt-get install -y jq >/dev/null 2>&1
  fi

  if ! command -v go &>/dev/null; then
    echo "[session-start] Installing Go..." >&2
    GO_VERSION=1.25.6
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" | tar -xz -C /usr/local || \
      echo "[session-start] WARNING: Go install failed" >&2
  fi

  if ! command -v golangci-lint &>/dev/null; then
    echo "[session-start] Installing golangci-lint..." >&2
    GOLANGCI_VERSION=2.11.3
    curl -fsSL "https://github.com/golangci/golangci-lint/releases/download/v${GOLANGCI_VERSION}/golangci-lint-${GOLANGCI_VERSION}-linux-amd64.tar.gz" \
      -o /tmp/golangci-lint.tar.gz \
      && tar -xzf /tmp/golangci-lint.tar.gz -C /tmp \
      && mv "/tmp/golangci-lint-${GOLANGCI_VERSION}-linux-amd64/golangci-lint" /usr/local/bin/ \
      && rm -rf /tmp/golangci-lint* \
      || echo "[session-start] WARNING: golangci-lint install failed" >&2
  fi
fi

exit 0
