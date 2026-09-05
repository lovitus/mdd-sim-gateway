#!/bin/sh
set -eu

usage() {
	printf '%s\n' "usage: install-macos-agent.sh preflight|install|rollback --candidate DIR --config FILE --state DIR"
	exit 2
}

action=
candidate=
config=
state=
while [ "$#" -gt 0 ]; do
	case "$1" in
	preflight|install|rollback) [ -z "$action" ] || usage; action=$1; shift ;;
	--candidate) candidate=${2-}; shift 2 ;;
	--config) config=${2-}; shift 2 ;;
	--state) state=${2-}; shift 2 ;;
	*) usage ;;
	esac
done
[ -n "$action" ] && [ -n "$candidate" ] && [ -n "$config" ] && [ -n "$state" ] || usage
[ "$(uname -s)" = Darwin ] || { printf '%s\n' 'macOS is required' >&2; exit 1; }
[ -d "$candidate" ] && [ -f "$candidate/mdd-agent" ] || { printf '%s\n' 'candidate mdd-agent is missing' >&2; exit 1; }
[ -f "$config" ] || { printf '%s\n' 'Agent config is missing' >&2; exit 1; }

hash_file() { shasum -a 256 "$1" | awk '{print $1}'; }
candidate_hash=$(hash_file "$candidate/mdd-agent")
case "$candidate_hash" in ''|*[!0-9a-f]*) exit 1 ;; esac
mkdir -p "$state"
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

old_path=
if pgrep -x mdd-agent >/dev/null 2>&1; then
	old_path=$(ps -axo command= | awk '/(^|[[:space:]])mdd-agent([[:space:]]|$)/ {print $1; exit}')
	kill -TERM "$(pgrep -x mdd-agent | head -n 1)"
	sleep 2
fi
backup="$state/previous-mdd-agent"
if [ -n "$old_path" ] && [ -x "$old_path" ]; then cp "$old_path" "$backup"; fi
target="$state/releases/$(basename "$candidate")"
mkdir -p "$state/releases"
cp "$candidate/mdd-agent" "$target"
chmod 0700 "$target"
if ! "$target" status --config "$config" >/dev/null 2>&1; then
	[ -x "$backup" ] && cp "$backup" "$target"
	printf '%s\n' '{"status":"rolled_back","code":"candidate_status_failed"}' >&2
	exit 1
fi
python3 - "$record" "$target" "$old_path" "$candidate_hash" <<'PY'
import json,sys
json.dump({"status":"installed","new_path":sys.argv[2],"old_path":sys.argv[3],"candidate_sha256":sys.argv[4]}, open(sys.argv[1],"w"), sort_keys=True)
PY
printf '%s\n' "{\"status\":\"installed\",\"path\":\"$target\",\"sha256\":\"$candidate_hash\"}"
