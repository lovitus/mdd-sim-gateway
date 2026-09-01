export function createToastLifecycle({
  setToast,
  timerRef,
  schedule = globalThis.setTimeout,
  clear = globalThis.clearTimeout,
  now = () => Date.now(),
  durationMS = 15000,
}) {
  if (typeof setToast !== 'function' || !timerRef || typeof schedule !== 'function' ||
      typeof clear !== 'function' || typeof now !== 'function' || durationMS < 1000)
    throw new TypeError('invalid toast lifecycle configuration')
  const dismiss = () => {
    if (timerRef.current != null) clear(timerRef.current)
    timerRef.current = null
    setToast(null)
  }
  const show = message => {
    if (timerRef.current != null) clear(timerRef.current)
    setToast({ message, id: now() })
    timerRef.current = schedule(dismiss, durationMS)
  }
  const cleanup = () => {
    if (timerRef.current != null) clear(timerRef.current)
    timerRef.current = null
  }
  return { show, dismiss, cleanup }
}
