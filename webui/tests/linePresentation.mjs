import assert from 'node:assert/strict'
import { deviceForLine, lineServiceStatus } from '../src/mdd/linePresentation.js'

const currentLine = { id: 'current', iccid: 'card', operations: {
  cellular_call: { ready: true }, vowifi_call: { ready: false, code: 'vowifi_disabled' },
  cellular_sms: { ready: true }, vowifi_sms: { ready: false },
} }
const retiredDevice = { instance_id: 'current', sim: { iccid: 'card' }, present: false }
const currentDevice = { instance_id: 'current', sim: { iccid: 'card' }, present: true }
assert.equal(deviceForLine(currentLine, [retiredDevice, currentDevice]), currentDevice)
assert.equal(lineServiceStatus(currentLine, 'call'), 'VoWiFi: vowifi_disabled · Cellular modem: Modem voice hardware ready')
assert.equal(lineServiceStatus(currentLine, 'sms'), 'VoWiFi SMS: Unavailable · 4G SMS: Ready')
assert.equal(lineServiceStatus({ operations: {} }, 'call'), 'VoWiFi: Voice unavailable · Cellular modem: Voice unavailable')
import {
  compactReaderName,
  lineCallReadinessStatus,
  lineCompositeStatus,
} from '../src/mdd/linePresentation.js'

const zh = (value) => ({
  Stopped: '已停止',
  Working: '运行正常',
  'Device offline': '设备离线',
  '4G data connected': '4G 数据已连接',
  'Cellular network registered': '蜂窝网络已注册',
  'Cellular network searching': '蜂窝网络搜寻中',
  'Cellular network not registered': '蜂窝网络未注册',
  'VoWiFi backend': 'VoWiFi 后端',
  'Browser voice route checking': '正在检查浏览器语音路由',
  'Browser voice route unconfirmed': '浏览器 WSS 语音不可用',
  'Browser softphone unavailable': '浏览器 WSS 语音不可用',
  'Browser softphone registered': '浏览器软电话已注册',
  'Browser softphone connecting': '浏览器软电话连接中',
  'Browser softphone offline': '浏览器软电话离线',
  'Browser voice verified': '浏览器语音已验证',
  'Browser WSS voice available; audio checked per call': '浏览器 WSS 语音可用；每通验证音频',
  'Browser WSS available; line evidence needs attention': '浏览器 WSS 可用；线路证据需处理',
  'Browser WSS voice unavailable': '浏览器 WSS 语音不可用',
  'Cellular voice self-test passed; browser audio is available.': '蜂窝语音自检已通过，浏览器双向音频可用。',
  'VoWiFi backend not ready': 'VoWiFi 后端未就绪',
}[value] || value)

assert.equal(compactReaderName('Virtual PCD 00 0A'), 'V PCD 00 0A')
assert.equal(compactReaderName('Generic Smartcard Reader'), 'Generic Smartcard Reader')
const unavailable = lineCallReadinessStatus({ id: '7', status: { state: 'OK' } }, [], {
  coordinatorLine: { prov: null, provisionError: 'Browser voice capability check failed' },
})
assert.equal(unavailable.browserVoiceReady, false)
assert.equal(unavailable.browserVoiceLabel, 'Browser voice capability check failed')

const line = { id: '6', iccid: '8985', status: { label: 'Stopped' } }
const modem = {
  instance_id: '6', present: true,
  cellular: { registration: 'roaming', data_active: false },
  capabilities: { cellular: { actual: 'error' } },
}
assert.equal(lineCompositeStatus(line, [modem], zh),
  'VoWiFi 已停止 · 蜂窝网络已注册')

modem.cellular.data_active = true
assert.equal(lineCompositeStatus(line, [modem], zh),
  'VoWiFi 已停止 · 4G 数据已连接')

modem.cellular.data_active = false
modem.present = false
assert.equal(lineCompositeStatus(line, [modem], zh),
  'VoWiFi 已停止 · 设备离线')

assert.equal(lineCompositeStatus(line, [], zh), 'VoWiFi 已停止')

// This is the production status.py contract, not the lower-level AMI registration label.
const registeredLine = { ...line, status: { state: 'OK', label: 'Working' } }
const registeredCoordinator = {
  prov: { enabled: true, generation: 'engine-a' },
  reg: 'registered',
  mediaTest: 'idle',
}
let readiness = lineCallReadinessStatus(registeredLine, [modem], {
  mediaIngress: { confirmed: false },
  coordinatorLine: registeredCoordinator,
}, zh)
assert.equal(readiness.imsReady, true)
assert.equal(readiness.browserVoiceReady, false)
assert.equal(readiness.browserVoiceLabel, '浏览器 WSS 语音不可用')
assert.equal(lineCompositeStatus(registeredLine, [modem], zh, {
  includeBrowserVoice: true,
  mediaIngress: { confirmed: false },
  coordinatorLine: registeredCoordinator,
}), 'VoWiFi 后端 运行正常 · 设备离线 · 浏览器 WSS 语音不可用')

readiness = lineCallReadinessStatus(registeredLine, [modem], {
  mediaIngress: { confirmed: true },
  coordinatorLine: { ...registeredCoordinator, prov: { enabled: false } },
}, zh)
assert.equal(readiness.browserVoiceReady, false)
assert.equal(readiness.browserVoiceLabel, '浏览器 WSS 语音不可用')

readiness = lineCallReadinessStatus(registeredLine, [modem], {
  coordinatorLine: registeredCoordinator,
}, zh)
assert.equal(readiness.browserVoiceReady, false, 'legacy SIP registration is not native audio capability')
assert.equal(readiness.browserVoiceLabel, '浏览器 WSS 语音不可用')

readiness = lineCallReadinessStatus(registeredLine, [modem], {
  coordinatorLine: { ...registeredCoordinator, prov: { browser_media: { outbound: true } }, mediaTest: 'passed' },
}, zh)
assert.equal(readiness.browserVoiceReady, true)
assert.equal(readiness.browserVoiceLabel, '浏览器语音已验证')

readiness = lineCallReadinessStatus(registeredLine, [modem], {
  mediaIngress: { confirmed: false },
  coordinatorLine: {
    ...registeredCoordinator,
    prov: { enabled: false, browser_media: { outbound: true } },
    reg: 'disconnected',
  },
}, zh)
assert.equal(readiness.browserVoiceReady, true)
assert.equal(readiness.browserVoiceLabel, '浏览器 WSS 语音可用；每通验证音频')

const nativeCoordinator = { prov: { enabled: true, browser_media: { outbound: true } } }
for (const state of ['REGISTERING', 'ERROR', 'STOPPED', 'NO_CARD', 'PIN_PROBLEM', 'unknown', '']) {
  const blocked = lineCallReadinessStatus({ ...line, status: { state, label: 'Working' } }, [],
    { coordinatorLine: nativeCoordinator }, zh)
  assert.equal(blocked.imsReady, false, `display label must not override machine state ${state}`)
  assert.equal(blocked.browserVoiceReady, true,
    'a stale presentation state must not become a second browser-call admission gate')
}
const translatedLabel = lineCallReadinessStatus({ ...line, status: { state: 'OK', label: '运行正常' } }, [],
  { coordinatorLine: nativeCoordinator }, zh)
assert.equal(translatedLabel.imsReady, true, 'translated display text must not change machine readiness')
assert.equal(translatedLabel.browserVoiceReady, true)
for (const label of ['Working', 'Registered']) {
  assert.equal(lineCallReadinessStatus({ ...line, status: { label } }, [],
    { coordinatorLine: nativeCoordinator }, zh).browserVoiceReady, true,
  'legacy label-only response remains compatible')
}
assert.equal(lineCallReadinessStatus(registeredLine, [], {
  coordinatorLine: { prov: { browser_media: { outbound: false } } },
}, zh).browserVoiceReady, false, 'IMS registration never replaces native media admission')

const cellularVoiceDevice = {
  instance_id: '6', present: true,
  capabilities: { call: { actual: 'on', available: true } },
}
readiness = lineCallReadinessStatus(line, [cellularVoiceDevice], {
  coordinatorLine: { prov: { browser_media: { outbound: false } } },
}, zh)
assert.equal(readiness.cellularBrowserVoiceReady, false, 'hardware presence is not authoritative operation readiness')
readiness = lineCallReadinessStatus({ ...line, operations: { cellular_call: { ready: true } } }, [cellularVoiceDevice], {
  coordinatorLine: { prov: { browser_media: { outbound: false } } },
}, zh)
assert.equal(readiness.vowifiBrowserVoiceReady, false)
assert.equal(readiness.vowifiBrowserVoiceLabel, '浏览器 WSS 语音不可用')
assert.equal(readiness.cellularBrowserVoiceReady, true)
assert.equal(readiness.browserVoiceReady, true)
assert.equal(readiness.browserVoiceLabel, 'Modem voice hardware ready; browser audio is checked per call.')
console.log('Mounted line presentation and independent media-readiness tests passed')
