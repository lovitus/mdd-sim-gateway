// Browser softphone: JsSIP UA over WSS to the engine's Asterisk WebRTC transport.
import JsSIP from 'jssip'
import { api } from './api.js'

// Surface JsSIP internals in the console to aid troubleshooting (registration, ICE, etc.)
try {
  if (localStorage.getItem('mdd_sip_debug') === '1') JsSIP.debug.enable('JsSIP:*')
  else JsSIP.debug.disable('JsSIP:*')
} catch {}

export class Softphone {
  // audioEl: a persistent <audio> element rendered by React and handed in via ref. Using one
  // stable, DOM-attached element (instead of a per-call `new Audio()`) is what makes remote
  // audio reliable under Chrome/Edge autoplay policy: the element is primed once inside a user
  // gesture (unlockAudio) and then every later srcObject swap plays without a NotAllowedError.
  constructor(onEvent, audioEl) {
    this.onEvent = onEvent            // (type, data) => void
    this.ua = null
    this.session = null
    this.remoteAudio = audioEl || null
    this._ownsRemoteAudio = false
    this._attachedRemoteStream = null
    this._dead = false                // set true by stop() to inert late JsSIP events
    this._unlocked = false
    this._rec = null
    this._recCtx = null
    this._recChunks = []
    this._disconnects = 0
    this._mediaEvidencePromises = new WeakMap()
    this._iceServers = []
    this._mediaTestTarget = 'mdd-media-check'
    this._instanceId = ''
    this._callAttempt = 0
    this._creatingCanary = false
  }

  emit(type, data) {
    if (this._dead) return
    try { this.onEvent(type, data) } catch {}
  }

  // Point the class at the React-owned <audio> element. Called from the component's ref effect.
  setAudioEl(el) {
    if (!el) return
    if (this._ownsRemoteAudio && this.remoteAudio && this.remoteAudio !== el) {
      try { this.remoteAudio.remove() } catch {}
    }
    this.remoteAudio = el
    this._ownsRemoteAudio = false
  }

  ensureAudio() {
    // Fallback only: if no element was injected (shouldn't happen in the React app), create a
    // hidden, DOM-attached one so audio can still render.
    if (!this.remoteAudio) {
      const el = new Audio()
      el.autoplay = true
      el.setAttribute('playsinline', '')
      el.style.display = 'none'
      try { document.body.appendChild(el) } catch {}
      this.remoteAudio = el
      this._ownsRemoteAudio = true
    }
    return this.remoteAudio
  }

  // Prime the sink INSIDE a user gesture (Call / Answer / Connect click). Playing the element
  // (even empty) while the page has transient activation marks it user-activated; every later
  // play() on this SAME element then resolves, defeating the autoplay policy. Must be called
  // synchronously from the click handler — do not await anything before it.
  unlockAudio() {
    if (this._unlocked) return Promise.resolve(true)
    const el = this.ensureAudio()
    try {
      el.muted = true
      const p = el.play()
      if (p && p.then) return p.then(() => {
        try { el.pause(); el.currentTime = 0 } catch {}
        el.muted = false
        this._unlocked = true
        return true
      }).catch((err) => {
        el.muted = false
        this.emit('audioblocked', (err && err.name) || 'play-failed')
        return false
      })
      try { el.pause() } catch {}
      el.muted = false
      this._unlocked = true
      return Promise.resolve(true)
    } catch {
      el.muted = false
      return Promise.resolve(false)
    }
  }

  // Attach a remote MediaStream to the audio element and force playback. The element was
  // primed by unlockAudio() on the click, so play() should resolve; we keep the catch as
  // telemetry + arm a one-time gesture retry as a last resort.
  attachRemote(stream, session = this.session) {
    // One shared audio sink serves sequential sessions. A late ontrack/autoplay callback from
    // the just-terminated canary must never replace the real call's stream.
    if (this._dead || !stream || !session || session !== this.session) return
    if (session) session.__mddPlaybackReady = false
    const el = this.ensureAudio()
    if (el.srcObject !== stream) el.srcObject = stream
    this._attachedRemoteStream = stream
    el.muted = false
    el.volume = 1
    const p = el.play()
    if (p && p.then) p.then(() => {
      if (!this._dead && session) session.__mddPlaybackReady = true
    }).catch((err) => {
      if (this._dead) return
      if (session) session.__mddPlaybackReady = false
      this.emit('audioblocked', (err && err.name) || 'play-failed')
      const resume = () => {
        if (this._dead || session !== this.session) {
          document.removeEventListener('click', resume, true)
          document.removeEventListener('touchend', resume, true)
          return
        }
        el.play().then(() => {
          if (!this._dead && session === this.session) session.__mddPlaybackReady = true
        }).catch(() => {
          if (!this._dead && session === this.session) session.__mddPlaybackReady = false
        }).finally(() => {
          document.removeEventListener('click', resume, true)
          document.removeEventListener('touchend', resume, true)
        })
      }
      document.addEventListener('click', resume, true)
      document.addEventListener('touchend', resume, true)
    })
    else if (!this._dead && session) session.__mddPlaybackReady = true
  }

  async _audioStats(pc) {
    const totals = { outboundPackets: 0, outboundBytes: 0, inboundPackets: 0, inboundBytes: 0 }
    const reports = await pc.getStats()
    reports.forEach((report) => {
      const kind = report.kind || report.mediaType
      if (kind !== 'audio' || report.isRemote) return
      if (report.type === 'outbound-rtp') {
        totals.outboundPackets += Number(report.packetsSent || 0)
        totals.outboundBytes += Number(report.bytesSent || 0)
      } else if (report.type === 'inbound-rtp') {
        totals.inboundPackets += Number(report.packetsReceived || 0)
        totals.inboundBytes += Number(report.bytesReceived || 0)
      }
    })
    return totals
  }

  // Prove the browser leg with two WebRTC stats samples. SIP accepted/confirmed alone only
  // proves signalling and must never authorize the physical cellular ATD/ATA operation.
  waitForBidirectionalMedia(timeoutMs = 5000, session = this.session) {
    if (!session) return Promise.reject(new Error('Browser WebRTC session is unavailable'))
    const pending = this._mediaEvidencePromises.get(session)
    if (pending) return pending
    const promise = (async () => {
      const pc = session && session.connection
      if (!pc) throw new Error('Browser WebRTC connection is unavailable')
      const deadline = Date.now() + timeoutMs
      let previous = await this._audioStats(pc)
      while (Date.now() < deadline) {
        await new Promise((resolve) => setTimeout(resolve, 500))
        const current = await this._audioStats(pc)
        const senders = pc.getSenders().map((value) => value.track)
          .filter((track) => track && track.kind === 'audio')
        const receivers = pc.getReceivers().map((value) => value.track)
          .filter((track) => track && track.kind === 'audio')
        const evidence = {
          connection_state: (pc.connectionState === 'connected' || pc.connectionState === 'completed')
            ? pc.connectionState : (pc.iceConnectionState || pc.connectionState || ''),
          local_track_live: senders.some((track) => track.readyState === 'live' && track.enabled),
          remote_track_live: receivers.some((track) => track.readyState === 'live'),
          playback_started: session.__mddPlaybackReady === true,
          outbound_packets_delta: current.outboundPackets - previous.outboundPackets,
          outbound_bytes_delta: current.outboundBytes - previous.outboundBytes,
          inbound_packets_delta: current.inboundPackets - previous.inboundPackets,
          inbound_bytes_delta: current.inboundBytes - previous.inboundBytes,
        }
        if ((evidence.connection_state === 'connected' || evidence.connection_state === 'completed') &&
            evidence.local_track_live && evidence.remote_track_live && evidence.playback_started &&
            evidence.outbound_packets_delta > 0 && evidence.outbound_bytes_delta > 0 &&
            evidence.inbound_packets_delta > 0 && evidence.inbound_bytes_delta > 0) return evidence
        previous = current
      }
      throw new Error('Browser audio did not produce fresh bidirectional RTP before the timeout')
    })()
    this._mediaEvidencePromises.set(session, promise)
    return promise.finally(() => {
      if (this._mediaEvidencePromises.get(session) === promise)
        this._mediaEvidencePromises.delete(session)
    })
  }

  // prov: { username, password, ws_port, ws_url, host, realm }
  start(prov, host) {
    if (this.ua) this.stop()
    this._dead = false
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const wsUrl = prov.ws_url || `${proto}//${host}:${prov.ws_port}/ws`
    const socket = new JsSIP.WebSocketInterface(wsUrl)
    const domain = prov.domain || host
    const transport = wsUrl.startsWith('ws:') ? 'ws' : 'wss'
    this._iceServers = Array.isArray(prov.ice_servers) ? prov.ice_servers : []
    this._mediaTestTarget = prov.media_test_target || 'mdd-media-check'
    this._instanceId = String(prov.instance_id || prov.id || '')
    this.ua = new JsSIP.UA({
      sockets: [socket],
      uri: `sip:${prov.username}@${domain}`,
      password: prov.password,
      register: true,
      session_timers: false,
      contact_uri: `sip:${prov.username}@${domain};transport=${transport}`,
    })
    this.ua.on('connected', () => this.emit('ws', 'connected'))
    this.ua.on('disconnected', () => {
      if (this._dead) return
      this._disconnects += 1
      this.emit('ws', 'disconnected')
      if (this._disconnects >= 3) {
        this.emit('retryexhausted')
        setTimeout(() => this.stop(), 0)
      }
    })
    this.ua.on('registered', () => this.emit('registered', true))
    this.ua.on('unregistered', () => this.emit('registered', false))
    this.ua.on('registrationFailed', (e) => this.emit('regfail', (e && e.cause) || 'failed'))
    this.ua.on('newRTCSession', (e) => this.handleSession(e))
    this.ua.start()
  }

  handleSession(e) {
    const session = e.session
    if (this._creatingCanary || session.__mddMediaCanary) {
      session.__mddMediaCanary = true
      return
    }
    // If already in a call, reject any second incoming session as busy.
    if (this.session && this.session !== session) {
      if (session.direction === 'incoming') { try { session.terminate({ status_code: 486 }) } catch {} }
      return
    }
    this.session = session
    session.__mddPlaybackReady = false
    // Idempotency guard: an outgoing call reaches here twice (once from call(), once from
    // the UA's 'newRTCSession' for the same session). Binding listeners twice would double
    // -fire events; bind exactly once per session.
    if (session.__vowifiBound) return
    session.__vowifiBound = true
    const dir = session.direction  // 'incoming' | 'outgoing'
    if (dir === 'incoming') {
      const from = (session.remote_identity && session.remote_identity.uri && session.remote_identity.uri.user) || 'Unknown'
      this.emit('incoming', { from })
    }
    // A carrier may return an announcement as early media (183 with SDP) and never answer the
    // call — for example for balance, barring, or routing failures. Attach the receiver here as
    // well as on accepted/confirmed so that announcement is audible instead of the UI showing a
    // silent "ringing" state. Retry briefly because some browsers expose the receiver just after
    // JsSIP emits progress while applying the remote SDP.
    session.on('progress', () => {
      this.emit('progress')
      this.attachFromSession(session)
      setTimeout(() => this.attachFromSession(session), 100)
      setTimeout(() => this.attachFromSession(session), 400)
    })
    session.on('accepted', () => { this.emit('active'); this.attachFromSession(session) })
    session.on('confirmed', () => { this.emit('active'); this.attachFromSession(session) })
    // 'ended' (BYE received/sent) and 'failed' (setup error / non-2xx) are the terminal
    // events. Always null the session and tell the view so the UI resets to idle even if
    // only one of them fires.
    session.on('ended', (d) => { if (this.session === session) this.session = null; this.emit('ended', { cause: d && d.cause }) })
    session.on('failed', (d) => { if (this.session === session) this.session = null; this.emit('failed', { cause: d && d.cause }) })
    session.on('peerconnection', (ev) => this._bindPeerConnection(session, ev.peerconnection))
    // JsSIP may create and announce the peer connection synchronously inside ua.call(), before
    // the caller can attach the listener above. Bind the already-created object too.
    this._bindPeerConnection(session, session.connection)
  }

  _bindPeerConnection(session, pc) {
    if (!pc || pc.__mddAudioBound) return
    pc.__mddAudioBound = true
      // ontrack fires as the remote audio track arrives. te.streams[0] is the usual source,
      // but some stacks deliver a track with no stream — fall back to wrapping the track.
      pc.ontrack = (te) => {
        const stream = (te.streams && te.streams[0]) || new MediaStream([te.track])
        this.attachRemote(stream, session)
      }
      // Belt-and-suspenders: if a remote track is already present (ontrack raced/missed),
      // build a stream from the receivers so audio still renders.
      const grab = () => {
        try {
          const tracks = pc.getReceivers().map((r) => r.track).filter((t) => t && t.kind === 'audio')
          if (tracks.length) this.attachRemote(new MediaStream(tracks), session)
        } catch {}
      }
      pc.addEventListener && pc.addEventListener('connectionstatechange', () => {
        if (pc.connectionState === 'connected') grab()
      })
  }

  // Most reliable remote-audio path: once the call is accepted/confirmed, read the remote
  // audio track straight off the session's RTCPeerConnection receivers and play it. This does
  // not depend on the 'peerconnection'/ontrack event having fired in time (the observed
  // failure was hasStream:false — ontrack never attached), and the server has confirmed RTP
  // is flowing, so a receiver track is present here.
  attachFromSession(session) {
    try {
      const pc = session && session.connection
      if (!pc) return
      const tracks = pc.getReceivers().map((r) => r.track).filter((t) => t && t.kind === 'audio')
      if (tracks.length) this.attachRemote(new MediaStream(tracks), session)
    } catch {}
  }

  _callOptions(withAdmission = false, admissionToken = '') {
    const options = {
      mediaConstraints: { audio: true, video: false },
      pcConfig: { rtcpMuxPolicy: 'require', iceServers: this._iceServers },
    }
    if (withAdmission && admissionToken)
      options.extraHeaders = [`X-MDD-Media-Token: ${admissionToken}`]
    return options
  }

  _runMediaCanary(attempt, admissionToken) {
    return new Promise((resolve, reject) => {
      if (!this.ua || this._dead || attempt !== this._callAttempt) {
        reject(new Error('Softphone is no longer available'))
        return
      }
      const domain = this.ua.configuration.uri.host
      let settled = false
      let proving = false
      let timer = null
      const finish = (error) => {
        if (settled) return
        settled = true
        clearTimeout(timer)
        if (this.session === session) this.session = null
        if (session) { try { session.terminate() } catch {} }
        if (error) reject(error)
        else resolve()
      }
      let session
      try {
        this._creatingCanary = true
        session = this.ua.call(
          `sip:${this._mediaTestTarget}@${domain}`,
          this._callOptions(true, admissionToken))
        session.__mddMediaCanary = true
        session.__mddPlaybackReady = false
        this.session = session
      } catch (error) {
        finish(error)
        return
      } finally {
        this._creatingCanary = false
      }
      session.on('peerconnection', (event) =>
        this._bindPeerConnection(session, event.peerconnection))
      this._bindPeerConnection(session, session.connection)
      const prove = () => {
        if (proving) return
        proving = true
        this.attachFromSession(session)
        this.waitForBidirectionalMedia(6000, session).then(async (evidence) => {
          if (attempt !== this._callAttempt || this.session !== session)
            throw new Error('media canary was cancelled')
          await api.submitSoftphoneMediaEvidence(this._instanceId, admissionToken, evidence)
          for (let index = 0; index < 20; index += 1) {
            if (attempt !== this._callAttempt || this.session !== session)
              throw new Error('media canary was cancelled')
            const status = await api.softphoneMediaAdmission(this._instanceId, admissionToken)
            if (status.ready) { finish(); return }
            await new Promise((resolve) => setTimeout(resolve, 250))
          }
          throw new Error('server did not confirm the local media canary')
        }).catch(finish)
      }
      session.on('accepted', prove)
      session.on('confirmed', () => this.attachFromSession(session))
      session.on('failed', (detail) => finish(new Error(detail?.cause || 'media canary failed')))
      session.on('ended', () => {
        if (!settled) finish(new Error('media canary ended before media was proven'))
      })
      timer = setTimeout(() => finish(new Error('media canary timed out')), 14000)
    })
  }

  call(number) {
    if (!this.ua) return
    if (!window.isSecureContext && location.hostname !== 'localhost' && location.hostname !== '127.0.0.1') {
      this.emit('failed', { cause: 'WebRTC calls require HTTPS for microphone access. Please use HTTPS.' })
      return
    }
    if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
      this.emit('failed', { cause: 'Microphone access is not supported or was blocked.' })
      return
    }
    const attempt = ++this._callAttempt
    let admissionToken = ''
    this.emit('mediacheck', { to: number })
    api.issueSoftphoneMediaAdmission(this._instanceId).then((admission) => {
      if (!this.ua || this._dead || attempt !== this._callAttempt) return
      admissionToken = admission.token || ''
      if (!admissionToken) throw new Error('server did not issue a media admission')
      return this._runMediaCanary(attempt, admissionToken)
    }).then(() => {
      if (!this.ua || this._dead || attempt !== this._callAttempt) return
      const domain = this.ua.configuration.uri.host
      this.emit('calling', { to: number })
      try {
        this.session = this.ua.call(
          `sip:${number}@${domain}`, this._callOptions(true, admissionToken))
        this.handleSession({ session: this.session })
      } catch (err) {
        this.session = null
        this.emit('failed', { cause: (err && err.message) || 'Call failed' })
      }
    }).catch((error) => {
      if (attempt === this._callAttempt)
        this.emit('failed', { cause: `WebRTC media self-test failed: ${error?.message || error}` })
    })
  }

  // Run the exact same local Echo + bidirectional RTP proof used before a paid carrier call,
  // but stop after the proof.  This is the page's no-charge diagnostic/confirmation action.
  verifyMedia() {
    if (!this.ua || this._dead || this.session)
      return Promise.reject(new Error('Softphone must be registered and idle'))
    const attempt = ++this._callAttempt
    return api.issueSoftphoneMediaAdmission(this._instanceId).then((admission) => {
      const token = admission?.token || ''
      if (!token) throw new Error('server did not issue a media admission')
      return this._runMediaCanary(attempt, token)
    }).then(() => {
      if (!this.ua || this._dead || attempt !== this._callAttempt)
        throw new Error('media test was cancelled')
      return true
    })
  }

  answer() {
    if (this.session) {
      if (!window.isSecureContext && location.hostname !== 'localhost' && location.hostname !== '127.0.0.1') {
        this.emit('failed', { cause: 'WebRTC calls require HTTPS for microphone access. Please use HTTPS.' })
        return
      }
      this.session.answer(this._callOptions())
    }
  }

  hangup() {
    this._callAttempt += 1
    const s = this.session
    if (s) {
      this.session = null
      try { s.terminate() } catch {}
    }
  }

  // Reject an un-answered INCOMING call. JsSIP's bare terminate() on a ringing incoming
  // session sends 480 Temporarily Unavailable, which Asterisk's Dial maps to NOANSWER →
  // the call is logged as "missed". Sending 603 Decline makes the disposition "rejected"
  // (declined) as the user intended. Falls back to hangup() for an outgoing/active session.
  reject() {
    const s = this.session
    if (!s) return
    if (s.direction === 'incoming' && !s.isEstablished?.()) {
      this.session = null
      try { s.terminate({ status_code: 603, reason_phrase: 'Decline' }) } catch { try { s.terminate() } catch {} }
    } else {
      this.hangup()
    }
  }

  sendDTMF(tone) { if (this.session) try { this.session.sendDTMF(tone) } catch {} }

  setMuted(muted) {
    if (!this.session) return
    try { muted ? this.session.mute({ audio: true }) : this.session.unmute({ audio: true }) } catch {}
  }

  // ---- call recording: mix local mic + remote audio and record to a downloadable blob ----
  async startRecording() {
    if (!this.session || !this.session.connection || this._rec) return false
    const pc = this.session.connection
    const Ctx = window.AudioContext || window.webkitAudioContext
    const ctx = new Ctx()
    const dest = ctx.createMediaStreamDestination()
    const local = pc.getSenders().map((s) => s.track).filter((t) => t && t.kind === 'audio')
    const remote = pc.getReceivers().map((r) => r.track).filter((t) => t && t.kind === 'audio')
    if (local.length) try { ctx.createMediaStreamSource(new MediaStream(local)).connect(dest) } catch {}
    if (remote.length) try { ctx.createMediaStreamSource(new MediaStream(remote)).connect(dest) } catch {}
    this._recChunks = []
    try {
      this._rec = new MediaRecorder(dest.stream)
    } catch { try { ctx.close() } catch {}; return false }
    this._recCtx = ctx
    this._rec.ondataavailable = (ev) => { if (ev.data && ev.data.size) this._recChunks.push(ev.data) }
    this._rec.start()
    return true
  }

  stopRecording() {
    return new Promise((resolve) => {
      if (!this._rec) { resolve(null); return }
      this._rec.onstop = () => {
        const blob = new Blob(this._recChunks, { type: this._rec ? this._rec.mimeType : 'audio/webm' })
        try { this._recCtx.close() } catch {}
        this._rec = null; this._recCtx = null; this._recChunks = []
        resolve(blob)
      }
      try { this._rec.stop() } catch { resolve(null) }
    })
  }

  get recording() { return !!this._rec }

  stop() {
    // Mark dead FIRST so any late JsSIP event from ua.stop() (async 'disconnected'/'unregistered')
    // is swallowed by emit() and cannot clobber a newly-started line's state.
    this._dead = true
    this.hangup()
    if (this._rec) { try { this._rec.stop() } catch {}; this._rec = null }
    if (this.ua) { try { this.ua.stop() } catch {} this.ua = null }
    if (this.remoteAudio) {
      try {
        if (this._ownsRemoteAudio) {
          this.remoteAudio.srcObject = null
          this.remoteAudio.remove()
        } else if (this._attachedRemoteStream && this.remoteAudio.srcObject === this._attachedRemoteStream) {
          this.remoteAudio.srcObject = null
        }
      } catch {}
      this.remoteAudio = null
    }
    this._attachedRemoteStream = null
    this._ownsRemoteAudio = false
  }
}
