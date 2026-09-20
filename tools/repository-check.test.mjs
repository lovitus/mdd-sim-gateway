import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { defaultRoot, readLedger, validateLedger, summaries, checkRepository } from './repository-check.mjs'

const original = readLedger()
validateLedger(original)
const negativeFixture = structuredClone(original)
Object.assign(negativeFixture.criteria[0], { implementation: 'implemented', acceptance: 'covered_by_tests' })
delete negativeFixture.criteria[0].acceptanceEvidence
const rejects = (change, pattern) => {
  const candidate = structuredClone(negativeFixture)
  change(candidate)
  assert.throws(() => validateLedger(candidate), pattern)
}
rejects(d => { d.criteria = d.criteria.filter(c => c.id !== 'M01') }, /criterion count changed|false|assert|63/i)
rejects(d => { d.criteria[0].id = d.criteria[1].id }, /duplicate criterion/)
rejects(d => { d.criteria[0].requirement = 'all complete' }, /original requirement lost/)
rejects(d => { d.criteria[0].acceptance = 'accepted_hardware'; delete d.criteria[0].acceptanceEvidence }, /hardware evidence missing/)
rejects(d => { d.criteria[0].acceptance = 'not_applicable' }, /live criterion cannot disappear/)
rejects(d => { d.criteria[0].evidence = ['../outside'] }, /invalid evidence path/)
rejects(d => { d.archives[0].sha256 = '0'.repeat(64) }, /historical bytes changed/)
rejects(d => { d.workstreams = d.workstreams.filter(w => w.id !== 'ANDROID') }, /workstream disappeared/)
assert.deepEqual(summaries(original), summaries(structuredClone(original)))

// Exercise documentation drift and deletion/provenance checks without touching
// the actual checkout or private deployment files.
const root = fs.mkdtempSync(path.join(os.tmpdir(), 'mdd-repository-contract-'))
try {
  const needed = new Set([
    'docs/status/acceptance.json', 'docs/reviews/retired-resources-3e6d5db.json', 'docs/reviews/retired-ui-3e6d5db.json',
    'AGENTS.md', 'README.md', 'DEPLOYMENT.md', 'DEVELOPMENT.md', 'GO_REWRITE.md', 'agent/MODEM_AGENT.md', 'webui/src/mdd/UPSTREAM.md',
    ...Object.keys(summaries(original)), ...original.archives.map(a => a.path),
    ...original.criteria.flatMap(c => c.evidence), ...original.workstreams.flatMap(w => w.evidence),
    ...original.decisions.map(d => d.sourcePath), ...original.evidenceRecords.map(e => e.reportPath),
    ...original.workstreams.flatMap(w => w.resolution?.evidence || []),
    ...original.criteria.flatMap(c => c.acceptanceEvidence ? [c.acceptanceEvidence.reportPath] : []),
    ...original.evidenceRecords.flatMap(e => [e.reviewReportPath, e.verification?.reportPath].filter(Boolean)),
    ...original.workstreams.flatMap(w => w.resolution?.acceptanceEvidence ? [w.resolution.acceptanceEvidence.reportPath] : []),
  ])
  const resources = JSON.parse(fs.readFileSync(path.join(defaultRoot, 'docs/reviews/retired-resources-3e6d5db.json')))
  resources.files.filter(f => f.activeCopy).forEach(f => needed.add(f.activeCopy))
  const ui = JSON.parse(fs.readFileSync(path.join(defaultRoot, 'docs/reviews/retired-ui-3e6d5db.json')))
  ui.retained_and_wired.concat(ui.preserved_shared).forEach(p => needed.add(p))
  for (const relative of needed) {
    const target = path.join(root, relative)
    fs.mkdirSync(path.dirname(target), { recursive: true })
    fs.copyFileSync(path.join(defaultRoot, relative), target)
  }
  assert.deepEqual(checkRepository(root), {
    criteria: original.criteria.length, workstreams: original.workstreams.length,
    historicalReports: original.evidenceRecords.length,
    recordedHardwareAcceptance: original.criteria.filter(c => c.acceptance === 'accepted_hardware').length,
  })
  fs.appendFileSync(path.join(root, 'TODO.md'), '\nEverything is finished.\n')
  assert.throws(() => checkRepository(root), /regenerate TODO.md/)
  checkRepository(root, { write: true })
  const deploy = fs.readFileSync(path.join(root, 'DEPLOYMENT.md'))
  fs.appendFileSync(path.join(root, 'DEPLOYMENT.md'), '\nLinux 统一 Agent 尚未交付\n')
  assert.throws(() => checkRepository(root), /retired deployment instruction/)
  fs.writeFileSync(path.join(root, 'DEPLOYMENT.md'), deploy)
  fs.appendFileSync(path.join(root, 'README.md'), '\nTODO_CURRENT_RECOVERY.md\n')
  assert.throws(() => checkRepository(root), /ignored local progress/)
  fs.copyFileSync(path.join(defaultRoot, 'README.md'), path.join(root, 'README.md'))
  fs.writeFileSync(path.join(root, 'webui/src/App.jsx'), 'export default function RetiredApp() {}')
  assert.throws(() => checkRepository(root), /retired UI restored/)
  fs.unlinkSync(path.join(root, 'webui/src/App.jsx'))
  fs.appendFileSync(path.join(root, resources.files.find(f => f.activeCopy).activeCopy), '\n')
  assert.throws(() => checkRepository(root), /update reviewed provenance/)
} finally { fs.rmSync(root, { recursive: true, force: true }) }
console.log('Repository contracts reject dropped criteria, unsupported acceptance, stale docs and orphan restoration')

// Scope/lifecycle and continuity regressions are part of the existing normal
// main/PR ledger gate, not a separate optional workflow.
await import('./acceptance-policy.test.mjs')
// The dated pre-fix counterexample runner is audit-only: do not require today's
// scope or evidence-review states forever in the evolving normal CI ledger.
await import('./acceptance-evolution.test.mjs')
