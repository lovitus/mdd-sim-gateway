import assert from 'node:assert/strict'
globalThis.window = { location:{pathname:'/'} }
const { conversationRows, mergeMessagePages, historyAPI, diagnosticLogView } = await import('../src/mdd/historyAdapter.js')
const { api:go } = await import('../src/api.js')
const at = '2026-09-07T01:00:00Z'
const later = '2026-09-07T02:00:00Z'
const threads = conversationRows([
  {line_id:'line-a',transport:'cellular',peer:'same-peer',count:2,last:{body:'cellular',received_at:at}},
  {line_id:'line-b',transport:'cellular',peer:'same-peer',count:1,last:{body:'other line',received_at:at}},
  {line_id:'line-a',transport:'vowifi',peer:'same-peer',count:1,last:{body:'vowifi',received_at:at}},
])
assert.equal(threads.length, 3)
assert.equal(new Set(threads.map(item => item.key)).size, 3)
assert.equal(conversationRows(threads.map(item => ({...item,count:item.n})), 'line-b').length, 1)
const submitted = { event_id:'submit-1', line_id:'line-a', transport:'cellular', message_id:'message',
  kind:'submitted', part:1, recipient:'same-peer', body:'fixture body', state:'submitted', received_at:at }
const second = {...submitted,event_id:'submit-2',part:2}
const delivered = {...submitted,event_id:'delivery-new',kind:'delivery',state:'delivered',received_at:later}
const failed = {...delivered,event_id:'delivery-old',state:'failed',received_at:at,error:'old failure'}
const rows = mergeMessagePages([{messages:[submitted, second, delivered]}, {messages:[submitted, failed]}])
assert.equal(rows.length, 2)
assert.equal(rows.find(item => item.part === 1).status, 'delivered')
assert.equal(rows.find(item => item.part === 2).status, 'sent')
assert.equal(rows.find(item => item.part === 1).body, 'fixture body')
const requests = []
go.messagePageV1 = async (...args) => { requests.push(args); return {messages:[submitted],next_before:'42'} }
const page = await historyAPI.messages('line-a','same-peer','cellular','84')
assert.deepEqual(requests,[['line-a','cellular','same-peer','84']])
assert.equal(page.next_before,'42')
go.deleteMessageHistoryV1 = async body => body
assert.deepEqual(await historyAPI.deleteMessages('line-a',{peer:'same-peer',transport:'cellular'}),
  {line_id:'line-a',transport:'cellular',peer:'same-peer'})
assert.throws(() => historyAPI.deleteMessages('',{all:true}), /message_history_line_required/)
assert.throws(() => historyAPI.messages('line-a','same-peer','auto'), /message_history_scope_required/)
const diagnostic = diagnosticLogView([
  {source:'agent',layer:'reader',condition:'unknown',code:'reader_unavailable',received_at:at},
  {source:'provider',layer:'ims',condition:'ready',code:'registered',detail:'fixture'},
  {source:'core',layer:'intent',condition:'blocked',code:'disabled'},
])
assert.match(diagnostic.all,/reader_unavailable/)
assert.match(diagnostic.agent,/unknown reader_unavailable/)
assert.doesNotMatch(diagnostic.agent,/registered/)
assert.match(diagnostic.provider,/ims ready registered fixture/)
assert.match(diagnostic.core,/intent blocked disabled/)
assert.equal(Object.hasOwn(diagnostic,'charon'),false)
console.log('Customized MDD history scope, pagination and per-part delivery contracts passed')
