// Projects existing Go diagnostic observations; no hardware actions or retries.
export function diagnosticStatus(status, code, fresh = true) {
  if (fresh === false) return 'unknown'
  if (['line_disabled', 'vowifi_disabled', 'unsupported'].includes(code)) return 'skipped'
  if (['pass', 'ready', 'running'].includes(status)) return 'pass'
  if (['fail', 'failed', 'error', 'degraded', 'blocked'].includes(status)) return 'fail'
  return 'unknown'
}

export async function runAdvancedDiagnostics(read, browser, publish, cancelled = () => false) {
  const emit = row => { if (!cancelled()) publish({...row, observed_at: new Date().toISOString()}) }
  for (const [id, available] of Object.entries(browser)) emit({group:'browser', id, kind:'capability', status:available ? 'pass' : 'unknown', code:available ? 'available' : 'unavailable'})
  for (const resource of ['runtime', 'diagnostics', 'components', 'exits']) {
    if (cancelled()) return
    try {
      const value = await read(resource)
      if (cancelled()) return
      emit({group:'core', id:resource, kind:'read', status:'pass', code:'api_read_succeeded'})
      if (resource === 'diagnostics') {
        if (!Array.isArray(value.checks) || !Array.isArray(value.lines) || !Array.isArray(value.agents)) throw Object.assign(new Error('diagnostic_schema_invalid'),{code:'diagnostic_schema_invalid'})
        for (const check of value.checks) emit({group:check.scope?.startsWith('agent:') ? 'agent' : 'component', id:check.id, kind:check.kind || 'observation', status:diagnosticStatus(check.status,check.code), code:check.code})
        if (!value.agents.length) emit({group:'agent',id:'inventory',kind:'observation',status:'unknown',code:'no_connected_agents'})
        for (const line of value.lines) for (const fact of line.facts || []) emit({group:'line',id:`${line.line_id}:${fact.layer}`,kind:'observation',status:diagnosticStatus(fact.condition,fact.code,fact.fresh),code:fact.code})
      }
      if (resource === 'runtime') emit({group:'component',id:'core_build',kind:'observation',status:value.vcs_revision || value.build_version ? 'pass' : 'unknown',code:value.vcs_revision || value.build_version ? 'build_identified' : 'build_unknown'})
      if (resource === 'components') emit({group:'component',id:'provider_apply',kind:'observation',status:value.applying === false ? 'pass' : 'unknown',code:value.applying === false ? 'apply_idle' : 'apply_busy_or_unknown'})
      if (resource === 'exits') {
        if (!Array.isArray(value.exits)) throw Object.assign(new Error('diagnostic_schema_invalid'),{code:'diagnostic_schema_invalid'})
        if (!value.exits.length) emit({group:'component',id:'exits',kind:'observation',status:'skipped',code:'no_exits'})
        for (const [index, exit] of value.exits.entries()) emit({group:'component',id:`exit:${index+1}`,kind:'observation',status:exit.enabled === false ? 'skipped' : exit.ready === true ? 'pass' : exit.ready === false ? 'fail' : 'unknown',code:exit.enabled === false ? 'exit_disabled' : exit.ready === true ? 'exit_ready' : 'exit_not_ready'})
      }
    } catch (error) {
      emit({group:'core',id:resource,kind:'read',status:'fail',code:error.code || (error.name === 'AbortError' ? 'request_timeout' : 'diagnostic_read_failed')})
      if ([401,403].includes(error.status)) return
    }
  }
}

export function diagnosticReport(rows, outcome) {
  return {schema_version:1, generated_at:new Date().toISOString(), outcome,
    checks:rows.map((row,index)=>({index:index+1,group:row.group,kind:row.kind,status:row.status,
      code:/^[a-z][a-z_]{0,63}$/.test(row.code || '') ? row.code : 'detail_omitted',observed_at:row.observed_at}))}
}
