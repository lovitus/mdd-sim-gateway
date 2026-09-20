// Dated audit counterexamples for the 2026-09-20 correction. Invoke explicitly
// against the original/correction snapshot; never import into evolving normal CI.
import assert from 'node:assert/strict'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
const root = path.resolve(process.argv[2] || path.join(path.dirname(fileURLToPath(import.meta.url)), '..'))
const { readLedger, validateLedger } = await import(pathToFileURL(path.join(root, 'tools/repository-check.mjs')))
const ledger = readLedger(root)
const cases = {
  ESIM_FINAL() {
    const work = ledger.workstreams.find(w => w.id === 'ESIM_DELETE')
    assert.equal(work.state, 'accepted', 'accepted physical-deletion direction was reopened')
    assert.match(work.summary, /physical deletion/)
    assert.match(work.summary, /multiple confirmations/)
  },
  TERMINAL_STATES() {
    for (const state of ['completed', 'accepted', 'superseded', 'deferred', 'cancelled']) {
      const copy = structuredClone(ledger)
      const work = copy.workstreams.find(w => w.id === 'ANDROID')
      work.state = state
      work.resolution = { basis: state === 'completed' ? 'implementation_evidence' : 'user_decision',
        reason: 'Test-only documented resolution.', decisionRefs: state === 'completed' ? [] : ['PRIMARY_AGENTS'], evidence: ['README.md'] }
      assert.doesNotThrow(() => validateLedger(copy, root), 'documented terminal state rejected')
    }
  },
  NOTARIZATION_SCOPE() {
    assert.equal(ledger.releaseScope?.find(s => s.id === 'MACOS_NOTARIZATION')?.disposition, 'excluded', 'excluded notarization must not be a release gap')
    assert.equal(ledger.releaseScope?.find(s => s.id === 'ANDROID')?.disposition, 'deferred', 'Android deferral must be explicit')
  },
  HISTORICAL_EVIDENCE() {
    const row = ledger.criteria.find(c => c.id === 'M50')
    assert.equal(row.acceptance, 'evidence_review_pending', 'historical call acceptance was flattened into fresh hardware pending')
    assert.ok(row.historicalEvidence.includes('H-CALL-PARTIAL'))
    assert.ok(ledger.evidenceRecords.find(e => e.id === 'H-CALL-PARTIAL'))
  },
}
for (const [id, test] of Object.entries(cases)) {
  try { test(); console.log(`PASS ${id}`) }
  catch (error) {
    // A code/import failure is not an intended negative-control assertion.
    if (error.code !== 'ERR_ASSERTION') throw error
    console.log(`FAIL ${id}: ${error.message}`)
    process.exitCode = 1
  }
}
