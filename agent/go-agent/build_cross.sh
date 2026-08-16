#!/usr/bin/env bash
set -euo pipefail

TARGET_OS="${1:-linux}"
TARGET_ARCH="${2:-amd64}"
OUTPUT_FILE="${3:-mdd-card-agent}"
ARM_VER="${4:-7}"

echo "==> Building MDD Card Agent for ${TARGET_OS}/${TARGET_ARCH} -> ${OUTPUT_FILE}..."

export DEBIAN_FRONTEND=noninteractive

if [ "${TARGET_OS}" = "linux" ] && [ "${TARGET_ARCH}" = "amd64" ]; then
    sudo apt-get update -qq || true
    sudo apt-get install -y -qq pkg-config libpcsclite-dev
    CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o "${OUTPUT_FILE}" main.go

elif [ "${TARGET_OS}" = "linux" ] && [ "${TARGET_ARCH}" = "arm64" ]; then
    sudo apt-get update -qq || true
    sudo apt-get install -y -qq gcc-aarch64-linux-gnu pkg-config libpcsclite-dev
    
    sudo mkdir -p /sysroot-arm64/lib/pkgconfig
    cat << 'EOF' | sudo aarch64-linux-gnu-gcc -fPIC -shared -Wl,-soname,libpcsclite.so.1 -o /sysroot-arm64/lib/libpcsclite.so -x c -
void SCardEstablishContext(void) {}
void SCardIsValidContext(void) {}
void SCardCancel(void) {}
void SCardReleaseContext(void) {}
void SCardListReaders(void) {}
void SCardListReaderGroups(void) {}
void SCardGetStatusChange(void) {}
void SCardConnect(void) {}
void SCardDisconnect(void) {}
void SCardReconnect(void) {}
void SCardBeginTransaction(void) {}
void SCardEndTransaction(void) {}
void SCardStatus(void) {}
void SCardTransmit(void) {}
void SCardControl(void) {}
void SCardGetAttrib(void) {}
void SCardSetAttrib(void) {}
void pcsc_stringify_error(void) {}
EOF
    sudo ln -sf /sysroot-arm64/lib/libpcsclite.so /sysroot-arm64/lib/libpcsclite.so.1

    cat << 'EOF' | sudo tee /sysroot-arm64/lib/pkgconfig/libpcsclite.pc > /dev/null
prefix=/sysroot-arm64
libdir=/sysroot-arm64/lib
includedir=/usr/include/PCSC
Name: libpcsclite
Description: PC/SC Lite
Version: 2.0.0
Libs: -L/sysroot-arm64/lib -lpcsclite
Cflags: -I/usr/include/PCSC
EOF

    PKG_CONFIG_PATH=/sysroot-arm64/lib/pkgconfig \
    CGO_CFLAGS="-I/usr/include/PCSC" \
    CGO_LDFLAGS="-L/sysroot-arm64/lib -lpcsclite" \
    CGO_ENABLED=1 GOOS=linux GOARCH=arm64 CC=aarch64-linux-gnu-gcc \
    go build -ldflags="-s -w" -o "${OUTPUT_FILE}" main.go

elif [ "${TARGET_OS}" = "linux" ] && [ "${TARGET_ARCH}" = "arm" ]; then
    sudo apt-get update -qq || true
    sudo apt-get install -y -qq gcc-arm-linux-gnueabihf pkg-config libpcsclite-dev
    
    sudo mkdir -p /sysroot-armhf/lib/pkgconfig
    cat << 'EOF' | sudo arm-linux-gnueabihf-gcc -fPIC -shared -Wl,-soname,libpcsclite.so.1 -o /sysroot-armhf/lib/libpcsclite.so -x c -
void SCardEstablishContext(void) {}
void SCardIsValidContext(void) {}
void SCardCancel(void) {}
void SCardReleaseContext(void) {}
void SCardListReaders(void) {}
void SCardListReaderGroups(void) {}
void SCardGetStatusChange(void) {}
void SCardConnect(void) {}
void SCardDisconnect(void) {}
void SCardReconnect(void) {}
void SCardBeginTransaction(void) {}
void SCardEndTransaction(void) {}
void SCardStatus(void) {}
void SCardTransmit(void) {}
void SCardControl(void) {}
void SCardGetAttrib(void) {}
void SCardSetAttrib(void) {}
void pcsc_stringify_error(void) {}
EOF
    sudo ln -sf /sysroot-armhf/lib/libpcsclite.so /sysroot-armhf/lib/libpcsclite.so.1

    cat << 'EOF' | sudo tee /sysroot-armhf/lib/pkgconfig/libpcsclite.pc > /dev/null
prefix=/sysroot-armhf
libdir=/sysroot-armhf/lib
includedir=/usr/include/PCSC
Name: libpcsclite
Description: PC/SC Lite
Version: 2.0.0
Libs: -L/sysroot-armhf/lib -lpcsclite
Cflags: -I/usr/include/PCSC
EOF

    PKG_CONFIG_PATH=/sysroot-armhf/lib/pkgconfig \
    CGO_CFLAGS="-I/usr/include/PCSC" \
    CGO_LDFLAGS="-L/sysroot-armhf/lib -lpcsclite" \
    CGO_ENABLED=1 GOOS=linux GOARCH=arm GOARM="${ARM_VER}" CC=arm-linux-gnueabihf-gcc \
    go build -ldflags="-s -w" -o "${OUTPUT_FILE}" main.go

elif [ "${TARGET_OS}" = "windows" ]; then
    sudo apt-get update -qq || true
    sudo apt-get install -y -qq gcc-mingw-w64-x86-64
    CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=x86_64-w64-mingw32-gcc \
    go build -ldflags="-s -w" -o "${OUTPUT_FILE}" main.go
fi

echo "==> Successfully built ${OUTPUT_FILE} ($(ls -lh "${OUTPUT_FILE}" | awk '{print $5}'))"
