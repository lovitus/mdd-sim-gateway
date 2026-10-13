import { useEffect, useState } from 'react'
import { api } from './api.js'
import { readerIMEIBinding } from './hardwareAdapter.js'

export function useReaderIMEI(device) {
  const cardID = device?.sim?.iccid || ''
  const reader = device?.device_type === 'reader'
  const [result, setResult] = useState(null)
  useEffect(() => {
    if (!reader || !cardID) return
    let cancelled = false
    api.imeiPool().then(snapshot => {
      const { saved } = readerIMEIBinding(snapshot, cardID)
      if (!cancelled) setResult({ cardID, imei: saved?.imei || '', error: '' })
    }).catch(error => {
      if (!cancelled) setResult({ cardID, imei: '', error: error.message })
    })
    return () => { cancelled = true }
  }, [reader, cardID, device?.imei])
  if (!reader) return { loading: false, imei: device?.imei || '', error: '' }
  if (!cardID) return { loading: false, imei: '', error: '' }
  return result?.cardID === cardID ? { ...result, loading: false } : { loading: true, imei: '', error: '' }
}
