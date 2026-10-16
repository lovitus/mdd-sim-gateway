import React, { useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { DevicesPage } from '../src/mdd/views/UnifiedPages.jsx'
import { UnifiedOverview } from '../src/mdd/views/UnifiedPages.jsx'
import { EgressPage } from '../src/mdd/views/UnifiedPages.jsx'
import DialogHost from '../src/DialogHost.jsx'
import { lineForm } from '../src/mdd/lineAdapter.js'
import { mapGoSnapshot } from '../src/goV1Adapter.js'
import { I18nProvider } from '../src/mdd/i18n.jsx'
import { api } from '../src/api.js'
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
  const instances = ['saved-line','replacement-line'].map((id,index)=>lineForm({schema_version:1,id,
    card_id:index ? '8944100000000009999' : reader.sim.iccid,enabled:false,
    name:index ? 'Replacement SIM' : 'Saved SIM',sim:{mcc:'234',mnc:'10'},network:{egress_country:'gb'}},2))
  const props = {devices:[device],discovering:false,instances,cards,selected:instances.find(line=>line.id===selected),setSelected,
    refresh:async()=>{},refreshDevices:async()=>{},showToast:setToast,selectedDeviceId:device.id,
    setSelectedDeviceId:()=>{},deviceTab:tab,setDeviceTab:setTab,setView,subscribe:()=>()=>{}}
  return <><DialogHost language="en"/><nav aria-label="Fixture controls">
    <button onClick={() => setCards(value => [...value,{name:'Other reader',index:value.length}])}>Other reader attached</button>
    <button onClick={() => { setView('devices'); setTab('status'); setDevice({...flowReader,instance_id:'confirmed-line',capabilities:{vowifi:{desired:true,actual:'starting',available:true}}}) }}>Waiting start</button>
    <button onClick={() => setDevice(value => ({...value,instance_id:'replacement-line',sim:{...value.sim,iccid:'8944100000000009999'}}))}>Change confirmed card</button>
    <button onClick={() => {setView('devices');setTab('cellular');setDevice(flowModem)}}>Modem profile</button>
    <button onClick={() => setDevice(value => ({...value,sim:{...value.sim,number:'+449999999999'}}))}>Modem number update</button>
    <button onClick={() => {setView('devices');setTab('status');setDevice({...flowModem,present:false,sms_diagnostics:null})}}>Offline modem</button>
    <button onClick={() => {setView('devices');setTab('status');setDevice({...flowModem,capabilities:{...flowModem.capabilities,connection:{desired:true,actual:'starting',available:true},vowifi:{desired:false,actual:'stopping',available:true}}})}}>Transitions</button>
    <button onClick={() => {setView('devices');setTab('status');setDevice(mapGoSnapshot({
      catalog:{lines:[{id:'saved-line',card_id:reader.sim.iccid,enabled:false}]},
      lines:[{line_id:'saved-line',facts:[{layer:'vowifi_intent',available:true}]}],devices:[{
        id:'offline-reader',kind:'reader',observed_only:true,reader:{card_id:reader.sim.iccid},
        endpoints:[{association:'exact',operation_candidate:false,card_ids:[reader.sim.iccid],line:{id:'saved-line'}}],
      }]}).devices[0])}}>Offline saved intent</button>
    <button onClick={() => {setView('devices');setTab('sim');setSelected('saved-line');setDevice({...flowReader,instance_id:'saved-line'})}}>Saved SIM editor</button>
    <button onClick={() => {setSelected('replacement-line');setDevice({...flowReader,id:'replacement-reader',instance_id:'replacement-line',sim:{...flowReader.sim,iccid:'8944100000000009999'}})}}>Replacement SIM editor</button>
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

function SetupFixture() {
  const [snapshot,setSnapshot]=useState(null)
  const [selected,setSelected]=useState(null)
  const [tab,setTab]=useState('sim')
  const [toast,setToast]=useState('')
  const [visible,setVisible]=useState(true)
  const refresh=async()=>setSnapshot(await api.snapshot())
  useEffect(()=>{refresh().catch(error=>setToast(error.message))},[])
  return <><DialogHost language="en"/><output aria-label="Toast">{toast}</output>
    <output aria-label="Selected line">{selected || 'none'}</output>
    <button onClick={refresh}>Refresh fixture</button>
    <button onClick={()=>{setVisible(value=>!value);setSelected(null)}}>{visible ? 'Leave devices' : 'Return to devices'}</button>
    {visible && snapshot && <DevicesPage {...snapshot} refresh={refresh} refreshDevices={refresh} showToast={setToast}
      selected={snapshot.instances.find(line=>line.id===selected)} setSelected={setSelected}
      selectedDeviceId="setup-reader" setSelectedDeviceId={()=>{}} deviceTab={tab} setDeviceTab={setTab}/>}
  </>
}

localStorage.setItem('mdd-language','en')
function EgressFixture() {
  const [toast,setToast]=useState('')
  const [visible,setVisible]=useState(true)
  return <><DialogHost language="en"/><output aria-label="Toast">{toast}</output>
    <button onClick={()=>{setVisible(value=>!value);setToast('')}}>{visible?'Leave budget page':'Return to budget page'}</button>
    {visible&&<EgressPage showToast={setToast}/>}</>
}
// The control-flow fixture uses the same root as main.jsx. Keep the existing
// Hardware readback stress fixture's separate StrictMode coverage unchanged.
createRoot(document.getElementById('root')).render(<I18nProvider>{new URLSearchParams(location.search).has('egress') ? <EgressFixture/> : new URLSearchParams(location.search).has('setup') ? <SetupFixture/> : new URLSearchParams(location.search).has('flow') ? <FlowFixture/> : <React.StrictMode><Fixture/></React.StrictMode>}</I18nProvider>)
