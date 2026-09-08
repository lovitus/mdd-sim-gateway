// Port of ec620942 webui/src/callCoordinator.jsx:runStabilityTest.
// Keep one call, bounded setup/active timers and independent terminal readback.
export function runCallStabilityTest({start,hangup,verifyTerminal,activeSeconds=50,setTimer=setTimeout,clearTimer=clearTimeout}) {
  const duration=Math.max(10,Math.min(300,Number(activeSeconds)||50))
  return new Promise((resolve,reject)=>{
    let settled=false,verifying=false,active=false,signalled=false,callID='',stats={},failure=''
    let activeTimer,setupTimer,terminalTimer,stopping=false
    const cleanup=()=>{clearTimer(activeTimer);clearTimer(setupTimer);clearTimer(terminalTimer)}
    const finish=(error,value)=>{if(settled)return;settled=true;cleanup();if(error)reject(error);else resolve(value)}
    const verify=async()=>{
      if(settled||verifying)return
      verifying=true
      try {
        const result=await verifyTerminal(callID)
        const measured=result.active_seconds
        if(typeof measured!=='number'||!Number.isFinite(measured)||measured<0)throw new Error('Call duration was not confirmed')
        finish(null,{passed:active && !failure && measured>=duration*0.9,
          reason:failure || (!active?'Call ended before becoming active':measured<duration*0.9?'Call ended before the requested stability duration':''),
          active_seconds:measured,requested_active_seconds:duration,stats,facts:result.facts})
      } catch(error){finish(error)}
    }
    const requestTerminal=()=>{
      if(settled||stopping)return
      stopping=true;clearTimer(activeTimer);clearTimer(setupTimer)
      clearTimer(terminalTimer)
      terminalTimer=setTimer(()=>{void verify()},12000)
      Promise.resolve().then(()=>hangup(callID)).catch(error=>{failure=error.message || String(error)})
    }
    const observe=(type,data={})=>{
      if(settled)return
      if(data.call_id)callID=data.call_id
      if(data.stats)stats={...data.stats}
      if(type==='signalling')signalled=true
      if(type==='failed'){
        failure=data.cause || 'Call stability test failed'
        if(!signalled){finish(new Error(failure));return}
        requestTerminal()
      }
      if(type==='active'&&!active){active=true;clearTimer(setupTimer);activeTimer=setTimer(requestTerminal,duration*1000)}
      if(type==='start_unknown'||type==='media_failed'){failure=data.cause || 'Call or media state is unconfirmed';requestTerminal()}
      if(type==='ended'){
        if(data.cause)failure=data.cause
        if(!signalled){finish(new Error(failure || 'Call ended before signalling'));return}
        clearTimer(activeTimer);clearTimer(setupTimer);clearTimer(terminalTimer)
        terminalTimer=setTimer(()=>{void verify()},750)
      }
    }
    setupTimer=setTimer(()=>{if(!active){failure='Call did not become active before the stability-test setup deadline';requestTerminal()}},75000)
    try {
      Promise.resolve(start(observe)).catch(error=>{
        if(settled)return
        if(!signalled){finish(error);return}
        failure=error.message || String(error);requestTerminal()
      })
    }catch(error){finish(error)}
  })
}
