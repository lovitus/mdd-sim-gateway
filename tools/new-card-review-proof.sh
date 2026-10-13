#!/usr/bin/env bash
set -euo pipefail
test "${GITHUB_ACTIONS:-}" = true
mkdir -p proof-evidence
baseline=ae29abb11436f8ff99e9a944496dcb0a24f493bb
# Exact compiled old dispatch and handler, retaining the new regression.
git show "$baseline:go-runtime/agentlink/server.go" >go-runtime/agentlink/server.go
git show "$baseline:go-runtime/internal/core/modem_recovery.go" >go-runtime/internal/core/modem_recovery.go
if go -C go-runtime test -count=1 -timeout 1m -run '^TestModemRecoveryDisconnectAfterDispatchStaysUnknown$' ./internal/core >proof-evidence/disconnect-red.log 2>&1; then
  echo 'dispatched recovery counterexample unexpectedly passed' >&2; exit 1
fi
grep -Fq 'DISPATCHED_RECOVERY_DOWNGRADED' proof-evidence/disconnect-red.log
git restore --source=HEAD -- go-runtime/agentlink/server.go go-runtime/internal/core/modem_recovery.go
git show "$baseline:webui/src/mdd/views/UnifiedPages.jsx" >webui/src/mdd/views/UnifiedPages.jsx
for scenario in recovery transitions; do
  if (cd webui && MDD_BROWSER_CASE="$scenario" MDD_BROWSER_EVIDENCE="browser-evidence/$scenario-before" node tests/newCardReviewBrowser.mjs) >"proof-evidence/$scenario-red.log" 2>&1; then
    echo "$scenario counterexample unexpectedly passed" >&2; exit 1
  fi
done
grep -Fq 'LATE_RECOVERY' proof-evidence/recovery-red.log
grep -Fq 'STOPPING_START' proof-evidence/transitions-red.log
git restore --source=HEAD -- webui/src/mdd/views/UnifiedPages.jsx
set -o pipefail
go -C go-runtime test -race -count=1 -timeout 1m -run '^TestModemRecovery' ./internal/core 2>&1 | tee proof-evidence/disconnect-green.log
(cd webui && node tests/newCardControl.mjs && node tests/newCardBrowser.mjs && node tests/newCardReviewBrowser.mjs) 2>&1 | tee proof-evidence/browser-green.log
