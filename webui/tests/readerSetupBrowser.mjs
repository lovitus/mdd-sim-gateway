import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
import { createServer } from 'vite'
import { chromium } from 'playwright-core'

const output=process.env.MDD_BROWSER_EVIDENCE || 'browser-evidence/reader-setup'
await mkdir(output,{recursive:true})
const vite=await createServer({server:{host:'127.0.0.1',port:0}})
const card='8944100000000002420', sim={imsi:'234100000002420',mcc:'234',mnc:'10',smsc:'+441234567890',imei:'123456789012347'}
const other={schema_version:1,id:'other-line',card_id:'8944100000000009999',name:'Other SIM',enabled:false,sim:{...sim,imsi:'234100000009999'},network:{egress_country:'gb'}}
let line=null, revision=2, appliedRevision=1, applied=false, desired=true, running=false, session='reader-session', rejectScoped=true
let unknownInitOnce=true, loseWideApplyOnce=true
let hardwareReads=0, providerUnavailable=false, holdLifecycle=false, releaseLifecycle, lifecycleSeen
let projectionUnavailable=false, lastApplyID=''
let rejectFirstSave=true
let rejectInitialization=''
let holdCandidate=false, releaseCandidate, candidateSeen
let holdSave=false, releaseSave, saveSeen
let holdWideApply=false, releaseApply, applySeen
let releaseFirstSave, firstSaveSeen
const firstSaveStarted=new Promise(resolve=>{firstSaveSeen=resolve})
let lifecycleStarted=new Promise(resolve=>{lifecycleSeen=resolve})
const requests=[], receipts=new Map(), errors=[]
const projection=()=>({line_id:'new-line',facts:[
  {layer:'vowifi_intent',fresh:true,available:desired,condition:desired?'ready':'inactive',code:desired?'vowifi_enabled':'vowifi_disabled'},
  {layer:'engine_process',fresh:true,available:applied,condition:applied?'ready':'unknown',code:applied?'provider_reachable':'provider_unavailable'},
  {layer:'vowifi_runtime',fresh:true,available:running,condition:running?'ready':'inactive',code:running?'runtime_running':'runtime_stopped'},
]})
let browser,page
try{
  await vite.listen();browser=await chromium.launch({channel:'chrome',headless:true});page=await browser.newPage({viewport:{width:1200,height:1000}})
  page.on('pageerror',error=>errors.push(error.message))
  await page.route('**/v1/**',async route=>{
    const request=route.request(),path=new URL(request.url()).pathname,method=request.method()
    const body=method==='POST'||method==='PUT' ? request.postDataJSON() : null
    if(method!=='GET')requests.push({path,body})
    if(path==='/v1/catalog/lines' && method==='GET')return route.fulfill({json:{revision,lines:[other,...(line?[line]:[])]}})
    if(path==='/v1/lines')return route.fulfill({json:{lines:line?[projection()]:[]}})
    if(path==='/v1/devices')return route.fulfill({json:{devices:[{id:'setup-reader',kind:'reader',agent_id:'agent',process_generation:'process',
      reader:{reader_name:'Setup reader',card_id:card,card_present:true,session_generation:session,sim:{...sim}},
      endpoints:line?[{association:'exact',operation_candidate:true,card_ids:[card],line:{id:line.id}}]:[]}]}})
    if(path==='/v1/agents')return route.fulfill({json:{agents:[]}})
    if(path==='/v1/egress/exits')return route.fulfill({json:{exits:[]}})
    if(path==='/v1/imei-pool')return route.fulfill({json:{revision:1,catalog_revision:revision,
      entries:[{id:'phone',name:'Test phone',imei:sim.imei}],bindings:[{card_id:card,entry_id:'phone',imei:sim.imei}],unpooled:[]}})
    if(path==='/v1/line-candidates'){
      if(holdCandidate){candidateSeen();await new Promise(resolve=>{releaseCandidate=resolve})}
      return route.fulfill({json:{catalog_revision:revision,candidates:[{candidate_id:'candidate',card_id:card,
      agent_id:'agent',process_generation:'process',reader_name:'Setup reader',session_generation:session,can_claim:!line,
      configured_line_id:line?.id,observed:sim}]}})
    }
    if(path==='/v1/line-candidates/candidate/claim'){
      assert.equal(line,null,'must claim only once');assert.equal(request.headers()['if-match'],`"${revision}"`)
      line={schema_version:1,id:'new-line',card_id:card,name:body.name,enabled:false,hardware_provision_state:'draft',sim:{...sim},network:{egress_country:'gb'}}
      return route.fulfill({json:{line,revision:++revision}})
    }
    if(path==='/v1/catalog/lines/new-line' && method==='PUT'){
      assert.equal(request.headers()['if-match'],`"${revision}"`);assert.equal(body.card_id,card)
      if(rejectFirstSave){
        rejectFirstSave=false;firstSaveSeen()
        await new Promise(resolve=>{releaseFirstSave=resolve})
        return route.fulfill({status:503,json:{code:'catalog_write_failed'}})
      }
      if(holdSave){saveSeen();await new Promise(resolve=>{releaseSave=resolve})}
      line=body;return route.fulfill({json:{line,revision:++revision}})
    }
    if(path==='/v1/catalog/lines/other-line' && method==='PUT'){
      assert.equal(request.headers()['if-match'],`"${revision}"`)
      if(holdLifecycle){lifecycleSeen();await new Promise(resolve=>{releaseLifecycle=resolve})}
      Object.assign(other,body);return route.fulfill({json:{line:other,revision:++revision}})
    }
    if(path==='/v1/readers/provision'){
      if(rejectInitialization){const code=rejectInitialization;rejectInitialization='';return route.fulfill({status:409,json:{code}})}
      if(body.sim_session_generation!==session)return route.fulfill({status:409,json:{code:'reader_provision_target_unavailable'}})
      if(body.expected_catalog_revision!==revision)return route.fulfill({status:409,json:{code:'reader_provision_requires_disabled_draft'}})
      assert.equal(body.expected_catalog_revision,revision);assert.equal(body.card_id,card);assert.equal(body.sim_session_generation,session)
      assert.equal(request.headers()['x-mdd-agent-id'],'agent');assert.equal(line.enabled,false)
      ++hardwareReads
      if(unknownInitOnce){
        unknownInitOnce=false
        receipts.set(body.operation_id,{operation_id:body.operation_id,line_id:line.id,state:'unknown'})
        return route.fulfill({status:202,json:{schema_version:1,operation_id:body.operation_id,state:'unknown',error_code:'reader_provision_unconfirmed'}})
      }
      line.hardware_provision_state='provisioned';++revision
      receipts.set(body.operation_id,{operation_id:body.operation_id,line_id:line.id,state:'succeeded'})
      return route.fulfill({json:{schema_version:1,operation_id:body.operation_id,state:'succeeded',catalog_revision:revision,line}})
    }
    if(path.startsWith('/v1/operations/'))return route.fulfill({json:receipts.get(path.split('/').at(-1))})
    if(path==='/v1/system/provider-config'){
      if(method==='GET')return route.fulfill({json:{catalog_revision:revision,applied_revision:appliedRevision,pending:revision!==appliedRevision,applying:false,last_apply_id:lastApplyID,last_state:lastApplyID?'applied':''}})
      assert.equal(body.catalog_revision,revision)
      if(body.only_add_line_id && rejectScoped)return route.fulfill({status:409,json:{code:'provider_apply_blocked',detail:'provider apply plan is blocked: unrelated_provider_changes_pending'}})
      applied=line.enabled;appliedRevision=revision;lastApplyID=`apply-${requests.length}`
      if(!applied)running=false
      if(!body.only_add_line_id && holdWideApply){applySeen();await new Promise(resolve=>{releaseApply=resolve})}
      if(!body.only_add_line_id && loseWideApplyOnce){loseWideApplyOnce=false;return route.abort('failed')}
      return route.fulfill({json:{state:'applied',catalog_revision:revision,apply_id:'apply-fixture'}})
    }
    if(path==='/v1/lines/new-line')return projectionUnavailable
      ? route.fulfill({status:503,json:{code:'projection_unavailable'}}) : route.fulfill({json:projection()})
    if(path==='/v1/lines/new-line/vowifi/status')return providerUnavailable || !applied
      ? route.fulfill({status:503,json:{code:'provider_unavailable'}})
      : route.fulfill({json:{line_id:line.id,process_generation:'provider',maintenance:{draining:false},runtime:{condition:running?'running':'stopped'}}})
    if(path.endsWith('/recovery'))return route.fulfill({json:{line_id:'new-line'}})
    if(path.endsWith('/availability'))return route.fulfill({json:{segments:[],summary:{}}})
    if(path==='/v1/lines/new-line/vowifi/runtime/stop' || path==='/v1/lines/new-line/vowifi/runtime/start'){
      assert.equal(body.expected_card_id,card)
      desired=path.endsWith('/start')
      if(desired)assert.ok(line.enabled && applied,'start cannot precede initialization/application')
      running=desired
      return route.fulfill({json:{accepted:true,code:desired?'started':'stopped'}})
    }
    errors.push(`${method} ${path}`);return route.fulfill({status:500,json:{code:'unexpected_fixture_request'}})
  })
  await page.goto(vite.resolvedUrls.local[0]+'tests/hardwareImei.html?setup')
  await page.getByPlaceholder('Telus').waitFor()
  const setup=page.getByLabel('Current SIM setup',{exact:true})
  const settle=()=>page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))))
  const idle=()=>page.waitForFunction(()=>{
    const button=[...document.querySelectorAll('button')].find(item=>item.textContent==='Save configuration')
    return button && !button.disabled && !button.closest('fieldset[disabled]')
  })
  const confirm=()=>page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click()
  const details=async()=>{if(await setup.locator('details').getAttribute('open')===null)await setup.locator('summary').click()}
  const read=async()=>{await details();await setup.getByRole('button',{name:'Read current state',exact:true}).first().click();await idle()}
  const reopen=async()=>{
    await page.getByRole('button',{name:'Leave devices',exact:true}).click()
    await page.getByRole('button',{name:'Return to devices',exact:true}).click();await idle()
  }
  // Behavioral red: the old mounted page has no single save-and-enable action.
  assert.equal(await setup.getByRole('button',{name:'Save and enable VoWiFi',exact:true}).count(),1,'SETUP_ACTIVATION: current card needs one save-and-enable action')
  assert.equal(await setup.getByRole('button',{name:'Save configuration',exact:true}).count(),1)
  assert.match(await page.locator('.u-device-option').first().innerText(),/ICCID \.\.\.2420.*agent/s)
  for(const action of ['cancel','leave']){
    const candidateStarted=new Promise(resolve=>{candidateSeen=resolve})
    holdCandidate=true
    await setup.getByRole('button',{name:'Save and enable VoWiFi',exact:true}).click();await confirm();await candidateStarted
    if(action==='cancel'){
      assert.equal(await setup.getByRole('button',{name:'Cancel remaining steps',exact:true}).isEnabled(),true,'SETUP_CANCEL_AVAILABLE: cancellation must remain operable during Save')
      await setup.getByRole('button',{name:'Cancel remaining steps',exact:true}).click()
    }
    else await page.getByRole('button',{name:'Leave devices',exact:true}).click()
    const candidateResponse=page.waitForResponse(response=>new URL(response.url()).pathname==='/v1/line-candidates')
    holdCandidate=false;releaseCandidate();await candidateResponse;await settle()
    assert.equal(requests.filter(item=>item.path==='/v1/line-candidates/candidate/claim').length,0,'cancel/navigation during candidate read prevents a later claim')
    if(action==='leave')await page.getByRole('button',{name:'Return to devices',exact:true}).click()
    else await reopen()
    await idle()
  }
  await page.getByRole('tab',{name:'Manage saved lines',exact:true}).click()
  await page.locator('.u-saved-lines select').selectOption('other-line')
  await page.getByPlaceholder('Telus').fill('Do not write another card')
  await page.locator('.u-saved-lines select').selectOption('other-line')
  const unchangedSnapshot=page.waitForResponse(response=>new URL(response.url()).pathname==='/v1/devices')
  await page.getByRole('button',{name:'Refresh fixture',exact:true}).click();await unchangedSnapshot;await settle()
  assert.equal(await page.getByPlaceholder('Telus').inputValue(),'Do not write another card')
  await page.locator('.u-saved-lines select').selectOption('')
  assert.equal(await page.getByPlaceholder('Telus').inputValue(),'')
  await page.getByRole('tab',{name:'Current SIM',exact:true}).click();await idle()
  await page.getByPlaceholder('Telus').fill('Current SIM A')
  await page.getByPlaceholder('ims',{exact:true}).fill('edited.ims')
  await setup.getByRole('button',{name:'Save configuration',exact:true}).click();await firstSaveStarted
  const pendingClaim=page.waitForResponse(response=>new URL(response.url()).pathname==='/v1/devices')
  await page.getByRole('button',{name:'Refresh fixture',exact:true}).click();await pendingClaim;await settle()
  assert.equal(await page.getByPlaceholder('ims',{exact:true}).inputValue(),'edited.ims','own claim publication preserves edits while Save is pending')
  releaseFirstSave()
  await page.getByRole('dialog').getByRole('button',{name:'OK',exact:true}).click();await idle()
  assert.equal(await page.getByPlaceholder('ims',{exact:true}).inputValue(),'edited.ims','partial Save preserves unsaved fields')
  assert.equal(await page.getByLabel('Selected line').innerText(),'new-line')
  assert.equal(requests.filter(item=>item.path==='/v1/line-candidates/candidate/claim').length,1)
  assert.equal(requests.filter(item=>item.path==='/v1/catalog/lines/new-line').length,1)
  assert.equal(requests.some(item=>item.path==='/v1/readers/provision' || item.path==='/v1/system/provider-config' || item.path.endsWith('/runtime/start')),false)
  await setup.getByRole('button',{name:'Save configuration',exact:true}).click();await idle()
  assert.equal(line.network.ims_apn,'edited.ims')
  assert.equal(line.enabled,false);assert.equal(line.hardware_provision_state,'draft');assert.equal(desired,true)
  assert.equal(requests.filter(item=>item.path==='/v1/line-candidates/candidate/claim').length,1)
  assert.equal(await page.getByLabel('Enabled in Provider catalog',{exact:true}).count(),0)
  assert.equal(other.name,'Other SIM')
  for(const interruption of ['cancel','newer-stop']){
    const saveStarted=new Promise(resolve=>{saveSeen=resolve})
    holdSave=true
    await page.getByPlaceholder('Telus').fill('Saved through '+interruption)
    await setup.getByRole('button',{name:'Save and enable VoWiFi',exact:true}).click();await confirm();await saveStarted
    if(interruption==='cancel')await setup.getByRole('button',{name:'Cancel remaining steps',exact:true}).click()
    else desired=false
    holdSave=false;releaseSave();await idle()
    assert.equal(line.name,'Saved through '+interruption)
    assert.equal(await page.getByPlaceholder('Telus').inputValue(),line.name,'admitted Save result is preserved after cancellation')
    assert.equal(line.enabled,false);assert.equal(hardwareReads,0,'SETUP_NEWER_STOP: preparation must stop before hardware or enable after the observed newer Stop')
    assert.equal(requests.filter(item=>item.path.endsWith('/runtime/start')).length,0)
    if(interruption==='newer-stop')await setup.getByRole('alert').filter({hasText:'A newer stop request'}).waitFor()
  }
  desired=true;await read()
  await page.getByRole('button',{name:'VoWiFi',exact:true}).click()
  await page.getByRole('button',{name:'Open SIM configuration',exact:true}).click()
  await page.getByRole('button',{name:'Status',exact:true}).click()
  await page.getByRole('button',{name:'Configure SIM',exact:true}).click();await idle()
  await setup.getByRole('button',{name:'Enable VoWiFi',exact:true}).click()
  session='reinserted-session'
  const refreshed=page.waitForResponse(response=>new URL(response.url()).pathname==='/v1/devices')
  await page.evaluate(()=>[...document.querySelectorAll('button')].find(button=>button.textContent==='Refresh fixture').click())
  await refreshed;await settle();await confirm();await idle()
  assert.equal(hardwareReads,0,'changed attachment invalidates open activation consent')
  ++revision
  await setup.getByRole('button',{name:'Enable VoWiFi',exact:true}).click();await confirm();await idle()
  await setup.getByRole('alert').filter({hasText:'Configuration changed elsewhere'}).waitFor()
  assert.equal(hardwareReads,0,'cannot silently rebase to a newer catalog')
  await details();await setup.getByRole('button',{name:'Reload configuration',exact:true}).click();await idle()
  for(const code of ['reader_provision_requires_disabled_draft','reader_provision_target_unavailable']){
    rejectInitialization=code
    await setup.getByRole('button',{name:'Enable VoWiFi',exact:true}).click();await confirm();await idle()
    assert.equal(await setup.getByRole('button',{name:'Check original result',exact:true}).count(),0)
    const retained=await page.evaluate(()=>Object.entries(sessionStorage).filter(([key])=>key.startsWith('mdd_provision_recovery_')).map(([,value])=>JSON.parse(value)))
    assert.ok(retained.some(value=>value.result?.rejected===code),'pre-dispatch rejection is archived')
    assert.equal(hardwareReads,0)
  }
  await setup.getByRole('button',{name:'Enable VoWiFi',exact:true}).click();await confirm();await idle()
  assert.equal(hardwareReads,1);assert.equal(line.enabled,false)
  await setup.getByRole('button',{name:'Check original result',exact:true}).click();await idle()
  await setup.getByRole('button',{name:'New read-only SIM verification',exact:true}).waitFor()
  const originalUnknown=[...receipts.keys()][0]
  await page.getByRole('tab',{name:'Manage saved lines',exact:true}).click()
  await page.locator('.u-saved-lines select').selectOption('new-line')
  await page.getByRole('button',{name:'Check original result',exact:true}).click()
  assert.equal(requests.filter(item=>item.path==='/v1/provision/reconcile').length,0)
  assert.equal(await page.evaluate(()=>JSON.parse(sessionStorage.getItem('mdd_provision_recovery_new-line')).operation_id),originalUnknown)
  await page.getByRole('tab',{name:'Current SIM',exact:true}).click();await idle()
  await setup.getByRole('button',{name:'Check original result',exact:true}).click();await idle()
  await setup.getByRole('button',{name:'New read-only SIM verification',exact:true}).click();await confirm();await idle()
  assert.equal(line.enabled,false,'recovery verification does not silently resume activation')
  // Saved-line lifecycle retains both pre-dispatch and late-response ownership.
  await page.getByRole('tab',{name:'Manage saved lines',exact:true}).click()
  await page.locator('.u-saved-lines select').selectOption('other-line')
  await page.getByRole('button',{name:'Enable line',exact:true}).click()
  await page.evaluate(()=>[...document.querySelectorAll('button')].find(button=>button.textContent==='Leave devices').click())
  await confirm()
  assert.equal(requests.filter(item=>item.path==='/v1/catalog/lines/other-line').length,0)
  await page.getByRole('button',{name:'Return to devices',exact:true}).click();await idle()
  await page.getByRole('tab',{name:'Manage saved lines',exact:true}).click()
  await page.locator('.u-saved-lines select').selectOption('other-line')
  holdLifecycle=true
  await page.getByRole('button',{name:'Enable line',exact:true}).click();await confirm();await lifecycleStarted
  await page.getByRole('button',{name:'Leave devices',exact:true}).click()
  const lifecycleResponse=page.waitForResponse(response=>new URL(response.url()).pathname==='/v1/catalog/lines/other-line')
  releaseLifecycle();await lifecycleResponse;await settle()
  assert.equal(await page.getByLabel('Selected line').innerText(),'none')
  await page.getByRole('button',{name:'Return to devices',exact:true}).click();await idle()
  await details();await setup.getByRole('button',{name:'Cancel pending start',exact:true}).click();await confirm();await idle()
  assert.equal(desired,false)
  await setup.getByRole('button',{name:'Enable VoWiFi',exact:true}).click();await confirm();await idle()
  assert.equal(line.enabled,true);assert.equal(running,false)
  await setup.getByRole('alert').filter({hasText:'unrelated_provider_changes_pending'}).waitFor()
  assert.equal(requests.filter(item=>item.path==='/v1/system/provider-config').length,1)
  await setup.getByRole('button',{name:'Review pending configuration and enable',exact:true}).click()
  await page.getByRole('dialog').getByRole('button',{name:'Cancel',exact:true}).click();await idle()
  assert.equal(requests.filter(item=>item.path==='/v1/system/provider-config').length,1)
  await setup.getByRole('button',{name:'Review pending configuration and enable',exact:true}).click();await confirm();await idle()
  await setup.getByRole('alert').filter({hasText:/fetch/i}).waitFor()
  assert.equal(requests.filter(item=>item.path==='/v1/system/provider-config').length,2)
  assert.equal(requests.filter(item=>item.path.endsWith('/runtime/start')).length,0,'lost apply response stops activation')
  providerUnavailable=true
  await page.getByRole('tab',{name:'Manage saved lines',exact:true}).click()
  await page.locator('.u-saved-lines select').selectOption('new-line')
  assert.equal(await page.getByRole('button',{name:'Apply saved catalog',exact:true}).isEnabled(),false)
  await page.getByRole('button',{name:'Read current state',exact:true}).click()
  await page.getByRole('status').filter({hasText:'provider_unavailable'}).waitFor()
  assert.ok(await page.evaluate(()=>sessionStorage.getItem('mdd_provider_apply_pending')))
  await page.getByRole('tab',{name:'Current SIM',exact:true}).click();await idle();await read()
  assert.ok(await page.evaluate(()=>sessionStorage.getItem('mdd_provider_apply_pending')))
  providerUnavailable=false;await read()
  assert.equal(await page.evaluate(()=>sessionStorage.getItem('mdd_provider_apply_pending')),null)
  assert.equal(requests.filter(item=>item.path.endsWith('/runtime/start')).length,0,'readback does not resume cancelled or interrupted activation')
  projectionUnavailable=true
  await setup.getByRole('button',{name:'Enable VoWiFi',exact:true}).click();await confirm();await idle()
  await setup.getByRole('alert').filter({hasText:'projection_unavailable'}).waitFor()
  assert.equal(requests.filter(item=>item.path.endsWith('/runtime/start')).length,0)
  projectionUnavailable=false;await read()
  await setup.getByRole('button',{name:'Enable VoWiFi',exact:true}).click();await confirm();await idle()
  await setup.getByText('VoWiFi runtime running',{exact:true}).waitFor()
  assert.equal(requests.filter(item=>item.path.endsWith('/runtime/start')).length,1)
  await page.screenshot({path:output+'/completed-reader-journey.png',fullPage:true})
  ++revision;await reopen()
  assert.equal(await setup.getByRole('button',{name:'Enable VoWiFi',exact:true}).count(),0,'unrelated pending edits do not restart a running line')
  await details();await setup.getByRole('button',{name:'Disable line',exact:true}).click();await confirm();await idle()
  assert.equal(line.enabled,false)
  await page.getByRole('tab',{name:'Manage saved lines',exact:true}).click()
  await page.locator('.u-saved-lines select').selectOption('new-line')
  loseWideApplyOnce=true
  await page.getByRole('button',{name:'Apply saved catalog',exact:true}).click();await confirm()
  await page.getByRole('status').filter({hasText:/fetch/i}).waitFor()
  assert.equal(applied,false);assert.equal(running,false)
  const applies=requests.filter(item=>item.path==='/v1/system/provider-config').length
  await page.getByRole('button',{name:'Read current state',exact:true}).click()
  await page.getByRole('status').filter({hasText:'Current Provider state confirmed'}).waitFor()
  assert.equal(await page.evaluate(()=>sessionStorage.getItem('mdd_provider_apply_pending')),null)
  assert.equal(requests.filter(item=>item.path==='/v1/system/provider-config').length,applies)
  // One consent traverses the complete draft path. Existing intent owns Start.
  line.hardware_provision_state='draft';++revision;rejectScoped=false;desired=true
  await page.getByRole('tab',{name:'Current SIM',exact:true}).click();await idle()
  await page.getByPlaceholder('Telus').fill('Current SIM ready')
  const startCount=requests.filter(item=>item.path.endsWith('/runtime/start')).length,offset=requests.length
  await setup.getByRole('button',{name:'Save and enable VoWiFi',exact:true}).click();await confirm();await idle()
  assert.equal(line.name,'Current SIM ready');assert.equal(line.enabled,true);assert.equal(line.hardware_provision_state,'provisioned')
  assert.deepEqual(requests.slice(offset).map(item=>item.path),['/v1/catalog/lines/new-line','/v1/readers/provision','/v1/catalog/lines/new-line','/v1/system/provider-config'])
  assert.equal(requests.filter(item=>item.path.endsWith('/runtime/start')).length,startCount)
  assert.equal(requests.filter(item=>item.path.endsWith('/runtime/stop')).length,1)
  assert.equal(other.name,'Other SIM');assert.deepEqual(errors,[])
  await page.screenshot({path:output+'/single-activation.png',fullPage:true})
  // Retain this component while resolving a second lost apply response; the
  // earlier case separately covers shared uncertainty across editor remounts.
  for(const mode of ['lost','cancelled-success']){
    applied=false;desired=false;running=false;rejectScoped=true;loseWideApplyOnce=mode==='lost';++revision
    await reopen()
    await setup.getByRole('button',{name:'Enable VoWiFi',exact:true}).click();await confirm();await idle()
    const applyStarted=new Promise(resolve=>{applySeen=resolve})
    holdWideApply=mode==='cancelled-success'
    const startsBeforeApply=requests.filter(item=>item.path.endsWith('/runtime/start')).length
    await setup.getByRole('button',{name:'Review pending configuration and enable',exact:true}).click();await confirm()
    if(holdWideApply){
      await applyStarted
      await setup.getByRole('button',{name:'Cancel remaining steps',exact:true}).click()
      holdWideApply=false;releaseApply()
    }
    await idle()
    if(mode==='lost')await setup.getByRole('alert').filter({hasText:/fetch/i}).waitFor()
    else {
      const archive=await page.evaluate(()=>Object.entries(sessionStorage).filter(([key])=>key.startsWith('mdd_provider_apply_pending:')).map(([,value])=>JSON.parse(value)))
      assert.ok(archive.some(attempt=>attempt.result?.state==='applied' && attempt.revision===revision),'the admitted successful result remains archived after cancellation')
    }
    assert.equal(requests.filter(item=>item.path.endsWith('/runtime/start')).length,startsBeforeApply)
    const beforeReadApply=requests.filter(item=>item.path==='/v1/system/provider-config').length
    await read()
    assert.equal(await setup.getByRole('button',{name:'Review pending configuration and enable',exact:true}).count(),0,
      mode==='lost' ? 'SETUP_SAME_VIEW_APPLY: resolved original apply must retire wider continuation' : 'SETUP_CANCELLED_APPLY: admitted successful apply must retire cancelled continuation')
    await setup.getByRole('button',{name:'Enable VoWiFi',exact:true}).click();await confirm();await idle()
    assert.equal(requests.filter(item=>item.path==='/v1/system/provider-config').length,beforeReadApply,'same-view recovery never repeats the confirmed apply')
    assert.equal(requests.filter(item=>item.path.endsWith('/runtime/start')).length,startsBeforeApply+1)
  }
  assert.deepEqual(errors,[])
  console.log('PASS SETUP_ACTIVATION: one current-card action with explicit recovery, retained intent and separate saved-line management')
}catch(error){if(page)await page.screenshot({path:`${output}/failure.png`,fullPage:true}).catch(()=>{});throw error}
finally{await browser?.close();await vite.close()}
