// Temporary hosted-only counterexamples; removed before the final candidate.
import {execFileSync,spawnSync} from 'node:child_process'
import {mkdirSync,readFileSync,writeFileSync} from 'node:fs'
const base='cda6b03', evidence='reader-sim-evidence'
const failures=[]
mkdirSync(evidence,{recursive:true})
const run=(name,command,args,redMarker)=>{
  const web=command==='node' && args[0].startsWith('webui/')
  const result=spawnSync(command,web?[args[0].slice(6),...args.slice(1)]:args,{cwd:web?'webui':'.',encoding:'utf8',timeout:180000,env:process.env,maxBuffer:32*1024*1024})
  const log=(result.stdout||'')+(result.stderr||'')
  writeFileSync(`${evidence}/${name}.log`,log)
  if(result.error)throw result.error
  if(redMarker ? result.status===0 || !log.includes(redMarker) : result.status!==0){failures.push(`${name}: unexpected result ${result.status}\n${log}`);return}
  console.log(`${name}: ${redMarker?'behavioral failure confirmed':'passed'}`)
}
const withFiles=(changes,action)=>{
  const saved=changes.map(([file])=>[file,readFileSync(file,'utf8')])
  try {for(const [file,transform] of changes)writeFileSync(file,transform(readFileSync(file,'utf8')));action()}
  finally {for(const [file,text] of saved)writeFileSync(file,text)}
}
const original=file=>[file,()=>execFileSync('git',['show',`${base}:${file}`],{encoding:'utf8'})]
const replace=(file,before,after)=>[file,text=>{if(!text.includes(before))throw new Error(`mutation missing in ${file}`);return text.replace(before,after)}]
const go=(pkg,test)=>['-C','go-runtime','test','-count=1','-timeout','2m',pkg,'-run',test]
withFiles([original('go-runtime/internal/agentsim/session.go')],()=>
  run('red-sim-independent','go',go('./internal/agentsim','^TestReaderSIMIdentitySkipsSecureElementProbeWithoutWeakeningContinuity/scoped$'),'SIM_SCOPE_INDEPENDENT'))
withFiles([original('go-runtime/internal/core/reader_provision.go'),original('go-runtime/internal/core/reader_readback.go')],()=>
  run('red-original-cause','go',go('./internal/core','^TestReaderReadbackOutcomePreservesOriginalCauseAndDispatchCertainty/(provision|readback)/unknown$'),'READBACK_CAUSE'))
withFiles([replace('go-runtime/agentlink/reader_readback.go','response.Scope != request.Scope || ','')],()=>
  run('red-response-scope','go',go('./agentlink','^TestReaderSIMIdentityResponseScopeAndInnerIdentity$'),'scope echo missing'))
withFiles([replace('go-runtime/agentlink/server.go',
  '\tif request.Scope == ReaderReadbackSIMIdentity && !featureEnabled(strings.Join(connection.capabilities, ","), readerSIMIdentityFeature) {\n\t\treturn ReaderReadbackResponse{}, ErrReaderSIMIdentityUnsupported\n\t}\n','')],()=>
  run('red-legacy-dispatch','go',go('./agentlink','^TestReaderSIMIdentityNegotiationDoesNotDispatchToLegacyAgent/legacy$'),'SIM_SCOPE_DISPATCH'))
withFiles([['go-runtime/agentlink/client.go',text=>text.replaceAll('Scope:       request.Scope,','')]],()=>
  run('red-synthetic-scope','go',go('./agentlink','^TestReaderSIMIdentitySyntheticFailuresEchoScope$'),'invalid synthetic result'))
withFiles([['go-runtime/agentlink/client.go',text=>{
  const start=text.indexOf('\tif message.Kind == kindReaderReadbackRequest {',text.indexOf('func (client Client) writeOverload'))
  const end=text.indexOf('\tif message.Kind == kindPolicyRequest {',start)
  if(start<0||end<start)throw new Error('readback overload mutation missing')
  return text.slice(0,start)+text.slice(end)
}]],()=>run('red-readback-overload','go',go('./agentlink','^TestReaderSIMIdentityNegotiationDoesNotDispatchToLegacyAgent/scoped$'),'invalid readback overload:'))
withFiles([original('webui/src/api.js'),original('webui/src/mdd/lineAdapter.js'),original('webui/src/mdd/provisionRecovery.js')],()=>
  run('red-detect-adapter','node',['webui/tests/mddLineAdapter.mjs'],'SIM_SCOPE_DETECT'))
withFiles([original('webui/src/mdd/esimAdapter.js')],()=>
  run('red-esim-scope','node',['webui/tests/mddEsimAdapter.mjs'],'Missing expected rejection'))
withFiles([original('webui/src/mdd/views/ReaderSetupActions.jsx')],()=>
  run('red-current-card','node',['webui/tests/readerSetupBrowser.mjs'],'SIM_SCOPE_PROVISION'))
run('green-go','go',['-C','go-runtime','test','-race','-count=1','-timeout','3m','./agentlink','./internal/agentsim','./internal/core'])
run('green-line-adapter','node',['webui/tests/mddLineAdapter.mjs'])
run('green-esim-adapter','node',['webui/tests/mddEsimAdapter.mjs'])
run('green-current-card','node',['webui/tests/readerSetupBrowser.mjs'])
run('green-adjacent-editor','node',['webui/tests/newCardBrowser.mjs'])
if(failures.length)throw new Error(failures.join('\n'))
