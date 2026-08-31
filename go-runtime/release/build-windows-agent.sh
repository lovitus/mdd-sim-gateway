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
go_licenses="${GO_LICENSES-}"

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
if [ -z "$go_licenses" ] || [ ! -x "$go_licenses" ]; then
	printf '%s\n' "GO_LICENSES must point to the pinned go-licenses executable" >&2
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
serial_license="$runtime_root/licenses/go-serial-BSD-3-Clause.txt"
sms_license="$runtime_root/licenses/warthog618-sms-MIT.txt"
malgo_license="$runtime_root/licenses/malgo-Unlicense.txt"
project_license="$repository_root/LICENSE"
project_notice="$repository_root/NOTICE"
third_party_notices="$repository_root/THIRD_PARTY_LICENSES.md"
if [ ! -f "$icon" ]; then
	printf '%s\n' "missing Agent icon: $icon" >&2
	exit 1
fi
if [ ! -f "$serial_license" ]; then
	printf '%s\n' "missing go-serial license: $serial_license" >&2
	exit 1
fi
if [ ! -f "$sms_license" ]; then
	printf '%s\n' "missing warthog618/sms license: $sms_license" >&2
	exit 1
fi
if [ ! -f "$malgo_license" ]; then
	printf '%s\n' "missing malgo license: $malgo_license" >&2
	exit 1
fi
for legal_file in "$project_license" "$project_notice" "$third_party_notices"; do
	if [ ! -f "$legal_file" ]; then
		printf '%s\n' "missing project legal file: $legal_file" >&2
		exit 1
	fi
done

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
)
(
	cd "$repository_root/agent/call-audio-helper"
	GOWORK=off CGO_ENABLED=1 GOOS=windows GOARCH="$architecture" CC="$compiler" \
		go build -trimpath -ldflags='-s -w' -o "$payload/mdd-call-audio-helper.exe" .
)
cp "$payload/mdd-agent.exe" "$staging/mdd-agent-gui.exe"

(
	cd "$staging"
	GOBIN="$staging/fyne-tools" GOWORK=off go install "fyne.io/tools/cmd/fyne@$fyne_tools_version"
	CC="$compiler" "$staging/fyne-tools/fyne" package \
		--target windows \
		--executable "$staging/mdd-agent-gui.exe" \
		--source-dir "$runtime_root/cmd/mdd-agent" \
		--tags gui \
		--name "$app_name" \
		--icon "$icon" \
		--app-id "$app_id" \
		--app-version "$version" \
		--app-build "$build_number" \
		--release
)
packaged_gui="$staging/$app_name.exe"
if [ ! -f "$packaged_gui" ]; then
	packaged_gui="$staging/mdd-agent-gui.exe"
fi
if [ ! -f "$packaged_gui" ]; then
	printf '%s\n' "Fyne did not produce the Windows GUI executable" >&2
	exit 1
fi
mv "$packaged_gui" "$payload/$app_name.exe"
mkdir -p "$payload/THIRD-PARTY-LICENSES"
install -m 0644 "$project_license" "$payload/LICENSE"
install -m 0644 "$project_notice" "$payload/NOTICE"
install -m 0644 "$third_party_notices" "$payload/THIRD_PARTY_LICENSES.md"
cp "$serial_license" "$payload/THIRD-PARTY-LICENSES/go-serial-BSD-3-Clause.txt"
cp "$sms_license" "$payload/THIRD-PARTY-LICENSES/warthog618-sms-MIT.txt"
cp "$malgo_license" "$payload/THIRD-PARTY-LICENSES/malgo-Unlicense.txt"

sing_usbip_root=$(cd "$runtime_root" && GOOS=windows GOARCH="$architecture" go list -m -f '{{.Dir}}' github.com/sagernet/sing-usbip)
driver_licenses="$payload/THIRD-PARTY-LICENSES/sing-usbip-windows-drivers"
mkdir -p "$driver_licenses"
install -m 0644 "$sing_usbip_root/internal/usbipvhci/assets/LICENSE.txt" \
	"$driver_licenses/usbip-win2-BSD-2-Clause.txt"
for license_file in "$sing_usbip_root"/internal/vboxusb/assets/"$architecture"/*.license; do
	install -m 0644 "$license_file" "$driver_licenses/$(basename "$license_file")"
done

(
	cd "$runtime_root"
	CGO_ENABLED=0 GOOS=windows GOARCH="$architecture" GOFLAGS= \
		"$go_licenses" save ./cmd/mdd-agent \
		--ignore github.com/lovitus/mdd-sim-gateway/go-runtime \
		--save_path "$payload/THIRD-PARTY-LICENSES/go-cli"
	CGO_ENABLED=1 GOOS=windows GOARCH="$architecture" CC="$compiler" GOFLAGS=-tags=gui \
		"$go_licenses" save ./cmd/mdd-agent \
		--ignore github.com/lovitus/mdd-sim-gateway/go-runtime \
		--save_path "$payload/THIRD-PARTY-LICENSES/go-gui"
)
(
	cd "$repository_root/agent/call-audio-helper"
	GOWORK=off CGO_ENABLED=1 GOOS=windows GOARCH="$architecture" CC="$compiler" GOFLAGS= \
		"$go_licenses" save . \
		--ignore mdd-call-audio-helper \
		--save_path "$payload/THIRD-PARTY-LICENSES/go-audio-helper"
)

printf '%s\n' \
	"MDD Go Agent Windows development candidate" \
	"" \
	"The package contains two shells over the same Agent configuration and singleton:" \
	"  mdd-agent.exe  service and CLI" \
	"  MDD Agent.exe  tray GUI and Windows service management" \
	"  mdd-call-audio-helper.exe  exact-modem UAC audio boundary" \
	"" \
	"Initialize the shared configuration from a terminal, then install/start the service from an elevated terminal or the GUI." \
	"The GUI always registers the sibling mdd-agent.exe, never itself, as the service executable." \
	"" \
	"The default is PC/SC-only. On Windows, modem_enabled=true adds read-only MBN facts and" \
	"exclusive AT ownership plus typed call, verified hangup, PCM and SMS operations. Raw AT," \
	"DTMF and general APDU operations are not exposed. modem_sim_apdu_enabled=true separately" \
	"enables only ICCID-fenced typed USIM/ISIM AKA on that same exclusive owner." \
	"Configured SIM PIN1 recovery is ICCID-fenced, retry-count protected and never auto-retries" \
	"a failed or uncertain credential attempt; PUK/PIN2/network locks remain manual." \
	"" \
	"Whole-Modem raw mode is opt-in and configured per exact ICCID+IMEI in the MDD Web console." \
	"A Windows/Linux source Agent exports the complete USB parent through the authenticated Core" \
	"WSS; a separate service-host Agent imports it and then exposes it through the ordinary MDD" \
	"Modem adapter, so every function supported by that adapter remains available. Windows embeds" \
	"the sing-usbip VBoxUSB and usbip-win2 drivers and installs them on first use from the privileged" \
	"Agent service; no separate driver download is required. PC/SC/eSIM reader routing is unchanged." \
	>"$payload/README.txt"

printf '%s\n' \
	"version=$version" \
	"build=$build_number" \
	"architecture=$architecture" \
	"source_revision=$source_revision" \
	"source_tree=$source_state" \
	"fyne_runtime=v2.8.1" \
	"fyne_tools=$fyne_tools_version" \
	"go_serial=v1.8.0" \
	"warthog618_sms=v0.3.0" \
	"malgo=v0.11.26" \
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
