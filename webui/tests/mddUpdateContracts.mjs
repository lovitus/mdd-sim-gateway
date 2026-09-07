import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {consumeUpdateCompletion,updateProgressOutcome,matchUpdateProgress} from '../src/mdd/updateProgress.js'
assert.equal(updateProgressOutcome({state:'succeeded'}),'complete')
assert.equal(updateProgressOutcome({state:'success'}),'unknown')
assert.equal(updateProgressOutcome({state:'requested'}),'pending')
const status={state:'succeeded',operation_id:'operation-a',target:'v2',updated_at:'2026-09-07T01:00:00Z'}
const once=consumeUpdateCompletion('',status)
assert.equal(once.notify,true)
assert.equal(consumeUpdateCompletion(once.key,{...status,updated_at:'2026-09-07T01:01:00Z'}).notify,false)
assert.equal(consumeUpdateCompletion('',{...status,operation_id:''}).notify,false)
assert.equal(matchUpdateProgress(status,'operation-b').accepted,false)
assert.equal(matchUpdateProgress(status,'',{previous:'operation-a',target:'v2'}).accepted,false)
const app=readFileSync(new URL('../src/mdd/App.jsx',import.meta.url),'utf8')
assert.equal(app.includes('api.applyUpdate(update.latest)'),true)
assert.equal(app.includes('api.applyUpdate()'),false)
assert.equal(app.includes('setTimeout(tick, 3000)'),false)
assert.equal(app.includes('setTimeout(tick, 10000)'),false)
assert.equal(app.includes('600000'),true)
assert.equal(app.includes('matchUpdateProgress(status,expected.current,uncertain.current)'),true)
assert.equal(app.includes('<UpdateModal update={updateDialog}'),true)
console.log('Customized MDD update target, operation identity and bounded observation contracts passed')
