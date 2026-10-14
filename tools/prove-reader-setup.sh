#!/usr/bin/env bash
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
base=185c73655c15ffaa7889c8347318f9cbb7f048e8
file=webui/src/mdd/views/UnifiedPages.jsx
git fetch --no-tags --depth=1 origin "$base"
mkdir -p proof-reader-setup
backup=$(mktemp)
cp "$file" "$backup"
trap 'cp "$backup" "$file"; rm -f "$backup"' EXIT
git show "$base:$file" > "$file"
set +e
(cd webui && MDD_BROWSER_EVIDENCE=browser-evidence/reader-setup-publication-red node tests/readerSetupBrowser.mjs) > proof-reader-setup/publication-red.log 2>&1
status=$?
set -e
cat proof-reader-setup/publication-red.log
test "$status" -ne 0
grep -Fq SETUP_PUBLICATION proof-reader-setup/publication-red.log
! grep -Eiq 'build failed|syntaxerror|panic:|data race|timed out' proof-reader-setup/publication-red.log
cp "$backup" "$file"
echo 'Confirmed loaded pre-fix publication counterexample; fixed source restored for permanent green gates.'
