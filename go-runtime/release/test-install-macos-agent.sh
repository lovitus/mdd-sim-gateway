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
grep -Fq 'previous_program' "$installer"
grep -Fq 'agent_program' "$installer"
grep -Fq 'mv -fh "$1" "$2"' "$installer"
grep -Fq 'plutil -convert json "$temporary_record"' "$installer"
if grep -Eiq 'python|os\.replace|json\.load|json\.dump' "$installer"; then
	printf '%s\n' 'installer must not depend on Python' >&2
	exit 1
fi
if grep -Fq 'launchctl kickstart' "$installer"; then
	printf '%s\n' 'installer must not use unbounded launchctl kickstart' >&2
	exit 1
fi

preflight_line=$(awk '/\[ "\$action" = preflight \]/{print NR; exit}' "$installer")
state_mkdir_line=$(awk '/mkdir -p "\$state"$/{print NR; exit}' "$installer")
[ -n "$preflight_line" ] && [ -n "$state_mkdir_line" ] && [ "$state_mkdir_line" -gt "$preflight_line" ]

temporary_parent=${TMPDIR:-/tmp}
mkdir -p "$temporary_parent"
root=$(mktemp -d "$temporary_parent/mdd-installer-primitives.XXXXXX")
trap 'rm -rf "$root"' EXIT HUP INT TERM
mkdir "$root/old" "$root/new"
ln -s "$root/old" "$root/current"
ln -s "$root/new" "$root/next"
mv -fh "$root/next" "$root/current"
[ "$(readlink "$root/current")" = "$root/new" ]
plutil -create xml1 "$root/record.json"
plutil -insert status -string installed "$root/record.json"
plutil -insert previous_target -json '""' "$root/record.json"
plutil -convert json "$root/record.json"
[ "$(plutil -extract status raw -o - "$root/record.json")" = installed ]
[ -z "$(plutil -extract previous_target raw -o - "$root/record.json")" ]

printf '%s\n' 'macOS Agent installer contract tests passed'
