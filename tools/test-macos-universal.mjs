import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { execFileSync, spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const [arm64, universal] = process.argv.slice(2);
assert(arm64 && universal && path.isAbsolute(arm64) && path.isAbsolute(universal));
assert(process.env.TMPDIR, 'task-scoped TMPDIR is required');
const root = fs.mkdtempSync(path.join(process.env.TMPDIR, 'mdd-universal-e2e-'));
const tool = fileURLToPath(new URL('./macos-package.mjs', import.meta.url));
const installer = fileURLToPath(new URL('../go-runtime/release/install-macos-agent.sh', import.meta.url));
const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !/^(MDD_|DYLD_)/.test(key)));
env.HOME = path.join(root, 'home');
env.XDG_CONFIG_HOME = path.join(root, 'config-home');
fs.mkdirSync(env.HOME, { mode: 0o700 });
const timeoutMs = 90000, snapshotAfterMs = 20000, killGraceMs = 5000, maxBuffer = 8 << 20;
function say(line) { fs.writeSync(2, `macos-universal ${line}\n`); }
function processTree(rootPid) {
  try {
    const rows = execFileSync('ps', ['-axo', 'pid=,ppid=,stat=,etime=,command='], { encoding: 'utf8' }).split('\n')
      .map(line => /^\s*(\d+)\s+(\d+)\s+(\S+)\s+(\S+)\s+(.*)$/.exec(line)).filter(Boolean)
      .map(([, pid, ppid, stat, etime, command]) => ({ pid: Number(pid), ppid: Number(ppid), stat, etime, command }));
    const tree = new Set([rootPid]);
    for (let grew = true; grew;) {
      grew = false;
      for (const row of rows) if (tree.has(row.ppid) && !tree.has(row.pid)) { tree.add(row.pid); grew = true; }
    }
    return rows.filter(row => tree.has(row.pid)).map(row => `  ${row.pid} ppid=${row.ppid} ${row.stat} ${row.etime} ${row.command.slice(0, 200)}`).join('\n');
  } catch (error) {
    return `  process snapshot unavailable: ${error.message}`;
  }
}
async function run(bin, args, expected = 0) {
  const label = `${path.basename(bin)} ${args.slice(0, 2).map(arg => path.basename(arg)).join(' ')}`;
  const started = Date.now();
  say(`start ${label}`);
  const result = await new Promise(resolve => {
    const child = spawn(bin, args, { env, stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '', stderr = '', timedOut = false, overflow = false, killer;
    child.stdout.setEncoding('utf8');
    child.stderr.setEncoding('utf8');
    const terminate = () => {
      child.kill('SIGTERM');
      killer = setTimeout(() => child.kill('SIGKILL'), killGraceMs);
    };
    child.stdout.on('data', chunk => {
      stdout += chunk;
      if (stdout.length + stderr.length > maxBuffer && !overflow) { overflow = true; terminate(); }
    });
    child.stderr.on('data', chunk => {
      stderr += chunk;
      if (stdout.length + stderr.length > maxBuffer && !overflow) { overflow = true; terminate(); }
    });
    const snapshot = setTimeout(() => say(`snapshot ${label} after ${snapshotAfterMs}ms\n${processTree(child.pid)}`), snapshotAfterMs);
    const limit = setTimeout(() => { timedOut = true; terminate(); }, timeoutMs);
    const finish = (status, signal, error) => {
      clearTimeout(snapshot);
      clearTimeout(limit);
      clearTimeout(killer);
      resolve({ status, signal, error, stdout, stderr });
    };
    child.on('error', error => finish(null, null, error));
    child.on('close', (status, signal) => finish(status, signal, timedOut
      ? Object.assign(new Error(`spawn ${bin} ETIMEDOUT`), { code: 'ETIMEDOUT' })
      : overflow ? Object.assign(new Error(`stdio maxBuffer exceeded for ${bin}`), { code: 'ENOBUFS' }) : null));
  });
  say(`end ${label} elapsed=${Date.now() - started}ms status=${result.status} signal=${result.signal} error=${result.error?.code ?? ''}`);
  assert(!result.error && !result.signal, `${bin} did not terminate normally: ${result.error || result.signal}`);
  assert.equal(result.status, expected, `${bin}: ${result.stderr}`);
  return result;
}
function hash(file) { return createHash('sha256').update(fs.readFileSync(file)).digest('hex'); }
function entries(directory, relative = '') {
  return fs.readdirSync(path.join(directory, relative)).sort().flatMap(name => {
    const item = path.join(relative, name);
    return fs.lstatSync(path.join(directory, item)).isDirectory() ? entries(directory, item) : [item];
  });
}
function checksums(directory) {
  const list = entries(directory).filter(name => name !== 'SHA256SUMS');
  fs.writeFileSync(path.join(directory, 'SHA256SUMS'), list.map(name => `${hash(path.join(directory, name))}  ./${name}\n`).join(''));
}
async function verify(directory, expected = 0) {
  return run(process.execPath, [tool, 'verify', '--package', directory, '--arch', 'universal'], expected);
}
const receipt = { schema: 1, arm64Manifest: hash(path.join(arm64, 'SHA256SUMS')), universalManifest: hash(path.join(universal, 'SHA256SUMS')), slices: [], rejected: [] };
try {
  await verify(universal);
  assert.match((await verify(arm64, 1)).stderr, /architecture mismatch: mdd-agent/);
  receipt.rejected.push('arm64-only is not Universal');
  const appBinary = (await run('/usr/libexec/PlistBuddy', ['-c', 'Print :CFBundleExecutable', path.join(universal, 'MDD Agent.app/Contents/Info.plist')])).stdout.trim();
  for (const arch of ['arm64', 'x86_64']) {
    const record = { architecture: arch, execution: arch === 'x86_64' ? 'Rosetta on Apple Silicon' : 'native', configEntries: 0, audioHelp: 0, cellularArgumentRejections: 0 };
    for (const [index, name] of ['mdd-agent', `MDD Agent.app/Contents/MacOS/${appBinary}`].entries()) {
      const config = path.join(root, `${arch}-${index}`, 'config.json');
      const initial = JSON.parse((await run('/usr/bin/arch', [`-${arch}`, path.join(universal, name), 'config', 'init', '-config', config])).stdout);
      const before = hash(config);
      const shown = JSON.parse((await run('/usr/bin/arch', [`-${arch}`, path.join(universal, name), 'config', 'show', '-config', config])).stdout);
      assert.deepEqual(shown, initial, 'show must retain the initialized identity/configuration');
      assert.equal(shown.path, config);
      assert.equal(shown.ready, false, 'unconfigured Agent must not report ready');
      assert.equal(shown.settings.agent.modem_enabled, false);
      assert.equal(fs.statSync(config).mode & 0o777, 0o600);
      assert.equal(hash(config), before, 'show modified the config');
      record.configEntries++;
    }
    for (const prefix of ['', 'MDD Agent.app/Contents/MacOS/']) {
      const help = await run('/usr/bin/arch', [`-${arch}`, path.join(universal, prefix + 'mdd-call-audio-helper'), '-h']);
      assert.match(help.stderr, /Usage of/);
      assert.match(help.stderr, /-mode/);
      record.audioHelp++;
      await run('/usr/bin/arch', [`-${arch}`, path.join(universal, prefix + 'mdd-cellular-io')], 2);
      record.cellularArgumentRejections++;
    }
    receipt.slices.push(record);
  }
  const config = path.join(root, 'arm64-0/config.json'), configHash = hash(config);
  const state = path.join(root, 'uncreated-state');
  async function preflight(script, directory, status = 0) {
    const result = await run('/bin/sh', [script, 'preflight', '--candidate', directory, '--config', config, '--state', state], status);
    assert(!fs.existsSync(state), 'preflight created deployment state');
    assert(!fs.existsSync(path.join(env.HOME, 'Library/LaunchAgents')), 'preflight wrote launchd configuration');
    assert.equal(hash(config), configHash, 'preflight changed user configuration');
    return result;
  }
  for (const directory of [arm64, universal]) assert.equal(JSON.parse((await preflight(installer, directory)).stdout).status, 'preflight_ok');
  const partial = path.join(root, 'partial-helper');
  fs.cpSync(universal, partial, { recursive: true });
  fs.copyFileSync(path.join(arm64, 'mdd-cellular-io'), path.join(partial, 'mdd-cellular-io'));
  checksums(partial);
  assert.match((await verify(partial, 1)).stderr, /architecture mismatch: mdd-cellular-io/);
  assert.match((await preflight(installer, partial, 1)).stderr, /candidate is missing a required architecture/);
  receipt.rejected.push('single-slice helper rejected before cutover');
  assert.equal(hash(path.join(arm64, 'SHA256SUMS')), receipt.arm64Manifest);
  assert.equal(hash(path.join(universal, 'SHA256SUMS')), receipt.universalManifest);
  console.log(JSON.stringify(receipt, null, 2));
} finally { fs.rmSync(root, { recursive: true, force: true }); }
