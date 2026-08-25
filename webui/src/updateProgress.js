export function updateProgressOutcome(status = {}) {
  if (status.state === 'failed') return 'failed'
  if (status.state === 'stalled') return 'stalled'
  if (status.state === 'action_required' ||
      status.phase === 'engine_media_migration_required' ||
      status.engine_media_migration_required === true) return 'engine-media-migration-required'
  if (status.state === 'success') return 'complete'
  return 'pending'
}

export function consumeUpdateCompletion(seenKey = '', status = {}) {
  if (updateProgressOutcome(status) !== 'complete') {
    return { key: seenKey, notify: false }
  }
  const key = `${String(status.target || '')}:${String(status.updated_at || 0)}`
  return { key, notify: key !== seenKey }
}
