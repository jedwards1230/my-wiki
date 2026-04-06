FROM node:24-alpine

ARG BUILD_VERSION=dev

RUN apk add --no-cache git coreutils

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

WORKDIR /data
