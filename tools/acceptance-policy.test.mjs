import assert from 'node:assert/strict'
import { readLedger, validateLedger, summaries } from './repository-check.mjs'
const original = readLedger()
// Build deliberate invalid-case inputs independently of today's completion state.
// A prior acceptance record must not make a 'missing evidence' test stop being invalid.
const fixture = structuredClone(original)
Object.assign(fixture.workstreams.find(w => w.id === 'ANDROID'), {
  state: 'deferred', releaseRelevance: 'deferred',
  resolution: { basis: 'user_decision', reason: 'Synthetic starting state.',
    decisionRefs: ['PRIMARY_AGENTS'], evidence: ['README.md'] },
})
Object.assign(fixture.criteria[0], { implementation: 'implemented', acceptance: 'covered_by_tests' })
delete fixture.criteria[0].acceptanceEvidence
Object.assign(fixture.evidenceRecords[0], { reviewState: 'reported_not_revalidated' })
for (const key of ['reviewReason', 'reviewReportPath', 'verification']) delete fixture.evidenceRecords[0][key]
const candidate = change => { const copy = structuredClone(fixture); change(copy); return copy }
const rejects = (change, pattern) => assert.throws(() => validateLedger(candidate(change)), pattern)
const resolution = (basis = 'user_decision') => ({ basis, reason: 'Synthetic transition fixture, not a new field claim.',
  decisionRefs: basis === 'user_decision' ? ['PRIMARY_AGENTS'] : [], evidence: ['README.md'] })
// Existence of an ID is immutable history, not a ban on legitimate closure.
for (const state of ['completed', 'accepted', 'superseded', 'deferred', 'cancelled']) {
  const copy = candidate(d => {
    const work = d.workstreams.find(w => w.id === 'ANDROID')
    work.state = state
    work.resolution = resolution(state === 'completed' ? 'implementation_evidence' : 'user_decision')
  })
  validateLedger(copy)
  assert.match(summaries(copy)['postponed-tasks.md'], new RegExp(`State: ${state}`))
}
// A later owner's documented decision can change scope without editing the validator.
const future = candidate(d => {
  d.decisions.push({ ...d.decisions[0], id: 'FUTURE-FIXTURE', summary: 'Synthetic later scope decision.' })
  d.releaseScope.find(s => s.id === 'ANDROID').disposition = 'required'
  d.releaseScope.find(s => s.id === 'ANDROID').decision = 'FUTURE-FIXTURE'
  const w = d.workstreams.find(w => w.id === 'ANDROID')
  w.releaseRelevance = 'current'; w.state = 'completed'; w.resolution = resolution('implementation_evidence')
  const mac = d.releaseScope.find(s => s.id === 'MACOS_NOTARIZATION')
  mac.disposition = 'required'; mac.decision = 'FUTURE-FIXTURE'; mac.summary = 'Synthetic future notarization decision.'
})
validateLedger(future)
assert.match(summaries(future)['TODO_MACOS_AGENT.md'], /MACOS_NOTARIZATION \| required/)
assert.doesNotMatch(summaries(future)['TODO_MACOS_AGENT.md'], /No notarization and no .p8 credentials in this round/)
rejects(d => { delete d.workstreams.find(w => w.id === 'ANDROID').resolution }, /resolution reason missing/)
rejects(d => { d.workstreams.find(w => w.id === 'ANDROID').resolution.reason = ' ' }, /resolution reason missing/)
rejects(d => { d.workstreams.find(w => w.id === 'ANDROID').resolution.decisionRefs = [] }, /scope decision missing/)
rejects(d => { d.workstreams.find(w => w.id === 'ANDROID').resolution.decisionRefs = ['UNKNOWN'] }, /unknown decision/)
rejects(d => { d.workstreams.find(w => w.id === 'ANDROID').resolution.evidence = [] }, /resolution evidence missing/)
rejects(d => { d.workstreams.find(w => w.id === 'ANDROID').state = 'forever_open' }, /invalid workstream state/)
rejects(d => { d.workstreams.find(w => w.id === 'ANDROID').releaseRelevance = 'hidden' }, /invalid release relevance/)
rejects(d => { d.releaseScope[0].decision = 'missing' }, /unknown decision/)
rejects(d => { d.criteria[0].decisionRefs = [] }, /current requirement needs scope decision/)
rejects(d => { d.evidenceRecords[0].excerptSHA256 = '0'.repeat(64) }, /historical report excerpt changed/)
rejects(d => { d.evidenceRecords[0].startLine = 0 }, /invalid evidence range/)
rejects(d => { d.evidenceRecords[0].endLine = 1000000 }, /invalid evidence range/)
rejects(d => { d.evidenceRecords[0].reportPath = '../private' }, /invalid evidence path/)
rejects(d => { d.criteria[0].historicalEvidence.pop() }, /historical evidence link lost/)
rejects(d => { d.evidenceRecords.shift() }, /unknown historical evidence/)
rejects(d => { d.evidenceRecords[0].reviewState = 'verified_existing' }, /evidence review reason missing/)
rejects(d => { Object.assign(d.evidenceRecords[0], { reviewState: 'verified_existing', reviewReason: 'fixture', reviewReportPath: 'README.md' }) }, /hardware evidence missing/)
// Reconciled old acceptance is legal with metadata; no new test is required by
// the schema. The synthetic hash below is only a unit-test fixture.
const fixtureEvidence = { date: '2026-09-01', environment: 'synthetic unit-test fixture', artifactSHA256: 'a'.repeat(64), reportPath: 'README.md' }
validateLedger(candidate(d => {
  Object.assign(d.evidenceRecords[0], { reviewState: 'verified_existing', reviewReason: 'Earlier scoped evidence reconciled.', reviewReportPath: 'README.md', verification: fixtureEvidence })
  Object.assign(d.criteria[0], { acceptance: 'accepted_hardware', acceptanceEvidence: fixtureEvidence })
}))
// Never turn a product-direction decision into hardware evidence implicitly.
rejects(d => { d.criteria[0].acceptance = 'accepted_hardware' }, /hardware evidence missing/)
// Rendering reflects the live ledger, not permanently frozen release decisions.
const rendered = summaries(original)
const [openWork] = rendered['postponed-tasks.md'].split('## Accepted, deferred')
for (const work of original.workstreams) {
  assert.ok(rendered['postponed-tasks.md'].includes(`### ${work.id}\n\n**State: ${work.state};`))
  const shouldBeOpen = !['completed', 'accepted', 'superseded', 'deferred', 'cancelled'].includes(work.state) && work.releaseRelevance === 'current'
  assert.equal(openWork.includes(`### ${work.id}\n`), shouldBeOpen)
}
for (const scope of original.releaseScope)
  assert.ok(rendered['TODO_MACOS_AGENT.md'].includes(`| ${scope.id} | ${scope.disposition} |`))
for (const record of original.evidenceRecords)
  assert.ok(rendered['docs/status/README.md'].includes(`### ${record.id}\n\n**${record.reviewState}.**`))
console.log('Acceptance policy: legitimate terminal states, later scope changes and evidence reconciliation pass; unsupported claims and dropped provenance fail')
