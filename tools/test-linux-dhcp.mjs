import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { spawn, spawnSync } from 'node:child_process';

const [binary, output] = process.argv.slice(2);
assert(binary && output && [binary, output].every(path.isAbsolute));
assert.equal(process.env.GITHUB_ACTIONS, 'true', 'disposable hosted runner only');
assert.equal(process.getuid(), 0);
const root = fs.mkdtempSync(path.join(path.dirname(output), 'mdd-dhcp-fixture-'));
const client = `mdd-dhcp-${process.pid}`;
const peer = `${client}-peer`;
const sender = `${client}-sender`;
const unit = '/etc/systemd/system/mdd-agent.service';
const guardUnit = '/etc/systemd/system/mdd-cellular-guard.service';
const receipt = { schema: 1, checks: [], complete: false };
let child;
let installed = false;
function hostCgroupView() {
  const entries = fs.readFileSync('/proc/self/mountinfo', 'utf8').trim().split('\n')
    .filter(line => line.split(' ')[4] === '/sys/fs/cgroup');
  assert.equal(entries.length, 1, 'one real cgroup mount required');
  assert(entries[0].includes(' - cgroup2 '), 'cgroup v2 required');
  return { mountNamespace: fs.readlinkSync('/proc/self/ns/mnt'),
    cgroupNamespace: fs.readlinkSync('/proc/self/ns/cgroup'), mountinfo: entries[0] };
}
function run(command, args, expectedCode = 0, timeout = 20000) {
  const result = spawnSync(command, args, { encoding: 'utf8', timeout, maxBuffer: 8 << 20 });
  assert(!result.error && !result.signal, `${command}: ${result.error || result.signal}`);
  assert.equal(result.status, expectedCode, `${command}: ${result.stderr || result.stdout}`);
  return result.stdout;
}
function ip(...args) { return run('ip', args); }
function inside(namespace, ...args) { return ip('netns', 'exec', namespace, ...args); }
function exists(file) { try { fs.lstatSync(file); return true; } catch (error) { if (error.code === 'ENOENT') return false; throw error; } }
const guardFiles = ['/var/lib/mdd-agent/cellular-guard-enabled', '/etc/udev/rules.d/90-mdd-cellular-guard.rules', '/etc/NetworkManager/conf.d/90-mdd-cellular-guard.conf'];
try {
  for (const file of [unit, guardUnit, ...guardFiles]) assert(!exists(file), `existing owner: ${file}`);
  receipt.hostBefore = hostCgroupView();
  receipt.tools = Object.fromEntries([['ip', '-V'], ['mount', '--version'], ['unshare', '--version']]
    .map(([tool, flag]) => [tool, run(tool, [flag]).trim()]));
  ip('netns', 'add', client); ip('netns', 'add', peer); ip('netns', 'add', sender);
  ip('link', 'add', 'mddcell', 'netns', client, 'type', 'veth', 'peer', 'name', 'cell-peer', 'netns', peer);
  ip('link', 'add', 'mdddefault', 'netns', client, 'type', 'veth', 'peer', 'name', 'default-peer', 'netns', peer);
  ip('link', 'add', 'mddforward', 'netns', client, 'type', 'veth', 'peer', 'name', 'sender', 'netns', sender);
  for (const ns of [client, peer, sender]) inside(ns, 'ip', 'link', 'set', 'lo', 'up');
  for (const [ns, name, address] of [[client, 'mdddefault', '192.0.2.2/24'], [peer, 'default-peer', '192.0.2.1/24'], [peer, 'cell-peer', '198.18.0.1/24'], [client, 'mddforward', '172.20.0.1/24'], [sender, 'sender', '172.20.0.2/24']]) {
    inside(ns, 'ip', 'addr', 'add', address, 'dev', name);
    inside(ns, 'ip', 'link', 'set', name, 'up');
  }
  inside(peer, 'ip', 'addr', 'add', '203.0.113.9/32', 'dev', 'lo');
  inside(peer, 'ip', 'addr', 'add', '198.18.0.3/24', 'dev', 'cell-peer');
  inside(peer, 'ip', 'route', 'add', '172.20.0.0/24', 'via', '198.18.0.2', 'dev', 'cell-peer');
  inside(sender, 'ip', 'route', 'add', '198.18.0.0/24', 'via', '172.20.0.1', 'dev', 'sender');
  inside(client, 'sysctl', '-q', '-w', 'net.ipv4.ip_forward=1');
  inside(client, 'ip', 'route', 'add', 'default', 'via', '192.0.2.1', 'dev', 'mdddefault');
  // A second, actually reachable path exposes accidental default-route fallback.
  for (const ns of [client, peer]) {
    inside(ns, 'sysctl', '-q', '-w', 'net.ipv4.conf.all.rp_filter=0');
    inside(ns, 'sysctl', '-q', '-w', 'net.ipv4.conf.default.rp_filter=0');
    for (const name of ns === client ? ['mddcell', 'mdddefault'] : ['cell-peer', 'default-peer']) {
      inside(ns, 'sysctl', '-q', '-w', `net.ipv4.conf.${name}.rp_filter=0`);
    }
  }
  fs.writeFileSync(path.join(root, 'mode'), 'renew', { mode: 0o600 });
  const peerLog = fs.openSync(path.join(root, 'peer.log'), 'w', 0o600);
  child = spawn('ip', ['netns', 'exec', peer, binary, '-test.run=^TestLinuxDHCPPeer$', '-test.v', '-test.timeout=3m'], {
    env: { ...process.env, MDD_DHCP_FIXTURE: root, MDD_DHCP_PEER: '1' }, stdio: ['ignore', peerLog, peerLog],
  });
  fs.closeSync(peerLog);
  const ipPath = run('which', ['ip']).trim();
  const unsharePath = run('which', ['unshare']).trim();
  fs.mkdirSync(path.join(root, 'cgroup'), { mode: 0o700 });
  // ip netns exec replaces /sys. Preserve the existing hierarchy in a private
  // mount namespace; a new cgroup2 mount could change global hierarchy flags.
  fs.writeFileSync(path.join(root, 'prepare-cgroup.sh'), `set -eu
test "$(readlink /proc/self/ns/mnt)" != "$MDD_DHCP_HOST_MNT"
test "$(readlink /proc/self/ns/cgroup)" = "$MDD_DHCP_HOST_CGROUP"
test "$(cat /proc/self/cgroup)" = '0::/system.slice/mdd-agent.service'
case "$1" in
preserve)
  cat /proc/self/mountinfo > "$MDD_DHCP_FIXTURE/mount-before.log"
  mount --bind /sys/fs/cgroup "$MDD_DHCP_FIXTURE/cgroup"
  mount -o remount,bind,ro,nosuid,nodev,noexec "$MDD_DHCP_FIXTURE/cgroup"
  exec "$MDD_DHCP_IP" netns exec "$MDD_DHCP_CLIENT_NS" /bin/sh "$MDD_DHCP_FIXTURE/prepare-cgroup.sh" restore
  ;;
restore)
  cat /proc/self/mountinfo > "$MDD_DHCP_FIXTURE/mount-netns.log"
  mount --bind "$MDD_DHCP_FIXTURE/cgroup" /sys/fs/cgroup
  mount -o remount,bind,ro,nosuid,nodev,noexec /sys/fs/cgroup
  test "$(findmnt -rn -M /sys/fs/cgroup -o FSTYPE)" = cgroup2
  case ",$(findmnt -rn -M /sys/fs/cgroup -o VFS-OPTIONS)," in
    *,ro,*) ;;
    *) exit 1 ;;
  esac
  test -d /sys/fs/cgroup/system.slice/mdd-agent.service
  test "$(stat -Lc '%d:%i' /sys/fs/cgroup/system.slice/mdd-agent.service)" = "$(stat -Lc '%d:%i' "$MDD_DHCP_FIXTURE/cgroup/system.slice/mdd-agent.service")"
  cat /proc/self/mountinfo > "$MDD_DHCP_FIXTURE/mount-after.log"
  cat /proc/self/cgroup > "$MDD_DHCP_FIXTURE/process-cgroup.log"
  readlink /proc/self/ns/mnt /proc/self/ns/cgroup > "$MDD_DHCP_FIXTURE/process-namespaces.log"
  exec "$MDD_DHCP_TEST_BINARY" -test.run=^TestLinuxDHCPClaimLifecycle$ -test.v -test.timeout=180s
  ;;
*) exit 1 ;;
esac
`, { mode: 0o600 });
  fs.symlinkSync(path.resolve('go-runtime/release/mdd-cellular-guard.service'), guardUnit);
  installed = true;
  fs.writeFileSync(unit, `[Unit]
Description=MDD DHCP hosted qualification only
[Service]
Type=oneshot
Environment=MDD_DHCP_FIXTURE=${root}
Environment=MDD_DHCP_CLIENT_NS=${client}
Environment=MDD_DHCP_SENDER_NS=${sender}
Environment=MDD_DHCP_HOST_MNT=${receipt.hostBefore.mountNamespace}
Environment=MDD_DHCP_HOST_CGROUP=${receipt.hostBefore.cgroupNamespace}
Environment=MDD_DHCP_IP=${ipPath}
Environment=MDD_DHCP_TEST_BINARY=${binary}
ExecStart=${unsharePath} --mount --propagation private /bin/sh ${root}/prepare-cgroup.sh preserve
TimeoutStartSec=190
StandardOutput=file:${root}/agent.log
StandardError=inherit
`, { mode: 0o644 });
  run('systemctl', ['daemon-reload']);
  const result = spawnSync('systemctl', ['start', 'mdd-agent.service'], { timeout: 200000, encoding: 'utf8' });
  assert(!result.error && !result.signal, `${result.error || result.signal}`);
  const log = fs.readFileSync(path.join(root, 'agent.log'), 'utf8');
  fs.copyFileSync(path.join(root, 'agent.log'), `${output}.log`);
  fs.chmodSync(`${output}.log`, 0o644);
  const code = run('systemctl', ['show', 'mdd-agent.service', '--property=ExecMainStatus', '--value']).trim();
  receipt.mainExit = Number(code);
  assert.equal(result.status, 0, log); assert.equal(code, '0', log);
  assert.match(log, /--- PASS: TestLinuxDHCPClaimLifecycle/);
  receipt.checks.push('DORA/private route and real WebSocket TCP/UDP borrower', 'T1 unicast/T2 broadcast',
    'NAK and expiry close existing sockets; busy call guard retained', 'persistent policy owner recovers unchanged',
    'real alternate default route without fallback; host DNS/routes preserved',
    'cancelled preparation/static/SIM epoch/same-name replacement');
  receipt.complete = true;
} catch (error) {
  receipt.error = error.stack;
  process.exitCode = 1;
} finally {
  const failures = [];
  if (installed) {
    for (const [command, args] of [
      ['systemctl', ['stop', 'mdd-agent.service']],
      ['systemctl', ['disable', 'mdd-cellular-guard.service']],
    ]) {
      try { run(command, args); } catch (error) { failures.push(error.message); }
    }
    for (const file of [unit, guardUnit, ...guardFiles]) fs.rmSync(file, { force: true });
    try { run('systemctl', ['daemon-reload']); run('udevadm', ['control', '--reload']); } catch (error) { failures.push(error.message); }
  }
  if (child) {
    child.kill('SIGTERM');
    await new Promise(resolve => { if (child.exitCode !== null || child.signalCode) resolve(); else child.once('exit', resolve); });
  }
  for (const ns of [client, peer, sender]) {
    if (exists(`/run/netns/${ns}`)) {
      try { ip('netns', 'delete', ns); } catch (error) { failures.push(error.message); }
    }
  }
  if (receipt.hostBefore) {
    try {
      receipt.hostAfter = hostCgroupView();
      assert.deepEqual(receipt.hostAfter, receipt.hostBefore, 'host cgroup mount or namespace changed');
    } catch (error) { failures.push(error.message); }
  }
  for (const file of ['wire.jsonl', 'peer.log', 'agent.log', 'default-packets', 'capture-barrier', 'capture-error',
    'mount-before.log', 'mount-netns.log', 'mount-after.log', 'process-cgroup.log', 'process-namespaces.log']) {
    if (exists(path.join(root, file))) {
      fs.copyFileSync(path.join(root, file), `${output}.${file}`);
      fs.chmodSync(`${output}.${file}`, 0o644);
    }
  }
  receipt.cleanupErrors = failures;
  if (failures.length) { receipt.complete = false; process.exitCode = 1; }
  fs.writeFileSync(output, `${JSON.stringify(receipt, null, 2)}\n`, { mode: 0o644 });
  console.log(JSON.stringify(receipt, null, 2));
  // The uploaded receipts contain all fixture evidence. Remove only this run's
  // private scratch directory so artifact globbing cannot fail on root-only data.
  fs.rmSync(root, { recursive: true, force: true });
}
