import assert from 'node:assert/strict'
import {mkdir} from 'node:fs/promises'
import {createServer} from 'vite'
import {chromium} from 'playwright-core'

const output=process.env.MDD_BROWSER_EVIDENCE || 'browser-evidence/esim-operations'
await mkdir(output,{recursive:true})
const vite=await createServer({root:new URL('..',import.meta.url).pathname,server:{host:'127.0.0.1',port:0}})
const firstEID='89049032000000000000000000000001'
let eid=firstEID,agent='agent-a',browser,page
const writes=[],queries=[],errors=[]
const scenario=process.env.MDD_ESIM_SCENARIO || ''
let heldDownload,holdDownload=false,downloadHeld
try {
  await vite.listen()
  browser=await chromium.launch({channel:'chrome',headless:true})
  page=await browser.newPage({viewport:{width:1120,height:900}})
  const attach=async()=>{
  page.on('pageerror',error=>errors.push(error.message))
  await page.route('**/v1/**',async route=>{
    const request=route.request(),url=new URL(request.url())
    if(url.pathname==='/v1/euiccs')return route.fulfill({json:{euiccs:[{agent_id:agent,reader_name:'Test reader',euicc:{eid,inventory_refresh:true,profiles_available:true,profile_management:true,profile_download:true,profiles:[]}}]}})
    if(url.pathname.endsWith('/deletions'))return route.fulfill({json:{deletions:[]}})
    if(url.pathname.endsWith('/notification-archives'))return route.fulfill({json:{archives:[]}})
    if(request.method()==='POST' && url.pathname.endsWith('/downloads')) {
      const input=request.postDataJSON()
      writes.push({eid:url.pathname.split('/')[3],...input})
      const records=await page.evaluate(()=>Object.keys(localStorage).filter(key=>key.startsWith('mdd_euicc_')).map(key=>localStorage.getItem(key)))
      assert.ok(records.some(value=>value.includes(input.operation_id)),'persist identity before the network write')
      if(holdDownload){holdDownload=false;heldDownload={route,input,eid:url.pathname.split('/')[3]};downloadHeld?.();return}
      return route.fulfill({json:{eid:url.pathname.split('/')[3],operation_id:input.operation_id,action:'start',job:{state:'uncertain',stage:'es10b_load_bound_profile_package',code:'install_unknown'}}})
    }
    if(request.method()==='GET' && url.pathname.includes('/downloads/')) {
      queries.push(url.pathname)
      return route.fulfill({json:{eid:url.pathname.split('/')[3],operation_id:url.pathname.split('/')[5],action:'status',job:{state:'uncertain',code:'install_unknown'}}})
    }
    errors.push(`${request.method()} ${url.pathname}`)
    return route.fulfill({status:500,json:{code:'unexpected_fixture_request'}})
  })
  }
  await attach()
  const url=`${vite.resolvedUrls.local[0]}tests/esimOperations.html`
  const open=async()=>{
    const button=page.getByRole('button',{name:'Download eSIM',exact:true})
    await button.waitFor()
    await page.waitForFunction(()=>[...document.querySelectorAll('button')].some(button=>button.textContent.trim()==='Download eSIM'&&!button.disabled))
    await button.click()
    await page.getByPlaceholder('LPA:1$smdp.example.com$MATCHING-ID').fill('LPA:1$smdp.example.test$synthetic-only')
    await page.getByRole('dialog',{name:'Download eSIM'}).getByRole('button',{name:'Download',exact:true}).click()
  }
  const fresh=async(query='',seed=null)=>{
    await page.close()
    page=await browser.newPage({viewport:{width:1120,height:900}})
    await attach()
    eid=firstEID;agent='agent-a'
    if(seed)await page.addInitScript(seed=>localStorage.setItem(seed.key,JSON.stringify(seed.value)),seed)
    await page.goto(url+query)
  }
  if(!scenario || scenario==='basic'){
  await page.goto(url)
  await open()
  await page.getByRole('dialog',{name:'Confirm',exact:true}).getByRole('button',{name:'Confirm',exact:true}).click()
  await page.getByRole('dialog',{name:'Download eSIM'}).waitFor({state:'hidden'})
  assert.equal(writes.length,1)
  // This assertion is the old-page counterexample, not a wait for a missing node.
  assert.equal(await page.getByRole('region',{name:'Download history'}).count(),1,'ESIM_UNKNOWN_RETAINED: uncertain download needs a retained original-operation entry')
  let history=page.getByRole('region',{name:'Download history'})
  assert.ok((await history.innerText()).includes(writes[0].operation_id))
  await page.reload()
  history=page.getByRole('region',{name:'Download history'})
  await history.waitFor()
  assert.ok((await history.innerText()).includes(writes[0].operation_id),'reload retains unknown')
  await open()
  assert.ok((await page.getByRole('dialog',{name:'Confirm',exact:true}).innerText()).includes('may already be installed'))
  await page.getByRole('dialog',{name:'Confirm',exact:true}).getByRole('button',{name:'Cancel',exact:true}).click()
  assert.equal(writes.length,1)
  await page.getByRole('dialog',{name:'Download eSIM'}).getByRole('button',{name:'Download',exact:true}).click()
  await page.getByRole('dialog',{name:'Confirm',exact:true}).getByRole('button',{name:'Confirm',exact:true}).click()
  await page.getByRole('dialog',{name:'Download eSIM'}).waitFor({state:'hidden'})
  assert.equal(writes.length,2)
  assert.notEqual(writes[0].operation_id,writes[1].operation_id)
  assert.ok((await history.innerText()).includes(writes[0].operation_id))
  eid='89049032000000000000000000000002'
  await page.getByRole('button',{name:'Other chip'}).click()
  agent='agent-b'
  await page.getByRole('button',{name:'Move reader'}).click()
  assert.ok((await history.innerText()).includes(firstEID))
  const readback=page.waitForResponse(response=>response.url().includes('/downloads/') && response.request().method()==='GET')
  await history.getByRole('button',{name:'Read original operation'}).first().click()
  await readback
  assert.equal(queries.length,1)
  assert.ok(queries[0].includes(firstEID),'readback uses original EID after chip and reader replacement')
  assert.equal(writes.length,2,'readback never downloads again')
  await page.screenshot({path:`${output}/unknown-history-desktop.png`,fullPage:true})
  await page.setViewportSize({width:390,height:844})
  await page.screenshot({path:`${output}/unknown-history-mobile.png`,fullPage:true})
  }
  if(!scenario || scenario==='legacy'){
    const original={eid:firstEID,operation_id:'legacy-original'}
    await fresh('',{key:'mdd_euicc_download_'+JSON.stringify(['previous-agent','old-reader']),value:original})
    await page.getByRole('button',{name:'Download eSIM',exact:true}).waitFor()
    await page.waitForFunction(()=>[...document.querySelectorAll('button')].some(b=>b.textContent.trim()==='Download eSIM'&&!b.disabled))
    const history=page.getByRole('region',{name:'Download history'})
    assert.equal(await history.count(),1,'ESIM_LEGACY: original reader history must migrate without selecting it')
    assert.ok((await history.innerText()).includes(original.operation_id))
    const before=writes.length
    await open()
    assert.ok((await page.getByRole('dialog',{name:'Confirm',exact:true}).innerText()).includes('may already be installed'))
    await page.getByRole('dialog',{name:'Confirm',exact:true}).getByRole('button',{name:'Cancel',exact:true}).click()
    assert.equal(writes.length,before)
  }
  if(!scenario || scenario==='empty'){
    const original={eid:firstEID,operation_id:'offline-original',job:{state:'uncertain'},created_at:1}
    await fresh('?empty=1',{key:`mdd_euicc_operation_${firstEID}_${original.operation_id}`,value:original})
    await page.getByText('No SIM present. Insert an eUICC into a PC/SC reader to manage profiles.',{exact:true}).waitFor()
    const history=page.getByRole('region',{name:'Download history'})
    assert.equal(await history.count(),1,'ESIM_EMPTY: offline hardware must not hide original-operation history')
    const before=writes.length
    const reply=page.waitForResponse(response=>response.url().endsWith('/downloads/offline-original'))
    await history.getByRole('button',{name:'Read original operation'}).click()
    await reply
    assert.equal(writes.length,before)
    assert.equal(await page.getByRole('button',{name:'Download eSIM',exact:true}).count(),0)
  }
  if(!scenario || scenario==='late')for(const outcome of ['completed','unknown']){
    await fresh()
    holdDownload=true;heldDownload=null
    const reachedPeer=new Promise(resolve=>{downloadHeld=resolve})
    await open()
    await page.getByRole('dialog',{name:'Confirm',exact:true}).getByRole('button',{name:'Confirm',exact:true}).click()
    await page.waitForFunction(()=>Object.keys(localStorage).some(key=>key.startsWith('mdd_euicc_operation_')))
    await reachedPeer
    await page.getByRole('region',{name:'Download history'}).waitFor()
    await page.getByRole('dialog',{name:'Download eSIM'}).getByRole('button',{name:'Cancel',exact:true}).click()
    await page.getByRole('button',{name:'Dismiss',exact:true}).click()
    await page.getByRole('button',{name:'Download eSIM',exact:true}).click()
    await page.getByPlaceholder('LPA:1$smdp.example.com$MATCHING-ID').fill('LPA:1$smdp.example.test$new-draft')
    assert.ok(heldDownload,'synthetic peer received the original POST')
    const old=heldDownload
    const reply=page.waitForResponse(response=>response.request().method()==='POST'&&response.url().endsWith('/downloads'))
    if(outcome==='completed')await old.route.fulfill({json:{eid:old.eid,operation_id:old.input.operation_id,action:'start',job:{state:'completed'}}})
    else await old.route.fulfill({status:502,json:{code:'transport_unknown'}})
    await reply
    // Wait for the actual promise continuation/render, not a wall-clock delay.
    await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))))
    assert.equal(await page.getByRole('dialog',{name:'Download eSIM'}).count(),1,'ESIM_LATE: old POST must not close the new draft')
    assert.equal(await page.getByPlaceholder('LPA:1$smdp.example.com$MATCHING-ID').inputValue(),'LPA:1$smdp.example.test$new-draft')
    const stored=await page.evaluate(key=>JSON.parse(localStorage.getItem(key)),`mdd_euicc_operation_${old.eid}_${old.input.operation_id}`)
    assert.equal(stored.operation_id,old.input.operation_id)
    if(outcome==='completed')assert.equal(stored.job.state,'completed')
    else assert.equal(stored.job,null)
  }
  assert.deepEqual(errors,[])
  console.log('eSIM unknown retention, new-attempt confirmation, location changes and original readback passed')
} finally {await browser?.close();await vite.close()}
