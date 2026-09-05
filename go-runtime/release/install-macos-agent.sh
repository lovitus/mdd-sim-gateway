#!/bin/sh
set -eu

usage() {
	printf '%s\n' "usage: install-macos-agent.sh preflight|install --candidate DIR --config FILE --state DIR; rollback --config FILE --state DIR"
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
[ -n "$action" ] && [ -n "$config" ] && [ -n "$state" ] || usage
[ "$(uname -s)" = Darwin ] || { printf '%s\n' 'macOS is required' >&2; exit 1; }
[ "$action" = rollback ] || [ -n "$candidate" ] || usage
if [ "$action" != rollback ]; then
	[ -d "$candidate" ] || { printf '%s\n' 'candidate directory is missing' >&2; exit 1; }
	[ -f "$candidate/mdd-agent" ] || { printf '%s\n' 'candidate mdd-agent is missing' >&2; exit 1; }
	[ -d "$candidate/MDD Agent.app" ] || { printf '%s\n' 'candidate MDD Agent.app is missing' >&2; exit 1; }
	[ -x "$candidate/MDD Agent.app/Contents/MacOS/mdd-agent" ] || {
		printf '%s\n' 'candidate App executable is missing' >&2
		exit 1
	}
fi
[ -f "$config" ] || { printf '%s\n' 'Agent config is missing' >&2; exit 1; }

hash_file() { shasum -a 256 "$1" | awk '{print $1}'; }
agent_pids() {
	ps -axo pid=,command= | awk '$0 ~ /\/mdd-agent([[:space:]]|$)/ || $0 ~ /\/MDD-Agent-macOS-arm64([[:space:]]|$)/ {print $1}'
}
launch_domain() { printf 'gui/%s\n' "$(id -u)"; }
launch_plist="$HOME/Library/LaunchAgents/com.mdd.agent.plist"
current="$state/current"
record="$state/deployment.json"
launch_program=

validate_candidate() {
	if [ -f "$candidate/SHA256SUMS" ]; then
		(cd "$candidate" && shasum -a 256 -c SHA256SUMS >/dev/null)
	fi
	codesign --verify --deep --strict "$candidate/MDD Agent.app" >/dev/null
	if [ -L "$current" ] && [ -d "$current/MDD Agent.app" ]; then
		candidate_requirement=$(codesign -d -r- "$candidate/MDD Agent.app" 2>&1 | sed -n '/^designated =>/p')
		current_requirement=$(codesign -d -r- "$current/MDD Agent.app" 2>&1 | sed -n '/^designated =>/p')
		[ -n "$candidate_requirement" ] && [ "$candidate_requirement" = "$current_requirement" ] || {
			printf '%s\n' 'candidate App signing identity does not match the installed App' >&2
			exit 1
		}
	fi
}

write_launch_plist() {
	program=$1
	launch_program=$program
	temporary="$state/.com.mdd.agent.plist.$$"
	case "$program$config" in
		*\&*|*\<*|*\>*) printf '%s\n' 'launchd path contains unsupported XML characters' >&2; exit 1 ;;
	esac
	cat >"$temporary" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.mdd.agent</string>
	<key>ProgramArguments</key>
	<array>
		<string>$program</string>
		<string>gui</string>
		<string>-config</string>
		<string>$config</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>LimitLoadToSessionType</key>
	<array>
		<string>Aqua</string>
		<string>Background</string>
	</array>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>StandardOutPath</key>
	<string>$state/logs/launchd.stdout.log</string>
	<key>StandardErrorPath</key>
	<string>$state/logs/launchd.stderr.log</string>
</dict>
</plist>
EOF
	plutil -lint "$temporary" >/dev/null
	mv -f "$temporary" "$launch_plist"
	chmod 600 "$launch_plist"
}

stop_launch_agent() {
	/bin/launchctl bootout "$(launch_domain)" "$launch_plist" >/dev/null 2>&1 || true
	running_pid=$(agent_pids | head -n 1)
	[ -n "$running_pid" ] || return 0
	kill -TERM "$running_pid"
	deadline=$(( $(date +%s) + 45 ))
	while kill -0 "$running_pid" 2>/dev/null; do
		[ "$(date +%s)" -lt "$deadline" ] || {
			printf '%s\n' 'running Agent did not exit before cutover' >&2
			exit 1
		}
		sleep 1
	done
}

start_launch_agent() {
	/bin/launchctl bootstrap "$(launch_domain)" "$launch_plist"
	deadline=$(( $(date +%s) + 45 ))
	while :; do
		if [ -n "$(agent_pids | head -n 1)" ] &&
			"$launch_program" status --config "$config" >/dev/null 2>&1; then
			return 0
		fi
		[ "$(date +%s)" -lt "$deadline" ] || {
			printf '%s\n' 'launchd did not start the MDD Agent' >&2
			return 1
		}
		sleep 1
	done
}

candidate_hash=
if [ "$action" != rollback ]; then
	validate_candidate
	candidate_hash=$(hash_file "$candidate/mdd-agent")
	case "$candidate_hash" in ''|*[!0-9a-f]*) exit 1 ;; esac
fi

if [ "$action" = preflight ]; then
	printf '%s\n' "{\"status\":\"preflight_ok\",\"candidate_sha256\":\"$candidate_hash\",\"config_sha256\":\"$(hash_file "$config")\"}"
	exit 0
fi

[ -d "$state" ] || mkdir -p "$state"
[ -d "$state/releases" ] || mkdir -p "$state/releases"
[ -d "$state/logs" ] || mkdir -p "$state/logs"
[ -d "$(dirname "$launch_plist")" ] || mkdir -p "$(dirname "$launch_plist")"

if [ "$action" = rollback ]; then
	[ -f "$record" ] || { printf '%s\n' 'deployment record is missing' >&2; exit 1; }
	previous=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["previous_target"])' "$record")
	[ -d "$previous" ] || { printf '%s\n' 'rollback release is missing' >&2; exit 1; }
	stop_launch_agent
	next_current="$state/.current.rollback.$$"
	ln -s "$previous" "$next_current"
	mv -f "$next_current" "$current"
	write_launch_plist "$previous/MDD Agent.app/Contents/MacOS/mdd-agent"
	start_launch_agent
	printf '%s\n' "{\"status\":\"rolled_back\",\"target\":\"$previous\"}"
	exit 0
fi

target="$state/releases/$(basename "$candidate")"
[ ! -e "$target" ] || { printf '%s\n' "release already exists: $target" >&2; exit 1; }
previous_target=$(readlink "$current" 2>/dev/null || true)
if [ -n "$previous_target" ]; then
	[ -d "$previous_target" ] || { printf '%s\n' 'current release target is missing' >&2; exit 1; }
fi
cp -R "$candidate" "$target"
stop_launch_agent
next_current="$state/.current.$candidate_hash"
ln -s "$target" "$next_current"
mv -f "$next_current" "$current"
write_launch_plist "$target/MDD Agent.app/Contents/MacOS/mdd-agent"
if ! start_launch_agent; then
	if [ -n "$previous_target" ] && [ -d "$previous_target" ]; then
		next_current="$state/.current.rollback.$$"
		ln -s "$previous_target" "$next_current"
		mv -f "$next_current" "$current"
		write_launch_plist "$previous_target/MDD Agent.app/Contents/MacOS/mdd-agent"
		start_launch_agent || true
	fi
	printf '%s\n' '{"status":"rolled_back","code":"candidate_start_failed"}' >&2
	exit 1
fi
python3 - "$record" "$target" "$previous_target" "$candidate_hash" <<'PY'
import json,sys
json.dump({"status":"installed","new_target":sys.argv[2],"previous_target":sys.argv[3],"current_path":sys.argv[2].rsplit("/releases/", 1)[0] + "/current","candidate_sha256":sys.argv[4]}, open(sys.argv[1],"w"), sort_keys=True)
PY
printf '%s\n' "{\"status\":\"installed\",\"path\":\"$target\",\"sha256\":\"$candidate_hash\"}"
