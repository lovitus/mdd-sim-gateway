import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';

const [archive, output] = process.argv.slice(2);
assert(archive && output && [archive, output].every(path.isAbsolute));
assert.equal(process.env.GITHUB_ACTIONS, 'true', 'disposable hosted runner only');
assert.equal(process.env.RUNNER_ARCH, 'ARM64');
assert.equal(process.getuid(), 0);
assert.equal(os.arch(), 'arm64');
const root = fs.mkdtempSync(path.join(path.dirname(archive), 'final-tar-e2e-'));
const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !/^(MDD_|LD_|INVOCATION_ID)/.test(key)));
const receipt = { schema: 1, archiveSHA256: hash(archive), checks: [], complete: false };
const stable = '/usr/libexec/mdd/mdd-agent';
const defaultConfig = '/var/lib/mdd-agent/config.json';
const customConfig = '/var/lib/mdd-agent-qualified/config.json';
const units = ['mdd-agent.service', 'mdd-cellular-guard.service'];
let ownsInstallation = false;
let started = false;
let guardApplied = false;
let optFD = null;
let optOriginal = null;
let optPrepared = false;
function hash(file) { return createHash('sha256').update(fs.readFileSync(file)).digest('hex'); }
function run(bin, args, expected = 0, extraEnv = {}) {
  const result = spawnSync(bin, args, { env: { ...env, ...extraEnv }, encoding: 'utf8', timeout: 90000, maxBuffer: 8 << 20 });
  assert(!result.error && !result.signal, `${bin}: ${result.error || result.signal}`);
  assert.equal(result.status, expected, `${bin}: ${result.stderr}`);
  return result;
}
function exists(file) { try { fs.lstatSync(file); return true; } catch (error) { if (error.code === 'ENOENT') return false; throw error; } }
function snapshot(file) {
  const info = fs.statSync(file);
  return { hash: hash(file), uid: info.uid, gid: info.gid, mode: info.mode };
}
function entries(directory, prefix = '') {
  return fs.readdirSync(path.join(directory, prefix)).sort().flatMap(name => {
    const item = path.join(prefix, name);
    return fs.lstatSync(path.join(directory, item)).isDirectory() ? entries(directory, item) : [item];
  });
}
function checksums(directory) {
  const names = entries(directory).filter(name => name !== 'SHA256SUMS');
  fs.writeFileSync(path.join(directory, 'SHA256SUMS'), names.map(name => `${hash(path.join(directory, name))}  ./${name}\n`).join(''));
}
function extract(tar, directory) {
  fs.mkdirSync(directory);
  const names = run('tar', ['-tzf', tar]).stdout.trim().split('\n');
  assert(names.every(name => !name.startsWith('/') && !name.split('/').includes('..')));
  run('tar', ['--no-same-owner', '-xzf', tar, '-C', directory]);
  return path.join(directory, 'MDD-Agent-Linux-arm64');
}
function record(name) { receipt.checks.push(name); }
function noGuard() {
  assert(!exists('/var/lib/mdd-agent/cellular-guard-enabled'));
  assert(!exists('/etc/udev/rules.d/90-mdd-cellular-guard.rules'));
  assert(!exists('/etc/NetworkManager/conf.d/90-mdd-cellular-guard.conf'));
  assert.notEqual(spawnSync('nft', ['list', 'table', 'inet', 'mdd_cellular_guard']).status, 0);
}
function removeInstallation() {
  if (!ownsInstallation) return;
  if (started) run('systemctl', ['stop', 'mdd-agent.service']);
  if (guardApplied) {
    run('systemctl', ['disable', '--now', 'mdd-cellular-guard.service']);
    run('nft', ['delete', 'table', 'inet', 'mdd_cellular_guard']);
    for (const file of ['/var/lib/mdd-agent/cellular-guard-enabled', '/etc/udev/rules.d/90-mdd-cellular-guard.rules', '/etc/NetworkManager/conf.d/90-mdd-cellular-guard.conf']) {
      fs.rmSync(file, { force: true });
    }
    run('udevadm', ['control', '--reload']);
  }
  for (const unit of units) fs.rmSync(`/etc/systemd/system/${unit}`, { force: true });
  fs.rmSync('/etc/systemd/system/mdd-agent.service.d', { recursive: true, force: true });
  fs.rmSync('/usr/libexec/mdd', { recursive: true, force: true });
  fs.rmSync('/opt/mdd-agent', { recursive: true });
  run('systemctl', ['daemon-reload']);
  ownsInstallation = false; started = false; guardApplied = false;
}

try {
  for (const file of ['/usr/lib/mdd', '/usr/libexec/mdd', '/opt/mdd-agent', '/var/lib/mdd-agent', '/var/lib/mdd-agent-qualified',
    ...units.map(unit => `/etc/systemd/system/${unit}`), '/etc/systemd/system/mdd-agent.service.d']) {
    assert(!exists(file), `not a fresh runner: ${file}`);
  }
  noGuard();
  // A real empty ModemManager is used, not a replaced Agent/prober mock.
  assert.match(run('mmcli', ['-L']).stdout, /No modems were found/);
  const candidate = extract(archive, path.join(root, 'final'));
  const installer = path.join(candidate, 'install-linux-agent.sh');
  const agent = path.join(candidate, 'mdd-agent');
  const build = fs.readFileSync(path.join(candidate, 'BUILD.txt'), 'utf8');
  const revision = build.match(/^source_revision=([a-f0-9]{40})$/m)?.[1];
  const tree = build.match(/^source_tree=([a-f0-9]{40})$/m)?.[1];
  assert.equal(revision, run('git', ['rev-parse', 'HEAD']).stdout.trim());
  assert.equal(tree, run('git', ['rev-parse', 'HEAD^{tree}']).stdout.trim());
  receipt.sourceRevision = revision; receipt.sourceTree = tree;
  const release = `/opt/mdd-agent/releases/${revision}`;
  const beforeManifest = hash(path.join(candidate, 'SHA256SUMS'));
  const source = path.join(root, 'source');
  fs.mkdirSync(source);
  run('tar', ['-xzf', path.join(candidate, 'mdd-source.tar.gz'), '-C', source]);
  for (const unit of units) assert.equal(hash(path.join(candidate, unit)), hash(path.join(source, 'go-runtime/release', unit)));
  for (const binary of ['mdd-agent', 'mdd-call-audio-helper']) {
    assert.match(run('readelf', ['-h', path.join(candidate, binary)]).stdout, /Machine: +AArch64/);
    assert.doesNotMatch(run('ldd', [path.join(candidate, binary)]).stdout, /not found/);
    assert.match(run('go', ['version', '-m', path.join(candidate, binary)]).stdout, /GOARCH=arm64/);
  }
  assert.match(run('go', ['version', '-m', path.join(candidate, 'mdd-call-audio-helper')]).stdout, /github.com\/gen2brain\/malgo\s+v0.11.26/);
  run(path.join(candidate, 'mdd-call-audio-helper'), ['-h']);
  const audio = JSON.parse(run(path.join(candidate, 'mdd-call-audio-helper'), ['-backend', 'alsa', '-mode', 'list']).stdout);
  assert.equal(audio.ok, true); assert.equal(audio.version, 4); assert.equal(audio.backend, 'alsa');
  record('final archive ELF/dependencies/source/units and real ALSA enumeration (not audio hardware)');

  const probe = JSON.parse(run(agent, ['modem-probe']).stdout);
  assert.equal(probe.condition, 'ready'); assert.equal(probe.modems?.length || 0, 0);
  record('final package modem-probe enumerates empty MM with its real helper');
  noGuard();

  function install(action, directory = candidate, config = defaultConfig, expected = 0) {
    return run('/bin/bash', [installer, action, '--candidate', directory, '-config', config], expected);
  }
  function rejected(directory, pattern, config = defaultConfig) {
    assert.match(install('preflight', directory, config, 1).stderr, pattern);
    assert(!exists('/opt/mdd-agent')); assert(!exists('/usr/libexec/mdd'));
    noGuard();
  }
  for (const directory of ['/etc/NetworkManager/conf.d', '/etc/udev/rules.d', '/etc/systemd/system/multi-user.target.wants']) {
    fs.mkdirSync(directory, { recursive: true, mode: 0o755 });
  }
  // Hosted images deliberately make /opt writable. Keep the product's refusal,
  // then prepare only this ancestor on the disposable host, never its contents.
  const ancestors = ['/opt', '/var', '/var/lib', '/usr', '/usr/libexec', '/etc',
    '/etc/NetworkManager', '/etc/NetworkManager/conf.d', '/etc/udev', '/etc/udev/rules.d',
    '/etc/systemd', '/etc/systemd/system', '/etc/systemd/system/multi-user.target.wants'];
  receipt.hostAncestors = ancestors.map(directory => {
    if (!exists(directory)) return { path: directory, missing: true };
    const info = fs.lstatSync(directory);
    return { path: directory, directory: info.isDirectory(), symlink: info.isSymbolicLink(),
      uid: info.uid, gid: info.gid, mode: info.mode & 0o7777, dev: info.dev, ino: info.ino };
  });
  for (const item of receipt.hostAncestors) {
    if (item.missing && item.path === '/usr/libexec') continue;
    assert(!item.missing && item.directory && !item.symlink && item.uid === 0, JSON.stringify(item));
    assert((item.mode & 0o022) === 0 || (item.path === '/opt' && item.mode === 0o777), JSON.stringify(item));
  }
  optFD = fs.openSync('/opt', fs.constants.O_RDONLY | fs.constants.O_DIRECTORY | fs.constants.O_NOFOLLOW);
  optOriginal = fs.fstatSync(optFD);
  const observedOpt = receipt.hostAncestors.find(item => item.path === '/opt');
  assert.equal(optOriginal.dev, observedOpt.dev); assert.equal(optOriginal.ino, observedOpt.ino);
  assert.equal(optOriginal.mode & 0o7777, observedOpt.mode); assert.equal(optOriginal.uid, 0);
  if ((optOriginal.mode & 0o7777) === 0o777) {
    rejected(candidate, /installation\/config path is writable/);
    assert.equal(fs.fstatSync(optFD).mode, optOriginal.mode);
    for (const unit of units) assert(!exists(`/etc/systemd/system/${unit}`));
    fs.fchmodSync(optFD, 0o755); optPrepared = true;
    record('actual root-owned 0777 deployment ancestor rejected unchanged, then prepared as 0755 on hosted fixture only');
  }
  // A private mount namespace removes one sandbox prerequisite without touching
  // the host's live NetworkManager directory, routes or service state.
  const emptyNM = path.join(root, 'empty-nm'); fs.mkdirSync(emptyNM);
  const missingDirectory = run('unshare', ['--mount', '--propagation', 'private', '/bin/bash', '-ec',
    'mount --bind "$1" /etc/NetworkManager; exec /bin/bash "$2" preflight --candidate "$3"',
    'missing-sandbox-directory', emptyNM, installer, candidate], 1);
  assert.match(missingDirectory.stderr, /prepare a root-owned 0755 directory/);
  noGuard();
  record('missing original-unit writable directory rejected before guard side effects');
  assert.equal(JSON.parse(install('preflight').stdout).config, 'needs_config');
  record('missing initial configuration does not deadlock placement and is not marked ready');
  for (const missing of ['mdd-call-audio-helper', 'mdd-cellular-guard.service']) {
    const incomplete = path.join(root, missing);
    fs.cpSync(candidate, incomplete, { recursive: true });
    fs.rmSync(path.join(incomplete, missing)); checksums(incomplete);
    rejected(incomplete, /missing package component/);
  }
  const invalid = path.join(root, 'invalid');
  fs.cpSync(candidate, invalid, { recursive: true });
  fs.appendFileSync(path.join(invalid, 'mdd-call-audio-helper'), 'invalid');
  rejected(invalid, /checksum mismatch/);
  fs.copyFileSync(path.join(candidate, 'mdd-call-audio-helper'), path.join(invalid, 'mdd-call-audio-helper'));
  const bytes = fs.readFileSync(path.join(invalid, 'mdd-call-audio-helper'));
  bytes.writeUInt16LE(62, 18); fs.writeFileSync(path.join(invalid, 'mdd-call-audio-helper'), bytes); checksums(invalid);
  rejected(invalid, /wrong ELF architecture/);
  fs.copyFileSync(path.join(candidate, 'mdd-call-audio-helper'), path.join(invalid, 'mdd-call-audio-helper'));
  const loaderBytes = fs.readFileSync(path.join(invalid, 'mdd-call-audio-helper'));
  const loader = Buffer.from('/lib/ld-linux-aarch64.so.1');
  const offset = loaderBytes.indexOf(loader); assert(offset > 0);
  Buffer.from('/bad/ld-linux-aarch64.so.1').copy(loaderBytes, offset);
  fs.writeFileSync(path.join(invalid, 'mdd-call-audio-helper'), loaderBytes); checksums(invalid);
  rejected(invalid, /missing ELF loader/);
  const dependencyBytes = fs.readFileSync(path.join(candidate, 'mdd-call-audio-helper'));
  const dependencyOffset = dependencyBytes.indexOf(Buffer.from('libc.so.6\0')); assert(dependencyOffset > 0);
  Buffer.from('libq.so.6\0').copy(dependencyBytes, dependencyOffset);
  fs.writeFileSync(path.join(invalid, 'mdd-call-audio-helper'), dependencyBytes); checksums(invalid);
  rejected(invalid, /missing dynamic dependency/);
  record('incomplete, corrupt, wrong-architecture and unavailable-loader payloads rejected before placement');

  for (const target of ['/usr/lib/mdd', '/usr/libexec/mdd', '/etc/systemd/system/mdd-agent.service.d']) {
    fs.mkdirSync(target); const marker = path.join(target, 'foreign'); fs.writeFileSync(marker, 'owner');
    const before = snapshot(marker);
    assert.match(install('install', candidate, defaultConfig, 1).stderr, /existing|owner|server/);
    assert.deepEqual(snapshot(marker), before); assert(!exists('/opt/mdd-agent'));
    fs.rmSync(target, { recursive: true });
  }
  // The target changes after a successful preflight; install must check again.
  install('preflight');
  fs.symlinkSync('/foreign-owner', '/etc/systemd/system/mdd-agent.service');
  install('install', candidate, defaultConfig, 1);
  assert.equal(fs.readlinkSync('/etc/systemd/system/mdd-agent.service'), '/foreign-owner');
  fs.unlinkSync('/etc/systemd/system/mdd-agent.service');
  record('Core, foreign/drop-in and changed-after-preflight ownership preserved');

  // Bash's debugger event lets a competing administrator replace a just-published
  // object deterministically. The real installer/ln/syscalls are not mocked.
  const hook = path.join(root, 'concurrent-owner.bash');
  fs.writeFileSync(hook, `set -T
inject_competing_owner() {
  if [ -L /etc/systemd/system/mdd-agent.service ] && [ ! -e "$MDD_INSTALL_CONFLICT_DONE" ]; then
    : > "$MDD_INSTALL_CONFLICT_DONE"
    rm /etc/systemd/system/mdd-agent.service
    ln -s /foreign-owner /etc/systemd/system/mdd-agent.service
    ln -s /foreign-guard /etc/systemd/system/mdd-cellular-guard.service
  fi
}
trap inject_competing_owner DEBUG
`, { mode: 0o600 });
  const injected = path.join(root, 'injected');
  const interrupted = run('/bin/bash', [installer, 'install', '--candidate', candidate], 1,
    { BASH_ENV: hook, MDD_INSTALL_CONFLICT_DONE: injected });
  assert.match(interrupted.stderr, /retained; an administrator/);
  assert.equal(fs.readlinkSync('/etc/systemd/system/mdd-agent.service'), '/foreign-owner');
  assert.equal(fs.readlinkSync('/etc/systemd/system/mdd-cellular-guard.service'), '/foreign-guard');
  assert(exists(path.join(root, 'injected')));
  noGuard();
  // Only this test's deliberate foreign objects and partial placement are reset.
  ownsInstallation = true; removeInstallation();
  record('partial-publication conflict preserves concurrently replaced foreign links');

  install('install'); ownsInstallation = true;
  assert(!exists(defaultConfig)); noGuard();
  for (const unit of units) {
    assert.equal(fs.readlinkSync(`/etc/systemd/system/${unit}`), `${release}/${unit}`);
    assert.equal(run('systemctl', ['show', unit, '-p', 'ActiveState', '--value']).stdout.trim(), 'inactive');
    assert(!exists(`/etc/systemd/system/multi-user.target.wants/${unit}`));
  }
  const initialized = JSON.parse(run(stable, ['config', 'init', '-config', defaultConfig]).stdout);
  assert.equal(initialized.ready, false);
  assert.equal(initialized.settings.agent.modem_enabled, false);
  assert.equal(initialized.settings.agent.raw_usb_source_enabled || false, false);
  assert.equal(initialized.settings.agent.raw_usb_importer_enabled || false, false);
  let configBefore = snapshot(defaultConfig);
  fs.mkdirSync('/var/lib/mdd-agent/state', { mode: 0o700 });
  fs.writeFileSync('/var/lib/mdd-agent/state/retained', 'existing operation ownership', { mode: 0o600 });
  const stateBefore = snapshot('/var/lib/mdd-agent/state/retained');
  assert.match(install('start-preflight', release, defaultConfig, 1).stderr, /configuration is not ready/);
  assert.deepEqual(snapshot(defaultConfig), configBefore);
  assert.deepEqual(snapshot('/var/lib/mdd-agent/state/retained'), stateBefore);
  assert.equal(JSON.parse(run(stable, ['modem-probe']).stdout).condition, 'ready');
  record('default placement remains inactive; stable CLI resolves real sibling; ready:false refuses startup');
  removeInstallation();
  noGuard();

  // Use the real private file, never the redacted presentation view.
  const readerConfig = JSON.parse(fs.readFileSync(defaultConfig, 'utf8'));
  readerConfig.agent.id = 'hosted-empty-modem';
  readerConfig.agent.server_url = 'wss://127.0.0.1:1/v1/agent/ws';
  readerConfig.agent.server_token = 'test-only-agent-token-000000000000000000000000';
  readerConfig.agent.tls_sha256 = 'a'.repeat(64);
  fs.writeFileSync(defaultConfig, JSON.stringify(readerConfig));
  configBefore = snapshot(defaultConfig);
  assert.equal(JSON.parse(run(agent, ['config', 'show', '-config', defaultConfig]).stdout).ready, true);
  install('preflight'); install('install'); ownsInstallation = true;
  assert.equal(JSON.parse(install('start-preflight', release).stdout).modem_enabled, false);
  assert.deepEqual(snapshot(defaultConfig), configBefore);
  assert.deepEqual(snapshot('/var/lib/mdd-agent/state/retained'), stateBefore);
  noGuard(); removeInstallation();
  record('ready:true with modem/raw off succeeds in all three entries without enabling guard');

  const stateDirectory = '/var/lib/mdd-agent/state';
  fs.mkdirSync(`${stateDirectory}/loop`, { mode: 0o700 });
  const loopScript = 'mount --bind "$1" "$1/loop"; if find "$1" -mindepth 1 -print0 >/dev/null; then exit 90; fi; exec /bin/bash "$2" preflight --candidate "$3"';
  const incompleteScan = run('unshare', ['--mount', '--propagation', 'private', '/bin/bash', '-ec', loopScript,
    'state-scan', stateDirectory, installer, candidate], 1);
  assert.match(incompleteScan.stderr, /cannot completely scan adjacent state/);
  assert.deepEqual(snapshot(defaultConfig), configBefore);
  assert.deepEqual(snapshot(`${stateDirectory}/retained`), stateBefore);
  assert(!exists('/opt/mdd-agent')); noGuard();
  fs.rmdirSync(`${stateDirectory}/loop`);
  record('real failed filesystem traversal is rejected without changing config/state');

  fs.mkdirSync('/var/lib/mdd-agent-qualified', { mode: 0o700 });
  const configured = structuredClone(readerConfig);
  configured.agent.modem_enabled = true;
  fs.writeFileSync(customConfig, JSON.stringify(configured), { mode: 0o600 });
  const customBefore = snapshot(customConfig);
  assert.equal(JSON.parse(run(agent, ['config', 'show', '-config', customConfig]).stdout).ready, true);
  const customState = '/var/lib/mdd-agent-qualified/state';
  fs.symlinkSync('/var/lib/mdd-agent/state', customState);
  rejected(candidate, /symlink/, customConfig);
  assert.equal(fs.readlinkSync(customState), '/var/lib/mdd-agent/state'); fs.unlinkSync(customState);
  fs.mkdirSync(customState, { mode: 0o700 });
  fs.writeFileSync(`${customState}/retained`, 'exact custom config state', { mode: 0o600 });
  const customStateBefore = snapshot(`${customState}/retained`);
  fs.writeFileSync(`${customState}/raw-modem-handoffs.db`, 'unresolved recovery belongs to original owner', { mode: 0o600 });
  const recoveryBefore = snapshot(`${customState}/raw-modem-handoffs.db`);
  rejected(candidate, /original deployment\/recovery owner/, customConfig);
  assert.deepEqual(snapshot(`${customState}/raw-modem-handoffs.db`), recoveryBefore);
  fs.unlinkSync(`${customState}/raw-modem-handoffs.db`);
  configured.agent.modem_backend = 'serial';
  fs.writeFileSync(customConfig, JSON.stringify(configured));
  rejected(candidate, /serial requires/, customConfig);
  delete configured.agent.modem_backend;
  fs.writeFileSync(customConfig, JSON.stringify(configured));
  fs.writeFileSync(`${customConfig}.setup-pending`, '{}', { mode: 0o600 });
  rejected(candidate, /finish the existing/, customConfig);
  fs.unlinkSync(`${customConfig}.setup-pending`);
  fs.chmodSync(customConfig, 0o644);
  rejected(candidate, /0600/, customConfig);
  fs.chmodSync(customConfig, 0o600);
  rejected(candidate, /protects/, '/etc/mdd-agent/config.json');
  install('install', candidate, customConfig); ownsInstallation = true;
  const lateSnippet = '/etc/udev/rules.d/90-mdd-cellular-guard.rules';
  fs.writeFileSync(lateSnippet, '# belongs to another deployment\n');
  const lateBefore = snapshot(lateSnippet);
  assert.match(install('start-preflight', release, customConfig, 1).stderr, /existing guard state/);
  assert.deepEqual(snapshot(lateSnippet), lateBefore); fs.unlinkSync(lateSnippet);
  run('nft', ['add', 'table', 'inet', 'mdd_cellular_guard']);
  const tableBefore = run('nft', ['-j', 'list', 'table', 'inet', 'mdd_cellular_guard']).stdout;
  assert.match(install('start-preflight', release, customConfig, 1).stderr, /existing cellular guard/);
  assert.equal(run('nft', ['-j', 'list', 'table', 'inet', 'mdd_cellular_guard']).stdout, tableBefore);
  run('nft', ['delete', 'table', 'inet', 'mdd_cellular_guard']);
  const startup = JSON.parse(install('start-preflight', release, customConfig).stdout);
  assert.equal(startup.modem_enabled, true); assert.equal(startup.started, false); assert.equal(startup.connected, false);
  assert.deepEqual(snapshot(customConfig), customBefore); assert.deepEqual(snapshot(defaultConfig), configBefore);
  assert.deepEqual(snapshot('/var/lib/mdd-agent/state/retained'), stateBefore);
  assert.deepEqual(snapshot(`${customState}/retained`), customStateBefore);
  noGuard();
  record('explicit private config uses only an ExecStart drop-in; config/state/pin unchanged; pure preflight');

  // Explicit start on a disposable empty hosted machine, never production.
  // A missing kernel/systemd capability is a real failed qualification, not skip.
  assert(!exists('/sys/module/vhci_hcd'), 'VHCI must remain unloaded until the original guard starts');
  record('VHCI is not loaded before explicit Agent startup');
  run('systemctl', ['start', 'mdd-agent.service']); started = true; guardApplied = true;
  await new Promise(resolve => setTimeout(resolve, 30000));
  const status = JSON.parse(run(stable, ['status', '-config', customConfig]).stdout);
  assert.equal(status.state, 'running');
  const controlGroup = run('systemctl', ['show', 'mdd-agent.service', '-p', 'ControlGroup', '--value']).stdout.trim();
  assert.equal(controlGroup, '/system.slice/mdd-agent.service');
  assert.equal(run('systemctl', ['show', 'mdd-agent.service', '-p', 'NRestarts', '--value']).stdout.trim(), '0');
  assert.equal(fs.readFileSync('/var/lib/mdd-agent/cellular-guard-enabled', 'utf8'), 'mdd-cellular-guard-v1\n');
  assert.equal(fs.statSync('/var/lib/mdd-agent/cellular-guard-enabled').mode & 0o777, 0o600);
  assert(fs.readFileSync('/etc/udev/rules.d/90-mdd-cellular-guard.rules', 'utf8').includes('/usr/libexec/mdd/mdd-agent cellular-guard protect-netdev'));
  assert(fs.readFileSync('/etc/NetworkManager/conf.d/90-mdd-cellular-guard.conf', 'utf8').includes('unmanaged-devices+=type:gsm'));
  assert(fs.lstatSync('/etc/systemd/system/multi-user.target.wants/mdd-cellular-guard.service').isSymbolicLink());
  const rules = JSON.parse(run('nft', ['-j', 'list', 'table', 'inet', 'mdd_cellular_guard']).stdout);
  assert(rules.nftables.some(entry => entry.table?.name === 'mdd_cellular_guard'));
  const defaults = run('find', ['/sys/devices/platform', '-path', '*vhci_hcd*', '-name', 'interface_authorized_default']).stdout.trim().split('\n').filter(Boolean);
  assert(defaults.length > 0, 'real VHCI interface authorization boundary is required');
  for (const file of defaults) assert.equal(fs.readFileSync(file, 'utf8').trim(), '0');
  assert.deepEqual(snapshot(customConfig), customBefore);
  assert.deepEqual(snapshot('/var/lib/mdd-agent/state/retained'), stateBefore);
  assert.deepEqual(snapshot(`${customState}/retained`), customStateBefore);
  record('original unit executes real persistent guard and empty managed MM in fixed cgroup without changing configuration');
  receipt.notQualified = ['physical modem/SIM/AT', 'audio/call/SMS', 'real bearer/traffic isolation', 'DHCP/PPP/IPv6', 'non-systemd managed modem'];
  assert.equal(hash(path.join(candidate, 'SHA256SUMS')), beforeManifest);
  receipt.complete = true;
} catch (error) {
  receipt.failure = String(error.stack || error);
  if (ownsInstallation) {
    receipt.unitStatus = run('systemctl', ['show', 'mdd-agent.service', '-p', 'ActiveState', '-p', 'SubState', '-p', 'Result', '-p', 'NRestarts']).stdout;
    receipt.hostedJournal = run('journalctl', ['-u', 'mdd-agent.service', '-u', 'mdd-cellular-guard.service', '--no-pager', '-n', '60']).stdout;
  }
  process.exitCode = 1;
} finally {
  try {
    removeInstallation();
    // Only this disposable runner's fixture directories are removed.
    fs.rmSync(root, { recursive: true, force: true });
  } catch (error) {
    receipt.cleanupFailure = String(error.stack || error);
    receipt.complete = false; process.exitCode = 1;
  } finally {
    try {
      if (optFD !== null && optOriginal !== null) {
        if (optPrepared) fs.fchmodSync(optFD, optOriginal.mode & 0o7777);
        const restored = fs.fstatSync(optFD);
        const visible = fs.lstatSync('/opt');
        assert.equal(visible.dev, optOriginal.dev); assert.equal(visible.ino, optOriginal.ino);
        assert(!visible.isSymbolicLink());
        assert.equal(restored.mode, optOriginal.mode);
        assert.equal(restored.uid, optOriginal.uid); assert.equal(restored.gid, optOriginal.gid);
        receipt.hostOptRestoration = { prepared: optPrepared, restored: true,
          uid: restored.uid, gid: restored.gid, mode: restored.mode & 0o7777, dev: restored.dev, ino: restored.ino };
      }
    } catch (error) {
      receipt.restoreFailure = String(error.stack || error);
      receipt.complete = false; process.exitCode = 1;
    } finally {
      if (optFD !== null) fs.closeSync(optFD);
      fs.writeFileSync(output, JSON.stringify(receipt, null, 2) + '\n');
    }
  }
}
console.log(JSON.stringify({ complete: receipt.complete, checks: receipt.checks, failure: receipt.failure }, null, 2));
