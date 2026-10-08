#!/usr/bin/env bash
# Temporary hosted-only behavioral proof; removed after recording qualification.
set -euo pipefail
base=86e34f7e777d9371db63ecef52dd86d7448df9f5
module=providers/vowifi-go
file=$module/internal/browsermedia/handler.go
backup=$(mktemp)
cp "$file" "$backup"
trap 'cp "$backup" "$file"; rm -f "$backup"' EXIT
mkdir -p counterexample-evidence
git rev-parse HEAD HEAD^{tree} > counterexample-evidence/source.txt
printf '%s\n' "$base" >> counterexample-evidence/source.txt
git fetch --no-tags --depth=1 origin "$base"
git show "$base:$file" > "$file"
set +e
go -C "$module" test -json -race -count=1 -timeout 2m -run '^(TestIncomingHoldKeepsWebSocketAliveWithoutForgingClientHeartbeat|TestReadySessionCarriesLivePCMAndResumes)$' ./internal/service ./internal/browsermedia > counterexample-evidence/red.jsonl 2> counterexample-evidence/red.stderr
red=$?
set -e
cp "$backup" "$file"
node - "$red" <<'NODE'
const fs = require('node:fs');
const raw = fs.readFileSync('counterexample-evidence/red.jsonl', 'utf8');
const stderr = fs.readFileSync('counterexample-evidence/red.stderr', 'utf8');
if (process.argv[2] === '0' || /\[build failed\]|undefined:|syntax error|DATA RACE|panic:/.test(raw + stderr)) throw Error('Red must compile and fail behaviorally');
const events = raw.trim().split('\n').map(JSON.parse);
const expected = ['TestIncomingHoldKeepsWebSocketAliveWithoutForgingClientHeartbeat', 'TestReadySessionCarriesLivePCMAndResumes'];
const failed = events.filter(e => e.Action === 'fail' && e.Test).map(e => e.Test).sort();
if (JSON.stringify(failed) !== JSON.stringify(expected) || events.some(e => e.Action === 'skip')) throw Error(`Unexpected baseline result: ${JSON.stringify(failed)}`);
console.log('Two compiled missing-downlink counterexamples confirmed');
NODE
go -C "$module" test -json -race -count=1 -timeout 3m -run '^(TestIncomingHoldKeepsWebSocketAliveWithoutForgingClientHeartbeat|TestReadySessionCarriesLivePCMAndResumes|TestBrowserCanaryThroughCoreProxy|TestBrowserCanaryRejectsSilentFrames)$' ./internal/browsermedia ./internal/service > counterexample-evidence/green.jsonl 2> counterexample-evidence/green.stderr
node - <<'NODE'
const fs = require('node:fs');
const events = fs.readFileSync('counterexample-evidence/green.jsonl', 'utf8').trim().split('\n').map(JSON.parse);
if (events.some(e => e.Action === 'fail' || e.Action === 'skip') || events.filter(e => e.Action === 'pass' && e.Test).length !== 4) throw Error('Green must execute all four focused cases without failure/skip');
console.log(JSON.stringify({passed: events.filter(e => e.Action === 'pass' && e.Test).length}));
NODE
