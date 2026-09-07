export function updateProgressOutcome(status = {}) {
  if (status.state === 'failed') return 'failed'
  if (status.state === 'succeeded') return 'complete'
  if (status.state === 'idle') return 'idle'
  if (status.state === 'requested' || status.state === 'running') return 'pending'
  return 'unknown'
}

export function matchUpdateProgress(status, expected = '', uncertain = null) {
  if (uncertain && (!status.operation_id || status.operation_id === uncertain.previous || status.target !== uncertain.target)) {
    return { accepted: false, code: 'update_request_outcome_unknown' }
  }
  if (!uncertain && expected && status.operation_id !== expected) return { accepted: false, code: 'update_operation_changed' }
  return { accepted: true, operation: status.operation_id || '', outcome: updateProgressOutcome(status) }
}
