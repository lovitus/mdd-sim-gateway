import React, { useState } from 'react'
import { createRoot } from 'react-dom/client'
import { DevicesPage } from '../src/mdd/views/UnifiedPages.jsx'
import { I18nProvider } from '../src/mdd/i18n.jsx'
import '../src/mdd/index.css'

const reader = { id:'fixture-reader', device_type:'reader', display_name:'Fixture reader',
  present:true, sim:{iccid:'8944100000000002420'}, stable_path:'USB fixture reader' }

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
createRoot(document.getElementById('root')).render(<React.StrictMode><I18nProvider><Fixture/></I18nProvider></React.StrictMode>)
