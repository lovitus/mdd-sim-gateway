import React, {useState} from 'react'
import {createRoot} from 'react-dom/client'
import Messages from '../src/mdd/views/Messages.jsx'
import {useGoCallCoordinator} from '../src/goCallCoordinator.jsx'
import {I18nProvider} from '../src/mdd/i18n.jsx'
import DialogHost from '../src/DialogHost.jsx'
import '../src/mdd/index.css'

const subscribe = () => () => {}
const original = {id:'fixture-line', name:'Fixture SIM',iccid:'8944100000000000001',operations:{
  vowifi_call:{ready:true},cellular_call:{ready:true},vowifi_sms:{ready:true},cellular_sms:{ready:true}}}
function Fixture() {
  const [line,setLine] = useState(original), [visible,setVisible] = useState(true), [toast,setToast] = useState('')
  const coordinator = useGoCallCoordinator({enabled:true,instances:[line],subscribe,showToast:setToast})
  return <>
    <nav aria-label="Fixture controls">
      <button onClick={() => setVisible(value=>!value)}>{visible?'Leave messages':'Return to messages'}</button>
      <button onClick={() => setLine({...original,iccid:'8944100000000000002'})}>Replace SIM</button>
      <button onClick={() => coordinator.answerIncoming(line.id,'vowifi',{call_id:'fixture-incoming',caller:'+15550100001'},500).catch(error=>setToast(error.message))}>Answer fixture</button>
      <button onClick={() => coordinator.hangup()}>Cancel fixture</button>
      <button onClick={() => coordinator.retryStart()}>Retry fixture</button>
      <button onClick={() => coordinator.refresh()}>Refresh call</button>
    </nav>
    <output aria-label="Call phase">{coordinator.current?.phase || 'idle'}</output>
    <output aria-label="Toast">{toast}</output>
    {visible && <Messages selected={line} instances={[line]} cards={[]} devices={[]} subscribe={subscribe} showToast={setToast} setSelected={()=>{}} callCoordinator={coordinator}/>}
    <DialogHost language="en"/>
  </>
}
localStorage.setItem('mdd-language','en')
createRoot(document.getElementById('root')).render(<I18nProvider><Fixture/></I18nProvider>)
