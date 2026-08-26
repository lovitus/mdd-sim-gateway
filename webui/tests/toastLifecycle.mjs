import assert from 'node:assert/strict'
import fs from 'node:fs'

const app = fs.readFileSync(new URL('../src/App.jsx', import.meta.url), 'utf8')
const start = app.includes('  const dismissToast =')
  ? app.indexOf('  const dismissToast =') : app.indexOf('  const showToast =')
const end = app.indexOf('  const openUpdateDialog', start)
assert.ok(start >= 0 && end > start, 'production toast handlers must be present')
const factory = new Function('useCallback', 'useEffect', 'clearTimeout', 'setTimeout',
  'setToast', 'toastTimer', 'Date', `${app.slice(start, end)}
  return { showToast, dismissToast: typeof dismissToast === 'function' ? dismissToast : null }
`)

function harness() {
  let now = 0, nextId = 0, toast = null, updates = 0
  const timers = new Map(), cleared = [], cleanup = [], toastTimer = { current: null }
  const handlers = factory(fn => fn, effect => cleanup.push(effect()),
    id => { cleared.push(id); timers.delete(id) },
    (fn, delay) => { const id = ++nextId; timers.set(id, { at: now + delay, fn }); return id },
    value => { toast = value; updates++ }, toastTimer, { now: () => now })
  return {
    ...handlers, timers, cleared, toastTimer,
    get toast() { return toast }, get updates() { return updates },
    advance(ms) {
      const until = now + ms
      for (;;) {
        const due = [...timers].filter(([, item]) => item.at <= until)
          .sort((a, b) => a[1].at - b[1].at)[0]
        if (!due) break
        now = due[1].at; timers.delete(due[0]); due[1].fn()
      }
      now = until
    },
    unmount() { cleanup.forEach(fn => fn?.()) },
  }
}

const duration = harness()
duration.showToast('Saved')
assert.equal([...duration.timers.values()][0].at, 15000, 'toast lasts fifteen seconds')
duration.advance(14999)
assert.equal(duration.toast.message, 'Saved')
duration.advance(1)
assert.equal(duration.toast, null)
assert.equal(duration.toastTimer.current, null)
assert.equal(duration.timers.size, 0)

const manual = harness()
manual.showToast('Saved')
const manualTimer = manual.toastTimer.current
manual.advance(1000)
manual.dismissToast()
assert.equal(manual.toast, null)
assert.equal(manual.toastTimer.current, null)
assert.ok(manual.cleared.includes(manualTimer))
assert.equal(manual.timers.size, 0)
const updatesAfterClose = manual.updates
manual.advance(20000)
assert.equal(manual.updates, updatesAfterClose, 'closed toast must leave no later timeout update')

const replacement = harness()
replacement.showToast('First')
const firstTimer = replacement.toastTimer.current
replacement.advance(5000)
replacement.showToast('Second')
assert.ok(replacement.cleared.includes(firstTimer))
assert.equal(replacement.timers.size, 1, 'replacement retains a single timer')
replacement.advance(10000) // Original toast deadline, not the replacement deadline.
assert.equal(replacement.toast.message, 'Second')
replacement.advance(4999)
assert.equal(replacement.toast.message, 'Second')
replacement.advance(1)
assert.equal(replacement.toast, null)

const afterClose = harness()
afterClose.showToast('Closed')
afterClose.advance(1000)
afterClose.dismissToast()
afterClose.showToast('New')
afterClose.advance(14000)
assert.equal(afterClose.toast.message, 'New', 'closed toast deadline must not clear the new toast')
afterClose.unmount()
assert.equal(afterClose.timers.size, 0, 'unmount clears the outstanding toast timer')

const markup = app.slice(app.indexOf('{toast&&'), app.indexOf('{Object.values(cellularAlerts)'))
assert.match(markup, /<button[^>]*type="button"[^>]*aria-label=\{t\('Dismiss'\)\}[^>]*onClick=\{dismissToast\}/)
assert.ok(markup.includes('role="status"'))
console.log('Toast lifecycle tests passed: 15s, manual close, replacement, cleanup, accessible close action')
