import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { KeyedTrailingRequests } from '../src/keyedTrailingRequests.js'
import { shouldRefreshRemoteSim } from '../src/remoteSimRefresh.js'

const deferred = () => {
  let resolve, reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
const tick = () => new Promise(resolve => setTimeout(resolve, 0))

const active = new Set(['1', '2'])
const runs = []
const commits = []
const queue = new Map([['1', []], ['2', []]])
const requests = new KeyedTrailingRequests({
  active: key => active.has(key),
  run: key => {
    const item = deferred()
    queue.get(key).push(item)
    runs.push(key)
    return item.promise
  },
  commit: (key, value) => commits.push([key, value]),
})

// Repeated renders/unchanged snapshots coalesce onto the one active request.
const first = requests.request('1')
requests.request('1')
await tick()
assert.deepEqual(runs, ['1'])

// A semantic refresh invalidates the old response and produces exactly one fresh trailing read.
requests.request('1', { fresh: true })
requests.request('1', { fresh: true })
queue.get('1')[0].resolve('stale')
await first
await tick()
assert.deepEqual(runs, ['1', '1'])
assert.deepEqual(commits, [])
queue.get('1')[1].resolve('fresh')
await tick()
assert.deepEqual(commits, [['1', 'fresh']])

// Different keys remain parallel.
requests.request('1', { fresh: true })
requests.request('2')
await tick()
assert.deepEqual(runs.slice(-2).sort(), ['1', '2'])
queue.get('1')[2].resolve('one')
queue.get('2')[0].resolve('two')
await tick()
assert.ok(commits.some(item => item[0] === '1' && item[1] === 'one'))
assert.ok(commits.some(item => item[0] === '2' && item[1] === 'two'))

// Cancelling and recreating a key isolates the replacement from the old request's finally.
const old = requests.request('1', { fresh: true })
await tick()
requests.cancel('1')
const replacement = requests.request('1')
await tick()
queue.get('1')[3].resolve('cancelled-old')
await old
queue.get('1')[4].resolve('replacement')
await replacement
assert.ok(!commits.some(item => item[1] === 'cancelled-old'))
assert.ok(commits.some(item => item[1] === 'replacement'))

// A line can disappear before its first request has committed and then reuse the same ID.
const pendingFirst = requests.request('1', { fresh: true })
await tick()
active.delete('1')
requests.cancelExcept(active)
active.add('1')
const readded = requests.request('1')
await tick()
queue.get('1')[5].resolve('removed-first-load')
await pendingFirst
queue.get('1')[6].resolve('readded-first-load')
await readded
assert.ok(!commits.some(item => item[1] === 'removed-first-load'))
assert.ok(commits.some(item => item[1] === 'readded-first-load'))

// Failures release the key and do not poison the next read.
const failed = requests.request('2', { fresh: true })
await tick()
queue.get('2')[1].reject(new Error('expected'))
assert.equal(await failed, null)
requests.request('2')
await tick()
assert.equal(queue.get('2').length, 3)
queue.get('2')[2].resolve('recovered')
await tick()
assert.ok(commits.some(item => item[1] === 'recovered'))

active.delete('1')
requests.cancel('1')
assert.equal(await requests.request('1'), null)

assert.equal(shouldRefreshRemoteSim({ type: 'remote-modem', instance: '7' }, '7'), true)
assert.equal(shouldRefreshRemoteSim({ type: 'remote-modem', instance: '1' }, '7'), false)
assert.equal(shouldRefreshRemoteSim({ type: 'remote-modem' }, '7'), true)
assert.equal(shouldRefreshRemoteSim({ type: 'device', instance: '7' }, '7'), true)
assert.equal(shouldRefreshRemoteSim({ type: 'device', instance: '1' }, '7'), false)

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..')
const coordinator = fs.readFileSync(path.join(root, 'webui/src/callCoordinator.jsx'), 'utf8')
const softphone = fs.readFileSync(path.join(root, 'webui/src/views/Softphone.jsx'), 'utf8')
assert.ok(!/\[enabled, ensurePhone, instances, loadProvision/.test(coordinator))
assert.ok(!/\[refreshRemoteSim, devices\]/.test(softphone))
assert.ok(softphone.includes('selectedDeviceIccidKey'))

// Execute the production provisioning commit with state/stop adapters. A transient native
// admission change must preserve an active owner; an actual Engine replacement must stop it.
const commitBody = coordinator.match(/commit: \(key, prov\) => \{([\s\S]*?)\n    \},/)[1]
let ownerHangups = 0
let lineStops = 0
const activeOwner = { state: 'active', hangup: () => { ownerHangups += 1 } }
const linesRef = { current: { '7': {
  prov: { generation: 'engine-one', enabled: true, browser_media: { outbound: true } },
  call: activeOwner,
} } }
const commitProvision = new Function('linesRef', 'stopLine', 'updateLine', 'ensureNative',
  `return (key, prov) => {${commitBody}}`)(
  linesRef,
  key => {
    lineStops += 1
    linesRef.current[key]?.call?.hangup()
    linesRef.current[key] = { call: null, prov: null }
  },
  (key, patch) => { linesRef.current[key] = { ...linesRef.current[key], ...patch } },
  () => {},
)
commitProvision('7', { generation: 'engine-one', enabled: false,
  browser_media: { outbound: false, inbound: false } })
assert.equal(lineStops, 0)
assert.equal(ownerHangups, 0)
assert.equal(linesRef.current['7'].call, activeOwner)
assert.equal(linesRef.current['7'].prov.browser_media.outbound, false,
  'new calls still observe disabled admission')
commitProvision('7', { generation: 'engine-two', enabled: false, browser_media: {} })
assert.equal(lineStops, 1)
assert.equal(ownerHangups, 1)
assert.equal(linesRef.current['7'].call, null)
commitProvision('8', { generation: 'first-engine', enabled: true, browser_media: {} })
assert.equal(lineStops, 2, 'initial provisioning still initializes the line boundary')

// Opt-in provisioning recovery uses one timer/key and never retries forever.
const realSetTimeout = globalThis.setTimeout
const realClearTimeout = globalThis.clearTimeout
const timers = new Map()
let timerSerial = 0
const flush = async () => { for (let i = 0; i < 12; i += 1) await Promise.resolve() }
globalThis.setTimeout = (callback, delay) => {
  const id = ++timerSerial
  timers.set(id, { callback, delay })
  return id
}
globalThis.clearTimeout = id => timers.delete(id)
const retryPolicy = new Function('error', `return ${
  coordinator.match(/shouldRetry: error => (.*),/)[1]}`)
const fixture = (run) => {
  const live = new Set(['7'])
  const values = [], errors = []
  let count = 0
  const reads = new KeyedTrailingRequests({
    active: key => live.has(key),
    run: key => { count += 1; return run(key, count) },
    commit: (key, value) => values.push(value),
    retryDelaysMs: [1000, 3000, 8000], shouldRetry: retryPolicy,
    onError: (key, error) => errors.push(error),
  })
  return { reads, live, values, errors, count: () => count }
}
const advance = async delay => {
  assert.equal(timers.size, 1, 'only one retry timer per failed line')
  const [id, timer] = [...timers.entries()][0]
  assert.equal(timer.delay, delay)
  timers.delete(id); timer.callback(); await flush()
}
try {
  const error503 = Object.assign(new Error('temporary'), { status: 503 })
  const recovery = fixture((key, count) => count === 1 ? Promise.reject(error503) : 'ready')
  await recovery.reads.request('7'); await flush()
  for (let i = 0; i < 100; i += 1) await recovery.reads.request('7')
  assert.equal(recovery.count(), 1, 'ordinary refresh does not bypass backoff')
  await advance(1000)
  assert.deepEqual(recovery.values, ['ready']); assert.equal(timers.size, 0)

  const failed = fixture(() => Promise.reject(error503))
  await failed.reads.request('7'); await flush()
  for (const delay of [1000, 3000, 8000]) await advance(delay)
  assert.equal(failed.count(), 4); assert.equal(failed.errors.length, 1)
  assert.equal(timers.size, 0)
  await failed.reads.request('7'); assert.equal(failed.count(), 4)
  await failed.reads.request('7', { fresh: true }); await flush()
  assert.equal(failed.count(), 5); assert.equal(timers.size, 1)
  failed.reads.clear(); assert.equal(timers.size, 0)

  for (const status of [401, 403, 404, 429]) {
    const denied = fixture(() => Promise.reject(Object.assign(new Error('denied'), { status })))
    await denied.reads.request('7'); await flush()
    assert.equal(denied.count(), 1); assert.equal(denied.errors.length, 1)
    assert.equal(timers.size, 0)
    await denied.reads.request('7'); assert.equal(denied.count(), 1)
  }
  for (const error of [new TypeError('network'), Object.assign(new Error('timeout'),
    { name: 'AbortError' }), Object.assign(new Error('request timeout'), { status: 408 })]) {
    const transient = fixture(() => Promise.reject(error))
    await transient.reads.request('7'); await flush()
    assert.equal(timers.size, 1); transient.reads.clear()
  }

  // Fresh supersedes a timer, even if its callback was already queued by the browser.
  const replacing = fixture((key, count) => count === 1 ? Promise.reject(error503) : 'new')
  await replacing.reads.request('7'); await flush()
  const oldTimer = [...timers.values()][0].callback
  await replacing.reads.request('7', { fresh: true }); await flush()
  oldTimer(); await flush()
  assert.equal(replacing.count(), 2); assert.deepEqual(replacing.values, ['new'])
  assert.equal(timers.size, 0)

  // Late failure cannot schedule retries or publish an error into a replacement key.
  const oldRequest = deferred()
  const replaced = fixture((key, count) => count === 1 ? oldRequest.promise : 'replacement')
  const pending = replaced.reads.request('7'); await flush()
  replaced.reads.cancel('7'); await replaced.reads.request('7')
  oldRequest.reject(error503); await pending; await flush()
  assert.deepEqual(replaced.values, ['replacement']); assert.equal(replaced.errors.length, 0)
  assert.equal(timers.size, 0)

  const trailing = deferred()
  const coalesced = fixture((key, count) => count === 1 ? trailing.promise : 'trailing')
  const initial = coalesced.reads.request('7'); await flush()
  coalesced.reads.request('7', { fresh: true }); coalesced.reads.request('7', { fresh: true })
  trailing.reject(error503); await initial; await flush()
  assert.equal(coalesced.count(), 2); assert.deepEqual(coalesced.values, ['trailing'])
  assert.equal(timers.size, 0); assert.equal(coalesced.errors.length, 0)

  for (const dispose of [f => f.reads.cancelExcept(new Set()), f => f.reads.clear()]) {
    const removed = fixture(() => Promise.reject(error503))
    await removed.reads.request('7'); await flush()
    const callback = [...timers.values()][0].callback
    dispose(removed); callback(); await flush()
    assert.equal(timers.size, 0); assert.equal(removed.count(), 1)
  }

  // The real API helper aborts a hung GET at eight seconds; no new timeout mechanism.
  const oldWindow = globalThis.window, oldFetch = globalThis.fetch
  try {
    globalThis.window = { location: { pathname: '/' } }
    const { api } = await import('../src/api.js')
    globalThis.fetch = (url, options) => new Promise((resolve, reject) => {
      assert.equal(url, '/api/instances/7/softphone')
      options.signal?.addEventListener('abort', () => reject(
        Object.assign(new Error('aborted'), { name: 'AbortError' })))
    })
    const request = api.softphone('7')
    const rejection = assert.rejects(request, { name: 'AbortError' })
    await advance(8000); await rejection
    assert.equal(timers.size, 0)
  } finally { globalThis.window = oldWindow; globalThis.fetch = oldFetch }
} finally {
  globalThis.setTimeout = realSetTimeout
  globalThis.clearTimeout = realClearTimeout
}

console.log('Request coalescing, bounded provisioning recovery and API timeout tests passed')
