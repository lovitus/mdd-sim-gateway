import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
import { createServer } from 'vite'
import { chromium } from 'playwright-core'

const output = process.env.MDD_BROWSER_EVIDENCE || 'browser-evidence/new-card'
await mkdir(output,{recursive:true})
const vite = await createServer({server:{host:'127.0.0.1',port:0}})
let browser, page
const calls=[], errors=[]
let releaseProfiles, profileRequested, profileWrites=0, recoveryWrites=0, recoveryID=''
const profilesSeen=new Promise(resolve=>{profileRequested=resolve})
const profilesReleased=new Promise(resolve=>{releaseProfiles=resolve})
try {
  await vite.listen()
  browser=await chromium.launch({channel:'chrome',headless:true})
  page=await browser.newPage({viewport:{width:1200,height:1000}})
  page.on('pageerror',error=>errors.push(error.message))
  await page.route('**/v1/**',async route=>{
    const request=route.request(), path=new URL(request.url()).pathname
    if (path==='/v1/imei-pool' && request.method()==='GET') return route.fulfill({json:{schema_version:1,revision:1,catalog_revision:2,
      entries:[{id:'phone',name:'Pending phone',imei:'123456789012347'}],
      bindings:[{card_id:'8944100000000002420',entry_id:'phone',imei:'123456789012347'}],unpooled:[]}})
    if (path==='/v1/lines/confirmed-line/vowifi/runtime/stop' && request.method()==='POST') {
      calls.push(request.postDataJSON())
      return route.fulfill({json:{accepted:true,code:'stopped'}})
    }
    if (path==='/v1/devices/fixture-modem/profiles' && request.method()==='GET') {
      profileRequested()
      await profilesReleased
      return route.fulfill({json:{device:{policy:{revision:3,profile_mode:'agent'}},
        profiles:[{source:'modem',name:'Carrier suggestion',apn:'suggested',auth:'PAP',username:'suggested-user'}]}})
    }
    if (path==='/v1/devices/fixture-modem/profiles' && request.method()==='PUT') {
      profileWrites++
      assert.equal(request.headers()['if-match'],'"3"')
      assert.equal(request.postDataJSON().apn,'my.apn')
      assert.equal(request.postDataJSON().auth,'CHAP')
      assert.equal(request.postDataJSON().expected_target.card_id,'8944100000000002420')
      return route.fulfill({json:{profiles:[]}})
    }
    if (path==='/v1/lines/modem-line/cellular/soft-restart' && request.method()==='POST') {
      recoveryWrites++; recoveryID=request.postDataJSON().operation_id
      return route.fulfill({status:202,json:{state:'unknown',operation_id:recoveryID,code:'modem_recovery_transport_unknown'}})
    }
    if (path===`/v1/operations/${recoveryID}` && request.method()==='GET') {
      return route.fulfill({json:{state:'unknown',operation_id:recoveryID,error_code:'modem_recovery_transport_unknown'}})
    }
    if (request.method()==='GET' && path==='/v1/line-candidates') return route.fulfill({json:{catalog_revision:2,candidates:[]}})
    if (request.method()==='GET' && path.endsWith('/availability')) return route.fulfill({json:{start:0,end:1,segments:[],summary:{}}})
    errors.push(`${request.method()} ${path}`)
    return route.fulfill({status:409,json:{code:'unexpected_fixture_request'}})
  })
  await page.goto(`${vite.resolvedUrls.local[0]}tests/hardwareImei.html?flow`)
  await page.getByRole('button',{name:'Call',exact:true}).click()
  assert.equal(await page.locator('.u-tabs .active').innerText(),'SIM','NEW_CARD_SHORTCUT must open this card configuration')
  assert.equal(await page.getByLabel('Selected line').innerText(),'none','new card must not select the previous line')
  const fields=page.locator('.mdd-sim-fields')
  const name=fields.getByPlaceholder('Telus')
  await name.fill('Keep my SIM draft')
  await page.getByRole('button',{name:'Other reader attached',exact:true}).click()
  assert.equal(await name.inputValue(),'Keep my SIM draft','reader inventory must not reset the edited card')
  assert.equal(await fields.getByPlaceholder('Choose an exit country').inputValue(),'gb')
  await page.getByText('The IMEI is inherited from this device’s Hardware settings.',{exact:true}).waitFor()
  await page.screenshot({path:`${output}/new-card.png`,fullPage:true})
  await page.getByRole('button',{name:'Waiting start',exact:true}).click()
  const toggle=page.getByRole('switch',{name:'VoWiFi / WiFi Calling'})
  assert.ok(await toggle.isEnabled(),'STARTING_STOP: a completed start request must not trap stop behind runtime starting')
  await toggle.click()
  await page.getByRole('dialog').waitFor()
  // Replace the card while confirmation is open, without interacting through the modal.
  await page.evaluate(()=>[...document.querySelectorAll('button')].find(b=>b.textContent==='Change confirmed card').click())
  await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click()
  await page.getByLabel('Toast').filter({hasText:'Request accepted'}).waitFor()
  assert.equal(calls.length,1)
  assert.equal(calls[0].expected_card_id,'8944100000000002420','confirmation cannot retarget a replacement SIM')
  await page.screenshot({path:`${output}/confirmed-stop.png`,fullPage:true})
  await page.getByRole('button',{name:'Modem profile',exact:true}).click()
  await profilesSeen
  const profile=page.locator('form').filter({has:page.getByRole('button',{name:'Save profile',exact:true})})
  const field=label=>profile.locator('.u-form-grid > div').filter({has:page.locator('label',{hasText:new RegExp(`^${label}$`)})})
  await field('APN').locator('input').fill('my.apn')
  await field('Authentication').locator('select').selectOption('CHAP')
  await field('Username').locator('input').fill('my-user')
  releaseProfiles()
  await profile.getByRole('option',{name:/Carrier suggestion/}).waitFor({state:'attached'})
  await page.getByRole('button',{name:'Modem number update',exact:true}).click()
  assert.equal(await field('APN').locator('input').inputValue(),'my.apn')
  assert.equal(await field('Authentication').locator('select').inputValue(),'CHAP')
  assert.equal(await field('Username').locator('input').inputValue(),'my-user')
  await profile.getByRole('button',{name:'Save profile',exact:true}).click()
  await page.getByLabel('Toast').filter({hasText:'profile saved'}).waitFor()
  assert.equal(profileWrites,1)
  await page.getByRole('button',{name:'Status',exact:true}).click()
  await page.getByRole('button',{name:'Soft restart modem',exact:true}).click()
  await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click()
  await page.getByLabel('Toast').filter({hasText:'Recovery outcome unknown'}).waitFor()
  await page.getByRole('button',{name:'Read original operation',exact:true}).click()
  assert.equal(recoveryWrites,1)
  await page.screenshot({path:`${output}/profile-and-recovery.png`,fullPage:true})
  assert.deepEqual(errors,[])
  console.log('PASS new-card shortcut/SIM draft/concrete home country/starting-stop/frozen confirmation in actual Chromium; no hardware or paid action')
} catch(error) {
  if(page) await page.screenshot({path:`${output}/failure.png`,fullPage:true}).catch(()=>{})
  throw error
} finally { await browser?.close(); await vite.close() }
