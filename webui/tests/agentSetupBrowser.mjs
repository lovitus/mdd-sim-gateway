import assert from 'node:assert/strict'
import {mkdir} from 'node:fs/promises'
import {createServer} from 'vite'
import {chromium} from 'playwright-core'

const output=process.env.MDD_BROWSER_EVIDENCE || 'browser-evidence/agent-setup'
await mkdir(output,{recursive:true})
const vite=await createServer({server:{host:'127.0.0.1',port:0}})
const writes=[],errors=[]
let browser, page, lost=true
const credentials={mode:'scoped',legacy_fallback_enabled:false,active:['existing-agent'],revoked:[]}
try {
  await vite.listen()
  browser=await chromium.launch({channel:'chrome',headless:true})
  page=await browser.newPage({viewport:{width:1100,height:850}})
  page.on('pageerror',error=>errors.push(error.message))
  await page.route('**/*',async route=>{
    const request=route.request(),path=new URL(request.url()).pathname
    if(path==='/v1/agents')return route.fulfill({json:{schema_version:1,agents:[{agent_id:'existing-agent'}]}})
    if(path==='/api/auth/status')return route.fulfill({json:{authenticated:true,csrf:'synthetic-csrf'}})
    if(path!=='/api/auth/agent-credentials')return route.continue()
    if(request.method()==='GET')return route.fulfill({json:credentials})
    const body=request.postDataJSON(); writes.push(body)
    assert.equal(request.headers()['x-mdd-csrf-token'],'synthetic-csrf')
    assert.equal(body.action,'create','Add must not call issue/rotate')
    if(!credentials.active.includes(body.agent_id))credentials.active.push(body.agent_id)
    if(lost){lost=false;return route.abort('failed')}
    return route.fulfill({json:{ok:true,agent_id:body.agent_id,agent_token:'synthetic-scoped-token-that-is-long-enough',credentials}})
  })
  await page.goto(vite.resolvedUrls.local[0]+'tests/agentSetup.html')
  await page.getByRole('heading',{name:'Agent credentials',exact:true}).waitFor()
  const add=page.getByRole('button',{name:'Add Agent',exact:true})
  assert.equal(await add.count(),1,'AGENT_ADD_ENTRY: Add must be distinct from rotation')
  await add.click()
  await page.getByLabel('Agent connection URL',{exact:true}).fill('https://gateway.example:8443')
  await page.getByLabel('Certificate trust',{exact:true}).selectOption('pin')
  await page.getByRole('button',{name:'Use this Core certificate',exact:true}).click()
  await page.getByRole('checkbox').check()
  await page.getByRole('button',{name:'Create credential',exact:true}).click()
  await page.getByRole('dialog').getByRole('button',{name:'Cancel',exact:true}).click()
  assert.equal(writes.length,0,'cancel may not create credentials')
  await page.getByRole('button',{name:'Create credential',exact:true}).dblclick()
  assert.equal(await page.getByRole('dialog').count(),1,'double-click must produce one confirmation')
  await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click()
  await page.getByRole('alert').waitFor()
  assert.equal(writes.length,1)
  await page.reload()
  await page.getByRole('button',{name:'Retry this enrollment',exact:true}).click()
  await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click()
  await page.getByLabel('Agent connection package',{exact:true}).waitFor()
  assert.deepEqual(writes[1],writes[0],'lost response/reload must preserve the original enrollment')
  assert.equal(credentials.active.length,2,'existing Agent remains and exactly one Agent was added')
  const input=page.getByLabel('Agent connection package',{exact:true})
  assert.equal(await input.getAttribute('type'),'password')
  const pkg=JSON.parse(await input.inputValue())
  assert.deepEqual(Object.keys(pkg).sort(),['kind','version','agent_id','server_url','server_token','tls_mode','tls_sha256'].sort())
  assert.equal(pkg.agent_id,writes[0].agent_id)
  assert.equal(pkg.tls_sha256,'a'.repeat(64))
  await page.screenshot({path:`${output}/desktop-created.png`,fullPage:true})
  await page.setViewportSize({width:390,height:844})
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'primary mobile layout must not overflow')
  await page.screenshot({path:`${output}/mobile-created.png`,fullPage:true})
  assert.deepEqual(errors,[])
  console.log('PASS: Add, cancel, double-click, lost response, retained identity, masked strict package; existing credentials untouched')
} catch (error) {
  await page?.screenshot({path:`${output}/failure.png`,fullPage:true})
  throw error
} finally { await browser?.close(); await vite.close() }
