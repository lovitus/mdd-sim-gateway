// One foreground controller; no default ADB target, carrier or production server.
import {execFile, spawn} from 'node:child_process';
import {promisify} from 'node:util';
import {createHash, randomUUID} from 'node:crypto';
import {readFile, writeFile, mkdir, stat, unlink} from 'node:fs/promises';
import {resolve, isAbsolute} from 'node:path';
import {setTimeout as sleep} from 'node:timers/promises';
import assert from 'node:assert/strict';

const options = new Map();
for (let i = 2; i < process.argv.length; i += 2) options.set(process.argv[i], process.argv[i + 1]);
function required(key) { const value = options.get(key); assert(value, `Missing ${key}`); return value; }
const adb = required('--adb'), port = required('--port'), serial = required('--serial');
const fixturePath = required('--fixture'), fixtureHash = required('--fixture-sha256');
const apkHash = required('--apk-sha256'), output = resolve(required('--output'));
const mode = required('--mode');
assert(['cellular', 'vowifi'].includes(mode), 'One explicit transport per bounded invocation');
const suite = options.get('--suite') ?? 'process';
assert(['process', 'control'].includes(suite), 'Unknown recovery suite');
assert(isAbsolute(adb) && isAbsolute(fixturePath), 'Executable paths must be absolute');
assert(/^\d{4,5}$/.test(port) && Number(port) < 65536, 'Explicit ADB server port required');
assert(/^[\w.:[\]-]+$/.test(serial), 'Explicit authorized serial required');
assert(/^[a-f0-9]{64}$/.test(fixtureHash) && /^[a-f0-9]{64}$/.test(apkHash), 'Exact CI artifact hashes required');
const qa = 'com.lovitus.mddagent.preview.qa';
const component = `${qa}/com.lovitus.mddagent.MainActivity`;
const remoteXML = `/data/local/tmp/mdd-process-${randomUUID()}.xml`;
const exec = promisify(execFile), hash = bytes => createHash('sha256').update(bytes).digest('hex');
const quote = text => `'${String(text).replaceAll("'", "'\\''")}'`;
const deadline = Date.now() + 560_000;
let child, bootstrap, reversed = false, ownedQA = false, ownedOutput = false, cleaning = false, step = 'preflight';
let cleanupDeadline;
const report = {fixtureSHA256: fixtureHash, apkSHA256: apkHash, mode, suite,
  controllerSHA256: hash(await readFile(new URL(import.meta.url))), cases: [], passed: false,
  scope: 'External synthetic peer, real App PID death; control suite injects unknown guard and lost receipt faults, not normal guard behavior or carrier/battery acceptance'};
const abort = new AbortController();
const timer = setTimeout(() => abort.abort(), 560_000);
const hardStop = setTimeout(() => { child?.kill('SIGKILL'); process.exit(1); }, 590_000);
process.once('SIGINT', () => abort.abort());
process.once('SIGTERM', () => abort.abort());
async function command(exe, args, {input, binary = false, timeout = 15_000} = {}) {
  const remaining = cleaning ? Math.min(5000, cleanupDeadline - Date.now()) : Math.min(timeout, deadline - Date.now());
  assert(remaining > 0, 'Controller deadline reached');
  try {
    // execFile does not log argv, which may contain synthetic login inputs.
    const running = exec(exe, args, {encoding: binary ? 'buffer' : 'utf8', maxBuffer: 40 * 1024 * 1024,
      timeout: remaining, signal: cleaning ? undefined : abort.signal});
    if (input !== undefined) running.child.stdin.end(input);
    return (await running).stdout;
  } catch { throw new Error(`Command failed at ${step}`); }
}
const device = (...args) => command(adb, ['-P', port, '-s', serial, ...args]);
const shell = async (...args) => {
  if (args[0] === 'input' && args[2] !== 'KEYCODE_HOME') await focused();
  return device('shell', args.map(quote).join(' '));
};
async function focused() {
  // API 35 moved global focus out of the "windows" subsection; the complete
  // WindowManager dump retains the same exact current-window identity on 28+.
  const windows = await device('shell', 'dumpsys window');
  const current = windows.match(/mCurrentFocus=Window\{[^\n]+/g) ?? [];
  assert(current.length === 1 && current[0].includes(` ${qa}/`), `Foreground window is not QA at ${step}`);
}
async function dump() {
  await focused();
  await shell('uiautomator', 'dump', remoteXML);
  const xml = await device('exec-out', 'cat', remoteXML); await focused(); return xml;
}
async function xpath(xml, expression) { return (await command('xmllint', ['--nonet', '--xpath', expression, '-'], {input: xml})).trim(); }
const resource = id => id.includes(':') ? id : `${qa}:id/${id}`;
const node = id => `//node[@resource-id='${resource(id)}']`;
async function bounds(xml, expression) {
  assert.equal(await xpath(xml, `count(${expression})`), '1', `Expected one UI control at ${step}`);
  assert.equal(await xpath(xml, `string(${expression}/@enabled)`), 'true', `Disabled UI control at ${step}`);
  const values = (await xpath(xml, `string(${expression}/@bounds)`)).match(/^\[(\d+),(\d+)\]\[(\d+),(\d+)\]$/);
  assert(values, `Missing UI bounds at ${step}`);
  const [x1, y1, x2, y2] = values.slice(1).map(Number);
  assert(x2 > x1 && y2 > y1, `Invisible control at ${step}`);
  return [Math.floor((x1 + x2) / 2), Math.floor((y1 + y2) / 2)];
}
async function tap(id, xml) {
  report.lastUIControl = id;
  const [x, y] = await bounds(xml ?? await readyControl(id), node(id));
  await shell('input', 'tap', x, y);
  const page = {tab_home:'home',tab_calls:'calls',tab_messages:'messages',tab_readers:'readers',tab_settings:'settings'}[id];
  if (page) await uiReady(async current => await xpath(current, `string(${node('page_title')}/@content-desc)`) === `page:${page}`);
}
async function readyControl(id) {
  const visible = async xml => {
    if (await xpath(xml, `count(${node(id)})`) !== '1' || await xpath(xml, `string(${node(id)}/@enabled)`) !== 'true') return false;
    const rect=(await xpath(xml, `string(${node(id)}/@bounds)`)).match(/^\[(\d+),(\d+)\]\[(\d+),(\d+)\]$/);
    return rect && Number(rect[3])>Number(rect[1]) && Number(rect[4])>Number(rect[2]);
  };
  if (!['sign_in_again','forget_login','diagnostics_open','message_reconcile'].includes(id)) return uiReady(visible);
  // Scroll only the QA page's own ScrollView, never fixed navigation or another app.
  for (let moves=0;moves<=4;moves++) {
    const xml=await dump();if(await visible(xml))return xml;if(moves===4)break;
    const area="//node[@class='android.widget.ScrollView' and @scrollable='true']";
    if(await xpath(xml,`count(${area})`)!=='1')break;
    const rect=(await xpath(xml,`string(${area}/@bounds)`)).match(/^\[(\d+),(\d+)\]\[(\d+),(\d+)\]$/);assert(rect,'QA scroll region unavailable');
    const [left,top,right,bottom]=rect.slice(1).map(Number),x=Math.floor((left+right)/2),height=bottom-top;
    await shell('input','swipe',x,Math.floor(top+height*0.8),x,Math.floor(top+height*0.25),300);
  }
  throw new Error(`QA control is not reachable: ${id}`);
}
async function uiReady(check) {
  // UI transitions only, inside this single bounded controller. Never re-click
  // a submission because its response is late, and never poll a remote service.
  for (const delay of [0, 500, 1500, 3000]) {
    if (delay) await sleep(delay, undefined, {signal: abort.signal});
    const xml = await dump(); if (await check(xml)) return xml;
  }
  throw new Error(`UI transition did not complete at ${step}`);
}
async function confirmAction(mode, kind) {
  const xml = await uiReady(async current => {
    const message = await xpath(current, "string(//node[@resource-id='android:id/message']/@text)");
    const intent = kind === 'call' ? /Place a carrier call|使用此 SIM 和线路拨号/ : /Send this message once|仅发送一次/;
    const route = mode === 'cellular' ? /Cellular|蜂窝/ : /VoWiFi/;
    return intent.test(message) && message.includes('+15550100999') && message.includes('External process fixture') && route.test(message);
  });
  await tap('android:id/button1', xml);
}
async function fill(id, value) {
  assert(/^[a-zA-Z0-9:/.+\-]+$/.test(value), 'Fixture input must be shell-safe ASCII');
  report.lastUIControl = id;
  const xml = await readyControl(id);
  const length = (await xpath(xml, `string(${node(id)}/@text)`)).length;
  await tap(id, xml);
  await shell('input', 'keyevent', 'KEYCODE_MOVE_END');
  if (length) await shell('input', 'keyevent', ...Array(Math.min(length, 256)).fill('KEYCODE_DEL'));
  await shell('input', 'text', value);
  await shell('input', 'keyevent', 'KEYCODE_BACK');
}
async function control(path, value) {
  const response = await fetch(`http://127.0.0.1:${bootstrap.control_port}${path}`, {
    method: value ? 'POST' : 'GET', headers: {Authorization: `Bearer ${bootstrap.control_token}`, 'Content-Type': 'application/json'},
    body: value ? JSON.stringify(value) : undefined,
    signal: AbortSignal.any([abort.signal, AbortSignal.timeout(47_000)])});
  report.lastPeerHTTP = {path, status: response.status, observedAt: new Date().toISOString()};
  const result = await response.json();
  if (result) report.lastPeerState = {path, observedAt: new Date().toISOString(), ...result};
  assert(response.ok, `Fixture event deadline or rejection at ${step}`);
  assert.equal(result.failure, '', `Fixture contract failure: ${result.failure}`); return result;
}
const waitEvent = (event, count = 1) => control('/wait', {event, count});
async function capture(name) {
  await writeFile(`${output}/${name}.xml`, await dump(), {mode: 0o600});
  const png = await command(adb, ['-P', port, '-s', serial, 'exec-out', 'screencap', '-p'], {binary: true});
  await writeFile(`${output}/${name}.png`, png, {mode: 0o600});
}
async function startFixture() {
  child = spawn(fixturePath, [], {stdio: ['ignore', 'pipe', 'pipe']});
  // Never pipe bootstrap stdout (credentials) or raw request logs to the console.
  child.stderr.resume();
  return new Promise((accept, reject) => {
    let text = ''; const timeout = setTimeout(() => reject(new Error('Fixture startup timed out')), 5000);
    child.once('error', () => { clearTimeout(timeout); reject(new Error('Fixture failed to start')); });
    child.once('exit', () => { clearTimeout(timeout); reject(new Error('Fixture exited before startup')); });
    child.stdout.on('data', bytes => {
      text += bytes.toString(); if (text.length > 8192) { clearTimeout(timeout); reject(new Error('Invalid fixture bootstrap')); return; }
      if (!text.includes('\n')) return;
      clearTimeout(timeout);
      try { const value = JSON.parse(text.trim()); child.stdout.removeAllListeners('data'); child.stdout.resume(); accept(value); }
      catch { reject(new Error('Invalid fixture bootstrap')); }
    });
  });
}
// NEW_TASK | CLEAR_TASK: "am" has no --activity-new-task long option.
async function launch(cold = false) { await shell('am', 'start', '-W', '-n', component, ...(cold ? ['-f', '0x10008000'] : [])); }
async function stopFixture() {
  if (child && child.exitCode === null && child.signalCode === null) {
    child.kill('SIGTERM');
    await Promise.race([new Promise(done => child.once('exit', done)), sleep(1500)]);
    if (child.exitCode === null && child.signalCode === null) {
      child.kill('SIGKILL');
      await Promise.race([new Promise(done => child.once('exit', done)), sleep(1000)]);
      assert(child.exitCode !== null || child.signalCode !== null, 'Fixture cleanup not confirmed');
    }
  }
  if (reversed) { await device('reverse', '--remove', `tcp:${bootstrap.data_port}`); reversed = false; }
}
async function resetAndLogin(mode, scenario) {
  step = `${mode}/${scenario}/login`;
  await stopFixture();
  assert.equal((await shell('pm', 'clear', qa)).trim(), 'Success');
  bootstrap = await startFixture();
  assert(Number.isInteger(bootstrap.data_port) && Number.isInteger(bootstrap.control_port) && bootstrap.data_port !== bootstrap.control_port);
  assert(/^[a-f0-9]{64}$/.test(bootstrap.pin) && /^[a-f0-9]{64}$/.test(bootstrap.control_token));
  await device('reverse', '--no-rebind', `tcp:${bootstrap.data_port}`, `tcp:${bootstrap.data_port}`); reversed = true;
  await control('/case', {mode, scenario});
  await shell('pm', 'grant', qa, 'android.permission.RECORD_AUDIO');
  if (report.api >= 33) await shell('pm', 'grant', qa, 'android.permission.POST_NOTIFICATIONS');
  await launch();
  await fill('gateway_server', `https://127.0.0.1:${bootstrap.data_port}`);
  await fill('gateway_username', bootstrap.username);
  await fill('gateway_password', bootstrap.password);
  await tap('connect_gateway');
  const trust = await uiReady(async xml => await xpath(xml, `count(//node[contains(@text,'${bootstrap.pin}') and contains(@text,'https://127.0.0.1:${bootstrap.data_port}')])`) === '1');
  await tap('android:id/button1', trust);
  await waitEvent('observers');
  await uiReady(async xml => await xpath(xml, `count(${node('home_availability_toggle')})`) === '1');
  const state = await control('/state'); assert.equal(state.counts.logins, 1);
}
async function identity() {
  const pid = (await shell('pidof', '-s', qa)).trim(); assert(/^\d+$/.test(pid), 'Expected one QA PID');
  const name = await shell('run-as', qa, 'cat', `/proc/${pid}/cmdline`); assert.equal(name.replace(/\0+$/, ''), qa, 'PID must belong to QA');
  const stat = (await shell('run-as', qa, 'cat', `/proc/${pid}/stat`)).trim();
  const start = stat.slice(stat.lastIndexOf(')') + 2).split(/\s+/)[19]; assert(/^\d+$/.test(start), 'Missing PID birth identity');
  const uid = (await shell('run-as', qa, 'id', '-u')).trim(); assert(/^\d+$/.test(uid));
  const status = await shell('run-as', qa, 'cat', `/proc/${pid}/status`);
  assert.equal(status.match(/^Uid:\s+(\d+)/m)?.[1], uid, 'QA UID must own PID');
  return {pid, start, uid};
}
async function killAndRecover(scenario) {
  // No screenshot/dump here: canary preparation times out after 20s. Inject
  // immediately after the external wire event, not after a polling sleep.
  await shell('input', 'keyevent', 'KEYCODE_HOME');
  const original = await identity();
  const again = await identity(); assert.deepEqual(again, original, 'PID changed before fault injection');
  const before = await control('/state');
  if (scenario !== 'sms') {
    assert.equal(before.counts.deletes ?? 0, 0, 'Preparation must still be live at kill');
    if (scenario === 'preparing' || scenario === 'submitted') assert(before.canary_age_ms >= 0 && before.canary_age_ms < 8000, 'Fault injection missed the canary window');
    assert.equal(before.counts.starts ?? 0, scenario === 'preparing' ? 0 : 1);
    if (scenario === 'ending') {
      assert.equal(before.counts.end_requests, 1); assert.equal(before.counts.ends, 1);
      assert.equal(before.end_receipt_held, true); assert(before.counts.status_unavailable >= 1);
    }
  }
  // run-as restricts the signal to QA's UID even in the PID-reuse race window.
  await command(adb, ['-P', port, '-s', serial, 'shell', ['run-as',qa,'kill','-9',original.pid].map(quote).join(' ')], {timeout: 2500});
  if (scenario === 'submitted') await control('/terminal', {});
  await sleep(30_000, undefined, {signal: abort.signal});
  await waitEvent('observers', before.counts.observers + 1);
  const restored = await identity(); assert.notEqual(restored.pid, original.pid, 'Actual new App PID required');
  assert.equal(restored.uid, original.uid, 'App UID changed');
  // Do not launch Activity until START_STICKY recovery has been observed.
  return {pidChanged: true, uidPreserved: true};
}
async function callCase(mode, scenario) {
  await resetAndLogin(mode, scenario); step = `${mode}/${scenario}/submit`;
  await tap('tab_calls'); await tap(`route_${mode}`);
  await fill('dial_number', '+15550100999'); await tap('call_dial'); await confirmAction(mode, 'call');
  await waitEvent(scenario === 'preparing' ? 'canary' : 'starts');
  step = `${mode}/${scenario}/process-death`;
  const processEvidence = await killAndRecover(scenario);
  await waitEvent('deletes');
  let state = await control('/state');
  assert.equal(state.counts.leases, 1); assert.equal(state.counts.media, 1);
  assert.equal(state.counts.starts ?? 0, scenario === 'preparing' ? 0 : 1);
  assert.equal(state.counts.start_requests ?? 0, scenario === 'preparing' ? 0 : 1);
  assert.equal(state.counts.ends ?? 0, 0); assert.equal(state.counts.receipts ?? 0, 0);
  assert.equal(state.counts.deletes, 1);
  if (scenario === 'submitted') assert(state.counts.statuses >= 1);
  const pcm = state.counts.pcm;
  await launch(); await tap('tab_calls');
  await uiReady(async xml => await xpath(xml, `count(${node('call_reconcile')} | ${node('call_hangup')})`) === '0' && await xpath(xml, `string(${node('call_dial')}/@enabled)`) === 'true');
  await capture(`${mode}-${scenario}`);
  state = await control('/state'); assert.equal(state.counts.pcm, pcm, 'No PCM after process recovery');
  assert.equal(state.counts.media, 1); assert.equal(state.counts.leases, 1);
  report.cases.push({mode, scenario, ...processEvidence, state, newNetworkMedia: false,
    microphoneClaim: 'Not independently verified by system recording instrumentation; no new network media'});
}
async function scrollJournal() {
  const xml = await dump(); const [width, height] = (await shell('wm', 'size')).match(/(?:Physical|Override) size: (\d+)x(\d+)/).slice(1).map(Number);
  // A page navigation gesture, not service-state polling. Keep the fixed footer untouched.
  if (await xpath(xml, `count(${node('message_reconcile')})`) !== '1') {
    await shell('input', 'swipe', Math.floor(width/2), Math.floor(height*0.75), Math.floor(width/2), Math.floor(height*0.28), 300);
  }
  return dump();
}
async function smsRows(xml) {
  const expression = "//node[starts-with(@content-desc,'sms-operation:')]";
  const count = Number(await xpath(xml, `count(${expression})`)); const rows = [];
  for (let i = 1; i <= count; i++) rows.push(await xpath(xml, `string((${expression})[${i}]/@content-desc)`));
  return rows.sort();
}
async function smsCase(mode) {
  await resetAndLogin(mode, 'sms'); step = `${mode}/sms/submit`;
  await tap('tab_messages'); await tap(`route_${mode}`); await fill('dial_number', '+15550100999');
  for (const [index, text] of ['fixture-A', 'fixture-B'].entries()) {
    await fill('message_body', text);
    await uiReady(async xml => await xpath(xml, `string(${node('message_body')}/@text)`) === text && await xpath(xml, `string(${node('message_send')}/@enabled)`) === 'true');
    await tap('message_send'); await confirmAction(mode, 'sms'); await waitEvent('sends', index+1);
    await uiReady(async xml => await xpath(xml, `string(${node('message_send')}/@enabled)`) === 'true');
  }
  const before = await smsRows(await scrollJournal());
  assert.equal(before.length, 2); assert.equal(before.filter(r => r.endsWith(':submitted')).length, 1);
  assert.equal(before.filter(r => r.endsWith(':unknown')).length, 1);
  step = `${mode}/sms/process-death`; const processEvidence = await killAndRecover('sms');
  let state = await control('/state'); assert.equal(state.counts.sends, 2); assert.equal(state.counts.receipts ?? 0, 0);
  await launch(); await tap('tab_messages');
  assert.deepEqual(await smsRows(await scrollJournal()), before, 'Both original SMS records must survive');
  await capture(`${mode}-sms-restored`); await tap('message_reconcile'); await waitEvent('receipts');
  const after = await smsRows(await uiReady(async xml => {const rows = await smsRows(xml); return rows.length === 2 && rows.every(r => r.endsWith(':submitted'));}));
  assert.deepEqual(after.map(r => r.split(':')[1]).sort(), before.map(r => r.split(':')[1]).sort());
  await capture(`${mode}-sms-reconciled`); state = await control('/state');
  assert.equal(state.counts.sends, 2); assert.equal(state.counts.receipts, 1); assert.equal(state.counts.leases ?? 0, 0);
  assert.equal(state.counts.send_requests, 2);
  report.cases.push({mode, scenario: 'sms', ...processEvidence, state, originalRecordsPreserved: true, automaticResends: 0});
}
async function controlCase(mode) {
  await resetAndLogin(mode, 'control'); step = `${mode}/control/start`;
  await tap('tab_calls'); await tap(`route_${mode}`); await fill('dial_number', '+15550100999');
  await tap('call_dial'); await confirmAction(mode, 'call'); await waitEvent('starts');
  await uiReady(async xml => await xpath(xml, `string(${node('call_dtmf')}/@enabled)`) === 'true');
  step = `${mode}/control/first-death`;
  const firstDeath = await killAndRecover('control'); await waitEvent('guard_attempts'); await waitEvent('statuses');
  let state = await control('/state');
  assert.equal(state.counts.ends ?? 0, 0); assert.equal(state.counts.media, 1); assert.equal(state.simulated_guard_unknown, true);
  await launch(); await tap('tab_calls');
  await uiReady(async xml => await xpath(xml, `count(${node('call_mute')} | ${node('call_dtmf')})`) === '0' && await xpath(xml, `count(${node('call_hangup')})`) === '1');
  await capture(`${mode}-original-call-unconfirmed`);
  step = `${mode}/control/reauthentication`;
  const observerBaseline = state.counts.observers;
  await tap('tab_settings'); await tap('sign_in_again');
  await uiReady(async xml => await xpath(xml, `string(${node('gateway_server')}/@text)`) === `https://127.0.0.1:${bootstrap.data_port}` &&
    Number(await xpath(xml, `string-length(${node('gateway_password')}/@text)`)) > 0);
  await tap('connect_gateway'); await waitEvent('logins', 2); await waitEvent('observers', observerBaseline + 1); await waitEvent('new_login_statuses');
  await tap('tab_calls');
  await uiReady(async xml => await xpath(xml, `count(${node('call_mute')})`) === '0' && await xpath(xml, `count(${node('call_hangup')})`) === '1');
  state = await control('/state'); assert.equal(state.counts.ends ?? 0, 0); assert.equal(state.counts.media, 1); assert.equal(state.counts.leases, 1);
  await capture(`${mode}-reauthenticated-original-control`);
  step = `${mode}/control/lost-end-receipt`;
  await control('/permit-end', {}); await tap('call_hangup'); await waitEvent('end_requests'); await waitEvent('status_unavailable');
  await capture(`${mode}-end-result-unknown`);
  const deniedBaseline = (await control('/state')).counts.status_unavailable;
  step = `${mode}/control/second-death`;
  const secondDeath = await killAndRecover('ending'); await waitEvent('status_unavailable', deniedBaseline + 1);
  state = await control('/state'); assert.equal(state.counts.end_requests, 1); assert.equal(state.counts.ends, 1); assert.equal(state.counts.deletes ?? 0, 0);
  step = `${mode}/control/cold-entry`;
  const restoredOwner = await identity(); await launch(true); assert.deepEqual(await identity(), restoredOwner, 'Cold Activity entry must not restart the restored Service process'); await tap('tab_calls');
  await uiReady(async xml => await xpath(xml, `count(${node('call_mute')})`) === '0' && await xpath(xml, `count(${node('call_hangup')})`) === '1');
  step = `${mode}/control/retry-end`;
  await control('/permit-end', {}); await tap('call_hangup'); await waitEvent('end_requests', 2); await waitEvent('deletes');
  step = `${mode}/control/retire-ui`;
  await uiReady(async xml => await xpath(xml, `count(${node('call_hangup')})`) === '0' && await xpath(xml, `string(${node('call_dial')}/@enabled)`) === 'true');
  await capture(`${mode}-original-end-confirmed`); state = await control('/state');
  assert.equal(state.counts.start_requests, 1); assert.equal(state.counts.starts, 1); assert.equal(state.counts.leases, 1); assert.equal(state.counts.media, 1);
  assert.equal(state.counts.end_requests, 2); assert.equal(state.counts.ends, 1); assert.equal(state.counts.deletes, 1); assert.equal(state.counts.logins, 2);
  assert.equal(state.counts.guard_attempts, 1); assert.equal(state.terminal, true); assert.equal(state.end_receipt_held, false);
  report.cases.push({mode, scenario: 'control-under-unknown-guard', firstDeath, secondDeath, state, coldActivityEntry: true, newNetworkMedia: false,
    boundary: 'Separate real Core tests enforce recovery authorization; this UI peer injects unknown guard and unavailable receipt evidence'});
}
try {
  await mkdir(output, {mode: 0o700}); // Refuse to overwrite an earlier run's evidence.
  ownedOutput = true;
  assert.equal((await stat(output)).mode & 0o077, 0, 'Evidence must be owner-only');
  assert.equal(hash(await readFile(fixturePath)), fixtureHash, 'Fixture artifact hash mismatch');
  assert.equal((await device('get-state')).trim(), 'device', 'Authorized target must be ready');
  const apkPath = (await shell('pm', 'path', qa)).trim();
  assert(/^package:\/data\/app\/[^\r\n]+\/base.apk$/.test(apkPath), 'Expected exact installed QA base APK');
  const installedPath=apkPath.slice(8), localAPK=`${output}/installed-qa-verification.apk`;
  const remoteHash=(await shell('sha256sum',installedPath)).trim().split(/\s+/)[0];
  report.installedArtifact={method:'adb-sync-pull-and-device-sha256',deviceSHA256:remoteHash};
  assert.equal(remoteHash,apkHash,'Device-side QA hash must match the exact CI APK');
  // ADB file sync avoids mixing remote exec stdout/stderr with APK bytes.
  await device('pull',installedPath,localAPK);
  const transferredHash=hash(await readFile(localAPK));report.installedArtifact.pulledSHA256=transferredHash;
  assert.equal(transferredHash,apkHash,'Pulled QA must match the exact CI APK');
  await unlink(localAPK);
  assert(/^\d+$/.test((await shell('run-as', qa, 'id', '-u')).trim()), 'QA must be debuggable');
  ownedQA = true; report.api = Number((await shell('getprop', 'ro.build.version.sdk')).trim());
  if (suite === 'process') { await callCase(mode, 'preparing'); await callCase(mode, 'submitted'); await smsCase(mode); }
  else await controlCase(mode);
  report.passed = true;
} catch (error) {
  report.failure = {step, reason: error.message}; process.exitCode = 1;
  if (ownedQA && !abort.signal.aborted) {
    try { await capture('failure-current-qa'); report.failureUI = 'fresh QA XML and screenshot captured'; }
    catch { report.failureUI = 'current QA focus/XML unavailable; no previous screenshot reused'; }
    try { if (bootstrap) await control('/state'); }
    catch { report.failurePeer = 'fresh peer state unavailable; last observed state retained with timestamp'; }
  }
} finally {
  cleaning = true; cleanupDeadline = Math.min(Date.now()+20_000, deadline+25_000); clearTimeout(timer);
  const failures = [];
  if (ownedOutput) await writeFile(`${output}/result.json`, JSON.stringify(report, null, 2)+'\n', {mode: 0o600}).catch(() => {});
  try { await stopFixture(); } catch { failures.push('fixture or owned reverse removal'); }
  if (ownedQA) { try { await shell('am', 'force-stop', qa); } catch { failures.push('QA stop'); } }
  try { if (ownedQA) await shell('rm', '-f', remoteXML); } catch { failures.push('owned XML removal'); }
  report.cleanupFailures = failures;
  if (failures.length) { report.passed = false; process.exitCode = 1; }
  try { if (ownedOutput) await writeFile(`${output}/result.json`, JSON.stringify(report, null, 2)+'\n', {mode: 0o600}); } catch { process.exitCode = 1; }
  clearTimeout(hardStop);
  console.log(JSON.stringify({passed: report.passed, completedCases: report.cases.length, failure: report.failure, cleanupFailures: failures}));
}
