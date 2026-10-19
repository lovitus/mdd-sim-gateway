// Temporary hosted-only counterexamples; remove from the delivery tree.
import { spawnSync } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const baseline = '6015f450603af0c2b5b836a8d5dcbffb3c736936';
const paths = [
  'go-runtime/internal/agentconnection/manager.go',
  'go-runtime/internal/agentpolicy/manager.go',
  'go-runtime/internal/linuxmodem/data_health_linux.go',
];
const expected = [
  'TestSharedConnectionHealthyBorrowerConvergesWithoutMutation',
  'TestSharedConnectionForcedCloseExpiresAndCannotReborrowBeforeRecovery',
  'TestSharedConnectionReadyRequiresFreshIdentityProfileAndRevision',
];
const packages = ['./internal/agentpolicy', './internal/agentconnection', './internal/agentdata'];
if (process.platform === 'linux') {
  packages.push('./internal/linuxmodem');
  expected.push('TestControlHealthObservationRejectsReplacedOrCancelledClaim');
}
mkdirSync('proof-evidence', { recursive: true });
function git(args) {
  const result = spawnSync('git', args, { encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim();
}
function run(label, args) {
  const result = spawnSync('go', ['-C', 'go-runtime', 'test', '-json', '-race', '-count=1', '-timeout=2m', ...args],
    { encoding: 'utf8', maxBuffer: 32 * 1024 * 1024, timeout: 240000 });
  writeFileSync(`proof-evidence/${label}.jsonl`, result.stdout || '');
  writeFileSync(`proof-evidence/${label}.stderr`, result.stderr || '');
  assert.equal(result.error, undefined, `${label}: ${result.error}`);
  const events = result.stdout.split(/\r?\n/).filter(Boolean).map(line => JSON.parse(line));
  assert(!events.some(e => /\[build failed\]|DATA RACE|panic:/.test(e.Output || '')), `${label}: not a behavioral result`);
  assert(!events.some(e => e.Action === 'skip' && e.Test && expected.includes(e.Test)), `${label}: skipped regression`);
  return { status: result.status, events };
}
const head = git(['rev-parse', 'HEAD']);
writeFileSync('proof-evidence/source.json', JSON.stringify({ head, baseline, platform: process.platform, paths }, null, 2));
try {
  git(['restore', `--source=${baseline}`, '--', ...paths]);
  const red = run('red', ['-run', `^(${expected.join('|')})$`, ...packages]);
  assert.notEqual(red.status, 0, 'baseline unexpectedly passed');
  for (const name of expected) {
    assert(red.events.some(e => e.Action === 'fail' && e.Test === name), `missing compiled counterexample ${name}`);
  }
} finally {
  git(['restore', `--source=${head}`, '--', ...paths]);
}
const green = run('green', packages);
assert.equal(green.status, 0, 'restored package tests failed');
for (const name of expected) {
  assert(green.events.some(e => e.Action === 'pass' && e.Test === name), `missing fixed result ${name}`);
}
console.log(JSON.stringify({ head, baseline, expected, result: 'compiled-red/restored-race-green' }));
