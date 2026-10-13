import React, { useState } from 'react'
import { createRoot } from 'react-dom/client'
import { DevicesPage } from '../src/mdd/views/UnifiedPages.jsx'
import { UnifiedOverview } from '../src/mdd/views/UnifiedPages.jsx'
import DialogHost from '../src/DialogHost.jsx'
import { I18nProvider } from '../src/mdd/i18n.jsx'
import '../src/mdd/index.css'

const reader = { id:'fixture-reader', device_type:'reader', display_name:'Fixture reader',
  present:true, sim:{iccid:'8944100000000002420'}, stable_path:'USB fixture reader' }

const flowReader = {...reader, name:'Fixture new reader', reader:'Fixture new reader',
  sim:{...reader.sim,present:true,mcc:'234',mnc:'10',imsi:'234100000002420',smsc:'+441234567890',apdu_available:true},
  capabilities:{vowifi:{desired:false,actual:'unsupported',available:false}},
  go_device:{agent_id:'fixture-agent',process_generation:'fixture-process',reader:{reader_name:'Fixture new reader',card_id:reader.sim.iccid,session_generation:'fixture-session'}}}

const flowModem = {...flowReader,id:'fixture-modem',device_type:'modem',instance_id:'modem-line',policy_revision:3,
  sim:{...flowReader.sim,number:'+441234567890'},
  capabilities:{cellular:{desired:false,actual:'off',available:true},vowifi:{desired:false,actual:'off',available:true}},
  go_device:{agent_id:'fixture-agent',process_generation:'fixture-process',modem:{equipment_id:'862547055201716',
    attachment_id:'fixture-attachment',sim:{iccid:reader.sim.iccid,session_generation:'fixture-session'}}},
  sms_diagnostics:{service_center:'+441234567890',recovery:{soft_restart:{available:true,recommended:true}}}}

function FlowFixture() {
  const [device, setDevice] = useState(flowReader)
  const [tab, setTab] = useState('status')
  const [view, setView] = useState('overview')
  const [cards, setCards] = useState([{name:flowReader.reader,index:0}])
  const [selected, setSelected] = useState('old-unrelated-line')
  const [toast, setToast] = useState('')
  const props = {devices:[device],discovering:false,instances:[],cards,selected,setSelected,
    refresh:async()=>{},refreshDevices:async()=>{},showToast:setToast,selectedDeviceId:device.id,
    setSelectedDeviceId:()=>{},deviceTab:tab,setDeviceTab:setTab,setView,subscribe:()=>()=>{}}
  return <><DialogHost language="en"/><nav aria-label="Fixture controls">
    <button onClick={() => setCards(value => [...value,{name:'Other reader',index:value.length}])}>Other reader attached</button>
    <button onClick={() => { setView('devices'); setTab('status'); setDevice({...flowReader,instance_id:'confirmed-line',capabilities:{vowifi:{desired:true,actual:'starting',available:true}}}) }}>Waiting start</button>
    <button onClick={() => setDevice(value => ({...value,instance_id:'replacement-line',sim:{...value.sim,iccid:'8944100000000009999'}}))}>Change confirmed card</button>
    <button onClick={() => {setView('devices');setTab('cellular');setDevice(flowModem)}}>Modem profile</button>
    <button onClick={() => setDevice(value => ({...value,sim:{...value.sim,number:'+449999999999'}}))}>Modem number update</button>
  </nav><output aria-label="Selected line">{selected || 'none'}</output><output aria-label="Toast">{toast}</output>
    {view === 'overview' ? <UnifiedOverview {...props}/> : <DevicesPage {...props}/>}
  </>
}

function Fixture() {
  const [device, setDevice] = useState(reader)
  const [tab, setTab] = useState('hardware')
  const [visible, setVisible] = useState(true)
  const [toast, setToast] = useState('')
  const heartbeat = () => setDevice(value => ({...value, imei:'999999999999999'}))
  return <>
    <nav aria-label="Fixture controls">
      <button onClick={() => setVisible(value => !value)}>{visible ? 'Leave devices' : 'Return to devices'}</button>
      <button onClick={heartbeat}>Device heartbeat</button>
      <button onClick={() => setDevice({...reader,sim:{iccid:'8944100000000009999'}})}>Replace SIM</button>
    </nav>
    <output aria-label="Toast">{toast}</output>
    {visible && <DevicesPage devices={[device]} discovering={false} refreshDevices={heartbeat}
      instances={[]} cards={[]} selected="" setSelected={() => {}} refresh={() => {}}
      showToast={setToast} selectedDeviceId={reader.id} setSelectedDeviceId={() => {}}
      deviceTab={tab} setDeviceTab={setTab}/>}
  </>
}

localStorage.setItem('mdd-language','en')
// The control-flow fixture uses the same root as main.jsx. Keep the existing
// Hardware readback stress fixture's separate StrictMode coverage unchanged.
createRoot(document.getElementById('root')).render(<I18nProvider>{new URLSearchParams(location.search).has('flow') ? <FlowFixture/> : <React.StrictMode><Fixture/></React.StrictMode>}</I18nProvider>)
