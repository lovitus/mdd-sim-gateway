import assert from 'node:assert/strict'
import {mkdir,writeFile} from 'node:fs/promises'
import {createServer} from 'vite'
import {chromium} from 'playwright-core'

const output=process.env.MDD_BROWSER_EVIDENCE || 'browser-evidence/egress-budget'
await mkdir(output,{recursive:true})
const card='8985200000000000001', mib=1024**2
let config={schema_version:2,enabled:true,missing_policy:'error',refresh_minutes:30,
  profiles:{donor:{name:'Data SIM fixture',type:'cellular_sim',sim_iccid:card}},
  exits:{hk:{enabled:true,profile_id:'donor'},gb:{enabled:true,mode:'direct'}}}
let revision=7, budget={card_id:card,revision:1,limit_bytes:100*mib,used_bytes:20*mib,
  reserved_bytes:30*mib,uncertain_bytes:0,owner_state:'ready',configured:true,started_at:'2026-01-01T00:00:00Z'}
const writes=[],errors=[]
const vite=await createServer({server:{host:'127.0.0.1',port:0}})
let browser,page,holdBudgetWrite=false,releaseBudgetWrite,budgetEntered
let releaseInventory,releaseInitialBudget,initialBudgetEntered,holdInventory=true,holdInitialBudget=true
const inventoryGate=new Promise(resolve=>{releaseInventory=resolve})
const initialBudgetGate=new Promise(resolve=>{releaseInitialBudget=resolve})
const initialBudgetStarted=new Promise(resolve=>{initialBudgetEntered=resolve})
try {
  await vite.listen()
  browser=await chromium.launch({channel:'chrome',headless:true})
  page=await browser.newPage({viewport:{width:1200,height:1000}})
  page.on('pageerror',e=>errors.push(e.message))
  await page.route('**/v1/**',async route=>{
    const request=route.request(),path=new URL(request.url()).pathname,method=request.method()
    const body=['PUT','POST'].includes(method)?request.postDataJSON():null
    if(method!=='GET') writes.push({path,body})
    if(path==='/v1/egress/config') {
      if(method==='PUT'){config=body;revision++}
      return route.fulfill({json:{revision,config}})
    }
    if(path==='/v1/egress/config/apply')return route.fulfill({json:{config_revision:revision,catalog_revision:2,
      state:'partial',code:'runtime_confirmed',failed_countries:['hk']}})
    if(path==='/v1/egress/exits')return route.fulfill({json:{exits:[{country:'gb',ready:true,node:'GB fixture'},
      {country:'hk',ready:false,error:'cellular_lease_unavailable'}]}})
    if(path==='/v1/catalog/lines')return route.fulfill({json:{revision:2,lines:[{schema_version:1,id:'donor',card_id:card,
      enabled:false,name:'Donor fixture',sim:{msisdn:'+15550100001',mcc:'454',mnc:'00'},network:{egress_country:'hk'}}]}})
    if(path==='/v1/lines')return route.fulfill({json:{lines:[]}})
    if(path==='/v1/agents')return route.fulfill({json:{agents:[]}})
    if(path==='/v1/devices') {
      if(holdInventory){holdInventory=false;await inventoryGate}
      return route.fulfill({json:{devices:[{id:'modem',kind:'modem',mode:'adapted',
      modem:{equipment_id:'862547055201716',sim:{iccid:card,state:'ready'},policy:{desired:{cellular_enabled:false,connection_enabled:false}}},
      endpoints:[{association:'exact',operation_candidate:true,card_ids:[card],line:{id:'donor'}}]}]}})
    }
    if(path==='/v1/lines/donor/cellular/data/budget') {
      if(method==='GET'&&holdInitialBudget){
        holdInitialBudget=false
        const initial=structuredClone(budget)
        initialBudgetEntered();await initialBudgetGate
        return route.fulfill({json:initial})
      }
      if(method==='PUT') {
        if(holdBudgetWrite){budgetEntered();await new Promise(resolve=>{releaseBudgetWrite=resolve})}
        assert.equal(body.expected_card_id,card);assert.equal(body.revision,budget.revision)
        if(body.limit_bytes<budget.used_bytes+budget.reserved_bytes&&!body.reset)
          return route.fulfill({status:409,json:{code:'donor_budget_limit_committed'}})
        assert.ok(!body.reset||body.confirm_reset&&budget.owner_state==='stopped')
        budget={...budget,revision:budget.revision+1,limit_bytes:body.limit_bytes,
          used_bytes:body.reset?0:budget.used_bytes,uncertain_bytes:body.reset?0:budget.uncertain_bytes}
      }
      return route.fulfill({json:budget})
    }
    errors.push(`${method} ${path}`)
    return route.fulfill({status:500,json:{code:'unexpected_request'}})
  })
  await page.goto(`${vite.resolvedUrls.local[0]}tests/hardwareImei.html?egress`)
  await page.getByRole('button',{name:'Save and apply',exact:true}).waitFor()
  await page.locator('select option').filter({hasText:'Saved SIM (not in current inventory)'}).waitFor({state:'attached'})
  releaseInventory()
  await page.locator('select option').filter({hasText:'+15550100001'}).waitFor({state:'attached'})
  const panel=page.getByRole('region',{name:'Borrowed data budget'}),limit=panel.getByRole('spinbutton')
  assert.equal(await panel.count(),1,'DONOR_BUDGET_PANEL_MISSING')
  await initialBudgetStarted
  assert.ok(await panel.getByRole('button',{name:'Read current state',exact:true}).isDisabled(),'DONOR_INITIAL_READ_NOT_SERIALIZED')
  releaseInitialBudget()
  await limit.waitFor()
  assert.equal(await limit.inputValue(),'100')
  assert.ok(await panel.getByRole('button',{name:'Reset usage',exact:true}).isDisabled())
  assert.match(await panel.innerText(),/20.00 MiB/)
  await limit.fill('10')
  await panel.getByRole('button',{name:'Save limit',exact:true}).click()
  await panel.getByRole('alert').waitFor()
  assert.equal(budget.limit_bytes,100*mib)
  await limit.fill('60')
  await panel.getByRole('button',{name:'Save limit',exact:true}).click()
  await page.waitForFunction(()=>document.querySelector('output')?.textContent==='Borrowing limit saved.')
  await page.reload();await limit.waitFor();assert.equal(await limit.inputValue(),'60')
  budget={...budget,owner_state:'stopped',reserved_bytes:0,uncertain_bytes:5*mib}
  await panel.getByRole('button',{name:'Read current state',exact:true}).click()
  await panel.getByText('5.00 MiB',{exact:true}).waitFor()
  await panel.getByRole('button',{name:'Reset usage',exact:true}).click()
  assert.match(await page.getByRole('dialog').innerText(),new RegExp(card))
  await page.getByRole('dialog').getByRole('button',{name:'Cancel',exact:true}).click()
  assert.equal(budget.uncertain_bytes,5*mib)
  await panel.getByRole('button',{name:'Reset usage',exact:true}).click()
  await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click()
  await page.waitForFunction(()=>document.querySelector('output')?.textContent==='Borrowing budget reset.')
  assert.equal(budget.used_bytes,0);assert.equal(budget.uncertain_bytes,0)
  await page.getByRole('button',{name:'Save and apply',exact:true}).click()
  await page.getByRole('status').filter({hasText:'Healthy exits applied. Unavailable countries: HK'}).waitFor()
  assert.equal(config.enabled,true);assert.equal(config.exits.hk.enabled,true)
  assert.ok(writes.every(item=>item.path.endsWith('/budget')||item.path==='/v1/egress/config'||item.path==='/v1/egress/config/apply'))
  const resetCount=()=>writes.filter(item=>item.path.endsWith('/budget')&&item.body.reset).length
  const beforeReset=resetCount()
  await panel.getByRole('button',{name:'Reset usage',exact:true}).click()
  await page.getByRole('dialog').waitFor()
  // Replace the owning route while its confirmation remains in the shared host.
  await page.getByRole('button',{name:'Leave budget page',exact:true}).evaluate(button=>button.click())
  await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click()
  await page.getByRole('button',{name:'Return to budget page',exact:true}).click()
  await limit.waitFor()
  assert.equal(resetCount(),beforeReset,'DONOR_RESET_OUTLIVED_EDITOR')
  holdBudgetWrite=true
  const entered=new Promise(resolve=>{budgetEntered=resolve})
  await limit.fill('45')
  await panel.getByRole('button',{name:'Save limit',exact:true}).click()
  await entered
  await page.getByRole('button',{name:'Leave budget page',exact:true}).click()
  await page.getByRole('button',{name:'Return to budget page',exact:true}).click()
  await limit.waitFor();assert.equal(await limit.inputValue(),'60')
  const response=page.waitForResponse(r=>r.url().endsWith('/budget')&&r.request().method()==='PUT')
  releaseBudgetWrite();await response;holdBudgetWrite=false
  assert.equal(await limit.inputValue(),'60','late save replaced the new editor')
  assert.equal(await page.getByLabel('Toast').innerText(),'','late save notified the new editor')
  for(const width of [1200,390]) {
    await page.setViewportSize({width,height:1000})
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),'budget must not overflow portrait viewport')
    await page.screenshot({path:`${output}/budget-${width}.png`,fullPage:true})
  }
  assert.deepEqual(errors,[])
  console.log('Mounted donor budget read/save/reset and partial egress flow passed; no data switch or paid endpoint invoked')
} catch(error) {
  if(page){await page.screenshot({path:`${output}/failure.png`,fullPage:true}).catch(()=>{});await writeFile(`${output}/failure.txt`,await page.locator('body').innerText())}
  throw error
} finally {releaseInventory();releaseInitialBudget();releaseBudgetWrite?.();await browser?.close();await vite.close()}
