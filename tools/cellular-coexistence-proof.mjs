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
// Original seven reds already qualified in run42153737616; do not replay them.
const cases = [
  ['internal/agentdata','TestCoexistenceInvalidDataDoesNotCancelVoiceOrLease','invalid data blocked voice'],
  ['internal/agentdata','TestCoexistencePartialRecoveryRetainsDebtWhenObservationIsUnknown','partial recovery debt stranded'],
  ['internal/agentdata','TestCoexistenceSlowOpenDoesNotDelayVoiceAndStopRejectsLateSocket','slow data dial blocked voice admission'],
  ['internal/agentconnection','TestCoexistenceConnectionRecoveryRetainsInvalidatedAndPartialOwners','invalidated owner could not attempt recovery'],
  ['internal/linuxmodem','TestCoexistenceCleanupRequiresExactVoiceInventoryWhenPathMissing','missing voice authority allowed ownership change'],
  ['internal/agentpolicy','TestCoexistenceTransportCloseRetainsCleanupUntilVoiceEnds','transport loss stranded cleanup ownership'],
  ['internal/agenthost','TestCoexistenceWorkerAnswersWithDurableIncomingFenceAndBorrower','worker borrower blocked answer'],
]
const baseline='b5db829a32137a6bbc0f97fa1e6676378ada7a68'
const fetched=spawnSync('git',['fetch','--depth=1','origin',baseline],{encoding:'utf8'})
if(fetched.status!==0) throw new Error('cannot obtain fixed review baseline: '+fetched.stderr)
try {
  for(const file of ['agentdata/manager.go','agentconnection/manager.go','agentpolicy/manager.go','linuxmodem/data_linux.go']) {
    const relative='go-runtime/internal/'+file
    const name=path.join(root,relative)
    const prior=spawnSync('git',['show',baseline+':'+relative],{encoding:'utf8'})
    if(prior.status!==0) throw new Error('cannot read review baseline: '+relative)
    patches.set(name,fs.readFileSync(name,'utf8'))
    fs.writeFileSync(name,prior.stdout)
  }
  // Restore the previous worker admission while retaining every real call fence.
  change('go-runtime/internal/agenthost/worker.go','admit = executor.data.DoCall','admit = executor.data.DoAuxiliary')
  const result = spawnSync('go', ['-C', 'go-runtime', 'test', '-json', '-count=1', '-timeout=2m', '-run', `^(${cases.map(c=>c[1]).join('|')})$`, ...[...new Set(cases.map(c=>'./'+c[0]))]], {encoding:'utf8',maxBuffer:16*1024*1024})
  const log = result.stdout + result.stderr
  fs.writeFileSync(path.join(records,'red.jsonl'),log)
  if (result.status !== 1 || /build failed|DATA RACE|panic:|fatal error:/.test(log)) throw new Error('red must be behavioral failures, not compile/infra/panic')
  const events = result.stdout.trim().split('\n').map(line=>JSON.parse(line))
  for (const [,name,reason] of cases) {
    if (!events.some(e=>e.Action==='fail'&&e.Test===name) || !events.filter(e=>e.Test===name||e.Test?.startsWith(name+'/')).map(e=>e.Output||'').join('').includes(reason)) throw new Error(`missing red: ${name}`)
  }
  console.log('Seven review-defect counterexamples failed for the expected reason; original seven reds were not repeated.')
} finally {
  for (const [name,source] of patches) fs.writeFileSync(name,source)
}
const green=spawnSync('go',['-C','go-runtime','test','-json','-race','-count=1','-timeout=3m',...['agentdata','agentcall','agentconnection','agentpolicy','agenthost','cellulardata','egressexec','linuxmodem'].map(p=>'./internal/'+p)],{encoding:'utf8',maxBuffer:32*1024*1024})
fs.writeFileSync(path.join(records,'green.jsonl'),green.stdout+green.stderr)
if(green.status!==0) { process.stdout.write(green.stdout); process.stderr.write(green.stderr); process.exit(1) }
console.log('Relevant packages pass with the production fixes restored; full normal CI remains separate.')
