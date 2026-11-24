#!/usr/bin/env node
// Hosted build only. Reuse the normal immutable bundle's dependencies and the
// real packager; these labelled test versions must never be published assets.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';

assert.equal(process.env.GITHUB_ACTIONS, 'true');
const source = path.resolve(process.env.UPDATE_RELEASE_SOURCE);
const output = path.resolve(process.env.UPDATE_QUALIFICATION_OUTPUT);
const bin = path.resolve(process.env.BUILD_ROOT, 'bin');
const run = (command, args) => execFileSync(command, args, { encoding: 'utf8', timeout: 180000, maxBuffer: 2 ** 20 }).trim();
const hash = file => createHash('sha256').update(fs.readFileSync(file)).digest('hex');
const manifest = JSON.parse(fs.readFileSync(path.join(source, 'manifest.json')));
const revision = run('git', ['rev-parse', 'HEAD']);
assert.equal(manifest.source_revision, revision);
assert.equal(manifest.artifacts.length, 22);
assert(!fs.existsSync(output), 'qualification output must be new');
fs.mkdirSync(output, { recursive: true });
const receipt = { source_revision: revision, tree: run('git', ['rev-parse', 'HEAD^{tree}']), packages: {} };
const flags = {
  'mdd-agent': 'agent', 'mdd-call-audio-helper': 'agent-audio-helper',
  'mdd-updater': 'updater', 'mdd-updater.service': 'updater-unit', 'mdd-updater.path': 'updater-path',
  'mdd-vowifi': 'provider', 'xray': 'xray', 'xray-source.tar.gz': 'xray-source', 'XRAY-NOTICE.md': 'xray-notice',
  'mdd-core.service': 'core-unit', 'mdd-agent.service': 'agent-unit', 'mdd-cellular-guard.service': 'cellular-guard-unit',
  'mdd-vowifi@.service': 'provider-unit', 'mdd-provider-apply.service': 'provider-apply-unit', 'mdd-egress.service': 'egress-unit',
  'mdd-vowifi-source.tar.gz': 'provider-source', 'LICENSE-NOTICE.md': 'provider-notice',
  LICENSE: 'project-license', NOTICE: 'project-notice', 'THIRD_PARTY_LICENSES.md': 'third-party-notices',
  'go-dependency-licenses.tar.gz': 'go-dependency-licenses',
};
const scratch = path.join(process.env.RUNNER_TEMP, 'mdd-update-build');
fs.mkdirSync(scratch, { recursive: false });
// One-time behavioral counterexample: compile the previously merged main, not
// an absent symbol or a rewritten runtime/checker response. Archive after proof.
const oldMain = path.join(scratch, 'old-main.go');
run('git', ['fetch', '--no-tags', '--depth=1', 'origin', '620b153fa79a87459eecca3e450824246317353b']);
fs.writeFileSync(oldMain, run('git', ['show', '620b153fa79a87459eecca3e450824246317353b:go-runtime/cmd/mdd-core/main.go']) + '\n');
const overlay = path.join(scratch, 'old-main-overlay.json');
fs.writeFileSync(overlay, JSON.stringify({ Replace: { [path.resolve('go-runtime/cmd/mdd-core/main.go')]: oldMain } }));
for (const [name, version] of [['red', '0.0.0-update-ci.1'], ['low', '0.0.0-update-ci.1'], ['high', '0.0.0-update-ci.2']]) {
  const core = path.join(scratch, `mdd-core-${name}`);
  const args = ['-C', 'go-runtime', 'build', '-trimpath', '-ldflags', `-X main.releaseVersion=${version}`];
  if (name === 'red') args.push('-overlay', overlay);
  run('go', [...args, '-o', core, './cmd/mdd-core']);
  const buildInfo = run('go', ['version', '-m', core]);
  assert(buildInfo.includes(`vcs.revision=${revision}`), 'build must preserve the real source revision');
  assert(buildInfo.includes('vcs.modified=false'), 'qualification source must be clean');
  fs.writeFileSync(path.join(output, `${name}-build-info.txt`), buildInfo + '\n');
  const releaseID = `mdd-update-ci-${name}-${revision.slice(0, 12)}`;
  const directory = path.join(scratch, releaseID);
  const pack = ['-output', directory, '-release-id', releaseID, '-source-revision', revision, '-architecture', 'amd64', '-core', core];
  for (const artifact of manifest.artifacts) {
    if (artifact.name === 'mdd-core') continue;
    assert(flags[artifact.name], `unmapped real bundle artifact ${artifact.name}`);
    pack.push(`-${flags[artifact.name]}`, path.join(source, artifact.name));
  }
  run(path.join(bin, 'mdd-release'), pack);
  const actual = JSON.parse(fs.readFileSync(path.join(directory, 'manifest.json')));
  assert.equal(actual.artifacts.length, 22);
  assert.equal(actual.source_revision, revision);
  fs.copyFileSync('go-runtime/release/install-release.sh', path.join(scratch, 'install-release.sh'));
  fs.chmodSync(path.join(scratch, 'install-release.sh'), 0o755);
  const tar = path.join(output, `${name}.tar`);
  run('tar', ['-C', scratch, '-cf', tar, 'install-release.sh', releaseID]);
  const coreSHA = actual.artifacts.find(item => item.role === 'core').sha256;
  assert.equal(coreSHA, hash(core));
  receipt.packages[name] = { version, release_id: releaseID, tar_sha256: hash(tar), core_sha256: coreSHA, manifest: actual,
    ...(name === 'red' ? { main_source: '620b153fa79a87459eecca3e450824246317353b', overlay_sha256: hash(oldMain) } : {}) };
}
assert.notEqual(receipt.packages.low.core_sha256, receipt.packages.high.core_sha256);
fs.writeFileSync(path.join(output, 'qualification.json'), JSON.stringify(receipt, null, 2) + '\n');
for (const file of ['package.json', 'package-lock.json']) fs.copyFileSync(`webui/${file}`, path.join(output, file));
fs.copyFileSync('tools/test-system-update.mjs', path.join(output, 'test-system-update.mjs'));
console.log('Prepared isolated update qualification packages; normal release artifact unchanged.');
