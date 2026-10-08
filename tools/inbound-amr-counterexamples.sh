#!/usr/bin/env bash
# One-time hosted verification entry; removed after retaining exact source/evidence.
set -euo pipefail
base=14524a09e7e84728b2e30bdcfae32270c9bcb16a
module=providers/vowifi-go
files=(internal/ims/inbound_media_call.go internal/media/amrnb.go internal/media/codec.go internal/media/bridge.go)
backup=$(mktemp -d)
restore() {
  for file in "${files[@]}" internal/ims/inbound_amr.go; do cp "$backup/$file" "$module/$file"; done
  rm -rf "$backup"
}
for file in "${files[@]}" internal/ims/inbound_amr.go; do
  mkdir -p "$backup/$(dirname "$file")"
  cp "$module/$file" "$backup/$file"
done
trap restore EXIT
mkdir -p counterexample-evidence
git rev-parse HEAD HEAD^{tree} > counterexample-evidence/source.txt
printf '%s\n' "$base" >> counterexample-evidence/source.txt
git fetch --no-tags --depth=1 origin "$base"
for file in "${files[@]}"; do git show "$base:$module/$file" > "$module/$file"; done
rm "$module/internal/ims/inbound_amr.go"
set +e
go -C "$module" test -json -race -count=1 -timeout 3m -run '^TestIncoming(NegotiatedPayloadCarriesBothDirections|NegotiationRejectsUnsupportedFormats)$' ./internal/ims > counterexample-evidence/red.jsonl 2> counterexample-evidence/red.stderr
red=$?
set -e
for file in "${files[@]}" internal/ims/inbound_amr.go; do cp "$backup/$file" "$module/$file"; done
node - "$red" <<'NODE'
const fs = require('node:fs');
const raw = fs.readFileSync('counterexample-evidence/red.jsonl', 'utf8');
const stderr = fs.readFileSync('counterexample-evidence/red.stderr', 'utf8');
if (process.argv[2] === '0' || /\[build failed\]|undefined:|syntax error|DATA RACE|panic:/.test(raw + stderr)) throw Error('Red must compile and fail behaviorally');
const events = raw.trim().split('\n').map(JSON.parse);
const tx = 'TestIncomingNegotiatedPayloadCarriesBothDirections';
const reject = 'TestIncomingNegotiationRejectsUnsupportedFormats';
const expected = [tx, reject,
  ...['amr_dynamic','amr_over_pcmu','amr_wb_and_nb_mux','mux_explicit_rtcp','interleaving_falls_back_pcmu','interleaving_falls_back_amr','mode_set_zero','mode_period_neighbors'].map(n => `${tx}/${n}`),
  ...['interleaving_zero','invalid_mode_set','invalid_mode_period'].map(n => `${reject}/${n}`)].sort();
const actual = events.filter(e => e.Action === 'fail' && e.Test).map(e => e.Test).sort();
if (JSON.stringify(actual) !== JSON.stringify(expected)) throw Error(`Unexpected failure set: ${JSON.stringify(actual)}`);
if (events.some(e => e.Action === 'skip')) throw Error('Skipped counterexample');
console.log(`Verified ${actual.length} exact compiled failure events`);
NODE
go -C "$module" test -json -race -count=1 -timeout 3m ./internal/ims ./internal/media > counterexample-evidence/green.jsonl 2> counterexample-evidence/green.stderr
node - <<'NODE'
const fs = require('node:fs');
const events = fs.readFileSync('counterexample-evidence/green.jsonl', 'utf8').trim().split('\n').map(JSON.parse);
if (events.some(e => e.Action === 'fail' || e.Action === 'skip')) throw Error('Green includes failures/skips');
console.log(JSON.stringify({passed: events.filter(e => e.Action === 'pass' && e.Test).length}));
NODE
