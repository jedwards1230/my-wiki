# --- Go builder stage ---
# Runs natively on the build host and cross-compiles to $TARGETARCH,
# avoiding QEMU emulation for multi-arch builds.
FROM --platform=$BUILDPLATFORM golang:1.26.4-alpine AS go-builder
WORKDIR /src

ARG TARGETOS
ARG TARGETARCH

# Cache dependency downloads (layer busted only when go.mod/go.sum change)
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Build binary (layer busted when source changes)
COPY cmd/ cmd/
COPY internal/ internal/
ARG BUILD_VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w -X github.com/jedwards1230/my-wiki/internal/version.Value=${BUILD_VERSION}" -o /wiki-server ./cmd/wiki-server

# --- Main image ---
FROM node:26-alpine

# obsidian-headless provides the `ob` CLI used by the sync sidecar
# container (rarely changes — keep at top for caching).
RUN --mount=type=cache,target=/root/.npm \
    apk add --no-cache git coreutils bash tzdata && \
    npm install -g obsidian-headless

# Create non-root user (uid 1001 — node:alpine already uses uid 1000 for 'node')
RUN adduser -D -u 1001 wiki && mkdir -p /data && chown -R wiki:wiki /data

# Copy Go binary from builder (parallel stage — doesn't block Node layers)
COPY --from=go-builder /wiki-server /usr/local/bin/wiki-server

WORKDIR /data
USER 1001
