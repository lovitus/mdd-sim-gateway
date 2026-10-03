// Temporary hosted-only proof; removed after its evidence is retained.
import assert from 'node:assert/strict'
import fs from 'node:fs'
import { spawnSync } from 'node:child_process'

assert.equal(process.env.GITHUB_ACTIONS, 'true')
assert.equal(process.platform, 'linux')
const dir = 'go-runtime/internal/linuxmodem'
const baseline = 'baseline/' + dir
// 446b910 already has the platform interface. Copy only tests, leaving every
// baseline production byte unchanged to expose the review counterexamples.
fs.copyFileSync(dir + '/acquisition_linux_test.go', baseline + '/acquisition_linux_test.go')

fs.mkdirSync('linux-acquisition-evidence')
const cases = new Map([
  ...['false', 'true'].map(present => [`TestLinuxAcquisitionDuplicateCannotDisconnectOrInhibit/retained_present=${present}`, 'duplicate preserved retained owner qualification']),
  ...['cancelled', 'deadline'].flatMap(state => ['false', 'true'].map(connected => [`TestLinuxAcquisitionCallStateFencesBothMutations/${state}/connected=${connected}`, 'unproven idle state permitted'])),
  ['TestLinuxAcquisitionCallStateFencesBothMutations/cancel_between_bearers', 'cancelled cleanup continued to another bearer'],
  ...['PIN', 'PUK', 'disabled'].map(state => [`TestLinuxAcquisitionKeepsInactivePINRecovery/${state}`, 'inactive Voice-less modem lost PIN/AT recovery']),
])
function run(label, cwd, args) {
  const result = spawnSync('go', ['test', '-json', '-race', '-count=1', '-timeout=3m', ...args, './internal/linuxmodem'],
    { cwd, encoding: 'utf8', timeout: 300000, maxBuffer: 16 << 20 })
  fs.writeFileSync(`linux-acquisition-evidence/${label}.jsonl`, result.stdout ?? '')
  fs.writeFileSync(`linux-acquisition-evidence/${label}.stderr`, result.stderr ?? '')
  assert.ifError(result.error)
  const records = (result.stdout ?? '').trim().split('\n').filter(Boolean).map(line => JSON.parse(line))
  assert(!records.some(row => row.Action === 'skip' || row.Action === 'build-fail'), `${label}: skipped/build failure`)
  assert(!/WARNING: DATA RACE|panic:|fatal error:|\[build failed\]/.test((result.stdout ?? '') + (result.stderr ?? '')), `${label}: race/panic/build failure`)
  return { status: result.status, records }
}
const red = run('red', 'baseline/go-runtime', ['-run', '^TestLinuxAcquisition'])
assert.notEqual(red.status, 0)
const expectedFailures = new Set()
for (const [name, message] of cases) {
  assert(red.records.some(row => row.Test === name && row.Action === 'fail'), `${name}: no compiled failure`)
  assert(red.records.some(row => row.Test === name && row.Output?.includes(message)), `${name}: wrong failure`)
  // These cases use one t.Run; slashes in its name do not create parent runs.
  expectedFailures.add(name)
  expectedFailures.add(name.split('/')[0])
}
assert.deepEqual(new Set(red.records.filter(row => row.Test && row.Action === 'fail').map(row => row.Test)), expectedFailures)
for (const name of ['TestLinuxAcquisitionCallStateFencesBothMutations/data_claim', 'TestLinuxAcquisitionCallStateFencesBothMutations/serial_without_MM_voice', 'TestLinuxAcquisitionConflictRetiresCachedOwnerUntilFreshHandoff'])
  assert(red.records.some(row => row.Test === name && row.Action === 'pass'), `${name}: normal control did not pass`)
const green = run('green', 'go-runtime', [])
assert.equal(green.status, 0)
assert(!green.records.some(row => row.Action === 'fail'))
for (const name of cases.keys())
  assert(green.records.some(row => row.Test === name && row.Action === 'pass'), `${name}: no pass`)
console.log('Exact review counterexamples failed behaviorally, normal controls passed; complete Linux modem package race suite passed with no skips.')
