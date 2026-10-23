import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
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
function run(bin, args, expected = 0) {
  const result = spawnSync(bin, args, { env, encoding: 'utf8', timeout: 90000, maxBuffer: 8 << 20 });
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
function verify(directory, expected = 0) {
  return run(process.execPath, [tool, 'verify', '--package', directory, '--arch', 'universal'], expected);
}
const receipt = { schema: 1, arm64Manifest: hash(path.join(arm64, 'SHA256SUMS')), universalManifest: hash(path.join(universal, 'SHA256SUMS')), slices: [], rejected: [] };
try {
  verify(universal);
  assert.match(verify(arm64, 1).stderr, /architecture mismatch: mdd-agent/);
  receipt.rejected.push('arm64-only is not Universal');
  const appBinary = run('/usr/libexec/PlistBuddy', ['-c', 'Print :CFBundleExecutable', path.join(universal, 'MDD Agent.app/Contents/Info.plist')]).stdout.trim();
  for (const arch of ['arm64', 'x86_64']) {
    const record = { architecture: arch, execution: arch === 'x86_64' ? 'Rosetta on Apple Silicon' : 'native', configEntries: 0, audioHelp: 0, cellularArgumentRejections: 0 };
    for (const [index, name] of ['mdd-agent', `MDD Agent.app/Contents/MacOS/${appBinary}`].entries()) {
      const config = path.join(root, `${arch}-${index}`, 'config.json');
      const initial = JSON.parse(run('/usr/bin/arch', [`-${arch}`, path.join(universal, name), 'config', 'init', '-config', config]).stdout);
      const before = hash(config);
      const shown = JSON.parse(run('/usr/bin/arch', [`-${arch}`, path.join(universal, name), 'config', 'show', '-config', config]).stdout);
      assert.deepEqual(shown, initial, 'show must retain the initialized identity/configuration');
      assert.equal(shown.path, config);
      assert.equal(shown.ready, false, 'unconfigured Agent must not report ready');
      assert.equal(shown.settings.agent.modem_enabled, false);
      assert.equal(fs.statSync(config).mode & 0o777, 0o600);
      assert.equal(hash(config), before, 'show modified the config');
      record.configEntries++;
    }
    for (const prefix of ['', 'MDD Agent.app/Contents/MacOS/']) {
      const help = run('/usr/bin/arch', [`-${arch}`, path.join(universal, prefix + 'mdd-call-audio-helper'), '-h']);
      assert.match(help.stderr, /Usage of/);
      assert.match(help.stderr, /-mode/);
      record.audioHelp++;
      run('/usr/bin/arch', [`-${arch}`, path.join(universal, prefix + 'mdd-cellular-io')], 2);
      record.cellularArgumentRejections++;
    }
    receipt.slices.push(record);
  }
  const config = path.join(root, 'arm64-0/config.json'), configHash = hash(config);
  const state = path.join(root, 'uncreated-state');
  function preflight(script, directory, status = 0) {
    const result = run('/bin/sh', [script, 'preflight', '--candidate', directory, '--config', config, '--state', state], status);
    assert(!fs.existsSync(state), 'preflight created deployment state');
    assert(!fs.existsSync(path.join(env.HOME, 'Library/LaunchAgents')), 'preflight wrote launchd configuration');
    assert.equal(hash(config), configHash, 'preflight changed user configuration');
    return result;
  }
  for (const directory of [arm64, universal]) assert.equal(JSON.parse(preflight(installer, directory).stdout).status, 'preflight_ok');
  const partial = path.join(root, 'partial-helper');
  fs.cpSync(universal, partial, { recursive: true });
  fs.copyFileSync(path.join(arm64, 'mdd-cellular-io'), path.join(partial, 'mdd-cellular-io'));
  checksums(partial);
  assert.match(verify(partial, 1).stderr, /architecture mismatch: mdd-cellular-io/);
  assert.match(preflight(installer, partial, 1).stderr, /candidate is missing a required architecture/);
  receipt.rejected.push('single-slice helper rejected before cutover');
  assert.equal(hash(path.join(arm64, 'SHA256SUMS')), receipt.arm64Manifest);
  assert.equal(hash(path.join(universal, 'SHA256SUMS')), receipt.universalManifest);
  console.log(JSON.stringify(receipt, null, 2));
} finally { fs.rmSync(root, { recursive: true, force: true }); }
