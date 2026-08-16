#!/usr/bin/env bash
# ==============================================================================
# MDD Card Agent - 全平台交叉编译脚本 (Cross-Platform Builder)
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${SCRIPT_DIR}"

echo "==> Building MDD Card Agent for all platforms..."

# 1. macOS (Apple Silicon + Intel)
if [[ "$(uname)" == "Darwin" ]]; then
    echo "  -> Building macOS arm64..."
    CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o mdd-card-agent-darwin-arm64 main.go
    echo "  -> Building macOS amd64..."
    CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o mdd-card-agent-darwin main.go || true
fi

# 2. Linux (x86_64) & Windows (x86_64) via Docker
if command -v docker &>/dev/null; then
    echo "  -> Building Linux amd64 and Windows amd64 in Docker container..."
    docker run --rm -v "${SCRIPT_DIR}:/src" -w /src golang:1.22-bookworm bash -c '
        apt-get update -qq && apt-get install -y -qq libpcsclite-dev gcc-mingw-w64
        CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o mdd-card-agent-linux-amd64 main.go
        CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=x86_64-w64-mingw32-gcc go build -ldflags="-s -w" -o mdd-card-agent-windows-amd64.exe main.go
    '
fi

echo "==> Build complete! Output binaries:"
ls -lh mdd-card-agent*
