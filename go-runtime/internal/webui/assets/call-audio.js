const FRAME_SAMPLES = 160
const FRAME_BYTES = 320

class Downsampler {
  constructor(inputRate, outputRate = 8000) {
    this.ratio = inputRate / outputRate
    this.buffer = new Float32Array(0)
    this.position = 0
    this.packet = []
  }

  push(samples) {
    const joined = new Float32Array(this.buffer.length + samples.length)
    joined.set(this.buffer)
    joined.set(samples, this.buffer.length)
    this.buffer = joined
    const frames = []
    while (this.position + 1 < this.buffer.length) {
      const left = Math.floor(this.position)
      const fraction = this.position - left
      this.packet.push(this.buffer[left] * (1 - fraction) + this.buffer[left + 1] * fraction)
      this.position += this.ratio
      if (this.packet.length === FRAME_SAMPLES) {
        const pcm = new ArrayBuffer(FRAME_BYTES)
        const view = new DataView(pcm)
        this.packet.forEach((sample, index) => view.setInt16(index * 2,
          Math.max(-32768, Math.min(32767, Math.round(sample * 32767))), true))
        frames.push(pcm)
        this.packet = []
      }
    }
    const drop = Math.min(Math.floor(this.position), Math.max(0, this.buffer.length - 1))
    if (drop) {
      this.buffer = this.buffer.slice(drop)
      this.position -= drop
    }
    return frames
  }
}

function rebufferFrames(ms) {
  const maxFrames = Math.ceil(ms / 20)
  return Math.min(maxFrames, 10, Math.max(3, Math.ceil(ms / 100)))
}

function playFrame(node, frame) {
  if (!(frame instanceof ArrayBuffer) || frame.byteLength !== FRAME_BYTES)
    throw new Error("服务端返回了无效的 PCM 帧")
  const view = new DataView(frame)
  const samples = new Float32Array(FRAME_SAMPLES)
  for (let index = 0; index < samples.length; index++)
    samples[index] = view.getInt16(index * 2, true) / 32768
  node.port.postMessage({type:"play", samples}, [samples.buffer])
}

export function normalizeDialTarget(value) {
  const compact = String(value || "").trim().replace(/[\s().-]/g, "")
  const normalized = compact.startsWith("00") ? `+${compact.slice(2)}` : compact
  if (/^\+[1-9][0-9]{5,14}$/.test(normalized) || /^[0-9]{2,6}$/.test(normalized)) return normalized
  throw new Error("号码必须是国际 E.164 格式（如 +15550100123）或 2–6 位短号")
}

export class CallMedia {
  constructor(bufferMS, onEvent = () => {}) {
    if (!Number.isInteger(bufferMS) || bufferMS < 100 || bufferMS > 2000)
      throw new RangeError("音频排队上限必须是 100–2000 ms 的整数")
    this.bufferMS = bufferMS
    this.onEvent = onEvent
    this.socket = null
    this.context = null
    this.stream = null
    this.source = null
    this.node = null
    this.started = false
    this.phase = "opening"
    this.muted = false
    this.challenge = ""
    this.resumeTicket = ""
    this.connectionEpoch = 0
    this.stats = {capture_callbacks:0, playback_callbacks:0, played_frames:0}
    this.evidenceTimer = null
    this.readyTimer = null
    this.reconnectTimer = null
    this.reconnectDeadline = 0
    this.closed = false
    this.readyResolve = null
    this.readyReject = null
  }

  openAudioFromGesture() {
    if (!globalThis.isSecureContext || !navigator.mediaDevices?.getUserMedia || !globalThis.AudioWorkletNode)
      throw new Error("浏览器必须通过 HTTPS 并支持麦克风 AudioWorklet")
    const Context = globalThis.AudioContext || globalThis.webkitAudioContext
    if (!Context) throw new Error("浏览器不支持 Web Audio")
    this.context = new Context()
    const resume = this.context.resume()
    const microphone = navigator.mediaDevices.getUserMedia({video:false,audio:{channelCount:1,echoCancellation:true,noiseSuppression:true,autoGainControl:true}})
    this.audioPromise = Promise.all([resume, microphone]).then(([, stream]) => stream)
    return this.audioPromise
  }

  async prepare(lease, ticket) {
    this.lease = lease
    this.ticket = ticket
    this.stream = await this.audioPromise
    await this.context.audioWorklet.addModule("/assets/call-worklet.js")
    this.source = this.context.createMediaStreamSource(this.stream)
    this.node = new AudioWorkletNode(this.context, "mdd-pcm-duplex", {numberOfInputs:1,numberOfOutputs:1,outputChannelCount:[1]})
    const maxFrames = Math.ceil(this.bufferMS / 20)
    this.node.port.postMessage({type:"configure",maxFrames,rebufferFrames:rebufferFrames(this.bufferMS)})
    this.source.connect(this.node)
    this.node.connect(this.context.destination)
    const downsampler = new Downsampler(this.context.sampleRate)
    const limitBytes = maxFrames * FRAME_BYTES
    const silence = new ArrayBuffer(FRAME_BYTES)
    this.node.port.onmessage = event => {
      if (event.data?.type === "stats") {
        this.stats = {
          capture_callbacks:Number(event.data.capture_callbacks || 0),
          playback_callbacks:Number(event.data.playback_callbacks || 0),
          played_frames:Number(event.data.played_frames || 0),
        }
        return
      }
      if (event.data?.type !== "capture" || !(event.data.samples instanceof Float32Array)) return
      for (const frame of downsampler.push(event.data.samples)) {
        if (this.started && this.socket?.readyState === WebSocket.OPEN && this.socket.bufferedAmount + FRAME_BYTES <= limitBytes)
          this.socket.send(this.muted ? silence.slice(0) : frame)
      }
    }
    this.node.onprocessorerror = () => this.fail(new Error("浏览器音频处理器已停止"))
    this.evidenceTimer = setInterval(() => this.sendEvidence(), 250)
    const ready = new Promise((resolve, reject) => { this.readyResolve = resolve; this.readyReject = reject })
    this.readyTimer = setTimeout(() => this.fail(new Error("20 秒内未取得双向音频证据；请确认麦克风有声音且扬声器可播放")), 20000)
    this.openSocket(false)
    return ready
  }

  openSocket(resume) {
    const scheme = location.protocol === "https:" ? "wss" : "ws"
    const socket = new WebSocket(`${scheme}://${location.host}${this.lease.ws_path}`)
    this.socket = socket
    this.started = false
    socket.binaryType = "arraybuffer"
    socket.onopen = () => {
      if (this.socket !== socket || this.closed) return
      socket.send(JSON.stringify(resume ? {
        type:"browser.media.resume",version:1,session_id:this.lease.session_id,
        resume_ticket:this.resumeTicket,connection_epoch:this.connectionEpoch,
      } : {type:"browser.media.hello",version:1,session_id:this.lease.session_id,ticket:this.ticket}))
    }
    socket.onmessage = event => {
      if (this.socket !== socket || this.closed) return
      if (event.data instanceof ArrayBuffer) {
        try { playFrame(this.node, event.data) } catch (error) { this.fail(error) }
        return
      }
      let message
      try { message = JSON.parse(event.data) } catch { this.fail(new Error("媒体控制消息不是 JSON")); return }
      if (message.type === "browser.media.claimed" || message.type === "browser.media.resumed") {
        this.challenge = String(message.challenge || "")
        this.resumeTicket = String(message.resume_ticket || "")
        this.connectionEpoch = Number(message.connection_epoch || 0)
        if (!this.challenge || !this.resumeTicket || !Number.isSafeInteger(this.connectionEpoch)) {
          this.fail(new Error("媒体恢复身份不完整")); return
        }
		if (message.type === "browser.media.resumed") this.reconnectDeadline = 0
		clearTimeout(this.reconnectTimer)
        this.sendEvidence()
      } else if (message.type === "browser.media.started") {
        const expected = resume || this.phase === "active" ? "call" : "canary"
        if (message.purpose !== expected) { this.fail(new Error("媒体会话用途发生变化")); return }
        this.started = true
        this.onEvent(resume ? "reconnected" : "media-started")
      } else if (message.type === "browser.media.ready" || (message.type === "browser.media.status" && message.ready === true)) {
        if (this.phase === "opening") {
          this.phase = "ready"
          clearTimeout(this.readyTimer)
          this.readyResolve?.()
          this.readyResolve = this.readyReject = null
        }
      }
    }
    socket.onerror = () => {}
    socket.onclose = event => {
      if (this.socket !== socket || this.closed) return
      this.socket = null
      this.started = false
      if (this.phase === "active" && event.code === 1000) {
        this.onEvent("ended", event.reason || "Provider 已结束通话")
        this.close()
        return
      }
      if (this.phase === "active" && this.resumeTicket) {
        if (!this.reconnectDeadline) this.reconnectDeadline = Date.now() + 9000
        if (Date.now() < this.reconnectDeadline) {
          this.onEvent("reconnecting", `媒体 WSS 已断开 (${event.code})，正在恢复同一通话`)
          clearTimeout(this.reconnectTimer)
          this.reconnectTimer = setTimeout(() => this.openSocket(true), 250)
          return
        }
      }
      this.fail(new Error(`媒体 WSS 已关闭 (${event.code})`))
    }
  }

  sendEvidence() {
    if (!this.challenge || this.socket?.readyState !== WebSocket.OPEN) return
    this.socket.send(JSON.stringify({type:"browser.media.evidence",version:1,challenge:this.challenge,...this.stats}))
  }

  markActive() {
    this.phase = "active"
    this.reconnectDeadline = 0
    this.onEvent("active")
  }

  setMuted(value) { this.muted = Boolean(value) }

  fail(error) {
    if (this.closed) return
    clearTimeout(this.readyTimer)
    const reject = this.readyReject
    this.readyResolve = this.readyReject = null
    reject?.(error)
    this.onEvent("failed", error.message || String(error))
    if (this.phase !== "active") this.close()
  }

  close() {
    if (this.closed) return
    this.closed = true
    clearInterval(this.evidenceTimer)
    clearTimeout(this.readyTimer)
    clearTimeout(this.reconnectTimer)
    try { this.socket?.close(1000, "browser call closed") } catch {}
    try { this.source?.disconnect() } catch {}
    try { this.node?.disconnect() } catch {}
    for (const track of this.stream?.getTracks?.() || []) try { track.stop() } catch {}
    if (this.context?.state !== "closed") void this.context?.close?.()
  }
}

export {Downsampler, FRAME_BYTES, FRAME_SAMPLES}
