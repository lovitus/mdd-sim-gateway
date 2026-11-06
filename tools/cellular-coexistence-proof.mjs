// One-time hosted behavioral counterexamples. Removed after qualification.
import fs from 'node:fs'
import path from 'node:path'
import { spawnSync } from 'node:child_process'
const root = process.cwd()
const records = path.join(process.env.RUNNER_TEMP, 'cellular-coexistence-proof')
fs.mkdirSync(records, { recursive: true })
const patches = new Map()
function change(file, oldText, replacement) {
  const name = path.join(root, file)
  const source = fs.readFileSync(name, 'utf8')
  if (source.split(oldText).length !== 2) throw new Error(`ambiguous proof patch: ${file}`)
  if (!patches.has(name)) patches.set(name, source)
  fs.writeFileSync(name, source.replace(oldText, replacement))
}
const cases = [
  ['internal/agentdata', 'TestManagerBridgesTCPAndUDPAndStopsBackend', 'borrower blocked call'],
  ['internal/agentpolicy', 'TestBorrowedLeaseSurvivesVoiceDataSuspensionWithoutLosingIdentity', 'natural voice suspension cancelled logical borrowing'],
  ['internal/cellulardata', 'TestCallDeferredBorrowKeepsOneOwnerAndResumesAutomatically', 'borrow intent rejected or falsely ready'],
  ['internal/egressexec', 'TestPendingCellularLeaseRetainsOwnerForCancellation', 'preparing lease was exposed as usable or reported invalid'],
  ['internal/linuxmodem', 'TestStaticDataCleanupRetainsVoiceOwnerUntilIdle', 'static cleanup bypassed active voice guard'],
  ['internal/linuxmodem', 'TestDataOwnerOffersOnlyModemManagerCommandCandidate', 'cleanup hid the original status/hangup command owner'],
  ['internal/agentconnection', 'TestBorrowerRecoveryPreservesLogicalOwnerAcrossPartialFailure', 'cellular data session is active'],
]
try {
  // Restore the former broad data/call admission and cleanup behavior, retaining
  // current signatures so a missing symbol can never count as the red result.
  change('go-runtime/internal/agentdata/manager.go', 'cleaning, closed := manager.cleanup[equipmentID] != nil, manager.closed', 'cleaning, closed := manager.items[equipmentID] != nil || manager.cleanup[equipmentID] != nil, manager.closed')
  change('go-runtime/internal/agentpolicy/manager.go', 'return manager.validateLease(ctx, target, false)', 'return manager.validateLease(ctx, target, true)')
  change('go-runtime/internal/cellulardata/service.go', 'err = current.prepareLocked(ctx)\n\tif err != nil && !waitingForCall(err) {', 'err = current.prepareLocked(ctx)\n\tif err != nil {')
  change('go-runtime/internal/egressexec/cellular.go', 'lease.ExpiresAt.Before(expiresAt.Add(-time.Second)) {', 'lease.State != "ready" || lease.ExpiresAt.Before(expiresAt.Add(-time.Second)) {')
  change('go-runtime/internal/linuxmodem/data_linux.go', 'if (!claim.bearerDisconnected || !claim.inhibited) && claim.commandSnapshot.ObjectPath != "" {', 'if claim.bearer.Method == 3 && (!claim.bearerDisconnected || !claim.inhibited) {')
  change('go-runtime/internal/linuxmodem/prober_linux.go', 'supported && !claim.inhibited && claim.commandSnapshot.ObjectPath != ""', 'supported && !claim.cleanup && claim.commandSnapshot.ObjectPath != ""')
  // Old ReleaseStale refuses any retained borrower; restore precisely that
  // refusal at the new recovery entry instead of testing a nonexistent symbol.
  change('go-runtime/internal/agentconnection/manager.go', '// Retry partial Prepare rollback as well as the old connected claim.', 'if current.borrowed { return agentdata.ErrSessionActive }\n\t// Retry partial Prepare rollback as well as the old connected claim.')
  const result = spawnSync('go', ['-C', 'go-runtime', 'test', '-json', '-count=1', '-timeout=2m', '-run', `^(${cases.map(c=>c[1]).join('|')})$`, ...[...new Set(cases.map(c=>'./'+c[0]))]], {encoding:'utf8',maxBuffer:16*1024*1024})
  const log = result.stdout + result.stderr
  fs.writeFileSync(path.join(records,'red.jsonl'),log)
  if (result.status !== 1 || /build failed|DATA RACE|panic:|fatal error:/.test(log)) throw new Error('red must be behavioral failures, not compile/infra/panic')
  const events = result.stdout.trim().split('\n').map(line=>JSON.parse(line))
  for (const [,name,reason] of cases) {
    if (!events.some(e=>e.Action==='fail'&&e.Test===name) || !events.filter(e=>e.Test===name).map(e=>e.Output||'').join('').includes(reason)) throw new Error(`missing red: ${name}`)
  }
  console.log('All seven original-behavior counterexamples failed for the expected reason.')
} finally {
  for (const [name,source] of patches) fs.writeFileSync(name,source)
}
const green=spawnSync('go',['-C','go-runtime','test','-json','-race','-count=1','-timeout=3m',...['agentdata','agentcall','agentconnection','agentpolicy','agenthost','cellulardata','egressexec','linuxmodem'].map(p=>'./internal/'+p)],{encoding:'utf8',maxBuffer:32*1024*1024})
fs.writeFileSync(path.join(records,'green.jsonl'),green.stdout+green.stderr)
if(green.status!==0) { process.stdout.write(green.stdout); process.stderr.write(green.stderr); process.exit(1) }
console.log('Relevant packages pass with the production fixes restored; full normal CI remains separate.')
