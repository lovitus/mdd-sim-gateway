import React, { useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore } from 'react'
import { createPortal } from 'react-dom'
import { dialogs } from './dialogs.js'
import './dialogs.css'

// HTML dialog stays in the page DOM, unlike window.alert/confirm/prompt.
// The top layer also works above existing eSIM detail modals.
function AppDialog({ request, language }) {
  const element = useRef(null)
  const input = useRef(null)
  const cancelButton = useRef(null)
  const [value, setValue] = useState(request.initialValue)
  const zh = language.startsWith('zh')
  const title = request.type === 'alert' ? (zh ? '提示' : 'Notice')
    : request.type === 'prompt' ? (zh ? '请输入' : 'Input required') : (zh ? '请确认' : 'Confirm')
  const cancel = () => dialogs.finish(request.id, request.type === 'prompt' ? null : false)
  useLayoutEffect(() => {
    const opener = document.activeElement
    const modal = element.current
    const overflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    modal.showModal()
    const target = input.current || cancelButton.current
    target?.focus()
    if (input.current) input.current.select()
    return () => {
      modal.close()
      document.body.style.overflow = overflow
      if (opener?.isConnected) opener.focus()
    }
  }, [])
  return createPortal(<dialog ref={element} className="mdd-dialog" aria-modal="true"
    aria-labelledby="mdd-dialog-title" aria-describedby="mdd-dialog-message"
    onCancel={event => { event.preventDefault(); cancel() }}
    onClose={cancel} onKeyDown={event => event.stopPropagation()}>
    <form onSubmit={event => {
      event.preventDefault()
      if (event.nativeEvent.isComposing) return
      dialogs.finish(request.id, request.type === 'prompt' ? value : true)
    }}>
      <h2 id="mdd-dialog-title">{title}</h2>
      <div id="mdd-dialog-message" className="mdd-dialog-message">{request.message}</div>
      {request.type === 'prompt' && <input ref={input} aria-label={request.message}
        autoComplete="off" value={value} onChange={event => setValue(event.target.value)}
        onKeyDown={event => { if (event.key === 'Enter' && event.nativeEvent.isComposing) event.preventDefault() }} />}
      <div className="mdd-dialog-actions">
        {request.type !== 'alert' && <button ref={cancelButton} type="button" className="btn btn-ghost" onClick={cancel}>{zh ? '取消' : 'Cancel'}</button>}
        <button type="submit" className="btn btn-primary">{request.type === 'alert' ? (zh ? '知道了' : 'OK') : (zh ? '确认' : 'Confirm')}</button>
      </div>
    </form>
  </dialog>, document.body)
}

export default function DialogHost({ language = 'zh' }) {
  const request = useSyncExternalStore(dialogs.subscribe, dialogs.getSnapshot)
  useEffect(() => {
    const cancel = () => dialogs.cancelAll()
    for (const event of ['hashchange', 'popstate', 'pagehide', 'mdd-auth-expired']) window.addEventListener(event, cancel)
    return () => {
      for (const event of ['hashchange', 'popstate', 'pagehide', 'mdd-auth-expired']) window.removeEventListener(event, cancel)
      cancel()
    }
  }, [])
  return request ? <AppDialog key={request.id} request={request} language={language} /> : null
}
