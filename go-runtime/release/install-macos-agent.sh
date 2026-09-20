#!/bin/sh
set -eu

usage() {
	printf '%s\n' "usage: install-macos-agent.sh preflight|install --candidate DIR --config FILE --state DIR [--mode gui|cli]; rollback --config FILE --state DIR"
	exit 2
}

action=
candidate=
config=
state=
launch_mode=
while [ "$#" -gt 0 ]; do
	case "$1" in
	preflight|install|rollback) [ -z "$action" ] || usage; action=$1; shift ;;
	--candidate) candidate=${2-}; shift 2 ;;
	--config) config=${2-}; shift 2 ;;
	--state) state=${2-}; shift 2 ;;
	--mode) launch_mode=${2-}; case "$launch_mode" in gui|cli) ;; *) usage ;; esac; shift 2 ;;
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
	# Only the installed per-user launchd label owns this cutover. Other users,
	# CLI hosts and separately configured Agents must not be discovered by name.
	/bin/launchctl print "$(launch_domain)/com.mdd.agent" 2>/dev/null |
		awk '$1 == "pid" && $2 == "=" && $3 ~ /^[0-9]+$/ {print $3; exit}'
}
agent_program() {
	[ -f "$launch_plist" ] || return 1
	plist_string "$launch_plist" ProgramArguments.0
}
launch_domain() { printf 'gui/%s\n' "$(id -u)"; }
launch_plist="$HOME/Library/LaunchAgents/com.mdd.agent.plist"
current="$state/current"
record="$state/deployment.json"
launch_program=
launch_argument=gui

previous_launch_argument() {
	if [ -f "$launch_plist" ]; then
		plist_string "$launch_plist" ProgramArguments.1
	else
		case "$previous_program" in */Contents/MacOS/*) printf 'gui\n' ;; *) printf 'run\n' ;; esac
	fi
}

replace_link() {
	mv -fh "$1" "$2"
}

plist_string() { plutil -extract "$2" raw -o - "$1"; }
plist_insert_string() {
	if [ -n "$3" ]; then
		plutil -insert "$2" -string "$3" "$1"
	else
		plutil -insert "$2" -json '""' "$1"
	fi
}

validate_candidate() {
	if [ -f "$candidate/SHA256SUMS" ]; then
		(cd "$candidate" && shasum -a 256 -c SHA256SUMS >/dev/null)
	fi
	codesign --verify --deep --strict "$candidate/MDD Agent.app" >/dev/null
	codesign --verify --strict "$candidate/mdd-agent" >/dev/null
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
	case "$launch_argument" in gui|run) ;; *) printf '%s\n' 'invalid launch mode' >&2; exit 1 ;; esac
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
		<string>$launch_argument</string>
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

wait_cutover_delay() {
	remaining=$(( deadline - $(date +%s) ))
	[ "$remaining" -gt 0 ] || return 1
	wait_seconds=$delay
	[ "$wait_seconds" -le "$remaining" ] || wait_seconds=$remaining
	sleep "$wait_seconds"
	[ "$delay" -ge 5 ] || delay=$(( delay * 2 ))
	[ "$delay" -le 5 ] || delay=5
}

stop_launch_agent() {
	# Capture the labelled PID before bootout can remove it from launchd.
	running_pid=$(agent_pids)
	if ! /bin/launchctl bootout "$(launch_domain)" "$launch_plist" >/dev/null 2>&1; then
		[ -z "$running_pid" ] || { printf '%s\n' 'cannot unload the owned Agent; cutover blocked' >&2; return 1; }
	fi
	[ -n "$running_pid" ] || return 0
	deadline=$(( $(date +%s) + 180 ))
	delay=1
	while kill -0 "$running_pid" 2>/dev/null; do
		wait_cutover_delay || { printf '%s\n' 'owned Agent did not exit before cutover' >&2; return 1; }
	done
}

start_launch_agent() {
	/bin/launchctl bootstrap "$(launch_domain)" "$launch_plist" || return 1
	deadline=$(( $(date +%s) + 180 ))
	delay=1
	while :; do
		# Observe before sleeping; a healthy fast startup has no 30-second floor.
		if [ -n "$(agent_pids)" ] &&
			status_json=$("$launch_program" status --config "$config" 2>/dev/null) &&
			[ "$(printf '%s' "$status_json" | plutil -extract state raw -o - - 2>/dev/null)" = running ]; then
			return 0
		fi
		wait_cutover_delay || { printf '%s\n' 'launchd did not start the owned MDD Agent' >&2; return 1; }
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
	previous=$(plist_string "$record" previous_target)
	previous_program=$(plist_string "$record" previous_program)
	launch_argument=$(plist_string "$record" previous_launch_argument 2>/dev/null || printf 'gui')
	[ -d "$previous" ] || { printf '%s\n' 'rollback release is missing' >&2; exit 1; }
	[ -x "$previous_program" ] || { printf '%s\n' 'rollback executable is missing' >&2; exit 1; }
	stop_launch_agent
	next_current="$state/.current.rollback.$$"
	ln -s "$previous" "$next_current"
	replace_link "$next_current" "$current"
	write_launch_plist "$previous_program"
	start_launch_agent
	printf '%s\n' "{\"status\":\"rolled_back\",\"target\":\"$previous\"}"
	exit 0
fi

target="$state/releases/$(basename "$candidate")"
[ ! -e "$target" ] || { printf '%s\n' "release already exists: $target" >&2; exit 1; }
previous_target=$(readlink "$current" 2>/dev/null || true)
previous_program=$(agent_program 2>/dev/null || true)
previous_argument=$(previous_launch_argument)
case "$previous_argument" in gui|run) ;; *) printf '%s\n' 'unknown existing Agent launch mode' >&2; exit 1 ;; esac
case "$launch_mode" in cli) launch_argument=run ;; gui) launch_argument=gui ;; '') launch_argument=$previous_argument ;; esac
if [ -z "$previous_program" ] && [ -z "$launch_mode" ]; then launch_argument=gui; fi
if [ -n "$previous_target" ]; then
	[ -d "$previous_target" ] || { printf '%s\n' 'current release target is missing' >&2; exit 1; }
fi
cp -R "$candidate" "$target"
stop_launch_agent
next_current="$state/.current.$candidate_hash"
ln -s "$target" "$next_current"
replace_link "$next_current" "$current"
if [ "$launch_argument" = run ]; then
	write_launch_plist "$target/mdd-agent"
else
	write_launch_plist "$target/MDD Agent.app/Contents/MacOS/mdd-agent"
fi
if ! start_launch_agent; then
	# Failed readiness does not imply process exit. Confirm candidate release
	# before restoring the old symlink/plist; never overlap two hardware owners.
	stop_launch_agent || { printf '%s\n' 'rollback blocked: candidate stop unconfirmed' >&2; exit 1; }
	if [ -n "$previous_target" ] && [ -d "$previous_target" ]; then
		next_current="$state/.current.rollback.$$"
		ln -s "$previous_target" "$next_current"
		replace_link "$next_current" "$current"
		if [ -n "$previous_program" ] && [ -x "$previous_program" ]; then
			launch_argument=$previous_argument
			write_launch_plist "$previous_program"
		fi
		start_launch_agent || true
	fi
	printf '%s\n' '{"status":"rolled_back","code":"candidate_start_failed"}' >&2
	exit 1
fi
temporary_record="$state/.deployment.json.$$"
plutil -create xml1 "$temporary_record"
plist_insert_string "$temporary_record" status installed
plist_insert_string "$temporary_record" new_target "$target"
plist_insert_string "$temporary_record" previous_target "$previous_target"
plist_insert_string "$temporary_record" previous_program "$previous_program"
plist_insert_string "$temporary_record" previous_launch_argument "$previous_argument"
plist_insert_string "$temporary_record" current_path "$state/current"
plist_insert_string "$temporary_record" candidate_sha256 "$candidate_hash"
plutil -convert json "$temporary_record"
chmod 600 "$temporary_record"
replace_link "$temporary_record" "$record"
printf '%s\n' "{\"status\":\"installed\",\"path\":\"$target\",\"sha256\":\"$candidate_hash\"}"
