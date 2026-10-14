import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
import { createServer } from 'vite'
import { chromium } from 'playwright-core'

const output=process.env.MDD_BROWSER_EVIDENCE || 'browser-evidence/new-card-review'
await mkdir(output,{recursive:true})
const vite=await createServer({server:{host:'127.0.0.1',port:0}})
let browser,page,releaseFirst,firstSeen
const firstReceived=new Promise(resolve=>{firstSeen=resolve})
const firstReleased=new Promise(resolve=>{releaseFirst=resolve})
const operations=[], stops=[], errors=[]
const key='mdd_modem_recovery_fixture-modem_8944100000000002420'
try {
  await vite.listen()
  browser=await chromium.launch({channel:'chrome',headless:true})
  page=await browser.newPage({viewport:{width:1200,height:1000}})
  page.on('pageerror',error=>errors.push(error.message))
  await page.route('**/v1/**',async route=>{
    const request=route.request(), path=new URL(request.url()).pathname
    if(path==='/v1/imei-pool')return route.fulfill({json:{revision:0,catalog_revision:1,entries:[],bindings:[],unpooled:[]}})
    if(path==='/v1/catalog/lines')return route.fulfill({json:{revision:1,lines:[]}})
    if(path==='/v1/line-candidates')return route.fulfill({json:{catalog_revision:1,candidates:[]}})
    if(path.endsWith('/availability'))return route.fulfill({json:{start:0,end:1,segments:[],summary:{}}})
    if(path==='/v1/devices/fixture-modem/profiles')return route.fulfill({json:{device:{policy:{revision:3,profile_mode:'agent'}},profiles:[]}})
    if(path==='/v1/lines/saved-line/vowifi/runtime/stop'){
      stops.push(request.postDataJSON()); return route.fulfill({json:{accepted:true,code:'stopped'}})
    }
    if(path==='/v1/lines/modem-line/cellular/soft-restart'){
      const id=request.postDataJSON().operation_id
      operations.push(id)
      if(operations.length===1){firstSeen();await firstReleased;return route.fulfill({json:{state:'accepted',operation_id:id}})}
      return route.fulfill({status:202,json:{state:'unknown',operation_id:id,code:'modem_recovery_transport_unknown'}})
    }
    if(path.startsWith('/v1/operations/')){
      const id=path.split('/').at(-1)
      assert.ok(operations.includes(id))
      return route.fulfill({json:{state:id===operations[0]?'succeeded':'unknown',operation_id:id}})
    }
    errors.push(`${request.method()} ${path}`)
    return route.fulfill({status:409,json:{code:'unexpected_fixture_request'}})
  })
  await page.goto(`${vite.resolvedUrls.local[0]}tests/hardwareImei.html?flow`)
  if(process.env.MDD_BROWSER_CASE!=='recovery'){
    await page.getByRole('button',{name:'Offline saved intent',exact:true}).click()
    const toggle=page.getByRole('switch',{name:'VoWiFi / WiFi Calling'})
    assert.ok(await toggle.isEnabled(),'OFFLINE_STOP must remain available through real mapper')
    await toggle.click()
    await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click()
    await page.getByLabel('Toast').filter({hasText:'Request accepted'}).waitFor()
    assert.equal(stops.length,1)
    assert.equal(stops[0].expected_card_id,'8944100000000002420')
    await page.getByRole('button',{name:'Transitions',exact:true}).click()
    assert.equal(await toggle.isEnabled(),false,'STOPPING_START must stay disabled')
    assert.equal(await page.getByRole('switch',{name:'4G connection'}).isEnabled(),false,'4G_TRANSITION must stay disabled')
    await page.screenshot({path:`${output}/offline-stop-and-transitions.png`,fullPage:true})
  }
  if(process.env.MDD_BROWSER_CASE!=='transitions'){
    await page.getByRole('button',{name:'Modem profile',exact:true}).click()
    await page.getByRole('button',{name:'Status',exact:true}).click()
    const restart=()=>page.getByRole('button',{name:'Soft restart modem',exact:true})
    await restart().click()
    await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click()
    await firstReceived
    await page.getByRole('button',{name:'Hardware',exact:true}).click()
    await page.getByRole('button',{name:'Status',exact:true}).click()
    const read=()=>page.getByRole('button',{name:'Read original operation',exact:true})
    await read().click()
    await page.getByRole('status').filter({hasText:`succeeded · ${operations[0]}`}).waitFor()
    await restart().click()
    await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click()
    await page.getByLabel('Toast').filter({hasText:'Recovery outcome unknown'}).waitFor()
    assert.equal(operations.length,2)
    const firstResponse=page.waitForResponse(response=>response.request().method()==='POST' && response.request().postDataJSON()?.operation_id===operations[0])
    releaseFirst();await firstResponse
    await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))))
    const current=await page.evaluate(key=>JSON.parse(localStorage.getItem(key)),key)
    assert.equal(current.operation_id,operations[1],'LATE_RECOVERY must not overwrite newer unknown operation')
    assert.equal(current.state,'unknown')
    const original=await page.evaluate(key=>JSON.parse(localStorage.getItem(key)),`${key}:${operations[0]}`)
    assert.equal(original.state,'succeeded','late POST must not regress prior terminal status readback')
    await restart().click()
    await page.getByLabel('Toast').filter({hasText:'unknown outcome must not be retried'}).waitFor()
    assert.equal(operations.length,2)
    await page.getByRole('button',{name:'Offline modem',exact:true}).click()
    assert.ok(await read().isVisible(),'OFFLINE_RECOVERY original receipt must remain readable without diagnostics')
    await read().click()
    await page.getByRole('status').filter({hasText:`unknown · ${operations[1]}`}).waitFor()
    assert.equal(operations.length,2)
    await page.screenshot({path:`${output}/retained-offline-recovery.png`,fullPage:true})
  }
  assert.deepEqual(errors,[])
  console.log('PASS mapped offline stop, restricted transitions, recovery late-response ownership and offline original status')
}catch(error){if(page)await page.screenshot({path:`${output}/failure.png`,fullPage:true}).catch(()=>{});throw error}
finally{releaseFirst();await browser?.close();await vite.close()}
