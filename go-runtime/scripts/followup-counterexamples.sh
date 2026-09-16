#!/usr/bin/env bash
# Negative controls run only against a disposable, pinned CI checkout.
set -euo pipefail
baseline=${1:?baseline checkout is required}
root=$(cd "$(dirname "$0")/../.." && pwd)
expected=dde262a5c3724323a20c2d5b7d5cf5bb09ddff84
if [[ $(git -C "$baseline" rev-parse HEAD) != "$expected" ]]; then
  echo 'Follow-up counterexamples require the exact reviewed pre-fix commit.' >&2
  exit 1
fi
for package in agentdata agentmedia; do
  cp "$root/go-runtime/internal/$package/followup_lifetime_test.go" "$baseline/go-runtime/internal/$package/"
done
set +e
GOWORK=off go -C "$baseline/go-runtime" test -json -race -count=1 -timeout 2m \
  -run '^TestFollowup' ./internal/agentdata ./internal/agentmedia > followup-baseline-tests.jsonl 2>&1
status=$?
set -e
if [[ $status != 1 ]]; then
  cat followup-baseline-tests.jsonl
  echo 'Expected ordinary counterexample failures, not success or infrastructure failure.' >&2
  exit 1
fi
node <<'JS'
const fs = require('node:fs');
const expected = new Map([
  ['TestFollowupMediaAcquireRejectsReplacement', 'stale media acquisition claimed the replacement'],
  ['TestFollowupDataAckFailurePreservesReplacement', 'old acknowledgement failure revoked the replacement reservation'],
  ...['revoke', 'disconnect', 'expiry'].map(action => [
    `TestFollowupMediaRevocationWakesAcquire/${action}`, 'revoked media acquisition stayed blocked',
  ]),
  ...['revoke', 'session', 'disconnect', 'expiry'].map(action => [
    `TestFollowupDataRevocationWakesAcquire/${action}`, 'revoked data acquisition stayed blocked',
  ]),
]);
const roots = new Set([...expected.keys()].map(test => test.split('/')[0]));
const failed = new Set();
const output = new Map();
for (const line of fs.readFileSync('followup-baseline-tests.jsonl', 'utf8').split('\n').filter(Boolean)) {
  if (line.startsWith('go: downloading ')) continue;
  let event;
  try { event = JSON.parse(line); } catch { console.error(line); process.exit(1); }
  if (/DATA RACE|build failed|panic:|fatal error:/.test(event.Output || '')) {
    console.error('Invalid negative control:', event.Output); process.exit(1);
  }
  if (event.Test && event.Output) output.set(event.Test, (output.get(event.Test) || '') + event.Output);
  if (event.Action === 'fail' && event.Test) {
    if (!roots.has(event.Test.split('/')[0])) { console.error('Unexpected failure:', event.Test); process.exit(1); }
    failed.add(event.Test);
  }
}
for (const [test, diagnostic] of expected) {
  if (!failed.has(test) || !(output.get(test) || '').includes(diagnostic)) {
    console.error('Missing exact counterexample:', test, diagnostic); process.exit(1);
  }
  console.log('CONFIRMED against dde262a5:', test);
}
JS
