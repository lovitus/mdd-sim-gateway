export function shouldRefreshRemoteSim(message, instanceId) {
  const selected = String(instanceId || '')
  if (!message || !selected) return false
  if (message.type === 'remote-modem') {
    // New servers include the mapped line. Accept the legacy no-instance event so an older
    // Control can still refresh the selected line after an Agent reconnect.
    return !message.instance || String(message.instance) === selected
  }
  return ['device', 'cellular', 'modem'].includes(message.type) &&
    String(message.instance || '') === selected
}
