#!/usr/bin/env bash
set -euo pipefail
baseline="$1"
evidence="$RUNNER_TEMP/imei-card-proof"
mkdir -p "$evidence/original"
files=(go-runtime/internal/linecatalog/store.go go-runtime/internal/linecatalog/operation.go go-runtime/internal/core/provision.go)
restore() {
  for file in "${files[@]}"; do cp "$evidence/original/${file//\//_}" "$file"; done
}
for file in "${files[@]}"; do cp "$file" "$evidence/original/${file//\//_}"; done
trap restore EXIT
for file in "${files[@]}"; do cp "$baseline/$file" "$file"; done
set +e
go -C go-runtime test -json -count=1 -timeout 2m -run '^TestProvisionRetainsClaimedIMEIBinding$' ./internal/core > "$evidence/baseline.jsonl" 2> "$evidence/baseline.stderr"
old_go=$?
set -e
restore
test "$old_go" -ne 0
node - "$evidence" <<'NODE'
const fs=require('node:fs'), assert=require('node:assert/strict'), root=process.argv[2];
const events=fs.readFileSync(root+'/baseline.jsonl','utf8').trim().split('\n').map(JSON.parse);
const failed=events.filter(e=>e.Action==='fail'&&e.Test?.includes('/'));
assert.equal(failed.length,2);
for(const failure of failed) assert(events.some(e=>e.Test===failure.Test&&e.Output?.includes('binding conflict dispatched hardware: status=200 calls=1')));
assert.equal(events.filter(e=>e.Action==='pass'&&e.Test?.includes('/')).length,1);
console.log('Compiled baseline: conflicting command and concurrent rebind dispatched hardware; matching control passed.');
NODE
go -C go-runtime test -race -json -count=1 -timeout 2m -run '^TestProvisionRetainsClaimedIMEIBinding$' ./internal/core > "$evidence/fixed.jsonl" 2> "$evidence/fixed.stderr"
node - "$evidence" <<'NODE'
const fs=require('node:fs'), assert=require('node:assert/strict'), root=process.argv[2];
const events=fs.readFileSync(root+'/fixed.jsonl','utf8').trim().split('\n').map(JSON.parse);
assert.equal(events.filter(e=>e.Action==='fail'||e.Action==='skip').length,0);
assert.equal(events.filter(e=>e.Action==='pass'&&e.Test?.includes('/')).length,3);
console.log('Fixed: three continuous claim/provision scenarios pass under race; conflicts execute no hardware command.');
NODE
