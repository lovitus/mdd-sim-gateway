class MddPcmDuplexProcessor extends AudioWorkletProcessor {
  constructor() {
    super()
    this.playQueue = []
    this.playOffset = 0
    this.playPhase = 1
    this.playSample = 0
    this.captureCallbacks = 0
    this.playbackCallbacks = 0
    this.playedFrames = 0
    this.statTick = 0
    this.port.onmessage = event => {
      if (event.data?.type === 'play' && event.data.samples instanceof Float32Array &&
          event.data.samples.length === 160) this.playQueue.push(event.data.samples)
      if (this.playQueue.length > 6) this.playQueue.splice(0, this.playQueue.length - 6)
    }
  }

  _nextPlaybackSample() {
    while (this.playQueue.length) {
      const frame = this.playQueue[0]
      if (this.playOffset < frame.length) return frame[this.playOffset++]
      this.playQueue.shift()
      this.playOffset = 0
      this.playedFrames += 1
    }
    return 0
  }

  process(inputs, outputs) {
    const input = inputs[0]?.[0]
    if (input?.length) {
      const copy = new Float32Array(input)
      this.port.postMessage({ type: 'capture', samples: copy }, [copy.buffer])
      this.captureCallbacks += 1
    }
    const output = outputs[0]?.[0]
    let consumed = false
    if (output) {
      for (let index = 0; index < output.length; index += 1) {
        this.playPhase += 8000 / sampleRate
        if (this.playPhase >= 1) {
          this.playPhase -= 1
          if (this.playQueue.length) consumed = true
          this.playSample = this._nextPlaybackSample()
        }
        output[index] = this.playSample
      }
      if (consumed) this.playbackCallbacks += 1
    }
    this.statTick += 1
    if (this.statTick >= 10) {
      this.statTick = 0
      this.port.postMessage({
        type: 'stats', capture_callbacks: this.captureCallbacks,
        playback_callbacks: this.playbackCallbacks, played_frames: this.playedFrames,
      })
    }
    return true
  }
}

registerProcessor('mdd-pcm-duplex', MddPcmDuplexProcessor)
