#!/bin/sh
set -eu

fyne_tools_version="v1.7.2"
app_id="com.mdd.agent"
app_name="MDD Agent"
version="0.1.0"
build_number="1"
architecture="amd64"
compiler="${CC-}"
output=""

usage() {
	printf '%s\n' "usage: build-windows-agent.sh --output /absolute/path [--version x.y.z] [--build number] [--arch amd64] [--cc mingw-gcc]"
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
	--arch)
		architecture=${2-}
		shift 2
		;;
	--cc)
		compiler=${2-}
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
if [ "$architecture" != "amd64" ]; then
	printf '%s\n' "--arch currently accepts only amd64" >&2
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
if [ -z "$compiler" ] || ! command -v "$compiler" >/dev/null 2>&1; then
	printf '%s\n' "--cc must name an installed MinGW-w64 compiler" >&2
	exit 2
fi
if [ -e "$output" ]; then
	printf '%s\n' "output already exists: $output" >&2
	exit 2
fi
if command -v sha256sum >/dev/null 2>&1; then
	hash_file() { sha256sum "$1"; }
elif command -v shasum >/dev/null 2>&1; then
	hash_file() { shasum -a 256 "$1"; }
else
	printf '%s\n' "sha256sum or shasum is required" >&2
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

staging=$(mktemp -d "$TMPDIR/mdd-agent-windows.XXXXXX")
trap 'rm -rf "$staging"' EXIT HUP INT TERM
payload="$staging/MDD-Agent-Windows-$architecture"
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
	CGO_ENABLED=0 GOOS=windows GOARCH="$architecture" go build -trimpath -ldflags='-s -w' -o "$payload/mdd-agent.exe" ./cmd/mdd-agent
	CGO_ENABLED=1 GOOS=windows GOARCH="$architecture" CC="$compiler" go build -trimpath -tags gui -ldflags='-s -w -H=windowsgui' -o "$staging/mdd-agent-gui.exe" ./cmd/mdd-agent
)

(
	cd "$staging"
	CC="$compiler" GOWORK=off go run "fyne.io/tools/cmd/fyne@$fyne_tools_version" package \
		--target windows \
		--executable "$staging/mdd-agent-gui.exe" \
		--source-dir "$runtime_root/cmd/mdd-agent" \
		--name "$app_name" \
		--icon "$icon" \
		--app-id "$app_id" \
		--app-version "$version" \
		--app-build "$build_number" \
		--release
)
mv "$staging/$app_name.exe" "$payload/$app_name.exe"

printf '%s\n' \
	"MDD Go Agent Windows development candidate" \
	"" \
	"The package contains two shells over the same Agent configuration and singleton:" \
	"  mdd-agent.exe  service and CLI" \
	"  MDD Agent.exe  tray GUI and Windows service management" \
	"" \
	"Initialize the shared configuration from a terminal, then install/start the service from an elevated terminal or the GUI." \
	"The GUI always registers the sibling mdd-agent.exe, never itself, as the service executable." \
	"" \
	"This candidate is PC/SC-only: modem_enabled=true is rejected." \
	>"$payload/README.txt"

printf '%s\n' \
	"version=$version" \
	"build=$build_number" \
	"architecture=$architecture" \
	"source_revision=$source_revision" \
	"source_tree=$source_state" \
	"fyne_runtime=v2.8.1" \
	"fyne_tools=$fyne_tools_version" \
	"signing=unsigned-development" \
	>"$payload/BUILD.txt"

(
	cd "$payload"
	find . -type f -print | LC_ALL=C sort | while IFS= read -r file; do
		hash_file "$file"
	done >"$staging/SHA256SUMS"
)
mv "$staging/SHA256SUMS" "$payload/SHA256SUMS"

mkdir -p "$(dirname -- "$output")"
mv "$payload" "$output"
trap - EXIT HUP INT TERM
printf '%s\n' "created $output"
