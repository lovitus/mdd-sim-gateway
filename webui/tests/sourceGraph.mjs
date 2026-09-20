import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { analyze } from '../scripts/source-graph.mjs'

const root = fs.mkdtempSync(path.join(os.tmpdir(), 'mdd-source-graph-'))
const write = (name, body) => { const file = path.join(root, name); fs.mkdirSync(path.dirname(file), { recursive: true }); fs.writeFileSync(file, body) }
try {
  write('src/main.jsx', `import './view.jsx'; export * from './export.js'; import('./lazy.js'); new URL('./worker.js', import.meta.url)`)
  write('src/view.jsx', `// import './not-a-module.js'\nexport default () => <span>import './also-not-a-module.js'</span>`)
  for (const name of ['export', 'lazy', 'worker']) write(`src/${name}.js`, 'export const value = 1')
  assert.deepEqual(analyze(root).errors, [])
  assert.equal(analyze(root).reachableModules, 5, 'static, re-export, lazy and worklet modules are reachable')
  write('src/orphan.js', 'export const unused = 1')
  write('tests/orphan.mjs', `import {unused} from '../src/orphan.js'`)
  assert.deepEqual(analyze(root).unreachable, ['src/orphan.js'])
  assert.deepEqual(analyze(root).testOnlyReferences, [{ test: 'tests/orphan.mjs', source: 'src/orphan.js' }])
  fs.unlinkSync(path.join(root, 'src/orphan.js'))
  assert.ok(analyze(root).errors.some(error => error.includes('Missing local import')))
  fs.unlinkSync(path.join(root, 'tests/orphan.mjs'))
  write('src/extra.mjs', 'export const extra = 1')
  assert.ok(analyze(root).unreachable.includes('src/extra.mjs'))
  fs.unlinkSync(path.join(root, 'src/extra.mjs'))
  write('src/main.jsx', `const target = './worker.js'; import(target)`)
  assert.ok(analyze(root).errors.some(error => error.includes('Nonliteral dynamic import')))
  write('src/main.jsx', `import.meta.glob('./*.js')`)
  assert.ok(analyze(root).errors.some(error => error.includes('explicit graph support')))
} finally { fs.rmSync(root, { recursive: true, force: true }) }
console.log('Source graph detects orphan/test-only modules without counting comments as imports')
