#!/usr/bin/env bash
# Demonstrate that the reviewed defects fail on the pinned pre-fix code.
# Run only in disposable CI checkouts; never use a modem, SIM or carrier.
set -euo pipefail
baseline=${1:?baseline checkout is required}
root=$(cd "$(dirname "$0")/../.." && pwd)
for dir in go-runtime/adminauth go-runtime/internal/agentdata go-runtime/internal/agentmedia providers/vowifi-go/internal/service; do
  cp "$root/$dir"/review_*_test.go "$baseline/$dir/"
done
set +e
GOWORK=off go -C "$baseline/go-runtime" test -json -race -count=1 -timeout 2m \
  -run '^TestReview' ./adminauth ./internal/agentdata ./internal/agentmedia > baseline-core-tests.jsonl 2>&1
core_status=$?
GOWORK=off go -C "$baseline/providers/vowifi-go" test -json -race -count=1 -timeout 2m \
  -run '^TestReview' ./internal/service > baseline-provider-tests.jsonl 2>&1
provider_status=$?
set -e
if [[ $core_status != 1 || $provider_status != 1 ]]; then
  cat baseline-core-tests.jsonl baseline-provider-tests.jsonl
  echo 'Counterexamples must produce ordinary test failures in both modules.' >&2
  exit 1
fi
node <<'JS'
const fs = require('node:fs');
const expected = new Set([
  'TestReviewOverloadedLoginsDoNotAllocatePeerHistory',
  'TestReviewLoginHistoryHasBoundAndExpires',
  'TestReviewDataRejectsReservationExpiringDuringUpgrade',
  'TestReviewAcquireRechecksDataDeadlineAfterWait',
  'TestReviewMediaRejectsReservationExpiringDuringUpgrade',
  'TestReviewFailedRuntimeStopRestoresCallGuard',
  'TestReviewFailedCallCommitRetainsUnconfirmedCall',
  'TestReviewLateHangupCannotClearReplacementCall',
  'TestReviewFailedMediaAttachmentRetainsUnconfirmedCall',
  'TestReviewLocalHangupStopsRemoteEndObserver',
  'TestReviewGuardSurvivesTransientReceiptReservationFailure',
]);
const failed = new Set();
for (const name of ['baseline-core-tests.jsonl', 'baseline-provider-tests.jsonl']) {
  for (const line of fs.readFileSync(name, 'utf8').split('\n').filter(Boolean)) {
    if (line.startsWith('go: downloading ')) continue;
    let event;
    try { event = JSON.parse(line); } catch { console.error(line); process.exit(1); }
    if (/DATA RACE|build failed|panic:/.test(event.Output || '')) {
      console.error('Unexpected build/race/panic failure:', event.Output); process.exit(1);
    }
    if (event.Action === 'fail' && event.Test) {
      if (!expected.has(event.Test)) { console.error('Unexpected failing test:', event.Test); process.exit(1); }
      failed.add(event.Test);
    }
  }
}
for (const name of expected) {
  if (!failed.has(name)) { console.error('Counterexample did not fail:', name); process.exit(1); }
  console.log('CONFIRMED against pre-fix source:', name);
}
JS
