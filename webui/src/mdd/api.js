// Adapter for the customized ec620942 MDD frontend. Never call retired APIs.
import { api as go } from '../api.js'
import { historyAPI } from './historyAdapter.js'
import { notificationAPI } from './notificationAdapter.js'
import { networkAPI } from './networkAdapter.js'
import { smsAPI } from './smsAdapter.js'
import { systemAPI } from './systemAdapter.js'
import { esimAPI } from './esimAdapter.js'
import { lineAPI } from './lineAdapter.js'
import { hardwareAPI } from './hardwareAdapter.js'
export { getBasePrefix, setCsrf, setAuthToken, getAuthToken, connectWs } from '../api.js'

// Remaining original page operations are deliberately not stubbed as success.
// This is the mounted entrypoint; unresolved actions are not evidence of parity.
export const api = { ...go, ...historyAPI, ...notificationAPI, ...networkAPI, ...smsAPI, ...systemAPI, ...esimAPI, ...lineAPI, ...hardwareAPI }
