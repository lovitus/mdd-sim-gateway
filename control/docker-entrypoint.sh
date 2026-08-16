#!/bin/sh
set -e

# Ensure clean runtime directory for pcscd
mkdir -p /run/pcscd
rm -f /run/pcscd/pcscd.* /run/pcscd/*.pid /run/pcscd/*.comm 2>/dev/null || true
chmod 755 /run/pcscd

# Start pcsc-lite daemon in background with polkit disabled
echo "[entrypoint] Starting pcscd (PC/SC + VPCD smart card daemon)..."
pcscd --foreground --disable-polkit &
PCSCD_PID=$!
sleep 1



# Execute the main application
echo "[entrypoint] Starting MDD Sim Gateway control plane..."
exec python run.py "$@"
