import assert from 'node:assert/strict'
import fs from 'node:fs'
import { runtimeHealthView, registrationOutcomeMessage } from '../src/runtimeHealth.js'
import { mapCatalogLine, mapDevice } from '../src/goV1Adapter.js'
const detail='dpd_enabled=true;dpd_dead=false;dpd_missed=0;ims_registered=false;ims_failures=4;ims_status=503;ims_failure=ims_register_rejected;ims_next_attempt_at=2026-09-18T02:00:00Z;ims_retry_after_until=2026-09-18T03:00:00Z'
const projection={line_id:'l1',facts:[{layer:'vowifi_runtime',fresh:true,condition:'ready',detail},{layer:'admission',fresh:true,detail:'core_recovering=true;core_recovery_due_at=2026-09-18T03:00:00Z'}],operations:{}}
const health=runtimeHealthView(projection)
assert.equal(health.state,'recovering')
assert.equal(health.ims_registered,false)
assert.equal(health.ims_failures,4)
assert.equal(health.core_recovery_due_at,health.ims_retry_after_until)
assert.equal(runtimeHealthView({...projection,facts:[{...projection.facts[0],fresh:false}]}),null)
assert.equal(runtimeHealthView({facts:[{layer:'vowifi_runtime',fresh:true,detail:'manual_register=true'}]}),null)
const disabled={layer:'vowifi_intent',fresh:true,code:'vowifi_disabled'}
for(const runtime of [[],[{layer:'vowifi_runtime',fresh:false,detail}]]) {
  assert.deepEqual(runtimeHealthView({facts:[disabled,...runtime]}),{state:'off',telemetry_available:false})
  assert.equal(runtimeHealthView({facts:[{...disabled,fresh:false},...runtime]}),null)
}
const line={id:'l1',enabled:true,card_id:'card'}
const device={id:'d1',kind:'reader',reader:{card_id:'card'},endpoints:[{association:'exact',operation_candidate:true,line:{id:'l1'}}]}
assert.deepEqual(mapCatalogLine(line,projection).runtime_health,mapDevice(device,[line],[projection]).vowifi.health)
for (const code of ['ims_recovering','ims_retry_wait']) assert.doesNotMatch(registrationOutcomeMessage({code}),/refreshed\./)
assert.match(registrationOutcomeMessage({code:'ims_recovering'}),/no duplicate/)
assert.match(registrationOutcomeMessage({code:'ims_registered'}),/refreshed/)
const source=fs.readFileSync(new URL('../src/mdd/views/UnifiedPages.jsx',import.meta.url),'utf8')
assert.ok(source.includes('<RuntimeHealth health={x.vowifi?.health} compact />'))
assert.ok(source.includes('<RuntimeHealth health={device?.vowifi?.health} compact={compact} />'))
assert.ok(source.includes('registrationOutcomeMessage(result, language)'))
console.log('runtime health: stale/legacy, shared list/detail, retry and manual outcome contracts passed')
