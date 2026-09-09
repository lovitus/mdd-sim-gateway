import assert from 'node:assert/strict'
import {diagnosticStatus,diagnosticReport,runAdvancedDiagnostics} from '../src/mdd/advancedDiagnostics.js'

assert.equal(diagnosticStatus('ready','ready',false),'unknown')
assert.equal(diagnosticStatus('not_run','line_disabled'),'skipped')
assert.equal(diagnosticStatus('blocked','pin_required'),'fail')
assert.equal(diagnosticStatus('unexpected','whatever'),'unknown')
const fixtures={runtime:{vcs_revision:'revision'},diagnostics:{checks:[{id:'agent.private-id',scope:'agent:private-id',status:'pass',code:'agent_wss_connected'}],agents:[{}],lines:[{line_id:'private-line',facts:[{layer:'ims',condition:'ready',code:'ready',fresh:false}]}]},components:{applying:false},exits:{exits:[{ready:true},{ready:false},{enabled:false,ready:false}]}}
const calls=[],rows=[]
await runAdvancedDiagnostics(async key=>{calls.push(key);return fixtures[key]}, {secure_context:true}, row=>rows.push(row))
assert.deepEqual(calls,['runtime','diagnostics','components','exits'])
assert.equal(rows.find(row=>row.id==='private-line:ims').status,'unknown')
assert.equal(rows.find(row=>row.id==='exit:2').status,'fail')
assert.equal(rows.find(row=>row.id==='exit:3').status,'skipped')
const report=JSON.stringify(diagnosticReport([...rows,{group:'browser',id:'private-token',kind:'active_test',status:'fail',code:'https://password@example.com',detail:'secret',observed_at:'now'}],'completed'))
assert.ok(!report.includes('private-id')&&!report.includes('private-line')&&!report.includes('private-token')&&!report.includes('password')&&!report.includes('secret'))
const failures=[]
await runAdvancedDiagnostics(async()=>{throw Object.assign(new Error('secret'),{status:401})},{},row=>failures.push(row))
assert.equal(failures.length,1)
assert.equal(failures[0].status,'fail')
let cancelled=false,count=0
await runAdvancedDiagnostics(async()=>{count++;cancelled=true;return fixtures.runtime},{},()=>assert.fail('cancelled results must not publish'),()=>cancelled)
assert.equal(count,1)
const invalid=[]
await runAdvancedDiagnostics(async key=>key==='diagnostics'?{}:fixtures[key],{},row=>invalid.push(row))
assert.ok(invalid.some(row=>row.code==='diagnostic_schema_invalid'&&row.status==='fail'))
console.log('Advanced diagnostic projection, cancellation and report contracts passed')
