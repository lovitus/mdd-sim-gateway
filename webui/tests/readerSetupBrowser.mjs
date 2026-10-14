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
    if(path==='/v1/line-candidates')return route.fulfill({json:{catalog_revision:revision,candidates:[{candidate_id:'candidate',card_id:card,
      agent_id:'agent',process_generation:'process',reader_name:'Setup reader',session_generation:session,can_claim:!line,
      configured_line_id:line?.id,observed:sim}]}})
    if(path==='/v1/line-candidates/candidate/claim'){
      assert.equal(line,null,'must claim only once');assert.equal(request.headers()['if-match'],`"${revision}"`)
      line={schema_version:1,id:'new-line',card_id:card,name:body.name,enabled:false,hardware_provision_state:'draft',sim:{...sim},network:{egress_country:'gb'}}
      return route.fulfill({json:{line,revision:++revision}})
    }
    if(path==='/v1/catalog/lines/new-line' && method==='PUT'){
      assert.equal(request.headers()['if-match'],`"${revision}"`);assert.equal(body.card_id,card)
      line=body;return route.fulfill({json:{line,revision:++revision}})
    }
    if(path==='/v1/readers/provision'){
      assert.equal(body.expected_catalog_revision,revision);assert.equal(body.card_id,card);assert.equal(body.sim_session_generation,session)
      assert.equal(request.headers()['x-mdd-agent-id'],'agent');assert.equal(line.enabled,false)
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
      if(method==='GET')return route.fulfill({json:{catalog_revision:revision,applied_revision:appliedRevision,pending:revision!==appliedRevision,applying:false}})
      assert.equal(body.catalog_revision,revision)
      if(body.only_add_line_id && rejectScoped)return route.fulfill({status:409,json:{code:'provider_apply_blocked',detail:'provider apply plan is blocked: unrelated_provider_changes_pending'}})
      applied=true;appliedRevision=revision
      if(!body.only_add_line_id && loseWideApplyOnce){loseWideApplyOnce=false;return route.abort('failed')}
      return route.fulfill({json:{state:'applied',catalog_revision:revision,apply_id:'apply-fixture'}})
    }
    if(path==='/v1/lines/new-line')return route.fulfill({json:projection()})
    if(path==='/v1/lines/new-line/vowifi/status')return route.fulfill({json:{line_id:line.id,process_generation:'provider',runtime:{condition:running?'running':'stopped'}}})
    if(path.endsWith('/recovery'))return route.fulfill({json:{line_id:'new-line'}})
    if(path.endsWith('/availability'))return route.fulfill({json:{segments:[],summary:{}}})
    if(path==='/v1/lines/new-line/vowifi/runtime/stop' || path==='/v1/lines/new-line/vowifi/runtime/start'){
      assert.equal(body.expected_card_id,card)
      desired=path.endsWith('/start');++revision
      if(desired)assert.ok(line.enabled && applied,'start cannot precede initialization/application')
      running=desired
      return route.fulfill({json:{accepted:true,code:desired?'started':'stopped'}})
    }
    errors.push(`${method} ${path}`);return route.fulfill({status:500,json:{code:'unexpected_fixture_request'}})
  })
  await page.goto(`${vite.resolvedUrls.local[0]}tests/hardwareImei.html?setup`)
  await page.getByPlaceholder('Telus').waitFor()
  // The pre-fix production page lacks any actionable stage. This is a behavioral red, not a missing export.
  assert.equal(await page.getByRole('button',{name:'Save draft',exact:true}).count(),1,'SETUP_JOURNEY: current card needs one explicit save-draft continuation')
  await page.getByRole('tab',{name:'Manage saved lines',exact:true}).click()
  await page.locator('.u-saved-lines select').selectOption('other-line')
  await page.getByPlaceholder('Telus').waitFor()
  await page.getByPlaceholder('Telus').fill('Do not write another card')
  await page.locator('.u-saved-lines select').selectOption('')
  assert.equal(await page.getByPlaceholder('Telus').inputValue(),'','empty selection clears foreign baseline')
  await page.getByRole('tab',{name:'Current SIM',exact:true}).click()
  assert.equal(await page.getByPlaceholder('Telus').inputValue(),'')
  await page.getByPlaceholder('Telus').fill('Current SIM A')
  await page.getByRole('button',{name:'Save draft',exact:true}).click()
  await page.getByRole('button',{name:'Complete initialization',exact:true}).waitFor()
  assert.equal(line.enabled,false);assert.equal(line.hardware_provision_state,'draft');assert.equal(desired,true)
  assert.equal(await page.getByRole('button',{name:'Enable line',exact:true}).count(),0,'draft has no enable action')
  assert.equal(await page.getByLabel('Enabled in Provider catalog',{exact:true}).isEnabled(),false)
  assert.equal(other.name,'Other SIM')
  await page.getByRole('button',{name:'VoWiFi',exact:true}).click()
  await page.getByRole('button',{name:'Configure SIM',exact:true}).click()
  assert.equal(requests.filter(item=>item.path.endsWith('/runtime/start')).length,0)
  await page.getByRole('button',{name:'Complete initialization',exact:true}).click()
  session='reinserted-session'
  const refreshed=page.waitForResponse(response=>new URL(response.url()).pathname==='/v1/devices')
  await page.evaluate(()=>[...document.querySelectorAll('button')].find(button=>button.textContent==='Refresh fixture').click())
  await refreshed
  await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))))
  await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click()
  await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))))
  assert.equal(requests.filter(item=>item.path==='/v1/readers/provision').length,0,'changed attachment invalidates open confirmation')
  await page.getByRole('button',{name:'Complete initialization',exact:true}).click()
  await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click()
  await page.getByRole('button',{name:'Check original result',exact:true}).click()
  await page.getByRole('button',{name:'New read-only SIM verification',exact:true}).waitFor()
  assert.equal(requests.filter(item=>item.path==='/v1/readers/provision').length,1,'unknown does not auto retry')
  await page.getByRole('button',{name:'New read-only SIM verification',exact:true}).click()
  await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click()
  await page.getByRole('button',{name:'Enable line',exact:true}).waitFor()
  assert.equal(line.enabled,false,'initialization must leave disabled')
  await page.getByRole('button',{name:'Cancel pending start',exact:true}).click()
  await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click()
  await page.getByRole('button',{name:'Cancel pending start',exact:true}).waitFor({state:'detached'})
  assert.equal(desired,false)
  await page.getByRole('button',{name:'Enable line',exact:true}).click()
  await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click()
  await page.getByRole('button',{name:'Apply this new line',exact:true}).waitFor()
  assert.equal(line.enabled,true);assert.equal(running,false)
  await page.getByRole('button',{name:'Apply this new line',exact:true}).click()
  await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click()
  await page.getByRole('alert').filter({hasText:'unrelated_provider_changes_pending'}).waitFor()
  assert.equal(applied,false);assert.equal(requests.filter(item=>item.path==='/v1/system/provider-config').length,1)
  await page.getByRole('button',{name:'Review pending Provider configuration',exact:true}).click()
  await page.getByRole('dialog').getByRole('button',{name:'Cancel',exact:true}).click()
  assert.equal(requests.filter(item=>item.path==='/v1/system/provider-config').length,1,'no automatic widening')
  await page.getByRole('button',{name:'Review pending Provider configuration',exact:true}).click()
  await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click()
  await page.getByRole('alert').filter({hasText:/fetch/i}).waitFor()
  assert.equal(requests.filter(item=>item.path==='/v1/system/provider-config').length,2,'lost response cannot auto reapply')
  await page.getByRole('button',{name:'Read current state',exact:true}).click()
  await page.getByRole('button',{name:'Start VoWiFi',exact:true}).waitFor()
  assert.equal(requests.filter(item=>item.path.endsWith('/runtime/start')).length,0)
  await page.getByRole('button',{name:'Start VoWiFi',exact:true}).click()
  await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click()
  await page.getByText('VoWiFi runtime running',{exact:true}).waitFor()
  assert.equal(requests.filter(item=>item.path.endsWith('/runtime/start')).length,1)
  assert.deepEqual(errors,[])
  await page.screenshot({path:`${output}/completed-reader-journey.png`,fullPage:true})
  console.log('PASS SETUP_JOURNEY: current-card/save/init/explicit-enable/scoped-rejection/wide-consent/start with retained intent and unrelated editor separation')
}catch(error){if(page)await page.screenshot({path:`${output}/failure.png`,fullPage:true}).catch(()=>{});throw error}
finally{await browser?.close();await vite.close()}
