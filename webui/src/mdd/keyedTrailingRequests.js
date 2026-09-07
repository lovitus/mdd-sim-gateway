export class KeyedTrailingRequests {
  constructor({ run, commit, active, retryDelaysMs = [], shouldRetry = () => true,
    onError = () => {} }) {
    this.run = run
    this.commit = commit
    this.active = active
    this.retryDelaysMs = retryDelaysMs
    this.shouldRetry = shouldRetry
    this.onError = onError
    this.states = new Map()
  }

  request(key, { fresh = false } = {}) {
    key = String(key || '')
    if (!key || !this.active(key)) return Promise.resolve(null)
    let state = this.states.get(key)
    if (!state) {
      state = { epoch: 0, promise: null, trailing: false, retries: 0,
        retryTimer: null, exhausted: false }
      this.states.set(key, state)
    }
    if (fresh) {
      state.epoch += 1
      clearTimeout(state.retryTimer)
      state.retryTimer = null
      state.retries = 0
      state.exhausted = false
      if (state.promise) {
        state.trailing = true
        return state.promise
      }
    }
    if (state.promise) return state.promise
    if (state.retryTimer !== null || state.exhausted) return Promise.resolve(null)
    return this._start(key, state)
  }

  _start(key, state) {
    if (this.states.get(key) !== state || !this.active(key)) return Promise.resolve(null)
    const epoch = state.epoch
    const promise = Promise.resolve().then(() => this.run(key)).then(value => {
      if (this.states.get(key) !== state || state.epoch !== epoch || !this.active(key))
        return null
      state.retries = 0
      state.exhausted = false
      return this.commit(key, value)
    }).catch(error => {
      if (this.states.get(key) !== state || state.epoch !== epoch || !this.active(key))
        return null
      const delay = this.retryDelaysMs[state.retries]
      if (delay !== undefined && this.shouldRetry(error)) {
        state.retries += 1
        state.retryTimer = setTimeout(() => {
          if (this.states.get(key) !== state || state.epoch !== epoch || !this.active(key)) return
          state.retryTimer = null
          this._start(key, state)
        }, delay)
      } else {
        // Opt-in only: callers without retry policy retain ordinary reread semantics.
        state.exhausted = this.retryDelaysMs.length > 0
        this.onError(key, error)
      }
      return null
    }).finally(() => {
      // A cancelled request may finish after the same key has been recreated. Never let that
      // old completion mutate or release the replacement state.
      if (this.states.get(key) !== state || state.promise !== promise) return
      state.promise = null
      if (state.trailing && this.active(key)) {
        state.trailing = false
        this._start(key, state)
      }
    })
    state.promise = promise
    return promise
  }

  cancel(key) {
    key = String(key || '')
    const state = this.states.get(key)
    if (!state) return
    state.epoch += 1
    state.trailing = false
    clearTimeout(state.retryTimer)
    this.states.delete(key)
  }

  cancelExcept(activeKeys) {
    const active = activeKeys instanceof Set ? activeKeys : new Set(activeKeys || [])
    for (const key of [...this.states.keys()]) {
      if (!active.has(key)) this.cancel(key)
    }
  }

  clear() {
    for (const state of this.states.values()) {
      state.epoch += 1
      state.trailing = false
      clearTimeout(state.retryTimer)
    }
    this.states.clear()
  }
}
