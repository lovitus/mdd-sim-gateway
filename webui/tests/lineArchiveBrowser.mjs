import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
import { createServer } from 'vite'
import { chromium } from 'playwright-core'

const output = process.env.MDD_BROWSER_EVIDENCE || 'browser-evidence/line-archive'
await mkdir(output,{recursive:true})
const lines = ['restore-line','remove-line','purge-line'].map((id,index) => ({schema_version:1,id,name:id,
  card_id:`894410000000000000${index+1}`,deleted:true,enabled:false,sim:{imsi:`23410000000000${index+1}`,mcc:'234',mnc:'10'}}))
let revision = 7, firstFailure = true
const receipts = new Map(), purges = new Map()
let wrongReceipt = true
const writes = [], errors = []
const vite = await createServer({server:{host:'127.0.0.1',port:0}})
let browser,page
try {
  await vite.listen()
  browser = await chromium.launch({channel:'chrome',headless:true})
  page = await browser.newPage({viewport:{width:1100,height:900}})
  page.on('pageerror',error => errors.push(error.message))
  await page.route('**/v1/**',async route => {
    const request = route.request(),url = new URL(request.url())
    if (request.method()==='GET' && url.pathname==='/v1/catalog/lines' && url.searchParams.get('include_deleted')==='true') {
      return route.fulfill({json:{schema_version:1,revision,lines}})
    }
    const match = /^\/v1\/catalog\/lines\/(restore-line|remove-line|purge-line)\/(restore|permanent-delete)$/.exec(url.pathname)
    if (request.method()==='POST' && match) {
      const [,id,action] = match, body = request.postDataJSON(), line = lines.find(line => line.id===id)
      assert.equal(request.headers()['if-match'],`"${revision}"`)
      writes.push({id,action,body})
      if (action==='restore') {
        assert.deepEqual(body,{})
        assert.equal(line.enabled,false)
        line.deleted = false
        return route.fulfill({json:{line,revision:++revision,changed:true}})
      }
      assert.equal(body.schema_version,1)
      assert.match(body.operation_id,/^[a-z0-9-]+$/)
      assert.equal(body.delete_history,id==='purge-line')
      if (receipts.has(body.operation_id)) {
        const receipt = receipts.get(body.operation_id)
        assert.equal(receipt.line_id,id)
        assert.equal(receipt.delete_history,body.delete_history)
        if (wrongReceipt) {
          wrongReceipt = false
          return route.fulfill({json:{schema_version:1,operation:{...receipt,operation_id:'other-operation'}}})
        }
        return route.fulfill({json:{schema_version:1,operation:receipt}})
      }
      if (firstFailure) {
        firstFailure = false
        return route.fulfill({status:409,json:{code:'line_sms_operation_unresolved',operation:{schema_version:1,
          line_id:id,operation_id:body.operation_id,delete_history:body.delete_history,stage:'prepared',catalog_revision:revision}}})
      }
      lines.splice(lines.indexOf(line),1)
      purges.set(id,(purges.get(id)||0)+1)
      const receipt = {...body,line_id:id,stage:'succeeded',catalog_revision:++revision}
      receipts.set(body.operation_id,receipt)
      if (id==='purge-line') return route.abort('failed')
      return route.fulfill({json:{schema_version:1,operation:receipt}})
    }
    errors.push(`unexpected API ${request.method()} ${url.pathname}`)
    return route.fulfill({status:500,json:{code:'unexpected_fixture_request'}})
  })
  const openRecycle = async () => {
    await page.getByText('No communication devices found',{exact:true}).waitFor()
    assert.equal(await page.getByRole('button',{name:'Recycle Bin',exact:true}).count(),1,'NO_DEVICE_RECYCLE: archived lines remain reachable without hardware')
    await page.getByRole('button',{name:'Recycle Bin',exact:true}).click()
    await page.getByRole('heading',{name:/^Recycle Bin (\(Archived lines\)|is empty)$/}).waitFor()
  }
  const row = id => page.locator('.u-recycled-line').filter({hasText:`(${id})`})
  const confirm = async id => {
    await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click()
    await page.getByRole('dialog').getByRole('textbox').fill(id)
    await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click()
  }
  await page.goto(`${vite.resolvedUrls.local[0]}tests/lineArchive.html`)
  await openRecycle()
  await row('restore-line').getByRole('button',{name:'Restore',exact:true}).click()
  await row('restore-line').waitFor({state:'detached'})
  assert.equal(lines.find(line => line.id==='restore-line').enabled,false,'restoration must not enable a line')
  await row('remove-line').getByRole('button',{name:'Delete permanently',exact:true}).click()
  await page.getByRole('dialog').getByRole('button',{name:'Cancel',exact:true}).click()
  assert.equal(writes.length,1,'cancellation must not delete')
  await row('remove-line').getByRole('button',{name:'Delete permanently',exact:true}).click()
  await confirm('wrong-line')
  await page.getByLabel('Toast').filter({hasText:'Permanent deletion was not confirmed.'}).waitFor()
  assert.equal(writes.length,1,'wrong typed identity must not delete')
  await row('remove-line').getByRole('checkbox').check()
  await row('remove-line').getByRole('button',{name:'Delete permanently',exact:true}).click()
  await confirm('remove-line')
  await page.getByLabel('Toast').filter({hasText:'line_sms_operation_unresolved'}).waitFor()
  await row('remove-line').getByRole('button',{name:'Resume permanent deletion',exact:true}).waitFor()
  const original = writes[1].body
  assert.equal(writes.length,2,'unresolved result must not trigger automatic retry')
  await page.reload(); await openRecycle()
  assert.equal(await row('remove-line').getByRole('checkbox').isChecked(),true)
  assert.equal(await row('remove-line').getByRole('checkbox').isDisabled(),true)
  assert.equal(await row('remove-line').getByRole('button',{name:'Restore',exact:true}).isDisabled(),true)
  await page.screenshot({path:`${output}/retained-operation.png`,fullPage:true})
  await row('remove-line').getByRole('button',{name:'Resume permanent deletion',exact:true}).click()
  await confirm('remove-line')
  await row('remove-line').waitFor({state:'detached'})
  assert.deepEqual(writes[2].body,original,'reload and explicit retry must reuse the same operation and history intent')
  await page.setViewportSize({width:390,height:844})
  await row('purge-line').getByRole('button',{name:'Delete permanently',exact:true}).click()
  await confirm('purge-line')
  await page.getByLabel('Toast').filter({hasText:'Error:'}).waitFor()
  const lost = writes[3].body
  assert.equal(lines.some(line => line.id==='purge-line'),false,'server already removed the line')
  assert.equal(purges.get('purge-line'),1)
  await page.reload(); await openRecycle()
  assert.equal(await row('purge-line').count(),1,'LOST_DELETION_RECEIPT: absent catalog line must not hide its unconfirmed deletion')
  assert.equal(await row('purge-line').getByRole('button',{name:'Restore',exact:true}).count(),0)
  assert.equal(writes.length,4,'reload must not automatically retry a deletion')
  await page.screenshot({path:`${output}/lost-response-portrait.png`,fullPage:true})
  await row('purge-line').getByRole('button',{name:'Resume permanent deletion',exact:true}).click()
  await confirm('purge-line')
  await page.getByLabel('Toast').filter({hasText:'line_deletion_receipt_unconfirmed'}).waitFor()
  assert.deepEqual(writes[4].body,lost,'unconfirmed receipt check must reuse the original operation')
  await page.reload(); await openRecycle()
  assert.equal(await row('purge-line').count(),1,'wrong receipt must preserve the original record')
  await row('purge-line').getByRole('button',{name:'Resume permanent deletion',exact:true}).click()
  await confirm('purge-line')
  await page.getByRole('heading',{name:'Recycle Bin is empty',exact:true}).waitFor()
  assert.equal(writes.length,6)
  assert.deepEqual(writes[5].body,lost,'successful receipt check must reuse the original operation')
  assert.equal(purges.get('purge-line'),1,'receipt replay must not purge again')
  assert.equal(await page.evaluate(() => localStorage.getItem('mdd.line-deletion.v1.purge-line')),null)
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth<=innerWidth),true,'portrait content must fit')
  await page.screenshot({path:`${output}/empty-recycle-portrait.png`,fullPage:true})
  assert.deepEqual(errors,[])
  console.log('PASS: mounted no-device recycle bin; disabled restore; cancel/typed-ID protection; retained same deletion operation across reload and lost success; rejected unrelated receipt; single purge; explicit history choice; no hardware or paid traffic')
} catch(error) {
  if(page) await page.screenshot({path:`${output}/failure.png`,fullPage:true}).catch(() => {})
  throw error
} finally {
  await browser?.close()
  await vite.close()
}
