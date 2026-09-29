#!/usr/bin/env bash
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
baseline=e51d97d3441d199ca82cbc80e808c4403381d3b1
evidence="${RUNNER_TEMP:?}/agent-control-compat"
mkdir -p "$evidence"
files=(api.go client.go)
for file in "${files[@]}"; do
  cp "go-runtime/internal/agentcontrol/$file" "$evidence/$file.fixed"
done
restore() {
  for file in "${files[@]}"; do
    cp "$evidence/$file.fixed" "go-runtime/internal/agentcontrol/$file"
  done
}
trap restore EXIT
git fetch --no-tags --depth=1 origin "$baseline"
for file in "${files[@]}"; do
  git show "$baseline:go-runtime/internal/agentcontrol/$file" > "go-runtime/internal/agentcontrol/$file"
done
set +e
go -C go-runtime test -race -count=1 -timeout 1m -json \
  -run '^TestAgentControlConnectionCompatibility$' ./internal/agentcontrol > "$evidence/baseline.jsonl" 2> "$evidence/baseline.stderr"
result=$?
set -e
restore
if [[ "$result" -ne 1 ]]; then
  printf 'Expected behavioral baseline failure; exit=%s\n' "$result" >&2
  exit 1
fi
EVIDENCE="$evidence" node <<'NODE'
const fs = require('fs'), assert = require('assert/strict');
const events = fs.readFileSync(process.env.EVIDENCE + '/baseline.jsonl', 'utf8').trim().split('\n').map(JSON.parse);
const root = 'TestAgentControlConnectionCompatibility';
const expected = ['stopped', 'running', 'stop', 'timeout'].map(n => root + '/legacy/' + n);
const failed = events.filter(e => e.Action === 'fail' && e.Test).map(e => e.Test);
assert.deepEqual(failed.filter(n => n !== root).sort(), [...expected].sort());
for (const name of expected) {
  const output = events.filter(e => e.Test === name).map(e => e.Output || '').join('');
  assert.ok(output.includes('legacy decoder rejected response: decode Agent control response: json: unknown field "core_connection"'), output);
}
assert.ok(failed.includes(root), 'Missing executed regression');
console.log('Baseline compiled: four exact legacy-wire unknown-field failures.');
NODE
go -C go-runtime test -race -count=1 -timeout 1m -json \
  -run '^TestAgentControlConnectionCompatibility$' ./internal/agentcontrol > "$evidence/fixed.jsonl" 2> "$evidence/fixed.stderr"
EVIDENCE="$evidence" node <<'NODE'
const fs = require('fs'), assert = require('assert/strict');
const events = fs.readFileSync(process.env.EVIDENCE + '/fixed.jsonl', 'utf8').trim().split('\n').map(JSON.parse);
const root = 'TestAgentControlConnectionCompatibility';
const passed = events.filter(e => e.Action === 'pass' && e.Test && e.Test !== root).map(e => e.Test);
assert.equal(passed.length, 8);
assert.equal(new Set(passed).size, 8);
assert.ok(events.some(e => e.Action === 'pass' && e.Test === root));
assert.ok(!events.some(e => e.Action === 'fail' || e.Action === 'skip'));
fs.writeFileSync(process.env.EVIDENCE + '/summary.json', JSON.stringify({baseline: 'e51d97d3441d199ca82cbc80e808c4403381d3b1', candidate: process.env.GITHUB_SHA, baselineUnknownFieldFailures: 4, fixedSubtests: passed, race: true}, null, 2));
console.log('Fixed compatibility matrix: eight subtests pass under race detection.');
NODE
git diff --exit-code -- go-runtime/internal/agentcontrol/api.go go-runtime/internal/agentcontrol/client.go
