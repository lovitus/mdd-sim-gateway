class MddCallDuplexProcessor extends AudioWorkletProcessor {
  constructor() {
    super()
    this.queue=[];this.offset=0;this.maxFrames=25;this.rebufferFrames=5;this.buffering=true
    this.phase=1;this.sample=0;this.captureCallbacks=0;this.playbackCallbacks=0;this.playedFrames=0;this.tick=0
    this.port.onmessage=event=>{
      if(event.data?.type==="configure"){
        const max=event.data.maxFrames,rebuffer=event.data.rebufferFrames
        if(Number.isInteger(max)&&max>=5&&max<=100&&Number.isInteger(rebuffer)&&rebuffer>=3&&rebuffer<=Math.min(10,max)){
          this.maxFrames=max;this.rebufferFrames=rebuffer;this.buffering=true
        }
      }
      if(event.data?.type==="play"&&event.data.samples instanceof Float32Array&&event.data.samples.length===160)this.queue.push(event.data.samples)
      if(this.queue.length>this.maxFrames){this.queue.splice(0,this.queue.length-this.maxFrames);this.offset=0}
    }
  }
  next(){
    this.consumed=false
    if(this.buffering){if(this.queue.length<this.rebufferFrames)return 0;this.buffering=false}
    while(this.queue.length){const frame=this.queue[0];if(this.offset<frame.length){this.consumed=true;return frame[this.offset++]}
      this.queue.shift();this.offset=0;this.playedFrames++}
    this.buffering=true;return 0
  }
  process(inputs,outputs){
    const input=inputs[0]?.[0]
    if(input?.length){const copy=new Float32Array(input);this.port.postMessage({type:"capture",samples:copy},[copy.buffer]);this.captureCallbacks++}
    const output=outputs[0]?.[0];let consumed=false
    if(output){for(let i=0;i<output.length;i++){this.phase+=8000/sampleRate;if(this.phase>=1){this.phase-=1;this.sample=this.next();if(this.consumed)consumed=true}output[i]=this.sample}if(consumed)this.playbackCallbacks++}
    if(++this.tick>=10){this.tick=0;this.port.postMessage({type:"stats",capture_callbacks:this.captureCallbacks,playback_callbacks:this.playbackCallbacks,played_frames:this.playedFrames})}
    return true
  }
}
registerProcessor("mdd-pcm-duplex",MddCallDuplexProcessor)
