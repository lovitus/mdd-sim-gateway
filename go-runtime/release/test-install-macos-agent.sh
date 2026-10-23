#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
installer="$script_dir/install-macos-agent.sh"

sh -n "$installer"
# Real candidate preflight and non-mutation checks run after packaging in CI.

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
