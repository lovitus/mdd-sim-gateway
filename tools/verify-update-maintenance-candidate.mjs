// Temporary hosted-only withdrawal proof. Removed after the qualified tag.
import assert from 'node:assert/strict'
import {readFileSync,writeFileSync,mkdirSync} from 'node:fs'
import {execFileSync,spawnSync} from 'node:child_process'
import path from 'node:path'
assert.equal(process.env.GITHUB_ACTIONS,'true','GitHub-hosted validation only')
const root=process.cwd(), output=path.join(process.env.RUNNER_TEMP,'update-maintenance-proof')
mkdirSync(output,{recursive:true})
const saved=new Map()
function change(file,before,after){
  const filename=path.join(root,file),source=readFileSync(filename,'utf8')
  if(!saved.has(filename))saved.set(filename,source)
  assert.ok(source.includes(before),`withdrawal source moved: ${file}`)
  writeFileSync(filename,source.replaceAll(before,after))
}
function restore(){for(const[file,source]of saved)writeFileSync(file,source);saved.clear()}
function run(name,command,args,marker,cwd=root,env={}){
  const result=spawnSync(command,args,{cwd,env:{...process.env,...env},encoding:'utf8',timeout:300000,maxBuffer:16*1024*1024})
  const log=(result.stdout||'')+(result.stderr||'')
  writeFileSync(path.join(output,`${name}.log`),log)
  assert.ok(!result.error,`${name}: ${result.error}`)
  if(marker){
    assert.notEqual(result.status,0,`${name}: withdrawal unexpectedly passed`)
    assert.ok(!/build failed|undefined:|syntax error|panic:|DATA RACE|Test timed out|TimeoutError/.test(log),`${name}: failure was not the behavioral assertion`)
    assert.match(log,marker,`${name}: intended assertion not reached`)
  }else assert.equal(result.status,0,`${name}: see retained log`)
  console.log(`${name}: ${marker?'compiled/behavioral red':'pass'}`)
}
function goRed(name,pkg,test,marker){run(name,'go',['-C','go-runtime','test','-count=1','-timeout=90s',pkg,'-run',test],marker)}
try{
  change('go-runtime/internal/systemupdate/admission.go','gate.lease = record.LeaseID','// withdrawn: local lease was not restored')
  change('go-runtime/internal/systemupdate/maintenance.go','if err := gate.freeze(record.LeaseID); err != nil {\n\t\t\treturn nil, err\n\t\t}','// withdrawn: replacement Core opened admission')
  goRed('restart-guard','./internal/systemupdate','^TestUpdateMaintenanceSurvivesCoreReplacementAndReleasesOnlyOriginalSet$',/RESTORED_GUARD/)
  restore()
  change('go-runtime/internal/systemupdate/admission.go',' || gate.active != 0','')
  change('go-runtime/internal/systemupdate/maintenance.go','if err := owner.runtime.Busy(); err != nil {\n\t\treturn fail(err)\n\t}','// withdrawn: only Provider drains checked')
  goRed('inflight-cellular-admission','./internal/systemupdate','^TestUpdateAdmissionRejectsInflightWritesAndBusyMediaWithoutFencingOldCalls$',/BUSY_ADMISSION/)
  restore()
  change('go-runtime/internal/systemupdate/store.go','if record != nil && (record.Phase != MaintenanceReleased || !found || status.OperationID == record.OperationID) {','if false {')
  goRed('status-authority','./internal/systemupdate','^TestUpdateStatusCannotHidePendingMaintenanceOrReleasedReceipt$',/PENDING_STATUS/)
  restore()
  change('go-runtime/internal/systemupdate/maintenance.go','record := &Maintenance{SchemaVersion: MaintenanceVersion, OperationID: operation, LeaseID: lease,','for _, provider := range providers { _ = owner.runtime.Provider(ctx, provider, lease, "begin") }\n\trecord := &Maintenance{SchemaVersion: MaintenanceVersion, OperationID: operation, LeaseID: lease,')
  goRed('persist-before-effect','./internal/systemupdate','^TestUpdateIdentityAndPersistenceGatePrecedeExternalEffects$/durable_write_failed',/PERSIST_BEFORE_DRAIN/)
  restore()
  change('go-runtime/internal/cellularmedia/service.go','if len(service.sessions) != 0 {','if false {')
  goRed('cellular-preparation','./internal/cellularmedia','^TestUpdateBusyIncludesPreparingReadyAndIncomingOwnership$',/CELLULAR_UPDATE_BUSY/)
  restore()
  change('go-runtime/internal/cellularmedia/service.go','if service.config.Admit != nil {','if false {')
  goRed('cellular-new-media','./internal/cellularmedia','^TestUpdateBlocksNewMediaBeforeResolvingHardware$',/MEDIA_ADMISSION/)
  restore()
  change('go-runtime/agentlink/server.go','if server.updateMaintenance != "" {','if false {')
  goRed('late-agent','./agentlink','^TestUpdateMaintenanceFencesNewAgentsButNotRefusedBusyAdmission$',/NEW_AGENT_RPC/)
  restore()
  change('go-runtime/internal/core/update_maintenance.go','UseExpected(ctx, expected,','UseExpected(ctx, func() mediaauth.ProviderFence { current, _ := runtime.Providers.CurrentProvider(expected.LineID); return current.Fence() }(),')
  goRed('provider-fence','./internal/core','^TestUpdateProviderCleanupKeepsExactGenerationCardAndLease$',/stale operation reached replacement HTTP endpoint/)
  restore()
  const binary=path.join(process.env.RUNNER_TEMP,'update-status.test')
  change('go-runtime/internal/systemupdate/store.go','if store.statusReaderGID >= 0 {','if false {')
  run('old-permission-build','go',['-C','go-runtime','test','-c','./internal/systemupdate','-o',binary])
  run('root-status-permission','sudo',[binary,'-test.run=^TestUpdateRootStatusReadableByCoreWithoutOpeningPrivateDirectory$','-test.v'],/update\.status\.json: permission denied/)
  restore()
  run('fixed-race','go',['-C','go-runtime','test','-race','-count=1','-timeout=3m','./internal/systemupdate','./internal/core','./internal/cellularmedia','./agentlink','./mediaauth','./providercontrol','./internal/runtimereconcile','./releasebundle','./cmd/mdd-updater'])
  run('fixed-permission-build','go',['-C','go-runtime','test','-c','./internal/systemupdate','-o',binary])
  run('fixed-root-status','sudo',[binary,'-test.run=^TestUpdateRootStatusReadableByCoreWithoutOpeningPrivateDirectory$','-test.v'])
  const app='webui/src/mdd/App.jsx',original=readFileSync(app,'utf8')
  saved.set(path.join(root,app),original)
  writeFileSync(app,execFileSync('git',['show','9a9aa783a5681ec203cd977b06c9582979dc7331:webui/src/mdd/App.jsx']))
  run('old-sidebar','npm',['run','test:update-entry-browser'],/SIDEBAR_UPDATE_ENTRY/,path.join(root,'webui'),{MDD_BROWSER_EVIDENCE:path.join(output,'old-browser')})
  restore()
  run('fixed-sidebar','npm',['run','test:update-entry-browser'],null,path.join(root,'webui'),{MDD_BROWSER_EVIDENCE:path.join(output,'fixed-browser')})
}finally{restore()}
