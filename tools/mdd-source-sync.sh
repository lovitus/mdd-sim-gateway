#!/bin/sh
# Safely publish source code to a deployment checkout without ever touching runtime data.
# This deliberately has no --delete mode: source cleanup must not be able to erase a host's
# configuration, database, certificates, call history, recovery evidence, or deploy records.
set -eu

if [ "$#" -ne 2 ]; then
  echo "usage: $0 SOURCE_DIR SSH_HOST:DEPLOY_DIR" >&2
  exit 64
fi

SOURCE_DIR=$1
TARGET=$2
case "$SOURCE_DIR" in
  /*) ;;
  *) echo "SOURCE_DIR must be absolute" >&2; exit 64 ;;
esac
case "$TARGET" in
  *:/data|*:/data/*|*:*/data|*:*/data/*|\
  *:/etc/mdd-sim-gateway|*:/etc/mdd-sim-gateway/*|\
  *:/var/lib/mdd-sim-gateway|*:/var/lib/mdd-sim-gateway/*|\
  *:/var/lib/mdd-sim-gateway-artifacts|*:/var/lib/mdd-sim-gateway-artifacts/*|\
  *:/run/mdd-sim-gateway|*:/run/mdd-sim-gateway/*)
    echo "refusing a runtime data destination" >&2; exit 64 ;;
esac
[ -d "$SOURCE_DIR" ] || { echo "SOURCE_DIR does not exist" >&2; exit 66; }

exec rsync -az --checksum --partial --delay-updates --protect-args \
  --exclude '/.git/' \
  --exclude '/data/' \
  --exclude '/.pytest_cache/' \
  --exclude '/__pycache__/' \
  --exclude '**/__pycache__/' \
  --exclude '/node_modules/' \
  --exclude '/webui/node_modules/' \
  --exclude '/Claude.md' \
  --exclude '/tools/ML307-Manager.ps1' \
  "${SOURCE_DIR%/}/" "$TARGET"
