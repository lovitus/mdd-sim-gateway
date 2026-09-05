#!/bin/sh
# The only supported workflow status reader. It waits before the first
# request so automatic continuation cannot create a tight query loop.
set -eu

[ "$#" -eq 1 ] || {
  printf '%s\n' "usage: $0 RUN_ID" >&2
  exit 2
}
case "$1" in ''|*[!0-9]*) exit 2;; esac

script_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
exec "$script_dir/wait-github-run.sh" "$1" 30
