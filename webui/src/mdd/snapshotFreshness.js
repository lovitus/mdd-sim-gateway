export const SNAPSHOT_MAX_AGE_MS = 75000

export function snapshotObservation(snapshot, receivedAt) {
  const observedAt = Date.parse(snapshot?.at || '')
  if (snapshot?.devices_available !== true || !Array.isArray(snapshot.devices) || !Number.isFinite(observedAt)) return null
  return { receivedAt, observedAt }
}

export function snapshotIsFresh(observation, now) {
  if (!observation) return false
  const receivedAge = now - observation.receivedAt
  const observedAge = now - observation.observedAt
  return receivedAge >= 0 && receivedAge <= SNAPSHOT_MAX_AGE_MS &&
    observedAge >= -5000 && observedAge <= SNAPSHOT_MAX_AGE_MS
}
