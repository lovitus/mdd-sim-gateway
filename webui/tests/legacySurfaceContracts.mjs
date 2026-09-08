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

assert.ok(read('src/main.jsx').includes("import App from './mdd/App.jsx'"))
const app = read('src/mdd/App.jsx')
assert.ok(app.includes('<HostAlerts/>'), 'host alerts must be visible outside System settings too')
const hostAlerts = read('src/mdd/views/HostAlerts.jsx')
assert.ok(hostAlerts.includes('api.acknowledgeHostAlert(alert)'))
assert.ok(hostAlerts.includes('alert.recovering'))
assert.equal(hostAlerts.includes('setInterval'), false)
for (const component of ['Softphone.jsx', 'Messages.jsx', 'Esim.jsx', 'UnifiedPages.jsx']) {
  assert.ok(app.includes(component), `App must import ${component}`)
}
const unified = read('src/mdd/views/UnifiedPages.jsx')
assert.ok(app.includes('deviceTab,setDeviceTab'), 'device subpage selection must survive overview navigation')
assert.ok(unified.includes("setDeviceTab('hardware'); setView('devices')"))
assert.ok(unified.includes("setDeviceTab('sim'); setView('devices')"))
assert.ok(unified.includes("errorCode === 'imei_binding_required'"), 'Go top-level errors must retain the original corrective navigation')
const networkPage=unified.slice(unified.indexOf('export function EgressPage('),unified.indexOf('export function NotificationsPage('))
const notificationPage=unified.slice(unified.indexOf('export function NotificationsPage('),unified.indexOf('export function SystemPage('))
for(const page of [networkPage,notificationPage]) {
  assert.ok(page.includes('if (!s) return settingsError ?'), 'failed initial reads must not stay on Loading forever')
  assert.ok(page.includes('onClick={retrySettings}'), 'original pages need an explicit retry without reloading the app')
}
const diagnostics=unified.slice(unified.indexOf('export function DiagnosticsPage('))
assert.ok(diagnostics.includes("capability(d, 'connection').actual"), '4G status must not display borrowing permission')
assert.ok(diagnostics.includes('setAgentsError(error.message)'), 'Agent read failures must not become empty inventory')
assert.ok(diagnostics.includes('onClick={loadAgents}'))
assert.ok(diagnostics.includes('onClick={loadHost}'))
const selector=read('src/mdd/views/SimSelector.jsx')
assert.ok(selector.includes('options.filter(option=>!option.disabled)'), 'unconfigured cards must not become call/SMS line IDs')
assert.ok(selector.includes('disabled={opt.disabled}'))
assert.equal(selector.includes('virtualReaderName'),false, 'missing hardware must not create a fictional Virtual PCD endpoint')
for (const restored of ['LineVerificationPanel', 'function HardwarePanel(', 'RecycleBinPanel', 'export function SystemPage']) {
  assert.equal(unified.includes(restored), true, `${restored} must remain in the requested customized UI`)
}

const api = read('src/api.js')
const history = read('src/mdd/views/VowifiHistory.jsx')
const css = read('src/mdd/index.css')
const allowance = read('src/mdd/views/AllowancePanel.jsx')
assert.equal(allowance.includes('setInterval'), false, 'allowance reply reads must not overlap')
assert.ok(allowance.includes('setTimeout(observe,30000)'), 'first reply read must use the low-frequency timer')
assert.ok(allowance.includes('Date.now() + 600000'), 'reply observation must be bounded')
const allowanceQuery = allowance.slice(allowance.indexOf('const query = async'))
assert.ok(allowanceQuery.indexOf("!['cellular','vowifi'].includes(transport)") < allowanceQuery.indexOf('window.confirm'),
  'reject missing transport before asking for a paid SMS confirmation')
assert.ok(allowanceQuery.includes('if (operationBusy.current) return'))
assert.match(css, /\.u-project-meta \.u-version \{[^}]*min-width:0;[^}]*overflow-wrap:anywhere;/,
  'full release revisions must wrap without pushing sidebar controls onto the page')
assert.match(css, /\.u-sidebar \{[^}]*visibility:hidden;/,
  'closed mobile navigation must not paint or expose focusable controls outside its bounds')
assert.match(css, /\.u-sidebar\.open \{[^}]*visibility:visible;/,
  'opening mobile navigation must restore visible controls')
assert.match(css, /\.u-split\s*>\s*\*\s*\{\s*min-width:0;/,
  'history grid children must be allowed to shrink below intrinsic text width')
assert.ok(css.includes('.u-split { grid-template-columns:minmax(0,1fr); }'),
  'mobile history grid must not use the auto minimum of a bare 1fr track')
assert.match(read('src/mdd/views/Messages.jsx'), /overflowWrap:'anywhere'/,
  'long message identifiers must wrap inside the history pane')
assert.ok(unified.includes('<VowifiHistory instanceId={d.instance_id}'))
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
