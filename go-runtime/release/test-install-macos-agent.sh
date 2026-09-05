#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
installer="$script_dir/install-macos-agent.sh"

sh -n "$installer"
grep -Fq 'preflight|install --candidate DIR --config FILE --state DIR; rollback --config FILE --state DIR' "$installer"
grep -Fq '<key>LimitLoadToSessionType</key>' "$installer"
grep -Fq '<key>StandardOutPath</key>' "$installer"
grep -Fq '<key>StandardErrorPath</key>' "$installer"
grep -Fq 'previous_target' "$installer"
grep -Fq 'os.replace' "$installer"
if grep -Fq 'mv -f "$next_current" "$current"' "$installer"; then
	printf '%s\n' 'installer must replace the current symlink itself, not follow its directory target' >&2
	exit 1
fi
if grep -Fq 'launchctl kickstart' "$installer"; then
	printf '%s\n' 'installer must not use unbounded launchctl kickstart' >&2
	exit 1
fi

preflight_line=$(awk '/\[ "\$action" = preflight \]/{print NR; exit}' "$installer")
state_mkdir_line=$(awk '/mkdir -p "\$state"$/{print NR; exit}' "$installer")
[ -n "$preflight_line" ] && [ -n "$state_mkdir_line" ] && [ "$state_mkdir_line" -gt "$preflight_line" ]

printf '%s\n' 'macOS Agent installer contract tests passed'
