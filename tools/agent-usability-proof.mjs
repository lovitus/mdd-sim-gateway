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
  const result = spawnSync('go', ['-C', 'go-runtime', 'test', '-race', '-tags', 'gui', '-count=1', '-json', '-run', selection, './cmd/mdd-agent'], {
    encoding: 'utf8', timeout: 240000, maxBuffer: 20 * 1024 * 1024, env: process.env,
  });
  fs.writeFileSync(path.join(directory, name + '.jsonl'), result.stdout ?? '');
  fs.writeFileSync(path.join(directory, name + '.stderr'), result.stderr ?? '');
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
    replace('setup_files.go', 'err = protectEditedConfig(name, preserveAccessFrom)', 'err = nil // withdraw private publication before writing');
    run('withdrawn-private-publication', '^TestConnectionEditPreservesWindowsACL$', ['TestConnectionEditPreservesWindowsACL']);
    restore();
    run('fixed-windows-contracts', '^Test(GUI|ConnectionEdit|SetupExplicitTrust)');
  } else {
    // The original setup/file/trust/view counterexamples already executed in
    // 39768133187. Preserve those artifacts; do not rerun successful reds.
    replace('gui_enabled.go', '\tif err := ctx.Err(); err != nil {\n\t\treturn err\n\t}\n\treturn operation(ctx)', '\treturn operation(ctx)');
    run('withdrawn-cancel-boundary', '^TestGUIActionCancellationAfterPreflight$', ['TestGUIActionCancellationAfterPreflight', 'TestGUIActionCancellationAfterPreflight/noncancellable-read']);
    restore();
    const owner = source('gui_owner.go');
    if (owner.split('owner.lifetime.Err()').length !== 3) throw new Error('owner withdrawal context changed');
    change('gui_owner.go', owner.replaceAll('owner.lifetime.Err()', 'ctx.Err()'));
    run('withdrawn-runtime-owner', '^TestGUIRuntimeOwnerDoesNotAdoptReplacement$', ['TestGUIRuntimeOwnerDoesNotAdoptReplacement']);
    restore();
    replace('setup_gui.go', '\tloadPending()\n\tbusy := false', '\t// withdraw pending form restoration\n\tbusy := false');
    run('withdrawn-pending-resume', '^TestGUISetupResumesPinnedPending$', ['TestGUISetupResumesPinnedPending', 'TestGUISetupResumesPinnedPending/create-receipt', 'TestGUISetupResumesPinnedPending/credential-check']);
    restore();
    replace('gui_view.go', '\tcontroller.renderRawModes(value)', '\t// withdraw saved modes view');
    run('withdrawn-persistent-modes', '^TestGUIPersistentModesRemainVisibleWithoutSessions$', ['TestGUIPersistentModesRemainVisibleWithoutSessions']);
    restore();
    run('fixed-contracts', '^Test(GUI|ConnectionEdit|SetupExplicitTrust)');
  }
} finally {
  restore();
}
