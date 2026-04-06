FROM node:24-alpine

RUN apk add --no-cache git coreutils

# Install obsidian-headless
RUN npm install -g obsidian-headless

# Set up Quartz project
WORKDIR /quartz
RUN git clone --depth 1 https://github.com/jackyzha0/quartz.git . && \
    npm ci --ignore-scripts && \
    rm -rf .git

WORKDIR /data
