#!/bin/sh
set -e

# Ensure clean runtime directory for pcscd
mkdir -p /run/pcscd
rm -f /run/pcscd/pcscd.* /run/pcscd/*.pid /run/pcscd/*.comm 2>/dev/null || true
chmod 755 /run/pcscd

# Dynamically generate 16 VPCD slots in pcscd config (ports 35963..35978)
python3 -c '
ports = [35963 + i for i in range(16)]
with open("/etc/reader.conf.d/vpcd", "w") as f:
    for i, port in enumerate(ports):
        f.write(f"FRIENDLYNAME \"Virtual PCD {i}\"\nDEVICENAME   /dev/null:0x{port:04X}\nLIBPATH      /usr/lib/pcsc/drivers/serial/libifdvpcd.so\nCHANNELID    0x{port:04X}\n\n")
'

# Start pcsc-lite daemon in background
echo "[entrypoint] Starting pcscd (PC/SC + VPCD daemon)..."
pcscd --foreground --disable-polkit &
PCSCD_PID=$!
sleep 1

# Execute the main application
echo "[entrypoint] Starting MDD Sim Gateway control plane..."
exec python run.py "$@"
