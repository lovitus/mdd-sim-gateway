#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)"
AGENT_DIR="$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd -P)"
REPO_DIR="$(CDPATH= cd -- "$AGENT_DIR/.." && pwd -P)"

BUILD_ROOT=""
OUTPUT_DIR=""
ARCHIVES_DIR=""
WHEELHOUSE=""
WHEELHOUSE_MANIFEST=""
WHEELHOUSE_MANIFEST_SHA256=""
SIGN_IDENTITY=""
MODE="development"
PYTHON_BOOTSTRAP="${MDD_PYTHON_BOOTSTRAP:-python3}"
JOBS="${MDD_BUILD_JOBS:-$(/usr/sbin/sysctl -n hw.ncpu 2>/dev/null || echo 2)}"
MACOS_DEPLOYMENT_TARGET="${MDD_MACOS_DEPLOYMENT_TARGET:-15.0}"
ARCHITECTURE="${MDD_AGENT_PACKAGE_ARCH:-macos-arm64}"

LIBUSB_ARCHIVE="libusb-1.0.30.tar.bz2"
LIBUSB_URL="https://github.com/libusb/libusb/releases/download/v1.0.30/${LIBUSB_ARCHIVE}"
LIBUSB_SHA256="fea36f34f9156400209595e300840767ab1a385ede1dc7ee893015aea9c6dbaf"
LWIP_ARCHIVE="lwip-STABLE-2_2_1_RELEASE.tar.gz"
LWIP_URL="https://github.com/lwip-tcpip/lwip/archive/refs/tags/STABLE-2_2_1_RELEASE.tar.gz"
LWIP_SHA256="ce0b7461c0ad9602c376f0bf07c5eb7253b48c7bf66f011c6bf3e2a96731c539"

usage() {
  sed -n '2,80p' "$0" >&2
  cat >&2 <<'USAGE'

Usage:
  Build-MacOS-Release.sh --build-root DIR --output-dir DIR --development [--archives-dir DIR]
  Build-MacOS-Release.sh --build-root DIR --output-dir DIR --release \
    --sign-identity ID --wheelhouse DIR --wheelhouse-manifest FILE \
    --wheelhouse-manifest-sha256 SHA256 [--archives-dir DIR]

Development mode may use network pip and produces no Control allowlist.
Release mode requires a signed app/CLI/helpers and a verified offline wheelhouse.
USAGE
}

fail() {
  echo "$*" >&2
  exit 1
}

require_tool() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required build tool: $1"
}

verify_sha256() {
  local path="$1"
  local expected="$2"
  local actual
  actual="$(shasum -a 256 "$path" | awk '{print $1}')"
  [ "$actual" = "$expected" ] ||
    fail "sha256 mismatch for $path: expected $expected, got $actual"
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --build-root)
      [ "$#" -ge 2 ] || fail "--build-root requires a value"
      BUILD_ROOT="$2"; shift 2;;
    --output-dir)
      [ "$#" -ge 2 ] || fail "--output-dir requires a value"
      OUTPUT_DIR="$2"; shift 2;;
    --archives-dir)
      [ "$#" -ge 2 ] || fail "--archives-dir requires a value"
      ARCHIVES_DIR="$2"; shift 2;;
    --release)
      MODE="release"; shift;;
    --development)
      MODE="development"; shift;;
    --sign-identity)
      [ "$#" -ge 2 ] || fail "--sign-identity requires a value"
      SIGN_IDENTITY="$2"; shift 2;;
    --wheelhouse)
      [ "$#" -ge 2 ] || fail "--wheelhouse requires a value"
      WHEELHOUSE="$2"; shift 2;;
    --wheelhouse-manifest)
      [ "$#" -ge 2 ] || fail "--wheelhouse-manifest requires a value"
      WHEELHOUSE_MANIFEST="$2"; shift 2;;
    --wheelhouse-manifest-sha256)
      [ "$#" -ge 2 ] || fail "--wheelhouse-manifest-sha256 requires a value"
      WHEELHOUSE_MANIFEST_SHA256="$2"; shift 2;;
    -h|--help)
      usage; exit 0;;
    *)
      fail "unknown argument: $1";;
  esac
done

[ -n "$BUILD_ROOT" ] || fail "--build-root is required"
[ -n "$OUTPUT_DIR" ] || fail "--output-dir is required"

mkdir -p "$BUILD_ROOT"
BUILD_ROOT="$(CDPATH= cd -- "$BUILD_ROOT" && pwd -P)"
case "$BUILD_ROOT" in
  /tmp|/tmp/*|/private/tmp|/private/tmp/*|/var/folders|/var/folders/*)
    fail "--build-root must not use a macOS system temporary directory";;
esac
case "$BUILD_ROOT/" in
  "$REPO_DIR"|"$REPO_DIR/"|"$REPO_DIR/"*)
    fail "--build-root must be outside the Git worktree";;
esac

if [ "$MODE" = "release" ]; then
  [ -n "$SIGN_IDENTITY" ] || fail "--release requires --sign-identity"
  [ "$SIGN_IDENTITY" != "-" ] ||
    fail "--release requires a Developer ID Application identity; ad-hoc signing is forbidden"
  require_tool security
  SIGN_IDENTITY_RECORD="$(security find-identity -v -p codesigning 2>/dev/null |
    grep -F -- "$SIGN_IDENTITY" | head -n 1 || true)"
  case "$SIGN_IDENTITY_RECORD" in
    *"Developer ID Application:"*) ;;
    *) fail "--release sign identity must resolve to Developer ID Application";;
  esac
  [ -n "$WHEELHOUSE" ] || fail "--release requires --wheelhouse"
  [ -n "$WHEELHOUSE_MANIFEST" ] || fail "--release requires --wheelhouse-manifest"
  [ -n "$WHEELHOUSE_MANIFEST_SHA256" ] ||
    fail "--release requires --wheelhouse-manifest-sha256"
else
  SIGN_IDENTITY=""
fi

require_tool "$PYTHON_BOOTSTRAP"
require_tool curl
require_tool shasum
require_tool awk
require_tool tar
require_tool make
require_tool clang
require_tool cmake
require_tool go
require_tool file
require_tool otool
require_tool lipo
if [ "$MODE" = "release" ]; then
  require_tool codesign
fi

canonical_new_output_dir() {
  local path="$1"
  [ -n "$path" ] || fail "--output-dir must not be empty"
  local parent
  parent="$(dirname -- "$path")"
  local leaf
  leaf="$(basename -- "$path")"
  [ "$leaf" != "." ] && [ "$leaf" != "/" ] ||
    fail "--output-dir must name a concrete package directory"
  mkdir -p "$parent"
  local parent_real
  parent_real="$(CDPATH= cd -- "$parent" && pwd -P)"
  local output_real="$parent_real/$leaf"
  case "$output_real" in
    /|/tmp|/tmp/*|/private/tmp|/private/tmp/*|/var/folders|/var/folders/*)
      fail "--output-dir must not use a macOS system temporary directory";;
  esac
  case "$output_real/" in
    "$REPO_DIR/"|"$REPO_DIR/"*)
      fail "--output-dir must be outside the Git worktree";;
  esac
  [ ! -e "$output_real" ] ||
    fail "--output-dir already exists; choose a new package directory: $output_real"
  printf '%s\n' "$output_real"
}

OUTPUT_DIR="$(canonical_new_output_dir "$OUTPUT_DIR")"

DOWNLOAD_DIR="$BUILD_ROOT/downloads"
SRC_DIR="$BUILD_ROOT/src"
PREFIX_DIR="$BUILD_ROOT/prefix"
HELPER_DIR="$BUILD_ROOT/helpers"
PYINSTALLER_DIST="$BUILD_ROOT/pyinstaller-dist"
PYINSTALLER_WORK="$BUILD_ROOT/pyinstaller-work"
VENV_DIR="$BUILD_ROOT/venv"
mkdir -p "$DOWNLOAD_DIR" "$SRC_DIR" "$PREFIX_DIR" "$HELPER_DIR" \
  "$PYINSTALLER_DIST" "$PYINSTALLER_WORK" "$BUILD_ROOT/tmp" \
  "$BUILD_ROOT/go-cache" "$BUILD_ROOT/go-mod" "$BUILD_ROOT/go-tmp"

fetch_archive() {
  local name="$1"
  local url="$2"
  local expected_sha="$3"
  local output="$DOWNLOAD_DIR/$name"
  if [ -n "$ARCHIVES_DIR" ] && [ -f "$ARCHIVES_DIR/$name" ]; then
    cp "$ARCHIVES_DIR/$name" "$output"
  else
    curl -fL --retry 2 --connect-timeout 20 -o "$output" "$url"
  fi
  verify_sha256 "$output" "$expected_sha"
}

safe_extract_tar() {
  local archive="$1"
  local destination="$2"
  rm -rf "$destination"
  mkdir -p "$destination"
  "$PYTHON_BOOTSTRAP" - "$archive" "$destination" <<'PY'
from __future__ import annotations

import pathlib
import sys
import tarfile

archive = pathlib.Path(sys.argv[1])
destination = pathlib.Path(sys.argv[2]).resolve()
with tarfile.open(archive, "r:*") as tar:
    for member in tar.getmembers():
        name = member.name
        Pure = pathlib.PurePosixPath(name)
        if Pure.is_absolute() or ".." in Pure.parts:
            raise SystemExit(f"unsafe archive path: {name}")
        if member.issym() or member.islnk():
            raise SystemExit(f"unsafe archive link entry: {name}")
        target = (destination / name).resolve()
        if target != destination and destination not in target.parents:
            raise SystemExit(f"archive entry escapes destination: {name}")
    tar.extractall(destination)
PY
}

verify_wheelhouse() {
  verify_sha256 "$WHEELHOUSE_MANIFEST" "$WHEELHOUSE_MANIFEST_SHA256"
  "$PYTHON_BOOTSTRAP" - "$WHEELHOUSE" "$WHEELHOUSE_MANIFEST" <<'PY'
from __future__ import annotations

import hashlib
import pathlib
import re
import sys

wheelhouse = pathlib.Path(sys.argv[1]).resolve()
manifest = pathlib.Path(sys.argv[2])
pattern = re.compile(r"^(?:sha256:)?([0-9a-fA-F]{64})[ \t]+(.+)$")
expected_paths = set()
for line_number, raw in enumerate(manifest.read_text(encoding="utf-8").splitlines(), 1):
    line = raw.strip()
    if not line or line.startswith("#"):
        continue
    match = pattern.match(line)
    if not match:
        raise SystemExit(f"invalid wheelhouse manifest line {line_number}")
    expected, name = match.group(1).lower(), match.group(2).strip()
    Pure = pathlib.PurePosixPath(name.replace("\\", "/"))
    if Pure.is_absolute() or ".." in Pure.parts:
        raise SystemExit(f"unsafe wheelhouse path on line {line_number}")
    path = (wheelhouse / Pure.as_posix()).resolve()
    if path != wheelhouse and wheelhouse not in path.parents:
        raise SystemExit(f"wheelhouse entry escapes root on line {line_number}")
    expected_paths.add(path)
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    if digest != expected:
        raise SystemExit(f"wheelhouse sha256 mismatch: {name}")
for path in wheelhouse.rglob("*.whl"):
    if path.resolve() not in expected_paths:
        raise SystemExit(f"unverified wheelhouse wheel: {path.relative_to(wheelhouse)}")
PY
}

fetch_archive "$LIBUSB_ARCHIVE" "$LIBUSB_URL" "$LIBUSB_SHA256"
fetch_archive "$LWIP_ARCHIVE" "$LWIP_URL" "$LWIP_SHA256"
safe_extract_tar "$DOWNLOAD_DIR/$LIBUSB_ARCHIVE" "$SRC_DIR/libusb"
safe_extract_tar "$DOWNLOAD_DIR/$LWIP_ARCHIVE" "$SRC_DIR/lwip"

LIBUSB_SRC="$SRC_DIR/libusb/libusb-1.0.30"
LWIP_DIR="$SRC_DIR/lwip/lwip-STABLE-2_2_1_RELEASE"
[ -d "$LIBUSB_SRC" ] || fail "libusb source root not found after extraction"
[ -d "$LWIP_DIR" ] || fail "lwIP source root not found after extraction"

"$PYTHON_BOOTSTRAP" -m venv "$VENV_DIR"
VENV_PYTHON="$VENV_DIR/bin/python"
if [ "$MODE" = "release" ]; then
  verify_wheelhouse
  "$VENV_PYTHON" -m pip install --no-index --find-links "$WHEELHOUSE" \
    -r "$AGENT_DIR/requirements-modem-build.txt"
else
  PIP_CACHE_DIR="$BUILD_ROOT/pip-cache" "$VENV_PYTHON" -m pip install \
    -r "$AGENT_DIR/requirements-modem-build.txt"
fi

CALL_AUDIO_BINARY="$HELPER_DIR/mdd-call-audio-helper"
(
  cd "$AGENT_DIR/call-audio-helper"
  TMPDIR="$BUILD_ROOT/tmp" GOCACHE="$BUILD_ROOT/go-cache" \
    GOMODCACHE="$BUILD_ROOT/go-mod" GOTMPDIR="$BUILD_ROOT/go-tmp" \
    GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 \
    CGO_CFLAGS="-arch arm64 -mmacosx-version-min=$MACOS_DEPLOYMENT_TARGET" \
    CGO_LDFLAGS="-arch arm64 -mmacosx-version-min=$MACOS_DEPLOYMENT_TARGET" \
    go mod verify
  TMPDIR="$BUILD_ROOT/tmp" GOCACHE="$BUILD_ROOT/go-cache" \
    GOMODCACHE="$BUILD_ROOT/go-mod" GOTMPDIR="$BUILD_ROOT/go-tmp" \
    GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 \
    CGO_CFLAGS="-arch arm64 -mmacosx-version-min=$MACOS_DEPLOYMENT_TARGET" \
    CGO_LDFLAGS="-arch arm64 -mmacosx-version-min=$MACOS_DEPLOYMENT_TARGET" \
    go test ./...
  TMPDIR="$BUILD_ROOT/tmp" GOCACHE="$BUILD_ROOT/go-cache" \
    GOMODCACHE="$BUILD_ROOT/go-mod" GOTMPDIR="$BUILD_ROOT/go-tmp" \
    GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 \
    CGO_CFLAGS="-arch arm64 -mmacosx-version-min=$MACOS_DEPLOYMENT_TARGET" \
    CGO_LDFLAGS="-arch arm64 -mmacosx-version-min=$MACOS_DEPLOYMENT_TARGET" \
    go build -trimpath -o "$CALL_AUDIO_BINARY" .
)

LIBUSB_PREFIX="$PREFIX_DIR/libusb"
(
  cd "$LIBUSB_SRC"
  CFLAGS="-arch arm64 -mmacosx-version-min=$MACOS_DEPLOYMENT_TARGET" \
  LDFLAGS="-arch arm64 -mmacosx-version-min=$MACOS_DEPLOYMENT_TARGET" \
    ./configure --prefix="$LIBUSB_PREFIX" --disable-shared --enable-static
  make -j "$JOBS"
  make install
)
[ -f "$LIBUSB_PREFIX/lib/libusb-1.0.a" ] ||
  fail "static libusb archive was not produced"

CELLULAR_BUILD="$BUILD_ROOT/build/cellular-io"
CELLULAR_BINARY="$HELPER_DIR/mdd-cellular-io"
cmake -S "$AGENT_DIR/cellular-io" -B "$CELLULAR_BUILD" \
  -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_OSX_ARCHITECTURES=arm64 \
  -DCMAKE_OSX_DEPLOYMENT_TARGET="$MACOS_DEPLOYMENT_TARGET" \
  -DLWIP_DIR="$LWIP_DIR" \
  -DLIBUSB_ROOT="$LIBUSB_PREFIX"
cmake --build "$CELLULAR_BUILD" --parallel "$JOBS"
ctest --test-dir "$CELLULAR_BUILD" --output-on-failure
install -m 0755 "$CELLULAR_BUILD/mdd-cellular-io" "$CELLULAR_BINARY"
file "$CELLULAR_BINARY" | grep -q "arm64" ||
  fail "mdd-cellular-io is not an arm64 binary"
if otool -L "$CELLULAR_BINARY" | grep -i "libusb" >/dev/null; then
  fail "mdd-cellular-io links a dynamic libusb; expected static libusb"
fi

verify_macho_arm64() {
  local target="$1"
  file "$target" | grep -q "arm64" || fail "Mach-O is not arm64: $target"
  lipo -archs "$target" | tr ' ' '\n' | grep -qx "arm64" ||
    fail "Mach-O lacks arm64 architecture: $target"
}

verify_macho_tree_arm64() {
  local root="$1"
  while IFS= read -r -d '' path; do
    if file "$path" | grep -q "Mach-O"; then
      verify_macho_arm64 "$path"
    fi
  done < <(find "$root" -type f -print0)
}

(
  cd "$AGENT_DIR"
  PYINSTALLER_CODESIGN_IDENTITY=""
  if [ "$MODE" = "release" ]; then
    PYINSTALLER_CODESIGN_IDENTITY="$SIGN_IDENTITY"
  fi
  MDD_CELLULAR_IO_BINARY="$CELLULAR_BINARY" MDD_CALL_AUDIO_BINARY="$CALL_AUDIO_BINARY" \
    MDD_PYINSTALLER_CODESIGN_IDENTITY="$PYINSTALLER_CODESIGN_IDENTITY" \
    "$VENV_PYTHON" -m PyInstaller --noconfirm --clean \
    --distpath "$PYINSTALLER_DIST" --workpath "$PYINSTALLER_WORK" \
    mdd-agent.spec
  MDD_CELLULAR_IO_BINARY="$CELLULAR_BINARY" MDD_CALL_AUDIO_BINARY="$CALL_AUDIO_BINARY" \
    MDD_PYINSTALLER_CODESIGN_IDENTITY="$PYINSTALLER_CODESIGN_IDENTITY" \
    "$VENV_PYTHON" -m PyInstaller --noconfirm --clean \
    --distpath "$PYINSTALLER_DIST" --workpath "$PYINSTALLER_WORK" \
    mdd-agent-gui.spec
)

verify_macho_arm64 "$CALL_AUDIO_BINARY"
verify_macho_arm64 "$CELLULAR_BINARY"
verify_macho_arm64 "$PYINSTALLER_DIST/mdd-agent"
verify_macho_tree_arm64 "$PYINSTALLER_DIST/MDD Agent.app/Contents"

sign_and_verify() {
  local target="$1"
  local entitlements="${2:-}"
  if [ -n "$entitlements" ]; then
    codesign --force --timestamp --options runtime --entitlements "$entitlements" \
      --sign "$SIGN_IDENTITY" "$target"
  else
    codesign --force --timestamp --options runtime --sign "$SIGN_IDENTITY" "$target"
  fi
  codesign --verify --strict --verbose=2 "$target"
}

sign_app_bundle() {
  local app="$1"
  local entitlements="$AGENT_DIR/macos/MDD-Agent.entitlements"
  while IFS= read -r -d '' path; do
    if file "$path" | grep -q "Mach-O"; then
      if [ "$(basename "$path")" = "mdd-call-audio-helper" ] || \
          [ "$path" = "$app/Contents/MacOS/mdd-agent-gui" ]; then
        codesign --force --timestamp --options runtime --entitlements "$entitlements" \
          --sign "$SIGN_IDENTITY" "$path"
      else
        codesign --force --timestamp --options runtime --sign "$SIGN_IDENTITY" "$path"
      fi
    fi
  done < <(find "$app/Contents" -type f -print0)
  codesign --force --timestamp --options runtime --entitlements "$entitlements" \
    --sign "$SIGN_IDENTITY" "$app"
  codesign --verify --deep --strict --verbose=2 "$app"
}

verify_pyinstaller_cli_starts() {
  local target="$1"
  local help_output="$BUILD_ROOT/tmp/mdd-agent-help-smoke.txt"
  "$target" --help > "$help_output"
  grep -q "usage: mdd-agent" "$help_output" ||
    fail "PyInstaller CLI smoke test produced unexpected help output"
}

PACKAGE_ARGS=(--skip-pyinstaller --dist-dir "$PYINSTALLER_DIST" \
  --output-dir "$OUTPUT_DIR" --pyinstaller-workpath "$PYINSTALLER_WORK" \
  --architecture "$ARCHITECTURE")

if [ "$MODE" = "release" ]; then
  AUDIO_ENTITLEMENTS="$AGENT_DIR/macos/MDD-Agent.entitlements"
  sign_and_verify "$PYINSTALLER_DIST/mdd-agent" "$AUDIO_ENTITLEMENTS"
  sign_and_verify "$CELLULAR_BINARY"
  sign_and_verify "$CALL_AUDIO_BINARY" "$AUDIO_ENTITLEMENTS"
  sign_app_bundle "$PYINSTALLER_DIST/MDD Agent.app"
  verify_pyinstaller_cli_starts "$PYINSTALLER_DIST/mdd-agent"
  PACKAGE_ARGS+=(--verified-release)
else
  PACKAGE_ARGS+=(--unsigned-development)
fi

MDD_PYTHON="$VENV_PYTHON" MDD_CELLULAR_IO_BINARY="$CELLULAR_BINARY" \
  MDD_CALL_AUDIO_BINARY="$CALL_AUDIO_BINARY" \
  "$SCRIPT_DIR/Build-MacOS-Package.sh" "${PACKAGE_ARGS[@]}"

if [ "$MODE" = "release" ]; then
  verify_pyinstaller_cli_starts "$OUTPUT_DIR/mdd-agent"
  [ -f "$OUTPUT_DIR/control-agent-allowlist.env" ] ||
    fail "release package did not generate Control allowlist"
else
  [ ! -e "$OUTPUT_DIR/control-agent-allowlist.env" ] ||
    fail "development package must not generate Control allowlist"
fi
