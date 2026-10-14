import React, { useState } from 'react'
import { createRoot } from 'react-dom/client'
import { DevicesPage } from '../src/mdd/views/UnifiedPages.jsx'
import { I18nProvider } from '../src/mdd/i18n.jsx'
import DialogHost from '../src/DialogHost.jsx'
import '../src/mdd/index.css'

function Fixture() {
  const [tab,setTab] = useState('status')
  const [toast,setToast] = useState('')
  return <main style={{padding:16}}>
    <output aria-label="Toast">{toast}</output>
    <DevicesPage devices={[]} discovering={false} refreshDevices={() => {}}
      instances={[]} cards={[]} selected="" setSelected={() => {}} refresh={() => {}}
      showToast={setToast} selectedDeviceId="" setSelectedDeviceId={() => {}}
      deviceTab={tab} setDeviceTab={setTab}/>
    <DialogHost language="en"/>
  </main>
}
localStorage.setItem('mdd-language','en')
createRoot(document.getElementById('root')).render(<I18nProvider><Fixture/></I18nProvider>)
