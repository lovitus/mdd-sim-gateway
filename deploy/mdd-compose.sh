#!/bin/sh
# Production Compose entrypoint. It validates isolated roots before building or replacing only
# the Control service; dynamically managed Engine containers are never part of this operation.
set -eu

SELF_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
REPO_DIR=$(CDPATH= cd -- "$SELF_DIR/.." && pwd -P)
ENV_FILE="${MDD_ENV_FILE:-/etc/mdd-sim-gateway/runtime.env}"
COMMAND="${1:-validate}"
LEGACY_ROOT="${2:-}"

[ -r "$ENV_FILE" ] || { echo "missing deployment environment: $ENV_FILE" >&2; exit 2; }
# shellcheck disable=SC1090
. "$ENV_FILE"

require_root() {
  name="$1"; value="$2"
  case "$value" in /*) ;; *) echo "$name must be an absolute path" >&2; exit 2 ;; esac
  case "$value/" in "$REPO_DIR/"*) echo "$name must be outside the source checkout" >&2; exit 2 ;; esac
}

require_root MDD_CONFIG_ROOT "${MDD_CONFIG_ROOT:-}"
require_root MDD_STATE_ROOT "${MDD_STATE_ROOT:-}"
require_root MDD_ARTIFACT_ROOT "${MDD_ARTIFACT_ROOT:-}"
require_root MDD_RUNTIME_ROOT "${MDD_RUNTIME_ROOT:-}"

[ "$MDD_CONFIG_ROOT" != "$MDD_STATE_ROOT" ] && \
[ "$MDD_CONFIG_ROOT" != "$MDD_ARTIFACT_ROOT" ] && \
[ "$MDD_CONFIG_ROOT" != "$MDD_RUNTIME_ROOT" ] && \
[ "$MDD_STATE_ROOT" != "$MDD_ARTIFACT_ROOT" ] && \
[ "$MDD_STATE_ROOT" != "$MDD_RUNTIME_ROOT" ] && \
[ "$MDD_ARTIFACT_ROOT" != "$MDD_RUNTIME_ROOT" ] || {
  echo "config, state, artifact and runtime roots must be distinct" >&2; exit 2;
}

prepare_roots() {
  install -d -m 0700 "$MDD_CONFIG_ROOT" "$MDD_STATE_ROOT" \
    "$MDD_ARTIFACT_ROOT" "$MDD_RUNTIME_ROOT"
}

compose() {
  docker compose --env-file "$ENV_FILE" -f "$REPO_DIR/compose.production.yaml" "$@"
}

case "$COMMAND" in
  validate)
    compose config --quiet
    ;;
  up-control)
    [ "$(id -u)" -eq 0 ] || { echo "up-control requires root" >&2; exit 2; }
    prepare_roots
    compose config --quiet
    compose build control
    compose up --no-deps -d control
    ;;
  status)
    compose ps
    ;;
  plan-migration)
    [ -n "$LEGACY_ROOT" ] || { echo "plan-migration requires LEGACY_ROOT" >&2; exit 2; }
    python3 "$REPO_DIR/deploy/migrate-data-layout.py" \
      --legacy-root "$LEGACY_ROOT" --config-root "$MDD_CONFIG_ROOT" \
      --state-root "$MDD_STATE_ROOT" --artifact-root "$MDD_ARTIFACT_ROOT"
    ;;
  migrate-legacy)
    [ "$(id -u)" -eq 0 ] || { echo "migrate-legacy requires root" >&2; exit 2; }
    [ -n "$LEGACY_ROOT" ] || { echo "migrate-legacy requires LEGACY_ROOT" >&2; exit 2; }
    python3 "$REPO_DIR/deploy/migrate-data-layout.py" --execute \
      --legacy-root "$LEGACY_ROOT" --config-root "$MDD_CONFIG_ROOT" \
      --state-root "$MDD_STATE_ROOT" --artifact-root "$MDD_ARTIFACT_ROOT"
    install -d -m 0700 "$MDD_RUNTIME_ROOT"
    ;;
  *)
    echo "usage: $0 [validate|up-control|status|plan-migration LEGACY_ROOT|migrate-legacy LEGACY_ROOT]" >&2
    exit 2
    ;;
esac
