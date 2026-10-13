// Temporary GitHub-hosted behavioral proof; remove after qualification.
import {readFileSync,writeFileSync,appendFileSync} from 'node:fs'
import {spawnSync,execFileSync} from 'node:child_process'
const log='line-archive-proof.log'
writeFileSync(log,'')
function run(command,args,red=false,marker='') {
  const result=spawnSync(command,args,{encoding:'utf8',timeout:240000,maxBuffer:16*1024*1024})
  const output=(result.stdout||'')+(result.stderr||'')
  appendFileSync(log,`\n$ ${command} ${args.join(' ')}\n${output}\n`)
  process.stdout.write(output)
  if(result.error || (red ? result.status===0 || !output.includes(marker) || /build failed|undefined:|SyntaxError/.test(output) : result.status!==0)) throw new Error(`invalid ${red?'red':'green'} evidence`)
}
const base='9a9aa783a5681ec203cd977b06c9582979dc7331'
const store='go-runtime/internal/cellularmessages/store.go'
const handler='go-runtime/internal/linedeletion/http.go'
const backup='go-runtime/internal/linecatalog/backup.go'
const page='webui/src/mdd/views/UnifiedPages.jsx'
const originals=new Map([store,handler,backup,page].map(path=>[path,readFileSync(path,'utf8')]))
try {
  // Exact old deletion coordinator and purge body, with new symbols retained so
  // counterexamples reach behavior instead of failing to compile.
  writeFileSync(handler,execFileSync('git',['show',`${base}:${handler}`],{encoding:'utf8'}))
  const oldStore=execFileSync('git',['show',`${base}:${store}`],{encoding:'utf8'})
  const purge=oldStore.slice(oldStore.indexOf('func (store *OperationStore) PurgeLine'),oldStore.indexOf('\nfunc ',oldStore.indexOf('func (store *OperationStore) PurgeLine')+1))
  const current=originals.get(store),start=current.indexOf('func (store *OperationStore) PurgeLine'),end=current.indexOf('\nfunc ',start+1)
  if(!purge || start<0 || end<0) throw new Error('purge proof boundary missing')
  writeFileSync(store,current.slice(0,start)+purge+current.slice(end))
  run('go',['-C','go-runtime','test','-count=1','./internal/linedeletion','-run','TestPermanentDeletionCleansEveryDurableLineStore'],true,'UNRESOLVED_SMS_DELETION')
  writeFileSync(handler,originals.get(handler))
  // Withdraw the transaction guard without changing test setup or signatures.
  writeFileSync(store,current.replace('func (store *OperationStore) PrepareLineDeletion(lineID string, history SubmissionHistory) error {',
    'func (store *OperationStore) PrepareLineDeletion(lineID string, history SubmissionHistory) error {\n if true { return nil }'))
  run('go',['-C','go-runtime','test','-count=1','./internal/cellularmessages','-run','TestDeletionBarrierRequiresResolvedSMSAndExactDurableHistory'],true,'unresolved record accepted')
  run('go',['-C','go-runtime','test','-count=1','./internal/cellularmessages','-run','TestDeletionBarrierSerializesWithBeginAndSurvivesBackup'],true,'earlier Begin must block deletion')
  writeFileSync(store,current)
  writeFileSync(backup,originals.get(backup).replace('json.Marshal(result)','json.Marshal(result.Snapshot)'))
  run('go',['-C','go-runtime','test','-count=1','./internal/systembackup','-run','TestBackupRetainsCatalogRecoveryAndSMSBarriersWithoutCredentials'],true,'RECOVERY_EXPORT_INCOMPLETE')
  writeFileSync(backup,originals.get(backup))
  writeFileSync(page,execFileSync('git',['show',`${base}:${page}`],{encoding:'utf8'}))
  run('npm',['--prefix','webui','run','test:line-archive-browser'],true,'NO_DEVICE_RECYCLE')
} finally { for(const [path,source] of originals)writeFileSync(path,source) }
run('go',['-C','go-runtime','test','-race','-count=1','./internal/cellularmessages','./internal/linedeletion','./internal/linecatalog','./internal/systembackup'])
run('npm',['--prefix','webui','run','test:line-archive-browser'])
