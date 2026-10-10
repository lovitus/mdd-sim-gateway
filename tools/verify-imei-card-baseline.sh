#!/usr/bin/env bash
set -euo pipefail
baseline="$1"
evidence="$RUNNER_TEMP/imei-card-proof"
mkdir -p "$evidence/original"
files=(go-runtime/internal/linecatalog/imei_pool.go go-runtime/internal/linecatalog/imei_pool_http.go go-runtime/internal/linecatalog/store.go webui/src/api.js)
restore() {
  for file in "${files[@]}"; do cp "$evidence/original/${file//\//_}" "$file"; done
}
for file in "${files[@]}"; do cp "$file" "$evidence/original/${file//\//_}"; done
trap restore EXIT
for file in "${files[@]}"; do cp "$baseline/$file" "$file"; done
set +e
go -C go-runtime test -json -count=1 -timeout 2m -run '^TestIMEICardBinding(Lifecycle|Conflicts)$' ./internal/linecatalog > "$evidence/baseline.jsonl" 2> "$evidence/baseline.stderr"
old_go=$?
node webui/tests/mddHardwareAdapter.mjs > "$evidence/baseline-ui.log" 2>&1
old_ui=$?
set -e
restore
test "$old_go" -ne 0
test "$old_ui" -ne 0
node - "$evidence" <<'NODE'
const fs=require('node:fs'), assert=require('node:assert/strict'), root=process.argv[2];
const events=fs.readFileSync(root+'/baseline.jsonl','utf8').trim().split('\n').map(JSON.parse);
assert.equal(fs.readFileSync(root+'/baseline.stderr','utf8'),'');
const failed=events.filter(e=>e.Action==='fail'&&e.Test?.includes('/'));
assert.equal(failed.length,14);
assert(events.some(e=>e.Output?.includes('card binding HTTP 400, want 200')));
assert.match(fs.readFileSync(root+'/baseline-ui.log','utf8'),/no configured line owns this ICCID/);
console.log('Compiled baseline: 14 rejected card-binding subcases; browser adapter reproduces owner error.');
NODE
go -C go-runtime test -race -json -count=1 -timeout 2m -run '^TestIMEICardBinding(Lifecycle|Conflicts)$' ./internal/linecatalog > "$evidence/fixed.jsonl" 2> "$evidence/fixed.stderr"
node webui/tests/mddHardwareAdapter.mjs > "$evidence/fixed-ui.log" 2>&1
node - "$evidence" <<'NODE'
const fs=require('node:fs'), assert=require('node:assert/strict'), root=process.argv[2];
const events=fs.readFileSync(root+'/fixed.jsonl','utf8').trim().split('\n').map(JSON.parse);
assert.equal(fs.readFileSync(root+'/fixed.stderr','utf8'),'');
assert.equal(events.filter(e=>e.Action==='fail'||e.Action==='skip').length,0);
assert.equal(events.filter(e=>e.Action==='pass'&&e.Test?.includes('/')).length,14);
console.log('Fixed: 14 behavior subcases pass under race; browser adapter saves and unbinds new-card identity.');
NODE
