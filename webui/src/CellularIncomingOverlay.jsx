import React, { useEffect, useRef, useState } from 'react'
import { api } from './api.js'
import { CellularIncomingController } from './cellularIncomingCoordinator.js'
import { useI18n } from './i18n.jsx'

const GREEN = '#22c55e'
const RED = '#ef4444'

function Avatar({ color = GREEN, size = 110 }) {
  return (
    <div style={{ width: size, height: size, borderRadius: '50%', background: color + '22',
      border: `2px solid ${color}55`, display: 'flex', alignItems: 'center', justifyContent: 'center',
      fontSize: size * 0.42, color, margin: '0 auto' }}>☎</div>
  )
}

export function useCellularIncomingCoordinator({
  enabled, subscribe, instances, callCoordinator, showToast,
}) {
  const { t } = useI18n()
  const [state, setState] = useState(null)
  const controllerRef = useRef(null)
  if (!controllerRef.current) {
    controllerRef.current = new CellularIncomingController({ onStateChange: setState })
  }
  controllerRef.current.updateOptions({
    api,
    createMediaPhone: callCoordinator?.createMediaPhone,
    showToast,
    t,
    host: () => location.hostname,
  })

  useEffect(() => {
    if (!enabled) {
      controllerRef.current.stop({ release: true })
      return undefined
    }
    if (!subscribe) return undefined
    return subscribe(message => controllerRef.current.handleMessage(message))
  }, [enabled, subscribe])

  useEffect(() => () => controllerRef.current.stop({ release: true }), [])

  const controller = controllerRef.current
  return {
    state,
    line: (id) => (state && String(state.instanceId) === String(id || '') ? state : null),
    instance: instances?.find(item => String(item.id) === String(state?.instanceId)) || null,
    answer: () => controller.answer(),
    decline: () => controller.decline(),
    hangup: () => controller.hangup(),
  }
}

export function GlobalCellularIncomingOverlay({ coordinator }) {
  const { t } = useI18n()
  const state = coordinator?.state
  if (!state) return null
  const line = coordinator?.instance
  if (state.state === 'active' || state.state === 'ending') {
    return (
      <div className="card" style={{ position: 'fixed', right: 20, bottom: 20, zIndex: 1002,
        padding: 16, minWidth: 260, boxShadow: '0 12px 40px rgba(0,0,0,.35)' }}>
        <div style={{ fontSize: 12, color: 'var(--text-mute)' }}>{line?.name || state.instanceId}</div>
        <div className="mono" style={{ fontSize: 16, fontWeight: 700, marginTop: 4 }}>{state.peer || 'Unknown'}</div>
        <div style={{ fontSize: 13, color: state.state === 'active' ? GREEN : '#eab308', marginTop: 4 }}>
          {t(state.state === 'active' ? 'Cellular call active' : 'Ending cellular call…')}
        </div>
        <button className="btn btn-ghost" style={{ marginTop: 10, color: RED }}
          onClick={() => coordinator.hangup()}>{t('Hangup')}</button>
      </div>
    )
  }
  if (state.state === 'ended') return null
  const answerReady = state.mediaReady && !state.busy
  return (
    <div style={{ position: 'fixed', inset: 0, zIndex: 1002, background: 'rgba(6,10,20,0.82)',
      backdropFilter: 'blur(3px)', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
      <div className="card" style={{ padding: 40, width: 380, textAlign: 'center',
        boxShadow: '0 20px 60px rgba(0,0,0,.6)', animation: 'none' }}>
        <div style={{ fontSize: 13, color: 'var(--text-mute)', letterSpacing: 1, textTransform: 'uppercase' }}>{t('Incoming cellular call')}</div>
        <div style={{ margin: '22px 0' }}><Avatar /></div>
        <div className="mono" style={{ fontSize: 26, fontWeight: 800 }}>{state.peer || 'Unknown'}</div>
        <div style={{ fontSize: 13, color: 'var(--text-mute)', marginTop: 6 }}>{line?.name || state.instanceId}</div>
        <div style={{ fontSize: 13, color: 'var(--text-soft)', marginTop: 10 }}>
          {t(answerReady ? 'Browser audio is ready.' : 'Preparing audio…')}
        </div>
        <div style={{ display: 'flex', justifyContent: 'center', gap: 56, marginTop: 30 }}>
          <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 8 }}>
            <button onClick={() => coordinator.decline()} style={{ width: 68, height: 68, borderRadius: '50%', border: 'none',
              cursor: 'pointer', fontSize: 26, background: RED, color: '#fff' }}>✕</button>
            <span style={{ fontSize: 13, color: 'var(--text-soft)' }}>{t('Decline')}</span>
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 8 }}>
            <button onClick={() => coordinator.answer()} disabled={!answerReady} style={{ width: 68, height: 68, borderRadius: '50%', border: 'none',
              cursor: answerReady ? 'pointer' : 'wait', fontSize: 26, background: answerReady ? GREEN : 'var(--border-strong)', color: '#fff',
              boxShadow: `0 0 0 0 ${GREEN}`, animation: answerReady ? 'ringpulse 1.4s infinite' : 'none' }}>✆</button>
            <span style={{ fontSize: 13, color: 'var(--text-soft)' }}>{t('Answer')}</span>
          </div>
        </div>
      </div>
      <style>{`@keyframes ringpulse{0%{box-shadow:0 0 0 0 ${GREEN}88}70%{box-shadow:0 0 0 16px ${GREEN}00}100%{box-shadow:0 0 0 0 ${GREEN}00}}`}</style>
    </div>
  )
}
