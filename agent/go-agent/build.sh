#!/usr/bin/env bash
# ==============================================================================
# MDD Card Agent - 全平台全架构交叉编译脚本 (Multi-Arch Cross-Platform Builder)
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${SCRIPT_DIR}"

echo "==> Building MDD Card Agent for all architectures..."

# 1. macOS (Apple Silicon + Intel)
if [[ "$(uname)" == "Darwin" ]]; then
    echo "  -> Building macOS arm64..."
    CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o mdd-card-agent-darwin-arm64 main.go
    echo "  -> Building macOS amd64..."
    CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o mdd-card-agent-darwin main.go || true
fi

# 2. Linux (amd64, arm64, armv7) & Windows (amd64) via Docker
if command -v docker &>/dev/null; then
    echo "  -> Building Linux (amd64/arm64/armv7) & Windows (amd64) in Docker container..."
    docker run --rm -v "${SCRIPT_DIR}:/src" -w /src golang:1.22-bookworm bash -c '
        apt-get update -qq
        apt-get install -y -qq gcc-mingw-w64 gcc-aarch64-linux-gnu gcc-arm-linux-gnueabihf pkg-config libpcsclite-dev
        
        # Setup multi-arch sysroots
        dpkg --add-architecture arm64
        dpkg --add-architecture armhf
        apt-get update -qq
        
        mkdir -p /tmp/arm64-pkg && cd /tmp/arm64-pkg
        apt-get download libpcsclite1:arm64 libpcsclite-dev:arm64
        mkdir -p /sysroot-arm64
        for deb in *.deb; do dpkg-deb -x "$deb" /sysroot-arm64; done
        
        mkdir -p /tmp/armhf-pkg && cd /tmp/armhf-pkg
        apt-get download libpcsclite1:armhf libpcsclite-dev:armhf
        mkdir -p /sysroot-armhf
        for deb in *.deb; do dpkg-deb -x "$deb" /sysroot-armhf; done
        
        cd /src
        echo "  [1/4] Linux amd64..."
        CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o mdd-card-agent-linux-amd64 main.go
        
        echo "  [2/4] Linux arm64..."
        PKG_CONFIG_PATH=/sysroot-arm64/usr/lib/aarch64-linux-gnu/pkgconfig \
        PKG_CONFIG_SYSROOT_DIR=/sysroot-arm64 \
        CGO_ENABLED=1 GOOS=linux GOARCH=arm64 CC=aarch64-linux-gnu-gcc \
        go build -ldflags="-s -w" -o mdd-card-agent-linux-arm64 main.go
        
        echo "  [3/4] Linux armv7..."
        PKG_CONFIG_PATH=/sysroot-armhf/usr/lib/arm-linux-gnueabihf/pkgconfig \
        PKG_CONFIG_SYSROOT_DIR=/sysroot-armhf \
        CGO_ENABLED=1 GOOS=linux GOARCH=arm GOARM=7 CC=arm-linux-gnueabihf-gcc \
        go build -ldflags="-s -w" -o mdd-card-agent-linux-armv7 main.go
        
        echo "  [4/4] Windows amd64..."
        CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=x86_64-w64-mingw32-gcc \
        go build -ldflags="-s -w" -o mdd-card-agent-windows-amd64.exe main.go
    '
fi

echo "==> Build complete! Output binaries:"
ls -lh mdd-card-agent*
