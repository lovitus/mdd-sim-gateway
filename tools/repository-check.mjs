import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import crypto from 'node:crypto'
import { fileURLToPath } from 'node:url'

const self = fileURLToPath(import.meta.url)
export const defaultRoot = path.resolve(path.dirname(self), '..')
const sha256 = bytes => crypto.createHash('sha256').update(bytes).digest('hex')
const text = value => typeof value === 'string' && value.trim().length > 0
const implementation = new Set(['implemented', 'partial', 'policy', 'superseded', 'not_implemented', 'needs_verification'])
const acceptance = new Set(['covered_by_tests', 'pending_hardware', 'pending_product', 'not_applicable', 'accepted_hardware'])
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
  assert.equal(ledger.schemaVersion, 1)
  assert.match(ledger.baseline, /^[0-9a-f]{40}$/)
  assert.match(ledger.baselineTree, /^[0-9a-f]{40}$/)
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
    if (criterion.acceptance === 'accepted_hardware') {
      const evidence = criterion.acceptanceEvidence
      assert.ok(evidence && text(evidence.environment) && text(evidence.date), `hardware evidence missing: ${criterion.id}`)
      assert.match(evidence.artifactSHA256 || '', /^[0-9a-f]{64}$/)
      safeFile(root, evidence.reportPath)
    }
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
    assert.ok(['not_implemented', 'partial', 'needs_verification', 'unresolved_cause', 'ongoing'].includes(item.state))
    assert.ok(text(item.summary) && text(item.acceptance))
    assert.ok(Array.isArray(item.evidence) && item.evidence.length)
    item.evidence.forEach(p => safeFile(root, p))
  }
  for (const id of ['ANDROID', 'ESIM_DELETE', 'RECORDING', 'IDENTICAL_REINSERT'])
    assert.ok(workIDs.has(id), `required open workstream disappeared: ${id}`)
  return ledger
}
const escape = value => value.replaceAll('|', '\\|').replaceAll('\n', ' ')
export function summaries(ledger) {
  const counts = key => [...new Set(ledger.criteria.map(row => row[key]))].sort()
    .map(state => `- ${state}: ${ledger.criteria.filter(row => row[key] === state).length}`).join('\n')
  const rowLinks = (rows, prefix) => rows.map(row => `| [${row.id}](${prefix}docs/status/README.md#${row.id.toLowerCase()}) | ${row.implementation} | ${row.acceptance} | ${escape(row.requirement)} |`).join('\n')
  const tableHead = '| Criterion | Implementation | Acceptance | Original requirement |\n|---|---|---|---|'
  const rootIntro = '# Current rewrite acceptance\n\n<!-- Generated by tools/repository-check.mjs; edit docs/status/acceptance.json. -->\n\n' +
    'This is an implementation/acceptance crosswalk, not a claim that every original box is complete.\n' +
    'All 59 original general criteria and four macOS criteria are preserved in the versioned ledger.\n' +
    'Archived Python/Asterisk/VPCD designs are historical; superseded does not mean implemented.\n' +
    'A green test run is not live device or carrier acceptance. See [the ledger](docs/status/README.md) and [overall issue #3](https://github.com/lovitus/mdd-sim-gateway/issues/3).\n\n'
  const current = rootIntro + tableHead + '\n' + rowLinks(ledger.criteria.filter(row => row.origin.document === 'TODO.md'), '') + '\n'
  const mac = '# macOS acceptance and safety boundary\n\n<!-- Generated by tools/repository-check.mjs; edit docs/status/acceptance.json. -->\n\n' +
    'Current macOS distribution is arm64; default Modem behavior remains disabled (PC/SC-only).\n' +
    'The Go installer supports a per-user LaunchAgent. Universal/notarized builds, multi-Modem and cross-vendor acceptance remain separate work.\n' +
    'Historical claims and original detailed safety requirements are preserved in [the archived plan](docs/archive/2026-09-20/TODO_MACOS_AGENT.md), not current deployment instructions.\n\n' + tableHead + '\n' + rowLinks(ledger.criteria.filter(row => row.origin.document === 'TODO_MACOS_AGENT.md'), '') + '\n'
  let status = '# Versioned implementation and acceptance ledger\n\n<!-- Generated by tools/repository-check.mjs; edit acceptance.json. -->\n\n' +
    `Baseline: \`${ledger.baseline}\`; tree \`${ledger.baselineTree}\`; review ${ledger.reviewDate}.\n\n` +
    `${ledger.scope}\n\n` +
    '## How to read this ledger\n\n' +
    'The [JSON](acceptance.json) is the versioned source of these summaries. Implementation and acceptance are deliberately separate. ' +
    '`implemented` means an identified source path exists and was reviewed, not that all hardware was accepted. ' +
    '`covered_by_tests` identifies an automated contract; it does not certify universal behavior. ' +
    '`pending_hardware` and `pending_product` stay open; `superseded` describes an obsolete design, not delivered functionality. ' +
    'Evidence paths identify the inspected boundaries; final CI run/head evidence belongs to the linked PR/issue. ' +
    'Historical field reports are not independently reproduced by this review.\n\n' +
    'CI verifies identities, preserved historical requirements, hashes, source paths and summary consistency—not the physical truth of a field report. ' +
    'A future `accepted_hardware` entry must name a redacted report, artifact hash, environment and date. ' +
    'Do not put credentials, raw device identities or unbounded private captures in the ledger.\n\n' +
    'Update acceptance.json, run `node tools/repository-check.mjs --write`, and review the generated diff. ' +
    'Machine-local progress notes and historical archives are not the authoritative status.\n\n' +
    '## Disposition counts\n\n### Implementation\n\n' + counts('implementation') + '\n\n### Acceptance\n\n' + counts('acceptance') +
    '\n\n## Unfinished cross-cutting work\n\n'
  for (const item of ledger.workstreams) status += `### ${item.id}\n\n**${item.state}.** ${item.summary}\n\nAcceptance needed: ${item.acceptance}\n\n`
  status += '## All original unchecked criteria\n\n'
  for (const row of ledger.criteria) {
    const archive = ledger.archives.find(item => item.original === row.origin.document)
    status += `### ${row.id}\n\n**Implementation: ${row.implementation}; acceptance: ${row.acceptance}.**\n\n` +
      `Original ([${row.origin.document}:${row.origin.line}](../../${archive.path})): ${row.requirement.replaceAll('\n', ' ')}\n\n` +
      `${row.rationale}\n\nSource/test boundaries: ${row.evidence.map(p => `[\`${p}\`](../../${p})`).join(', ')}.\n\n`
  }
  const postponed = '# Open product work and unresolved historical causes\n\n<!-- Generated by tools/repository-check.mjs; edit docs/status/acceptance.json. -->\n\n' +
    'The [versioned ledger](docs/status/README.md) contains every original criterion and its disposition. ' +
    'The [unchanged historical notes](docs/archive/2026-09-20/postponed-tasks.md) retain old field reports and hypotheses; they are not current operating instructions. ' +
    'Repository cleanup does not close product or hardware gaps, and does not establish the cause of past carrier/Modem failures.\n\n' +
    ledger.workstreams.map(item => `## ${item.id} — ${item.state}\n\n${item.summary}\n\nNext acceptance: ${item.acceptance}\n`).join('\n')
  return { 'TODO.md': current, 'TODO_MACOS_AGENT.md': mac, 'postponed-tasks.md': postponed, 'docs/status/README.md': status.trimEnd() + '\n' }
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
  return { criteria: ledger.criteria.length, workstreams: ledger.workstreams.length, claimsOfNewHardwareAcceptance: ledger.criteria.filter(c => c.acceptance === 'accepted_hardware').length }
}
if (process.argv[1] && path.resolve(process.argv[1]) === self) {
  console.log(JSON.stringify(checkRepository(defaultRoot, { write: process.argv.includes('--write') }), null, 2))
}
