import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { spawnSync } from 'node:child_process'
import { analyze, defaultRoot } from '../webui/scripts/source-graph.mjs'

const baseline = process.argv[2]
assert.ok(baseline, 'a disposable pinned baseline checkout is required')
const target = path.resolve(baseline, 'webui')
assert.notEqual(target, defaultRoot, 'never overwrite the candidate test input')
const result = analyze(target)
assert.deepEqual(result.errors, [])
assert.equal(result.totalModules, 83)
assert.equal(result.reachableModules, 49)
assert.equal(result.unreachable.length, 34)
assert.equal(result.unreachableLines, 6355)
assert.ok(result.reachable.includes('src/api.js'), 'do not classify the shared API as dead')
assert.equal(result.testOnlyReferences.length, 10)
console.log('CONFIRMED: pinned baseline has 34 orphan modules / 6355 lines and 10 test-only source references; shared API is live')

// Copy only the counterexample and its parser helper. Production code is not
// changed. Use the candidate's exact npm-ci dependencies in the disposable tree.
fs.mkdirSync(path.join(target, 'scripts'), { recursive: true })
fs.copyFileSync(path.join(defaultRoot, 'scripts/source-graph.mjs'), path.join(target, 'scripts/source-graph.mjs'))
fs.copyFileSync(path.join(defaultRoot, 'tests/sharedI18n.mjs'), path.join(target, 'tests/sharedI18n.mjs'))
assert.ok(!fs.existsSync(path.join(target, 'node_modules')), 'baseline dependency path must be disposable and absent')
fs.symlinkSync(path.join(defaultRoot, 'node_modules'), path.join(target, 'node_modules'), 'dir')
const run = spawnSync(process.execPath, ['tests/sharedI18n.mjs'], { cwd: target, encoding: 'utf8', timeout: 30000 })
const output = (run.stdout || '') + (run.stderr || '')
assert.equal(run.status, 1, output)
assert.ok(output.includes('src/goCallCoordinator.jsx must use the mounted language context'), output)
assert.ok(output.includes('<span>en:Cancel</span>') && output.includes('<span>zh:取消</span>'), output)
assert.ok(!/Cannot find|ERR_MODULE_NOT_FOUND|SyntaxError/.test(output), output)
console.log(output)
console.log('CONFIRMED: original mounted call controls read the wrong React language context; this is an assertion failure, not a missing dependency')
