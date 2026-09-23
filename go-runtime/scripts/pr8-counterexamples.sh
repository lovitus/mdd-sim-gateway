#!/usr/bin/env bash
# Negative controls copy tests, never fixes, into the pinned pre-review source.
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
baseline=${1:?expected separate baseline checkout}
expected=ffb469798c047ba0bc2b7aa41b30348030b32801
[[ $(git -C "$baseline" rev-parse HEAD) == "$expected" ]]
mkdir -p "$root/pr8-evidence"
for path in providers/vowifi-go/internal/service/pr8_repair_test.go providers/vowifi-go/internal/ims/pr8_cleanup_test.go providers/vowifi-go/upstream/vowifi-go/runtimehost/voicehost/pr8_accepted_cleanup_test.go; do cp "$root/$path" "$baseline/$path"; done
negative() {
  local module=$1 package=$2 test=$3 diagnostic=$4 file=$5 status
  set +e
  GOWORK=off go -C "$baseline/$module" test -json -count=1 -timeout 90s -run "^${test}$" "$package" > "$root/pr8-evidence/$file.jsonl" 2>&1
  status=$?
  set -e
  [[ $status == 1 ]]
  node - "$root/pr8-evidence/$file.jsonl" "$test" "$diagnostic" <<'JS'
const fs=require('node:fs');const [file,test,message]=process.argv.slice(2);
const text=fs.readFileSync(file,'utf8');
const events=text.split('\n').filter(x=>x.startsWith('{')).map(x=>JSON.parse(x));
const out=events.map(x=>x.Output||'').join('');
if(!events.some(x=>x.Test===test&&x.Action==='fail')||!out.includes(message)||/build failed|panic:|DATA RACE/.test(out))throw Error('Not the expected counterexample: '+file+'\n'+text);
if(events.some(x=>x.Action==='fail'&&x.Test&&!(x.Test===test||x.Test.startsWith(test+'/'))))throw Error('Unexpected test failure');
console.log('Confirmed expected baseline failure: '+test);
JS
}
negative providers/vowifi-go ./internal/service TestPR8AcceptedMediaFailureRetainsExactCleanupAcrossAllLayers 'accepted dialog lost its callable cleanup owner' accepted-media
negative providers/vowifi-go ./internal/service TestPR8RejectedCallHasDurableOutcomeWithoutAnotherInvite 'final rejection has no recoverable exact outcome' rejected-call
negative providers/vowifi-go ./internal/service TestPR8StopPersistsCallTerminalEvenWhenRuntimeCloseFails 'Stop discarded exact terminal evidence' stopped-call
negative providers/vowifi-go ./internal/service TestPR8StopRetainsReceiptRetryWithoutRepeatingConfirmedBye 'failed receipt write hidden' receipt-write
negative providers/vowifi-go ./internal/service TestPR8StopRequiresAcceptedTerminalResult 'Stop accepted an unconfirmed terminal response' unaccepted-end
negative providers/vowifi-go ./internal/ims TestPR8MediaFailureWithRejectedByeReturnsCleanupHandle 'media failure discarded unconfirmed dialog' ims-cleanup
negative providers/vowifi-go/upstream/vowifi-go ./runtimehost/voicehost TestPR8AcceptedAckOrSDPFailureKeepsDialogForBye 'accepted dialog disappeared after local SDP failure' accepted-sdp
