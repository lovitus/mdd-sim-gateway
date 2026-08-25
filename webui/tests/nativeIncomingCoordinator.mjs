import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..')
const coordinator = fs.readFileSync(path.join(root, 'webui/src/callCoordinator.jsx'), 'utf8')
const media = fs.readFileSync(path.join(root, 'webui/src/browserMedia.js'), 'utf8')
const fallback = fs.readFileSync(path.join(root, 'webui/src/vowifiIncomingFallback.js'), 'utf8')
const api = fs.readFileSync(path.join(root, 'webui/src/api.js'), 'utf8')
const main = fs.readFileSync(path.join(root, 'control/app/main.py'), 'utf8')

assert.ok(api.includes('prepareBrowserIncoming'))
assert.ok(api.includes("disposition = 'hangup'"))
assert.ok(main.includes('"inbound": bool(running and runtime.get("media_websocket") is True'))
assert.ok(coordinator.includes("prov?.browser_media?.inbound === true"))
assert.ok(coordinator.includes('...nativeCalls.current.keys()'))
assert.ok(coordinator.includes('...Object.keys(linesRef.current)'))
assert.ok(coordinator.includes('stopNativeCall(nativeCall)'))
const ensurePhone = coordinator.slice(
  coordinator.indexOf('const ensurePhone'), coordinator.indexOf('provisioningHandlers.current'))
assert.ok(ensurePhone.indexOf("prov?.browser_media?.inbound === true") <
  ensurePhone.indexOf('phone = new BrowserPhone'))
assert.ok(coordinator.includes('nativeIncomingCall(key, backendCall'))
assert.ok(fallback.includes("source: 'native-wss-incoming'"))
assert.ok(coordinator.includes('native.ownsBackendCall(call)'))
assert.ok(media.includes("String(call?.browser_owner_session || '') === this.sessionId"))
assert.ok(media.includes("String(call?.browser_operation || '') === this.operationId"))
assert.ok(media.includes("String(call?.browser_epoch || '') === this.mediaEpoch"))
assert.ok(coordinator.includes('boundedIdentityMapSet(incomingSuppressions.current'))
assert.ok(coordinator.includes('boundedIdentityMapSet(incomingAudioFailures.current'))
assert.ok(coordinator.includes('boundedIdentityMapSet(incomingCapacityFailures.current'))
assert.ok(coordinator.includes("localDecline ? 'decline' : 'hangup'"))
assert.ok(coordinator.includes('nativeDeclineEligible(current)'))
assert.ok(coordinator.includes('const nativeDeclineStage = nativeDeclineEligible(call)'))
assert.ok(coordinator.includes('routeNativeHangup(nativeCall'))
assert.ok(coordinator.includes("routed.route === 'wss'"))
assert.ok(coordinator.includes("const declineLabel = nativeDeclineStage || call.source === 'jssip'"))
assert.ok(fallback.includes("call?.source === 'native-wss-incoming'"))
assert.ok(fallback.includes('call?.localOwner === true'))
assert.ok(fallback.includes("'answer_submitted_unknown'"))
assert.ok(coordinator.includes("current?.source === 'backend' && current.exactIdentity"))
const hangup = coordinator.slice(
  coordinator.indexOf('hangup: (id)'), coordinator.indexOf('sendDTMF: (id)'))
assert.ok(hangup.indexOf("nativeCall.direction === 'inbound'") <
  hangup.indexOf('nativeCalls.current.delete(key)'))
const overlay = coordinator.slice(coordinator.indexOf('export function GlobalCallOverlay'))
assert.ok(overlay.includes('const answerable = nativeInbound'))
assert.ok(overlay.includes('const canConfirmRoute = !nativeInbound'))
assert.ok(overlay.includes('{!nativeInbound && <button'))
assert.ok(media.includes("this._emit('needs-user-gesture'"))
assert.ok(media.includes('enableAudioFromGesture()'))
assert.ok(media.includes("type: 'browser.call.answer'"))
assert.ok(media.includes('this.terminationTimer = setTimeout'))
assert.ok(media.includes("this.callPhase === 'active'"))

console.log('Native incoming coordinator safety tests passed')
