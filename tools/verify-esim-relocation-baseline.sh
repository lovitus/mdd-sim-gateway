#!/usr/bin/env bash
set -euo pipefail
root=$(git rev-parse --show-toplevel)
page=webui/src/mdd/views/Esim.jsx
evidence="$root/esim-relocation-evidence"
mkdir -p "$evidence"
cp "$root/$page" "$evidence/fixed-Esim.jsx"
trap 'cp "$evidence/fixed-Esim.jsx" "$root/$page"' EXIT
git show "38af2d406dd8925750c3bf4d946d8bec0350c422:$page" > "$root/$page"
set +e
(cd "$root/webui" && node tests/esimRelocation.mjs) > "$evidence/baseline.log" 2>&1
code=$?
set -e
if [ "$code" -eq 0 ] || ! grep -Fq 'historical reader entries must be separated from current attachments' "$evidence/baseline.log"; then
  cat "$evidence/baseline.log"
  echo 'Expected rendered eSIM relocation counterexample was not reproduced' >&2
  exit 1
fi
cp "$evidence/fixed-Esim.jsx" "$root/$page"
(cd "$root/webui" && node tests/esimRelocation.mjs) > "$evidence/fixed.log" 2>&1
cat "$evidence/fixed.log"
