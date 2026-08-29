#!/bin/sh
set -eu

fyne_tools_version="v1.7.2"
app_id="com.mdd.agent"
app_name="MDD Agent"
version="0.1.0"
build_number="1"
output=""
identity=""

usage() {
	printf '%s\n' "usage: build-macos-agent.sh --output /absolute/path [--version x.y.z] [--build number] [--identity Developer-ID]"
}

while [ "$#" -gt 0 ]; do
	case "$1" in
	--output)
		output=${2-}
		shift 2
		;;
	--version)
		version=${2-}
		shift 2
		;;
	--build)
		build_number=${2-}
		shift 2
		;;
	--identity)
		identity=${2-}
		shift 2
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		usage >&2
		exit 2
		;;
	esac
done

case "$output" in
/*) ;;
*)
	printf '%s\n' "--output must be an absolute path" >&2
	exit 2
	;;
esac
case ${TMPDIR-} in
"" | /tmp | /tmp/* | /var/folders/*)
	printf '%s\n' "TMPDIR must point to a task-scoped non-system temporary directory" >&2
	exit 2
	;;
esac
if [ "$(uname -s)" != "Darwin" ]; then
	printf '%s\n' "macOS Agent packages must be built on macOS" >&2
	exit 2
fi
if [ -e "$output" ]; then
	printf '%s\n' "output already exists: $output" >&2
	exit 2
fi
if ! printf '%s\n' "$version" | grep -Eq '^[0-9]+(\.[0-9]+){0,2}$'; then
	printf '%s\n' "--version must contain only numeric dot-separated components" >&2
	exit 2
fi
case "$build_number" in
"" | *[!0-9]*)
	printf '%s\n' "--build must be a positive integer" >&2
	exit 2
	;;
esac
if [ "$build_number" -lt 1 ]; then
	printf '%s\n' "--build must be a positive integer" >&2
	exit 2
fi

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
runtime_root=$(CDPATH='' cd -- "$script_dir/.." && pwd)
repository_root=$(CDPATH='' cd -- "$runtime_root/.." && pwd)
icon="$repository_root/agent/assets/mdd-agent.png"
if [ ! -f "$icon" ]; then
	printf '%s\n' "missing Agent icon: $icon" >&2
	exit 1
fi

staging=$(mktemp -d "$TMPDIR/mdd-agent-package.XXXXXX")
trap 'rm -rf "$staging"' EXIT HUP INT TERM
payload="$staging/MDD-Agent-macOS"
mkdir -p "$payload"

: "${GOCACHE:=$TMPDIR/go-cache}"
: "${GOMODCACHE:=$TMPDIR/go-mod-cache}"
: "${GOTMPDIR:=$TMPDIR/go-tmp}"
export GOCACHE GOMODCACHE GOTMPDIR
mkdir -p "$GOCACHE" "$GOMODCACHE" "$GOTMPDIR"

source_revision=$(git -C "$repository_root" rev-parse HEAD)
source_state=clean
if [ -n "$(git -C "$repository_root" status --porcelain -- go-runtime)" ]; then
	source_state=dirty
fi

(
	cd "$runtime_root"
	CGO_ENABLED=1 go build -trimpath -o "$payload/mdd-agent" ./cmd/mdd-agent
)

(
	cd "$staging"
	GOWORK=off go run "fyne.io/tools/cmd/fyne@$fyne_tools_version" package \
		--target darwin \
		--source-dir "$runtime_root/cmd/mdd-agent" \
		--tags gui \
		--name "$app_name" \
		--icon "$icon" \
		--app-id "$app_id" \
		--app-version "$version" \
		--app-build "$build_number" \
		--release
)
mv "$staging/$app_name.app" "$payload/$app_name.app"
app_executable_name=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "$payload/$app_name.app/Contents/Info.plist")
app_executable="$payload/$app_name.app/Contents/MacOS/$app_executable_name"
if [ ! -f "$app_executable" ]; then
	printf '%s\n' "packaged app is missing its main executable" >&2
	exit 1
fi

if [ -n "$identity" ]; then
	codesign --force --timestamp --options runtime --sign "$identity" "$payload/mdd-agent"
	codesign --force --timestamp --options runtime --sign "$identity" "$app_executable"
	codesign --force --timestamp --options runtime --sign "$identity" "$payload/$app_name.app"
	signing=developer-id
else
	codesign --force --sign - "$payload/mdd-agent"
	codesign --force --sign - "$app_executable"
	codesign --force --sign - "$payload/$app_name.app"
	signing=ad-hoc-development
fi
codesign --verify --strict "$payload/mdd-agent"
codesign --verify --deep --strict "$payload/$app_name.app"

printf '%s\n' \
	"MDD Go Agent development candidate" \
	"" \
	"1. Initialize the shared owner-only configuration:" \
	"   ./mdd-agent config init" \
	"   ./mdd-agent config set server gateway.example:8443" \
	"   ./mdd-agent config set tls_sha256 CERTIFICATE_SHA256" \
	"   printf '%s\\n' \"\$MDD_AGENT_TOKEN\" | ./mdd-agent config set token --stdin" \
	"" \
	"2. Start either the headless host or MDD Agent.app. They use the same configuration and cannot own PC/SC simultaneously." \
	"" \
	"This candidate is PC/SC-only: modem_enabled=true is rejected." \
	>"$payload/README.txt"

printf '%s\n' \
	"version=$version" \
	"build=$build_number" \
	"architecture=$(uname -m)" \
	"source_revision=$source_revision" \
	"source_tree=$source_state" \
	"fyne_runtime=v2.8.1" \
	"fyne_tools=$fyne_tools_version" \
	"signing=$signing" \
	>"$payload/BUILD.txt"

(
	cd "$payload"
	find . -type f -print | LC_ALL=C sort | while IFS= read -r file; do
		shasum -a 256 "$file"
	done >"$staging/SHA256SUMS"
)
mv "$staging/SHA256SUMS" "$payload/SHA256SUMS"

mkdir -p "$(dirname -- "$output")"
mv "$payload" "$output"
trap - EXIT HUP INT TERM
printf '%s\n' "created $output"
