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
