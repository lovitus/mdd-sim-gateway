#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)"
AGENT_DIR="$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd -P)"
REPO_DIR="$(CDPATH= cd -- "$AGENT_DIR/.." && pwd -P)"
OUTPUT_DIR="$AGENT_DIR/dist/mdd-agent-macos-arm64"
DIST_DIR="$AGENT_DIR/dist"
ARCHITECTURE="${MDD_AGENT_PACKAGE_ARCH:-macos-arm64}"
PYTHON="${MDD_PYTHON:-python3}"
PYINSTALLER_WORKPATH="${MDD_PYINSTALLER_WORKPATH:-$AGENT_DIR/build/pyinstaller}"
SKIP_PYINSTALLER=0
UNSIGNED_DEVELOPMENT=0
VERIFIED_RELEASE=0
OVERWRITE=0

while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-dir)
      [ "$#" -ge 2 ] || { echo "--output-dir requires a value" >&2; exit 2; }
      OUTPUT_DIR="$2"; shift 2;;
    --architecture)
      [ "$#" -ge 2 ] || { echo "--architecture requires a value" >&2; exit 2; }
      ARCHITECTURE="$2"; shift 2;;
    --dist-dir)
      [ "$#" -ge 2 ] || { echo "--dist-dir requires a value" >&2; exit 2; }
      DIST_DIR="$2"; shift 2;;
    --pyinstaller-workpath)
      [ "$#" -ge 2 ] || { echo "--pyinstaller-workpath requires a value" >&2; exit 2; }
      PYINSTALLER_WORKPATH="$2"; shift 2;;
    --skip-pyinstaller)
      SKIP_PYINSTALLER=1; shift;;
    --unsigned-development)
      UNSIGNED_DEVELOPMENT=1; shift;;
    --verified-release)
      VERIFIED_RELEASE=1; shift;;
    --overwrite)
      OVERWRITE=1; shift;;
    *)
      echo "unknown argument: $1" >&2; exit 2;;
  esac
done

[ "$((UNSIGNED_DEVELOPMENT + VERIFIED_RELEASE))" -eq 1 ] || {
  echo "choose exactly one package mode: --unsigned-development or --verified-release" >&2
  exit 2
}

require_file() {
  [ -f "$1" ] || { echo "missing required package input: $1" >&2; exit 1; }
}

require_dir() {
  [ -d "$1" ] || { echo "missing required package input: $1" >&2; exit 1; }
}

copy_executable() {
  require_file "$1"
  install -m 0755 "$1" "$2"
}

canonical_output_dir() {
  local path="$1"
  [ -n "$path" ] || { echo "--output-dir must not be empty" >&2; exit 2; }
  local parent
  parent="$(dirname -- "$path")"
  local leaf
  leaf="$(basename -- "$path")"
  [ "$leaf" != "." ] && [ "$leaf" != "/" ] ||
    { echo "--output-dir must name a concrete package directory" >&2; exit 2; }
  mkdir -p "$parent"
  local parent_real
  parent_real="$(CDPATH= cd -- "$parent" && pwd -P)"
  printf '%s/%s\n' "$parent_real" "$leaf"
}

OUTPUT_DIR="$(canonical_output_dir "$OUTPUT_DIR")"
case "$OUTPUT_DIR" in
  /|"$REPO_DIR")
    echo "refusing unsafe package output path: $OUTPUT_DIR" >&2
    exit 2;;
esac
if [ -e "$OUTPUT_DIR" ]; then
  if [ "$OVERWRITE" -ne 1 ]; then
    echo "package output already exists; pass --overwrite for an explicit replacement: $OUTPUT_DIR" >&2
    exit 2
  fi
  case "$OUTPUT_DIR/" in
    /|/tmp/*|/private/tmp/*|/var/folders/*|"$REPO_DIR/"|"$REPO_DIR/"*)
      echo "refusing to overwrite unsafe package output path: $OUTPUT_DIR" >&2
      exit 2;;
  esac
  rm -rf "$OUTPUT_DIR"
fi

verify_macho_arm64() {
  local target="$1"
  if ! file "$target" | grep -q "Mach-O"; then
    return 0
  fi
  file "$target" | grep -q "arm64" || {
    echo "Mach-O payload is not arm64: $target" >&2
    exit 1
  }
  lipo -archs "$target" | tr ' ' '\n' | grep -qx "arm64" || {
    echo "Mach-O payload lacks arm64 architecture: $target" >&2
    exit 1
  }
}

verify_macho_tree_arm64() {
  [ "$ARCHITECTURE" = "macos-arm64" ] || return 0
  command -v file >/dev/null 2>&1 || { echo "missing required tool: file" >&2; exit 1; }
  command -v lipo >/dev/null 2>&1 || { echo "missing required tool: lipo" >&2; exit 1; }
  while IFS= read -r -d '' path; do
    verify_macho_arm64 "$path"
  done < <(find "$1" -type f -print0)
}

codesign_metadata() {
  codesign -dv --verbose=4 "$1" 2>&1
}

verify_developer_id_code() {
  local target="$1"
  local expected_team="$2"
  local metadata requirement team
  codesign --verify --strict --verbose=2 "$target" >/dev/null 2>&1 || {
    echo "invalid code signature: $target" >&2
    exit 1
  }
  metadata="$(codesign_metadata "$target")"
  printf '%s\n' "$metadata" | grep -F "Authority=Developer ID Application:" >/dev/null || {
    echo "release code is not signed by Developer ID Application: $target" >&2
    exit 1
  }
  team="$(printf '%s\n' "$metadata" | awk -F= '/^TeamIdentifier=/{print $2; exit}')"
  [ -n "$team" ] && [ "$team" != "not set" ] || {
    echo "release code has no stable TeamIdentifier: $target" >&2
    exit 1
  }
  if [ -n "$expected_team" ] && [ "$team" != "$expected_team" ]; then
    echo "release code TeamIdentifier mismatch: $target" >&2
    exit 1
  fi
  requirement="$(codesign -d -r- "$target" 2>&1)"
  printf '%s\n' "$requirement" | grep -F 'cdhash H"' >/dev/null && {
    echo "release code uses a version-bound ad-hoc designated requirement: $target" >&2
    exit 1
  }
  printf '%s\n' "$team"
}

verify_release_signatures() {
  command -v codesign >/dev/null 2>&1 || {
    echo "missing required tool for verified release: codesign" >&2
    exit 1
  }
  local team path observed
  team="$(verify_developer_id_code "$DIST_DIR/mdd-agent" "")"
  verify_developer_id_code "$CELLULAR_IO" "$team" >/dev/null
  verify_developer_id_code "$CALL_AUDIO" "$team" >/dev/null
  codesign --verify --deep --strict --verbose=2 "$DIST_DIR/MDD Agent.app" >/dev/null 2>&1 || {
    echo "invalid application bundle signature" >&2
    exit 1
  }
  verify_developer_id_code "$DIST_DIR/MDD Agent.app" "$team" >/dev/null
  while IFS= read -r -d '' path; do
    if file "$path" | grep -q "Mach-O"; then
      observed="$(verify_developer_id_code "$path" "$team")"
      [ "$observed" = "$team" ] || exit 1
    fi
  done < <(find "$DIST_DIR/MDD Agent.app/Contents" -type f -print0)
}

if [ "$SKIP_PYINSTALLER" -ne 1 ]; then
  (cd "$AGENT_DIR" && "$PYTHON" -m PyInstaller --noconfirm --clean \
    --distpath "$DIST_DIR" --workpath "$PYINSTALLER_WORKPATH" \
    mdd-agent.spec)
  (cd "$AGENT_DIR" && "$PYTHON" -m PyInstaller --noconfirm --clean \
    --distpath "$DIST_DIR" --workpath "$PYINSTALLER_WORKPATH" \
    mdd-agent-gui.spec)
fi

CELLULAR_IO="${MDD_CELLULAR_IO_BINARY:-}"
CALL_AUDIO="${MDD_CALL_AUDIO_BINARY:-}"
require_file "$CELLULAR_IO"
require_file "$CALL_AUDIO"
require_file "$DIST_DIR/mdd-agent"
require_dir "$DIST_DIR/MDD Agent.app"

if [ "$VERIFIED_RELEASE" -eq 1 ]; then
  verify_release_signatures
fi

mkdir -p "$OUTPUT_DIR"
copy_executable "$DIST_DIR/mdd-agent" "$OUTPUT_DIR/mdd-agent"
copy_executable "$CELLULAR_IO" "$OUTPUT_DIR/mdd-cellular-io"
copy_executable "$CALL_AUDIO" "$OUTPUT_DIR/mdd-call-audio-helper"
cp -R "$DIST_DIR/MDD Agent.app" "$OUTPUT_DIR/MDD Agent.app"
install -m 0644 "$AGENT_DIR/MODEM_AGENT.md" "$OUTPUT_DIR/MODEM_AGENT.md"
install -m 0644 "$AGENT_DIR/../VERSION" "$OUTPUT_DIR/VERSION"
install -m 0755 "$AGENT_DIR/run-macos.command" "$OUTPUT_DIR/run-macos.command"
verify_macho_tree_arm64 "$OUTPUT_DIR"
if [ "$UNSIGNED_DEVELOPMENT" -eq 1 ]; then
  install -m 0644 /dev/null "$OUTPUT_DIR/UNSIGNED_DEVELOPMENT_ARTIFACT"
fi
if [ "$VERIFIED_RELEASE" -eq 1 ]; then
  "$PYTHON" "$AGENT_DIR/macos/verify_release_entitlements.py" "$OUTPUT_DIR"
fi

MANIFEST_ARGS=("$OUTPUT_DIR" --architecture "$ARCHITECTURE")
if [ "$UNSIGNED_DEVELOPMENT" -eq 1 ]; then
  MANIFEST_ARGS+=(--no-allowlist)
fi
DIGEST="$("$PYTHON" "$AGENT_DIR/package_manifest.py" "${MANIFEST_ARGS[@]}")"
"$PYTHON" "$AGENT_DIR/package_manifest.py" \
  --verify "$OUTPUT_DIR/manifest.json" --expect-digest "$DIGEST" >/dev/null

printf 'macOS Agent package: %s\n' "$OUTPUT_DIR"
printf 'Agent package digest: %s\n' "$DIGEST"
if [ "$UNSIGNED_DEVELOPMENT" -eq 1 ]; then
  printf 'unsigned development artifact: no Control allowlist generated\n'
else
  printf 'verified Developer ID release; Control allowlist env: %s\n' \
    "$OUTPUT_DIR/control-agent-allowlist.env"
fi
