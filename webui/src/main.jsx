import React from 'react'
import { createRoot } from 'react-dom/client'
import './mdd/index.css'
import App from './mdd/App.jsx'
import { I18nProvider } from './mdd/i18n.jsx'

createRoot(document.getElementById('root')).render(<I18nProvider><App /></I18nProvider>)
