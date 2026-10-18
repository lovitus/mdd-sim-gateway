#!/usr/bin/env node
// External GitHub boundary for the real publisher's offline behavior tests.
import fs from 'node:fs';
import path from 'node:path';
import { createHash } from 'node:crypto';
const file = process.env.RELEASE_TEST_STATE;
const state = JSON.parse(fs.readFileSync(file));
const args = process.argv.slice(2);
const option = name => args[args.indexOf(name) + 1];
const save = () => fs.writeFileSync(file, JSON.stringify(state));
const hash = bytes => createHash('sha256').update(bytes).digest('hex');
const result = value => { save(); console.log(JSON.stringify(value)); process.exit(0); };
const fail = (message, body) => { save(); if (body) console.log(JSON.stringify(body)); console.error(message); process.exit(1); };
if (args[0] === 'api') {
  if (args[1] === 'graphql') {
    if (state.lookupError) fail('transport unavailable');
    const tag = args.find(x => x.startsWith('tag=')).slice(4);
    const release = state.releases[tag];
    result({ data: { repository: { release: release ? { databaseId: release.id } : null } } });
  }
  const route = args.find(x => x.startsWith('repos/')).split('/').slice(3).join('/');
  if (route.startsWith('git/ref/tags/')) result({ object: { type: 'commit', sha: state.remoteSHA || state.sha } });
  if (route === 'releases/latest') result({ tag_name: state.latest });
  if (route.startsWith('releases?')) result([Object.values(state.releases)]);
  if (route.startsWith('releases/tags/')) {
    if (state.lookupError) fail('transport unavailable');
    const release = state.releases[route.slice('releases/tags/'.length)];
    if (!release || release.draft) fail('HTTP 404', { status: '404', message: 'Not Found' });
    result(release);
  }
  if (route.startsWith('releases/assets/') && option('--method') === 'DELETE') {
    const id = Number(route.split('/').at(-1));
    for (const release of Object.values(state.releases)) {
      const asset = release.assets.find(x => x.id === id);
      if (!asset) continue;
      if (!release.draft || asset.state !== 'starter' || asset.size !== 0 || asset.digest != null) fail('unsafe asset deletion');
      state.writes.push(`delete-starter:${id}`);
      release.assets = release.assets.filter(x => x.id !== id);
      result({});
    }
    fail('unknown asset');
  }
  if (/^releases\/\d+$/.test(route)) {
    const release = Object.values(state.releases).find(x => x.id === Number(route.split('/')[1]));
    if (!release) fail('HTTP 404', { status: '404' });
    result(release);
  }
} else if (args[0] === 'release') {
  const [_, action, tag] = args;
  const release = state.releases[tag];
  if (action === 'create') {
    if (release) fail('already exists');
    state.writes.push('create');
    state.releases[tag] = { id: 100 + Object.keys(state.releases).length, tag_name: tag, draft: true, prerelease: args.includes('--prerelease'), assets: [], html_url: `https://example.invalid/${tag}` };
    if (state.loseCreateResponse) fail('response lost after draft creation');
    result({});
  }
  if (!release) fail('missing release');
  if (action === 'upload') {
    state.writes.push('upload');
    for (const source of args.slice(args.indexOf('--repo') + 2)) {
      const name = path.basename(source);
      if (release.assets.some(x => x.name === name)) fail('clobber attempted');
      const id = ++state.nextAssetID;
      if (state.leaveStarter) {
        state.leaveStarter = false;
        release.assets.push({ id, name, state: 'starter', size: 0, digest: null });
        fail('HTTP 502 incomplete upload');
      }
      const bytes = fs.readFileSync(source);
      fs.writeFileSync(path.join(state.storage, `${tag}--${name}`), bytes);
      release.assets.push({ id, name, state: 'uploaded', size: bytes.length, digest: `sha256:${hash(bytes)}` });
      if (state.interruptUpload) { state.interruptUpload = false; fail('upload interrupted'); }
    }
    result({});
  }
  if (action === 'download') {
    const name = option('--pattern');
    const target = path.join(option('--dir'), name);
    fs.copyFileSync(path.join(state.storage, `${tag}--${name}`), target);
    if (state.corruptDownload === name) fs.appendFileSync(target, 'corrupt');
    result({});
  }
  if (action === 'edit') {
    if (!args.includes('--draft=false')) fail('unexpected edit');
    state.writes.push('publish');
    release.draft = false;
    if (args.includes('--latest=true')) state.latest = tag;
    if (state.losePublishResponse) fail('response lost after publication');
    result({});
  }
}
fail(`unexpected GitHub request ${JSON.stringify(args)}`);
