import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
const root = path.resolve(process.argv[2] || '')
const expected = '19f966f7d673a074e7218680d6c0ed8b1d530e1c'
const head = spawnSync('git', ['-C', root, 'rev-parse', 'HEAD'], { encoding: 'utf8' })
assert.equal(head.status, 0); assert.equal(head.stdout.trim(), expected, 'wrong negative-control baseline')
const test = path.join(path.dirname(fileURLToPath(import.meta.url)), 'acceptance-regressions.mjs')
const result = spawnSync(process.execPath, [test, root], { encoding: 'utf8', timeout: 30000 })
process.stdout.write(result.stdout || ''); process.stderr.write(result.stderr || '')
assert.equal(result.error, undefined); assert.equal(result.signal, null); assert.equal(result.status, 1)
const failures = [...result.stdout.matchAll(/^FAIL ([A-Z_]+):/gm)].map(m => m[1]).sort()
assert.deepEqual(failures, ['ESIM_FINAL', 'HISTORICAL_EVIDENCE', 'NOTARIZATION_SCOPE', 'TERMINAL_STATES'])
for (const diagnostic of ['accepted physical-deletion direction was reopened', 'documented terminal state rejected', 'excluded notarization must not be a release gap', 'historical call acceptance was flattened'])
  assert.ok(result.stdout.includes(diagnostic), `missing intended diagnostic: ${diagnostic}`)
assert.equal(result.stderr, '', 'import/runtime errors do not prove the findings')
console.log('CONFIRMED: four intended ledger/scope assertions on pinned pre-fix source; not compilation/import failures')
