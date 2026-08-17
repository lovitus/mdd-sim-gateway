#!/bin/sh
set -e

# Clean any stale sockets or pid files
pkill -9 pcscd 2>/dev/null || true
mkdir -p /run/pcscd
rm -rf /run/pcscd/* 2>/dev/null || true
chmod 755 /run/pcscd

# Standard 2-slot VPCD config
cat << 'EOF' > /etc/reader.conf.d/vpcd
FRIENDLYNAME "Virtual PCD"
DEVICENAME   /dev/null:0x8C7B
LIBPATH      /usr/lib/pcsc/drivers/serial/libifdvpcd.so
CHANNELID    0x8C7B
EOF

# Start pcsc-lite daemon in background
echo "[entrypoint] Starting pcscd (PC/SC + VPCD daemon)..."
pcscd --foreground &
PCSCD_PID=$!
sleep 1

# Execute the main application
echo "[entrypoint] Starting MDD Sim Gateway control plane..."
exec python run.py "$@"
