// Temporary hosted-only counterexamples; remove from the delivery tree after archival.
import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';

if (process.env.GITHUB_ACTIONS !== 'true' || !process.env.RUNNER_TEMP || process.platform !== 'win32') throw new Error('hosted Windows proof only');
const directory = path.join(process.env.RUNNER_TEMP, 'agent-usability-proof');
fs.mkdirSync(directory, { recursive: true });
const root = 'go-runtime/cmd/mdd-agent/';
const saved = new Map();
const source = name => fs.readFileSync(root + name, 'utf8');
function change(name, value) {
  if (!saved.has(name)) saved.set(name, source(name));
  fs.writeFileSync(root + name, value);
}
function restore() {
  for (const [name, value] of saved) fs.writeFileSync(root + name, value);
  saved.clear();
}
function run(name, selection, failures = []) {
  const executable = path.join(directory, name + (process.platform === 'win32' ? '.exe' : '.test'));
  const compiled = spawnSync('go', ['-C', 'go-runtime', 'test', '-x', '-race', '-tags', 'gui', '-c', '-o', executable, './cmd/mdd-agent'], {
    encoding: 'utf8', timeout: 600000, maxBuffer: 20 * 1024 * 1024, env: process.env,
  });
  fs.writeFileSync(path.join(directory, name + '.compile.stdout'), compiled.stdout ?? '');
  fs.writeFileSync(path.join(directory, name + '.compile.stderr'), compiled.stderr ?? '');
  fs.writeFileSync(path.join(directory, name + '.compile.json'), JSON.stringify({status: compiled.status, signal: compiled.signal, error: compiled.error?.message}));
  if (compiled.error || compiled.signal || compiled.status !== 0) throw new Error(`${name}: compilation failed or timed out; not a behavioral counterexample`);
  const result = spawnSync('go', ['tool', 'test2json', '-t', '-p', 'github.com/lovitus/mdd-sim-gateway/go-runtime/cmd/mdd-agent', executable, '-test.v=test2json', '-test.run=' + selection, '-test.timeout=3m'], {
    cwd: path.resolve(root),
    encoding: 'utf8', timeout: 240000, maxBuffer: 20 * 1024 * 1024, env: process.env,
  });
  fs.writeFileSync(path.join(directory, name + '.jsonl'), result.stdout ?? '');
  fs.writeFileSync(path.join(directory, name + '.stderr'), result.stderr ?? '');
  fs.rmSync(executable);
  if (result.error || result.signal) throw new Error(`${name}: execution did not finish`);
  const events = result.stdout.trim().split('\n').map(line => JSON.parse(line));
  const failed = events.filter(e => e.Action === 'fail' && e.Test).map(e => e.Test).sort();
  const skipped = events.filter(e => e.Action === 'skip' && e.Test);
  const passed = events.filter(e => e.Action === 'pass' && e.Test);
  if (skipped.length || result.status !== (failures.length ? 1 : 0) || JSON.stringify(failed) !== JSON.stringify([...failures].sort()) || (!failures.length && !passed.length)) {
    throw new Error(`${name}: unexpected test result: ${JSON.stringify({ status: result.status, failed, skipped: skipped.map(e => e.Test), passed: passed.length })}`);
  }
  if (failures.length && !events.some(e => e.Test === failures[0] && e.Action === 'output' && e.Output?.includes('SERVICE_STATUS_CONTROL_RIGHTS:'))) {
    throw new Error(`${name}: failure did not reach the old status permission boundary`);
  }
  console.log(`${name}: ${failures.length ? 'compiled expected failures ' + failed.length : 'passed ' + passed.length}`);
}

try {
  // Earlier UI/ownership counterexamples are already archived. Only the
  // newly found query-only permission regression remains to be demonstrated.
  const oldHead = '1f37fbee3b63f5009aaaae0d3eb60d4e8d5a6031';
  const fetched = spawnSync('git', ['fetch', '--no-tags', '--depth=1', 'origin', oldHead], {encoding: 'utf8', timeout: 60000});
  fs.writeFileSync(path.join(directory, 'baseline-fetch.txt'), (fetched.stdout ?? '') + (fetched.stderr ?? ''));
  if (fetched.error || fetched.signal || fetched.status !== 0) throw new Error('archived baseline unavailable; not a behavioral counterexample');
  for (const file of ['service_windows.go']) {
    const oldPage = spawnSync('git', ['show', `${oldHead}:${root}${file}`], {encoding: 'utf8', timeout: 25000});
    if (oldPage.error || oldPage.signal || oldPage.status !== 0 || !oldPage.stdout) throw new Error('archived page unavailable');
    fs.writeFileSync(path.join(directory, 'baseline-' + file), oldPage.stdout);
    change(file, oldPage.stdout);
  }
  run('old-service-query', '^TestWindowsServiceStatusDoesNotRequireControlRights$', ['TestWindowsServiceStatusDoesNotRequireControlRights']);
  restore();
  run('fixed-windows-contracts', '^Test(GUI|ConnectionEdit|SetupExplicitTrust|WindowsServiceStatusDoesNotRequireControlRights)');
} finally {
  restore();
}
