import React, { useEffect, useRef, useState } from 'react'
import { api } from '../api.js'
import { useI18n } from '../i18n.jsx'
import { dialogs } from '../../dialogs.js'

const mib = 1024 ** 2
const amount = bytes => `${(Number(bytes || 0) / mib).toFixed(2)} MiB`

export default function DonorBudget({ card, lineID, showToast }) {
  const { t } = useI18n()
  const [budget, setBudget] = useState(null)
  const [limit, setLimit] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const mounted = useRef(true), active = useRef(null), edit = useRef(0), current = useRef(null)
  current.current = {card, lineID, revision:budget?.revision, edit:edit.current}
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; active.current = null } }, [])
  const owns = operation => mounted.current && active.current === operation && current.current.card === operation.card &&
    current.current.lineID === operation.lineID && current.current.edit === operation.edit && current.current.revision === operation.revision
  const begin = () => {
    if (active.current) return null
    const operation = {...current.current}
    active.current = operation; setBusy(true)
    return operation
  }
  const finish = operation => {
    if (active.current !== operation) return
    active.current = null
    if (mounted.current) setBusy(false)
  }
  const accept = value => {
    if (value.card_id !== card) throw new Error(t('Donor identity changed. Reload the device list.'))
    setBudget(value); setLimit(String(value.limit_bytes / mib)); setError('')
  }
  useEffect(() => {
    let disposed = false
    const operation = {card, lineID, revision:undefined, edit:++edit.current}
    current.current = operation
    active.current = lineID ? operation : null
    setBusy(Boolean(lineID)); setBudget(null); setError('')
    if (lineID) api.donorBudget(lineID)
      .then(value => { if (!disposed && owns(operation)) accept(value) })
      .catch(e => { if (!disposed && owns(operation)) setError(e.message) })
      .finally(() => finish(operation))
    return () => { disposed = true }
  }, [card, lineID])
  const refresh = async () => {
    const operation = begin()
    if (!operation) return
    try { const value = await api.donorBudget(operation.lineID); if (owns(operation)) accept(value) }
    catch (e) { if (owns(operation)) setError(e.message) }
    finally { finish(operation) }
  }
  const save = async reset => {
    if (!budget || budget.card_id !== card || active.current) return
    const bytes = Number(limit) * mib
    if (!limit.trim() || !Number.isSafeInteger(bytes) || bytes < 0 || bytes > 1024 ** 4) { setError(t('Enter a limit from 0 to 1048576 MiB.')); return }
    const operation = begin()
    if (!operation) return
    const input = {expected_card_id:operation.card, revision:operation.revision, limit_bytes:bytes, reset, confirm_reset:reset}
    try {
      if (reset && !await dialogs.confirm(t('Reset measured and uncertain usage for ICCID {card}? This authorizes a new full budget. No borrowing session may be active.', { card:operation.card }))) return
      if (!owns(operation)) return
      const value = await api.saveDonorBudget(operation.lineID, input)
      if (!owns(operation)) return
      accept(value)
      showToast(t(reset ? 'Borrowing budget reset.' : 'Borrowing limit saved.'))
    } catch (e) { if (owns(operation)) setError(t(e.code?.startsWith('donor_budget_') ? e.code : e.message)) }
    finally { finish(operation) }
  }
  if (!lineID) return <small>{t('SIM budget is unavailable until its saved line is identified.')}</small>
  return <section className="u-donor-budget" aria-label={t('Borrowed data budget')}>
    <strong>{t('Borrowed data budget')}</strong>
    {budget && <>
      <dl className="u-kv"><dt>{t('Measured usage')}</dt><dd>{amount(budget.used_bytes)}</dd>
        <dt>{t('Remaining allowance')}</dt><dd>{amount(budget.limit_bytes - budget.used_bytes - budget.uncertain_bytes)}</dd>
        <dt>{t('Reserved, not yet used')}</dt><dd>{amount(budget.reserved_bytes)}</dd>
        {budget.uncertain_bytes > 0 && <><dt>{t('Uncertain usage after interruption')}</dt><dd>{amount(budget.uncertain_bytes)}</dd></>}
        <dt>{t('Metering since')}</dt><dd>{budget.started_at && !budget.started_at.startsWith('0001-') ? new Date(budget.started_at).toLocaleString() : t('Not started')}</dd></dl>
      <small>{t('MDD borrowing only; earlier and carrier-billed usage is not included.')}</small>
      {!budget.configured && <small>{t('Existing 1 TiB limit retained; no limit has been chosen for this SIM yet.')}</small>}
      <label>{t('Total limit (MiB, 0 blocks new borrowing)')}<input type="number" min="0" max="1048576" step="1" value={limit} onChange={e => {edit.current++; setLimit(e.target.value)}} disabled={busy}/></label>
      <div className="u-inline"><button type="button" className="btn btn-primary" disabled={busy} onClick={() => save(false)}>{t('Save limit')}</button>
        <button type="button" className="btn btn-ghost" disabled={busy || budget.owner_state !== 'stopped'} onClick={() => save(true)}>{t('Reset usage')}</button></div>
    </>}
    {error && <p role="alert" className="u-error">{error}</p>}
    <button type="button" className="btn btn-ghost" disabled={busy} onClick={refresh}>{t('Read current state')}</button>
  </section>
}
