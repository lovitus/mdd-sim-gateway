// Temporary hosted-only counterexamples; remove from the delivery tree after archival.
import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';

if (process.env.GITHUB_ACTIONS !== 'true' || !process.env.RUNNER_TEMP) throw new Error('hosted proof only');
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
function replace(name, before, after) {
  const value = source(name);
  if (value.split(before).length !== 2) throw new Error(`withdrawal context changed: ${name}`);
  change(name, value.replace(before, after));
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
  const skipped = events.filter(e => e.Action === 'skip' && e.Test && e.Test !== 'TestGUIServiceTargetsOnlyItsOwnConfiguration');
  const passed = events.filter(e => e.Action === 'pass' && e.Test);
  if (skipped.length || result.status !== (failures.length ? 1 : 0) || JSON.stringify(failed) !== JSON.stringify([...failures].sort()) || (!failures.length && !passed.length)) {
    throw new Error(`${name}: unexpected test result: ${JSON.stringify({ status: result.status, failed, skipped: skipped.map(e => e.Test), passed: passed.length })}`);
  }
  console.log(`${name}: ${failures.length ? 'compiled expected failures ' + failed.length : 'passed ' + passed.length}`);
}

try {
  if (process.platform === 'win32') {
    replace('gui_service_windows.go', 'if len(arguments) != 4 ||', 'if false && (len(arguments) != 4 ||');
    replace('gui_service_windows.go', 'filepath.Clean(path)) {', 'filepath.Clean(path))) {');
    run('withdrawn-service-owner', '^TestGUIServiceTargetsOnlyItsOwnConfiguration$', ['TestGUIServiceTargetsOnlyItsOwnConfiguration']);
    restore();
    replace('setup_files_windows.go', 'attributes.Length = uint32(unsafe.Sizeof(attributes))', 'attributes.Length = uint32(unsafe.Sizeof(attributes))\n\tattributes.SecurityDescriptor = nil // withdraw creation-time ACL');
    run('withdrawn-private-publication', '^TestConnectionEditPreservesWindowsACL$', ['TestConnectionEditPreservesWindowsACL']);
    restore();
    replace('setup_gui.go', '\t\t\ttoken.Disable()\n', '\t\t\ttoken.Disable()\n\t\t\tlocationForm.Disable()\n');
    run('withdrawn-config-selection', '^TestGUISetupCanChooseExistingConfigBesidePending$', ['TestGUISetupCanChooseExistingConfigBesidePending']);
    restore();
    run('fixed-windows-contracts', '^Test(GUI|ConnectionEdit|SetupExplicitTrust)');
  } else {
    // All previous Mac counterexamples passed in 39768133187/39772068347;
    // preserve those artifacts, and only recheck the changed fixed batch here.
    run('fixed-contracts', '^Test(GUI|ConnectionEdit|SetupExplicitTrust)');
  }
} finally {
  restore();
}
