#!/usr/bin/env bash
# One-time no-op refresh proof. Initial hold/CMR15 proof is already archived.
set -euo pipefail
base=7f605d0b1de567adaedc1c1202045d42de3da19a
module=providers/vowifi-go
file=$module/internal/ims/inbound_media_call.go
backup=$(mktemp)
cp "$file" "$backup"
trap 'cp "$backup" "$file"; rm -f "$backup"' EXIT
mkdir -p counterexample-evidence
git rev-parse HEAD HEAD^{tree} > counterexample-evidence/source.txt
printf '%s\n' "$base" >> counterexample-evidence/source.txt
git fetch --no-tags --depth=1 origin "$base"
git show "$base:$file" > "$file"
set +e
go -C "$module" test -json -race -count=1 -timeout 3m -run '^TestIncomingDTMFUsesNegotiatedSocketAndHoldBoundary$' ./internal/ims > counterexample-evidence/red.jsonl 2> counterexample-evidence/red.stderr
red=$?
set -e
cp "$backup" "$file"
node - "$red" <<'NODE'
const fs = require('node:fs');
const raw = fs.readFileSync('counterexample-evidence/red.jsonl', 'utf8');
const stderr = fs.readFileSync('counterexample-evidence/red.stderr', 'utf8');
if (process.argv[2] === '0' || /\[build failed\]|undefined:|syntax error|DATA RACE|panic:/.test(raw + stderr)) throw Error('Red must compile and fail behaviorally');
const events = raw.trim().split('\n').map(JSON.parse);
const expected = ['TestIncomingDTMFUsesNegotiatedSocketAndHoldBoundary'];
const actual = events.filter(e => e.Action === 'fail' && e.Test).map(e => e.Test).sort();
if (JSON.stringify(actual) !== JSON.stringify(expected)) throw Error(`Unexpected baseline failures: ${JSON.stringify(actual)}`);
if (events.some(e => e.Action === 'skip')) throw Error('Skipped counterexample');
console.log(`Verified ${actual.length} exact compiled refresh failure`);
NODE
go -C "$module" test -json -race -count=1 -timeout 3m ./internal/ims ./internal/media > counterexample-evidence/green.jsonl 2> counterexample-evidence/green.stderr
node - <<'NODE'
const fs = require('node:fs');
const events = fs.readFileSync('counterexample-evidence/green.jsonl', 'utf8').trim().split('\n').map(JSON.parse);
if (events.some(e => e.Action === 'fail' || e.Action === 'skip')) throw Error('Green includes failures/skips');
console.log(JSON.stringify({passed: events.filter(e => e.Action === 'pass' && e.Test).length}));
NODE
