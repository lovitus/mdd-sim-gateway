#!/usr/bin/env bash
# Only copy tests into a disposable pinned pre-fix checkout. Never copy fixes.
set -euo pipefail
baseline=${1:?pre-fix checkout required}
root=$(cd "$(dirname "$0")/../.." && pwd)
expected=0c3a64ca6fa7292b4f3c0fedef9fc2b4b993590c
if [[ $(git -C "$baseline" rev-parse HEAD) != "$expected" || $(cd "$baseline" && pwd -P) == "$root" ]]; then
 echo 'Negative controls require a separate checkout of the exact pre-fix baseline.' >&2
 exit 1
fi
for path in \
 go-runtime/vowifiipc/review_health_counterexample_test.go \
 providers/vowifi-go/internal/usernet/review_liveness_test.go \
 providers/vowifi-go/upstream/vowifi-go/engine/swu/review_liveness_counterexample_test.go \
 providers/vowifi-go/upstream/vowifi-go/runtimehost/review_registration_counterexample_test.go; do
 cp "$root/$path" "$baseline/$path"
done
set +e
GOWORK=off go -C "$baseline/go-runtime" test -json -race -count=1 -timeout 2m -run '^TestLivenessReview' ./vowifiipc > liveness-negative-core.jsonl 2>&1
core=$?
GOWORK=off go -C "$baseline/providers/vowifi-go" test -json -race -count=1 -timeout 2m -run '^TestLivenessReview' ./internal/usernet > liveness-negative-provider.jsonl 2>&1
provider=$?
GOWORK=off go -C "$baseline/providers/vowifi-go/upstream/vowifi-go" test -json -race -count=1 -timeout 2m -run '^TestLivenessReview' ./engine/swu ./runtimehost > liveness-negative-upstream.jsonl 2>&1
upstream=$?
set -e
if [[ $core != 1 || $provider != 1 || $upstream != 1 ]]; then cat liveness-negative-*.jsonl;exit 1;fi
node <<'JS'
const fs=require('node:fs');
const expected=new Map([
 ['TestLivenessReviewInitialRegisterPreservesRetryAfter','initial REGISTER discarded Retry-After evidence'],
 ['TestLivenessReviewCountsEachFailedProbeOnce','budget charged twice'],
 ['TestLivenessReviewExpiredRegistrationIsNotReady','expired IMS registration remains ready'],
 ['TestLivenessReviewCancelledManualRecoveryDoesNotWaitForOwner','cancelled manual recovery queued behind maintenance'],
 ['TestLivenessReviewCloseHonorsDeadlineWhileMaintenanceBusy','Close ignored deadline behind maintenance lock'],
 ['TestLivenessReviewStackDrivesMaintenanceWithoutPrematureClose','stack never drove liveness maintenance'],
 ['TestLivenessReviewPersistentIMSRequestsBudgetedRecovery','persistent IMS failure never reaches Core failure budget'],
]);
const failed=new Set(),diagnostics=new Set();
for(const path of ['liveness-negative-core.jsonl','liveness-negative-provider.jsonl','liveness-negative-upstream.jsonl']){
 for(const line of fs.readFileSync(path,'utf8').split('\n').filter(Boolean)){
  if(line.startsWith('go: downloading '))continue;
  const e=JSON.parse(line);
  if(/DATA RACE|panic:|build failed/.test(e.Output||''))throw Error('Invalid counterexample: '+e.Output);
  if(e.Test&&e.Action==='fail'){if(!expected.has(e.Test))throw Error('Unexpected failed test '+e.Test);failed.add(e.Test)}
  if(e.Test&&expected.has(e.Test)&&(e.Output||'').includes(expected.get(e.Test)))diagnostics.add(e.Test);
 }
}
for(const name of expected.keys()){if(!failed.has(name)||!diagnostics.has(name))throw Error('Missing intended failure '+name);console.log('CONFIRMED pre-fix: '+name)}
JS
