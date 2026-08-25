import assert from 'node:assert/strict'
import { consumeUpdateCompletion, updateProgressOutcome } from '../src/updateProgress.js'

assert.equal(updateProgressOutcome({ state: 'success', phase: 'done' }), 'complete')
assert.equal(updateProgressOutcome({
  state: 'action_required', phase: 'engine_media_migration_required',
  engine_media_migration_required: true,
}), 'engine-media-migration-required')
// Fail closed for an older updater that used success with the migration-required phase.
assert.equal(updateProgressOutcome({
  state: 'success', phase: 'engine_media_migration_required',
}), 'engine-media-migration-required')
assert.equal(updateProgressOutcome({ state: 'failed' }), 'failed')
assert.equal(updateProgressOutcome({
  state: 'failed', phase: 'engine_media_migration_required',
  engine_media_migration_required: true,
}), 'failed')

const completed = { state: 'success', phase: 'done', target: '9.9.9', updated_at: 123 }
const first = consumeUpdateCompletion('', completed)
assert.equal(first.notify, true)
assert.equal(consumeUpdateCompletion(first.key, completed).notify, false)
assert.deepEqual(consumeUpdateCompletion(first.key, { state: 'running' }), {
  key: first.key, notify: false,
})

console.log('Update migration status tests passed')
