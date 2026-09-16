export function createDialogStore() {
  let current = null
  let nextID = 0
  let resolveCurrent = null
  const listeners = new Set()
  const emit = () => { for (const listener of listeners) listener() }
  const cancelled = type => type === 'prompt' ? null : false
  const finish = (id, value) => {
    if (!current || current.id !== id) return
    const resolve = resolveCurrent
    const type = current.type
    current = null
    resolveCurrent = null
    emit()
    resolve(type === 'prompt' ? (value == null ? null : String(value)) : value === true)
  }
  const open = (type, message, initialValue = '') => {
    // Repeated clicks cannot queue another destructive confirmation behind this one.
    if (current) return Promise.resolve(cancelled(type))
    return new Promise(resolve => {
      resolveCurrent = resolve
      current = { id: ++nextID, type, message: String(message ?? ''), initialValue: String(initialValue ?? '') }
      emit()
    })
  }
  return {
    subscribe(listener) { listeners.add(listener); return () => listeners.delete(listener) },
    getSnapshot: () => current,
    alert: message => open('alert', message),
    confirm: message => open('confirm', message),
    prompt: (message, initialValue) => open('prompt', message, initialValue),
    finish,
    cancelAll() { if (current) finish(current.id, cancelled(current.type)) },
  }
}

export const dialogs = createDialogStore()
