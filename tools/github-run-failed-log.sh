#!/bin/sh
# The only supported failed-job log reader. It performs one bounded read.
set -eu

[ "$#" -eq 1 ] || {
	printf '%s\n' "usage: $0 RUN_ID" >&2
	exit 2
}
exec /Users/fanli/.codex/tools/github-run-failed-log.sh "$1"
