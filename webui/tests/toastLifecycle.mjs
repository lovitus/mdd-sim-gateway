import assert from 'node:assert/strict'
import fs from 'node:fs'
import { createToastLifecycle } from '../src/toastLifecycle.js'

function harness() {
  let now = 0, nextId = 0, toast = null, updates = 0
  const timers = new Map(), cleared = [], timerRef = { current: null }
  const lifecycle = createToastLifecycle({
    setToast: value => { toast = value; updates += 1 }, timerRef,
    clear: id => { cleared.push(id); timers.delete(id) },
    schedule: (fn, delay) => { const id = ++nextId; timers.set(id, { at: now + delay, fn }); return id },
    now: () => now,
  })
  return {
    ...lifecycle, timers, cleared, timerRef,
    get toast() { return toast }, get updates() { return updates },
    advance(ms) {
      const until = now + ms
      for (;;) {
        const due = [...timers].filter(([, item]) => item.at <= until)
          .sort((left, right) => left[1].at - right[1].at)[0]
        if (!due) break
        now = due[1].at; timers.delete(due[0]); due[1].fn()
      }
      now = until
    },
  }
}

const duration = harness()
duration.show('Saved')
assert.equal([...duration.timers.values()][0].at, 15000, 'toast lasts fifteen seconds')
duration.advance(14999)
assert.equal(duration.toast.message, 'Saved')
duration.advance(1)
assert.equal(duration.toast, null)
assert.equal(duration.timerRef.current, null)
assert.equal(duration.timers.size, 0)

const manual = harness()
manual.show('Saved')
const manualTimer = manual.timerRef.current
manual.advance(1000)
manual.dismiss()
assert.equal(manual.toast, null)
assert.equal(manual.timerRef.current, null)
assert.ok(manual.cleared.includes(manualTimer))
const updatesAfterClose = manual.updates
manual.advance(20000)
assert.equal(manual.updates, updatesAfterClose, 'closed toast leaves no later state update')

const replacement = harness()
replacement.show('First')
const firstTimer = replacement.timerRef.current
replacement.advance(5000)
replacement.show('Second')
assert.ok(replacement.cleared.includes(firstTimer))
assert.equal(replacement.timers.size, 1, 'replacement retains a single timer')
replacement.advance(14999)
assert.equal(replacement.toast.message, 'Second')
replacement.advance(1)
assert.equal(replacement.toast, null)

const cleanup = harness()
cleanup.show('Pending')
const cleanupTimer = cleanup.timerRef.current
cleanup.cleanup()
assert.ok(cleanup.cleared.includes(cleanupTimer))
assert.equal(cleanup.timerRef.current, null)
assert.equal(cleanup.timers.size, 0)

const app = fs.readFileSync(new URL('../src/App.jsx', import.meta.url), 'utf8')
assert.ok(app.includes('createToastLifecycle({ setToast, timerRef: toastTimer })'))
assert.match(app, /<button[^>]*type="button"[^>]*aria-label=\{t\('Dismiss'\)\}[^>]*onClick=\{dismissToast\}/)
assert.ok(app.includes('role="status"'))

console.log('Toast lifecycle tests passed: 15s, manual close, replacement, cleanup, accessible close action')
