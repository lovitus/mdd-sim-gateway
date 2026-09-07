import { api as go } from '../api.js'
import { operationID } from '../goV1Adapter.js'

export function euiccReaderKey(card) { return JSON.stringify([card.agent_id || '', card.reader || card.name]) }

export function downloadView(job) {
  const terminal = ['completed','failed','canceled','uncertain'].includes(job?.state)
  return {step:job?.stage || 'queued',event:job?.state === 'completed' ? 'completed' : terminal ? 'error' : job?.state || 'unknown',
    done:job?.state === 'completed',error:terminal && job.state !== 'completed' ? job.code || job.state : '',
    metadata:job?.metadata ? {profileName:job.metadata.profile_name,serviceProviderName:job.metadata.service_provider_name} : null,
    terminal,
  }
}

export function readerEuiccs(entries, key) {
  const [agent, reader] = JSON.parse(key)
  if (!agent || !reader) throw new Error('euicc_reader_identity_required')
  return entries.filter(entry => entry.agent_id === agent && entry.reader_name === reader)
}

export function secureElementView(entry) {
  const euicc = entry.euicc
  return { id:euicc.eid, eid:euicc.eid, label:entry.slot_label || entry.slot_id,
    capabilities:euicc, download:euicc.download,
    error:euicc.profiles_available ? '' : 'euicc_profile_inventory_unavailable',
    profiles:(euicc.profiles || []).map(profile => ({...profile,
      profileState:profile.state, profileNickname:profile.nickname || '',
      profileName:profile.profile_name || '', serviceProviderName:profile.service_provider_name || '',
    })), notifications:[],
  }
}

export function profileRequest(action, target, nickname) {
  if (!target.eid || !target.profile) throw new Error('euicc_profile_identity_required')
  const request = {operation_id:operationID(`mdd-euicc-${action}`)}
  if (action === 'nickname') return {...request,nickname,expected_nickname:target.profile.profileNickname || ''}
  if (!['enable','disable'].includes(action)) throw new Error('euicc_profile_action_unavailable')
  return {...request,expected_state:target.profile.profileState}
}

async function mutate(action, iccid, target, nickname) {
  if (target.profile?.iccid !== iccid) throw new Error('euicc_profile_identity_changed')
  const result = await go.mutateEuiccProfile(target.eid,iccid,action,profileRequest(action,target,nickname))
  if (!['refresh_pending','already_applied'].includes(result.outcome)) throw new Error(result.code || `euicc_profile_${result.outcome || 'unknown'}`)
  return result
}

async function inventory(reader, notifications) {
  const result = await go.euiccs()
  let entries = readerEuiccs(result.euiccs || [], reader)
  let observedAt = ''
  if (!entries.length && !notifications) {
    const [agentID,readerName] = JSON.parse(reader)
    const snapshot = await go.devices()
    const remembered = snapshot.devices.find(device => device.observed_only && device.go_device?.agent_id === agentID && device.go_device?.reader?.reader_name === readerName)
    if (remembered) {
      const fact = remembered.go_device.reader
      const slots = fact.secure_elements?.length ? fact.secure_elements : fact.euicc ? [{euicc:fact.euicc}] : []
      entries = slots.map(slot => ({agent_id:agentID,reader_name:readerName,slot_id:slot.slot_id,slot_label:slot.label,
        euicc:{...slot.euicc,profile_management:false,profile_download:false,notification_inventory:false}}))
      observedAt = remembered.last_observed_at
    }
  }
  const ses = await Promise.all(entries.map(async entry => {
    const se = secureElementView(entry)
    if (notifications && entry.euicc.notification_inventory) {
      const result = await go.euiccNotifications(se.eid)
      se.notifications = (result.entries || []).map(item => ({...item,seq:item.sequence_number,seqNumber:item.sequence_number,profileManagementOperation:item.event}))
    }
    return se
  }))
  return {ses,cached:true,ts:observedAt ? Date.parse(observedAt)/1000 : 0}
}

export function notificationEntries(target) {
  if (!target.eid || target.confirmed !== true) throw new Error('euicc_notification_confirmation_required')
  const entries = (target.notifications || []).filter(item => target.seq === undefined || item.sequence_number === target.seq)
  if (!entries.length) throw new Error('euicc_notification_identity_unavailable')
  if (entries.some(item => item.event === 'delete')) throw new Error('euicc_deletion_customization_deferred')
  return entries
}

export const esimAPI = {
  esimReplayNotification: async () => { throw new Error('euicc_deletion_customization_deferred') },
  stop: lineID => go.setLineRuntime(lineID,'stop'),
  async esimStatus() { const result = await go.euiccs(); return {available:(result.euiccs || []).length > 0} },
  esimChipCached: reader => inventory(reader,false),
  esimChip: reader => inventory(reader,true),
  esimEnable: (iccid,target) => mutate('enable',iccid,target),
  esimDisable: (iccid,target) => mutate('disable',iccid,target),
  esimNickname: (iccid,nickname,target) => mutate('nickname',iccid,target,nickname),
  esimDownload(body) {
    if (!body.eid || !body.operation_id || !/^\d{15}$/.test(body.imei || '')) throw new Error('euicc_download_identity_required')
    const code = body.activation_code || `LPA:1$${body.smdp || ''}$${body.matching_id || ''}`
    return go.startEuiccDownload(body.eid,{operation_id:body.operation_id,activation_code:code,imei:body.imei,confirmation_code:body.confirmation_code || ''})
  },
  esimDownloadCancel: target => go.cancelEuiccDownload(target.eid,target.operation_id),
  async esimNotificationsProcess(target) {
    const entries = notificationEntries(target)
    for (const item of entries) await go.deliverEuiccNotification(target.eid,item.sequence_number,{
      confirmed:true,event:item.event,iccid:item.iccid || '',address:item.address,
    })
  },
  esimNotificationRemove(sequence, target) {
    const [item] = notificationEntries({...target,seq:sequence})
    if (target.receiver_acknowledged !== true) throw new Error('euicc_notification_acknowledgement_required')
    return go.removeEuiccNotification(target.eid,sequence,{confirmed:true,receiver_acknowledged:true,event:item.event,iccid:item.iccid || '',address:item.address})
  },
}
