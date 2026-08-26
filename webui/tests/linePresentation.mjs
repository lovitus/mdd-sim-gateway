import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import {
  compactReaderName,
  lineCallReadinessStatus,
  lineCompositeStatus,
} from '../src/linePresentation.js'

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
  'Browser WSS voice unavailable': '浏览器 WSS 语音不可用',
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
  assert.equal(blocked.browserVoiceReady, false)
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

const backendStatusSource = readFileSync(new URL('../../control/app/status.py', import.meta.url), 'utf8')
assert.ok(backendStatusSource.includes('"OK": "Working"'), 'keep the fixture aligned with the backend state/label contract')
const messagesSource = readFileSync(new URL('../src/views/Messages.jsx', import.meta.url), 'utf8')
assert.ok(messagesSource.includes('status_state: item.status?.state ?? null'),
  'Messages memoization must observe the machine state used by shared selectors')

const i18nSource = readFileSync(new URL('../src/i18n.jsx', import.meta.url), 'utf8')
const dictionary = (name) => {
  const start = i18nSource.indexOf(`const ${name} = `) + `const ${name} = `.length
  return new Function(`return (${i18nSource.slice(start, i18nSource.indexOf('\n}', start) + 2)})`)()
}
const unifiedSource = readFileSync(new URL('../src/views/UnifiedPages.jsx', import.meta.url), 'utf8')
const messageStart = unifiedSource.indexOf('      const translated = t(error.message)')
const messageEnd = unifiedSource.indexOf('      setProfileTests', messageStart)
const profileErrorMessage = new Function('error', 't',
  `${unifiedSource.slice(messageStart, messageEnd)}; return message`)
for (const language of ['zh', 'en']) {
  const translations = dictionary(language)
  const translate = value => translations[value] || value
  for (const safeError of ['proxy protocol support is unavailable', 'proxy protocol support has a different source root']) {
    assert.equal(profileErrorMessage({ message: safeError }, translate), translations[safeError])
    assert.notEqual(translations[safeError], safeError,
      'known internal failures must not fall back to proxy credentials/UDP advice')
  }
  const sensitiveError = 'unrecognized error for socks5://user:secret@proxy.invalid:1080'
  const fallback = profileErrorMessage({ message: sensitiveError }, translate)
  assert.equal(fallback, translate('UDP test failed. Check the proxy address, credentials, protocol and UDP support.'))
  assert.ok(!fallback.includes('secret'), 'unknown raw errors must not leak proxy credentials')
}
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
