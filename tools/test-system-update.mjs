#!/usr/bin/env node
// Consume complete artifacts on a disposable GitHub-hosted VM, without a
// checkout. Only the external release HTTP service is synthetic. Never route,
// mock or intercept the real Core, browser API, IPC, worker or systemctl.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import https from 'node:https';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const self = fileURLToPath(import.meta.url);
const artifact = path.dirname(self);
const qualification = JSON.parse(fs.readFileSync(path.join(artifact, 'qualification.json')));
const high = qualification.packages.high;
const low = qualification.packages.low;
const digest = data => createHash('sha256').update(data).digest('hex');

if (process.argv[2] === '--fixture') {
  const directory = process.argv[3];
  assert.equal(process.getuid(), 0);
  const prefix = '/repos/lovitus/mdd-sim-gateway';
  const assetPath = '/qualification/mdd.tar';
  const tar = fs.readFileSync(path.join(artifact, 'high.tar'));
  assert.equal(digest(tar), high.tar_sha256);
  const release = { tag_name: `v${high.version}`, html_url: `https://api.github.com${prefix}/releases/tags/v${high.version}`,
    body: 'Isolated same-source update qualification; not a public release.', published_at: new Date().toISOString(),
    assets: [{ name: `mdd-${high.version}-linux-amd64.tar`, size: tar.length,
      digest: `sha256:${high.tar_sha256}`, browser_download_url: `https://api.github.com${assetPath}` }] };
  const server = https.createServer({ key: fs.readFileSync(path.join(directory, 'api.key')),
    cert: fs.readFileSync(path.join(directory, 'api.crt')) }, (request, response) => {
    const known = request.method === 'GET' && request.headers.host === 'api.github.com' &&
      [prefix + '/releases/latest', prefix + `/releases/tags/v${high.version}`, assetPath].includes(request.url);
    fs.appendFileSync(path.join(directory, 'fixture-requests.jsonl'), JSON.stringify({ at: new Date().toISOString(),
      method: request.method, path: request.url, known }) + '\n');
    if (!known) { response.writeHead(404); response.end('unknown fixture route'); return; }
    const body = request.url === assetPath ? tar : Buffer.from(JSON.stringify(release));
    response.writeHead(200, { 'Content-Type': request.url === assetPath ? 'application/octet-stream' : 'application/json', 'Content-Length': body.length });
    response.end(body);
  });
  server.listen(443, '127.0.0.1');
  process.on('SIGTERM', () => server.close(() => process.exit(0)));
} else {
  await journey();
}

async function journey() {
  assert.equal(process.env.GITHUB_ACTIONS, 'true');
  assert.equal(process.platform, 'linux');
  assert.notEqual(process.getuid(), 0, 'browser runs as the original unprivileged runner');
  const { chromium } = await import('playwright-core');
  const work = path.join(process.env.RUNNER_TEMP, 'mdd-update-e2e');
  assert(!fs.existsSync(work), 'fresh qualification directory required');
  fs.mkdirSync(work, { mode: 0o700 });
  const output = path.join(artifact, 'evidence');
  fs.mkdirSync(output, { recursive: true });
  const receipt = { schema_version: 1, source_revision: qualification.source_revision, tree: qualification.tree,
    scope: 'same-source qualification versions; no physical Provider, paid action or public-version compatibility claim',
    checks: [], cleanup_errors: [] };
  const run = (command, args, input) => execFileSync(command, args, { input, encoding: 'utf8', timeout: 45000, maxBuffer: 2 ** 20 }).trim();
  const root = (...args) => run('sudo', ['-n', ...args]);
  const rootBytes = file => execFileSync('sudo', ['-n', 'cat', file], { timeout: 10000, maxBuffer: 8 * 2 ** 20 });
  const json = file => JSON.parse(rootBytes(file));
  const exists = file => { try { root('test', '-e', file); return true; } catch (error) { if (error.status === 1) return false; throw error; } };
  const record = name => receipt.checks.push(name);
  const units = ['mdd-core.service', 'mdd-provider-apply.service', 'mdd-egress.service'];
  const fixtureUnit = 'mdd-update-ci-release.service';
  const fixtureCA = '/usr/local/share/ca-certificates/mdd-update-qualification.crt';
  const hostsBefore = fs.readFileSync('/etc/hosts');
  const hostsAdded = Buffer.concat([hostsBefore, Buffer.from('\n127.0.0.1 api.github.com # mdd-update-qualification\n')]);
  const originalCABundle = digest(fs.readFileSync('/etc/ssl/certs/ca-certificates.crt'));
  const configFiles = ['/etc/mdd/core.json', '/etc/mdd/auth.json', '/etc/mdd/tls/server.crt', '/etc/mdd/tls/server.key', '/var/lib/mdd-agent/config.json'];
  const configHashes = () => Object.fromEntries(configFiles.map(file => [file, digest(rootBytes(file))]));
  const system = (...args) => root('/bin/systemctl', ...args);
  const show = unit => Object.fromEntries(system('show', unit, '-p', 'MainPID', '-p', 'ActiveState', '-p', 'SubState', '-p', 'Result', '-p', 'NRestarts', '-p', 'InvocationID', '-p', 'ExecMainStartTimestampMonotonic', '-p', 'ExecMainExitTimestampMonotonic', '-p', 'ExecMainPID', '-p', 'ExecMainStatus', '-p', 'User', '-p', 'Type').split('\n').map(line => [line.slice(0, line.indexOf('=')), line.slice(line.indexOf('=') + 1)]));
  const processSnapshot = unit => {
    const state = show(unit);
    assert.equal(state.ActiveState, 'active', `${unit} inactive`);
    assert(Number(state.MainPID) > 0, `${unit} PID missing`);
    return { ...state, image: root('readlink', `/proc/${state.MainPID}/exe`), sha256: root('sha256sum', `/proc/${state.MainPID}/exe`).split(' ')[0] };
  };
  const statusFile = '/var/lib/mdd/update-state.status.json';
  const maintenanceFile = '/var/lib/mdd/update-state.maintenance.json';
  const requestFile = '/var/lib/mdd/update-state.request.json';
  let installed = false, mapped = false, trusted = false, fixtureStarted = false, browser;
  let startedAt, before, browserHome;
  const requests = [], pageErrors = [], results = [];
  const password = 'update-e2e-disposable-password';
  // One bounded timer owns each wait; no background poller or rapid retry.
  async function waitFor(label, observe) {
    const deadline = Date.now() + 600000;
    let delay = 30000;
    for (;;) {
      assert(Date.now() + delay <= deadline, `${label}: bounded observation timed out`);
      await new Promise(resolve => setTimeout(resolve, delay));
      const value = await observe();
      if (value) return value;
      delay = Math.min(delay * 2, 240000);
    }
  }
  function install(name) {
    const packageInfo = qualification.packages[name];
    const archive = path.join(artifact, name + '.tar');
    assert.equal(digest(fs.readFileSync(archive)), packageInfo.tar_sha256);
    const directory = path.join(work, name);
    fs.mkdirSync(directory);
    run('tar', ['-xf', archive, '-C', directory]);
    installed = true;
    run('sudo', ['-n', path.join(directory, 'install-release.sh'), 'install', path.join(directory, packageInfo.release_id)], password + '\n');
    return path.join(directory, 'install-release.sh');
  }
  async function login(page) {
    await page.getByLabel('Password', { exact: true }).fill(password);
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await page.getByRole('button', { name: 'Sign out', exact: true }).waitFor();
  }
  // Read through the real authenticated browser context; never inject a token,
  // CSRF value or response. The UI sends the sole update mutation below.
  async function read(page, route) {
    const response = await page.evaluate(async route => {
      const reply = await fetch(route, { credentials: 'same-origin', cache: 'no-store' });
      return { status: reply.status, body: await reply.json() };
    }, route);
    assert.equal(response.status, 200, `${route} returned ${response.status}`);
    return response.body;
  }
  async function owner() {
    const config = json('/etc/mdd/core.json');
    const response = await fetch(`http://${config.local.listen}/v1/system/update/maintenance`, {
      headers: { 'X-MDD-Provider-Apply-Token': config.local.token }, signal: AbortSignal.timeout(10000),
    });
    assert.equal(response.status, 200, 'original authenticated maintenance readback');
    return response.json();
  }
  async function newPage() {
    const page = await browser.newPage();
    await page.addInitScript(() => localStorage.setItem('mdd-language', 'en'));
    page.on('pageerror', error => pageErrors.push(error.message));
    page.on('request', request => {
      if (request.method() === 'POST' && new URL(request.url()).pathname === '/v1/system/update/apply') requests.push(request.postDataJSON());
    });
    page.on('response', response => {
      if (new URL(response.url()).pathname === '/v1/system/update/progress' && response.status() === 200) results.push(response.json().catch(error => ({ read_error: error.message })));
    });
    await page.goto('https://localhost:8443/', { waitUntil: 'domcontentloaded' });
    await login(page);
    return page;
  }
  try {
    for (const file of ['/etc/mdd/core.json', '/usr/lib/mdd/current', fixtureCA]) assert(!exists(file), `preexisting state: ${file}`);
    run('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1', '-subj', '/CN=MDD update qualification CA',
      '-keyout', path.join(work, 'ca.key'), '-out', path.join(work, 'ca.crt'), '-addext', 'basicConstraints=critical,CA:TRUE']);
    run('openssl', ['req', '-newkey', 'rsa:2048', '-nodes', '-subj', '/CN=api.github.com', '-keyout', path.join(work, 'api.key'), '-out', path.join(work, 'api.csr')]);
    fs.writeFileSync(path.join(work, 'api.ext'), 'subjectAltName=DNS:api.github.com\nbasicConstraints=critical,CA:FALSE\nextendedKeyUsage=serverAuth\n');
    run('openssl', ['x509', '-req', '-in', path.join(work, 'api.csr'), '-CA', path.join(work, 'ca.crt'), '-CAkey', path.join(work, 'ca.key'),
      '-CAcreateserial', '-days', '1', '-extfile', path.join(work, 'api.ext'), '-out', path.join(work, 'api.crt')]);
    root('install', '-m', '0644', path.join(work, 'ca.crt'), fixtureCA); trusted = true;
    root('update-ca-certificates');
    fs.writeFileSync(path.join(work, 'hosts.qualification'), hostsAdded);
    root('cp', path.join(work, 'hosts.qualification'), '/etc/hosts'); mapped = true;
    root('systemd-run', '--unit=' + fixtureUnit, '--property=Type=exec', '--property=RuntimeMaxSec=1500',
      '--', process.execPath, self, '--fixture', work); fixtureStarted = true;
    // Loopback only, original TLS validation; never --insecure.
    await waitFor('external fixture readiness', () => {
      if (show(fixtureUnit).ActiveState === 'failed') throw new Error('release fixture failed');
      run('curl', ['--fail', '--silent', '--show-error', '--noproxy', '*', '--max-time', '10',
        'https://api.github.com/repos/lovitus/mdd-sim-gateway/releases/latest']);
      return true;
    });
    record('strict TLS release boundary ready');

    const redInstaller = install('red');
    root(redInstaller, 'start');
    await waitFor('old Core readiness', () => units.every(unit => show(unit).ActiveState === 'active'));
    const coreCert = path.join(work, 'core.crt');
    fs.writeFileSync(coreCert, rootBytes('/etc/mdd/tls/server.crt'));
    browserHome = path.join(work, 'browser-home');
    const nss = path.join(browserHome, '.pki', 'nssdb');
    fs.mkdirSync(nss, { recursive: true });
    run('certutil', ['-N', '-d', 'sql:' + nss, '--empty-password']);
    run('certutil', ['-A', '-d', 'sql:' + nss, '-n', 'MDD qualification Core', '-t', 'P,,', '-i', coreCert]);
    // Browser-context reads inherit the real browser TLS trust and login cookie.
    browser = await chromium.launch({ channel: 'chrome', headless: true, env: { ...process.env, HOME: browserHome } });
    let page = await newPage();
    const redRuntime = await read(page, '/v1/system/runtime');
    const redCheck = await read(page, '/v1/system/update/check');
    const redProcesses = Object.fromEntries(units.map(unit => [unit, processSnapshot(unit)]));
    assert.equal(redRuntime.vcs_revision, qualification.source_revision);
    assert.equal(redRuntime.vcs_modified, false);
    assert.equal(redCheck.ok, true);
    assert.equal(redCheck.comparison_known, false);
    assert.equal(redCheck.error_code, 'update.version_uncomparable');
    let counterexample;
    try { assert.equal(redRuntime.build_version, low.version, 'RELEASE_VERSION_NOT_WIRED'); }
    catch (error) { assert.match(error.message, /RELEASE_VERSION_NOT_WIRED/); counterexample = error.message; }
    assert(counterexample, 'old actual binary did not fail the new version assertion');
    assert.equal(requests.length, 0);
    assert(!exists(requestFile));
    assert.deepEqual(Object.fromEntries(units.map(unit => [unit, processSnapshot(unit)])), redProcesses);
    receipt.red = { reason: 'RELEASE_VERSION_NOT_WIRED', runtime: redRuntime, check: redCheck, processes: redProcesses };
    record('compiled pre-fix Core fails real release-version comparison before any mutation');
    await page.close(); await browser.close(); browser = undefined;
    root(redInstaller, 'stop');

    const installer = install('low');
    root(installer, 'start');
    root('install', '-d', '-m', '0700', '/var/lib/mdd-agent');
    const agent = '/usr/libexec/mdd/mdd-agent';
    const config = '/var/lib/mdd-agent/config.json';
    root(agent, 'config', 'init', '-config', config);
    root(agent, 'config', 'set', 'agent_id', 'ci-update-agent', '-config', config);
    root(agent, 'config', 'set', 'server', 'localhost:8443', '-config', config);
    run('sudo', ['-n', agent, 'config', 'set', 'token', '--stdin', '-config', config], json('/etc/mdd/auth.json').agent_token + '\n');
    const pin = digest(execFileSync('openssl', ['x509', '-in', coreCert, '-outform', 'DER']));
    root(agent, 'config', 'set', 'tls_sha256', pin, '-config', config);
    system('start', 'mdd-agent.service');
    await waitFor('qualification services ready', () => [...units, 'mdd-agent.service'].every(unit => show(unit).ActiveState === 'active'));
    browser = await chromium.launch({ channel: 'chrome', headless: true, env: { ...process.env, HOME: browserHome } });
    page = await newPage();
    const runtime = await read(page, '/v1/system/runtime');
    assert.equal(runtime.build_version, low.version, 'RELEASE_VERSION_NOT_WIRED');
    assert.equal(runtime.vcs_revision, qualification.source_revision);
    assert.equal(runtime.vcs_modified, false);
    const checked = await read(page, '/v1/system/update/check');
    assert.equal(checked.comparison_known, true); assert.equal(checked.update_available, true);
    const catalog = await read(page, '/v1/catalog/lines');
    const agents = await read(page, '/v1/agents');
    assert(agents.agents.some(item => item.agent_id === 'ci-update-agent'), 'original no-hardware Agent must actually register');
    before = { processes: Object.fromEntries([...units, 'mdd-agent.service'].map(unit => [unit, processSnapshot(unit)])), config: configHashes(), catalog_revision: catalog.revision,
      owner: await owner(), agent: agents.agents.find(item => item.agent_id === 'ci-update-agent') };
    assert.equal(before.owner.guard_installed, false);
    for (const unit of units) assert.equal(before.processes[unit].sha256, low.core_sha256);
    receipt.before = before;
    record('real low release, login, unmodified configuration and connected Agent');
    const badge = page.locator('.u-version.has-update');
    await badge.click();
    const dialog = page.getByRole('dialog');
    await dialog.getByRole('button', { name: 'Update now', exact: true }).waitFor();
    await page.screenshot({ path: path.join(output, 'confirm.png'), fullPage: true });
    await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
    assert.equal(requests.length, 0); assert(!exists(requestFile));
    assert.deepEqual(configHashes(), before.config);
    assert.deepEqual(Object.fromEntries([...units, 'mdd-agent.service'].map(unit => [unit, processSnapshot(unit)])), before.processes);
    record('browser cancel leaves zero update operations and original processes');
    startedAt = new Date().toISOString();
    system('start', 'mdd-updater.path');
    await badge.click();
    const applied = page.waitForResponse(response => new URL(response.url()).pathname === '/v1/system/update/apply' && response.request().method() === 'POST');
    await dialog.getByRole('button', { name: 'Update now', exact: true }).click();
    const applyResponse = await applied;
    assert.equal(applyResponse.status(), 202);
    const operation = await applyResponse.json();
    assert(operation.operation_id); assert.equal(operation.target, high.version);
    assert.deepEqual(requests, [{ expected_target: high.version }]);
    receipt.operation = operation;
    const finished = await waitFor('original updater completion', () => {
      const state = json(statusFile);
      assert.equal(state.operation_id, operation.operation_id);
      assert.equal(state.target, high.version);
      if (['failed', 'unknown'].includes(state.state)) throw new Error(`update stopped: ${state.phase}/${state.error_code}`);
      const worker = show('mdd-updater.service');
      const watcher = show('mdd-updater.path');
      assert.equal(watcher.ActiveState, 'active', 'original update watcher must remain active');
      assert.equal(watcher.Result, 'success');
      assert.notEqual(worker.ActiveState, 'failed');
      assert.equal(worker.Result, 'success');
      if (state.state !== 'succeeded' || worker.ActiveState !== 'inactive') return false;
      assert.equal(worker.MainPID, '0'); assert.equal(worker.SubState, 'dead');
      assert.equal(worker.ExecMainStatus, '0'); assert(Number(worker.ExecMainPID) > 0);
      assert.equal(watcher.SubState, 'waiting');
      receipt.worker_terminal = worker; receipt.watcher = watcher;
      return state;
    });
    assert.equal(finished.phase, 'restarted'); assert.equal(finished.error_code, 'update_applied');
    const maintenance = json(maintenanceFile);
    assert.equal(maintenance.operation_id, operation.operation_id);
    assert.equal(maintenance.lease_id, 'update-drain-' + operation.operation_id);
    assert.equal(maintenance.phase, 'released');
    assert.equal(maintenance.catalog_revision, before.catalog_revision);
    assert(maintenance.verified_core && maintenance.original_core !== maintenance.verified_core);
    assert.equal(maintenance.original_core, before.owner.core_instance);
    assert.equal(maintenance.target.core_sha256, high.core_sha256);
    assert.equal(maintenance.target.release_id, high.release_id);
    assert.deepEqual(maintenance.restarted_fixed_services, units);
    assert(Object.hasOwn(maintenance, 'providers'));
    assert(maintenance.providers === null || (Array.isArray(maintenance.providers) && maintenance.providers.length === 0));
    const after = Object.fromEntries([...units, 'mdd-agent.service'].map(unit => [unit, processSnapshot(unit)]));
    for (const unit of units) {
      assert.notEqual(after[unit].InvocationID, before.processes[unit].InvocationID);
      assert.notEqual(after[unit].MainPID, before.processes[unit].MainPID);
      assert.equal(after[unit].sha256, high.core_sha256);
      assert.equal(after[unit].NRestarts, '0');
    }
    assert.deepEqual(after['mdd-agent.service'], before.processes['mdd-agent.service']);
    assert.deepEqual(configHashes(), before.config);
    assert.equal(json(requestFile).operation_id, operation.operation_id);
    assert.equal(json('/var/lib/mdd-system/release-install/current.json').release_id, high.release_id);
    receipt.after = { processes: after, status: finished, maintenance, config: configHashes() };
    record('path-launched real root worker activates target; three real images change; original maintenance released');
    await page.getByRole('button', { name: 'Sign in', exact: true }).waitFor();
    await page.screenshot({ path: path.join(output, 'reauthenticate.png'), fullPage: true });
    const expiredSession = await page.evaluate(async () => {
      const response = await fetch('/v1/system/runtime', { credentials: 'same-origin', cache: 'no-store' });
      return { status: response.status, body: await response.json() };
    });
    assert.equal(expiredSession.status, 401);
    assert.equal(expiredSession.body.code, 'authentication_required');
    receipt.expired_session = expiredSession;
    await login(page);
    const finalRuntime = await read(page, '/v1/system/runtime');
    assert.equal(finalRuntime.build_version, high.version);
    assert.equal(finalRuntime.vcs_revision, runtime.vcs_revision);
    assert.equal(finalRuntime.module_version, runtime.module_version);
    assert.equal(finalRuntime.vcs_modified, runtime.vcs_modified);
    const finalCheck = await read(page, '/v1/system/update/check');
    assert.equal(finalCheck.comparison_known, true); assert.equal(finalCheck.update_available, false);
    const finalStatus = await read(page, '/v1/system/update/progress');
    assert.equal(finalStatus.operation_id, operation.operation_id); assert.equal(finalStatus.state, 'succeeded');
    assert.equal(finalStatus.maintenance.phase, 'released');
    const finalOwner = await owner();
    assert.equal(finalOwner.guard_installed, false);
    assert.equal(finalOwner.record.lease_id, maintenance.lease_id);
    assert.equal(finalOwner.core_instance, maintenance.verified_core);
    receipt.final_owner = finalOwner;
    const worker = show('mdd-updater.service');
    assert.equal(worker.Type, 'oneshot'); assert.equal(worker.User, 'root');
    assert(Number(worker.ExecMainPID) > 0); assert.equal(worker.ExecMainStatus, '0');
    assert(Number(worker.ExecMainExitTimestampMonotonic) >= Number(worker.ExecMainStartTimestampMonotonic));
    receipt.worker = worker;
    const finalAgent = (await read(page, '/v1/agents')).agents.find(item => item.agent_id === 'ci-update-agent');
    assert(finalAgent, 'original Agent re-registers after Core replacement');
    assert.equal(finalAgent.process_generation, before.agent.process_generation);
    assert.deepEqual((await read(page, '/v1/lines')).lines, []);
    assert.deepEqual((await read(page, '/v1/calls?limit=100')).calls, []);
    assert.deepEqual((await read(page, '/v1/messages')).messages, []);
    // App retains the open modal across authentication loss. Close it through
    // its real visible action before navigating; never click through its mask.
    await dialog.getByRole('button', { name: /^(Cancel|Close)$/ }).click();
    await dialog.waitFor({ state: 'hidden' });
    assert.deepEqual(requests, [{ expected_target: high.version }]);
    await page.getByRole('button', { name: /System settings/ }).click();
    await page.getByRole('button', { name: 'Backup & updates', exact: true }).click();
    await page.getByRole('button', { name: 'Update status', exact: true }).click();
    await page.getByRole('dialog').getByText(`Updated to v${high.version}`, { exact: true }).waitFor();
    await page.screenshot({ path: path.join(output, 'completed-original-operation.png'), fullPage: true });
    assert.deepEqual(requests, [{ expected_target: high.version }]);
    const observed = await Promise.all(results);
    assert(observed.some(item => item.operation_id === operation.operation_id && item.state === 'succeeded'));
    assert.deepEqual(pageErrors, []);
    record('browser reauthenticates, reads original operation and target version; zero paid state');
    receipt.runtime = finalRuntime;
    receipt.success = true;
  } catch (error) {
    receipt.success = false;
    receipt.failure = { name: error.name, message: error.message, stack: error.stack };
  } finally {
    const cleanup = async (name, action) => { try { await action(); } catch (error) { receipt.cleanup_errors.push(`${name}: ${error.message}`); } };
    let workersStopped = !installed;
    if (installed) {
      await cleanup('updater evidence', () => {
        for (const [name, file] of [['request', requestFile], ['status', statusFile], ['maintenance', maintenanceFile]]) {
          if (exists(file)) fs.writeFileSync(path.join(output, name + '.json'), JSON.stringify(json(file), null, 2) + '\n');
        }
        if (startedAt) {
          const journal = root('journalctl', '--since', startedAt, '-u', 'mdd-updater.service', '-u', 'mdd-updater.path', '-u', 'mdd-core.service',
            '-u', 'mdd-provider-apply.service', '-u', 'mdd-egress.service', '-o', 'json', '--no-pager');
          // Do not publish arbitrary program messages, environment or tokens.
          const entries = journal.split('\n').filter(Boolean).map(line => JSON.parse(line)).map(row => ({
            at: row.__REALTIME_TIMESTAMP, unit: row._SYSTEMD_UNIT, pid: row._PID, uid: row._UID,
            invocation: row._SYSTEMD_INVOCATION_ID, manager_unit: row.UNIT, message_id: row.MESSAGE_ID,
            job_result: row.JOB_RESULT, unit_result: row.UNIT_RESULT, exit_code: row.EXIT_CODE, exit_status: row.EXIT_STATUS,
            message: row._PID === '1' ? row.MESSAGE : undefined,
          }));
          fs.writeFileSync(path.join(output, 'service-invocations.json'), JSON.stringify(entries, null, 2) + '\n');
          if (receipt.success) {
            // systemd's structured failure IDs cover failed start jobs, failed
            // unit results and abnormal process exits, including prior worker
            // invocations whose Result may have been overwritten by a retry.
            const failures = new Set(['be02cf6855d2428ba40df7e9d022f03d', 'd9b373ed55a64feb8242e02dbe79a49c']);
            const updaterEntries = entries.filter(row => [row.unit, row.manager_unit].some(unit => ['mdd-updater.service', 'mdd-updater.path'].includes(unit)));
            assert(!updaterEntries.some(row => failures.has(row.message_id) ||
              row.message_id === '98e322203f7a4ed290d09fe03c09fe15' && !(row.exit_code === 'exited' && row.exit_status === '0') ||
              row.job_result && row.job_result !== 'done' || row.unit_result && row.unit_result !== 'success'),
            'failed updater invocation or watcher must not be hidden by later success');
            for (const unit of units) {
              const invocations = new Set(entries.filter(row => row.unit === unit && row.invocation).map(row => row.invocation));
              assert(invocations.size <= 2, `unexpected repeated process replacement for ${unit}`);
              const started = entries.filter(row => row.manager_unit === unit && row.message_id === '39f53479d3a045ac8e11786248231fbf');
              assert.equal(started.length, 1, `exactly one real start after update confirmation: ${unit}`);
            }
          }
        }
      });
      await cleanup('stop updater path and worker', () => system('stop', 'mdd-updater.path', 'mdd-updater.service'));
      await cleanup('stop owned services', () => system('stop', 'mdd-agent.service', 'mdd-cellular-guard.service', ...units));
      await cleanup('confirm no running update worker', () => {
        for (const unit of ['mdd-updater.path', 'mdd-updater.service', 'mdd-agent.service', 'mdd-cellular-guard.service', ...units]) {
          const state = show(unit); assert(!['active', 'activating', 'deactivating'].includes(state.ActiveState), `${unit} still running`);
          if (unit.endsWith('.service')) assert.equal(state.MainPID, '0');
        }
        workersStopped = true;
      });
    }
    await cleanup('browser', async () => { await browser?.close(); });
    if (fixtureStarted && workersStopped) await cleanup('stop release fixture', () => system('stop', fixtureUnit));
    if (exists(path.join(work, 'fixture-requests.jsonl'))) await cleanup('fixture receipt', () => {
      const lines = rootBytes(path.join(work, 'fixture-requests.jsonl')).toString().trim().split('\n').filter(Boolean).map(JSON.parse);
      fs.writeFileSync(path.join(output, 'release-requests.json'), JSON.stringify(lines, null, 2) + '\n');
      assert(lines.every(row => row.known), 'fixture received an unexpected path');
    });
    if (mapped && workersStopped) await cleanup('restore hosts', () => {
      assert.equal(digest(fs.readFileSync('/etc/hosts')), digest(hostsAdded), 'hosts changed outside fixture');
      fs.writeFileSync(path.join(work, 'hosts.before'), hostsBefore);
      root('cp', path.join(work, 'hosts.before'), '/etc/hosts');
      assert.deepEqual(fs.readFileSync('/etc/hosts'), hostsBefore);
    });
    if (trusted && workersStopped) await cleanup('remove temporary CA', () => {
      assert.equal(digest(fs.readFileSync(fixtureCA)), digest(fs.readFileSync(path.join(work, 'ca.crt'))));
      root('rm', fixtureCA); root('update-ca-certificates');
      assert.equal(digest(fs.readFileSync('/etc/ssl/certs/ca-certificates.crt')), originalCABundle);
    });
    if (workersStopped) await cleanup('remove fixture keys and browser sessions', () => root('rm', '-rf', '--', work));
    else receipt.cleanup_errors.push('retained fixture boundary because worker shutdown was unconfirmed; disposable VM must be destroyed');
    receipt.requests = requests;
    receipt.cleanup_complete = receipt.cleanup_errors.length === 0;
    receipt.success = receipt.success === true && receipt.cleanup_complete;
    fs.writeFileSync(path.join(output, 'receipt.json'), JSON.stringify(receipt, null, 2) + '\n');
    if (!receipt.success) process.exitCode = 1;
    console.log(JSON.stringify({ success: receipt.success, checks: receipt.checks, failure: receipt.failure?.message, cleanup_errors: receipt.cleanup_errors }));
  }
}
