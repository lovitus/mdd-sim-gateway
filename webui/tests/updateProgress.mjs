import assert from 'node:assert/strict'
import { matchUpdateProgress, updateProgressOutcome } from '../src/updateProgress.js'

for (const [state, result] of Object.entries({ idle:'idle', requested:'pending', running:'pending', succeeded:'complete', failed:'failed', unknown:'unknown', success:'unknown' })) {
  assert.equal(updateProgressOutcome({state}), result)
}
assert.equal(matchUpdateProgress({operation_id:'old',state:'succeeded'},'new').accepted,false)
const uncertain = {previous:'old',target:'2.4.0'}
for (const status of [{state:'idle'},{state:'succeeded',operation_id:'old',target:'2.4.0'},{state:'running',operation_id:'new',target:'2.5.0'}]) {
  assert.equal(matchUpdateProgress(status,'',uncertain).code,'update_request_outcome_unknown')
}
assert.deepEqual(matchUpdateProgress({state:'running',operation_id:'new',target:'2.4.0'},'',uncertain),{accepted:true,operation:'new',outcome:'pending'})
assert.equal(matchUpdateProgress({state:'succeeded',operation_id:'new'},'new').outcome,'complete')
console.log('Go update operation identity and outcome tests passed')
