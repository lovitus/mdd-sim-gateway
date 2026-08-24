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

console.log('Request coalescing and remote SIM refresh tests passed')
