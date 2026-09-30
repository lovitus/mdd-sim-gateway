import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { spawnSync } from 'node:child_process'

assert.equal(process.env.GITHUB_ACTIONS, 'true', 'temporary hosted-only counterexample')
const root = process.cwd()
const evidence = path.join(process.env.RUNNER_TEMP, 'at-call-capability-evidence')
fs.mkdirSync(evidence)
const source = path.join(root, 'go-runtime/internal/agentat/manager.go')
const saved = fs.readFileSync(source)
const expected = [
  'TestManagerRecoversCallCapabilityWithoutReplacingOwner',
  'TestManagerCallCapabilityRetryIsBoundedAndKeepsOwner',
  'TestCancelledCallCapabilityProbeCannotPublishSuccess',
].sort()
function run(label, names, packages) {
  const result = spawnSync('go', ['test', '-json', '-race', '-count=1', '-timeout=2m', '-run', `^(${names.join('|')})$`, ...packages],
    { cwd: path.join(root, 'go-runtime'), encoding: 'utf8', timeout: 180000, maxBuffer: 16 * 1024 * 1024 })
  fs.writeFileSync(path.join(evidence, label + '.jsonl'), result.stdout || '')
  fs.writeFileSync(path.join(evidence, label + '.stderr'), result.stderr || '')
  assert.equal(result.error, undefined)
  const events = result.stdout.trim().split('\n').filter(Boolean).map(line => JSON.parse(line))
  assert.ok(!events.some(e => e.Action === 'skip'), 'no skipped counterexample')
  const output = events.map(e => e.Output || '').join('') + result.stderr
  assert.ok(!/build failed|undefined:|syntax error|WARNING: DATA RACE|panic:/.test(output), 'must be behavioral failure, not compile/race/panic')
  return { status: result.status, failed: events.filter(e => e.Action === 'fail' && e.Test).map(e => e.Test).sort(),
    passed: events.filter(e => e.Action === 'pass' && e.Test).map(e => e.Test).sort() }
}
let old
try {
  const patch = spawnSync('git', ['apply', 'tools/at-call-capability-counterexample.patch'], { encoding: 'utf8' })
  assert.equal(patch.status, 0, patch.stderr)
  old = run('refresh-withdrawn', expected, ['./internal/agentat'])
  assert.equal(old.status, 1)
  assert.deepEqual(old.failed, expected)
} finally {
  fs.writeFileSync(source, saved)
}
const all = [...expected, 'TestMMCommandPortRechecksExactSIMWithoutOpeningTTY', 'TestDataOwnerOffersOnlyModemManagerCommandCandidate']
const fixed = run('fixed', all, ['./internal/agentat', './internal/linuxmodem'])
assert.equal(fixed.status, 0)
assert.deepEqual(fixed.failed, [])
assert.deepEqual(fixed.passed, [...all].sort())
const summary = { baseline: 'new refresh branch withdrawn; original persistent negative-cache behavior, with fixtures/clock retained', old, fixed }
fs.writeFileSync(path.join(evidence, 'summary.json'), JSON.stringify(summary, null, 2))
console.log(JSON.stringify(summary))
