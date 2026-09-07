export function compactReaderName(value) {
  return String(value || '').replace(/\bVirtual PCD\b/g, 'V PCD')
}

function deviceForLine(line, devices) {
  const iid = String(line?.id || '')
  const iccid = String(line?.iccid || '')
  return (devices || []).find((device) =>
    (iid && String(device.instance_id || '') === iid) ||
    (iccid && String(device.sim?.iccid || device.iccid || '') === iccid))
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
  const cellularCall = device?.capabilities?.call || device?.ims_capabilities?.voice || {}
  const cellularBrowserVoiceReady = device?.present !== false &&
    cellularCall.actual === 'on' && cellularCall.available !== false
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
    ? translate('Cellular voice self-test passed; browser audio is available.')
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
