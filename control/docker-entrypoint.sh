#!/bin/sh
set -e

# Ensure clean runtime directory for pcscd
mkdir -p /run/pcscd
rm -f /run/pcscd/pcscd.* /run/pcscd/*.pid /run/pcscd/*.comm 2>/dev/null || true
chmod 755 /run/pcscd

# One libifdvpcd reader definition compiled with 16 slots opens ports 35963..35978.
# Do not generate one stanza per slot: every stanza would itself open 16 sockets and the
# overlapping listeners would make reader enumeration depend on startup order.
VPCD_CONFIG=/etc/reader.conf.d/vpcd
if [ ! -f "$VPCD_CONFIG" ] || [ "$(grep -c '^FRIENDLYNAME' "$VPCD_CONFIG")" -ne 1 ]; then
    echo "[entrypoint] ERROR: expected exactly one 16-slot VPCD reader definition" >&2
    exit 1
fi

# Start pcsc-lite daemon in background with polkit disabled
echo "[entrypoint] Starting pcscd (PC/SC + 16-slot VPCD on 35963..35978)..."
pcscd --foreground --disable-polkit &
PCSCD_PID=$!
sleep 1



# Execute the main application
echo "[entrypoint] Starting MDD Sim Gateway control plane..."
exec python run.py "$@"
