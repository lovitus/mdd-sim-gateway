#!/bin/sh
set -e

# Ensure clean runtime directory for pcscd
mkdir -p /run/pcscd
rm -f /run/pcscd/pcscd.* /run/pcscd/*.pid /run/pcscd/*.comm 2>/dev/null || true
chmod 755 /run/pcscd

# Ensure 8 distinct VPCD slots are configured in pcscd
cat << 'EOF' > /etc/reader.conf.d/vpcd
FRIENDLYNAME "Virtual PCD 0"
DEVICENAME   /dev/null:0x8C7B
LIBPATH      /usr/lib/pcsc/drivers/serial/libifdvpcd.so
CHANNELID    0x8C7B

FRIENDLYNAME "Virtual PCD 1"
DEVICENAME   /dev/null:0x8C7C
LIBPATH      /usr/lib/pcsc/drivers/serial/libifdvpcd.so
CHANNELID    0x8C7C

FRIENDLYNAME "Virtual PCD 2"
DEVICENAME   /dev/null:0x8C7D
LIBPATH      /usr/lib/pcsc/drivers/serial/libifdvpcd.so
CHANNELID    0x8C7D

FRIENDLYNAME "Virtual PCD 3"
DEVICENAME   /dev/null:0x8C7E
LIBPATH      /usr/lib/pcsc/drivers/serial/libifdvpcd.so
CHANNELID    0x8C7E

FRIENDLYNAME "Virtual PCD 4"
DEVICENAME   /dev/null:0x8C7F
LIBPATH      /usr/lib/pcsc/drivers/serial/libifdvpcd.so
CHANNELID    0x8C7F

FRIENDLYNAME "Virtual PCD 5"
DEVICENAME   /dev/null:0x8C80
LIBPATH      /usr/lib/pcsc/drivers/serial/libifdvpcd.so
CHANNELID    0x8C80

FRIENDLYNAME "Virtual PCD 6"
DEVICENAME   /dev/null:0x8C81
LIBPATH      /usr/lib/pcsc/drivers/serial/libifdvpcd.so
CHANNELID    0x8C81

FRIENDLYNAME "Virtual PCD 7"
DEVICENAME   /dev/null:0x8C82
LIBPATH      /usr/lib/pcsc/drivers/serial/libifdvpcd.so
CHANNELID    0x8C82
EOF

# Start pcsc-lite daemon in background with polkit disabled
echo "[entrypoint] Starting pcscd (PC/SC + VPCD smart card daemon with 8 slots)..."
pcscd --foreground --disable-polkit &
PCSCD_PID=$!
sleep 1

# Execute the main application
echo "[entrypoint] Starting MDD Sim Gateway control plane..."
exec python run.py "$@"
