import assert from 'node:assert/strict'
import fs from 'node:fs'

const root = new URL('../', import.meta.url)
const read = path => fs.readFileSync(new URL(path, root), 'utf8')
const removed = [
  'src/views/Esim.jsx', 'src/views/Messages.jsx', 'src/views/SimConfig.jsx',
  'src/views/Softphone.jsx', 'src/views/Logs.jsx', 'src/views/VowifiHistory.jsx',
  'src/CellularIncomingOverlay.jsx', 'src/browserMedia.js', 'src/callCoordinator.jsx',
  'src/cellularBrowserCall.js', 'src/cellularIncomingCoordinator.js',
]
for (const path of removed) assert.equal(fs.existsSync(new URL(path, root)), false, `${path} must remain retired`)

const app = read('src/App.jsx')
for (const component of ['CallsV1.jsx', 'MessagesV1.jsx', 'EsimV1.jsx', 'NotificationsV1.jsx', 'SystemV1.jsx', 'DiagnosticsV1.jsx']) {
  assert.ok(app.includes(component), `App must import ${component}`)
}
const unified = read('src/views/UnifiedPages.jsx')
for (const dead of ['LineVerificationPanel', 'function HardwarePanel(', 'RecycleBinPanel', 'export function SystemPage']) {
  assert.equal(unified.includes(dead), false, `${dead} must remain outside the active unified page module`)
}

const api = read('src/api.js')
const legacyPaths = [...api.matchAll(/['"`]\/api\/[^'"`$]*/g)].map(match => match[0].slice(1))
assert.ok(legacyPaths.length > 0)
assert.deepEqual([...new Set(legacyPaths.map(path => path.split('/').slice(0, 3).join('/')))], ['/api/auth'],
  'only the Go Core admin-auth compatibility namespace may remain under /api')

const scripts = JSON.parse(read('package.json')).scripts
for (const command of Object.values(scripts)) {
  for (const path of removed) assert.equal(command.includes(path.split('/').at(-1)), false)
}

console.log('Legacy frontend surface contracts passed')
