export const NOTIFICATION_TERMINAL_STATES = new Set([
  'delivered', 'failed', 'uncertain', 'canceled',
])

const defaultSleep = milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds))

export async function waitForNotificationDelivery({
  deliveryID,
  listDeliveries,
  timeoutMS = 30000,
  pollMS = 500,
  now = Date.now,
  sleep = defaultSleep,
}) {
  if (!deliveryID || typeof listDeliveries !== 'function')
    throw new Error('notification delivery identity is unavailable')
  const deadline = now() + timeoutMS
  let last = null
  while (true) {
    try {
      const result = await listDeliveries()
      last = (result?.deliveries || []).find(item => item.delivery_id === deliveryID) || last
    } catch { /* a transient read failure does not repeat the real test POST */ }
    if (last && NOTIFICATION_TERMINAL_STATES.has(last.state))
      return { delivery: last, timed_out: false }
    const remaining = deadline - now()
    if (remaining <= 0) return { delivery: last, timed_out: true }
    await sleep(Math.min(pollMS, remaining))
  }
}

export async function runNotificationTest({ channel, enqueue, listDeliveries, onAccepted, ...waitOptions }) {
  const accepted = await enqueue(channel)
  const deliveryID = accepted?.delivery?.delivery_id
  if (!deliveryID) throw new Error('notification test did not return a delivery identity')
  await onAccepted?.(accepted)
  const tracked = await waitForNotificationDelivery({
    deliveryID, listDeliveries, ...waitOptions,
  })
  return { accepted, ...tracked }
}
