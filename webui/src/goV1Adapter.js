const ACTIVE_DATA_STATES = new Set(['active', 'connected', 'ready', 'up'])
const STARTING_DATA_STATES = new Set(['starting', 'connecting', 'preparing'])
const STOPPING_DATA_STATES = new Set(['stopping', 'disconnecting', 'cleanup'])

function text(value) {
  return value == null ? '' : String(value)
}

function byID(values, field = 'id') {
  return new Map((Array.isArray(values) ? values : []).map(value => [text(value?.[field]), value]))
}

export function operationID(prefix = 'ui') {
  const random = globalThis.crypto?.randomUUID?.() ||
    `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`
  return `${prefix}-${random}`
}

export function euiccProfileInventory(euicc) {
  const profiles = Array.isArray(euicc?.profiles) ? euicc.profiles : []
  const available = euicc?.profiles_available === true
  return { available, profiles, count: available ? profiles.length : null }
}

export function factsByLayer(projection) {
  const result = {}
  for (const fact of projection?.facts || []) {
    if (!fact?.layer) continue
    result[fact.layer] = {
      ...fact,
      state: fact.condition || 'unknown',
    }
  }
  return result
}

function factSummary(projection) {
  const facts = factsByLayer(projection)
  const operations = projection?.operations || {}
  if (operations.vowifi_call?.ready || operations.vowifi_sms?.ready) {
    return { state: 'ready', code: 'vowifi_operation_ready', blockers: [], unknown: [] }
  }
  const current = Object.values(facts)
  const unknown = current.filter(fact => !fact.fresh || fact.state === 'unknown').map(fact => fact.layer)
  const failed = current.find(fact => ['failed', 'blocked'].includes(fact.state))
  if (failed) {
    return { state: 'blocked', code: failed.code || `${failed.layer}_blocked`,
      blockers: [failed.layer], unknown }
  }
  const degraded = current.find(fact => ['degraded', 'backoff'].includes(fact.state))
  if (degraded) {
    return { state: 'degraded', code: degraded.code || `${degraded.layer}_degraded`,
      blockers: [degraded.layer], unknown }
  }
  const starting = current.find(fact => fact.state === 'starting')
  if (starting) {
    return { state: 'degraded', code: starting.code || `${starting.layer}_starting`,
      blockers: [], unknown }
  }
  return { state: 'unknown', code: unknown.length ? 'facts_incomplete' : 'operation_not_ready',
    blockers: [], unknown }
}

function lineStatus(projection) {
  const facts = factsByLayer(projection)
  const summary = factSummary(projection)
  const runtime = facts.vowifi_runtime
  const state = summary.state === 'ready' ? 'OK'
    : runtime?.state === 'starting' ? 'STARTING'
      : runtime?.state === 'inactive' ? 'STOPPED'
        : summary.state === 'blocked' ? 'ERROR' : 'DEGRADED'
  return {
    state,
    label: summary.code,
    reason: runtime?.detail || Object.values(facts).find(fact => fact.detail)?.detail || '',
    activity: {
      current: runtime?.code || summary.code,
      next: runtime?.state === 'backoff' ? 'automatic_retry' : '',
    },
  }
}

export function mapCatalogLine(line, projection) {
  const facts = factsByLayer(projection)
  const summary = factSummary(projection)
  return {
    id: text(line?.id),
    name: line?.name || text(line?.id),
    enabled: line?.enabled === true,
    provisioning_state: line?.enabled ? 'ready' : 'draft',
    iccid: text(line?.card_id),
    card_id: text(line?.card_id),
    imsi: text(line?.sim?.imsi),
    mcc: text(line?.sim?.mcc),
    mnc: text(line?.sim?.mnc),
    imei: text(line?.sim?.imei),
    msisdn: text(line?.sim?.msisdn),
    number: text(line?.sim?.msisdn),
    smsc: text(line?.sim?.smsc),
    proxy_country: text(line?.network?.egress_country),
    proxy_country_effective: text(line?.network?.egress_country),
    network: line?.network || {},
    ims: line?.ims || {},
    operations: projection?.operations || {},
    facts: { facts, summary, raw: projection || null },
    status: lineStatus(projection),
    go_line: line,
    go_projection: projection || null,
  }
}

function exactEndpoint(device) {
  const values = (device?.endpoints || []).filter(endpoint =>
    endpoint?.association === 'exact' && endpoint?.operation_candidate === true && endpoint?.line?.id)
  const ids = new Set(values.map(endpoint => text(endpoint.line.id)))
  return ids.size === 1 ? values[0] : null
}

function modemFor(device) {
  return device?.modem || device?.raw?.imported_modem || null
}

function dataState(modem, policy) {
  const value = text(modem?.network?.data).toLowerCase()
  if (ACTIVE_DATA_STATES.has(value)) return 'on'
  if (STARTING_DATA_STATES.has(value)) return 'starting'
  if (STOPPING_DATA_STATES.has(value)) return 'stopping'
  if (modem?.network?.data_guard === 'failed' || policy?.state === 'failed') return 'error'
  if (policy?.state === 'backoff') return 'degraded'
  return 'off'
}

function vowifiState(projection, desired) {
  if (projection?.operations?.vowifi_call?.ready || projection?.operations?.vowifi_sms?.ready) return 'on'
  const facts = factsByLayer(projection)
  if (!desired || facts.vowifi_intent?.available === false && facts.vowifi_intent?.code === 'vowifi_disabled') return 'off'
  if (facts.vowifi_runtime?.state === 'starting') return 'starting'
  if (facts.vowifi_runtime?.state === 'failed' || facts.ims?.state === 'failed') return 'error'
  if (Object.values(facts).some(fact => ['degraded', 'backoff', 'blocked'].includes(fact.state))) return 'degraded'
  return 'starting'
}

function policyCapability(policy, key, actual, available) {
  return {
    desired: policy?.desired?.[key] === true,
    actual,
    available,
    requestable: available,
    reason: policy?.code || '',
  }
}

export function mapDevice(device, catalogLines = [], projections = [], egress = {}, agent = null) {
  const modem = modemFor(device)
  const endpoint = exactEndpoint(device)
  const lineID = text(endpoint?.line?.id)
  const catalog = byID(catalogLines)
  const projectionByLine = byID(projections, 'line_id')
  const line = catalog.get(lineID)
  const projection = projectionByLine.get(lineID)
  const policy = modem?.policy || null
  const adapted = device?.kind === 'modem' && device?.mode === 'adapted' && !!modem
  const dataActual = adapted ? dataState(modem, policy) : 'unsupported'
  const flightActual = adapted ? (text(modem?.network?.software_radio).toLowerCase() === 'off' ? 'on' : 'off') : 'unsupported'
  const intent = factsByLayer(projection).vowifi_intent
  const vowifiDesired = intent ? intent.available === true : line?.enabled === true
  const liveExit = egress[text(line?.network?.egress_country).toLowerCase()] || null
	const sim = modem?.sim || device?.reader?.sim || {}
  const cardIDs = endpoint?.card_ids || []
  const iccid = text(sim.iccid || (cardIDs.length === 1 ? cardIDs[0] : ''))
  const msisdn = (Array.isArray(sim.msisdns) ? sim.msisdns.find(Boolean) : '') || line?.sim?.msisdn || ''
  const policyAvailable = adapted && !!policy && sim.state === 'ready'
	const borrowActual = policyAvailable ? (policy?.desired?.cellular_enabled ? 'on' : 'off') : 'unsupported'
  return {
    id: text(device?.id),
    name: modem?.model || modem?.manufacturer || device?.reader?.reader_name || 'Communication device',
    device_type: device?.kind === 'reader' ? 'reader' : 'modem',
    mode: device?.mode || '',
    present: true,
    remote_modem: device?.kind === 'modem',
    reader: device?.reader?.reader_name || '',
    stable_path: modem?.attachment_id || device?.reader?.reader_name || device?.id || '',
    instance_id: lineID,
    endpoints: device?.endpoints || [],
    endpoint_ambiguous: !endpoint && (device?.endpoints || []).some(value => value?.line),
    sim: {
      present: device?.kind === 'reader' ? device?.reader?.card_present === true : sim.state !== 'absent',
      iccid,
      imsi: text(sim.imsi || line?.sim?.imsi),
      number: text(msisdn),
      name: line?.name || modem?.network?.operator_name || '',
		mcc: text(sim.mcc || line?.sim?.mcc),
      mnc: text(sim.mnc || line?.sim?.mnc),
		smsc: text(sim.smsc || line?.sim?.smsc),
      pin_state: text(sim.pin_state),
      pin_configured: sim.pin_configured === true,
      pin_attempts_remaining: sim.pin_attempts_remaining,
      apdu_available: modem?.at_control?.sim_apdu === true || device?.kind === 'reader',
    },
    imei: text(modem?.equipment_id || line?.sim?.imei),
    model: modem?.model || '',
    manufacturer: modem?.manufacturer || '',
    firmware: modem?.firmware || '',
    condition: device?.condition || '',
    condition_code: device?.code || '',
	capabilities: {
	  cellular: policyCapability(policy, 'cellular_enabled', borrowActual, policyAvailable),
	  connection: policyCapability(policy, 'connection_enabled', policy?.connection_active === true ? 'on' : 'off', policyAvailable && policy?.connection_available === true),
      flight: policyCapability(policy, 'flight_mode', flightActual, policyAvailable),
      roaming: policyCapability(policy, 'roaming_enabled', policyAvailable ? (policy?.desired?.roaming_enabled ? 'on' : 'off') : 'unsupported', policyAvailable),
      vowifi: {
        desired: vowifiDesired,
        actual: lineID ? vowifiState(projection, vowifiDesired) : 'unsupported',
        available: !!lineID,
        requestable: !!lineID,
        reason: factSummary(projection).code,
      },
      sms: {
        desired: true,
        actual: projection?.operations?.cellular_sms?.ready || projection?.operations?.vowifi_sms?.ready ? 'on' : 'degraded',
        available: !!lineID,
      },
      call: {
        desired: true,
        actual: projection?.operations?.cellular_call?.ready ? 'on' : 'off',
        available: projection?.operations?.cellular_call?.ready === true,
      },
    },
    ims_capabilities: {
      voice: { actual: projection?.operations?.vowifi_call?.ready || projection?.operations?.cellular_call?.ready ? 'on' : 'off' },
      sms: { actual: projection?.operations?.vowifi_sms?.ready || projection?.operations?.cellular_sms?.ready ? 'on' : 'off' },
      rcs: { actual: 'unsupported' },
    },
    cellular: adapted ? {
      registration: modem?.network?.registration || '',
      operator: modem?.network?.operator_name || modem?.network?.operator_id || '',
      signal: modem?.network?.signal_percent ?? null,
      profile: modem?.network?.profile || policy?.desired?.selected_profile || '',
      data_state: modem?.network?.data || '',
      data_guard: modem?.network?.data_guard || '',
      data_guard_detail: modem?.network?.data_guard_detail || '',
      data_lease: policy?.data_lease || null,
    } : null,
    vowifi: lineID ? { ims: factsByLayer(projection).ims?.code || '', epdg: factsByLayer(projection).tunnel?.code || '' } : null,
    facts: projection ? { facts: factsByLayer(projection), summary: factSummary(projection), raw: projection } : null,
    status: projection ? lineStatus(projection) : null,
    egress: line ? {
      country: line.network?.egress_country || '',
      override: line.network?.egress_country || '',
      detected_country: '',
      available_countries: Object.keys(egress),
      node: liveExit?.node || '',
      ready: liveExit?.ready === true,
      error: liveExit?.error || '',
    } : null,
	policy_revision: Number(policy?.revision || 0),
	sms_diagnostics: adapted ? {
		service_center: sim.smsc || '',
		advisory: sim.sms_error ? [sim.sms_error] : (!sim.sms_configured ? ['cellular_sms_smsc_readback_missing'] : []),
		recovery: {
			refresh: { recommended: !!sim.sms_error || !sim.sms_configured, reason: sim.sms_error || '' },
			soft_restart: { available: (agent?.capabilities || []).includes('modem-recovery-v1'),
				recommended: !!sim.sms_error },
		},
	} : null,
	go_device: device,
  }
}

export function mapReaderCards(devices) {
  const result = []
  let index = 0
  for (const device of devices || []) {
    if (device?.kind !== 'reader' || !device.reader) continue
    const reader = device.reader
    result.push({
      index: index++,
      name: reader.reader_name,
      reader: reader.reader_name,
      present: reader.card_present === true,
      iccid: reader.card_id || '',
      identity_state: reader.identity_state || '',
		sim: reader.sim || null,
      euicc: reader.euicc || null,
      secure_elements: reader.secure_elements || [],
      agent_id: device.agent_id || '',
      process_generation: device.process_generation || '',
      session_generation: reader.session_generation || '',
    })
  }
  return result
}

function egressMap(payload) {
  return Object.fromEntries((payload?.exits || []).map(exit => [text(exit.country).toLowerCase(), exit]))
}

export function mapGoSnapshot(input = {}) {
  const catalogLines = input.catalog?.lines || []
  const projections = input.lines || []
  const liveEgress = egressMap(input.egress)
  return {
    schema_version: 1,
    at: input.at || new Date().toISOString(),
    instances: catalogLines.map(line => mapCatalogLine(line,
      projections.find(value => text(value?.line_id) === text(line?.id)))),
    cards: mapReaderCards(input.devices || []),
    devices: (input.devices || []).map(device => mapDevice(device, catalogLines, projections, liveEgress,
		(input.agents || []).find(agent => agent.agent_id === device.agent_id))),
    agents: input.agents || [],
    discovering: false,
    go: {
      lines: projections,
      catalog: input.catalog || { schema_version: 1, revision: 0, lines: [] },
      devices: input.devices || [],
      egress: input.egress || { exits: [] },
      euiccs: input.euiccs || [],
    },
  }
}

export function mapBrowserSnapshot(snapshot, previous = null) {
  return mapGoSnapshot({
    at: snapshot?.at,
    lines: snapshot?.lines,
    catalog: snapshot?.catalog,
    devices: snapshot?.devices,
    agents: snapshot?.agents,
    egress: previous?.go?.egress,
    euiccs: snapshot?.euiccs || previous?.go?.euiccs,
  })
}

export function mapDeviceProfilesResponse(payload) {
  const mode = String(payload?.device?.policy?.profile_mode || '')
  const supported = mode !== 'system_managed'
  const profiles = payload?.profiles || []
	const configured = profiles.filter(profile => profile.source === 'saved' || profile.source === 'system' || (!profile.source && profile.system))
	const suggestions = profiles.filter(profile => ['system', 'modem', 'network', 'provider'].includes(profile.source) || (!profile.source && profile.system))
  return {
    supported,
    profile_mode: mode,
    revision: Number(payload?.device?.policy?.revision || 0),
    profiles: configured,
    suggested_profiles: suggestions.map((profile, index) => ({
	  id: `${profile.source || 'system'}-${index}`, ...profile,
    })),
    error: supported ? '' : 'Mobile-broadband profiles are managed by macOS and are read-only in MDD.',
  }
}
