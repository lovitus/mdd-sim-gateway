// Temporary hosted-only behavioral counterexamples; removed after qualification.
import {readFileSync,writeFileSync,appendFileSync} from 'node:fs'
import {spawnSync,execFileSync} from 'node:child_process'
const log='esim-proof.log'
writeFileSync(log,'')
const run=(command,args,red,marker,env={})=>{
  const result=spawnSync(command,args,{encoding:'utf8',timeout:240000,maxBuffer:16*1024*1024,env:{...process.env,...env}})
  const output=(result.stdout||'')+(result.stderr||'')
  appendFileSync(log,`\n$ ${command} ${args.join(' ')}\n${output}\n`)
  process.stdout.write(output)
  if(result.error || (red ? result.status===0 || !output.includes(marker) || /build failed|undefined:|SyntaxError/.test(output) : result.status!==0))throw new Error(`invalid ${red?'red':'green'} evidence`)
}
const service='go-runtime/internal/euiccprofiles/service.go'
const transport='go-runtime/agentlink/server.go'
const page='webui/src/mdd/views/Esim.jsx'
const adapter='webui/src/mdd/esimAdapter.js'
const guard='go-runtime/internal/core/agent_restart_guard.go'
const mutation='go-runtime/internal/euiccprofiles/mutation_guard.go'
const drain='go-runtime/providerapply/drain.go'
const originals=new Map([service,transport,page,adapter,guard,mutation,drain].map(path=>[path,readFileSync(path,'utf8')]))
const base='9a9aa783a5681ec203cd977b06c9582979dc7331'
const reviewed='7e4534fb5d411566c9d620b1c5ad9747b6677bad'
try {
  // Withdraw the new guarded dispatcher while preserving signatures and test setup.
  writeFileSync(service,originals.get(service).replace('service.mutateProfile(request.Context(), command)','service.agents.ExecuteEUICCProfileCommand(request.Context(), command)'))
  run('go',['-C','go-runtime','test','-count=1','./internal/euiccprofiles','-run','TestProfileSwitchProtectsWholeEIDAndFrozenInsertion/active-other-disabled-line'],true,'unsafe switch: 202')
  writeFileSync(service,originals.get(service))
  writeFileSync(transport,execFileSync('git',['show',`${base}:${transport}`],{encoding:'utf8'}))
  run('go',['-C','go-runtime','test','-count=1','./agentlink','-run','TestEUICCMaintenanceAllowsOnlyFrozenCommand'],true,'protected dispatch:')
  writeFileSync(transport,originals.get(transport))
  for(const path of [page,adapter])writeFileSync(path,execFileSync('git',['show',`${base}:${path}`],{encoding:'utf8'}))
  run('node',['webui/tests/esimOperationsBrowser.mjs'],true,'ESIM_UNKNOWN_RETAINED')
  for(const path of [page,adapter])writeFileSync(path,execFileSync('git',['show',`${reviewed}:${path}`],{encoding:'utf8'}))
  for(const [scenario,marker] of [['late','ESIM_LATE'],['legacy','ESIM_LEGACY'],['empty','ESIM_EMPTY']]) {
    run('node',['webui/tests/esimOperationsBrowser.mjs'],true,marker,{MDD_ESIM_SCENARIO:scenario})
  }
  for(const path of [page,adapter])writeFileSync(path,originals.get(path))
  for(const path of [guard,mutation])writeFileSync(path,execFileSync('git',['show',`${reviewed}:${path}`],{encoding:'utf8'}))
  run('go',['-C','go-runtime','test','-count=1','./internal/core','-run','TestProfileCleanupUsesRealCoreGuardAfterCatalogChange'],true,'PROFILE_CLEANUP')
  run('go',['-C','go-runtime','test','-count=1','./internal/euiccprofiles','-run','TestProfileUnknownRetainsPermitUntilExactReadbackWithoutRewrite'],true,'PROFILE_READBACK')
  for(const path of [guard,mutation])writeFileSync(path,originals.get(path))
  writeFileSync(drain,originals.get(drain).replace('generation != "" && provider.Generation != generation','false'))
  run('go',['-C','go-runtime','test','-count=1','./providerapply','-run','TestMaintenanceDrainsAndResumesCurrentGeneration'],true,'PROFILE_RELEASE_TARGET')
} finally {for(const [path,text] of originals)writeFileSync(path,text)}
run('go',['-C','go-runtime','test','-race','-count=1','./agentlink','./internal/euiccprofiles','./internal/core','./providerapply'],false)
run('node',['webui/tests/mddEsimAdapter.mjs'],false)
run('node',['webui/tests/esimOperationsBrowser.mjs'],false)
