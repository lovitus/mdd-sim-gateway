#!/bin/sh
# The only supported workflow status reader. It waits before the first
# request so automatic continuation cannot create a tight query loop.
set -eu

[ "$#" -eq 1 ] || {
  printf '%s\n' "usage: $0 RUN_ID_OR_COMMIT_SHA" >&2
  exit 2
}
case "$1" in ''|*[!0-9a-fA-F]*) exit 2;; esac

script_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
exec /Users/fanli/.codex/tools/github-run-status.sh "$1"
