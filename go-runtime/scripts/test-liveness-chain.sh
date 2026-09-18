#!/usr/bin/env bash
# Disposable, loopback-only protocol/line lifecycle test. No live SIM or paid operation.
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
go -C "$root/providers/vowifi-go" test -c -race -o "$tmp/provider-service.test" ./internal/service
MDD_LIVENESS_SIMULATOR="$tmp/provider-service.test" go -C "$root/go-runtime" test -race -count=1 -timeout 60s -v -run '^TestLivenessRecoveryChainWithRealProviderProcess$' ./internal/runtimereconcile
