export function compactReaderName(value) {
  return String(value || '').replace(/\bVirtual PCD\b/g, 'V PCD')
}

export function lineFailureReasons(device) {
  if (intentionalLineStop(device?.facts) || device?.facts?.summary?.state === 'ready') return []
  const facts = device?.facts?.raw?.operations?.vowifi_call?.facts || Object.values(device?.facts?.facts || {})
  const reasons = new Map()
  for (const fact of facts) {
    const state = fact.condition || fact.state
    const code = fact.fresh !== true ? 'facts_stale' : fact.code || (state === 'unknown' ? 'facts_incomplete' : '')
    if (!code || (fact.fresh === true && !['blocked','failed','degraded','backoff','unknown'].includes(state))) continue
    if (!reasons.has(code)) reasons.set(code, {code,layers:[]})
    const item = reasons.get(code)
    if (fact.layer && !item.layers.includes(fact.layer)) item.layers.push(fact.layer)
  }
  return [...reasons.values()]
}

export function intentionalLineStop(projection) {
  if(projection?.summary?.state!=='blocked')return false
  const code=projection.summary.code
  return (code==='vowifi_disabled' && projection.facts?.vowifi_intent?.available===false) ||
    (code==='line_disabled' && projection.facts?.intent?.available===false)
}

export function unavailableCellularLabel(device) {
  if(device?.device_type==='reader')return 'This is a smart-card reader. It provides SIM access for VoWiFi and has no 4G radio.'
  return device?.present===false ? 'Device not connected' : 'Cellular modem is unavailable'
}

export function lineEndpointLabel(card, device, line, translate) {
  const index=card?.vpcd_slot ?? card?.index ?? line?.reader_index
  const name=compactReaderName(card?.name || device?.name || line?.reader_name || '') || translate('Device not reported')
  return Number.isInteger(index) && index>=0 ? `[${translate('Slot')} ${index}] ${name}` : name
}

export function deviceForLine(line, devices) {
  const iid = String(line?.id || '')
  const iccid = String(line?.iccid || '')
  const matches = (devices || []).filter((device) =>
    (iid && String(device.instance_id || '') === iid) ||
    (iccid && String(device.sim?.iccid || device.iccid || '') === iccid))
  return matches.find(device => device.present === true && String(device.instance_id || '') === iid) ||
    matches.find(device => device.present === true) || matches[0]
}

export function lineServiceStatus(line, service, translate = value => value) {
  const ready = transport => line?.operations?.[`${transport}_${service}`]?.ready === true
  return service === 'sms'
    ? `VoWiFi ${translate('SMS')}: ${translate(ready('vowifi') ? 'Ready' : 'Unavailable')} · ${translate('4G SMS')}: ${translate(ready('cellular') ? 'Ready' : 'Unavailable')}`
    : `VoWiFi: ${translate(ready('vowifi') ? 'Ready' : line?.operations?.vowifi_call?.code || 'Voice unavailable')} · ${translate('Cellular modem')}: ${translate(ready('cellular') ? 'Modem voice hardware ready' : 'Voice unavailable')}`
}

export function lineCallReadinessStatus(line, devices, options = {}, translate = (value) => value) {
  const device = deviceForLine(line, devices)
  const imsRaw = String(line?.status?.label || 'Stopped')
  const facts = line?.facts?.facts || {}
  const summary = line?.facts?.summary || {}
  const hasFacts = Boolean(line?.facts?.version)
  // state is the API contract; label is display text (the server labels OK as "Working").
  // Only older responses without a machine state use the legacy label compatibility path.
  const imsState = line?.status?.state
  const legacyImsReady = imsState == null
    ? ['working', 'ok', 'registered', 'connected', 'running'].includes(imsRaw.trim().toLowerCase())
    : String(imsState).trim().toUpperCase() === 'OK'
  // Facts are presentation evidence.  They make a stale/contradictory route visible, but do
  // not become a second client-side call admission gate: the current Engine media prepare is
  // still the final authority and returns an exact error when an action truly cannot proceed.
  const imsReady = hasFacts
    ? facts.ims?.state === 'ready' && facts.tunnel?.state === 'ready'
    : legacyImsReady
  const imsLabel = hasFacts
    ? `${summary.state || 'unknown'} · ${summary.code || 'evidence_incomplete'}`
    : translate(imsRaw)

  let cellularLabel = ''
  let cellularReady = false
  const registration = String(device?.cellular?.registration || '').toLowerCase()
  if (device?.cellular) {
    const dataConnected = Boolean(device.cellular.data_active ||
      device.capabilities?.cellular?.actual === 'on')
    if (device.present === false) cellularLabel = translate('Device offline')
    else if (dataConnected) {
      cellularLabel = translate('4G data connected')
      cellularReady = true
    } else if (['home', 'roaming', 'registered'].includes(registration)) {
      cellularLabel = translate('Cellular network registered')
      cellularReady = true
    } else if (['searching', 'registering'].includes(registration)) {
      cellularLabel = translate('Cellular network searching')
    } else {
      cellularLabel = translate('Cellular network not registered')
    }
  }

  const coordinatorLine = options.coordinatorLine || {}
  const prov = coordinatorLine.prov || null
  const nativeOutbound = prov?.browser_media?.outbound === true
  const cellularBrowserVoiceReady = line?.operations?.cellular_call?.ready === true
  const vowifiBrowserVoiceReady = nativeOutbound
  const browserVoiceReady = vowifiBrowserVoiceReady || cellularBrowserVoiceReady
  let vowifiBrowserVoiceLabel
  if (nativeOutbound) vowifiBrowserVoiceLabel = translate(
    coordinatorLine.mediaTest === 'passed' ? 'Browser voice verified'
      : hasFacts && !imsReady ? 'Browser WSS available; line evidence needs attention'
        : 'Browser WSS voice available; audio checked per call')
  else if (!prov) vowifiBrowserVoiceLabel = translate(coordinatorLine.provisionError
    ? 'Browser voice capability check failed' : 'Browser voice capability checking')
  else vowifiBrowserVoiceLabel = translate('Browser WSS voice unavailable')
  const browserVoiceLabel = (!vowifiBrowserVoiceReady && cellularBrowserVoiceReady)
    ? translate('Modem voice hardware ready; browser audio is checked per call.')
    : vowifiBrowserVoiceLabel

  return {
    imsReady,
    imsLabel,
    cellularReady,
    cellularLabel,
    cellularBrowserVoiceReady,
    vowifiBrowserVoiceReady,
    vowifiBrowserVoiceLabel,
    browserVoiceReady,
    browserVoiceLabel,
  }
}

export function lineCompositeStatus(line, devices, translate = (value) => value, options = {}) {
  const readiness = lineCallReadinessStatus(line, devices, options, translate)
  const parts = [
    `${options.includeBrowserVoice ? translate('VoWiFi backend') : 'VoWiFi'} ${readiness.imsLabel}`,
  ]
  if (readiness.cellularLabel) parts.push(readiness.cellularLabel)
  if (options.includeBrowserVoice) parts.push(readiness.browserVoiceLabel)
  return parts.join(' · ')
}
