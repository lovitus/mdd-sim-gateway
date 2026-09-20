// Real Chromium/Web Audio/MediaRecorder on loopback synthetic tones. No modem,
// microphone, carrier, credentials, or external page is used.
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import http from 'node:http'
import { spawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const executable = process.env.MDD_TEST_CHROME || ['google-chrome', 'chromium', 'chromium-browser']
  .flatMap(name => (process.env.PATH || '').split(path.delimiter).map(p => path.join(p, name)))
  .find(p => fs.existsSync(p))
assert.ok(executable, 'A real Chrome/Chromium executable is required for this integration test')
const page = `<!doctype html><meta charset="utf-8"><script type="module">
import {LocalCallRecording} from '/src/callRecording.js';
import {CallMedia} from '/src/goCallMedia.js';
window.runRecordingTest = async () => {
  const check=(x,m)=>{if(!x)throw Error(m)};
  const delay=ms=>new Promise(r=>setTimeout(r,ms));
  const context=new AudioContext(); await context.resume();
  const microphone=context.createOscillator(), playback=context.createOscillator();
  microphone.frequency.value=440; playback.frequency.value=660;
  microphone.start(); playback.start();
  const recordings=[];
  const make=opts=>{const r=new LocalCallRecording({context,microphone,playback,...opts}); recordings.push(r);return r};
  const done=r=>new Promise((resolve,reject)=>{
    const deadline=performance.now()+4000;
    const inspect=()=>{if(r.state==='ready')resolve(r);else if(r.state==='failed'||performance.now()>deadline)reject(Error(r.reason||'recording timeout'));else setTimeout(inspect,10)};inspect();
  });
  const rms=values=>Math.sqrt(values.reduce((n,x)=>n+x*x,0)/values.length);
  const output=[];
  try {
    const denied=make(); let rejected=false; try{denied.start(false)}catch{rejected=true};check(rejected&&!denied.destination,'consent must precede graph allocation');
    for(const muted of [false,true]){
      const r=make({muted});r.start(true);await delay(300);r.stop();await done(r);
      const audio=await context.decodeAudioData(await r.clip.arrayBuffer());
      check(audio.numberOfChannels===2,'stereo channel count');
      const left=rms(audio.getChannelData(0)),right=rms(audio.getChannelData(1));
      check(right>0.05,'remote tone missing');check(muted?left<0.005:left>0.05,'mute/local tone mismatch');
      output.push({muted,leftRMS:left,rightRMS:right,mime:r.clip.type,bytes:r.bytes});r.discard();check(!r.clip,'discard must release clip');
    }
    const limited=make({maxMS:150});limited.start(true);await done(limited);check(limited.reason==='time_limit','automatic time limit');limited.discard();
    const media=new CallMedia(500);Object.assign(media,{context,source:microphone,node:playback,phase:'active',started:true});
    const ended=media.startRecording({consent:true});recordings.push(ended);await delay(300);media.close();await done(ended);
    check(ended.reason==='call_ended','media close must finalize recording');check(ended.bytes>0,'missing final encoded data');
    return {passed:true,tests:output,callCloseFinalized:true,timeLimit:true,consent:true};
  } finally {for(const r of recordings)r.discard();try{microphone.stop();playback.stop()}catch{};if(context.state!=='closed')await context.close()}
};
</script>`
const server = http.createServer((request, response) => {
  if (process.env.MDD_BROWSER_DEBUG) console.error('request', request.url)
  if (request.url === '/') { response.setHeader('Content-Type', 'text/html'); response.end(page); return }
  if (!['/src/callRecording.js', '/src/goCallMedia.js'].includes(request.url)) { response.writeHead(404).end(); return }
  response.setHeader('Content-Type', 'text/javascript')
  response.end(fs.readFileSync(path.join(root, request.url)))
})
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'mdd-recording-browser-'))
const chrome = spawn(executable, ['--headless=new', '--no-sandbox', '--disable-dev-shm-usage',
  '--autoplay-policy=no-user-gesture-required', '--remote-debugging-port=0', '--no-first-run',
  '--no-default-browser-check', `--user-data-dir=${profile}`, 'about:blank'], { stdio: ['ignore', 'ignore', 'pipe'] })
let socket, timeout
try {
  const endpoint = await new Promise((resolve, reject) => {
    let stderr = ''
    timeout = setTimeout(() => reject(new Error(`Chrome startup timed out: ${stderr.slice(-500)}`)), 15000)
    chrome.on('error', reject)
    chrome.stderr.on('data', bytes => { stderr += bytes; const match = stderr.match(/DevTools listening on (ws:\/\/[^\s]+)/); if (match) resolve(match[1]) })
    chrome.once('exit', code => reject(new Error(`Chrome exited early: ${code}`)))
  })
  clearTimeout(timeout)
  const port = new URL(endpoint).port
  const tab = await (await fetch(`http://127.0.0.1:${port}/json/new?http://127.0.0.1:${server.address().port}/`, { method: 'PUT' })).json()
  socket = new WebSocket(tab.webSocketDebuggerUrl)
  await new Promise((resolve, reject) => { socket.onopen = resolve; socket.onerror = reject })
  let sequence = 0
  const pending = new Map()
  let loadPage
  socket.onmessage = message => {
    const data = JSON.parse(message.data)
    if (process.env.MDD_BROWSER_DEBUG) console.error(JSON.stringify(data))
    if (data.method === 'Page.loadEventFired') loadPage?.()
    const handlers = pending.get(data.id)
    if (!handlers) return
    pending.delete(data.id)
    if (data.error || data.result?.exceptionDetails) handlers.reject(new Error(JSON.stringify(data)))
    else handlers.resolve(data.result)
  }
  const send = (method, params = {}) => new Promise((resolve, reject) => {
    const id = ++sequence; pending.set(id, { resolve, reject }); socket.send(JSON.stringify({ id, method, params }))
  })
  const run = async () => {
    await send('Page.enable')
    await send('Runtime.enable')
    const loaded = new Promise(resolve => { loadPage = resolve })
    const navigation = await send('Page.navigate', { url: `http://127.0.0.1:${server.address().port}/` })
    assert.ok(!navigation.errorText, navigation.errorText)
    await loaded
    const data = await send('Runtime.evaluate', {
      expression: 'window.runRecordingTest()', awaitPromise: true, returnByValue: true,
    })
    return data.result.value
  }
  const result = await Promise.race([run(), new Promise((_, reject) => {
    timeout = setTimeout(() => reject(new Error('Real recording test timed out')), 20000)
  })])
  assert.equal(result.passed, true)
  console.log(JSON.stringify(result, null, 2))
} finally {
  clearTimeout(timeout); socket?.close(); chrome.kill('SIGTERM')
  await new Promise(resolve => { if (chrome.exitCode !== null) { resolve(); return }; const timer=setTimeout(()=>{chrome.kill('SIGKILL');resolve()},2000);chrome.once('exit',()=>{clearTimeout(timer);resolve()}) })
  await new Promise(resolve => server.close(resolve)); fs.rmSync(profile, { recursive: true, force: true })
}
