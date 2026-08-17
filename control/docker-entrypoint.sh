#!/bin/sh
set -e

# Clean any stale sockets or pid files
pkill -9 pcscd 2>/dev/null || true
mkdir -p /run/pcscd
rm -rf /run/pcscd/* 2>/dev/null || true
chmod 755 /run/pcscd

# Dynamically generate 16 VPCD slots in pcscd config (8 entries x 2 slots = 16 ports 35963..35978)
python3 -c '
base_ports = [35963 + i * 2 for i in range(8)]
with open("/etc/reader.conf.d/vpcd", "w") as f:
    for p in base_ports:
        f.write(f"FRIENDLYNAME \"Virtual PCD\"\nDEVICENAME   /dev/null:0x{p:04X}\nLIBPATH      /usr/lib/pcsc/drivers/serial/libifdvpcd.so\nCHANNELID    0x{p:04X}\n\n")
'

# Start pcsc-lite daemon in background
echo "[entrypoint] Starting pcscd (PC/SC + VPCD daemon)..."
pcscd --foreground &
PCSCD_PID=$!
sleep 1

# Execute the main application
echo "[entrypoint] Starting MDD Sim Gateway control plane..."
exec python run.py "$@"
