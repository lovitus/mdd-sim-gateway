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
  let incomingID='', endMode='success', terminalCalls=[]
  let delayOtherHistory=false, holdOtherHistory=null
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
    if(path.endsWith('/vowifi/status'))return route.fulfill({json:path.includes('/fixture-other/')?{}:{
      active_call:active?{call_id:incomingID}:null,pending_incoming_call:incomingID?{call_id:incomingID,caller:'+15550100001'}:null}})
    if(path==='/v1/calls')return route.fulfill({json:{calls:terminalCalls}})
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
    if(path.endsWith('/calls/end')) {
      ends.push(body)
      if(endMode==='unknown')return route.fulfill({status:503,json:{code:'provider_transport_failed'}})
      active=false;incomingID='';return route.fulfill({json:{...body,accepted:true,code:'ended'}})
    }
    if(method==='POST'&&(path.endsWith('/messages/send')||path.endsWith('/cellular/messages'))) {
      sms.push(body)
      holdSMS=(unknown=false)=>route.fulfill(unknown?{status:503,json:{code:'provider_transport_failed'}}:
        {json:{accepted:true,code:path.endsWith('/cellular/messages')?'cellular_sms_submitted':'sent',message_id:body.message_id,operation_id:body.operation_id}})
      return
    }
    if(path==='/v1/messages/conversations') {
      if(delayOtherHistory) {
        delayOtherHistory=false
        holdOtherHistory=()=>route.fulfill({json:{conversations:[{line_id:'fixture-other',transport:'vowifi',peer:'+15550100003',count:1,last:{body:'SIM B conversation',received_at:new Date().toISOString()}}]}})
        return
      }
      return route.fulfill({json:{conversations:lastMessage?[{line_id:'fixture-line',transport:'vowifi',peer:'+15550100001',count:1,last:lastMessage}]:[]}})
    }
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
  const confirmSend=async()=>{
    await page.getByRole('button',{name:'Send',exact:true}).click()
    assert.equal(await page.getByRole('dialog').count(),1,'SMS_JOURNAL: the actual new send needs a frozen paid confirmation')
    const submitted=page.waitForRequest(request=>request.method()==='POST'&&request.url().includes('/messages'))
    await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click()
    await submitted
  }
  const completeSMS=async(unknown=false)=>{
    const result=page.waitForResponse(response=>response.request().method()==='POST'&&response.url().includes('/messages'))
    await holdSMS(unknown);await result;await settle()
  }
  const journalRows=()=>page.evaluate(()=>Object.entries(localStorage).filter(([key])=>key.startsWith('mdd_sms_receipt_')).map(([,value])=>JSON.parse(value)))
  if(!only||only==='sms') {
    await page.goto(url);await fill('original SMS A')
    await confirmSend();await settle();await completeSMS(true)
    assert.equal(sms.length,1)
    await page.getByRole('button',{name:'Leave messages',exact:true}).click()
    await page.getByRole('button',{name:'Return to messages',exact:true}).click()
    await page.getByRole('button',{name:'+ New message',exact:true}).click()
    await fill('original SMS A')
    await page.getByRole('button',{name:'Send',exact:true}).click();await settle()
    assert.match(await page.getByRole('dialog').innerText(),/earlier SMS may already have been sent/,'SMS_JOURNAL: duplicate-payload new send must disclose unknown original')
    await page.getByRole('dialog').getByRole('button',{name:'Cancel',exact:true}).click();await settle()
    assert.equal(sms.length,1);assert.equal((await journalRows()).length,1)
    // Confirmation does not reserve a changed draft or a replacement card.
    await page.getByRole('button',{name:'Send',exact:true}).click()
    await page.getByPlaceholder('Type a message…').evaluate(input=>{
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value').set.call(input,'changed during confirmation')
      input.dispatchEvent(new Event('input',{bubbles:true}))
    })
    await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click();await settle()
    assert.equal(sms.length,1);assert.equal((await journalRows()).length,1)
    await page.getByRole('button',{name:'Send',exact:true}).click()
    await page.getByRole('button',{name:'Replace SIM',exact:true}).evaluate(button=>button.click())
    await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click();await settle()
    assert.equal(sms.length,1);assert.equal((await journalRows()).length,1)
    await page.reload()
    await page.getByRole('button',{name:'+ New message',exact:true}).click();await fill('original SMS A')
    await page.getByRole('combobox',{name:'Send via',exact:true}).selectOption('cellular')
    await confirmSend();await settle()
    assert.equal(sms.length,2,'SMS_JOURNAL: explicit separate send uses a new operation')
    assert.notEqual(sms[0].operation_id,sms[1].operation_id)
    const rows=await journalRows()
    assert.equal(rows.length,2,'SMS_JOURNAL: both operation identities must survive')
    assert.equal(rows.find(row=>row.id===sms[0].operation_id)?.state,'unknown')
    assert.equal(rows.find(row=>row.id===sms[1].operation_id)?.state,'unknown')
    assert.equal(rows.find(row=>row.id===sms[1].operation_id)?.payload.transport,'cellular')
    await completeSMS()
    await page.screenshot({path:`${output}/sms-journal.png`,fullPage:true})
    // Historical line identity alone cannot silently select a replacement SIM.
    lastMessage={event_id:'history-a',line_id:'fixture-line',transport:'vowifi',kind:'received',sender:'+15550100001',body:'history without original card identity',received_at:new Date().toISOString()}
    await page.reload()
    await page.getByText('history without original card identity',{exact:true}).click()
    await page.getByRole('button',{name:'Replace SIM',exact:true}).click()
    await page.getByText('history without original card identity',{exact:true}).first().click()
    await page.getByPlaceholder('Type a message…').fill('unsafe reply')
    assert.ok(await page.getByRole('button',{name:'Send',exact:true}).isDisabled(),'REPLY_IDENTITY: historical line cannot authorize a new SIM')
    await page.getByRole('button',{name:'+ New message',exact:true}).click();await fill('new message from replacement')
    await page.getByRole('button',{name:'Send',exact:true}).click()
    assert.match(await page.getByRole('dialog').innerText(),/8944100000000000002/,'REPLY_IDENTITY: explicit new send identifies current card')
    await page.getByRole('dialog').getByRole('button',{name:'Cancel',exact:true}).click()
  }
  if(!only||only==='sms-switch') {
    lastMessage=null
    for(const unknown of [false,true]) {
      await page.goto(url)
      await page.evaluate(()=>{
        for(const key of Object.keys(localStorage))if(key.startsWith('mdd_sms_'))localStorage.removeItem(key)
        const row={id:'other-receipt',lineID:'fixture-other',payload:{cardID:'8944100000000000003',transport:'vowifi',to:'+15550100003',body:'Only SIM B journal'},state:'submitted',created:Date.now()}
        localStorage.setItem('mdd_sms_receipt_other-receipt',JSON.stringify(row))
      })
      await page.reload();await fill('delayed SIM A')
      const before=sms.length
      const submitted=page.waitForRequest(request=>request.method()==='POST'&&request.url().includes('/messages'))
      await page.getByRole('button',{name:'Send',exact:true}).click()
      // The old candidate had no dialog; this case isolates journal ownership.
      if(await page.getByRole('dialog').count())await page.getByRole('dialog').getByRole('button',{name:'Confirm',exact:true}).click()
      await submitted;await settle();assert.equal(sms.length,before+1)
      await page.getByRole('combobox').first().selectOption('fixture-other');await settle()
      const draft=page.getByPlaceholder('Type a message…')
      if(await draft.isEnabled())await draft.fill('SIM B draft stays')
      await completeSMS(unknown)
      await page.locator('details > summary').click()
      assert.match(await page.locator('details').innerText(),/Only SIM B journal/,'SMS_VIEW: old A response must refresh the current B journal')
      assert.doesNotMatch(await page.locator('details').innerText(),/delayed SIM A/,'SMS_VIEW: old A journal must not replace B')
      assert.equal(await draft.inputValue(),'SIM B draft stays','SMS_VIEW: B remains editable while A is pending')
      assert.equal(await page.getByRole('combobox').first().inputValue(),'fixture-other')
    }
  }
  if(!only||only==='sms-history') {
    lastMessage=null
    for(const mode of ['send','receipt']) {
      delayOtherHistory=false;holdOtherHistory=null
      await page.goto(url)
      await page.evaluate(mode=>{
        for(const key of Object.keys(localStorage))if(key.startsWith('mdd_sms_'))localStorage.removeItem(key)
        const other={id:'other-receipt',lineID:'fixture-other',payload:{cardID:'8944100000000000003',transport:'vowifi',to:'+15550100003',body:'Only SIM B journal'},state:'submitted',created:Date.now()}
        localStorage.setItem('mdd_sms_receipt_other-receipt',JSON.stringify(other))
        if(mode==='receipt') {
          const original={id:'original-receipt',lineID:'fixture-line',payload:{cardID:'8944100000000000001',transport:'cellular',to:'+15550100001',body:'retained SIM A'},state:'unknown',created:Date.now()}
          localStorage.setItem('mdd_sms_receipt_original-receipt',JSON.stringify(original))
        }
      },mode)
      await page.reload()
      await page.getByRole('combobox',{name:'Message history scope',exact:true}).selectOption('line')
      if(mode==='send') {await fill('delayed SIM A');await confirmSend()}
      else {
        await page.locator('details > summary').click()
        const submitted=page.waitForRequest(request=>request.method()==='POST'&&request.url().includes('/messages'))
        await page.getByRole('button',{name:'Read stored SMS receipt',exact:true}).click();await submitted
      }
      await settle();delayOtherHistory=true
      const reading=page.waitForRequest(request=>request.url().includes('/messages/conversations'))
      await page.getByRole('combobox').first().selectOption('fixture-other');await reading;await settle()
      await page.getByPlaceholder('Type a message…').fill('SIM B draft stays')
      await completeSMS()
      assert.ok(holdOtherHistory,'B history is held until after the original completion callback')
      const loaded=page.waitForResponse(response=>response.url().includes('/messages/conversations'))
      await holdOtherHistory();await loaded;await settle()
      assert.equal(await page.getByText('SIM B conversation',{exact:true}).count(),1,'SMS_HISTORY: old A completion must not invalidate B history')
      assert.equal(await page.getByText('Loading conversations…',{exact:true}).count(),0,'SMS_HISTORY: B loading must end')
      if(!(await page.locator('details').evaluate(details=>details.open)))await page.locator('details > summary').click()
      assert.match(await page.locator('details').innerText(),/Only SIM B journal/)
      assert.equal(await page.getByPlaceholder('Type a message…').inputValue(),'SIM B draft stays')
    }
    delayOtherHistory=false
  }
  if(!only||only==='call'||only==='call-cancel') {
    incomingID='fixture-incoming-A';active=false;terminalCalls=[]
    await page.goto(url)
    await page.getByRole('button',{name:'Answer',exact:true}).click()
    await page.getByLabel('Call phase').filter({hasText:'start_unknown'}).waitFor()
    const refreshed=page.waitForResponse(response=>response.url().endsWith('/vowifi/status'))
    await page.getByRole('button',{name:'Refresh call',exact:true}).click();await refreshed;await settle()
    assert.equal(await page.getByLabel('Call phase').innerText(),'start_unknown','CALL_OWNERSHIP: shared active is not our answer receipt')
    await page.getByRole('button',{name:'Retry fixture',exact:true}).click();await settle()
    assert.equal(answers.length,1,'unknown incoming answer must not be resubmitted')
    await page.getByRole('button',{name:'Hang up',exact:true}).click();await settle()
    assert.equal(ends.length,0,'CALL_OWNERSHIP: loser may release only its media')
    assert.equal(releases.length,1)
    assert.equal(await page.getByRole('button',{name:'Answer',exact:true}).count(),0,'CALL_CANCEL: normal Answer must not reopen the same incoming request')
    await page.getByRole('button',{name:'Answer fixture',exact:true}).click()
    await settle();assert.equal(answers.length,1,'CALL_CANCEL: ordinary begin guard retains cancelled request')
    assert.equal(await page.getByLabel('Owned call').innerText(),'fixture-incoming-A')
    active=false;incomingID=''
    await page.getByRole('button',{name:'Refresh call',exact:true}).click();await settle()
    assert.equal(await page.getByLabel('Call phase').innerText(),'start_unknown','CALL_CANCEL: empty status is not terminal proof')
    terminalCalls=[{call_id:'fixture-incoming-A',line_id:'fixture-line',transport:'vowifi',status:'ended',ended_at:new Date().toISOString()}]
    await page.getByRole('button',{name:'Refresh call',exact:true}).click()
    await page.getByLabel('Call phase').filter({hasText:'idle'}).waitFor()
  }
  if(!only||only==='call'||only==='call-late') {
    incomingID='fixture-incoming-B';active=false;answerMode='late';terminalCalls=[]
    await page.goto(url)
    await page.getByRole('button',{name:'Answer',exact:true}).click()
    await page.getByLabel('Call phase').filter({hasText:'signalling'}).waitFor();await settle()
    assert.ok(holdAnswer)
    const originalAnswer=answers.at(-1)
    await page.getByRole('button',{name:'Hang up',exact:true}).click();await settle()
    assert.equal(ends.length,0)
    endMode='unknown';active=true
    const ended=page.waitForResponse(response=>response.url().endsWith('/calls/end'))
    await holdAnswer();await ended;await settle()
    assert.equal(ends.length,1,'CALL_OWNERSHIP: cancelled late accepted answer must end the exact owned call')
    assert.equal(ends[0].call_id,originalAnswer.call_id)
    assert.equal(await page.getByLabel('Call phase').innerText(),'ending','CALL_LATE: unconfirmed exact end must retain ownership')
    assert.equal(await page.getByLabel('Owned call').innerText(),originalAnswer.call_id)
    assert.equal(await page.getByRole('button',{name:'Answer',exact:true}).count(),0)
    active=false;incomingID=''
    terminalCalls=[{call_id:originalAnswer.call_id,line_id:'fixture-line',transport:'vowifi',status:'failed',ended_at:new Date().toISOString()}]
    await page.getByRole('button',{name:'Refresh call',exact:true}).click();await settle()
    assert.equal(await page.getByLabel('Call phase').innerText(),'ending','CALL_LATE: failed shared history is not authoritative terminal')
    await page.screenshot({path:`${output}/call-cancelled-late-answer.png`,fullPage:true})
    terminalCalls[0].status='ended'
    await page.getByRole('button',{name:'Refresh call',exact:true}).click()
    await page.getByLabel('Call phase').filter({hasText:'idle'}).waitFor()
  }
  if(!only||only==='allowance') {
    await page.goto(url)
    await page.getByRole('button',{name:'+ New message',exact:true}).click()
    await fill('not sent')
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
