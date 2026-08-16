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
    cat << 'EOF' | sudo aarch64-linux-gnu-gcc -I/usr/include/PCSC -shared -Wl,-soname,libpcsclite.so.1 -o /sysroot-arm64/lib/libpcsclite.so -x c -
#include <winscard.h>
#include <reader.h>
LONG SCardEstablishContext(DWORD a, LPCVOID b, LPCVOID c, LPSCARDCONTEXT d) { return 0; }
LONG SCardIsValidContext(SCARDCONTEXT a) { return 0; }
LONG SCardCancel(SCARDCONTEXT a) { return 0; }
LONG SCardReleaseContext(SCARDCONTEXT a) { return 0; }
LONG SCardListReaders(SCARDCONTEXT a, LPCSTR b, LPSTR c, LPDWORD d) { return 0; }
LONG SCardListReaderGroups(SCARDCONTEXT a, LPSTR b, LPDWORD c) { return 0; }
LONG SCardGetStatusChange(SCARDCONTEXT a, DWORD b, LPSCARD_READERSTATE c, DWORD d) { return 0; }
LONG SCardConnect(SCARDCONTEXT a, LPCSTR b, DWORD c, DWORD d, LPSCARDHANDLE e, LPDWORD f) { return 0; }
LONG SCardDisconnect(SCARDHANDLE a, DWORD b) { return 0; }
LONG SCardReconnect(SCARDHANDLE a, DWORD b, DWORD c, DWORD d, LPDWORD e) { return 0; }
LONG SCardBeginTransaction(SCARDHANDLE a) { return 0; }
LONG SCardEndTransaction(SCARDHANDLE a, DWORD b) { return 0; }
LONG SCardStatus(SCARDHANDLE a, LPSTR b, LPDWORD c, LPDWORD d, LPDWORD e, LPBYTE f, LPDWORD g) { return 0; }
LONG SCardTransmit(SCARDHANDLE a, LPCSCARD_IO_REQUEST b, LPCBYTE c, DWORD d, LPSCARD_IO_REQUEST e, LPBYTE f, LPDWORD g) { return 0; }
LONG SCardControl(SCARDHANDLE a, DWORD b, LPCVOID c, DWORD d, LPVOID e, DWORD f, LPDWORD g) { return 0; }
LONG SCardGetAttrib(SCARDHANDLE a, DWORD b, LPBYTE c, LPDWORD d) { return 0; }
LONG SCardSetAttrib(SCARDHANDLE a, DWORD b, LPCBYTE c, DWORD d) { return 0; }
char *pcsc_stringify_error(LONG a) { return ""; }
EOF

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
    CGO_ENABLED=1 GOOS=linux GOARCH=arm64 CC=aarch64-linux-gnu-gcc \
    go build -ldflags="-s -w" -o "${OUTPUT_FILE}" main.go

elif [ "${TARGET_OS}" = "linux" ] && [ "${TARGET_ARCH}" = "arm" ]; then
    sudo apt-get update -qq || true
    sudo apt-get install -y -qq gcc-arm-linux-gnueabihf pkg-config libpcsclite-dev
    
    sudo mkdir -p /sysroot-armhf/lib/pkgconfig
    cat << 'EOF' | sudo arm-linux-gnueabihf-gcc -I/usr/include/PCSC -shared -Wl,-soname,libpcsclite.so.1 -o /sysroot-armhf/lib/libpcsclite.so -x c -
#include <winscard.h>
#include <reader.h>
LONG SCardEstablishContext(DWORD a, LPCVOID b, LPCVOID c, LPSCARDCONTEXT d) { return 0; }
LONG SCardIsValidContext(SCARDCONTEXT a) { return 0; }
LONG SCardCancel(SCARDCONTEXT a) { return 0; }
LONG SCardReleaseContext(SCARDCONTEXT a) { return 0; }
LONG SCardListReaders(SCARDCONTEXT a, LPCSTR b, LPSTR c, LPDWORD d) { return 0; }
LONG SCardListReaderGroups(SCARDCONTEXT a, LPSTR b, LPDWORD c) { return 0; }
LONG SCardGetStatusChange(SCARDCONTEXT a, DWORD b, LPSCARD_READERSTATE c, DWORD d) { return 0; }
LONG SCardConnect(SCARDCONTEXT a, LPCSTR b, DWORD c, DWORD d, LPSCARDHANDLE e, LPDWORD f) { return 0; }
LONG SCardDisconnect(SCARDHANDLE a, DWORD b) { return 0; }
LONG SCardReconnect(SCARDHANDLE a, DWORD b, DWORD c, DWORD d, LPDWORD e) { return 0; }
LONG SCardBeginTransaction(SCARDHANDLE a) { return 0; }
LONG SCardEndTransaction(SCARDHANDLE a, DWORD b) { return 0; }
LONG SCardStatus(SCARDHANDLE a, LPSTR b, LPDWORD c, LPDWORD d, LPDWORD e, LPBYTE f, LPDWORD g) { return 0; }
LONG SCardTransmit(SCARDHANDLE a, LPCSCARD_IO_REQUEST b, LPCBYTE c, DWORD d, LPSCARD_IO_REQUEST e, LPBYTE f, LPDWORD g) { return 0; }
LONG SCardControl(SCARDHANDLE a, DWORD b, LPCVOID c, DWORD d, LPVOID e, DWORD f, LPDWORD g) { return 0; }
LONG SCardGetAttrib(SCARDHANDLE a, DWORD b, LPBYTE c, LPDWORD d) { return 0; }
LONG SCardSetAttrib(SCARDHANDLE a, DWORD b, LPCBYTE c, DWORD d) { return 0; }
char *pcsc_stringify_error(LONG a) { return ""; }
EOF

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
    CGO_ENABLED=1 GOOS=linux GOARCH=arm GOARM="${ARM_VER}" CC=arm-linux-gnueabihf-gcc \
    go build -ldflags="-s -w" -o "${OUTPUT_FILE}" main.go

elif [ "${TARGET_OS}" = "windows" ]; then
    sudo apt-get update -qq || true
    sudo apt-get install -y -qq gcc-mingw-w64-x86-64
    CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=x86_64-w64-mingw32-gcc \
    go build -ldflags="-s -w" -o "${OUTPUT_FILE}" main.go
fi

echo "==> Successfully built ${OUTPUT_FILE} ($(ls -lh "${OUTPUT_FILE}" | awk '{print $5}'))"
