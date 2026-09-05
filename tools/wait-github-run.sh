#!/bin/sh
# Compatibility wrapper. The implementation is global and synchronous.
set -eu
[ "$#" -ge 1 ] && [ "$#" -le 2 ] || {
  printf '%s\n' "usage: $0 RUN_ID_OR_COMMIT_SHA [ignored-interval]" >&2
  exit 2
}
case "$1" in ''|*[!0-9a-fA-F]*) exit 2;; esac
exec /Users/fanli/.codex/tools/github-run-status.sh "$1"
