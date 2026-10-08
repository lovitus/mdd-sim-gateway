#!/usr/bin/env bash
# One-time hosted proof; archive qualified source, then remove this entry.
set -euo pipefail
base=192e63bc96fd1bf77c1a6d1b1d26a62110930f6f
module=providers/vowifi-go
files=(internal/ims/inbound_media_call.go internal/ims/media_call.go internal/media/bridge.go upstream/vowifi-go/runtimehost/voicehost/sdp_rewrite.go)
backup=$(mktemp -d)
restore() {
  for file in "${files[@]}" internal/ims/inbound_dtmf.go internal/media/amrnb.go; do cp "$backup/$file" "$module/$file"; done
}
for file in "${files[@]}" internal/ims/inbound_dtmf.go internal/media/amrnb.go; do
  mkdir -p "$backup/$(dirname "$file")"
  cp "$module/$file" "$backup/$file"
done
trap 'restore; rm -rf "$backup"' EXIT
mkdir -p counterexample-evidence
git rev-parse HEAD HEAD^{tree} > counterexample-evidence/source.txt
printf '%s\n' "$base" >> counterexample-evidence/source.txt
git fetch --no-tags --depth=1 origin "$base"
for file in "${files[@]}"; do git show "$base:$module/$file" > "$module/$file"; done
rm "$module/internal/ims/inbound_dtmf.go"
set +e
go -C "$module" test -json -race -count=1 -timeout 3m -run '^TestIncoming(ReinviteHoldResumePreservesMedia|DTMFUsesNegotiatedSocketAndHoldBoundary)$' ./internal/ims > counterexample-evidence/red.jsonl 2> counterexample-evidence/red.stderr
red=$?
set -e
restore
node - "$red" <<'NODE'
const fs = require('node:fs');
const raw = fs.readFileSync('counterexample-evidence/red.jsonl', 'utf8');
const stderr = fs.readFileSync('counterexample-evidence/red.stderr', 'utf8');
if (process.argv[2] === '0' || /\[build failed\]|undefined:|syntax error|DATA RACE|panic:/.test(raw + stderr)) throw Error('Red must compile and fail behaviorally');
const events = raw.trim().split('\n').map(JSON.parse);
const hold = 'TestIncomingReinviteHoldResumePreservesMedia';
const expected = [hold, `${hold}/pcmu`, `${hold}/amr`, 'TestIncomingDTMFUsesNegotiatedSocketAndHoldBoundary'].sort();
const actual = events.filter(e => e.Action === 'fail' && e.Test).map(e => e.Test).sort();
if (JSON.stringify(actual) !== JSON.stringify(expected)) throw Error(`Unexpected baseline failures: ${JSON.stringify(actual)}`);
if (events.some(e => e.Action === 'skip')) throw Error('Skipped counterexample');
console.log(`Verified ${actual.length} exact compiled baseline failure events`);
NODE
# Existing CMR15 behavior is correct. Demonstrate that the new regression detects
# a deliberate policy violation; do not mislabel this mutation as a baseline bug.
node - <<'NODE'
const fs = require('node:fs');
const path = 'providers/vowifi-go/internal/media/amrnb.go';
const original = fs.readFileSync(path, 'utf8');
const marker = '\tif cmr >= 0 && cmr <= 7 && codec.config.ModeSet&(1<<cmr) != 0 {';
if (original.split(marker).length !== 2) throw Error('Mutation target must be unique');
fs.writeFileSync(path, original.replace(marker, '\tif cmr == 15 { cmr = 7; for codec.config.ModeSet&(1<<cmr) == 0 { cmr-- } }\n' + marker));
NODE
set +e
go -C "$module" test -json -race -count=1 -timeout 3m -run '^TestAMRNBRestrictedModeRemainsLowestWithNoRequest$' ./internal/media > counterexample-evidence/mutation.jsonl 2> counterexample-evidence/mutation.stderr
mutation=$?
set -e
restore
node - "$mutation" <<'NODE'
const fs = require('node:fs');
const raw = fs.readFileSync('counterexample-evidence/mutation.jsonl', 'utf8');
const stderr = fs.readFileSync('counterexample-evidence/mutation.stderr', 'utf8');
if (process.argv[2] === '0' || /\[build failed\]|undefined:|syntax error|DATA RACE|panic:/.test(raw + stderr)) throw Error('Mutation must fail behaviorally');
const events = raw.trim().split('\n').map(JSON.parse);
const name = 'TestAMRNBRestrictedModeRemainsLowestWithNoRequest';
const expected = [name, `${name}/zero_two_seven`, `${name}/two_seven`].sort();
const actual = events.filter(e => e.Action === 'fail' && e.Test).map(e => e.Test).sort();
if (JSON.stringify(actual) !== JSON.stringify(expected) || events.some(e => e.Action === 'skip')) throw Error('Unexpected mutation failures/skips');
if (!events.some(e => e.Action === 'pass' && e.Test === `${name}/seven`)) throw Error('Missing unchanged single-mode control');
console.log(`Verified ${actual.length} compiled policy-mutation failure events`);
NODE
go -C "$module" test -json -race -count=1 -timeout 3m ./internal/ims ./internal/media > counterexample-evidence/green.jsonl 2> counterexample-evidence/green.stderr
node - <<'NODE'
const fs = require('node:fs');
const events = fs.readFileSync('counterexample-evidence/green.jsonl', 'utf8').trim().split('\n').map(JSON.parse);
if (events.some(e => e.Action === 'fail' || e.Action === 'skip')) throw Error('Green includes failures/skips');
console.log(JSON.stringify({passed: events.filter(e => e.Action === 'pass' && e.Test).length}));
NODE
