import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
import { createServer } from 'vite'
import { chromium } from 'playwright-core'

// Actual React DevicesPage and REST adapters, with only the HTTP boundary simulated.
const card = '8944100000000002420'
const entries = [
  {schema_version:1,id:'phone-a',name:'Example phone A',imei:'123456789012347'},
  {schema_version:1,id:'phone-b',name:'Example phone B',imei:'123456789012354'},
]
let snapshot = {schema_version:1,revision:3,catalog_revision:7,entries,
  bindings:[{card_id:card,entry_id:entries[0].id,imei:entries[0].imei}],unpooled:[]}
let failRead = false, failAfterWrite = false, pendingRead = null, heldRead = null
const writes = [], errors = []
const output = process.env.MDD_BROWSER_EVIDENCE || 'browser-evidence/hardware-imei'
await mkdir(output,{recursive:true})
const vite = await createServer({server:{host:'127.0.0.1',port:0}})
let browser, page
try {
  await vite.listen()
  browser = await chromium.launch({channel:'chrome',headless:true})
  page = await browser.newPage({viewport:{width:1200,height:1000}})
  page.on('pageerror',error => errors.push(error.message))
  await page.route('**/v1/**',async route => {
    const request = route.request(), url = new URL(request.url())
    if (request.method() === 'GET' && url.pathname === '/v1/imei-pool') {
      const body = structuredClone(snapshot)
      if (pendingRead) {
        heldRead = () => route.fulfill({json:body})
        pendingRead(); pendingRead = null
        return
      }
      return route.fulfill(failRead ? {status:503,json:{code:'fixture_read_failed'}} : {json:body})
    }
    if (request.method() === 'PUT') {
      const entry = entries.find(item => url.pathname === `/v1/imei-pool/${item.id}/cards/${card}`)
      assert.ok(entry,'only the explicit current-card binding may be written')
      assert.equal(request.headers()['if-match'],`"${snapshot.revision}"`)
      assert.deepEqual(request.postDataJSON(),{expected_catalog_revision:snapshot.catalog_revision,expected_card_id:card})
      writes.push(entry.id)
      snapshot = {...snapshot,revision:snapshot.revision+1,catalog_revision:snapshot.catalog_revision+1,
        bindings:[{card_id:card,entry_id:entry.id,imei:entry.imei}]}
      if (failAfterWrite) failRead = true
      return route.fulfill({json:{changed:true,revision:snapshot.revision,catalog_revision:snapshot.catalog_revision}})
    }
    errors.push(`unexpected API ${request.method()} ${url.pathname}`)
    return route.fulfill({status:500,json:{code:'unexpected_fixture_request'}})
  })
  const panel = () => page.locator('.u-hardware-panel')
  const current = () => panel().locator('.u-detail').filter({has:page.locator('span',{hasText:'Current IMEI'})}).locator('b')
  const input = () => panel().locator('.u-hardware-imei input').nth(0)
  const name = () => panel().locator('.u-hardware-imei input').nth(1)
  const select = () => panel().locator('.u-hardware-imei select')
  const save = () => panel().getByRole('button',{name:'Save',exact:true})
  const settled = async () => {
    await select().waitFor({state:'visible'})
    await page.waitForFunction(() => !document.querySelector('.u-hardware-imei select')?.disabled)
  }
  const check = async entry => {
    await settled()
    assert.ok((await current().innerText()).includes(entry.name),'SAVED_BINDING_READBACK: Current IMEI must show the saved card binding')
    assert.equal(await select().inputValue(),entry.id)
    assert.equal(await input().inputValue(),entry.imei)
    assert.equal(await name().inputValue(),entry.name)
  }
  const url = `${vite.resolvedUrls.local[0]}tests/hardwareImei.html`
  await page.goto(url)
  await check(entries[0]) // Old page loads the pool but displays Not set/empty fields.
  await name().fill('Unsaved draft')
  await page.getByRole('button',{name:'Device heartbeat',exact:true}).click()
  assert.equal(await name().inputValue(),'Unsaved draft')
  assert.ok((await current().innerText()).includes(entries[0].name),'draft must not change confirmed display')
  await select().selectOption(entries[1].id)
  assert.ok((await current().innerText()).includes(entries[0].name))
  await save().click()
  await check(entries[1])
  assert.deepEqual(writes,[entries[1].id])
  await page.locator('.u-tabs').getByRole('button',{name:'Status',exact:true}).click()
  await page.locator('.u-tabs').getByRole('button',{name:'Hardware',exact:true}).click()
  await check(entries[1])
  await page.getByRole('button',{name:'Leave devices',exact:true}).click()
  await page.getByRole('button',{name:'Return to devices',exact:true}).click()
  await check(entries[1])
  await page.reload()
  await check(entries[1])
  await page.screenshot({path:`${output}/saved-and-reopened.png`,fullPage:true})

  // Existing catalog bindings and older values outside the pool use the same readback.
  snapshot.bindings[0].line_id = 'fixture-line'
  await page.reload(); await check(entries[1])
  snapshot.bindings = []
  snapshot.unpooled = [{line_id:'fixture-line',card_id:card,imei:entries[0].imei}]
  await page.reload(); await settled()
  assert.equal(await select().inputValue(),'')
  assert.equal(await input().inputValue(),entries[0].imei)
  assert.ok((await current().innerText()).includes(entries[0].imei.slice(-4)))

  // A late response for the old ICCID cannot populate the replacement SIM.
  await page.getByRole('button',{name:'Leave devices',exact:true}).click()
  const held = new Promise(resolve => {pendingRead = resolve})
  await page.getByRole('button',{name:'Return to devices',exact:true}).click()
  await held
  await page.getByRole('button',{name:'Replace SIM',exact:true}).click()
  await settled()
  await heldRead()
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
  assert.equal(await input().inputValue(),'')
  assert.equal(await select().inputValue(),'')
  assert.equal(await current().innerText(),'Not set')
  assert.ok(await save().isDisabled())

  failRead = true
  await page.reload()
  await panel().getByRole('alert').waitFor()
  assert.equal(await current().innerText(),'Read failed')
  assert.ok(await input().isDisabled())
  assert.ok(await save().isDisabled())
  failRead = false
  await panel().getByRole('button',{name:'Reload binding',exact:true}).click()
  await settled()
  snapshot.unpooled = []
  failAfterWrite = true
  await select().selectOption(entries[1].id)
  await save().click()
  await page.getByLabel('Toast').filter({hasText:'IMEI saved, but readback failed'}).waitFor()
  assert.equal(await current().innerText(),'Read failed')
  assert.ok(await save().isDisabled())
  assert.equal(writes.length,2,'no automatic write retry after readback failure')
  failRead = false
  await panel().getByRole('button',{name:'Reload binding',exact:true}).click()
  await check(entries[1])
  assert.deepEqual(errors,[])
  console.log('PASS: actual Chromium DevicesPage save, tab/page return, reload, heartbeat, ICCID replacement, legacy value and failed readback; no hardware operations')
} catch (error) {
  if (page) await page.screenshot({path:`${output}/failure.png`,fullPage:true}).catch(() => {})
  throw error
} finally {
  await browser?.close()
  await vite.close()
}
