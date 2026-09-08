import assert from 'node:assert/strict'
import {runCallStabilityTest} from '../src/mdd/callStability.js'

function fixture({duration=10,measured=10,terminalError=false,holdTerminal,signal=true}={}) {
  let now=0,next=0,observer,starts=0,verifications=0
  const timers=new Map(),hangups=[]
  const setTimer=(fn,delay)=>{const id=++next;timers.set(id,{fn,at:now+delay});return id}
  const clearTimer=id=>timers.delete(id)
  const flush=async()=>{for(let i=0;i<8;i++)await Promise.resolve()}
  const advance=async ms=>{
    const until=now+ms
    while(true){const entry=[...timers].filter(([,v])=>v.at<=until).sort((a,b)=>a[1].at-b[1].at)[0];if(!entry)break;now=entry[1].at;timers.delete(entry[0]);entry[1].fn();await flush()}
    now=until;await flush()
  }
  const result=runCallStabilityTest({activeSeconds:duration,setTimer,clearTimer,
    start:observe=>{starts++;observer=observe;observe('created',{call_id:'exact-call'});if(signal)observe('signalling');return Promise.resolve(true)},
    hangup:id=>{hangups.push(id);observer('ended',{stats:{played_frames:50}})},
    verifyTerminal:async id=>{verifications++;assert.equal(id,'exact-call');if(holdTerminal)await holdTerminal;if(terminalError)throw new Error('terminal not confirmed');return {active_seconds:measured,facts:{summary:{state:'ready'}}}},
  })
  return {result,advance,emit:(type,data)=>observer(type,data),hangups,starts:()=>starts,verifications:()=>verifications,timers}
}

const success=fixture()
success.emit('active')
await success.advance(10000)
await success.advance(750)
assert.equal((await success.result).passed,true)
assert.deepEqual(success.hangups,['exact-call'])
assert.equal(success.starts(),1)
assert.equal(success.timers.size,0)

const early=fixture({duration:50,measured:3})
early.emit('active')
await early.advance(3000)
early.emit('ended')
await early.advance(750)
const earlyResult=await early.result
assert.equal(earlyResult.passed,false)
assert.equal(earlyResult.active_seconds,3,'terminal readback delay must not inflate active duration')
assert.equal(early.hangups.length,0)

const timeout=fixture({measured:0})
await timeout.advance(75000)
await timeout.advance(750)
assert.equal((await timeout.result).passed,false)
assert.equal(timeout.starts(),1)
assert.deepEqual(timeout.hangups,['exact-call'])

const unknown=fixture({terminalError:true})
const rejection=assert.rejects(unknown.result,/terminal not confirmed/)
unknown.emit('active')
await unknown.advance(10000)
await unknown.advance(750)
await rejection
assert.equal(unknown.starts(),1)
assert.deepEqual(unknown.hangups,['exact-call'])
const missingDuration=fixture({measured:null})
const missingRejected=assert.rejects(missingDuration.result,/duration was not confirmed/)
missingDuration.emit('active')
await missingDuration.advance(10000)
await missingDuration.advance(750)
await missingRejected

const failedSetup=fixture({signal:false})
const setupRejected=assert.rejects(failedSetup.result,/no microphone/)
failedSetup.emit('failed',{cause:'no microphone'})
await setupRejected
assert.equal(failedSetup.hangups.length,0)
assert.equal(failedSetup.timers.size,0)

let finishRead
const slow=fixture({holdTerminal:new Promise(resolve=>{finishRead=resolve})})
slow.emit('active')
await slow.advance(10000)
await slow.advance(750)
slow.emit('ended')
await slow.advance(750)
assert.equal(slow.verifications(),1,'terminal events must not overlap readbacks')
finishRead()
assert.equal((await slow.result).passed,true)
console.log('Original call stability lifecycle contracts passed without carrier operations')
