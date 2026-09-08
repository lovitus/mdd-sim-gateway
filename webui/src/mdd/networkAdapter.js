import { api as go } from '../api.js'

let saved = null

export function networkProbeError(error, translate) {
  const code=error?.data?.cause_code
  const label={cellular_data_disabled:'Data borrowing is disabled by the device policy.',cellular_connection_disabled:'The 4G data connection switch is disabled.'}[code]
  if(label)return `${translate(label)} · ${error.message || code}`
  return translate(error.message)
}

export function networkProfileView(profile) {
  return {...profile}
}

export function networkProfileWire(profile) {
  const {iccid,...rest} = profile
  return profile.type === 'cellular_sim' ? {...rest,sim_iccid:Object.hasOwn(profile,'sim_iccid') ? profile.sim_iccid : iccid || ''} : rest
}

function settingsView(result) {
  return {proxy:{...result.config,profiles:Object.fromEntries(Object.entries(result.config.profiles || {}).map(([id,profile]) => [id,networkProfileView(profile)]))},__revision:result.revision}
}

export const networkAPI = {
  async networkSettings() {
    const result = await go.egressConfig()
    saved = settingsView(result)
    return structuredClone(saved)
  },
  async saveNetworkSettings(draft) {
    if (!Number.isSafeInteger(draft.__revision)) throw new Error('egress_revision_missing')
    const config = {...draft.proxy,profiles:Object.fromEntries(Object.entries(draft.proxy.profiles || {}).map(([id,profile]) => [id,networkProfileWire(profile)]))}
    const result = await go.saveEgressConfig(config,draft.__revision)
    saved = settingsView(result)
    return structuredClone(saved)
  },
  async refreshEgress(revision = saved?.__revision) {
    if (!Number.isSafeInteger(revision) || revision < 1) throw new Error('egress_revision_missing')
    const result = await go.applyEgress(revision)
    if (result.config_revision !== revision || !['applied','unchanged'].includes(result.state) || result.code !== 'runtime_confirmed') {
      throw new Error('egress_runtime_unconfirmed')
    }
    return result
  },
  async testProxyProfile(profileID, profile) {
    if (!saved) throw new Error('egress_revision_missing')
    const response = await go.testEgressProfile(profileID,saved.__revision,networkProfileWire(profile))
    return { ...response.result, ok:true, config_revision:response.config_revision }
  },
}
