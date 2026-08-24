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
  const imsKey = imsRaw.toLowerCase()
  const imsReady = ['ok', 'registered', 'connected', 'running'].includes(imsKey)

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

  const mediaKnown = options.mediaIngress != null
  const mediaConfirmed = options.mediaIngress?.confirmed === true
  const coordinatorLine = options.coordinatorLine || {}
  const prov = coordinatorLine.prov || null
  const softphoneEnabled = prov?.enabled === true
  const reg = String(coordinatorLine.reg || '').toLowerCase()
  const browserVoiceReady = Boolean(mediaConfirmed && softphoneEnabled && reg === 'registered')
  let browserVoiceLabel
  if (!mediaKnown) browserVoiceLabel = translate('Browser voice route checking')
  else if (!mediaConfirmed) browserVoiceLabel = translate('Browser voice route unconfirmed')
  else if (!softphoneEnabled) browserVoiceLabel = translate('Browser softphone unavailable')
  else if (reg === 'registered') {
    browserVoiceLabel = translate(
      coordinatorLine.mediaTest === 'passed'
        ? 'Browser voice verified'
        : 'Browser softphone registered')
  }
  else if (['failed', 'disconnected'].includes(reg) || coordinatorLine.retryExhausted) {
    browserVoiceLabel = translate('Browser softphone offline')
  } else {
    browserVoiceLabel = translate('Browser softphone connecting')
  }

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
