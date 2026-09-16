import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { parse } from '@babel/parser'
import traversePackage from '@babel/traverse'
import { createDialogStore } from '../src/dialogs.js'

const traverse = traversePackage.default || traversePackage
const store = createDialogStore()
let changes = 0
const unsubscribe = store.subscribe(() => changes++)
const answer = store.confirm('Proceed?')
const firstID = store.getSnapshot().id
assert.equal(await store.confirm('Double click'), false)
assert.equal(await store.prompt('Concurrent prompt'), null)
assert.equal(store.getSnapshot().message, 'Proceed?')
store.finish(firstID, true)
assert.equal(await answer, true)
assert.equal(store.getSnapshot(), null)

const cancelled = store.prompt('Exact identity', 'initial')
assert.equal(store.getSnapshot().initialValue, 'initial')
store.cancelAll()
assert.equal(await cancelled, null)
const empty = store.prompt('Allow empty input')
store.finish(store.getSnapshot().id, '')
assert.equal(await empty, '', 'empty input must remain different from cancellation')
const typed = store.prompt('Text')
store.finish(firstID, 'stale completion')
assert.notEqual(store.getSnapshot(), null)
store.finish(store.getSnapshot().id, 'entered')
assert.equal(await typed, 'entered')

let mutations = 0
async function tripleConfirm() {
  if (!await store.confirm('First warning')) return
  if (!await store.confirm('Second warning')) return
  if (await store.prompt('Exact ID') !== '1234') return
  mutations++
}
const sequence = tripleConfirm()
store.finish(store.getSnapshot().id, true)
await Promise.resolve()
assert.equal(store.getSnapshot().message, 'Second warning')
store.finish(store.getSnapshot().id, true)
await Promise.resolve()
assert.equal(store.getSnapshot().type, 'prompt')
store.finish(store.getSnapshot().id, 'wrong')
await sequence
assert.equal(mutations, 0)
const aborted = tripleConfirm()
store.cancelAll()
await aborted
assert.equal(mutations, 0)
const notice = store.alert('<script>plain text</script>\nDetails')
assert.equal(store.getSnapshot().type, 'alert')
store.finish(store.getSnapshot().id, true)
await notice
unsubscribe()
assert.ok(changes > 0)

// Cover both the copied MDD pages and the retained V1 surfaces. Fail CI if a
// native browser dialog is reintroduced or a Promise guard is not awaited.
const sourceRoot = new URL('../src/', import.meta.url)
let convertedCalls = 0
for (const file of readdirSync(sourceRoot, { recursive: true }).filter(file => /\.(js|jsx)$/.test(file))) {
  const ast = parse(readFileSync(new URL(file, sourceRoot), 'utf8'), { sourceType: 'module', plugins: ['jsx'] })
  traverse(ast, { CallExpression(path) {
    const callee = path.node.callee
    const member = callee.type === 'MemberExpression' && !callee.computed
    const name = member ? callee.property.name : callee.type === 'Identifier' ? callee.name : ''
    if (!['alert', 'confirm', 'prompt'].includes(name)) return
    if (member && callee.object.type === 'Identifier' && callee.object.name === 'dialogs') {
      assert.equal(path.parent.type, 'AwaitExpression', `${file}: dialog result must be awaited`)
      convertedCalls++
    } else if (!member && !path.scope.hasBinding(name) || member && ['window', 'globalThis', 'self'].includes(callee.object.name)) {
      assert.fail(`${file}: native browser ${name} is forbidden`)
    }
  } })
}
assert.ok(convertedCalls >= 80, 'all page dialog call sites must remain covered')
const host = readFileSync(new URL('../src/DialogHost.jsx', import.meta.url), 'utf8')
assert.ok(host.includes('onCancel=') && host.includes('opener.focus()'))
assert.ok(host.includes('hashchange') && host.includes('mdd-auth-expired'))
assert.ok(!host.includes('dangerouslySetInnerHTML'))
console.log('In-page dialogs: cancellation, input, duplicate suppression, confirmation sequence and source audit passed')
