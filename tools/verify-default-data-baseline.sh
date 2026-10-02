#!/usr/bin/env bash
set -euo pipefail

# Temporary hosted evidence entry; remove after retaining the qualified receipt.
baseline=${1:?expected pinned baseline checkout}
root=$(git rev-parse --show-toplevel)
expected=f847b033e3e3e5dd97bd22fec7fd2b0df19af02b
[[ $(git -C "$baseline" rev-parse HEAD) == "$expected" ]]
cp "$root/go-runtime/internal/agentpolicy/default_data_test.go" \
  "$baseline/go-runtime/internal/agentpolicy/default_data_test.go"

set +e
go -C "$baseline/go-runtime" test -json -count=1 -timeout 2m \
  -run '^TestDefaultDataReconcile' ./internal/agentpolicy \
  > "$root/default-data-red.jsonl" 2> "$root/default-data-red.stderr"
red_status=$?
set -e
[[ $red_status == 1 ]]
node --input-type=module - "$root/default-data-red.jsonl" <<'JS'
import fs from 'node:fs';
import assert from 'node:assert/strict';
const events = fs.readFileSync(process.argv[2], 'utf8').trim().split('\n').map(JSON.parse);
const expected = ['TestDefaultDataReconcileDoesNotOverridePolicySavedWhileWaiting', 'TestDefaultDataReconcileRequiresObservedDisconnect'];
const failed = events.filter(e => e.Action === 'fail' && e.Test && !e.Test.includes('/')).map(e => e.Test).sort();
assert.deepEqual(failed, expected, 'need compiled behavioral failures in both regressions');
assert.equal(events.some(e => e.Action === 'skip'), false);
console.log('Pinned pre-fix implementation failed both behavioral regressions.');
JS

go -C "$root/go-runtime" test -json -race -count=1 -timeout 2m \
  -run '^TestDefaultDataReconcile' ./internal/agentpolicy \
  > "$root/default-data-green.jsonl" 2> "$root/default-data-green.stderr"
node --input-type=module - "$root/default-data-green.jsonl" <<'JS'
import fs from 'node:fs';
import assert from 'node:assert/strict';
const events = fs.readFileSync(process.argv[2], 'utf8').trim().split('\n').map(JSON.parse);
assert.equal(events.some(e => e.Action === 'fail' || e.Action === 'skip'), false);
assert.equal(events.filter(e => e.Action === 'pass' && e.Test && !e.Test.includes('/')).length, 2);
console.log('Fixed implementation passed both regressions under race detection.');
JS
