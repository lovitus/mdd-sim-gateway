#!/bin/sh
set -eu

usage() {
	printf '%s\n' "usage: install-macos-agent.sh preflight|install|rollback --candidate DIR --config FILE --state DIR [--restart-command CMD]"
	exit 2
}

action=
candidate=
config=
state=
restart_command=
while [ "$#" -gt 0 ]; do
	case "$1" in
	preflight|install|rollback) [ -z "$action" ] || usage; action=$1; shift ;;
	--candidate) candidate=${2-}; shift 2 ;;
	--config) config=${2-}; shift 2 ;;
	--state) state=${2-}; shift 2 ;;
	--restart-command) restart_command=${2-}; shift 2 ;;
	*) usage ;;
	esac
done
[ -n "$action" ] && [ -n "$candidate" ] && [ -n "$config" ] && [ -n "$state" ] || usage
[ "$(uname -s)" = Darwin ] || { printf '%s\n' 'macOS is required' >&2; exit 1; }
[ -d "$candidate" ] && [ -f "$candidate/mdd-agent" ] || { printf '%s\n' 'candidate mdd-agent is missing' >&2; exit 1; }
[ -f "$config" ] || { printf '%s\n' 'Agent config is missing' >&2; exit 1; }

hash_file() { shasum -a 256 "$1" | awk '{print $1}'; }
agent_pids() {
	ps -axo pid=,command= | awk '$0 ~ /\/mdd-agent([[:space:]]|$)/ || $0 ~ /\/MDD-Agent-macOS-arm64([[:space:]]|$)/ {print $1}'
}
candidate_hash=$(hash_file "$candidate/mdd-agent")
case "$candidate_hash" in ''|*[!0-9a-f]*) exit 1 ;; esac
record="$state/deployment.json"

if [ "$action" = rollback ]; then
	[ -f "$record" ] || { printf '%s\n' 'deployment record is missing' >&2; exit 1; }
	old=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["old_path"])' "$record")
	[ -x "$old" ] || { printf '%s\n' 'rollback binary is missing' >&2; exit 1; }
	install -m 0700 "$old" "$config" >/dev/null 2>&1 || true
	printf '%s\n' '{"status":"rollback_ready","note":"restart the existing managed launcher with the recorded path"}'
	exit 0
fi

if [ "$action" = preflight ]; then
	printf '%s\n' "{\"status\":\"preflight_ok\",\"candidate_sha256\":\"$candidate_hash\",\"config_sha256\":\"$(hash_file "$config")\"}"
	exit 0
fi

mkdir -p "$state"
old_path=
target="$state/releases/$(basename "$candidate")"
mkdir -p "$state/releases"
cp "$candidate/mdd-agent" "$target"
chmod 0700 "$target"
if ! "$target" status --config "$config" >/dev/null 2>&1; then
	printf '%s\n' '{"status":"rejected","code":"candidate_status_failed"}' >&2
	exit 1
fi
running_pid=$(agent_pids | head -n 1)
if [ -n "$running_pid" ] && [ -z "$restart_command" ]; then
	printf '%s\n' 'running Agent has no explicit launcher; refuse in-place replacement' >&2
	exit 1
fi
if [ -n "$running_pid" ]; then
	old_path=$(ps -p "$running_pid" -o comm= | sed -e 's/[[:space:]]*$//')
	kill -TERM "$running_pid"
	deadline=$(( $(date +%s) + 45 ))
	while kill -0 "$running_pid" 2>/dev/null; do
		[ "$(date +%s)" -lt "$deadline" ] || {
			printf '%s\n' 'running Agent did not exit before cutover' >&2
			exit 1
		}
		sleep 1
	done
fi
backup="$state/previous-mdd-agent"
if [ -n "$old_path" ] && [ -x "$old_path" ]; then cp "$old_path" "$backup"; fi
current="$state/current"
next_current="$state/.current.$candidate_hash"
ln -s "$target" "$next_current"
mv -f "$next_current" "$current"
log="$state/logs/go-agent-$(basename "$target").log"
mkdir -p "$state/logs"
nohup "$target" run --config "$config" >"$log" 2>&1 < /dev/null &
new_pid=$!
sleep 1
if ! kill -0 "$new_pid" 2>/dev/null; then
	printf '%s\n' '{"status":"rolled_back","code":"candidate_start_failed"}' >&2
	[ -x "$backup" ] && nohup "$backup" run --config "$config" >"$state/logs/rollback.log" 2>&1 < /dev/null &
	exit 1
fi
python3 - "$record" "$target" "$old_path" "$candidate_hash" <<'PY'
import json,sys
json.dump({"status":"installed","new_path":sys.argv[2],"old_path":sys.argv[3],"current_path":sys.argv[2].rsplit("/releases/", 1)[0] + "/current","candidate_sha256":sys.argv[4]}, open(sys.argv[1],"w"), sort_keys=True)
PY
printf '%s\n' "{\"status\":\"installed\",\"path\":\"$target\",\"sha256\":\"$candidate_hash\"}"
