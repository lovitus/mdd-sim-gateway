#!/bin/bash
# Print Debian packages owning the shared-library closure of copied runtime payloads.
set -euo pipefail

output="${1:?output path required}"
shift
[ "$#" -gt 0 ] || { echo "at least one runtime root is required" >&2; exit 2; }

libraries=$(mktemp)
packages=$(mktemp)
trap 'rm -f "$libraries" "$packages"' EXIT

library_path=$(
  find "$@" \( -type f -o -type l \) -name '*.so*' -printf '%h\n' 2>/dev/null |
    LC_ALL=C sort -u | paste -sd: -
)

while IFS= read -r -d '' candidate; do
  if readelf -h "$candidate" >/dev/null 2>&1; then
    LD_LIBRARY_PATH="$library_path" ldd "$candidate" 2>/dev/null || true
  fi
done < <(find "$@" -type f -print0) |
  awk '/=> \// {print $3} $1 ~ /^\// {print $1}' |
  LC_ALL=C sort -u > "$libraries"

while IFS= read -r library; do
  owner=$(dpkg-query -S "$library" 2>/dev/null |
    awk -F': ' '$1 !~ /^diversion / {print $1; exit}' || true)
  if [ -z "$owner" ]; then
    owner=$(dpkg-query -S "$(readlink -f "$library")" 2>/dev/null |
      awk -F': ' '$1 !~ /^diversion / {print $1; exit}' || true)
  fi
  [ -n "$owner" ] && printf '%s\n' "${owner%%:*}"
done < "$libraries" | LC_ALL=C sort -u > "$packages"

install -m 0644 "$packages" "$output"
