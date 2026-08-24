export class KeyedTrailingRequests {
  constructor({ run, commit, active }) {
    this.run = run
    this.commit = commit
    this.active = active
    this.states = new Map()
  }

  request(key, { fresh = false } = {}) {
    key = String(key || '')
    if (!key || !this.active(key)) return Promise.resolve(null)
    let state = this.states.get(key)
    if (!state) {
      state = { epoch: 0, promise: null, trailing: false }
      this.states.set(key, state)
    }
    if (fresh) {
      state.epoch += 1
      if (state.promise) {
        state.trailing = true
        return state.promise
      }
    }
    if (state.promise) return state.promise
    return this._start(key, state)
  }

  _start(key, state) {
    if (this.states.get(key) !== state || !this.active(key)) return Promise.resolve(null)
    const epoch = state.epoch
    const promise = Promise.resolve().then(() => this.run(key)).then(value => {
      if (this.states.get(key) !== state || state.epoch !== epoch || !this.active(key))
        return null
      return this.commit(key, value)
    }).catch(() => null).finally(() => {
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
    }
    this.states.clear()
  }
}
