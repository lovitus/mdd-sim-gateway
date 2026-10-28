import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';

// Both existing workflows use this bounded draft/upload/readback/publish path.
// No asset is overwritten, and an interrupted draft is resumed only byte-for-byte.
const [kind, mode] = process.argv.slice(2);
assert(['go', 'android'].includes(kind), 'expected go or android');
assert(!mode || mode === '--prepare-only', 'unknown release mode');
const root = path.resolve(process.env.RELEASE_ROOT || '');
assert(process.env.RELEASE_ROOT, 'RELEASE_ROOT is required');
const repo = process.env.GITHUB_REPOSITORY;
assert(/^[\w.-]+\/[\w.-]+$/.test(repo || ''), 'invalid repository');
function command(bin, args, timeout = 25000) {
  const result = spawnSync(bin, args, { encoding: 'utf8', timeout, maxBuffer: 16 << 20 });
  assert(!result.error && result.status === 0, `${bin} failed: ${result.error || result.stderr}`);
  return result.stdout.trim();
}
const sha = command('git', ['rev-parse', 'HEAD']);
const tree = command('git', ['rev-parse', 'HEAD^{tree}']);
const version = fs.readFileSync('VERSION', 'utf8').trim();
assert(/^\d+\.\d+\.\d+$/.test(version), 'invalid VERSION');
const tag = kind === 'go' ? `v${version}` : `android-preview-v${version}`;
if (!mode) {
  assert.equal(process.env.GITHUB_REF, `refs/tags/${tag}`, 'only the exact version tag can publish');
  assert.equal(process.env.GITHUB_SHA, sha, 'checkout differs from tagged source');
  assert.equal(command('git', ['rev-parse', `${tag}^{commit}`]), sha);
  command('git', ['merge-base', '--is-ancestor', sha, 'origin/main']);
}
function digest(file) {
  const hash = createHash('sha256');
  const fd = fs.openSync(file, 'r');
  const buffer = Buffer.alloc(1024 * 1024);
  try { for (let n; (n = fs.readSync(fd, buffer)) > 0;) hash.update(buffer.subarray(0, n)); }
  finally { fs.closeSync(fd); }
  return hash.digest('hex');
}
function one(directory, predicate) {
  const names = fs.readdirSync(path.join(root, directory)).filter(predicate);
  assert.equal(names.length, 1, `expected exactly one asset in ${directory}`);
  return path.join(root, directory, names[0]);
}
function tarText(file, member) { return command('tar', ['-xOf', file, member]); }
function fields(text) {
  return Object.fromEntries(text.split('\n').filter(x => x.includes('=')).map(x => {
    const split = x.indexOf('='); return [x.slice(0, split), x.slice(split + 1)];
  }));
}
const directory = path.join(root, `publish-${kind}`);
fs.mkdirSync(directory, { recursive: true });
assert.equal(fs.readdirSync(directory).length, 0, 'local staging directory must be empty');
const files = {};
function add(source, name = path.basename(source)) {
  assert(!files[name] && path.basename(name) === name, 'duplicate or invalid asset name');
  fs.copyFileSync(source, path.join(directory, name));
  files[name] = { sha256: digest(source), size: fs.statSync(source).size };
}
let versionCode;
if (kind === 'go') {
  const linux = one('linux', x => /^mdd-[a-f0-9]{12}-linux-amd64\.tar$/.test(x));
  const manifestPath = command('tar', ['-tf', linux]).split('\n').filter(x => /^mdd-[a-f0-9]{12}\/manifest.json$/.test(x));
  assert.equal(manifestPath.length, 1, 'Linux manifest missing');
  const manifest = JSON.parse(tarText(linux, manifestPath[0]));
  assert.equal(manifest.source_revision, sha);
  assert.equal(manifest.os, 'linux');
  assert.equal(manifest.architecture, 'amd64');
  assert(fs.statSync(linux).size <= (512 << 20), 'Linux archive exceeds updater limit');
  add(linux);
  add(linux, `mdd-${version}-linux-amd64.tar`);
  for (const [folder, platform, architecture] of [['arm64', 'Linux', 'arm64'], ['windows', 'Windows', 'amd64'], ['macos', 'macOS', 'arm64'], ['macos-universal', 'macOS', 'universal']]) {
    const name = `MDD-Agent-${platform}-${architecture}`;
    const archive = one(folder, x => x === `${name}.tar.gz`);
    const build = fields(tarText(archive, `${name}/BUILD.txt`));
    assert.equal(build.source_revision, sha, `${platform} source mismatch`);
    assert.equal(platform === 'Linux' ? build.source_tree : build.source_tree_hash, tree, `${platform} tree mismatch`);
    assert.equal(platform === 'Linux' ? build.platform : build.architecture, platform === 'Linux' ? 'linux/arm64' : architecture);
    if (architecture === 'universal') {
      assert.equal(build.minimum_macos, '15.0');
      assert.equal(build.source_tree, 'clean');
    }
    if (platform === 'macOS' && !(mode && process.env.GITHUB_EVENT_NAME === 'pull_request')) {
      assert.equal(build.team_identifier, '8WPJLUNLY8');
      assert.equal(build.signing, 'developer-id');
    }
    if (platform !== 'Linux') {
      assert.equal(build.version, version);
      assert(tarText(archive, `${name}/MODEM_AGENT.md`).length > 0);
      assert(tarText(archive, `${name}/install-${platform === 'Windows' ? 'windows-agent.ps1' : 'macos-agent.sh'}`).length > 0);
    }
    add(archive);
  }
} else {
  const evidence = path.join(root, 'android', 'android-evidence');
  const source = fs.readFileSync(path.join(evidence, 'source.txt'), 'utf8').trim().split('\n');
  assert.deepEqual(source, [sha, tree], 'Android source mismatch');
  const pkg = fs.readFileSync(path.join(evidence, 'package.txt'), 'utf8');
  const identity = pkg.match(/^package: name='([^']+)' versionCode='(\d+)' versionName='([^']+)'/m);
  assert(identity, 'APK identity missing');
  assert.equal(identity[1], 'com.lovitus.mddagent.preview');
  versionCode = Number(identity[2]);
  assert.equal(versionCode, Number(process.env.GITHUB_RUN_NUMBER));
  assert(versionCode > 178, 'APK would not upgrade the known delivered preview');
  assert.equal(identity[3], `${version}-preview`);
  assert(!pkg.includes('application-debuggable'), 'debug APK cannot be published');
  assert(fs.readFileSync(path.join(evidence, 'signature.txt'), 'utf8').includes('Signer #1 certificate SHA-256 digest: 8b5e818fccfa6f6738cd53658b2df9ab57a014f9a5269decc4104d91894dd5e2'), 'wrong Android signer');
  const apk = path.join(evidence, 'mdd-agent-preview.apk');
  assert.equal(fs.readFileSync(path.join(evidence, 'SHA256SUMS'), 'utf8').trim(), `${digest(apk)}  mdd-agent-preview.apk`);
  for (const name of ['mdd-agent-preview.apk', 'signature.txt', 'package.txt', 'source.txt', 'README.md']) add(path.join(evidence, name));
}
const metadata = { schema: 1, kind, tag, source_revision: sha, source_tree: tree, version, version_code: versionCode, workflow_run: process.env.GITHUB_RUN_ID, files };
fs.writeFileSync(path.join(directory, 'RELEASE.json'), `${JSON.stringify(metadata, null, 2)}\n`);
fs.writeFileSync(path.join(directory, 'SHA256SUMS'), [...Object.keys(files), 'RELEASE.json'].sort().map(name => `${digest(path.join(directory, name))}  ${name}\n`).join(''));
const expected = fs.readdirSync(directory).sort();
if (mode) {
  console.log(`Prepared ${tag}: ${expected.length} assets; source=${sha}; tree=${tree}; no remote write`);
  process.exit(0);
}
const api = (suffix) => JSON.parse(command('gh', ['api', `repos/${repo}/${suffix}`]));
function verifyRemoteTag(name) {
  let object = api(`git/ref/tags/${name}`).object;
  for (let depth = 0; object.type === 'tag' && depth < 3; depth++) object = api(`git/tags/${object.sha}`).object;
  assert(object.type === 'commit' && object.sha === sha, `remote ${name} no longer matches approved source`);
}
verifyRemoteTag(tag);
function compareVersion(a, b) {
  assert(/^v\d+\.\d+\.\d+$/.test(a) && /^v\d+\.\d+\.\d+$/.test(b), 'unexpected release version');
  const x = a.slice(1).split('.').map(Number), y = b.slice(1).split('.').map(Number);
  return x[0] - y[0] || x[1] - y[1] || x[2] - y[2];
}
const releaseIDs = new Map();
function releaseFor(name) {
  // Like gh's FetchRelease: pending draft tags require GraphQL discovery,
  // then REST by database ID. The published-tag endpoint omits drafts.
  const [owner, repository] = repo.split('/');
  const lookup = JSON.parse(command('gh', ['api', 'graphql', '-f', 'query=query($owner:String!,$name:String!,$tag:String!){repository(owner:$owner,name:$name){release(tagName:$tag){databaseId}}}', '-f', `owner=${owner}`, '-f', `name=${repository}`, '-f', `tag=${name}`]));
  assert(!lookup.errors && lookup.data?.repository, 'release discovery failed, not absence');
  const id = lookup.data.repository.release?.databaseId;
  if (id == null) {
    assert(!releaseIDs.has(name), 'previously observed release disappeared');
    return null;
  }
  assert(Number.isSafeInteger(id) && id > 0, 'invalid release ID');
  assert(!releaseIDs.has(name) || releaseIDs.get(name) === id, 'release identity changed');
  releaseIDs.set(name, id);
  const release = api(`releases/${id}`);
  assert.equal(release.id, id);
  assert.equal(release.tag_name, name);
  return release;
}
function downloadAsset(release, asset, destination) {
  assert.equal(asset.state, 'uploaded', 'asset upload is not complete');
  command('gh', ['release', 'download', release.tag_name, '--repo', repo, '--pattern', asset.name, '--dir', destination], 120000);
  const local = path.join(destination, asset.name);
  assert.equal(asset.size, fs.statSync(local).size, 'remote asset size mismatch');
  assert.equal(asset.digest, `sha256:${digest(local)}`, 'remote asset digest mismatch');
  return local;
}
const scratch = fs.mkdtempSync(path.join(os.tmpdir(), 'mdd-release-verify-'));
let release = releaseFor(tag);
function readAsset(release, name) {
  const matches = release.assets.filter(x => x.name === name);
  assert.equal(matches.length, 1, `missing/duplicate ${name} in ${release.tag_name}`);
  const target = fs.mkdtempSync(path.join(scratch, 'read-'));
  return fs.readFileSync(downloadAsset(release, matches[0], target), 'utf8');
}
if (kind === 'go') {
  const previewTag = `android-preview-v${version}`;
  assert.equal(command('git', ['rev-parse', `${previewTag}^{commit}`]), sha, 'preview tag source mismatch');
  verifyRemoteTag(previewTag);
  const preview = releaseFor(previewTag);
  assert(preview && !preview.draft && preview.prerelease, 'matching public Android preview required');
  const recordText = readAsset(preview, 'RELEASE.json');
  const record = JSON.parse(recordText);
  assert.equal(record.kind, 'android');
  assert.equal(record.tag, previewTag);
  assert.equal(record.source_revision, sha);
  assert.equal(record.source_tree, tree);
  assert(record.version_code > 178);
  assert.deepEqual(Object.keys(record.files).sort(), ['README.md', 'mdd-agent-preview.apk', 'package.txt', 'signature.txt', 'source.txt']);
  assert.equal(record.schema, 1);
  assert.equal(record.version, version);
  assert.deepEqual(preview.assets.map(x => x.name).sort(), [...Object.keys(record.files), 'RELEASE.json', 'SHA256SUMS'].sort(), 'preview asset set incomplete');
  const previewChecksums = [...Object.keys(record.files), 'RELEASE.json'].sort().map(name => `${name === 'RELEASE.json' ? createHash('sha256').update(recordText).digest('hex') : record.files[name].sha256}  ${name}\n`).join('');
  assert.equal(readAsset(preview, 'SHA256SUMS'), previewChecksums, 'preview checksum manifest mismatch');
  for (const [name, fact] of Object.entries(record.files)) {
    const asset = preview.assets.find(x => x.name === name);
    assert(asset && asset.digest === `sha256:${fact.sha256}` && asset.size === fact.size, 'preview asset mismatch');
    const target = fs.mkdtempSync(path.join(scratch, 'preview-'));
    downloadAsset(preview, asset, target);
  }
} else if (!release || release.draft) {
  // Preserve the one workflow's versionCode sequence; reject a late older run.
  const releases = JSON.parse(command('gh', ['api', '--paginate', '--slurp', `repos/${repo}/releases?per_page=100`])).flat();
  for (const previous of releases.filter(x => !x.draft && x.prerelease && x.tag_name.startsWith('android-preview-') && x.tag_name !== tag)) {
    const pkg = readAsset(previous, 'package.txt');
    const previousCode = pkg.match(/^package: name='com\.lovitus\.mddagent\.preview' versionCode='(\d+)'/m);
    assert(previousCode && versionCode > Number(previousCode[1]), 'Android versionCode is not newer than a published preview');
  }
}
if (!release) {
  const notes = kind === 'go'
    ? `Go-only release from ${sha} (tree ${tree}).\n\nLinux amd64 server/Agent; Linux ARM64 headless Agent with PC/SC, audio helper and fresh managed installer (glibc >=2.39; managed modem requires systemd/cgroup and the existing isolation guard; reader-only manual operation remains available); Windows amd64 Agent; macOS 15+ arm64 and Universal (Apple Silicon/Intel), Developer ID signed, not notarized. Linux version alias and revision archive are identical. Verify SHA256SUMS before installation.\n\n[Download, first installation and existing-Go upgrade instructions](https://github.com/${repo}/blob/${sha}/README.md#-快速开始). This is not an automatic Python/Docker migration or a production rollout. [Matching Android preview](https://github.com/${repo}/releases/tag/android-preview-v${version}) remains a preview. Hardware/endurance deferrals remain unchanged.`
    : `Native Android ${version}-preview, versionCode ${versionCode}; source ${sha}, tree ${tree}.\n\nStable existing preview signing identity and application ID; no uninstall required for compatible earlier previews. Android 9+, optional reader sharing and server calls/SMS. Verify SHA256SUMS and signature.txt.\n\n[Matching Go Core v${version}](https://github.com/${repo}/releases/tag/v${version}) is published separately after its gates pass; that link may not exist yet. Do not use the old Python 1.x Latest as a fallback. [Setup and scoped acceptance](https://github.com/${repo}/blob/${sha}/android-agent/README.md). No fleet upgrade, new paid operation or extended endurance acceptance is implied.`;
  const result = spawnSync('gh', ['release', 'create', tag, '--repo', repo, '--verify-tag', '--draft', '--latest=false', ...(kind === 'android' ? ['--prerelease'] : []), '--title', `MDD ${kind === 'android' ? 'Android preview ' : ''}${version}`, '--notes', notes], { encoding: 'utf8', timeout: 25000 });
  release = releaseFor(tag);
  assert(release && release.draft, `draft creation was not confirmed: ${result.error || result.stderr}`);
}
assert.equal(release.prerelease, kind === 'android');
assert.equal(release.tag_name, tag);
for (const asset of release.assets) {
  assert(expected.includes(asset.name), `unexpected existing asset ${asset.name}`);
  if (release.draft && asset.state === 'starter' && asset.size === 0 && asset.digest == null) {
    assert(Number.isSafeInteger(asset.id) && asset.id > 0, 'invalid starter asset ID');
    console.log(`Recover empty starter: release=${release.id}, asset=${asset.id}, name=${asset.name}`);
    command('gh', ['api', '--method', 'DELETE', `repos/${repo}/releases/assets/${asset.id}`]);
    continue;
  }
  assert.equal(asset.state, 'uploaded', 'unconfirmed asset state; never clobber');
  assert.equal(asset.digest, `sha256:${digest(path.join(directory, asset.name))}`, `existing ${asset.name} differs; never clobber`);
}
release = releaseFor(tag);
const missing = expected.filter(name => !release.assets.some(x => x.name === name));
assert(release.draft || missing.length === 0, 'published release incomplete; use a new version');
if (missing.length) command('gh', ['release', 'upload', tag, '--repo', repo, ...missing.map(name => path.join(directory, name))], 240000);
release = releaseFor(tag);
assert.deepEqual(release.assets.map(x => x.name).sort(), expected);
const downloaded = fs.mkdtempSync(path.join(scratch, 'draft-'));
for (const asset of release.assets) {
  const file = downloadAsset(release, asset, downloaded);
  assert.equal(digest(file), digest(path.join(directory, asset.name)), 'draft bytes differ');
}
if (release.draft) {
  if (kind === 'go') {
    const latest = api('releases/latest');
    assert(/^v\d+\.\d+\.\d+$/.test(latest.tag_name), 'unexpected latest version');
    assert(compareVersion(tag, latest.tag_name) > 0, 'older release cannot reclaim latest');
  }
  // A lost publish response is reconciled once, never followed by another create.
  const result = spawnSync('gh', ['release', 'edit', tag, '--repo', repo, '--draft=false', `--latest=${kind === 'go'}`], { encoding: 'utf8', timeout: 25000 });
  release = releaseFor(tag);
  assert(release && !release.draft, `publish did not complete: ${result.error || result.stderr}`);
}
release = releaseFor(tag);
assert(release && !release.draft && release.prerelease === (kind === 'android'));
if (kind === 'go') assert(compareVersion(api('releases/latest').tag_name, tag) >= 0, 'latest did not advance');
console.log(`Published and verified ${release.html_url}; source=${sha}; tree=${tree}; assets=${expected.length}`);
