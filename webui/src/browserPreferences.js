export const CALL_AUDIO_BUFFER_DEFAULT_MS = 500
export const CALL_AUDIO_BUFFER_MIN_MS = 100
export const CALL_AUDIO_BUFFER_MAX_MS = 2000

const callAudioBufferKey = 'mdd.cellular_audio_buffer_ms'
let cachedCallAudioBufferMS = null

export function normalizeCallAudioBufferMS(value) {
  if (value === null || value === undefined || value === '') return CALL_AUDIO_BUFFER_DEFAULT_MS
  const number = Number(value)
  if (!Number.isFinite(number)) return CALL_AUDIO_BUFFER_DEFAULT_MS
  return Math.max(CALL_AUDIO_BUFFER_MIN_MS, Math.min(CALL_AUDIO_BUFFER_MAX_MS, Math.round(number)))
}

export function getCallAudioBufferMS(storage = globalThis.localStorage) {
	if (cachedCallAudioBufferMS !== null) return cachedCallAudioBufferMS
  try { return normalizeCallAudioBufferMS(storage?.getItem(callAudioBufferKey)) }
  catch { return CALL_AUDIO_BUFFER_DEFAULT_MS }
}

export function saveCallAudioBufferMS(value, storage = globalThis.localStorage) {
  const normalized = normalizeCallAudioBufferMS(value)
	cachedCallAudioBufferMS = normalized
  try { storage?.setItem(callAudioBufferKey, String(normalized)) } catch {}
  return normalized
}

export function cacheCallAudioBufferMS(value) {
	return saveCallAudioBufferMS(value)
}
