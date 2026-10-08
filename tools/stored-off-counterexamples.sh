#!/usr/bin/env bash
# Temporary hosted proof; retain source and evidence, then remove this entry.
set -euo pipefail
base=8e4b9e531fbe97f9ec11a8800196ab6471622296
file=go-runtime/internal/agentpolicy/manager.go
backup=$(mktemp)
cp "$file" "$backup"
trap 'cp "$backup" "$file"; rm -f "$backup"' EXIT
mkdir -p counterexample-evidence
git rev-parse HEAD HEAD^{tree} > counterexample-evidence/source.txt
printf '%s\n' "$base" >> counterexample-evidence/source.txt
git fetch --no-tags --depth=1 origin "$base"
git show "$base:$file" > "$file"
set +e
go -C go-runtime test -json -race -count=1 -timeout 3m -run '^TestStoredDataPolicyRequiresGenerationAndObservedDisconnect$' ./internal/agentpolicy > counterexample-evidence/red.jsonl 2> counterexample-evidence/red.stderr
red=$?
set -e
cp "$backup" "$file"
node - "$red" <<'NODE'
const fs=require('node:fs');
const raw=fs.readFileSync('counterexample-evidence/red.jsonl','utf8');
const stderr=fs.readFileSync('counterexample-evidence/red.stderr','utf8');
if(process.argv[2]==='0'||/\[build failed\]|undefined:|syntax error|DATA RACE|panic:/.test(raw+stderr)) throw Error('Not a compiled behavioral red');
const events=raw.trim().split('\n').map(JSON.parse), name='TestStoredDataPolicyRequiresGenerationAndObservedDisconnect';
const expected=[name,...['connected_unknown','connected_disconnecting','connected_missing','flight_unknown','flight_disconnecting'].map(n=>`${name}/${n}`)].sort();
const actual=events.filter(e=>e.Action==='fail'&&e.Test).map(e=>e.Test).sort();
if(JSON.stringify(actual)!==JSON.stringify(expected)||events.some(e=>e.Action==='skip')) throw Error(`Unexpected red events ${JSON.stringify(actual)}`);
console.log('Verified five compiled transition failures and parent event');
NODE
go -C go-runtime test -json -race -count=1 -timeout 3m ./internal/agentpolicy > counterexample-evidence/green.jsonl 2> counterexample-evidence/green.stderr
node - <<'NODE'
const fs=require('node:fs'),events=fs.readFileSync('counterexample-evidence/green.jsonl','utf8').trim().split('\n').map(JSON.parse);
if(events.some(e=>e.Action==='fail'||e.Action==='skip')) throw Error('Green has failure/skip');
console.log(JSON.stringify({passed:events.filter(e=>e.Action==='pass'&&e.Test).length}));
NODE
