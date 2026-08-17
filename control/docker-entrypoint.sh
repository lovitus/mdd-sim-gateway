#!/bin/sh
set -e

# Clean any stale sockets or pid files
pkill -9 pcscd 2>/dev/null || true
mkdir -p /run/pcscd
rm -rf /run/pcscd/* 2>/dev/null || true
chmod 755 /run/pcscd

# Dynamically generate 16 VPCD virtual slots in pcscd config
# 8 base reader entries x 2 slots each = 16 ports: 35963 to 35978
python3 -c '
base_ports = [35963 + i * 2 for i in range(8)]
with open("/etc/reader.conf.d/vpcd", "w") as f:
    for i, p in enumerate(base_ports):
        name = "Virtual PCD" if i == 0 else f"Virtual PCD {i}"
        f.write(
            f"FRIENDLYNAME \"{name}\"\n"
            f"DEVICENAME   /dev/null:{p}\n"
            f"LIBPATH      /usr/lib/pcsc/drivers/serial/libifdvpcd.so\n"
            f"CHANNELID    0x{p:04X}\n\n"
        )
'

# Start pcsc-lite daemon in background without polkit dependency
export PCSCLITE_NO_POLKIT=1
echo "[entrypoint] Starting pcscd (PC/SC + VPCD daemon)..."
pcscd --foreground --disable-polkit &
PCSCD_PID=$!
sleep 1

# Execute the main application
echo "[entrypoint] Starting MDD Sim Gateway control plane..."
exec python run.py "$@"
