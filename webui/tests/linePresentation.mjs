import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import {
  compactReaderName,
  lineCallReadinessStatus,
  lineCompositeStatus,
} from '../src/linePresentation.js'

const zh = (value) => ({
  Stopped: '已停止',
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
  'Browser WSS voice unavailable': '浏览器 WSS 语音不可用',
  'VoWiFi backend not ready': 'VoWiFi 后端未就绪',
}[value] || value)

assert.equal(compactReaderName('Virtual PCD 00 0A'), 'V PCD 00 0A')
assert.equal(compactReaderName('Generic Smartcard Reader'), 'Generic Smartcard Reader')

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

const registeredLine = { ...line, status: { label: 'Registered' } }
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
}), 'VoWiFi 后端 Registered · 设备离线 · 浏览器 WSS 语音不可用')

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

const i18nSource = readFileSync(new URL('../src/i18n.jsx', import.meta.url), 'utf8')
for (const translation of [
  '运营商 IMS 暂时不可用；Asterisk 将按计划在当前线路内重试注册。',
  '服务器 P-CSCF 暂时拒绝 IMS 注册；已安排原位重试',
  '服务器 P-CSCF {peer} 暂时返回 SIP {status}；将在 {retry_after} 秒后重试',
  'Asterisk 将按已安排的延迟在当前线路内原位重试，不重建线路。',
  '本地 VoWiFi Engine 未继续推进注册',
  '通过安全检查后，只恢复当前确认空闲的 Engine 代际。',
  'Engine 未上报 SIM 启动状态。',
  '无法读取 Engine 内 Asterisk 的注册状态。',
  'Engine 内 IMS 注册未启动。',
]) {
  assert.ok(i18nSource.includes(translation), `missing Chinese translation: ${translation}`)
}
