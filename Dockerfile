# --- Go builder stage ---
FROM golang:1.25-alpine AS go-builder
WORKDIR /src
COPY go.mod ./
# Copy go.sum only if it exists (no external dependencies currently)
COPY go.sum* ./
COPY cmd/ cmd/
COPY internal/ internal/
ARG BUILD_VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${BUILD_VERSION}" -o /wiki-server ./cmd/wiki-server

# --- Main image ---
FROM node:24-alpine

ARG BUILD_VERSION=dev

RUN apk add --no-cache git coreutils bash ripgrep

# Install obsidian-headless
RUN npm install -g obsidian-headless

# Set up Quartz project
WORKDIR /quartz
RUN git clone --depth 1 https://github.com/jackyzha0/quartz.git . && \
    npm ci --ignore-scripts && \
    rm -rf .git

# Copy custom Quartz configuration and components
COPY quartz/quartz.config.ts ./quartz.config.ts
COPY quartz/quartz.layout.ts ./quartz.layout.ts
COPY quartz/components/RawLink.tsx ./quartz/components/RawLink.tsx
RUN echo 'export { default as RawLink } from "./RawLink"' >> ./quartz/components/index.ts
RUN sed -i "s/%%BUILD_VERSION%%/v${BUILD_VERSION}/" ./quartz.layout.ts

# Install wiki scripts
COPY scripts/ /usr/local/bin/
RUN chmod +x /usr/local/bin/wiki-*

# Copy Go binary from builder
COPY --from=go-builder /wiki-server /usr/local/bin/wiki-server

WORKDIR /data
