import React, {useState} from 'react'
import {createRoot} from 'react-dom/client'
import Messages from '../src/mdd/views/Messages.jsx'
import {GlobalGoCallOverlay, useGoCallCoordinator} from '../src/goCallCoordinator.jsx'
import {I18nProvider} from '../src/mdd/i18n.jsx'
import DialogHost from '../src/DialogHost.jsx'
import '../src/mdd/index.css'

const subscribe = () => () => {}
const original = {id:'fixture-line', name:'Fixture SIM',iccid:'8944100000000000001',operations:{
  vowifi_call:{ready:true},cellular_call:{ready:true},vowifi_sms:{ready:true},cellular_sms:{ready:true}}}
const other = {...original,id:'fixture-other',name:'Other SIM',iccid:'8944100000000000003'}
function Fixture() {
  const [line,setLine] = useState(original), [visible,setVisible] = useState(true), [toast,setToast] = useState('')
  const instances = [line.id === original.id ? line : original, other]
  const coordinator = useGoCallCoordinator({enabled:true,instances,subscribe,showToast:setToast})
  return <>
    <nav aria-label="Fixture controls">
      <button onClick={() => setVisible(value=>!value)}>{visible?'Leave messages':'Return to messages'}</button>
      <button onClick={() => setLine({...original,iccid:'8944100000000000002'})}>Replace SIM</button>
      <button onClick={() => {const incoming=coordinator.incoming[0]; if(incoming) coordinator.answerIncoming(incoming.line.id,incoming.mode,incoming.call,500).catch(error=>setToast(error.message))}}>Answer fixture</button>
      <button onClick={() => coordinator.hangup()}>Cancel fixture</button>
      <button onClick={() => coordinator.retryStart()}>Retry fixture</button>
      <button onClick={() => coordinator.refresh()}>Refresh call</button>
    </nav>
    <output aria-label="Call phase">{coordinator.current?.phase || 'idle'}</output>
    <output aria-label="Owned call">{coordinator.current?.call_id || ''}</output>
    <output aria-label="Toast">{toast}</output>
    {visible && <Messages selected={line} instances={instances} cards={[]} devices={[]} subscribe={subscribe} showToast={setToast} setSelected={id=>setLine(instances.find(value=>value.id===id))} callCoordinator={coordinator}/>}
    <GlobalGoCallOverlay coordinator={coordinator}/>
    <DialogHost language="en"/>
  </>
}
localStorage.setItem('mdd-language','en')
createRoot(document.getElementById('root')).render(<I18nProvider><Fixture/></I18nProvider>)
