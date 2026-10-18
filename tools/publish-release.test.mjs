import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

// Exercise the shipped CLI, real Git/tars/files and a persisted external API.
// These fixtures never build software or contact GitHub/production.
const publisher = path.resolve(process.env.RELEASE_TEST_PUBLISHER || fileURLToPath(new URL('./publish-release.mjs', import.meta.url)));
const root = fs.mkdtempSync(path.join(os.tmpdir(), 'mdd-release-contract-'));
const hash = bytes => createHash('sha256').update(bytes).digest('hex');
function command(bin, args, cwd) {
  const result = spawnSync(bin, args, { cwd, encoding: 'utf8', timeout: 20000 });
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim();
}
function put(file, bytes) { fs.mkdirSync(path.dirname(file), { recursive: true }); fs.writeFileSync(file, bytes); }
const git = path.join(root, 'repo');
fs.mkdirSync(git);
put(path.join(git, 'VERSION'), '2.0.0\n');
command('git', ['init', '-q', '-b', 'main'], git);
command('git', ['add', '.'], git);
command('git', ['-c', 'user.name=Release Test', '-c', 'user.email=release@example.invalid', 'commit', '-qm', 'fixture'], git);
const sha = command('git', ['rev-parse', 'HEAD'], git), tree = command('git', ['rev-parse', 'HEAD^{tree}'], git);
for (const tag of ['v2.0.0', 'android-preview-v2.0.0']) command('git', ['-c', 'user.name=Release Test', '-c', 'user.email=release@example.invalid', 'tag', '-a', tag, '-m', tag], git);
command('git', ['update-ref', 'refs/remotes/origin/main', sha], git);
const bin = path.join(root, 'bin');
fs.mkdirSync(bin);
fs.copyFileSync(new URL('./fixtures/release-github.mjs', import.meta.url), path.join(bin, 'gh'));
fs.chmodSync(path.join(bin, 'gh'), 0o755);
let count = 0;
function fixture(kind, change = () => {}) {
  const dir = path.join(root, String(++count)); fs.mkdirSync(dir);
  const storage = path.join(dir, 'remote'); fs.mkdirSync(storage);
  const state = { sha, storage, releases: {}, writes: [], nextAssetID: 1000, latest: 'v1.3.14' };
  const tag = kind === 'go' ? 'v2.0.0' : 'android-preview-v2.0.0';
  const stateFile = path.join(dir, 'state.json');
  const ctx = { dir, state, tag, kind, versionCode: 200 };
  const tar = (folder, name, members) => {
    const payload = path.join(dir, 'input', folder); fs.mkdirSync(payload, { recursive: true });
    for (const [member, bytes] of Object.entries(members)) put(path.join(payload, member), bytes);
    fs.mkdirSync(path.join(dir, folder), { recursive: true });
    command('tar', ['-cf', path.join(dir, folder, name), ...Object.keys(members)], payload);
  };
  ctx.tar = tar;
  if (kind === 'go') {
    tar('linux', `mdd-${sha.slice(0, 12)}-linux-amd64.tar`, { [`mdd-${sha.slice(0, 12)}/manifest.json`]: JSON.stringify({ source_revision: sha, os: 'linux', architecture: 'amd64' }) });
    for (const [folder, platform, arch] of [['arm64', 'Linux', 'arm64'], ['windows', 'Windows', 'amd64'], ['macos', 'macOS', 'arm64']]) {
      const name = `MDD-Agent-${platform}-${arch}`;
      const build = `source_revision=${sha}\nsource_tree=${tree}\nsource_tree_hash=${tree}\nplatform=linux/arm64\narchitecture=${arch}\nversion=2.0.0\nteam_identifier=8WPJLUNLY8\nsigning=developer-id\n`;
      tar(folder, `${name}.tar.gz`, { [`${name}/BUILD.txt`]: build, [`${name}/MODEM_AGENT.md`]: 'fixture guide', [`${name}/install-${platform === 'Windows' ? 'windows-agent.ps1' : 'macos-agent.sh'}`]: 'fixture installer' });
    }
    const previewTag = 'android-preview-v2.0.0';
    const preview = { id: 1, tag_name: previewTag, draft: false, prerelease: true, assets: [] };
    const files = {};
    for (const name of ['README.md', 'mdd-agent-preview.apk', 'package.txt', 'signature.txt', 'source.txt']) {
      const bytes = Buffer.from(`preview fixture ${name}`);
      put(path.join(storage, `${previewTag}--${name}`), bytes);
      files[name] = { sha256: hash(bytes), size: bytes.length };
      preview.assets.push({ name, state: 'uploaded', size: bytes.length, digest: `sha256:${hash(bytes)}` });
    }
    const bytes = Buffer.from(JSON.stringify({ schema: 1, kind: 'android', tag: previewTag, version: '2.0.0', source_revision: sha, source_tree: tree, version_code: 200, files }));
    put(path.join(storage, `${previewTag}--RELEASE.json`), bytes);
    preview.assets.push({ name: 'RELEASE.json', state: 'uploaded', size: bytes.length, digest: `sha256:${hash(bytes)}` });
    const sums = Buffer.from([...Object.keys(files), 'RELEASE.json'].sort().map(name => `${name === 'RELEASE.json' ? hash(bytes) : files[name].sha256}  ${name}\n`).join(''));
    put(path.join(storage, `${previewTag}--SHA256SUMS`), sums);
    preview.assets.push({ name: 'SHA256SUMS', state: 'uploaded', size: sums.length, digest: `sha256:${hash(sums)}` });
    state.releases[previewTag] = preview;
  } else {
    const evidence = path.join(dir, 'android', 'android-evidence');
    ctx.evidence = evidence;
    put(path.join(evidence, 'source.txt'), `${sha}\n${tree}\n`);
    put(path.join(evidence, 'package.txt'), "package: name='com.lovitus.mddagent.preview' versionCode='200' versionName='2.0.0-preview'\n");
    put(path.join(evidence, 'signature.txt'), 'Signer #1 certificate SHA-256 digest: 8b5e818fccfa6f6738cd53658b2df9ab57a014f9a5269decc4104d91894dd5e2\n');
    put(path.join(evidence, 'README.md'), 'fixture guide');
    put(path.join(evidence, 'mdd-agent-preview.apk'), 'fixture APK, not an executable');
    put(path.join(evidence, 'SHA256SUMS'), `${hash(fs.readFileSync(path.join(evidence, 'mdd-agent-preview.apk')))}  mdd-agent-preview.apk\n`);
  }
  change(ctx);
  put(stateFile, JSON.stringify(state));
  ctx.run = () => {
    fs.rmSync(path.join(dir, `publish-${kind}`), { recursive: true, force: true });
    return spawnSync(process.execPath, [publisher, kind], { cwd: git, encoding: 'utf8', timeout: 30000, env: {
      ...process.env, PATH: `${bin}${path.delimiter}${process.env.PATH}`, RELEASE_ROOT: dir,
      RELEASE_TEST_STATE: stateFile, GITHUB_REPOSITORY: 'fixture/repo', GITHUB_REF: `refs/tags/${tag}`,
      GITHUB_SHA: sha, GITHUB_RUN_ID: '123', GITHUB_RUN_NUMBER: String(ctx.versionCode), GITHUB_EVENT_NAME: 'push',
    } });
  };
  ctx.read = () => JSON.parse(fs.readFileSync(stateFile));
  ctx.write = value => put(stateFile, JSON.stringify(value));
  return ctx;
}
try {
  for (const kind of ['go', 'android']) {
    const test = fixture(kind); const result = test.run();
    assert.equal(result.status, 0, result.stderr);
    assert.equal(test.read().releases[test.tag].draft, false);
    assert.equal(test.read().latest, kind === 'go' ? test.tag : 'v1.3.14');
    if (kind === 'go') {
      const published = path.join(test.dir, 'publish-go');
      assert.deepEqual(fs.readFileSync(path.join(published, 'mdd-2.0.0-linux-amd64.tar')), fs.readFileSync(path.join(published, `mdd-${sha.slice(0, 12)}-linux-amd64.tar`)));
    }
    const writes = test.read().writes;
    assert.equal(test.run().status, 0, 'exact public rerun must be read-only');
    assert.deepEqual(test.read().writes, writes);
  }
  const failures = [
    ['go', 'missing package', c => fs.rmSync(path.join(c.dir, 'windows'), { recursive: true })],
    ['go', 'wrong source', c => c.tar('linux', `mdd-${sha.slice(0, 12)}-linux-amd64.tar`, { [`mdd-${sha.slice(0, 12)}/manifest.json`]: JSON.stringify({ source_revision: 'wrong', os: 'linux', architecture: 'amd64' }) })],
    ['go', 'wrong Mac signing identity', c => c.tar('macos', 'MDD-Agent-macOS-arm64.tar.gz', { 'MDD-Agent-macOS-arm64/BUILD.txt': `source_revision=${sha}\nsource_tree_hash=${tree}\narchitecture=arm64\nversion=2.0.0\nteam_identifier=wrong\nsigning=developer-id\n`, 'MDD-Agent-macOS-arm64/MODEM_AGENT.md': 'fixture guide', 'MDD-Agent-macOS-arm64/install-macos-agent.sh': 'fixture installer' })],
    ['go', 'draft preview', c => { c.state.releases['android-preview-v2.0.0'].draft = true; }],
    ['go', 'missing preview checksum manifest', c => { const r = c.state.releases['android-preview-v2.0.0']; r.assets = r.assets.filter(x => x.name !== 'SHA256SUMS'); }],
    ['go', 'wrong preview checksum contents', c => {
      const tag = 'android-preview-v2.0.0', bytes = Buffer.from('incorrect manifest\n');
      put(path.join(c.state.storage, `${tag}--SHA256SUMS`), bytes);
      Object.assign(c.state.releases[tag].assets.find(x => x.name === 'SHA256SUMS'), { size: bytes.length, digest: `sha256:${hash(bytes)}` });
    }],
    ['go', 'remote tag changed', c => { c.state.remoteSHA = 'a'.repeat(40); }],
    ['go', 'corrupt download', c => { c.state.corruptDownload = 'mdd-agent-preview.apk'; }],
    ['go', 'newer stable exists', c => { c.state.latest = 'v2.1.0'; }],
    ['android', 'wrong signer', c => put(path.join(c.evidence, 'signature.txt'), 'wrong signer')],
    ['android', 'APK hash mismatch', c => put(path.join(c.evidence, 'mdd-agent-preview.apk'), 'changed')],
    ['android', 'debug package', c => fs.appendFileSync(path.join(c.evidence, 'package.txt'), 'application-debuggable\n')],
    ['android', 'old versionCode', c => { c.versionCode = 178; put(path.join(c.evidence, 'package.txt'), "package: name='com.lovitus.mddagent.preview' versionCode='178' versionName='2.0.0-preview'\n"); }],
    ['android', 'newer preview exists', c => {
      const tag = 'android-preview-v1.9.0';
      const bytes = Buffer.from("package: name='com.lovitus.mddagent.preview' versionCode='201' versionName='1.9.0-preview'\n");
      put(path.join(c.state.storage, `${tag}--package.txt`), bytes);
      c.state.releases[tag] = { tag_name: tag, draft: false, prerelease: true, assets: [{ name: 'package.txt', state: 'uploaded', size: bytes.length, digest: `sha256:${hash(bytes)}` }] };
    }],
    ['android', 'lookup error is not absence', c => { c.state.lookupError = true; }],
  ];
  for (const [kind, name, change] of failures) {
    const test = fixture(kind, change), result = test.run();
    assert.notEqual(result.status, 0, `${name} must fail`);
    assert(!test.read().writes.includes('publish'), `${name} must not publish`);
  }
  for (const kind of ['go', 'android']) {
    const test = fixture(kind, c => { c.state.interruptUpload = true; c.state.losePublishResponse = true; });
    assert.notEqual(test.run().status, 0);
    assert.equal(test.read().releases[test.tag].draft, true);
    assert.equal(test.run().status, 0, 'partial draft must resume and reconcile lost publish response');
    assert.equal(test.read().writes.filter(x => x === 'create').length, 1);
    assert.equal(test.read().writes.filter(x => x === 'publish').length, 1);
    const state = test.read(); state.releases[test.tag].assets[0].digest = `sha256:${'0'.repeat(64)}`; test.write(state);
    assert.notEqual(test.run().status, 0, 'different public content must never be overwritten');
    assert.deepEqual(test.read().writes, state.writes);
    const starter = fixture(kind, c => { c.state.leaveStarter = true; c.state.loseCreateResponse = true; });
    assert.notEqual(starter.run().status, 0);
    const initial = starter.read();
    assert.equal(initial.releases[starter.tag].draft, true);
    assert.equal(initial.releases[starter.tag].assets[0].state, 'starter');
    assert.equal(starter.run().status, 0, 'empty starter must recover on the original draft');
    assert.equal(starter.read().writes.filter(x => x === 'create').length, 1);
    assert.equal(starter.read().writes.filter(x => x.startsWith('delete-starter:')).length, 1);
  }
  console.log(`Release CLI: complete source-bound Go/Android publication, identical Linux alias, ${failures.length} rejection cases, interrupted draft resume and read-only repetition passed`);
} finally { fs.rmSync(root, { recursive: true, force: true }); }
