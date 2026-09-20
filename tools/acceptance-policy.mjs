import assert from 'node:assert/strict'
import fs from 'node:fs'
import crypto from 'node:crypto'

const text = value => typeof value === 'string' && value.trim().length > 0
const unique = (values, name) => {
  assert.ok(Array.isArray(values), `${name} must be an array`)
  assert.equal(new Set(values).size, values.length, `${name} contains duplicate IDs`)
}
export const resolvedStates = new Set(['completed', 'accepted', 'superseded', 'deferred', 'cancelled'])
export const workstreamStates = new Set(['not_implemented', 'partial', 'needs_verification', 'unresolved_cause', 'ongoing', ...resolvedStates])
const relevance = new Set(['current', 'deferred', 'excluded', 'needs_decision', 'ongoing', 'historical'])

// Preserve the identity of requirements, not their open state. Completion and
// user-directed scope changes have normal, evidence-bearing transitions.
export function validatePolicy(ledger, root, safeFile) {
  assert.ok(Array.isArray(ledger.decisions) && ledger.decisions.length > 0, 'missing scope decisions')
  const decisions = new Map()
  for (const item of ledger.decisions) {
    assert.ok(text(item.id) && !decisions.has(item.id), 'duplicate/invalid decision')
    assert.equal(item.authority, 'project_owner', `decision authority: ${item.id}`)
    assert.match(item.date || '', /^\d{4}-\d{2}-\d{2}$/)
    assert.ok(text(item.summary))
    safeFile(root, item.sourcePath)
    decisions.set(item.id, item)
  }
  const refs = (values = []) => {
    unique(values, 'decision references')
    for (const id of values) assert.ok(decisions.has(id), `unknown decision: ${id}`)
  }
  assert.ok(Array.isArray(ledger.releaseScope) && ledger.releaseScope.length > 0, 'missing release scope')
  unique(ledger.releaseScope.map(item => item.id), 'scope IDs')
  for (const item of ledger.releaseScope) {
    assert.ok(text(item.id) && text(item.summary))
    assert.ok(['required', 'accepted', 'deferred', 'excluded', 'needs_decision'].includes(item.disposition), 'invalid release disposition')
    refs([item.decision])
  }
  assert.ok(Array.isArray(ledger.workstreams), 'missing workstreams')
  const work = new Map(ledger.workstreams.map(item => [item.id, item]))
  for (const item of ledger.workstreams) {
    assert.ok(workstreamStates.has(item.state), `invalid workstream state: ${item.id}`)
    assert.ok(relevance.has(item.releaseRelevance), `invalid release relevance: ${item.id}`)
    if (resolvedStates.has(item.state)) {
      const resolution = item.resolution
      assert.ok(resolution && text(resolution.reason), `resolution reason missing: ${item.id}`)
      assert.ok(['implementation_evidence', 'user_decision', 'acceptance_evidence'].includes(resolution.basis), `resolution basis missing: ${item.id}`)
      assert.ok(Array.isArray(resolution.evidence) && resolution.evidence.length > 0, `resolution evidence missing: ${item.id}`)
      resolution.evidence.forEach(p => safeFile(root, p))
      refs(resolution.decisionRefs)
      if (['superseded', 'deferred', 'cancelled'].includes(item.state) || resolution.basis === 'user_decision') {
        assert.equal(resolution.basis, 'user_decision', `scope change needs owner decision: ${item.id}`)
        assert.ok(resolution.decisionRefs?.length, `scope decision missing: ${item.id}`)
      }
      if (item.state === 'accepted')
        assert.ok(['user_decision', 'acceptance_evidence'].includes(resolution.basis), `acceptance basis missing: ${item.id}`)
      if (resolution.basis === 'acceptance_evidence') validateAcceptanceEvidence(resolution.acceptanceEvidence, root, safeFile, item.id)
    }
  }
  assert.ok(Array.isArray(ledger.evidenceRecords), 'missing evidence registry')
  const records = new Map()
  const criteria = new Map(ledger.criteria.map(c => [c.id, c]))
  for (const item of ledger.evidenceRecords) {
    assert.ok(text(item.id) && !records.has(item.id), 'duplicate/invalid evidence ID')
    assert.ok(text(item.summary) && text(item.limitations), `evidence scope missing: ${item.id}`)
    assert.ok(['reported_not_revalidated', 'verified_existing', 'contradicted', 'superseded'].includes(item.reviewState), `invalid evidence review state: ${item.id}`)
    const lines = fs.readFileSync(safeFile(root, item.reportPath), 'utf8').match(/[^\n]*(?:\n|$)/g).filter(Boolean)
    assert.ok(Number.isInteger(item.startLine) && Number.isInteger(item.endLine) && item.startLine >= 1 && item.endLine >= item.startLine && item.endLine <= lines.length, `invalid evidence range: ${item.id}`)
    const digest = crypto.createHash('sha256').update(lines.slice(item.startLine - 1, item.endLine).join('')).digest('hex')
    assert.equal(item.excerptSHA256, digest, `historical report excerpt changed: ${item.id}`)
    unique(item.criteria, 'evidence criterion references')
    assert.ok(item.criteria.length > 0)
    for (const id of item.criteria) assert.ok(criteria.has(id), `unknown evidence criterion: ${id}`)
    if (item.reviewState !== 'reported_not_revalidated') {
      assert.ok(text(item.reviewReason), `evidence review reason missing: ${item.id}`)
      safeFile(root, item.reviewReportPath)
      if (item.reviewState === 'verified_existing') validateAcceptanceEvidence(item.verification, root, safeFile, item.id)
    }
    records.set(item.id, item)
  }
  for (const row of ledger.criteria) {
    refs(row.decisionRefs)
    if (row.currentRequirement !== undefined) {
      assert.ok(text(row.currentRequirement) && row.decisionRefs?.length, `current requirement needs scope decision: ${row.id}`)
    }
    unique(row.historicalEvidence, `historical references: ${row.id}`)
    for (const id of row.historicalEvidence) {
      assert.ok(records.has(id), `unknown historical evidence: ${id}`)
      assert.ok(records.get(id).criteria.includes(row.id), `historical evidence scope mismatch: ${row.id}`)
    }
    const expected = [...records.values()].filter(r => r.criteria.includes(row.id)).map(r => r.id).sort()
    assert.deepEqual([...row.historicalEvidence].sort(), expected, `historical evidence link lost: ${row.id}`)
    if (row.acceptance === 'evidence_review_pending') assert.ok(row.historicalEvidence.length && text(row.remainingAcceptance), `evidence-review context missing: ${row.id}`)
    for (const id of row.workstreamRefs || []) assert.ok(work.has(id), `unknown workstream: ${id}`)
  }
}
export function validateAcceptanceEvidence(evidence, root, safeFile, id) {
  assert.ok(evidence && text(evidence.environment) && text(evidence.date), `hardware evidence missing: ${id}`)
  assert.match(evidence.date, /^\d{4}-\d{2}-\d{2}$/)
  assert.match(evidence.artifactSHA256 || '', /^[0-9a-f]{64}$/)
  safeFile(root, evidence.reportPath)
}
