#!/usr/bin/env bash
# Temporary hosted proof. Remove this entry before final delivery; retain its source by exact SHA.
set -euo pipefail
base=154947beffdaa7c89e80fb31833edc6d2e3f3899
scope=${1:?provider, linux or windows required}
case "$scope" in
  provider)
    module=providers/vowifi-go
    packages=(./internal/ims ./internal/media)
    pattern='^TestIncoming(NegotiatedPayloadCarriesBothDirections|NegotiationRejectsUnsupportedFormats)$'
    files=(providers/vowifi-go/internal/ims/inbound_media_call.go providers/vowifi-go/internal/ims/media_call.go providers/vowifi-go/internal/media/bridge.go)
    ;;
  linux|windows)
    module=go-runtime
    packages=(./internal/agentpolicy)
    pattern='^Test(StoredDataPolicyRequiresGenerationAndObservedDisconnect|DataClaimRetainsATSnapshotAndCoreAPDUBlocker)$'
    files=(go-runtime/internal/agentpolicy/manager.go)
    if [[ "$scope" == linux ]]; then
      packages+=(./internal/linuxmodem)
      files+=(go-runtime/internal/linuxmodem/data_linux.go)
    fi
    ;;
  *) exit 2 ;;
esac
backup=$(mktemp -d)
restore() {
  for file in "${files[@]}"; do cp "$backup/$file" "$file"; done
  rm -rf "$backup"
}
for file in "${files[@]}"; do
  mkdir -p "$backup/$(dirname "$file")"
  cp "$file" "$backup/$file"
done
trap restore EXIT
mkdir -p counterexample-evidence
git rev-parse HEAD HEAD^{tree} > counterexample-evidence/source.txt
printf '%s\n' "$base" >> counterexample-evidence/source.txt
for file in "${files[@]}"; do git show "$base:$file" > "$file"; done
set +e
go -C "$module" test -json -race -count=1 -timeout 3m -run "$pattern" "${packages[@]}" > counterexample-evidence/red.jsonl 2> counterexample-evidence/red.stderr
red=$?
set -e
for file in "${files[@]}"; do cp "$backup/$file" "$file"; done
node - "$scope" "$red" <<'NODE'
const fs = require('node:fs');
const [scope, status] = process.argv.slice(2);
const raw = fs.readFileSync('counterexample-evidence/red.jsonl', 'utf8');
const stderr = fs.readFileSync('counterexample-evidence/red.stderr', 'utf8');
if (status === '0' || /\[build failed\]|undefined:|syntax error|DATA RACE|panic:/.test(raw + stderr)) throw Error('Red was not a compiled behavioral failure');
const events = raw.trim().split('\n').map(line => JSON.parse(line));
const failures = events.filter(e => e.Action === 'fail' && e.Test).map(e => e.Test).sort();
const provider = 'TestIncomingNegotiatedPayloadCarriesBothDirections';
const policy = 'TestStoredDataPolicyRequiresGenerationAndObservedDisconnect';
const expected = scope === 'provider'
  ? [provider, ...['amr_dynamic', 'amr_over_pcmu', 'amr_wb_and_nb_mux', 'mux_explicit_rtcp'].map(n => `${provider}/${n}`)]
  : [policy, ...['connected', 'connecting', 'flight_mode', 'unowned_connected', 'unowned_disconnected'].map(n => `${policy}/${n}`)];
if (scope === 'linux') expected.push('TestDataClaimRetainsATSnapshotAndCoreAPDUBlocker');
if (JSON.stringify(failures) !== JSON.stringify(expected.sort())) throw Error(`Unexpected failure set: ${JSON.stringify(failures)}`);
if (events.some(e => e.Action === 'skip')) throw Error('Unexpected skipped counterexample');
console.log(`Verified ${failures.length} exact compiled failure events (${scope})`);
NODE
go -C "$module" test -json -race -count=1 -timeout 3m "${packages[@]}" > counterexample-evidence/green.jsonl 2> counterexample-evidence/green.stderr
node - <<'NODE'
const fs = require('node:fs');
const events = fs.readFileSync('counterexample-evidence/green.jsonl', 'utf8').trim().split('\n').map(line => JSON.parse(line));
if (events.some(e => e.Action === 'fail')) throw Error('Green has failures');
console.log(JSON.stringify({passed: events.filter(e => e.Action === 'pass' && e.Test).length, skipped: events.filter(e => e.Action === 'skip').map(e => e.Test)}));
NODE
