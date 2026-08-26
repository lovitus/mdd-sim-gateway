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
  // state is the API contract; label is display text (the server labels OK as "Working").
  // Only older responses without a machine state use the legacy label compatibility path.
  const imsState = line?.status?.state
  const imsReady = imsState == null
    ? ['working', 'ok', 'registered', 'connected', 'running'].includes(imsRaw.trim().toLowerCase())
    : String(imsState).trim().toUpperCase() === 'OK'

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
  const browserVoiceReady = nativeOutbound && imsReady
  let browserVoiceLabel
  if (!prov) browserVoiceLabel = translate('Browser voice capability checking')
  else if (nativeOutbound && imsReady) browserVoiceLabel = translate(
    coordinatorLine.mediaTest === 'passed' ? 'Browser voice verified'
      : 'Browser WSS voice available; audio checked per call')
  else if (nativeOutbound) browserVoiceLabel = translate('VoWiFi backend not ready')
  else browserVoiceLabel = translate('Browser WSS voice unavailable')

  return {
    imsReady,
    imsLabel: translate(imsRaw),
    cellularReady,
    cellularLabel,
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
