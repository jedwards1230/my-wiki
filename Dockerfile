# --- Go builder stage ---
FROM golang:1.25.6-alpine AS go-builder
WORKDIR /src

# Cache dependency downloads (layer busted only when go.mod/go.sum change)
COPY go.mod go.sum ./
RUN go mod download

# Build binary (layer busted when source changes)
COPY cmd/ cmd/
COPY internal/ internal/
ARG BUILD_VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${BUILD_VERSION}" -o /wiki-server ./cmd/wiki-server

# --- Main image ---
FROM node:24-alpine

# System packages + obsidian-headless (rarely changes — keep at top for caching)
RUN apk add --no-cache git coreutils bash && \
    npm install -g obsidian-headless

# Set up Quartz project (only re-runs when Quartz upstream changes)
WORKDIR /quartz
RUN git clone --depth 1 https://github.com/jackyzha0/quartz.git . && \
    npm ci --ignore-scripts && \
    rm -rf .git

# Copy custom Quartz configuration and components
COPY quartz/quartz.config.ts ./quartz.config.ts
COPY quartz/quartz.layout.ts ./quartz.layout.ts
COPY quartz/components/RawLink.tsx ./quartz/components/RawLink.tsx
COPY quartz/components/SidebarToggle.tsx ./quartz/components/SidebarToggle.tsx
COPY quartz/styles/custom.scss ./quartz/styles/custom.scss
RUN echo 'export { default as RawLink } from "./RawLink"' >> ./quartz/components/index.ts && \
    echo 'export { default as SidebarToggle } from "./SidebarToggle"' >> ./quartz/components/index.ts
ARG BUILD_VERSION=dev
ARG BASE_URL
ARG REPO_URL
RUN sed -i "s/%%BUILD_VERSION%%/v${BUILD_VERSION}/" ./quartz.layout.ts && \
    if [ -n "${BASE_URL}" ]; then sed -i "s|%%BASE_URL%%|${BASE_URL}|" ./quartz.config.ts; fi && \
    if [ -n "${REPO_URL}" ]; then sed -i "s|%%REPO_URL%%|${REPO_URL}|" ./quartz.layout.ts; fi

# Create non-root user (uid 1001 — node:alpine already uses uid 1000 for 'node')
RUN adduser -D -u 1001 wiki && mkdir -p /data && chown -R wiki:wiki /data

# Copy Go binary from builder (parallel stage — doesn't block Node layers)
COPY --from=go-builder /wiki-server /usr/local/bin/wiki-server

WORKDIR /data
USER 1001
