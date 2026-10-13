import React, {useState} from 'react'
import {createRoot} from 'react-dom/client'
import Esim from '../src/mdd/views/Esim.jsx'
import DialogHost from '../src/DialogHost.jsx'
import {I18nProvider} from '../src/mdd/i18n.jsx'
import '../src/mdd/index.css'

function Fixture() {
  const [chip,setChip]=useState('01')
  const [agent,setAgent]=useState('agent-a')
  const eid=`890490320000000000000000000000${chip}`
  return <>
    <nav><button onClick={()=>setChip('02')}>Other chip</button><button onClick={()=>setAgent('agent-b')}>Move reader</button></nav>
    <Esim cards={[{agent_id:agent,name:'Test reader',reader:'Test reader',present:true,remote:true,
      iccid:`89440000000000000${chip}`,session_generation:`session-${chip}`,
      euicc:{eid,profiles_available:true,profile_management:true,profile_download:true,profiles:[]}}]}
      instances={[]} refresh={()=>{}} showToast={()=>{}}/>
    <DialogHost language="en"/>
  </>
}
localStorage.setItem('mdd-language','en')
createRoot(document.getElementById('root')).render(<I18nProvider><Fixture/></I18nProvider>)
