import assert from 'node:assert/strict'
import { createLatestRequestGate } from '../src/latestRequestGate.js'

const gate = createLatestRequestGate()
gate.select('cellular:line-a')
const slowA = gate.begin('cellular:line-a')
const newerA = gate.begin('cellular:line-a')
assert.equal(gate.accepts(slowA), false, 'an older request on the same route cannot overwrite a newer request')
assert.equal(gate.accepts(newerA), true)

gate.select('vowifi:line-b')
const fastB = gate.begin('vowifi:line-b')
assert.equal(gate.accepts(fastB), true)
assert.equal(gate.accepts(newerA), false, 'a late line A response cannot overwrite the selected line B')

gate.select('cellular:line-c')
assert.equal(gate.accepts(fastB), false, 'changing selection invalidates every in-flight response')

console.log('Latest route request gate tests passed')
