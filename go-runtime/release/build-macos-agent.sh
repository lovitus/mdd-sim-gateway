#!/bin/sh
set -eu

fyne_tools_version="v1.7.2"
app_id="com.mdd.agent"
app_name="MDD Agent"
version="0.1.0"
build_number="1"
output=""
identity=""
go_licenses="${GO_LICENSES-}"
deployment_target="15.0"
libusb_archive="libusb-1.0.30.tar.bz2"
libusb_url="https://github.com/libusb/libusb/releases/download/v1.0.30/$libusb_archive"
libusb_sha256="fea36f34f9156400209595e300840767ab1a385ede1dc7ee893015aea9c6dbaf"
lwip_archive="lwip-STABLE-2_2_1_RELEASE.tar.gz"
lwip_url="https://github.com/lwip-tcpip/lwip/archive/refs/tags/STABLE-2_2_1_RELEASE.tar.gz"
lwip_sha256="ce0b7461c0ad9602c376f0bf07c5eb7253b48c7bf66f011c6bf3e2a96731c539"

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
if [ -z "$go_licenses" ] || [ ! -x "$go_licenses" ]; then
	printf '%s\n' "GO_LICENSES must point to the pinned go-licenses executable" >&2
	exit 2
fi

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
runtime_root=$(CDPATH='' cd -- "$script_dir/.." && pwd)
repository_root=$(CDPATH='' cd -- "$runtime_root/.." && pwd)
icon="$repository_root/agent/assets/mdd-agent.png"
cellular_source="$repository_root/agent/cellular-io"
audio_source="$repository_root/agent/call-audio-helper"
cli_info_plist="$script_dir/macos-agent-info.plist"
entitlements="$script_dir/macos-agent.entitlements"
project_license="$repository_root/LICENSE"
project_notice="$repository_root/NOTICE"
third_party_notices="$repository_root/THIRD_PARTY_LICENSES.md"
if [ ! -f "$icon" ]; then
	printf '%s\n' "missing Agent icon: $icon" >&2
	exit 1
fi
for legal_file in "$project_license" "$project_notice" "$third_party_notices"; do
	if [ ! -f "$legal_file" ]; then
		printf '%s\n' "missing project legal file: $legal_file" >&2
		exit 1
	fi
done

staging=$(mktemp -d "$TMPDIR/mdd-agent-package.XXXXXX")
trap 'rm -rf "$staging"' EXIT HUP INT TERM
payload="$staging/MDD-Agent-macOS"
mkdir -p "$payload"

case ${MDD_SHARED_CACHE_ROOT-} in
"") ;;
/*) ;;
*)
	printf '%s\n' "MDD_SHARED_CACHE_ROOT must be an absolute path" >&2
	exit 2
	;;
esac
if [ -n "${MDD_SHARED_CACHE_ROOT-}" ]; then
	: "${GOCACHE:=$MDD_SHARED_CACHE_ROOT/go-build}"
	: "${GOMODCACHE:=$MDD_SHARED_CACHE_ROOT/go-mod}"
else
	: "${GOCACHE:=$(go env GOCACHE)}"
	: "${GOMODCACHE:=$(go env GOMODCACHE)}"
fi
: "${GOTMPDIR:=$TMPDIR/go-tmp}"
export GOCACHE GOMODCACHE GOTMPDIR
mkdir -p "$GOCACHE" "$GOMODCACHE" "$GOTMPDIR"

download_root="${MDD_SHARED_CACHE_ROOT:-$staging/cache}/downloads/macos-agent"
source_root="$staging/source"
prefix_root="$staging/prefix"
helper_root="$staging/helpers"
mkdir -p "$download_root" "$source_root" "$prefix_root" "$helper_root"

verify_sha256() {
	actual=$(shasum -a 256 "$1" | awk '{print $1}')
	[ "$actual" = "$2" ] || {
		printf '%s\n' "sha256 mismatch for $1" >&2
		exit 1
	}
}

fetch_archive() {
	name=$1
	url=$2
	expected=$3
	destination="$download_root/$name"
	if [ -f "$destination" ] && [ "$(shasum -a 256 "$destination" | awk '{print $1}')" = "$expected" ]; then
		return
	fi
	temporary=$(mktemp "$download_root/.${name}.XXXXXX")
	if ! curl --fail --location --retry 2 --connect-timeout 20 --output "$temporary" "$url"; then
		rm -f "$temporary"
		exit 1
	fi
	verify_sha256 "$temporary" "$expected"
	mv -f "$temporary" "$destination"
}

fetch_archive "$libusb_archive" "$libusb_url" "$libusb_sha256"
fetch_archive "$lwip_archive" "$lwip_url" "$lwip_sha256"
mkdir -p "$source_root/libusb" "$source_root/lwip"
tar -xf "$download_root/$libusb_archive" -C "$source_root/libusb"
tar -xf "$download_root/$lwip_archive" -C "$source_root/lwip"
libusb_source="$source_root/libusb/libusb-1.0.30"
lwip_source="$source_root/lwip/lwip-STABLE-2_2_1_RELEASE"
[ -d "$libusb_source" ] && [ -d "$lwip_source" ] || {
	printf '%s\n' "pinned cellular dependency archive has an unexpected layout" >&2
	exit 1
}

libusb_prefix="$prefix_root/libusb"
(
	cd "$libusb_source"
	CFLAGS="-arch arm64 -mmacosx-version-min=$deployment_target" \
	LDFLAGS="-arch arm64 -mmacosx-version-min=$deployment_target" \
		./configure --prefix="$libusb_prefix" --disable-shared --enable-static
	make -j "$(sysctl -n hw.ncpu)"
	make install
)
[ -f "$libusb_prefix/lib/libusb-1.0.a" ] || {
	printf '%s\n' "static libusb build is missing" >&2
	exit 1
}

cellular_build="$staging/cellular-build"
cmake -S "$cellular_source" -B "$cellular_build" \
	-DCMAKE_BUILD_TYPE=Release \
	-DCMAKE_OSX_ARCHITECTURES=arm64 \
	-DCMAKE_OSX_DEPLOYMENT_TARGET="$deployment_target" \
	-DLWIP_DIR="$lwip_source" \
	-DLIBUSB_ROOT="$libusb_prefix"
cmake --build "$cellular_build" --parallel "$(sysctl -n hw.ncpu)"
ctest --test-dir "$cellular_build" --output-on-failure
install -m 0755 "$cellular_build/mdd-cellular-io" "$helper_root/mdd-cellular-io"
if otool -L "$helper_root/mdd-cellular-io" | grep -qi libusb; then
	printf '%s\n' "mdd-cellular-io unexpectedly links dynamic libusb" >&2
	exit 1
fi

(
	cd "$audio_source"
	GOWORK=off go mod verify
	GOWORK=off go test ./...
	GOWORK=off go build -trimpath -o "$helper_root/mdd-call-audio-helper" .
)

source_revision=$(git -C "$repository_root" rev-parse HEAD)
source_state=clean
if [ -n "$(git -C "$repository_root" status --porcelain -- go-runtime agent/cellular-io agent/call-audio-helper)" ]; then
	source_state=dirty
fi

(
	cd "$runtime_root"
	CGO_ENABLED=1 go build -trimpath \
		-ldflags "-linkmode=external -extldflags=-Wl,-sectcreate,__TEXT,__info_plist,$cli_info_plist" \
		-o "$payload/mdd-agent" ./cmd/mdd-agent
)
install -m 0755 "$helper_root/mdd-cellular-io" "$payload/mdd-cellular-io"
install -m 0755 "$helper_root/mdd-call-audio-helper" "$payload/mdd-call-audio-helper"
mkdir -p "$payload/licenses"
install -m 0644 "$project_license" "$payload/LICENSE"
install -m 0644 "$project_notice" "$payload/NOTICE"
install -m 0644 "$third_party_notices" "$payload/THIRD_PARTY_LICENSES.md"
install -m 0644 "$cellular_source/THIRD_PARTY.md" "$payload/licenses/cellular-io-THIRD-PARTY.md"
install -m 0644 "$libusb_source/COPYING" "$payload/licenses/libusb-LGPL-2.1.txt"
install -m 0644 "$lwip_source/COPYING" "$payload/licenses/lwip-BSD-3-Clause.txt"
mkdir -p "$payload/corresponding-source"
install -m 0644 "$download_root/$libusb_archive" "$payload/corresponding-source/$libusb_archive"
install -m 0644 "$download_root/$lwip_archive" "$payload/corresponding-source/$lwip_archive"
printf '%s\n' \
	"# Corresponding source and relinking information" \
	"" \
	"This package was built from MDD revision $source_revision:" \
	"https://github.com/lovitus/mdd-sim-gateway/tree/$source_revision" \
	"" \
	"The complete mdd-cellular-io source and reproducible build entrypoint are:" \
	"- agent/cellular-io/" \
	"- go-runtime/release/build-macos-agent.sh" \
	"" \
	"The exact external source archives used for relinking are included beside this file:" \
	"- $libusb_archive (SHA-256 $libusb_sha256, LGPL-2.1-or-later)" \
	"- $lwip_archive (SHA-256 $lwip_sha256, BSD-3-Clause)" \
	"" \
	"Set a task-scoped TMPDIR and run the build entrypoint at the recorded revision to rebuild" \
	"mdd-cellular-io with a modified libusb. The project is GPL-3.0-only and its complete source" \
	"is available at the revision URL above." \
	>"$payload/corresponding-source/SOURCE.md"

(
	cd "$runtime_root"
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 GOFLAGS= \
		"$go_licenses" save ./cmd/mdd-agent \
		--ignore github.com/lovitus/mdd-sim-gateway/go-runtime \
		--save_path "$payload/licenses/go-cli"
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 GOFLAGS=-tags=gui \
		"$go_licenses" save ./cmd/mdd-agent \
		--ignore github.com/lovitus/mdd-sim-gateway/go-runtime \
		--save_path "$payload/licenses/go-gui"
)
(
	cd "$audio_source"
	GOWORK=off CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 GOFLAGS= \
		"$go_licenses" save . \
		--ignore mdd-call-audio-helper \
		--save_path "$payload/licenses/go-audio-helper"
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
/usr/libexec/PlistBuddy -c 'Delete :NSMicrophoneUsageDescription' "$payload/$app_name.app/Contents/Info.plist" 2>/dev/null || true
/usr/libexec/PlistBuddy -c 'Add :NSMicrophoneUsageDescription string MDD Agent uses the exact USB modem audio device for cellular calls.' "$payload/$app_name.app/Contents/Info.plist"
install -m 0755 "$helper_root/mdd-cellular-io" "$payload/$app_name.app/Contents/MacOS/mdd-cellular-io"
install -m 0755 "$helper_root/mdd-call-audio-helper" "$payload/$app_name.app/Contents/MacOS/mdd-call-audio-helper"

if [ -n "$identity" ]; then
	codesign --force --timestamp --options runtime --sign "$identity" "$payload/mdd-cellular-io"
	codesign --force --timestamp --options runtime --entitlements "$entitlements" --sign "$identity" "$payload/mdd-call-audio-helper"
	codesign --force --timestamp --options runtime --entitlements "$entitlements" --sign "$identity" "$payload/mdd-agent"
	codesign --force --timestamp --options runtime --sign "$identity" "$payload/$app_name.app/Contents/MacOS/mdd-cellular-io"
	codesign --force --timestamp --options runtime --entitlements "$entitlements" --sign "$identity" "$payload/$app_name.app/Contents/MacOS/mdd-call-audio-helper"
	codesign --force --timestamp --options runtime --entitlements "$entitlements" --sign "$identity" "$app_executable"
	codesign --force --timestamp --options runtime --entitlements "$entitlements" --sign "$identity" "$payload/$app_name.app"
	signing=developer-id
else
	codesign --force --sign - "$payload/mdd-cellular-io"
	codesign --force --entitlements "$entitlements" --sign - "$payload/mdd-call-audio-helper"
	codesign --force --entitlements "$entitlements" --sign - "$payload/mdd-agent"
	codesign --force --sign - "$payload/$app_name.app/Contents/MacOS/mdd-cellular-io"
	codesign --force --entitlements "$entitlements" --sign - "$payload/$app_name.app/Contents/MacOS/mdd-call-audio-helper"
	codesign --force --entitlements "$entitlements" --sign - "$app_executable"
	codesign --force --entitlements "$entitlements" --sign - "$payload/$app_name.app"
	signing=ad-hoc-development
fi
for executable in "$payload/mdd-agent" "$payload/mdd-cellular-io" "$payload/mdd-call-audio-helper"; do
	codesign --verify --strict "$executable"
done
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
	"The default remains PC/SC-only. Set modem_enabled=true explicitly to claim supported raw-USB modems with private PPP; the Mac does not receive a cellular interface or route." \
	>"$payload/README.txt"

printf '%s\n' \
	"version=$version" \
	"build=$build_number" \
	"architecture=$(uname -m)" \
	"source_revision=$source_revision" \
	"source_tree=$source_state" \
	"fyne_runtime=v2.8.1" \
	"fyne_tools=$fyne_tools_version" \
	"libusb=1.0.30" \
	"lwip=2.2.1" \
	"cellular_io_protocol=1" \
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
