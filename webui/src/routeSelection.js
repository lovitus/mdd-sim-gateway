function blocked(operation) {
  const codes = (operation?.facts || [])
    .filter(fact => fact?.fresh !== true || fact?.available !== true)
    .map(fact => fact?.code || fact?.layer)
    .filter(Boolean)
  return (codes.length ? codes : (operation?.blocked || ['state_unavailable'])).join(', ')
}

export function callRouteOptions(instances) {
  const values = []
  for (const line of instances || []) {
    for (const [mode, key] of [['vowifi', 'vowifi_call'], ['cellular', 'cellular_call']]) {
      const operation = line.operations?.[key]
      values.push({ line, mode, ready: operation?.ready === true, blocked: blocked(operation) })
    }
  }
  return values
}

export function messageRouteOptions(instances) {
  const values = []
  for (const line of instances || []) {
    for (const [transport, key] of [['vowifi', 'vowifi_sms'], ['cellular', 'cellular_sms']]) {
      const operation = line.operations?.[key]
      values.push({ line, transport, ready: operation?.ready === true, blocked: blocked(operation) })
    }
  }
  return values
}

export function routeKey(route) {
  if (!route) return ''
  return `${route.mode || route.transport}:${route.line.id}`
}

export function routeForExactLine(options, lineID) {
  const exact = (options || []).filter(value => String(value.line.id) === String(lineID))
  return exact.find(value => value.ready) || exact[0] || null
}

export function retainOrDefaultRoute(options, currentKey) {
  return (options || []).find(value => routeKey(value) === currentKey) ||
    (options || []).find(value => value.ready) || (options || [])[0] || null
}
