import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
import { createServer } from 'vite'
import { chromium } from 'playwright-core'

// Mount the production root. Only authenticated REST and the control socket
// boundary are synthetic; no component, adapter or confirmation is replaced.
const output = process.env.MDD_BROWSER_EVIDENCE || 'browser-evidence/update-entry'
await mkdir(output,{recursive:true})
const vite = await createServer({server:{host:'127.0.0.1',port:0}})
const errors = [], writes = []
let latest = '2.0.0', available = true, rejectChanged = false
let progress = {schema_version:1,state:'idle',updated_at:new Date().toISOString()}
const release = () => ({ok:true,current:'1.0.0',latest,comparison_known:true,update_available:available,
  release_url:'https://github.com/example/project/releases/tag/v'+latest,notes:'Synthetic verified release',repository:'example/project'})
let browser
try {
  await vite.listen()
  browser = await chromium.launch({channel:'chrome',headless:true})
  const page = await browser.newPage({viewport:{width:1280,height:900}})
  await page.addInitScript(() => localStorage.setItem('mdd-language','en'))
  page.on('pageerror',error => errors.push(error.message))
  await page.routeWebSocket('**/v1/browser/ws',() => {})
  await page.route('**/*',async route => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname
    if (!path.startsWith('/v1/') && !path.startsWith('/api/auth/')) return route.continue()
    if (request.method()==='GET') {
      const snapshots = {
        '/api/auth/status':{configured:true,authenticated:true,csrf:'synthetic-csrf'},
        '/v1/lines':{schema_version:1,at:new Date().toISOString(),lines:[]},
        '/v1/catalog/lines':{schema_version:1,revision:1,defaults:{},lines:[]},
        '/v1/devices':{schema_version:1,at:new Date().toISOString(),devices:[]},
        '/v1/agents':{schema_version:1,agents:[]},
        '/v1/egress/exits':{schema_version:1,exits:[]},
        '/v1/system/runtime':{schema_version:1,build_version:'1.0.0'},
        '/v1/system/status':{schema_version:1,update:release()},
        '/v1/system/alerts':{schema_version:1,alerts:[]},
        '/v1/system/update/check':release(),
        '/v1/system/update/progress':progress,
      }
      if (Object.hasOwn(snapshots,path)) return route.fulfill({json:snapshots[path]})
    }
    if (request.method()==='POST' && path==='/v1/system/update/apply') {
      writes.push(request.postDataJSON())
      if (rejectChanged) return route.fulfill({status:409,json:{code:'update_target_changed'}})
      assert.deepEqual(request.postDataJSON(),{expected_target:latest})
      progress = {schema_version:1,operation_id:'synthetic-update-one',state:'unknown',phase:'restart',
        error_code:'update_restart_unknown',target:latest,updated_at:new Date().toISOString()}
      return route.fulfill({status:202,json:{ok:true,operation_id:progress.operation_id,target:latest}})
    }
    errors.push(`unexpected API ${request.method()} ${path}`)
    return route.fulfill({status:500,json:{code:'unexpected_fixture_request'}})
  })
  await page.goto(vite.resolvedUrls.local[0]+'#/devices')
  const badge = page.locator('.u-version.has-update')
  await badge.waitFor({state:'visible'})
  await badge.click()
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
  assert.equal(await page.getByRole('dialog').count(),1,'SIDEBAR_UPDATE_ENTRY: sidebar must open the existing update dialog')
  const dialog = page.getByRole('dialog')
  await dialog.getByRole('button',{name:'Update now',exact:true}).waitFor({state:'visible'})
  assert.ok((await dialog.innerText()).includes('v2.0.0'))
  await page.screenshot({path:`${output}/sidebar-confirmation.png`,fullPage:true})
  await dialog.getByRole('button',{name:'Cancel',exact:true}).click()
  assert.deepEqual(writes,[],'opening and cancelling must never start an update')

  rejectChanged = true
  await badge.click()
  await dialog.getByRole('button',{name:'Update now',exact:true}).click()
  await dialog.getByText('update_target_changed',{exact:true}).waitFor()
  assert.deepEqual(writes,[{expected_target:'2.0.0'}])
  await dialog.getByRole('button',{name:'Cancel',exact:true}).click()
  latest='2.1.0'; rejectChanged=false
  await page.reload(); await badge.waitFor({state:'visible'}); await badge.click()
  await dialog.getByRole('button',{name:'Update now',exact:true}).waitFor()
  assert.ok((await dialog.innerText()).includes('v2.1.0'),'reopening must use current metadata, not the prior confirmation')
  await dialog.getByRole('button',{name:'Update now',exact:true}).click()
  await dialog.getByText('Update outcome unknown',{exact:true}).waitFor()
  await page.screenshot({path:`${output}/retained-update-unknown.png`,fullPage:true})
  await dialog.getByRole('button',{name:'Close',exact:true}).click()
  await page.reload(); await badge.waitFor({state:'visible'}); await badge.click()
  await dialog.getByText('Update outcome unknown',{exact:true}).waitFor()
  assert.equal(await dialog.getByRole('button',{name:'Update now',exact:true}).count(),0,'existing unknown operation must not offer a second update')
  assert.deepEqual(writes,[{expected_target:'2.0.0'},{expected_target:'2.1.0'}])
  assert.deepEqual(errors,[])
  console.log('PASS: production sidebar confirmation, cancel, changed target and original unknown operation')
} finally {
  await browser?.close()
  await vite.close()
}
