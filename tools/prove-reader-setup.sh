#!/usr/bin/env bash
set -euo pipefail
kind=${1:?browser or core}
root=$(git rev-parse --show-toplevel)
cd "$root"
base=a56684c422db2006112941a8238ec38742fe33dd
git fetch --no-tags --depth=1 origin "$base"
mkdir -p proof-reader-setup
case "$kind" in
  browser) file=webui/src/mdd/views/SimConfig.jsx; marker=SETUP_JOURNEY ;;
  core) file=go-runtime/cmd/mdd-core/plan.go; marker=SETUP_BLOCKER ;;
  *) exit 2 ;;
esac
backup=$(mktemp)
cp "$file" "$backup"
trap 'cp "$backup" "$file"; rm -f "$backup"' EXIT
git show "$base:$file" > "$file"
set +e
if [[ "$kind" == browser ]]; then
  (cd webui && MDD_BROWSER_EVIDENCE=browser-evidence/reader-setup-red node tests/readerSetupBrowser.mjs) > proof-reader-setup/browser-red.log 2>&1
else
  go -C go-runtime test -race -count=1 ./cmd/mdd-core -run '^TestSetupApplyBlockersKeepActualReason$' > proof-reader-setup/core-red.log 2>&1
fi
status=$?
set -e
cat "proof-reader-setup/$kind-red.log"
test "$status" -ne 0
grep -Fq "$marker" "proof-reader-setup/$kind-red.log"
! grep -Eiq 'build failed|syntaxerror|panic:|data race|timed out' "proof-reader-setup/$kind-red.log"
cp "$backup" "$file"
echo "Confirmed compiled/loaded pre-fix counterexample: $marker; fixed source restored for permanent green gates."
