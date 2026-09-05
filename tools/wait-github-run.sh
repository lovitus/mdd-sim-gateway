#!/bin/sh
# Wait for one GitHub Actions run without emitting intermediate polling output.
# The caller receives exactly one terminal record or one timeout record.
set -eu

usage() {
  printf '%s\n' "usage: $0 RUN_ID [INTERVAL_SECONDS]" >&2
  exit 2
}

[ "$#" -ge 1 ] && [ "$#" -le 2 ] || usage
run_id=$1
interval=${2:-30}
case "$run_id" in ''|*[!0-9]*) usage;; esac
case "$interval" in ''|*[!0-9]*) usage;; esac
[ "$interval" -ge 30 ] && [ "$interval" -le 60 ] || usage

command -v gh >/dev/null 2>&1 || { printf '%s\n' 'wait-github-run: gh is required' >&2; exit 127; }
command -v jq >/dev/null 2>&1 || { printf '%s\n' 'wait-github-run: jq is required' >&2; exit 127; }

started=$(date +%s)
deadline=$((started + 600))
while :; do
  payload=$(gh run view "$run_id" --json status,conclusion,headSha --jq '{status,conclusion,headSha}' 2>/dev/null) || {
    printf '%s\n' 'wait-github-run: unable to read run state' >&2
    exit 3
  }
  status=$(printf '%s\n' "$payload" | jq -r '.status // ""')
  if [ "$status" = completed ]; then
    printf '%s\n' "$payload"
    conclusion=$(printf '%s\n' "$payload" | jq -r '.conclusion // ""')
    [ "$conclusion" = success ] && exit 0
    exit 1
  fi
  now=$(date +%s)
  [ "$now" -lt "$deadline" ] || {
    printf '%s\n' "{\"status\":\"timeout\",\"run_id\":\"$run_id\"}"
    exit 124
  }
  remaining=$((deadline - now))
  sleep_for=$interval
  [ "$sleep_for" -le "$remaining" ] || sleep_for=$remaining
  sleep "$sleep_for"
done
