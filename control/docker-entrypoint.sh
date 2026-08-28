#!/bin/sh
set -e
umask 077

# This container owns only its ephemeral configuration socket. Never clean persistent config,
# state, certificates, databases or lifecycle evidence here.
mkdir -p "${MDD_RUNTIME_DIR:-/run/mdd-sim-gateway}/engine-config"
chmod 0700 "${MDD_RUNTIME_DIR:-/run/mdd-sim-gateway}/engine-config"
rm -f "${MDD_RUNTIME_DIR:-/run/mdd-sim-gateway}/engine-config/engine-config.sock"

# /run/pcscd belongs to the host daemon and is mounted only so Control can act as a client.
# Never remove its sockets/PIDs and never start a competing daemon in this container.
[ -S /run/pcscd/pcscd.comm ] || {
    echo "[entrypoint] ERROR: host pcscd socket is unavailable" >&2
    exit 1
}

# Execute the main application
echo "[entrypoint] Starting MDD Sim Gateway control plane..."
exec python run.py "$@"
