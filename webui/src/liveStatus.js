export function liveStatusFromWsMessage(msg) {
  if (!msg || !msg.type || msg.instance === undefined || msg.instance === null) return null
  if (msg.type === 'status') {
    return Object.fromEntries(
      Object.entries(msg).filter(([key]) => !['type', 'instance'].includes(key)),
    )
  }
  if (msg.type === 'engine' && msg.status_transition && typeof msg.status_transition === 'object') {
    return { ...msg.status_transition }
  }
  return null
}

function lineCapabilityState(status, desired = true) {
  const state = String(status?.state || '').toUpperCase()
  if (state === 'OK') return 'on'
  if (state === 'STOPPED') return desired ? 'degraded' : 'off'
  if (['ERROR', 'NO_CARD', 'PIN_PROBLEM'].includes(state)) return 'error'
  return desired ? 'starting' : 'off'
}

export function mergeLiveLineStatus(device, status, facts) {
  const currentCapability = device.capabilities?.vowifi || {}
  const isDraft = device.provisioning?.state === 'draft'
  // A live status event describes the configured Engine. It cannot override the unified
  // device snapshot's stronger fact that this hardware cannot provide the capability.
  const preserveCapability = isDraft || currentCapability.available === false
  const actual = preserveCapability
    ? (currentCapability.actual || 'off')
    : lineCapabilityState(status, currentCapability.desired !== false)
  const reason = preserveCapability
    ? (currentCapability.reason || 'Automatic setup is waiting for SIM or hardware information')
    : (status.reason || '')
  return {
    ...device,
    status, ...(facts ? { facts } : {}),
    vowifi: {
      ...(device.vowifi || {}),
      epdg: status.detail || {},
      ims: isDraft ? (device.vowifi?.ims || '') : (status.label || ''),
    },
    capabilities: {
      ...(device.capabilities || {}),
      vowifi: { ...currentCapability, actual, reason },
    },
  }
}
