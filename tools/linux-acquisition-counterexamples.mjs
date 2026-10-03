// Temporary hosted-only proof; removed after its evidence is retained.
import assert from 'node:assert/strict'
import fs from 'node:fs'
import { spawnSync } from 'node:child_process'

assert.equal(process.env.GITHUB_ACTIONS, 'true')
assert.equal(process.platform, 'linux')
const dir = 'go-runtime/internal/linuxmodem'
const baseline = 'baseline/' + dir
const original = fs.readFileSync(baseline + '/prober_linux.go', 'utf8')
const field = 'guard         *linuxdataguard.Guard'
assert.equal(original.split(field).length, 2)
// Only permit the identical-method platform fixture. The entire pre-fix
// acquisition, AT publication and fact cache code remains unchanged.
fs.writeFileSync(baseline + '/prober_linux.go', original.replace(field, 'guard         cellularGuard'))
for (const file of ['acquisition_linux.go', 'acquisition_linux_test.go'])
  fs.copyFileSync(dir + '/' + file, baseline + '/' + file)

fs.mkdirSync('linux-acquisition-evidence')
const cases = new Map([
  ['TestLinuxAcquisitionDuplicateCannotDisconnectOrInhibit', 'ambiguous equipment identity mutated'],
  ['TestLinuxAcquisitionCallStateFencesBothMutations', 'unproven idle state permitted'],
  ['TestLinuxAcquisitionConflictRetiresCachedOwnerUntilFreshHandoff', 'conflicting ownership republished cached ready/disconnected'],
])
function run(label, cwd, args) {
  const result = spawnSync('go', ['test', '-json', '-race', '-count=1', '-timeout=3m', ...args, './internal/linuxmodem'],
    { cwd, encoding: 'utf8', timeout: 300000, maxBuffer: 16 << 20 })
  fs.writeFileSync(`linux-acquisition-evidence/${label}.jsonl`, result.stdout ?? '')
  fs.writeFileSync(`linux-acquisition-evidence/${label}.stderr`, result.stderr ?? '')
  assert.ifError(result.error)
  const records = (result.stdout ?? '').trim().split('\n').filter(Boolean).map(line => JSON.parse(line))
  assert(!records.some(row => row.Action === 'skip' || row.Action === 'build-fail'), `${label}: skipped/build failure`)
  return { status: result.status, records }
}
const red = run('red', 'baseline/go-runtime', ['-run', '^TestLinuxAcquisition'])
assert.notEqual(red.status, 0)
for (const [name, message] of cases) {
  assert(red.records.some(row => row.Test === name && row.Action === 'fail'), `${name}: no compiled failure`)
  assert(red.records.some(row => row.Test?.startsWith(name) && row.Output?.includes(message)), `${name}: wrong failure`)
}
const green = run('green', 'go-runtime', [])
assert.equal(green.status, 0)
assert(!green.records.some(row => row.Action === 'fail'))
for (const name of cases.keys())
  assert(green.records.some(row => row.Test === name && row.Action === 'pass'), `${name}: no pass`)
console.log('Three compiled pre-fix behavioral failures; complete Linux modem package race suite passed with no skips.')
