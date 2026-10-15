// Temporary hosted-only counterexamples. Mutations remove the corresponding
// behavior while retaining declarations; compilation failures are never reds.
import fs from 'node:fs'
import {spawnSync} from 'node:child_process'
fs.mkdirSync('egress-proof',{recursive:true})
const originals=new Map()
function mutate(path,before,after) {
  const text=fs.readFileSync(path,'utf8')
  if(!text.includes(before))throw new Error(`mutation anchor missing: ${path}`)
  if(!originals.has(path)) originals.set(path,text)
  fs.writeFileSync(path,text.replace(before,after))
}
function restore() {for(const [path,text] of originals)fs.writeFileSync(path,text);originals.clear()}
function run(name,args,cwd,redNames=[]) {
  const [command,...options]=args
  const result=spawnSync(command,options,{cwd,encoding:'utf8',maxBuffer:64*1024*1024,timeout:600000})
  fs.writeFileSync(`egress-proof/${name}.stdout`,result.stdout||'')
  fs.writeFileSync(`egress-proof/${name}.stderr`,result.stderr||'')
  if(result.error)throw result.error
  if(typeof redNames==='string') {
    const marker=redNames==='browser-owner'?'DONOR_RESET_OUTLIVED_EDITOR':redNames==='browser-initial'?'DONOR_INITIAL_READ_NOT_SERIALIZED':'DONOR_BUDGET_PANEL_MISSING'
    if(result.status===0 || !/AssertionError/.test(result.stderr||'') || !(result.stderr||'').includes(marker) || /TimeoutError/.test(result.stderr||''))throw new Error(`${name}: missing mounted budget behavioral assertion`)
    return
  }
  if(!redNames.length) {if(result.status!==0)throw new Error(`${name} failed (${result.status}); inspect retained output`);return}
  if(result.status===0)throw new Error(`${name}: behavioral counterexample unexpectedly passed`)
  const events=(result.stdout||'').split('\n').filter(Boolean).map(line=>JSON.parse(line))
  const failures=events.filter(event=>event.Action==='fail'&&event.Test).map(event=>event.Test)
  if(/\[build failed\]|undefined:|syntax error/.test((result.stdout||'')+(result.stderr||'')))throw new Error(`${name}: compilation failure is not a red`)
  for(const test of redNames)if(!failures.includes(test))throw new Error(`${name}: missing behavioral failure ${test}`)
  fs.writeFileSync(`egress-proof/${name}.failures.json`,JSON.stringify(failures,null,2))
}
const packages=['./internal/egressconfig','./internal/egressdesired','./internal/egressexec','./internal/cellulardata','./cmd/mdd-core']
try {
  const failed=[]
  for (const task of [
    ['green-core',['go','test','-race','-count=1','-timeout','5m','-json',...packages],'go-runtime'],
    ['green-provider',['go','test','-race','-count=1','-timeout','5m','-json','./internal/service'],'providers/vowifi-go'],
    ['green-eap',['go','test','-race','-count=1','-json','./engine/swu/ikev2'],'providers/vowifi-go/upstream/vowifi-go'],
    ['green-adapter',['node','tests/mddNetworkAdapter.mjs'],'webui'],
    ['green-browser',['npm','run','test:egress-browser'],'webui'],
  ]) {try {run(...task)} catch(error) {failed.push(error)}}
  if(failed.length)throw new AggregateError(failed,'Focused batch failed; inspect all retained outputs')

  mutate('go-runtime/internal/egressexec/cellular.go',
    'profile.RuntimeError = "cellular_lease_unavailable"',
    'client.stopUnused(ctx, map[string]struct{}{}); if err != nil { return copy, err }\n\t\t\tprofile.RuntimeError = "cellular_lease_unavailable"')
  mutate('go-runtime/internal/egressexec/render.go','allReady = false\n\t\t\tstatus.Error = err.Error()',
    'if err != nil { return result, err }\n\t\t\tallReady = false\n\t\t\tstatus.Error = err.Error()')
  mutate('go-runtime/internal/egressexec/runner.go','runtimeProxy := document.Proxy',
    'if document.Generation == runner.applied && runner.runtimeReady() { return }\n\truntimeProxy := document.Proxy')
  mutate('go-runtime/internal/egressdesired/desired.go',
    'result.RequestedAttempt == document.AttemptID && result.RequestedGeneration == document.Generation &&\n\t\t\t(result.Outcome == "rolled_back" || result.Outcome == "not_applied" || result.AttemptID == document.AttemptID && result.DesiredGeneration == document.Generation)',
    'result.DesiredGeneration == document.Generation')
  mutate('go-runtime/internal/cellulardata/service.go',
    'if current.stopError != nil {\n\t\treturn current.stopError',
    'if current.stopError != nil {\n\t\tcurrent.service.mu.Lock(); delete(current.service.byLine,current.lineID); delete(current.service.byID,current.id); current.service.mu.Unlock()\n\t\treturn current.stopError')
  mutate('go-runtime/internal/cellulardata/service.go',
    'if !ready {', 'if false && !ready {')
  mutate('go-runtime/internal/egressexec/runner.go',
    'if stopErr := runner.stopPair(); stopErr != nil {\n\t\trunner.failWithCandidate(document.Generation, "failed exit process could not stop", false, result.Status)\n\t\treturn\n\t}',
    'if runner.child != nil { _ = runner.child.Stop(5 * time.Second); runner.child = nil }')
  mutate('go-runtime/internal/egressexec/runner.go',
    'func (runner *executor) invalidateDependencies(status *Status, candidate Rendered) {',
    'func (runner *executor) invalidateDependencies(status *Status, candidate Rendered) { return;')
  mutate('go-runtime/internal/egressexec/runner.go',
    '(runner.child != nil && runner.child.Running() || runner.xrayChild != nil && runner.xrayChild.Running()) {',
    'runner.runtimeReady() {')
  mutate('go-runtime/internal/egressexec/cellular.go',
    'if current == lease && ctx.Err() == nil {', 'if false && current == lease && ctx.Err() == nil {')
  mutate('go-runtime/cmd/mdd-core/egress_attempt_unix.go',
    'if !line.ProviderPresent || line.Code != "provider_reachable" || line.ProcessGeneration != attempt.Drain.ExpectedGenerations[id] {\n\t\t\t\tbreak',
    'if !line.ProviderPresent || line.Code != "provider_reachable" || line.ProcessGeneration != attempt.Drain.ExpectedGenerations[id] {\n\t\t\t\treturn errors.New("original owner unknown")')
  mutate('go-runtime/internal/egressconfig/donor_budget.go',
    'if reset && b.Grant != nil {','if false && reset && b.Grant != nil {')
  mutate('go-runtime/internal/egressconfig/donor_budget.go',
    'if !reset && limit < committed {','if false && !reset && limit < committed {')
  mutate('go-runtime/internal/egressconfig/donor_budget.go',
    'if remaining < 1024 {','if false && remaining < 1024 {')
  mutate('go-runtime/internal/egressconfig/donor_budget.go',
    'if grant.MaxBytes > remaining {','if false && grant.MaxBytes > remaining {')
  const expected=['TestCellularPrepareFailurePreservesUnrelatedLeaseAndRenewal','TestCellularProfileBecomesRuntimeSOCKSAndRenewsOneSession',
    'TestEgressApplyUsesExactSnapshotsAndWaitsForRuntimeGeneration','TestRuntimeConfirmationRequiresFreshExactAttempt/old_attempt_with_same_generation',
    'TestExplicitSessionIsMemoryOnlyAndRevocable','TestSOCKSConnectTraversesCoreBrokerAndAgentManager',
    'TestDonorBudgetSurvivesRestartAndCannotBeReissued','TestDonorResetAndAdmissionAreOneTransaction',
    'TestPreparingDonorCannotRenewOrSettleBeforePrepareCompletes','TestExecutorUnconfirmedStopCannotConfirmFailClosed',
    'TestCellularKeepaliveOutlivesSlowSiblingPrepare','TestDeadXrayAndChangedFeedRequireNewMaintenanceAttempt',
    'TestEgressMaintenanceReleasesHealthySubsetAndRetainsUnknownOwner']
  run('red-core',['go','test','-race','-count=1','-timeout','2m','-json','-run',
    'Test(CellularPrepareFailurePreserves|CellularProfileBecomes|EgressApplyUses|RuntimeConfirmationRequires|ExplicitSessionIs|SOCKSConnectTraverses|DonorBudgetSurvives|DonorResetAnd|PreparingDonor|ExecutorUnconfirmedStop|CellularKeepalive|DeadXrayAnd|EgressMaintenanceReleases)',...packages],'go-runtime',expected)
  restore()
  mutate('go-runtime/internal/egressexec/runner.go',
    'func (runner *executor) invalidateDependencies(status *Status, candidate Rendered) {',
    'func (runner *executor) invalidateDependencies(status *Status, candidate Rendered) { return;')
  run('red-rejected-donor',['go','test','-race','-count=1','-timeout','2m','-json','-run','TestCellularProfileBecomesRuntimeSOCKSAndRenewsOneSession','./internal/egressexec'],'go-runtime',
    ['TestCellularProfileBecomesRuntimeSOCKSAndRenewsOneSession'])
  restore()
  mutate('go-runtime/internal/egressexec/runner.go',
    'if !status.RuntimeConfirmed {\n\t\t// This drained attempt',
    'if false && !status.RuntimeConfirmed {\n\t\t// This drained attempt')
  run('red-rejected-attempt',['go','test','-race','-count=1','-timeout','2m','-json','-run','TestEgressApplyUsesExactSnapshotsAndWaitsForRuntimeGeneration','./cmd/mdd-core'],'go-runtime',
    ['TestEgressApplyUsesExactSnapshotsAndWaitsForRuntimeGeneration'])
  restore()
  mutate('go-runtime/internal/egressexec/runner.go',
    'if (runner.child == nil || !runner.child.Running()) && runner.xrayChild != nil',
    'if false && (runner.child == nil || !runner.child.Running()) && runner.xrayChild != nil')
  run('red-consumer-recovery',['go','test','-race','-count=1','-timeout','2m','-json','-run','TestUnchangedXrayPairRestoresMissingSingBoxOnly','./internal/egressexec'],'go-runtime',
    ['TestUnchangedXrayPairRestoresMissingSingBoxOnly'])
  restore()
  mutate('go-runtime/internal/egressexec/xray_process.go',
    'runner.consumerUnconfirmed = true', 'runner.consumerUnconfirmed = false')
  run('red-consumer-unconfirmed',['go','test','-race','-count=1','-timeout','2m','-json','-run','TestUnchangedXrayPairRestoresMissingSingBoxOnly','./internal/egressexec'],'go-runtime',
    ['TestUnchangedXrayPairRestoresMissingSingBoxOnly'])
  restore()
  mutate('webui/src/mdd/views/UnifiedPages.jsx',
    '<DonorBudget key={profile.sim_iccid} card={profile.sim_iccid} lineID={remoteModems.find(modem => modem.iccid === profile.sim_iccid)?.line_id} showToast={showToast}/>',
    '<span />')
  run('red-browser',['npm','run','test:egress-browser'],'webui','browser')
  restore()
  mutate('webui/src/mdd/views/DonorBudget.jsx','if (!owns(operation)) return','if (false && !owns(operation)) return')
  run('red-browser-owner',['npm','run','test:egress-browser'],'webui','browser-owner')
  restore()
  mutate('webui/src/mdd/views/DonorBudget.jsx',
    'active.current = lineID ? operation : null\n    setBusy(Boolean(lineID));',
    'active.current = null\n    setBusy(false);')
  run('red-browser-initial',['npm','run','test:egress-browser'],'webui','browser-initial')
  restore()
  mutate('providers/vowifi-go/upstream/vowifi-go/engine/swu/ikev2/auth.go',
    'fmt.Errorf("%w: %w", ErrInvalidAuthResponse, ErrEAPFailure)',
    'fmt.Errorf("%w: EAP failure", ErrInvalidAuthResponse)')
  run('red-eap',['go','test','-race','-count=1','-json','-run','TestRunIKEAuthFullCompletesRequiredFinalEAPAuth','./engine/swu/ikev2'],
    'providers/vowifi-go/upstream/vowifi-go',['TestRunIKEAuthFullCompletesRequiredFinalEAPAuth/typed_EAP_rejection'])
  restore()
  // Restored source is re-executed after the counterexamples, not just copied back.
  run('restored-core',['go','test','-race','-count=1','-timeout','5m','-json',...packages],'go-runtime')
  run('restored-eap',['go','test','-race','-count=1','-json','-run','TestRunIKEAuthFullCompletesRequiredFinalEAPAuth','./engine/swu/ikev2'],'providers/vowifi-go/upstream/vowifi-go')
  run('restored-browser',['npm','run','test:egress-browser'],'webui')
} finally {restore()}
console.log('Hosted focused regressions and behavioral counterexamples completed')
