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
assert.ok(app.includes('<HostAlertsV1/>'), 'host alerts must be visible outside System settings too')
const hostAlerts = read('src/views/HostAlertsV1.jsx')
assert.ok(hostAlerts.includes('api.acknowledgeHostAlert(alert)'))
assert.ok(hostAlerts.includes('alert.recovering'))
assert.equal(hostAlerts.includes('setInterval'), false)
for (const component of ['CallsV1.jsx', 'MessagesV1.jsx', 'EsimV1.jsx', 'NotificationsV1.jsx', 'SystemV1.jsx', 'DiagnosticsV1.jsx']) {
  assert.ok(app.includes(component), `App must import ${component}`)
}
const unified = read('src/views/UnifiedPages.jsx')
for (const dead of ['LineVerificationPanel', 'function HardwarePanel(', 'RecycleBinPanel', 'export function SystemPage']) {
  assert.equal(unified.includes(dead), false, `${dead} must remain outside the active unified page module`)
}

const api = read('src/api.js')
const history = read('src/views/VowifiHistoryV1.jsx')
const css = read('src/index.css')
assert.match(css, /\.u-split\s*>\s*\*\s*\{\s*min-width:0;/,
  'history grid children must be allowed to shrink below intrinsic text width')
assert.ok(css.includes('.u-split { grid-template-columns:minmax(0,1fr); }'),
  'mobile history grid must not use the auto minimum of a bare 1fr track')
assert.match(css, /\.u-message\s*\{[^}]*overflow-wrap:anywhere/,
  'long message identifiers must wrap inside the history pane')
assert.ok(unified.includes('<VowifiHistory instanceId={d.instance_id}/>'))
assert.ok(api.includes('/availability'))
assert.ok(history.includes('api.lineAvailability(instanceId)'))
assert.ok(history.includes('if (!stopped)'))
assert.equal(history.includes('setInterval'), false, 'history requests must not overlap')
assert.equal(history.includes("msg.type !== 'status'"), false, 'retired status events must not trigger queries')
const legacyPaths = [...api.matchAll(/['"`]\/api\/[^'"`$]*/g)].map(match => match[0].slice(1))
assert.ok(legacyPaths.length > 0)
assert.deepEqual([...new Set(legacyPaths.map(path => path.split('/').slice(0, 3).join('/')))], ['/api/auth'],
  'only the Go Core admin-auth compatibility namespace may remain under /api')

const scripts = JSON.parse(read('package.json')).scripts
for (const command of Object.values(scripts)) {
  for (const path of removed) assert.equal(command.includes(path.split('/').at(-1)), false)
}

console.log('Legacy frontend surface contracts passed')
