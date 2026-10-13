// Real mounted views, REST adapters, coordinator and Web Audio; only HTTP/WS peers
// are synthetic. No carrier, production, microphone or allowance SMS is contacted.
import assert from 'node:assert/strict'
import {mkdir} from 'node:fs/promises'
import {createServer} from 'vite'
import {chromium} from 'playwright-core'

const output = process.env.MDD_BROWSER_EVIDENCE || 'browser-evidence/communication'
await mkdir(output,{recursive:true})
const vite = await createServer({server:{host:'127.0.0.1',port:0}})
let browser
const only = process.env.MDD_COMMUNICATION_CASE || ''
try {
  await vite.listen()
  browser = await chromium.launch({channel:'chrome',headless:true,args:['--use-fake-ui-for-media-stream','--use-fake-device-for-media-stream','--autoplay-policy=no-user-gesture-required']})
  const page = await browser.newPage({viewport:{width:1200,height:950}})
  const errors=[], sms=[], answers=[], ends=[], releases=[], queries=[]
  let holdSMS=null, holdAnswer=null, answerMode='unknown', active=false, ruleRevision=2, lastMessage=null
  page.on('pageerror',error=>errors.push(error.message))
  await page.routeWebSocket('**/fixture-media',socket=>{
    socket.onMessage(message=>{
      if(typeof message!=='string')return
      const data=JSON.parse(message)
      if(data.type==='browser.media.hello') {
        socket.send(JSON.stringify({type:'browser.media.claimed',challenge:'fixture',resume_ticket:'resume',connection_epoch:1}))
        socket.send(JSON.stringify({type:'browser.media.started',purpose:'canary'}))
        socket.send(JSON.stringify({type:'browser.media.ready'}))
      }
    })
  })
  await page.route('**/v1/**',async route=>{
    const request=route.request(), path=new URL(request.url()).pathname, method=request.method()
    const body=method==='GET'?null:request.postDataJSON()
    if(path.endsWith('/calls/status'))return route.fulfill({json:{sessions:[]}})
    if(path.endsWith('/vowifi/status'))return route.fulfill({json:{active_call:active?{call_id:'fixture-incoming'}:null}})
    if(path==='/v1/calls')return route.fulfill({json:{calls:[]}})
    if(path==='/v1/media/leases') {
      if(method==='DELETE'){releases.push(body);return route.fulfill({json:{released:true}})}
      return route.fulfill({json:{session_id:`fixture-lease-${answers.length}`,ws_path:'/fixture-media'}})
    }
    if(path.endsWith('/calls/incoming/answer')) {
      answers.push(body)
      if(answerMode==='late') {holdAnswer=()=>route.fulfill({json:{...body,accepted:true,code:'active'}});return}
      active=true
      return route.fulfill({status:503,json:{code:'provider_transport_failed'}})
    }
    if(path.endsWith('/calls/end')) {ends.push(body);active=false;return route.fulfill({json:{accepted:true,code:'ended',call_id:body.call_id}})}
    if(path.endsWith('/messages/send')) {
      sms.push(body)
      holdSMS=()=>route.fulfill({json:{accepted:true,code:'sent',message_id:body.message_id,operation_id:body.operation_id}})
      return
    }
    if(path==='/v1/messages/conversations')return route.fulfill({json:{conversations:lastMessage?[{line_id:'fixture-line',transport:'vowifi',peer:'+15550100001',count:1,last:lastMessage}]:[]}})
    if(path==='/v1/messages')return route.fulfill({json:{messages:lastMessage?[lastMessage]:[]}})
    if(path.endsWith('/allowance/query-rule'))return route.fulfill({json:{rule:{revision:ruleRevision,recipient:ruleRevision===2?'6700':'6701',body:ruleRevision===2?'BAL':'NEW',parser:'none'}}})
    if(path.endsWith('/allowance/query')) {queries.push(body);return route.fulfill({status:409,json:{code:'allowance_query_changed'}})}
    if(path.endsWith('/allowance'))return route.fulfill({json:{revision:1}})
    // Any unintended paid endpoint or extra route makes the run fail.
    errors.push(`unexpected ${method} ${path}`)
    return route.fulfill({status:500,json:{code:'unexpected_fixture_request'}})
  })
  const url=`${vite.resolvedUrls.local[0]}tests/communication.html`
  const settle=()=>page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))))
  const fill=async body=>{
    await page.getByPlaceholder('Recipient number e.g. +1...').fill('+15550100001')
    await page.getByRole('combobox',{name:'Send via',exact:true}).selectOption('vowifi')
    await page.getByPlaceholder('Type a message…').fill(body)
  }
  if(!only||only==='sms') {
    await page.goto(url);await fill('original SMS A')
    const firstSMS = page.waitForRequest(request=>request.url().endsWith('/messages/send'))
    await page.getByRole('button',{name:'Send',exact:true}).click()
    await firstSMS;await settle()
    await page.getByRole('button',{name:'Leave messages',exact:true}).click()
    await page.getByRole('button',{name:'Return to messages',exact:true}).click()
    await page.getByRole('button',{name:'+ New message',exact:true}).click()
    await fill('new draft B')
    assert.ok(holdSMS,'original SMS request reached HTTP')
    const firstResult=page.waitForResponse(response=>response.url().endsWith('/messages/send'))
    await holdSMS();await firstResult;await settle()
    assert.equal(await page.getByPlaceholder('Type a message…').inputValue(),'new draft B','SMS_OWNERSHIP: late A must not clear B')
    await page.getByRole('button',{name:'Send',exact:true}).click();await settle()
    assert.equal(sms.length,2,'SMS_JOURNAL: explicit new request must not discard A or stop at a discard dialog')
    const rows=await page.evaluate(()=>Object.entries(localStorage).filter(([key])=>key.startsWith('mdd_sms_receipt_')).map(([,value])=>JSON.parse(value)))
    assert.equal(rows.length,2,'SMS_JOURNAL: both operation identities must survive')
    assert.equal(rows.find(row=>row.id===sms[0].operation_id)?.state,'submitted')
    assert.equal(rows.find(row=>row.id===sms[1].operation_id)?.state,'unknown')
    const secondResult=page.waitForResponse(response=>response.url().endsWith('/messages/send'))
    await holdSMS();await secondResult;await settle()
    await page.screenshot({path:`${output}/sms-journal.png`,fullPage:true})
    // Historical line identity alone cannot silently select a replacement SIM.
    lastMessage={event_id:'history-a',line_id:'fixture-line',transport:'vowifi',kind:'received',sender:'+15550100001',body:'history without original card identity',received_at:new Date().toISOString()}
    await page.reload()
    await page.getByText('history without original card identity',{exact:true}).click()
    await page.getByRole('button',{name:'Replace SIM',exact:true}).click()
    await page.getByText('history without original card identity',{exact:true}).first().click()
    await page.getByPlaceholder('Type a message…').fill('unsafe reply')
    assert.ok(await page.getByRole('button',{name:'Send',exact:true}).isDisabled(),'REPLY_IDENTITY: historical line cannot authorize a new SIM')
  }
  if(!only||only==='call') {
    await page.goto(url)
    await page.getByRole('button',{name:'Answer fixture',exact:true}).click()
    await page.getByLabel('Call phase').filter({hasText:'start_unknown'}).waitFor()
    const refreshed=page.waitForResponse(response=>response.url().endsWith('/vowifi/status'))
    await page.getByRole('button',{name:'Refresh call',exact:true}).click();await refreshed;await settle()
    assert.equal(await page.getByLabel('Call phase').innerText(),'start_unknown','CALL_OWNERSHIP: shared active is not our answer receipt')
    await page.getByRole('button',{name:'Retry fixture',exact:true}).click();await settle()
    assert.equal(answers.length,1,'unknown incoming answer must not be resubmitted')
    await page.getByRole('button',{name:'Cancel fixture',exact:true}).click();await settle()
    assert.equal(ends.length,0,'CALL_OWNERSHIP: loser may release only its media')
    assert.equal(releases.length,1)
    active=false;answerMode='late'
    await page.getByRole('button',{name:'Answer fixture',exact:true}).click()
    await page.getByLabel('Call phase').filter({hasText:'signalling'}).waitFor();await settle()
    assert.ok(holdAnswer)
    const originalAnswer=answers.at(-1)
    await page.getByRole('button',{name:'Cancel fixture',exact:true}).click();await settle()
    assert.equal(ends.length,0)
    const ended=page.waitForResponse(response=>response.url().endsWith('/calls/end'))
    await holdAnswer();await ended;await settle()
    assert.equal(ends.length,1,'CALL_OWNERSHIP: cancelled late accepted answer must end the exact owned call')
    assert.equal(ends[0].call_id,originalAnswer.call_id)
    assert.equal(await page.getByLabel('Call phase').innerText(),'idle')
    await page.screenshot({path:`${output}/call-cancelled-late-answer.png`,fullPage:true})
  }
  if(!only||only==='allowance') {
    await page.goto(url);await fill('not sent')
    await page.getByRole('button',{name:'Query allowance',exact:true}).click()
    await page.getByRole('dialog').waitFor()
    assert.equal(await page.getByRole('button',{name:'Working…',exact:true}).count(),1,'ALLOWANCE_CONFIRMATION: confirmation must hold the synchronous lock')
    assert.ok(await page.getByRole('button',{name:'Working…',exact:true}).isDisabled())
    ruleRevision=3
    await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click()
    await settle()
    assert.equal(queries.length,1)
    assert.equal(queries[0].expected_card_id,'8944100000000000001','ALLOWANCE_CONFIRMATION: original card must be sent')
    assert.equal(queries[0].expected_rule_revision,2,'ALLOWANCE_CONFIRMATION: original rule revision must be sent')
    assert.equal(queries[0].recipient,'6700')
    assert.equal(queries[0].body,'BAL')
    // Replacing the selected SIM while the confirmation is open cancels admission.
    await page.getByRole('button',{name:'Query allowance',exact:true}).click()
    await page.getByRole('dialog').waitFor()
    await page.getByRole('button',{name:'Replace SIM',exact:true}).evaluate(button=>button.click())
    await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click();await settle()
    assert.equal(queries.length,1)
    await page.screenshot({path:`${output}/allowance-confirmation.png`,fullPage:true})
  }
  assert.deepEqual(errors,[])
  console.log(JSON.stringify({passed:true,case:only||'all',sms:sms.length,answers:answers.length,ends:ends.length,queries:queries.length}))
} finally {await browser?.close();await vite.close()}
