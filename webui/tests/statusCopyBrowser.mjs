import assert from 'node:assert/strict'
import {mkdir} from 'node:fs/promises'
import {createServer} from 'vite'
import {chromium} from 'playwright-core'

const output = process.env.MDD_BROWSER_EVIDENCE || 'browser-evidence/status-copy'
await mkdir(output, {recursive:true})
const vite = await createServer({server:{host:'127.0.0.1',port:0}})
let browser
try {
  await vite.listen()
  browser = await chromium.launch({channel:'chrome',headless:true})
  for (const language of ['zh','en']) {
    const expected = language === 'zh'
      ? 'VoWiFi 网关（ePDG）返回认证失败；仅凭此状态无法确定具体原因。'
      : 'The VoWiFi gateway (ePDG) reported an authentication failure. This status does not identify the specific cause.'
    for (const code of ['swu_authentication_failed','swu_proxy_dns_unavailable','reg_rejected']) {
      const page = await browser.newPage({viewport:{width:1200,height:1000}})
      const errors = []
      page.on('pageerror',error=>errors.push(error.message))
      await page.route('**/v1/**',async route=>{
        const request=route.request(), url=new URL(request.url())
        assert.equal(request.method(),'GET','Status views must not mutate state')
        assert.equal(url.pathname,'/v1/lines/auth-fixture/availability')
        await route.fulfill({json:{start:1000,end:1200,summary:{down:100,outages:1},segments:[
          {start:1000,end:1100,state:'down',reason:code,
            detail:JSON.stringify({code:'server_epdg_identity_rejected',peer:'private-detail-must-not-leak'})},
        ]}})
      })
      await page.goto(`${vite.resolvedUrls.local[0]}tests/hardwareImei.html?status-copy&language=${language}&code=${code}`)
      const status=page.locator('.u-line-activity').first()
      if (code==='swu_authentication_failed') {
        assert.ok((await status.innerText()).includes(expected),'AUTH_STATUS_COPY_MISSING')
        await status.locator('details summary').click()
        assert.equal(await status.locator('code').first().innerText(),code)
      } else {
        assert.ok(!(await status.innerText()).includes(expected),'Unrelated failure was reclassified')
      }
      const history=page.locator('.u-uptime-table')
      await history.locator('summary').click()
      const row=history.locator('tbody tr').first()
      await row.waitFor()
      if (code==='swu_authentication_failed') {
        assert.ok((await row.innerText()).includes(expected),'AUTH_HISTORY_COPY_OVERRIDDEN')
        assert.equal(await row.locator('code').innerText(),code)
        assert.ok(!(await row.innerText()).includes('private-detail-must-not-leak'))
        await page.screenshot({path:`${output}/${language}.png`,fullPage:true})
      } else {
        assert.ok(!(await row.innerText()).includes(expected),'Unrelated history was reclassified')
        assert.ok((await row.innerText()).includes('private-detail-must-not-leak'),'Existing non-auth detail behavior changed')
      }
      assert.deepEqual(errors,[])
      await page.close()
    }
  }
  console.log('Status and history preserve typed authentication scope, bilingual copy and raw code; no mutations')
} finally {await browser?.close();await vite.close()}
