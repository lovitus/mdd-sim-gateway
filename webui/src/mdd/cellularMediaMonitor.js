// Small, UI-independent primitives for the call-scoped cellular media watchdog.

export async function refreshCellularMediaState({ refreshEvidence, getStatus, terminate }) {
  let mediaRefreshError = null
  try {
    await refreshEvidence()
  } catch (error) {
    mediaRefreshError = error
  }
  // This must run even when browser getStats/evidence submission failed.  The server owns
  // freshness and termination state; a browser-side error may never hide its degraded phase.
  const status = await getStatus()
  if (status?.media?.phase === 'degraded') await terminate(status)
  return { status, mediaRefreshError }
}

export async function boundedCellularRelease({ callId, release, attempts = 3, delay }) {
  let lastError = null
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    try {
      const result = await release(callId)
      if (result?.released || result?.termination_pending || result?.missing) return result
      lastError = new Error(result?.hangup?.error || 'Call state is unknown')
    } catch (error) {
      lastError = error
    }
    if (attempt + 1 < attempts) await delay(750 * (attempt + 1))
  }
  throw lastError || new Error('Cellular termination was not accepted')
}
