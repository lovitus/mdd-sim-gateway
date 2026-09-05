#!/bin/sh
# Start one bounded GitHub run wait as a detached job.
set -eu

usage() {
  printf '%s\n' "usage: $0 RUN_ID OUTPUT_DIR [INTERVAL_SECONDS]" >&2
  exit 2
}

[ "$#" -ge 2 ] && [ "$#" -le 3 ] || usage
run_id=$1
output_dir=$2
interval=${3:-30}
case "$run_id" in ''|*[!0-9]*) usage;; esac
case "$interval" in ''|*[!0-9]*) usage;; esac
[ "$interval" -ge 30 ] && [ "$interval" -le 60 ] || usage

mkdir -p "$output_dir"
script_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
wait_script="$script_dir/wait-github-run.sh"
pid_file="$output_dir/pid"
result_file="$output_dir/result.json"
error_file="$output_dir/error.log"
exit_file="$output_dir/exit-code"

if [ -s "$pid_file" ]; then
  old_pid=$(cat "$pid_file")
  if kill -0 "$old_pid" 2>/dev/null; then
    printf '%s\n' "$old_pid"
    exit 0
  fi
fi

runner=''
if command -v setsid >/dev/null 2>&1; then
  runner=setsid
fi
nohup ${runner:+"$runner"} sh -c '
  set +e
  sh "$1" "$2" "$3" >"$4" 2>"$5"
  status=$?
  printf "%s\n" "$status" >"$6"
' _ "$wait_script" "$run_id" "$interval" "$result_file" "$error_file" "$exit_file" </dev/null >/dev/null 2>&1 &
pid=$!
printf '%s\n' "$pid" >"$pid_file"
printf '%s\n' "$pid"
