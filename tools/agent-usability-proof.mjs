// Temporary hosted-only counterexamples; remove from the delivery tree after archival.
import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';

if (process.env.GITHUB_ACTIONS !== 'true' || !process.env.RUNNER_TEMP) throw new Error('hosted proof only');
const base = '09eb67971f8c611a1eae7b770939a572f6c5d5ea';
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
    run('fixed-service-owner', '^TestGUIServiceTargetsOnlyItsOwnConfiguration$');
  } else {
    const fetch = spawnSync('git', ['fetch', '--no-tags', '--depth=1', 'origin', base], { encoding: 'utf8' });
    if (fetch.status !== 0) throw new Error('baseline source fetch failed');
    const baseline = name => {
      const result = spawnSync('git', ['show', `${base}:${root}${name}`], { encoding: 'utf8' });
      if (result.status !== 0) throw new Error('baseline source unavailable');
      return result.stdout;
    };
    change('setup_gui.go', baseline('setup_gui.go'));
    run('old-setup-ui', '^TestGUISetup(PrimaryActionAndLocation|ModeShowsOnlyApplicableRows)$', ['TestGUISetupPrimaryActionAndLocation', 'TestGUISetupModeShowsOnlyApplicableRows']);
    restore();

    const editor = source('connection_edit.go');
    const start = editor.indexOf('func (edit connectionEdit) checkOriginal() error {');
    const end = editor.indexOf('func (edit connectionEdit) save(', start);
    if (start < 0 || end < 0) throw new Error('editor withdrawal context changed');
    change('connection_edit.go', editor.slice(0, start) + 'func (edit connectionEdit) checkOriginal() error { return nil }\n\n' + editor.slice(end));
    const setup = source('setup.go'), oldSetup = baseline('setup.go');
    const modeStart = setup.indexOf('\t\tif input.TLSMode != ""');
    const modeEnd = setup.indexOf('\t\tif input.Token != ""', modeStart);
    const oldStart = oldSetup.indexOf('\t\tif input.Fingerprint != ""');
    const oldEnd = oldSetup.indexOf('\t\tif input.Token != ""', oldStart);
    if ([modeStart, modeEnd, oldStart, oldEnd].some(n => n < 0)) throw new Error('trust withdrawal context changed');
    change('setup.go', setup.slice(0, modeStart) + oldSetup.slice(oldStart, oldEnd) + setup.slice(modeEnd));
    replace('gui_view.go', 'connection, importance = "Core 连接：已连接", widget.SuccessImportance', 'connection, importance = "Core 连接：已连接", widget.MediumImportance');
    replace('gui_view.go', 'json.MarshalIndent(result, "", "  ")', 'json.MarshalIndent(value, "", "  ")');
    replace('gui_enabled.go', 'controller.actionBusy || controller.ctx.Err() != nil', 'controller.ctx.Err() != nil');
    run('withdrawn-contracts', '^Test(GUIOverview|GUIDiagnostics|GUIAction|ConnectionEdit|SetupExplicitTrust)', [
      'TestGUIOverviewSeparatesRuntimeConnectionAndDevices', 'TestGUIDiagnosticsAndSavedConfigDoNotPublishSecrets', 'TestGUIActionCancellationDoesNotDispatch',
      'TestConnectionEditPreservesOwnerAndChecksCredential', ...['external-before', 'external-during', 'replaced-same-bytes', 'pending-setup'].map(s => 'TestConnectionEditPreservesOwnerAndChecksCredential/' + s),
      'TestSetupExplicitTrustDoesNotReuseHiddenPin', 'TestSetupExplicitTrustDoesNotReuseHiddenPin/system', 'TestSetupExplicitTrustDoesNotReuseHiddenPin/pin',
    ]);
    restore();
    run('fixed-contracts', '^Test(GUI|ConnectionEdit|SetupExplicitTrust)');
  }
} finally {
  restore();
}
