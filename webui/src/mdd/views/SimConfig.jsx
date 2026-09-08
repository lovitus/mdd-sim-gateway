import React, { useEffect, useRef, useState } from 'react'
import { api } from '../api.js'
import { simPINIdentity, pinProof, runtimeNetworkSelection } from '../lineAdapter.js'
import ProvisionActions from './ProvisionActions.jsx'
import { useI18n } from '../i18n.jsx'
import { compactReaderName, lineCompositeStatus } from '../linePresentation.js'

const emptyInstance = () => ({
  id: '', name: '', imsi: '', mcc: '', mnc: '', imei: '', imeisv: '', pin: '', reader: '', proxy_country: '',
  reader_index: 0, reader_port: '', msisdn: '', smsc: '', enabled: true, apn: 'ims', idr_mode: 'apn', cp_mode: 'auto',
  sip: { listen_addr: '0.0.0.0', webrtc: { enable: true } },
  debug: { asterisk: false, charon: false },
})

function Field({ label, children }) {
  return <div><label>{label}</label>{children}</div>
}

export default function SimConfig({ instances, selected, refresh, cards, setSelected, targetDevice, devices = [] }) {
  const { t } = useI18n()
  const [readers, setReaders] = useState([])
  const [card, setCard] = useState(null)
  const [pin, setPin] = useState('')
  const [pinMsg, setPinMsg] = useState('')
  const [form, setForm] = useState(emptyInstance())
  const [saving, setSaving] = useState(false)
  const [detecting,setDetecting]=useState(false)
  const [deleting, setDeleting] = useState(false)
  const [deleteHistory, setDeleteHistory] = useState(true)
  const [creating, setCreating] = useState(false)
  const [savedLineId, setSavedLineId] = useState('')
  const [smscMode, setSmscMode] = useState('auto')   // 'auto' = read from SIM, 'manual' = typed
  const currentFormID = useRef('')
  const newLineClaim = useRef({})
  const currentTarget = useRef('')
  const deletionRequests = useRef(new Map())
  const mutationBusy = useRef(false)
  const [proof, setProof] = useState(null)
  const [pinConfiguration, setPinConfiguration] = useState(null)
  const [pinBusy, setPinBusy] = useState(false)
  const pinEpoch = useRef(0)
  useEffect(() => { ++pinEpoch.current; setProof(null); setPinConfiguration(null); setPin('') }, [targetDevice?.id, targetDevice?.sim?.iccid, targetDevice?.go_device?.process_generation, targetDevice?.go_device?.modem?.sim_session_generation, targetDevice?.go_device?.reader?.session_generation, form.id])
  currentFormID.current = form.id
  currentTarget.current=JSON.stringify([targetDevice?.id,targetDevice?.sim?.iccid])
  // Capability is authoritative; do not depend on a transport/type label that an older
  // device-list adapter may omit.  A reserved VPCD name is not proof of APDU access.
  const providerOnly = targetDevice?.sim?.apdu_available === false
  const actualNetwork = runtimeNetworkSelection(instances,form.id)

  // Reader order is display-only. Hardware actions use the selected typed device.
  useEffect(() => {
    setReaders(cards.filter(card => card.name).map(card => card.name))
  }, [cards.map(card => `${card.agent_id}:${card.name}`).join('|')])
  const boundLineId = String(targetDevice?.instance_id || '')
  const managedSelected = selected && String(selected.id) === String(boundLineId || savedLineId)
    ? selected : null
  useEffect(() => {
    if (!managedSelected) {
      if (targetDevice?.present === false) setForm(emptyInstance())
      return
    }
    let cancelled = false
    setForm({...emptyInstance(),...managedSelected})
    api.lineConfiguration(managedSelected.id).then(value => {
      if (cancelled) return
      setCreating(value.provisioning_state === 'draft')
      setForm({...emptyInstance(),...managedSelected,...value})
    }).catch(error => { if (!cancelled) setPinMsg(error.message) })
    return () => { cancelled = true }
  }, [managedSelected?.id, managedSelected?.provisioning_state, targetDevice?.present])
  // Opening the SIM tab for an unconfigured physical reader starts a new-line form bound to
  // that reader. This avoids silently editing the currently selected (unrelated) line.
  useEffect(() => {
    if (!targetDevice) return
    if (targetDevice.instance_id) {
      setSavedLineId('')
      setSelected(String(targetDevice.instance_id))
      return
    }
    // With no authoritative device->SIM association, clear global selection. A saved line is
    // loaded only after the user explicitly chooses it in the separate manager below.
    setSavedLineId('')
    setSelected(null)
    setForm(emptyInstance())
    // An offline saved hardware record has no authoritative SIM association. Do not turn it
    // into a new-line form merely because another reader exists; saved lines remain selectable
    // below and their delete action stays available regardless of hardware presence.
    if (targetDevice.present === false || targetDevice.sim?.present === false) {
      setCreating(false)
      return
    }
    const providerIdentity = !!(targetDevice.remote_modem &&
      (targetDevice.sim?.iccid || targetDevice.sim?.imsi))
    if (!readers.length && !providerIdentity) return
    const wanted = targetDevice.reader || targetDevice.name
    const index = Math.max(0, readers.findIndex((reader) => reader === wanted))
    setCreating(true)
    const sim = targetDevice.sim || {}
    setCard(sim.iccid || sim.imsi ? {
      present: sim.present !== false, iccid: sim.iccid || '', imsi: sim.imsi || '',
      pin_enabled: null, identity_source: sim.identity_source || '',
    } : null)
    setPin(''); setPinMsg('')
    setForm({ ...emptyInstance(), reader_index: index,
      reader_port: (cards.find((item) => item.index === index) || {}).reader_port || '',
      imsi: sim.imsi || '', mcc: sim.mcc || '', mnc: sim.mnc || '',
      msisdn: sim.number || '',
      smsc: targetDevice.sms_diagnostics?.service_center || '' })
  }, [targetDevice?.id, targetDevice?.instance_id, targetDevice?.sim?.iccid,
    targetDevice?.sim?.imsi, targetDevice?.sim?.apdu_available,
    readers.join('|')]) // eslint-disable-line react-hooks/exhaustive-deps
  // Keep the reader selection valid for the CURRENT hardware. A stored reader_index can be
  // stale — saved when more readers were attached — and point past the live reader list; the
  // <select> then has no matching option and "Detect card" probes a phantom reader ("No SIM
  // card in reader N"). Clamp any out-of-range index back onto a reader that actually exists.
  useEffect(() => {
    if (!readers.length) return
    setForm((f) => (f.reader_index >= readers.length || f.reader_index < 0)
      ? { ...f, reader_index: 0 } : f)
  }, [readers.length])
  // Keep the "PIN saved?" indicator in sync when it changes server-side (delete-PIN,
  // start-with-PIN) without a full line switch — mirror the fresh value onto the form.
  useEffect(() => { if (managedSelected) setForm((f) => ({ ...f, has_pin: managedSelected.has_pin })) }, [managedSelected?.has_pin])

  const upd = (patch) => setForm((f) => ({ ...f, ...patch }))
  const updSip = (patch) => setForm((f) => ({ ...f, sip: { ...f.sip, ...patch } }))
  // The reader index to act on, clamped to a reader that currently exists (never probe a
  // stale/out-of-range index that would report a phantom empty reader).
  const readerIdx = () => (form.reader_index >= 0 && form.reader_index < readers.length) ? form.reader_index : 0
  // Stable USB port path of a reader index (from the live card monitor). A line binds to this
  // port, not the enumeration index, so it sticks to the physical reader socket even when pcscd
  // re-enumerates two identical readers in a different order.
  const portForIdx = (i) => (cards.find((c) => c.index === i) || {}).reader_port || ''

  const detect = async () => {
    if(mutationBusy.current)return
    mutationBusy.current=true;setDetecting(true)
    const epoch=pinEpoch.current,target=currentTarget.current
    const current=()=>pinEpoch.current===epoch && currentTarget.current===target
    setPinMsg(t('Detecting…'))
    try {
      const c=await api.detect(targetDevice)
      if(!current())return
      // OS modem providers expose subscriber identity without raw APDU access. Use their
      // authoritative snapshot instead of probing a reserved-but-disconnected VPCD slot.
      // PC/SC and APDU-capable modems keep the full card/PIN path below.
      if (providerOnly) {
        setCard(c)
        upd({ imsi: c.imsi || form.imsi, mcc: c.mcc || form.mcc,
          mnc: c.mnc || form.mnc, msisdn: c.number || form.msisdn,
          ...(smscMode==='auto' && c.smsc ? {smsc:c.smsc} : {}) })
        setPinMsg(c.imsi ? t('Card identity read from the modem provider.')
          : t('Only ICCID is available from this modem provider.'))
        return
      }
      setCard(c)
      if (!c.present) {
        setPinMsg(t('No SIM card in this reader.'))
        return
      }
      const patch = { imsi: c.imsi || form.imsi, mcc: c.mcc || form.mcc, mnc: c.mnc || form.mnc }
      if (c.smsc && smscMode === 'auto') patch.smsc = c.smsc   // SMSC from the SIM (EF_SMSP)
      if (c.imsi) patch.reader = `imsi:${c.imsi}`
      // Bind the line to the reader's stable physical USB port (from the detected card, else the
      // live monitor). Persisted so start-time re-resolves the correct index for this socket.
      const port = c.reader_port || portForIdx(readerIdx())
      if (port) patch.reader_port = port
      upd(patch)
      setPinMsg(c.imsi ? t('Card read.') : t('Card present; enter PIN to read IMSI. ICCID {iccid}, {tries} tries left.', { iccid: c.iccid || '?', tries: c.pin_tries ?? '?' }))
    } catch (e) { if(current())setPinMsg(`${t('Error')}: ${e.message}`) }
    finally {mutationBusy.current=false;setDetecting(false)}
  }

  const checkPINStatus = async () => {
    if (mutationBusy.current) return
    mutationBusy.current = true; setPinBusy(true); setProof(null); setPinConfiguration(null)
    const epoch = pinEpoch.current
    try {
      const target = simPINIdentity(targetDevice,form.iccid)
      const operation = crypto.randomUUID()
      const result = await api.simPIN({operation_id:operation,...target,action:'status'})
      if (pinEpoch.current !== epoch) return
      setProof(pinProof(result,operation,target)); setPinConfiguration(result.configuration || null)
      setPinMsg(`${result.state} · ${result.attempts_remaining ?? '?'} ${t('attempts remaining')}`)
    } catch (error) { setPinMsg(error.message) }
    finally { mutationBusy.current = false; setPinBusy(false) }
  }
  const verifyPin = async (saveOnAgent = false) => {
    if (mutationBusy.current) return
    const epoch = pinEpoch.current
    try {
      const target = simPINIdentity(targetDevice,form.iccid)
      if (!proof || proof.target !== JSON.stringify(target) || !/^\d{4,8}$/.test(pin)) throw new Error('sim_pin_preflight_required')
      if (saveOnAgent && !pinConfiguration?.revision) throw new Error('sim_pin_configuration_unavailable')
      mutationBusy.current = true; setPinBusy(true)
      const preflight = proof.operation
      setProof(null)
      const result = await api.simPIN({operation_id:crypto.randomUUID(),...target,
        action:saveOnAgent ? 'verify_save' : 'verify',pin,preflight_operation_id:preflight,
        ...(saveOnAgent ? {expected_config_revision:pinConfiguration.revision} : {})})
      if (pinEpoch.current !== epoch) return
      setPinConfiguration(result.configuration || null)
      setPinMsg(result.state || 'unknown')
      await refresh()
    } catch (error) { setPinMsg(error.message) }
    finally { setPin(''); mutationBusy.current = false; setPinBusy(false) }
  }
  const save = async () => {
    if (mutationBusy.current) return
    mutationBusy.current = true
    const forId = form.id
    const forTarget=currentTarget.current
    setSaving(true)
    try {
      const rawMnc = String(form.mnc || '').trim()
      const body = { ...form, mnc: rawMnc }
      if (!creating && managedSelected?.id) body.original_id = String(managedSelected.id)
      const editedNumber = String(form.msisdn || '').trim() !== String(managedSelected?.msisdn || '').trim()
      if (editedNumber) body.msisdn_source = String(form.msisdn || '').trim() ? 'manual' : ''
      // Strip runtime-only fields that ride along on the instance object from /api/instances
      // (they are computed per-request, not config — never persist them).
      delete body.status; delete body.has_pin
      // PIN is managed only by the explicit Agent actions, never by catalog saves.
      delete body.pin
      // Device identity belongs to the physical modem/reader and is managed on the
      // Hardware tab. Never let a stale SIM form overwrite the current hardware snapshot.
      delete body.imei; delete body.imeisv
      // A Windows/system-managed modem can expose ICCID/IMSI/SMS without exposing raw SIM
      // APDUs.  It still needs an editable ICCID-scoped line record for the user-supplied
      // MSISDN/SMSC and message history, but provisioning must not pretend VoWiFi can start.
      // Save that record stopped; a future APDU-capable attachment can use the same ICCID.
      const res = await api.saveInstance(body,targetDevice,newLineClaim.current)
      await refresh()
      if (currentFormID.current === forId && currentTarget.current===forTarget) {
        setForm({...emptyInstance(),...res})
        setSavedLineId(String(res.id));setSelected(String(res.id))
        setCreating(res.provisioning_state === 'draft')
        setPinMsg(t('Catalog saved. Runtime apply is a separate action.'))
      }
    } catch (e) {
      if(e.createdDraft && currentFormID.current===forId && currentTarget.current===forTarget) {
        setForm({...emptyInstance(),...e.createdDraft});setSavedLineId(String(e.createdDraft.id));setSelected(String(e.createdDraft.id))
      }
      alert(e.message)
    }
    setSaving(false)
    mutationBusy.current = false
  }

  const del = async () => {
    if (mutationBusy.current) return
    const lineLabel = form.name || `${form.mcc || ''}-${form.mnc || ''}` || form.id
    const warning = deleteHistory
      ? t('Delete this SIM line and all of its messages and call records?')
      : t('Delete this SIM line? Messages and call records will be preserved.')
    if (!confirm(`${t('You are deleting SIM line “{name}” (ID {id}).', { name: lineLabel, id: form.id })}\n\n${warning}\n\n${t('If the SIM is still inserted, automatic setup pauses until it is removed and inserted again.')}`)) return
    const typed = prompt(t('Type the line ID “{id}” to confirm deletion.', { id: form.id }), '')
    if (String(typed || '').trim() !== String(form.id)) {
      if (typed !== null) alert(t('Line ID did not match. Nothing was deleted.'))
      return
    }
    setDeleting(true)
    mutationBusy.current = true
    const forId = form.id
    try {
      let request = deletionRequests.current.get(forId)
      if (!request) { request = {operation:crypto.randomUUID(),deleteHistory}; deletionRequests.current.set(forId,request) }
      if (request.deleteHistory !== deleteHistory) throw new Error('line_deletion_intent_changed')
      await api.deleteInstance(forId, deleteHistory, form.__catalog_revision, request.operation)
      if (currentFormID.current !== forId) { await refresh(); return }
      setSelected(null)
      setSavedLineId('')
      setForm(emptyInstance())
      await refresh()
      setPinMsg(t(deleteHistory ? 'SIM line and history deleted.' : 'SIM line deleted; history preserved.'))
    } catch (error) { alert(error.message) }
    finally { mutationBusy.current = false; setDeleting(false) }
  }

  const softDel = async () => {
    if (mutationBusy.current) return
    const lineLabel = form.name || `${form.mcc || ''}-${form.mnc || ''}` || form.id
    if (!confirm(t('Move SIM line “{name}” to Recycle Bin (Soft Delete)?\n\nThe line will be stopped and hidden, but all settings, messages, and call history are preserved and can be restored at any time.', { name: lineLabel }))) return
    setDeleting(true)
    mutationBusy.current = true
    const forId = form.id
    try {
      await api.softDeleteInstance(form.id, form.__catalog_revision)
      if (currentFormID.current !== forId) { await refresh(); return }
      setSelected(null)
      setSavedLineId('')
      setForm(emptyInstance())
      await refresh()
      setPinMsg(t('SIM line moved to Recycle Bin (Soft-deleted).'))
    } catch (error) { alert(error.message) }
    finally { mutationBusy.current = false; setDeleting(false) }
  }


  const deleteSavedPin = async () => {
    if (mutationBusy.current || !pinConfiguration?.configured || !pinConfiguration.revision) return
    if (!window.confirm(t('Remove the saved PIN from this Agent? The SIM card itself will not be changed.'))) return
    mutationBusy.current = true; setPinBusy(true)
    const epoch = pinEpoch.current
    try {
      const target = simPINIdentity(targetDevice,form.iccid)
      const result = await api.simPIN({operation_id:crypto.randomUUID(),...target,action:'remove_saved',
        pin:'',expected_config_revision:pinConfiguration.revision})
      if (pinEpoch.current !== epoch) return
      setPinConfiguration(result.configuration || null); setProof(null); setPin('')
      setPinMsg(result.state || 'unknown')
      await refresh()
    } catch (error) { setPinMsg(error.message) }
    finally { mutationBusy.current = false; setPinBusy(false) }
  }
  const missing = targetDevice?.provisioning?.missing || []
  const missingLabels = { imsi: 'IMSI / PIN', imei: 'IMEI', smsc: t('SMS centre (SMSC)') }
  const imeiReady = String(targetDevice?.imei || '').replace(/[^0-9]/g, '').length === 15
  const existingLine = instances.some(line => String(line.id) === String(form.id))

  return (
    <div style={{ maxWidth: 1000 }}>
      {!!instances.length && <div className="u-saved-lines">
        <div><label>{t('Saved SIM line')}</label><p>{t('Select a saved SIM line to edit or delete it. This list does not depend on whether its former device is connected.')}</p></div>
        <select value={boundLineId || savedLineId} disabled={!!boundLineId} onChange={event => {
          const value = event.target.value
          setCreating(false)
          setSavedLineId(value)
          setSelected(value || null)
        }}>
          <option value="">{t('Choose a saved line')}</option>
          {instances.map(line => <option value={line.id} key={line.id}>{line.name || `${line.mcc || ''}-${line.mnc || ''}`} · {lineCompositeStatus(line, devices, t)}</option>)}
        </select>
      </div>}
      {!!savedLineId && !!targetDevice?.sim?.iccid && !!managedSelected?.iccid &&
        String(targetDevice.sim.iccid) !== String(managedSelected.iccid) &&
        <div className="u-note" style={{ marginBottom: 14 }}>
          {t('This saved line belongs to another ICCID. Selecting it only opens that saved record; it does not bind it to the current SIM.')}
        </div>}
      {creating && <div className="u-note" style={{ marginBottom: 14 }}>
        <b>{t(form.__catalog_revision ? 'Disabled draft' : 'Unconfigured SIM')}</b><br />
        {providerOnly
          ? t('The operating-system modem provider exposes identity and SMS, but not raw APDU access. You can edit and save the number, SMS centre and line metadata; VoWiFi remains unavailable until an APDU-capable function is present.')
          : t('Claim the SIM, save its configuration, then verify and provision the hardware.')}
        {!!missing.length && <div style={{ marginTop: 6 }}>{t('Missing information')}: {missing.map(key => missingLabels[key] || key).join('、')}</div>}
      </div>}
      <div className="mdd-sim-layout">
      {/* Card / PIN panel */}
      <div className="card" style={{ padding: 20 }}>
        <h3 style={{ marginTop: 0 }}>{t('SIM card')}</h3>
        <Field label={t('Reader')}>
          <select value={form.reader_index} disabled={!!targetDevice} onChange={(e) => upd({ reader_index: +e.target.value, reader_port: portForIdx(+e.target.value) || form.reader_port })}>
            {readers.map((r, i) => <option key={i} value={i}>{i}: {compactReaderName(r)}{portForIdx(i) ? ` — USB ${portForIdx(i)}` : ''}</option>)}
            {readers.length === 0 && <option>{t('No readers')}</option>}
          </select>
        </Field>
        {form.reader_port &&
          <div className="mono" style={{ fontSize: 11, color: 'var(--text-mute)', marginTop: 4 }}>
            {t('Bound to USB port {port} (stable across reader re-enumeration)', { port: form.reader_port })}
          </div>}
        <button className="btn btn-ghost" style={{ marginTop: 10 }} onClick={detect} disabled={detecting || saving || deleting || pinBusy || !targetDevice}>{detecting ? t('Detecting…') : providerOnly ? t('Refresh SIM identity') : t('Detect card')}</button>
        {card && (
          <div className="mono" style={{ fontSize: 12, color: card.present ? 'var(--text-dim)' : '#ef4444', marginTop: 12, lineHeight: 1.6 }}>
            {card.present ? (<>
              ICCID: {card.iccid || '—'}<br />IMSI: {card.imsi || t('(locked)')}<br />
              {card.reader_port && <>{t('USB port')}: {card.reader_port}<br /></>}
              PIN: {card.pin_enabled == null ? t('Unknown') : card.pin_enabled ? t('enabled, {tries} tries', { tries: card.pin_tries }) : t('disabled')}
            </>) : (<>{t('No SIM card in reader {reader}.', { reader: card.reader_index })}</>)}
          </div>
        )}
        <hr style={{ borderColor: 'var(--border)', margin: '16px 0' }} />
        <Field label={t('SIM PIN (CHV1)')}>
          <input type="password" autoComplete="off" inputMode="numeric" maxLength={8} value={pin} disabled={!proof || pinBusy} onChange={(e) => setPin(e.target.value.replace(/\D/g,''))} placeholder={t('Current SIM PIN')} />
        </Field>
        <div style={{ display: 'flex', gap: 8, marginTop: 10, flexWrap: 'wrap' }}>
          <button className="btn btn-ghost" onClick={checkPINStatus} disabled={pinBusy || !targetDevice}>{t('Check PIN status')}</button>
          <button className="btn btn-primary" onClick={() => verifyPin(false)} disabled={pinBusy || !proof || !/^\d{4,8}$/.test(pin)}>{t('Verify once')}</button>
          <button className="btn btn-ghost" onClick={() => verifyPin(true)} disabled={pinBusy || !proof || !pinConfiguration?.revision || !/^\d{4,8}$/.test(pin)}>{t('Verify and save on Agent')}</button>
          {pinConfiguration?.configured &&
            <button className="btn btn-ghost" disabled={pinBusy} style={{ color: '#ef4444' }} onClick={deleteSavedPin}>{t('Delete saved PIN')}</button>}
        </div>
        {form.id && (
          <div style={{ fontSize: 12, color: 'var(--text-mute)', marginTop: 8 }}>
            {t(!pinConfiguration ? 'Saved PIN status unknown' : pinConfiguration.configured ? 'Saved on Agent' : 'Not saved on Agent')}
          </div>
        )}
        {pinMsg && <div style={{ fontSize: 13, marginTop: 10, color: pinMsg.includes('OK') || pinMsg.includes('read') || pinMsg === 'Saved.' || pinMsg.includes('deleted') ? '#22c55e' : '#eab308' }}>{pinMsg}</div>}
      </div>

      {/* Instance form */}
      <div className="card" style={{ padding: 20 }}>
        <h3 style={{ marginTop: 0 }}>{t('Line configuration')}</h3>
        <label><input type="checkbox" checked={form.enabled === true} disabled={!form.__catalog_revision || form.provisioning_state === 'draft' || saving || deleting || pinBusy} onChange={event => upd({enabled:event.target.checked})} />{t('Enabled in Provider catalog')}</label>
        <div className="mdd-sim-fields">
          {!creating && <Field label={t('Instance ID')}><input className="mono" value={form.id} readOnly title={t('Assigned by the system and cannot be changed.')} /></Field>}
          <Field label={t('Name')}><input value={form.name} onChange={(e) => upd({ name: e.target.value })} placeholder="Telus" /></Field>
          <Field label="IMSI"><input className="mono" value={form.imsi} onChange={(e) => upd({ imsi: e.target.value })} /></Field>
          <Field label="MCC"><input value={form.mcc} onChange={(e) => upd({ mcc: e.target.value })} /></Field>
          <Field label="MNC"><input value={form.mnc} onChange={(e) => upd({ mnc: e.target.value })} /></Field>
          <Field label={t('Proxy country override')}><input className="mono" value={form.proxy_country || ''} maxLength={2}
            onChange={(e) => upd({ proxy_country: e.target.value.replace(/[^a-z]/gi, '').toLowerCase() })}
            placeholder={`auto (${(form.proxy_country_effective || 'MCC').toUpperCase()})`} /></Field>
          <Field label={t('Phone number (MSISDN)')}><input className="mono" value={form.msisdn} onChange={(e) => upd({ msisdn: e.target.value })} placeholder={t('auto-learned')} /></Field>
          <Field label={t('SMS centre (SMSC)')}>
            <div style={{ display: 'flex', gap: 12, marginBottom: 6, fontSize: 13 }}>
              <label style={{ display: 'flex', alignItems: 'center', gap: 5, cursor: 'pointer' }}>
                <input type="radio" name="scmode" checked={smscMode === 'auto'} style={{ width: 'auto' }}
                  onChange={() => { setSmscMode('auto'); if (card?.smsc) upd({ smsc: card.smsc }) }} />{t('Auto (from SIM)')}
              </label>
              <label style={{ display: 'flex', alignItems: 'center', gap: 5, cursor: 'pointer' }}>
                <input type="radio" name="scmode" checked={smscMode === 'manual'} style={{ width: 'auto' }}
                  onChange={() => setSmscMode('manual')} />{t('Manual')}
              </label>
            </div>
            <input className="mono" value={form.smsc} readOnly={smscMode === 'auto'}
              onChange={(e) => upd({ smsc: e.target.value })}
              placeholder={smscMode === 'auto' ? t('detect card / verify PIN to read from SIM') : '+1...'}
              style={smscMode === 'auto' ? { opacity: .7 } : undefined} />
          </Field>
          {!creating && <Field label="ICCID"><input className="mono" value={form.iccid || ''} readOnly /></Field>}
          <Field label="APN"><input className="mono" value={form.apn ?? 'ims'} onChange={(e) => upd({ apn: e.target.value })} placeholder="ims" /></Field>
          <Field label={t('ePDG identity (IDr)')}>
            <select value={form.idr_mode ?? 'apn'} onChange={(e) => upd({ idr_mode: e.target.value })}>
              <option value="apn">{t('Bare APN (default)')}</option>
              <option value="fqdn">APN-FQDN</option>
            </select>
          </Field>
          <Field label={t('IMS address family (CP)')}>
            <select value={form.cp_mode ?? 'auto'} onChange={(e) => upd({ cp_mode: e.target.value })}>
              <option value="auto">{t('Auto-detect (recommended)')}</option>
              <option value="dual">{t('Dual-stack (IPv4+IPv6)')}</option>
              <option value="v6">{t('IPv6 only')}</option>
              <option value="v4">{t('IPv4 only')}</option>
            </select>
            {form.cp_mode && form.cp_mode !== 'auto' && form.cp_mode_source === 'auto' && (
              <div style={{ fontSize: 11, color: 'var(--text-mute)', marginTop: 2 }}>
                {t('Auto-detected: {mode}. Switch back to Auto-detect to re-probe.', { mode: form.cp_mode.toUpperCase() })}
              </div>
            )}
          </Field>
        </div>
        <p className="u-note">{imeiReady
          ? t('The IMEI is inherited from this device’s Hardware settings.')
          : t('Set a 15-digit IMEI on the Hardware tab before starting VoWiFi.')}</p>
        <p className="u-note">{t('Desired IMS network')}: {form.apn || 'ims'} · {form.idr_mode || 'apn'} · {form.cp_mode || 'auto'}<br/>{t('Actual IMS network')}: {actualNetwork.responderID || '—'} · {actualNetwork.pdnFamily || '—'}</p>
        <div style={{ fontSize: 11, color: 'var(--text-mute)', marginTop: 4 }}>
          {t('IDr help')}
        </div>
        <div style={{ fontSize: 11, color: 'var(--text-mute)', marginTop: 4 }}>
          {t('IMS address family help')}
        </div>

        <details style={{ marginTop: 12 }}>
          <summary>{t('Advanced IMS identity')}</summary>
          <p className="u-note">{t('Carrier defaults are applied automatically. Change these fields only when required by the carrier.')}</p>
          <div className="mdd-sim-fields">
            <Field label="P-Access-Network-Info (PANI)">
              <input className="mono" value={form.sip.pani || ''} onChange={(e) => updSip({ pani: e.target.value })}
                placeholder={t('Automatic carrier default')} />
            </Field>
            <Field label={t('IMS access type')}>
              <input className="mono" value={form.sip.access_type || ''} onChange={(e) => updSip({ access_type: e.target.value })}
                placeholder={t('Automatic carrier default')} />
            </Field>
          </div>
          <label style={{ marginTop: 8 }}>
            <input type="checkbox" style={{ width: 'auto', marginRight: 8 }} checked={!!form.sip.user_eq_phone}
              onChange={(e) => updSip({ user_eq_phone: e.target.checked })} />
            {t('Add ;user=phone to telephone-number SIP requests')}
          </label>
        </details>

        <div style={{ display: 'flex', gap: 8, marginTop: 18 }}>
          <button className="btn btn-primary" onClick={save} disabled={saving || deleting || detecting || pinBusy || (!form.__catalog_revision && !(creating && targetDevice?.present === true && targetDevice?.sim?.iccid && !targetDevice.stale && !targetDevice.observed_only))}>{t('Save')}</button>
        </div>
        <ProvisionActions form={form} device={targetDevice} refresh={refresh} operationLock={mutationBusy} blocked={saving || deleting || detecting || pinBusy}
          onSaved={value => { setForm({...emptyInstance(),...value}); setCreating(value.provisioning_state === 'draft'); setSavedLineId(String(value.id)); setSelected(String(value.id)) }} />
        {existingLine && <div className="u-line-delete" style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 12 }}>
            <div>
              <h4 style={{ margin: 0 }}>{t('Move to Recycle Bin (Soft Delete)')}</h4>
              <p style={{ margin: '4px 0 0', fontSize: 12, color: 'var(--text-mute)' }}>{t('Hides this SIM and stops its line. Settings and history are 100% preserved and can be restored anytime.')}</p>
            </div>
            <button className="btn btn-ghost" style={{ color: '#d97706', borderColor: '#fcd34d' }} disabled={deleting} onClick={softDel}>
              {t('Move to Recycle Bin')}
            </button>
          </div>
          <hr style={{ border: 'none', borderTop: '1px solid var(--border)', margin: 0 }} />
          <div>
            <h4>{t('Permanently Delete SIM line')}</h4>
            <p>{t('Deletes IMS settings, saved PIN, routing and runtime files for this SIM. The physical device record is not affected.')}</p>
            <label><input type="checkbox" checked={deleteHistory} onChange={event => setDeleteHistory(event.target.checked)} />{t('Also delete all messages and call records')}</label>
          </div>
          <button className="btn btn-danger" disabled={deleting} onClick={del}>{t(deleting ? 'Deleting…' : 'Delete permanently')}</button>
        </div>}

      </div>
      </div>
    </div>
  )
}
