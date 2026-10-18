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
  const route = args.find(x => x.startsWith('repos/')).split('/').slice(3).join('/');
  if (route.startsWith('git/ref/tags/')) result({ object: { type: 'commit', sha: state.remoteSHA || state.sha } });
  if (route === 'releases/latest') result({ tag_name: state.latest });
  if (route.startsWith('releases?')) result([Object.values(state.releases)]);
  if (route.startsWith('releases/tags/')) {
    if (state.lookupError) fail('transport unavailable');
    const release = state.releases[route.slice('releases/tags/'.length)];
    if (!release) fail('HTTP 404', { status: '404', message: 'Not Found' });
    result(release);
  }
} else if (args[0] === 'release') {
  const [_, action, tag] = args;
  const release = state.releases[tag];
  if (action === 'create') {
    if (release) fail('already exists');
    state.writes.push('create');
    state.releases[tag] = { tag_name: tag, draft: true, prerelease: args.includes('--prerelease'), assets: [], html_url: `https://example.invalid/${tag}` };
    result({});
  }
  if (!release) fail('missing release');
  if (action === 'upload') {
    state.writes.push('upload');
    for (const source of args.slice(args.indexOf('--repo') + 2)) {
      const name = path.basename(source);
      if (release.assets.some(x => x.name === name)) fail('clobber attempted');
      const bytes = fs.readFileSync(source);
      fs.writeFileSync(path.join(state.storage, `${tag}--${name}`), bytes);
      release.assets.push({ name, size: bytes.length, digest: `sha256:${hash(bytes)}` });
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
