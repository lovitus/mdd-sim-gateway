import assert from 'node:assert/strict'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'
import { mapReaderCards } from '../src/goV1Adapter.js'

const previousWindow = globalThis.window
globalThis.window = {location:{pathname:'/'}}
const { esimReaders, relatedSIMReaders } = await import('../src/mdd/esimAdapter.js')

const eid = '89049032000000000000000000000001'
const iccid = '8944000000000000001'
const history = {kind:'reader',agent_id:'mac-previous',observed_only:true,
  last_observed_at:'2026-10-03T03:00:00Z',reader:{reader_name:'Desktop reader',
    card_present:true,card_id:iccid,session_generation:'previous-session',
    secure_elements:[{slot_id:'se0',euicc:{eid,profiles_available:true,profile_management:true,
      profiles:[{iccid,state:'enabled',nickname:'Test SIM'}]}}]}}
const current = {kind:'reader',agent_id:'android-current',process_generation:'current-process',
  last_observed_at:'2026-10-09T10:00:00Z',reader:{reader_name:'USB reader',card_present:true,
    card_id:iccid,session_generation:'current-session',identity_state:'identified'}}
const cards = mapReaderCards([history,current])
const readers = esimReaders(cards)
assert.equal(readers[0].agent_id,'android-current')
assert.equal(readers[0].euicc,null,'do not transfer old chip data to the new attachment')
assert.equal(readers[1].last_observed_at,history.last_observed_at)
assert.deepEqual(relatedSIMReaders(readers[1],readers),[readers[0]])
for (const [label,change,expected] of [
  ['changed SIM',{iccid:'8944000000000000002'},false],
  ['different known chip',{euicc:{eid:'89049032000000000000000000000002'}},false],
  ['same chip',{euicc:{eid}},true],
  ['no chip report',{},true],
]) {
  const candidate={...readers[0],...change}
  assert.equal(relatedSIMReaders(readers[1],[readers[1],candidate]).length,expected?1:0,label)
}
assert.equal(esimReaders([{...cards[1],stale:true}])[0].online,false)
assert.equal(esimReaders([{...cards[1],present:false,remote:false}]).length,0)

const storage = globalThis.localStorage
globalThis.localStorage={getItem:()=> 'en',setItem(){}}
const server=await createServer({root:new URL('..',import.meta.url).pathname,
  logLevel:'silent',server:{middlewareMode:true,hmr:false},appType:'custom'})
try {
  const {default:Esim}=await server.ssrLoadModule('/src/mdd/views/Esim.jsx')
  const {I18nProvider}=await server.ssrLoadModule('/src/mdd/i18n.jsx')
  const render=value=>renderToStaticMarkup(React.createElement(I18nProvider,null,
    React.createElement(Esim,{cards:value,instances:[{id:'line-test',iccid,
      operations:{vowifi_call:{ready:true}}}]})))
  const html=render(cards)
  assert.match(html,/<optgroup label="Connected readers">/,
    'historical reader entries must be separated from current attachments')
  assert.match(html,/<optgroup label="Previous connections \(read-only\)">/)
  assert.match(html,/<option[^>]*selected=""[^>]*>[^<]*android-current/)
  assert.match(html,/SIM connected; this Agent has not reported eUICC management information/)
  assert.match(html,/View history \(read-only\)/)
  assert.doesNotMatch(html,/switching works from here/)
  const historicalHTML=render([cards[0]])
  assert.match(historicalHTML,/Historical connection: read-only/)
  assert.match(historicalHTML,/No current connection for this SIM is confirmed/)
  assert.doesNotMatch(historicalHTML,/>Stop line<|VoWiFi running on this reader/,
    'a ready line for the same ICCID does not make its previous attachment live')
  assert.match(historicalHTML,/<button[^>]*disabled=""[^>]*>Load<\/button>/)
  const unknownHTML=render([{...cards[1],stale:true}])
  assert.doesNotMatch(unknownHTML,/>Stop line<|VoWiFi running on this reader/)
  console.log('eSIM relocation: current attachment, read-only history, capability and chip identity boundaries passed')
} finally {
  globalThis.localStorage=storage
  globalThis.window=previousWindow
  await server.close()
}
