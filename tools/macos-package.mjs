import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { parseArgs } from 'node:util';
import { fileURLToPath } from 'node:url';

const [mode, ...args] = process.argv.slice(2);
const { values } = parseArgs({ args, options: Object.fromEntries(
  ['package', 'arch', 'arm64', 'amd64', 'output', 'identity', 'team-id'].map(name => [name, { type: 'string' }])) });
assert.equal(process.platform, 'darwin', 'macOS packaging requires Apple tools');
assert(['verify', 'combine'].includes(mode), 'expected verify or combine');
const arches = { arm64: ['arm64'], amd64: ['x86_64'], universal: ['arm64', 'x86_64'] };
const app = 'MDD Agent.app';
const entitlements = fileURLToPath(new URL('../go-runtime/release/macos-agent.entitlements', import.meta.url));
function command(bin, argv, input) {
  const result = spawnSync(bin, argv, { input, encoding: 'utf8', timeout: 90000, maxBuffer: 8 << 20 });
  assert(!result.error && result.status === 0, `${bin} failed: ${result.error || result.stderr}`);
  return { stdout: result.stdout, text: result.stdout + result.stderr };
}
function sha(file) { return createHash('sha256').update(fs.readFileSync(file)).digest('hex'); }
function files(root, relative = '') {
  return fs.readdirSync(path.join(root, relative)).sort().flatMap(name => {
    const entry = path.join(relative, name), stat = fs.lstatSync(path.join(root, entry));
    assert(!stat.isSymbolicLink(), `unexpected package symlink: ${entry}`);
    if (stat.isDirectory()) return files(root, entry);
    assert(stat.isFile(), `unexpected package entry: ${entry}`);
    return [entry];
  });
}
function fields(text) {
  const result = {};
  for (const line of text.trim().split('\n')) {
    const match = /^([a-z_]+)=(.*)$/.exec(line);
    assert(match && !Object.hasOwn(result, match[1]), 'invalid or duplicate BUILD field');
    result[match[1]] = match[2];
  }
  return result;
}
function plist(file) { return JSON.parse(command('plutil', ['-convert', 'json', '-o', '-', file]).stdout); }
function components(info) {
  assert(/^[\w .-]+$/.test(info.CFBundleExecutable) && !['.', '..'].includes(info.CFBundleExecutable));
  return ['mdd-agent', 'mdd-cellular-io', 'mdd-call-audio-helper',
    `${app}/Contents/MacOS/${info.CFBundleExecutable}`,
    `${app}/Contents/MacOS/mdd-cellular-io`, `${app}/Contents/MacOS/mdd-call-audio-helper`];
}
function manifest(root) {
  const list = files(root).filter(name => name !== 'SHA256SUMS');
  fs.writeFileSync(path.join(root, 'SHA256SUMS'), list.map(name => `${sha(path.join(root, name))}  ./${name}\n`).join(''));
}
function isMachO(file) {
  const fd = fs.openSync(file, 'r'), bytes = Buffer.alloc(4);
  try { fs.readSync(fd, bytes); } finally { fs.closeSync(fd); }
  return ['cafebabe', 'bebafeca', 'cafebabf', 'bfbafeca', 'feedface', 'cefaedfe', 'feedfacf', 'cffaedfe'].includes(bytes.toString('hex'));
}
function signature(file, arch, build, audio) {
  command('codesign', ['--verify', '--strict', '--all-architectures', file]);
  const text = command('codesign', ['--display', '--architecture', arch, '--verbose=4', '-r-', file]).text;
  const identifier = /^Identifier=(.+)$/m.exec(text)?.[1];
  // Apple TN2206 documents the '# ' prefix for a synthesized requirement.
  const requirement = /^(?:# )?designated => (.+)$/m.exec(text)?.[1];
  assert(identifier && requirement, `missing code identity: ${path.basename(file)}/${arch} (identifier=${!!identifier}, requirement=${!!requirement})`);
  if (build.signing === 'developer-id') {
    assert.equal(/^TeamIdentifier=(.+)$/m.exec(text)?.[1], '8WPJLUNLY8');
    assert(/^Authority=Developer ID Application:/m.test(text) && /^Timestamp=/m.test(text));
    assert(/^CodeDirectory .*flags=.*\(runtime\)/m.test(text), 'hardened runtime missing');
  } else {
    assert.equal(build.signing, 'ad-hoc-development');
    assert(/^Signature=adhoc$/m.test(text), 'development package must have an ad-hoc signature');
  }
  const xml = command('codesign', ['--display', '--architecture', arch, '--entitlements', ':-', file]).stdout.trim();
  const rights = xml ? JSON.parse(command('plutil', ['-convert', 'json', '-o', '-', '-'], xml).stdout) : {};
  assert.deepEqual(rights, audio ? { 'com.apple.security.device.audio-input': true } : {}, 'unexpected component entitlements');
  return { identifier, requirement };
}
function verify(root, expected) {
  assert(root && path.isAbsolute(root), 'package path must be absolute');
  const list = files(root), sums = new Map();
  for (const line of fs.readFileSync(path.join(root, 'SHA256SUMS'), 'utf8').trim().split('\n')) {
    const match = /^([a-f0-9]{64})  \.\/(.+)$/.exec(line);
    assert(match && list.includes(match[2]) && !sums.has(match[2]), 'invalid checksum member');
    assert.equal(sha(path.join(root, match[2])), match[1], `checksum mismatch: ${match[2]}`);
    sums.set(match[2], match[1]);
  }
  assert.deepEqual([...sums.keys()].sort(), list.filter(name => name !== 'SHA256SUMS').sort(), 'incomplete checksum set');
  const build = fields(fs.readFileSync(path.join(root, 'BUILD.txt'), 'utf8'));
  assert(/^[a-f0-9]{40}$/.test(build.source_revision) && /^[a-f0-9]{40}$/.test(build.source_tree_hash));
  assert.equal(build.source_tree, 'clean');
  assert.equal(build.minimum_macos, '15.0');
  assert.equal(build.notarization, 'not-performed');
  assert.equal(build.team_identifier, build.signing === 'developer-id' ? '8WPJLUNLY8' : 'not-set');
  const architecture = expected || build.architecture;
  assert(Object.hasOwn(arches, architecture), 'unsupported package architecture');
  const info = plist(path.join(root, app, 'Contents/Info.plist'));
  assert.equal(info.CFBundleIdentifier, 'com.mdd.agent');
  assert.equal(info.CFBundleShortVersionString, build.version);
  assert.equal(String(info.CFBundleVersion), build.build);
  assert.equal(info.LSMinimumSystemVersion, '15.0');
  assert(info.NSMicrophoneUsageDescription);
  const code = components(info), signatures = {};
  assert.deepEqual(list.filter(name => isMachO(path.join(root, name))).sort(), [...code].sort(), 'unrecognized or missing Mach-O component');
  for (const name of code) {
    const file = path.join(root, name);
    assert(fs.statSync(file).mode & 0o111, `non-executable component: ${name}`);
    assert.deepEqual(command('lipo', ['-archs', file]).stdout.trim().split(/\s+/).sort(), arches[architecture], `architecture mismatch: ${name}`);
    signatures[name] = {};
    for (const arch of arches[architecture]) {
      const dependencies = command('otool', ['-arch', arch, '-L', file]).stdout.trim().split('\n').slice(1);
      assert(dependencies.length > 0 && dependencies.every(line => /^\s+(\/usr\/lib\/|\/System\/Library\/)/.test(line)), `non-system dependency: ${name}/${arch}`);
      assert(!dependencies.some(line => /libusb/i.test(line)), 'libusb must remain static');
      const loads = command('otool', ['-arch', arch, '-l', file]).stdout.split(/Load command \d+/);
      const version = loads.flatMap(load => {
        if (/cmd LC_BUILD_VERSION\b/.test(load)) return [/\bminos (\d+(?:\.\d+)*)/.exec(load)?.[1]];
        if (/cmd LC_VERSION_MIN_MACOSX\b/.test(load)) return [/\bversion (\d+(?:\.\d+)*)/.exec(load)?.[1]];
        return [];
      });
      assert.equal(version.length, 1, 'missing/ambiguous macOS load target');
      const numbers = version[0]?.split('.').map(Number);
      assert(numbers && numbers[0] >= 10 && (numbers[0] < 15 || (numbers[0] === 15 && numbers.slice(1).every(n => n === 0))), 'component exceeds macOS 15.0 minimum');
      if (name.endsWith('mdd-cellular-io')) assert.equal(numbers[0], 15, 'cellular helper minimum changed');
      signatures[name][arch] = signature(file, arch, build, !name.endsWith('mdd-cellular-io'));
    }
  }
  assert.equal(build.architecture, architecture, 'BUILD architecture disagrees with package');
  command('codesign', ['--verify', '--deep', '--strict', '--all-architectures', path.join(root, app)]);
  return { build, info, code, signatures, files: list };
}

if (mode === 'verify') {
  const result = verify(values.package, values.arch);
  console.log(JSON.stringify({ source: result.build.source_revision, architecture: result.build.architecture, components: result.code.length, signing: result.build.signing }));
} else {
  const arm = verify(values.arm64, 'arm64'), intel = verify(values.amd64, 'amd64');
  assert(values.output && path.isAbsolute(values.output) && !fs.existsSync(values.output), 'output must be a new absolute path');
  assert.deepEqual({ ...arm.build, architecture: '' }, { ...intel.build, architecture: '' }, 'input build/source/signing mismatch');
  assert.deepEqual(arm.info, intel.info, 'App metadata mismatch');
  for (const name of arm.code) {
    const a = arm.signatures[name].arm64, b = intel.signatures[name].x86_64;
    assert.equal(a.identifier, b.identifier, 'component identifier mismatch');
    if (arm.build.signing === 'developer-id') assert.equal(a.requirement, b.requirement, 'designated requirement mismatch');
  }
  const ignore = name => arm.code.includes(name) || ['BUILD.txt', 'SHA256SUMS'].includes(name) || name.startsWith(`${app}/Contents/_CodeSignature/`);
  for (const name of new Set([...arm.files, ...intel.files])) {
    if (ignore(name)) continue;
    const a = path.join(values.arm64, name), b = path.join(values.amd64, name);
    if (fs.existsSync(a) && fs.existsSync(b)) assert.equal(sha(a), sha(b), `resource mismatch: ${name}`);
    else assert(name.startsWith('licenses/'), `missing shared resource: ${name}`);
  }
  const signed = arm.build.signing === 'developer-id';
  assert(signed ? values.identity && values.identity !== '-' && values['team-id'] === '8WPJLUNLY8' : !values.identity && !values['team-id'], 'signing mode/identity mismatch');
  const stage = fs.mkdtempSync(path.join(path.dirname(values.output), '.mdd-universal-'));
  const target = path.join(stage, 'package');
  try {
    fs.cpSync(values.arm64, target, { recursive: true, errorOnExist: true, force: false });
    for (const name of intel.files.filter(name => name.startsWith('licenses/') && !arm.files.includes(name))) {
      fs.mkdirSync(path.dirname(path.join(target, name)), { recursive: true });
      fs.copyFileSync(path.join(values.amd64, name), path.join(target, name));
    }
    for (const name of arm.code) {
      const file = path.join(target, name);
      command('lipo', ['-create', path.join(values.arm64, name), path.join(values.amd64, name), '-output', file]);
      fs.chmodSync(file, 0o755);
      const sign = ['--force', '--sign', signed ? values.identity : '-', '--identifier', arm.signatures[name].arm64.identifier];
      if (signed) sign.push('--timestamp', '--options', 'runtime');
      if (!name.endsWith('mdd-cellular-io')) sign.push('--entitlements', entitlements);
      command('codesign', [...sign, file]);
    }
    const sign = ['--force', '--sign', signed ? values.identity : '-', '--entitlements', entitlements];
    if (signed) sign.push('--timestamp', '--options', 'runtime');
    command('codesign', [...sign, path.join(target, app)]);
    const build = { ...arm.build, architecture: 'universal' };
    fs.writeFileSync(path.join(target, 'BUILD.txt'), Object.entries(build).map(([key, value]) => `${key}=${value}\n`).join(''));
    manifest(target);
    const result = verify(target, 'universal');
    for (const name of arm.code) for (const arch of arches.universal) {
      assert.equal(result.signatures[name][arch].identifier, arm.signatures[name].arm64.identifier);
      if (signed) assert.equal(result.signatures[name][arch].requirement, arm.signatures[name].arm64.requirement);
    }
    assert(!fs.existsSync(values.output), 'output appeared during packaging');
    fs.renameSync(target, values.output);
    console.log(JSON.stringify({ source: build.source_revision, architecture: build.architecture, components: arm.code.length, signing: build.signing }));
  } finally { fs.rmSync(stage, { recursive: true, force: true }); }
}
