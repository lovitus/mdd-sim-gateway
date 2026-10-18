import React from 'react'
import {createRoot} from 'react-dom/client'
import AgentCredentials from '../src/mdd/views/AgentCredentials.jsx'
import {I18nProvider} from '../src/mdd/i18n.jsx'
import DialogHost from '../src/DialogHost.jsx'
import '../src/mdd/index.css'
import {setCsrf} from '../src/api.js'

localStorage.setItem('mdd-language','en')
setCsrf('synthetic-csrf')
createRoot(document.getElementById('root')).render(<I18nProvider>
  <main style={{padding:24,maxWidth:1000,margin:'auto'}}><AgentCredentials fingerprint={'a'.repeat(64)}/></main>
  <DialogHost language="en"/>
</I18nProvider>)
