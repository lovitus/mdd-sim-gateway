import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';

// Exercise the complete shipped shell entrypoint and the actual installed Core
// preflight. Only systemctl is a stateful fault fixture, inside a private mount
// namespace. No fixture command reaches the host service manager or Remove.
const [installer] = process.argv.slice(2);
assert(process.platform === 'linux' && process.getuid() === 0);
assert(installer && path.isAbsolute(installer) && process.env.RUNNER_TEMP);
const root = fs.mkdtempSync(path.join(process.env.RUNNER_TEMP, 'mdd-uninstall-refusal-'));
const systemctl = fs.realpathSync('/bin/systemctl');
const provider = 'mdd-vowifi@line-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.service';
const preserved = ['/etc/mdd/core.json', '/etc/mdd/auth.json', '/etc/mdd/tls/server.crt',
  '/etc/mdd/tls/server.key', '/var/lib/mdd-system/release-install/current.json'];
const digest = file => createHash('sha256').update(fs.readFileSync(file)).digest('hex');
const before = preserved.map(digest);
const targetBefore = fs.readlinkSync('/usr/lib/mdd/current');
const shim = path.join(root, 'systemctl');
fs.writeFileSync(shim, `#!/bin/sh
set -eu
cd "$MDD_REFUSAL_ROOT"
touch reached-systemctl
case "$1" in
  show)
    property=$2
    for unit do :; done
    if [ "$property" = --property=LoadState ]; then
      if [ "$unit" = mdd-core.service ] && [ "$MDD_REFUSAL_CASE" = missing-core ]; then
        echo not-found
      else echo loaded; fi
      exit 0
    fi
    case "$unit" in
      mdd-updater.path)
        case "$MDD_REFUSAL_CASE" in
          path-query-fails) exit 76 ;;
          path-state-empty) : ;;
          path-state-unknown) echo unknown ;;
          *) cat path-state ;;
        esac ;;
      mdd-updater.service)
        case "$MDD_REFUSAL_CASE" in
          worker-query-fails) exit 71 ;;
          worker-active|restore-fails|restore-unconfirmed) echo active ;;
          *) echo inactive ;;
        esac ;;
      mdd-core.service)
        case "$MDD_REFUSAL_CASE" in
          state-query-fails) exit 72 ;;
          state-empty) : ;;
          state-unknown) echo unknown ;;
          state-failed) echo failed ;;
          *) echo inactive ;;
        esac ;;
      *) echo inactive ;;
    esac ;;
  is-active)
    # Model the old helper's genuine ambiguous responses as well as the new
    # property query, so the baseline fails on behavior rather than syntax.
    for unit do :; done
    case "$unit" in
      mdd-updater.path) cat path-state; [ "$(cat path-state)" = active ] && exit 0; exit 3 ;;
      mdd-core.service)
        case "$MDD_REFUSAL_CASE" in
          state-query-fails) exit 72 ;;
          state-empty) exit 3 ;;
          state-unknown|missing-core) echo unknown; exit 4 ;;
          state-failed) echo failed; exit 3 ;;
        esac ;;
    esac
    echo inactive; exit 3 ;;
  list-units|list-unit-files) echo '${provider} disabled' ;;
  stop)
    touch stop-attempted
    shift
    for unit do
      [ "$unit" != mdd-updater.service ] || { touch worker-killed; exit 99; }
      [ "$unit" != mdd-updater.path ] || echo inactive > path-state
      if [ "$unit" = mdd-updater.path ] && [ "$MDD_REFUSAL_CASE" = path-stop-fails ]; then exit 77; fi
      if [ "$unit" = '${provider}' ]; then
        case "$MDD_REFUSAL_CASE" in
          provider-stop-fails) exit 73 ;;
          signal-*) signal=$(printf '%s' "$MDD_REFUSAL_CASE" | cut -d- -f2); kill -"$signal" "$PPID" ;;
        esac
      fi
    done ;;
  start)
    [ "$2" = mdd-updater.path ] || { touch unexpected-start; exit 99; }
    touch restore-attempted
    [ "$MDD_REFUSAL_CASE" != restore-fails ] || exit 74
    [ "$MDD_REFUSAL_CASE" = restore-unconfirmed ] || echo active > path-state ;;
  enable) touch unexpected-enable; exit 99 ;;
  disable)
    # Always stop at this destructive boundary. Success and actual removal are
    # covered immediately afterward by the existing real-systemd lifecycle E2E.
    touch disable-attempted
    exit 75 ;;
  *) echo 'unexpected systemctl fixture request' >&2; exit 99 ;;
esac
`, { mode: 0o755 });

const cases = [
  { name: 'worker-active', original: 'active', restored: true },
  { name: 'worker-query-fails', original: 'active', restored: true },
  { name: 'worker-active', original: 'inactive' },
  { name: 'worker-active', original: 'failed' },
  { name: 'provider-stop-fails', original: 'active', restored: true },
  ...['HUP', 'INT', 'TERM'].map(signal => ({ name: `signal-${signal}`, original: 'active', restored: true })),
  ...['state-query-fails', 'state-empty', 'state-unknown', 'missing-core'].map(name =>
    ({ name, original: 'active', restored: true })),
  { name: 'state-failed', original: 'active', disable: true },
  { name: 'disable-fails', original: 'active', disable: true },
  { name: 'restore-fails', original: 'active', recoveryFailure: true },
  { name: 'restore-unconfirmed', original: 'active', recoveryFailure: true },
  ...['state-query-fails', 'state-empty', 'state-unknown', 'missing-core'].map(name =>
    ({ name, original: 'inactive', command: 'stop' })),
  { name: 'ordinary-stop', original: 'active', command: 'stop', success: true },
  ...['path-query-fails', 'path-state-empty', 'path-state-unknown'].map(name =>
    ({ name, original: 'active', untouched: true })),
  { name: 'path-stop-fails', original: 'active', restored: true, exit: 77, stopAttempted: true },
];
const receipt = { passed: [] };
function exercise(script, item, label) {
  const directory = path.join(root, label);
  fs.mkdirSync(directory);
  fs.writeFileSync(path.join(directory, 'path-state'), item.original + '\n');
  const result = spawnSync('unshare', ['--mount', '--propagation', 'private', '/bin/sh', '-ec',
    'mount --bind "$1" "$2"; exec /bin/sh "$3" "$4"',
    'mdd-uninstall-refusal', shim, systemctl, script, item.command || 'uninstall'], {
    env: { ...process.env, MDD_REFUSAL_ROOT: directory, MDD_REFUSAL_CASE: item.name },
    encoding: 'utf8', timeout: 30000, maxBuffer: 1 << 20,
  });
  assert(!result.error && !result.signal, `${label}: ${result.error || result.signal}`);
  const exists = name => fs.existsSync(path.join(directory, name));
  assert(!exists('worker-killed') && !exists('unexpected-start') && !exists('unexpected-enable'), label);
  assert.deepEqual(preserved.map(digest), before, `${label}: changed durable data`);
  assert.equal(fs.readlinkSync('/usr/lib/mdd/current'), targetBefore);
  // Do not count preflight/environment rejection as a regression result.
  assert(exists('reached-systemctl'), `${label}: fixture did not reach the intended behavior: ${result.stderr}`);
  const expectedPath = item.untouched ? item.original : item.restored ? 'active' : 'inactive';
  const accepted = !result.signal && (item.success ? result.status === 0 : result.status !== 0) &&
    (item.exit === undefined || result.status === item.exit) &&
    (item.stopAttempted === undefined || exists('stop-attempted') === item.stopAttempted) &&
    (!item.untouched || !exists('stop-attempted')) &&
    exists('disable-attempted') === !!item.disable &&
    fs.readFileSync(path.join(directory, 'path-state'), 'utf8').trim() === expectedPath &&
    exists('restore-attempted') === !!(item.restored || item.recoveryFailure) &&
    (!item.recoveryFailure || /restoring the previously active updater path failed/.test(result.stderr));
  return { accepted, detail: `${label}: exit=${result.status}, signal=${result.signal}, stderr=${result.stderr}` };
}
try {
  for (const [index, item] of cases.entries()) {
    const result = exercise(installer, item, `fixed-${index}`);
    assert(result.accepted, result.detail);
    receipt.passed.push(`${item.command || 'uninstall'}/${item.original}/${item.name}`);
  }
  console.log(JSON.stringify(receipt, null, 2));
} finally {
  fs.rmSync(root, { recursive: true, force: true });
}
