#!/usr/bin/env bash
# One-time hosted proof. Revert only new behavioral guards in a disposable
# checkout, retaining the added types/signatures so compile failures cannot pass.
set -euo pipefail
test "${GITHUB_ACTIONS:-}" = true
mkdir -p proof-evidence
node --input-type=module <<'NODE'
import {readFile,writeFile} from 'node:fs/promises'
async function edit(path,before,after) {
  const source=await readFile(path,'utf8')
  if(!source.includes(before))throw new Error(`proof setup does not match ${path}`)
  await writeFile(path,source.replace(before,after))
}
await edit('go-runtime/internal/linecatalog/store.go',
`\t\tif len(expectedCard) > 1 || len(expectedCard) == 1 && line.CardID != expectedCard[0] {
\t\t\treturn ErrRuntimeCardChanged
\t\t}
`,'')
for(const path of ['go-runtime/agentlink/server.go']) {
  for(const type of ['ModemRecoveryResponse','ModemPolicyResponse'])await edit(path,
`\tif command.ExpectedTarget != nil && !command.ExpectedTarget.Matches(target) {
\t\treturn ${type}{}, ErrGenerationMismatch
\t}
`,'')
}
const guard=`\tif input.ExpectedTarget == nil || !input.ExpectedTarget.Matches(view.target()) {
\t\twriteDevicePolicyError(response, agentlink.ErrGenerationMismatch)
\t\treturn
\t}
`
await edit('go-runtime/internal/core/device_policy.go',guard,'')
await edit('go-runtime/internal/core/device_policy.go',guard,'')
await edit('go-runtime/internal/core/modem_recovery.go',
'map[string]string{"code": receipt.ErrorCode, "state": "unknown", "operation_id": command.OperationID}',
'map[string]string{"code": receipt.ErrorCode}')
await edit('webui/src/api.js',
"{ operation_id: operationID(`react-vowifi-${action}`), expected_card_id: cardID }",
"{ operation_id: operationID(`react-vowifi-${action}`) }")
NODE
expect_go_red() {
  local package="$1" test_name="$2" log="proof-evidence/$2-red.log"
  if go -C go-runtime test -count=1 -timeout 1m -run "^${test_name}$" "$package" >"$log" 2>&1; then
    cat "$log"
    echo "counterexample unexpectedly passed: $test_name" >&2
    exit 1
  fi
  if ! grep -Fq -- "--- FAIL: $test_name" "$log" || grep -Eq 'build failed|undefined:|syntax error' "$log"; then
    cat "$log"
    exit 1
  fi
  echo "Confirmed behavioral red: $test_name"
}
expect_go_red ./internal/linecatalog TestRuntimeIntentRejectsReplacedCardEvenForNoOp
expect_go_red ./agentlink TestConfirmedModemCommandsDoNotRetargetAfterResolution
expect_go_red ./internal/core TestDeviceWritesRejectChangedConfirmationAtSamePolicyRevision
expect_go_red ./internal/core TestModemRecoveryHandlerRecordsTransportUnknownWithoutRetry
if (cd webui && node tests/newCardControl.mjs) >proof-evidence/control-red.log 2>&1; then
  echo 'control counterexample unexpectedly passed' >&2
  exit 1
fi
grep -Fq 'AssertionError' proof-evidence/control-red.log
# The page counterexample uses the exact old implementation, not a mock.
git show 9a9aa783a5681ec203cd977b06c9582979dc7331:webui/src/mdd/views/UnifiedPages.jsx >webui/src/mdd/views/UnifiedPages.jsx
if (cd webui && MDD_BROWSER_EVIDENCE=browser-evidence/new-card-before node tests/newCardBrowser.mjs) >proof-evidence/browser-red.log 2>&1; then
  echo 'old page counterexample unexpectedly passed' >&2
  exit 1
fi
grep -Fq 'NEW_CARD_SHORTCUT' proof-evidence/browser-red.log
echo 'Confirmed old page behavior; fixed Chromium evidence was captured before reverting.'
# Restore only proof-edited paths in this disposable hosted checkout.
git restore --source=HEAD -- go-runtime/internal/linecatalog/store.go go-runtime/agentlink/server.go \
  go-runtime/internal/core/device_policy.go go-runtime/internal/core/modem_recovery.go \
  webui/src/api.js webui/src/mdd/views/UnifiedPages.jsx
git show ef465e837fa12b21ebc4132d0e06fb2a5d64c950:webui/src/goV1Adapter.js >webui/src/goV1Adapter.js
if (cd webui && node tests/newCardControl.mjs) >proof-evidence/offline-intent-red.log 2>&1; then
  echo 'offline intent counterexample unexpectedly passed' >&2; exit 1
fi
grep -Fq 'AssertionError' proof-evidence/offline-intent-red.log
git restore --source=HEAD -- webui/src/goV1Adapter.js
git show ef465e837fa12b21ebc4132d0e06fb2a5d64c950:webui/src/mdd/views/SimConfig.jsx >webui/src/mdd/views/SimConfig.jsx
if (cd webui && MDD_BROWSER_CASE=late-save MDD_BROWSER_EVIDENCE=browser-evidence/late-save-before node tests/newCardBrowser.mjs) >proof-evidence/late-save-red.log 2>&1; then
  echo 'late-save counterexample unexpectedly passed' >&2; exit 1
fi
grep -Fq 'LATE_SIM_SAVE' proof-evidence/late-save-red.log
git restore --source=HEAD -- webui/src/mdd/views/SimConfig.jsx
go -C go-runtime test -race -count=1 -timeout 2m \
  -run '^(TestRuntimeIntentRejectsReplacedCardEvenForNoOp|TestConfirmedModemCommandsDoNotRetargetAfterResolution|TestDeviceWritesRejectChangedConfirmationAtSamePolicyRevision|TestModemRecoveryHandlerRecordsTransportUnknownWithoutRetry)$' \
  ./agentlink ./internal/linecatalog ./internal/core
(cd webui && node tests/newCardControl.mjs && node tests/newCardBrowser.mjs)
echo 'Fixed regressions passed again after each compiled behavioral red.'
