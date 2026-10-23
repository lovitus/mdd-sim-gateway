import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { spawn, spawnSync } from 'node:child_process';

const [binary, output, expected = 'green'] = process.argv.slice(2);
assert(binary && output && [binary, output].every(path.isAbsolute));
assert.equal(process.env.GITHUB_ACTIONS, 'true', 'disposable hosted runner only');
assert.equal(process.getuid(), 0);
assert(['red', 'green'].includes(expected));
const root = fs.mkdtempSync(path.join(path.dirname(output), 'dhcp-e2e-'));
const client = `mdd-dhcp-${process.pid}`;
const peer = `${client}-peer`;
const unit = '/etc/systemd/system/mdd-agent.service';
const guardUnit = '/etc/systemd/system/mdd-cellular-guard.service';
const receipt = { schema: 1, expected, checks: [], complete: false };
let child;
let installed = false;
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
  ip('netns', 'add', client); ip('netns', 'add', peer);
  ip('link', 'add', 'mddcell', 'netns', client, 'type', 'veth', 'peer', 'name', 'cell-peer', 'netns', peer);
  ip('link', 'add', 'mdddefault', 'netns', client, 'type', 'veth', 'peer', 'name', 'default-peer', 'netns', peer);
  for (const ns of [client, peer]) inside(ns, 'ip', 'link', 'set', 'lo', 'up');
  for (const [ns, name, address] of [[client, 'mdddefault', '192.0.2.2/24'], [peer, 'default-peer', '192.0.2.1/24'], [peer, 'cell-peer', '198.18.0.1/24']]) {
    inside(ns, 'ip', 'addr', 'add', address, 'dev', name);
    inside(ns, 'ip', 'link', 'set', name, 'up');
  }
  inside(peer, 'ip', 'addr', 'add', '203.0.113.9/32', 'dev', 'lo');
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
  fs.symlinkSync(path.resolve('go-runtime/release/mdd-cellular-guard.service'), guardUnit);
  installed = true;
  fs.writeFileSync(unit, `[Unit]
Description=MDD DHCP hosted qualification only
[Service]
Type=oneshot
Environment=MDD_DHCP_FIXTURE=${root}
ExecStart=${ipPath} netns exec ${client} ${binary} -test.run=^TestLinuxDHCPClaimLifecycle$ -test.v -test.timeout=120s
TimeoutStartSec=130
StandardOutput=file:${root}/agent.log
StandardError=inherit
`, { mode: 0o644 });
  run('systemctl', ['daemon-reload']);
  const result = spawnSync('systemctl', ['start', 'mdd-agent.service'], { timeout: 140000, encoding: 'utf8' });
  assert(!result.error && !result.signal, `${result.error || result.signal}`);
  const log = fs.readFileSync(path.join(root, 'agent.log'), 'utf8');
  fs.copyFileSync(path.join(root, 'agent.log'), `${output}.log`);
  const code = run('systemctl', ['show', 'mdd-agent.service', '--property=ExecMainStatus', '--value']).trim();
  receipt.mainExit = Number(code);
  if (expected === 'red') {
    assert.notEqual(result.status, 0); assert.equal(code, '1');
    assert.match(log, /DHCP_CONNECT_REJECTED:/);
    assert.doesNotMatch(log, /DHCP_CONNECT_ACCEPTED:/);
    receipt.checks.push('compiled pre-fix PrepareData entry rejects the real method=3 workflow');
  } else {
    assert.equal(result.status, 0, log); assert.equal(code, '0', log);
    assert.match(log, /--- PASS: TestLinuxDHCPClaimLifecycle/);
    receipt.checks.push('DORA/private route and real WebSocket TCP/UDP borrower', 'T1 unicast/T2 broadcast',
      'NAK and expiry close existing sockets; busy call guard retained', 'persistent policy owner recovers unchanged',
      'real alternate default route without fallback; host DNS/routes preserved',
      'cancelled preparation/static/SIM epoch/same-name replacement');
  }
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
  for (const ns of [client, peer]) {
    if (exists(`/run/netns/${ns}`)) {
      try { ip('netns', 'delete', ns); } catch (error) { failures.push(error.message); }
    }
  }
  for (const file of ['wire.jsonl', 'peer.log', 'agent.log']) {
    if (exists(path.join(root, file))) fs.copyFileSync(path.join(root, file), `${output}.${file}`);
  }
  receipt.cleanupErrors = failures;
  if (failures.length) { receipt.complete = false; process.exitCode = 1; }
  fs.writeFileSync(output, `${JSON.stringify(receipt, null, 2)}\n`);
}
