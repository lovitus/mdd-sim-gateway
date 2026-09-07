import {updateProgressOutcome} from '../updateProgress.js'
export {updateProgressOutcome, matchUpdateProgress} from '../updateProgress.js'

export function consumeUpdateCompletion(seenKey = '', status = {}) {
  if (updateProgressOutcome(status) !== 'complete') {
    return { key: seenKey, notify: false }
  }
  if (!status.operation_id) return {key:seenKey,notify:false}
  const key = `${status.operation_id}:${String(status.target || '')}`
  return { key, notify: key !== seenKey }
}
