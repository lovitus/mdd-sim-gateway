import { api as go } from '../api.js'

let saved = null

export function networkProfileView(profile) {
  const {sim_iccid,...rest} = profile
  return profile.type === 'cellular_sim' ? {...rest,iccid:sim_iccid || ''} : {...profile}
}

export function networkProfileWire(profile) {
  const {iccid,...rest} = profile
  return profile.type === 'cellular_sim' ? {...rest,sim_iccid:Object.hasOwn(profile,'iccid') ? iccid : profile.sim_iccid || ''} : rest
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
  async refreshEgress() {
    if (!saved) throw new Error('egress_revision_missing')
    return go.applyEgress(saved.__revision)
  },
  async testProxyProfile(profileID, profile) {
    if (!saved) throw new Error('egress_revision_missing')
    const response = await go.testEgressProfile(profileID,saved.__revision,networkProfileWire(profile))
    return { ...response.result, ok:true, config_revision:response.config_revision }
  },
}
