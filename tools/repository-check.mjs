import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import crypto from 'node:crypto'
import { fileURLToPath } from 'node:url'
import { validatePolicy, validateAcceptanceEvidence } from './acceptance-policy.mjs'
import { summaries } from './acceptance-summary.mjs'
export { summaries } from './acceptance-summary.mjs'

const self = fileURLToPath(import.meta.url)
export const defaultRoot = path.resolve(path.dirname(self), '..')
const sha256 = bytes => crypto.createHash('sha256').update(bytes).digest('hex')
const text = value => typeof value === 'string' && value.trim().length > 0
const implementation = new Set(['implemented', 'partial', 'policy', 'superseded', 'not_implemented', 'needs_verification'])
const acceptance = new Set(['covered_by_tests', 'pending_hardware', 'pending_product', 'not_applicable', 'accepted_hardware', 'evidence_review_pending'])
function safeFile(root, relative) {
  assert.ok(text(relative) && !relative.includes('\\') && !path.isAbsolute(relative) &&
    !relative.split('/').some(part => !part || part === '..' || part === '.'), `invalid evidence path ${relative}`)
  const file = path.resolve(root, relative)
  assert.ok(fs.statSync(file).isFile(), `evidence is not a file: ${relative}`)
  const real = fs.realpathSync(file)
  assert.ok(real.startsWith(fs.realpathSync(root) + path.sep), `evidence escapes checkout: ${relative}`)
  return file
}
export function readLedger(root = defaultRoot) {
  return JSON.parse(fs.readFileSync(path.join(root, 'docs/status/acceptance.json'), 'utf8'))
}
export function validateLedger(ledger, root = defaultRoot) {
  assert.equal(ledger.schemaVersion, 2)
  assert.match(ledger.baseline, /^[0-9a-f]{40}$/)
  assert.match(ledger.baselineTree, /^[0-9a-f]{40}$/)
  assert.match(ledger.correctionBaseline, /^[0-9a-f]{40}$/)
  assert.match(ledger.correctionBaselineTree, /^[0-9a-f]{40}$/)
  assert.match(ledger.reviewDate, /^\d{4}-\d{2}-\d{2}$/)
  assert.ok(text(ledger.scope))
  assert.match(ledger.issue, /^https:\/\/github\.com\/lovitus\/mdd-sim-gateway\/issues\/\d+$/)
  assert.ok(Array.isArray(ledger.archives))
  assert.ok(Array.isArray(ledger.criteria) && ledger.criteria.length >= 63)
  const archiveMap = new Map()
  for (const archive of ledger.archives) {
    assert.ok(!archiveMap.has(archive.original), 'duplicate archive')
    const bytes = fs.readFileSync(safeFile(root, archive.path))
    assert.equal(sha256(bytes), archive.sha256, `${archive.path}: historical bytes changed`)
    archiveMap.set(archive.original, bytes.toString('utf8'))
  }
  const seen = new Set()
  for (const criterion of ledger.criteria) {
    assert.ok(!seen.has(criterion.id), `duplicate criterion ${criterion.id}`)
    seen.add(criterion.id)
    assert.ok(implementation.has(criterion.implementation), `invalid implementation: ${criterion.id}`)
    assert.ok(acceptance.has(criterion.acceptance), `invalid acceptance: ${criterion.id}`)
    assert.ok(text(criterion.requirement) && text(criterion.rationale))
    assert.ok(Array.isArray(criterion.evidence) && criterion.evidence.length > 0)
    criterion.evidence.forEach(p => safeFile(root, p))
    if (['policy', 'superseded'].includes(criterion.implementation))
      assert.equal(criterion.acceptance, 'not_applicable', `obsolete/policy is not field acceptance: ${criterion.id}`)
    else assert.notEqual(criterion.acceptance, 'not_applicable', `live criterion cannot disappear: ${criterion.id}`)
    if (criterion.acceptance === 'accepted_hardware')
      validateAcceptanceEvidence(criterion.acceptanceEvidence, root, safeFile, criterion.id)
  }
  // Preserve every original unchecked requirement and its ordered identity. A
  // passing build may not silently drop an inconvenient product acceptance item.
  for (const [doc, prefix, count] of [['TODO.md', 'M', 59], ['TODO_MACOS_AGENT.md', 'MAC', 4]]) {
    const source = archiveMap.get(doc)
    assert.ok(source, `missing historical ${doc}`)
    const matches = [...source.matchAll(/^- \[ \] (.*(?:\n(?!- \[|#|\s*$).*)*)/gm)]
    assert.equal(matches.length, count)
    const mapped = ledger.criteria.filter(row => row.origin.document === doc)
    assert.equal(mapped.length, count, `criterion count changed for ${doc}`)
    matches.forEach((match, i) => {
      assert.equal(mapped[i].id, `${prefix}${String(i + 1).padStart(2, '0')}`)
      assert.equal(mapped[i].requirement, match[1].trim(), `${mapped[i].id}: original requirement lost`)
      assert.equal(mapped[i].origin.line, source.slice(0, match.index).split('\n').length)
    })
  }
  const workIDs = new Set()
  for (const item of ledger.workstreams) {
    assert.ok(text(item.id) && !workIDs.has(item.id), 'duplicate/invalid workstream')
    workIDs.add(item.id)
    assert.ok(text(item.summary) && text(item.acceptance))
    assert.ok(Array.isArray(item.evidence) && item.evidence.length)
    item.evidence.forEach(p => safeFile(root, p))
  }
  for (const id of ['ANDROID', 'ESIM_DELETE', 'RECORDING', 'IDENTICAL_REINSERT'])
    assert.ok(workIDs.has(id), `historical workstream disappeared: ${id}`)
  validatePolicy(ledger, root, safeFile)
  return ledger
}
export function checkRepository(root = defaultRoot, { write = false } = {}) {
  const ledger = validateLedger(readLedger(root), root)
  for (const [name, generated] of Object.entries(summaries(ledger))) {
    const file = path.join(root, name)
    if (write) fs.writeFileSync(file, generated)
    else assert.equal(fs.readFileSync(file, 'utf8'), generated, `regenerate ${name} with --write`)
  }
  const activeDocs = ['AGENTS.md', 'README.md', 'DEPLOYMENT.md', 'DEVELOPMENT.md', 'GO_REWRITE.md', 'TODO.md', 'TODO_MACOS_AGENT.md', 'postponed-tasks.md', 'agent/MODEM_AGENT.md', 'webui/src/mdd/UPSTREAM.md']
  for (const name of activeDocs) {
    const content = fs.readFileSync(safeFile(root, name), 'utf8')
    assert.ok(!content.includes('TODO_CURRENT_RECOVERY.md'), `${name}: depends on ignored local progress`)
  }
  const deploy = fs.readFileSync(path.join(root, 'DEPLOYMENT.md'), 'utf8')
  for (const stale of ['Linux 统一 Agent 尚未交付', '开放 TCP VPCD', 'Asterisk/Agent\n本地有界租约', '首版不安装 launchd'])
    assert.ok(!deploy.includes(stale), `retired deployment instruction: ${stale}`)
  assert.ok(!fs.readFileSync(path.join(root, 'README.md'), 'utf8').includes('macOS%20%7C%20Android-green'))
  const resources = JSON.parse(fs.readFileSync(path.join(root, 'docs/reviews/retired-resources-3e6d5db.json')))
  for (const item of resources.files) {
    assert.ok(!fs.existsSync(path.join(root, item.path)), `retired resource restored: ${item.path}`)
    if (item.activeCopy) assert.equal(sha256(fs.readFileSync(safeFile(root, item.activeCopy))), item.activeSHA256,
      `update reviewed provenance for ${item.activeCopy}`)
  }
  const ui = JSON.parse(fs.readFileSync(path.join(root, 'docs/reviews/retired-ui-3e6d5db.json')))
  for (const item of ui.removed) assert.ok(!fs.existsSync(path.join(root, item.path)), `retired UI restored: ${item.path}`)
  ui.retained_and_wired.concat(ui.preserved_shared).forEach(p => safeFile(root, p))
  return { criteria: ledger.criteria.length, workstreams: ledger.workstreams.length, historicalReports: ledger.evidenceRecords.length, recordedHardwareAcceptance: ledger.criteria.filter(c => c.acceptance === 'accepted_hardware').length }
}
if (process.argv[1] && path.resolve(process.argv[1]) === self) {
  console.log(JSON.stringify(checkRepository(defaultRoot, { write: process.argv.includes('--write') }), null, 2))
}
