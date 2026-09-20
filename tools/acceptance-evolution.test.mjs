import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { spawnSync } from 'node:child_process'
import { defaultRoot, readLedger, validateLedger, checkRepository } from './repository-check.mjs'

// Run the actual normal test entry point against a future valid ledger, not
// merely validateLedger() on an in-memory copy. The marker prevents this test
// alone from recursively spawning itself; every other normal check still runs.
if (process.env.MDD_ACCEPTANCE_EVOLUTION_CHILD !== '1') {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'mdd-acceptance-evolution-'))
  try {
    fs.cpSync(defaultRoot, root, { recursive: true, filter: source =>
      !path.relative(defaultRoot, source).split(path.sep).some(part =>
        ['.git', 'node_modules', '.go-dist'].includes(part) || part.startsWith('review-')) })
    const ledger = readLedger(root)
    const reportPath = 'docs/status/synthetic-evolution-report.md'
    fs.writeFileSync(path.join(root, reportPath), '# Synthetic test fixture\nNo real field test or owner decision is asserted.\n')
    const evidence = { date: '2026-09-20', environment: 'synthetic future fixture',
      artifactSHA256: 'a'.repeat(64), reportPath }
    ledger.decisions.push({ id: 'EVOLUTION-FIXTURE', date: '2026-09-20', authority: 'project_owner',
      summary: 'Synthetic later decision for a checker regression test.', sourcePath: reportPath })
    for (const [index, state] of ['completed', 'accepted', 'superseded', 'cancelled', 'deferred'].entries()) {
      const work = ledger.workstreams[index]
      work.state = state
      work.releaseRelevance = ['completed', 'accepted'].includes(state) ? 'current' : 'deferred'
      work.resolution = { basis: state === 'completed' ? 'implementation_evidence' : state === 'accepted' ? 'acceptance_evidence' : 'user_decision',
        reason: 'Synthetic supported transition, not a real change to project scope.',
        decisionRefs: ['completed', 'accepted'].includes(state) ? [] : ['EVOLUTION-FIXTURE'], evidence: [reportPath] }
      if (state === 'accepted') work.resolution.acceptanceEvidence = evidence
    }
    for (const id of ['ANDROID', 'MACOS_NOTARIZATION']) {
      const scope = ledger.releaseScope.find(item => item.id === id)
      Object.assign(scope, { disposition: 'required', decision: 'EVOLUTION-FIXTURE', summary: 'Synthetic future scope.' })
    }
    ledger.workstreams.push({ id: 'FUTURE-WORK-FIXTURE', state: 'completed', releaseRelevance: 'current',
      summary: 'Synthetic additional workstream.', acceptance: 'Synthetic tests only.', evidence: [reportPath],
      resolution: { basis: 'implementation_evidence', reason: 'Synthetic completed task.', evidence: [reportPath] } })
    for (const id of [ledger.criteria[0].id, 'M50']) {
      Object.assign(ledger.criteria.find(row => row.id === id), { acceptance: 'accepted_hardware', acceptanceEvidence: evidence })
    }
    Object.assign(ledger.evidenceRecords[0], { reviewState: 'verified_existing',
      reviewReason: 'Synthetic reconciliation of existing evidence, no fresh hardware run.',
      reviewReportPath: reportPath, verification: evidence })
    validateLedger(ledger, root)
    fs.writeFileSync(path.join(root, 'docs/status/acceptance.json'), JSON.stringify(ledger, null, 2) + '\n')
    checkRepository(root, { write: true })
    const result = spawnSync(process.execPath, [path.join(root, 'tools/repository-check.test.mjs')], {
      cwd: root, encoding: 'utf8', timeout: 30000,
      env: { ...process.env, MDD_ACCEPTANCE_EVOLUTION_CHILD: '1' },
    })
    assert.equal(result.error, undefined)
    assert.equal(result.signal, null)
    assert.equal(result.status, 0, `normal CI rejects documented ledger evolution:\n${result.stdout}\n${result.stderr}`)
    assert.match(result.stdout, /Acceptance policy:/, 'child must run policy checks, not bypass the suite')
    console.log('Full normal CI accepts terminal states, new workstreams, later scope decisions and reconciled historical evidence')
  } finally { fs.rmSync(root, { recursive: true, force: true }) }
}
