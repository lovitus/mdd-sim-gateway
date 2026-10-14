// One browser continuation for the existing revision-wide applier, across editors.
const key = 'mdd_provider_apply_pending'
export function pendingProviderApply() {
  const value=JSON.parse(sessionStorage.getItem(key) || 'null')
  if(value && (!value.id || !value.line_id || !value.card_id || !Number.isSafeInteger(value.revision)))throw new Error('apply_recovery_identity_changed')
  return value
}
export function rememberProviderApply(attempt) {
  if(pendingProviderApply())throw new Error('Application is not confirmed; read status before any further action.')
  sessionStorage.setItem(key,JSON.stringify(attempt))
}
export function archiveProviderApply(attempt,outcome) {
  sessionStorage.setItem(`${key}:${attempt.id}`,JSON.stringify({...attempt,...outcome}))
  if(pendingProviderApply()?.id===attempt.id)sessionStorage.removeItem(key)
}
export function providerApplyRejected(error) {
  return ['provider_apply_blocked','catalog_revision_changed','provider_render_failed','egress_status_invalid',
    'provider_apply_in_progress','configuration_apply_in_progress'].includes(error.code)
}
export function currentProviderReady(value,lineID) {
  const provider=value?.provider
  // The current endpoint validates the Provider/card binding and generation.
  // A still-fresh projection cannot override that read failing.
  return !!provider && !value.provider_error && !value.recovery_error && provider.line_id===lineID &&
    !!provider.process_generation && value.generation?.engine_run_id===provider.process_generation &&
    provider.maintenance?.draining===false && !provider.maintenance?.lease_id &&
    !['pending','unknown'].includes(value.recovery?.selection?.state)
}
export function providerApplyObserved(attempt,status,value,configuration) {
  return status.applied_revision===attempt.revision && !status.applying &&
    !['prepared','applying','manual_recovery_required','applied_resume_incomplete'].includes(status.last_state) &&
    configuration.id===attempt.line_id && configuration.iccid===attempt.card_id &&
    currentProviderReady(value,attempt.line_id)
}
