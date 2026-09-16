# MDD SIM Gateway — follow-up review and remediation ledger

## Scope and provenance

Review baseline: `80e2642141e2a27891b5a8127a2c45c2b9c575f3`.
Current development branch: `codex/mdd-custom-ui-batch` at
`f8a8c33793c95308e95043a4cb6acd3ebe01eeed`.
Additional team hardening branch: `codex/review-hardening-f8a8c337` at
`dde262a5c3724323a20c2d5b7d5cf5bb09ddff84`.

This review examines the five-commit delta since the previous review, all
changed production lifetime paths, their callers and regression tests, the
nested-module CI selection, and adjacent data/media/USB-IP admission paths.
It is not a claim of a fresh line-by-line audit of the entire repository.

The source archive downloaded from GitHub Actions run `35123415328` was
expanded in a disposable sandbox. Its complete Git tree, including archived
tracked files matching ignore rules, was verified as
`bed0f9914cbf74ce483ab73847473a4605f20eea`, matching the GitHub commit.
No production host, modem, SIM, carrier account, or live paid operation is used.

## Original findings rechecked

| ID | Original finding | Disposition |
| --- | --- | --- |
| O1 | Agent credential revocation versus pending control handshake | Closed: epoch captured before token lookup and checked during locked admission. |
| O2 | Old-password derivation completes after password replacement | Closed: password epoch checked before session publication. |
| O3 | Unbounded concurrent login scrypt work | Closed: global and per-peer admission precede derivation. |
| O4 | SMS deduplication scoped only to process generation | Closed: stable paid-message receipts with request/SIM scope and conservative legacy tombstones. |
| O5 | Registration not counted as in-flight work | Closed: registration reservation gates stop, drain, and conflicting paid operations. |
| O6 | Connection failure waits before cancelling operations | Cancellable-work defect closed. Noncancellable native work still intentionally retains ownership and may delay reconnect. |
| O7 | New runtime overwrites resources retained after failed Stop | Closed: retained runtime pointer gates Start; failed cleanup preserves ownership. |
| O8 | New SWu/IKE package tests omitted from CI | Closed: both nested `engine/swu/...` and `runtimehost/...` are directly race-tested and vetted. |

## Existing team hardening independently revalidated

The previous run's downloaded JSON test evidence contains exactly eleven
expected pre-fix failures and no unexpected test failures, build failures,
panics, or race reports in the negative controls. The following regression
families are included unchanged in the proposed PR; their implementation is
attributable to the existing team branch, not newly authored in this pass.

| ID | Confirmed defect on f8a8c337 | Counterexample test(s) | Fix reviewed in dde262a5 |
| --- | --- | --- | --- |
| H1 | Login-peer history grows on rejected overload and retains unbounded addresses | TestReviewOverloadedLoginsDoNotAllocatePeerHistory; TestReviewLoginHistoryHasBoundAndExpires | Admission before history allocation, bounded history, expiration sweep. |
| H2 | Data capability expires during upgrade or while Acquire waits | TestReviewDataRejectsReservationExpiringDuringUpgrade; TestReviewAcquireRechecksDataDeadlineAfterWait | Deadline and exact-record checks at attachment and acquisition. |
| H3 | Media preparation capability expires during upgrade | TestReviewMediaRejectsReservationExpiringDuringUpgrade | Recheck preparation deadline at attachment without expiring active calls. |
| H4 | Failed runtime Stop leaves a paid call without its safety guard | TestReviewFailedRuntimeStopRestoresCallGuard | Restore exact-call guard after failed BYE. |
| H5 | Receipt failure or failed media attachment plus failed BYE discards a call handle | TestReviewFailedCallCommitRetainsUnconfirmedCall; TestReviewFailedMediaAttachmentRetainsUnconfirmedCall | Retain cleanup ownership and retry independently of browser freshness. |
| H6 | Late hangup completion clears a replacement call | TestReviewLateHangupCannotClearReplacementCall | Finish only the exact call lifetime. |
| H7 | Remote-end observer survives successful local hangup | TestReviewLocalHangupStopsRemoteEndObserver | Explicit per-call completion channel retires observers. |
| H8 | Guard exits after transient receipt reservation failure | TestReviewGuardSurvivesTransientReceiptReservationFailure | Preserve bounded retry ownership when failure precedes carrier dispatch. |

Previous hardening run: 1,133 Core top-level tests passed (2 skipped), 678
boundary test executions passed, 170 Provider top-level tests passed, 1,359
upstream top-level tests passed, and 120 paid-call stress executions passed.
These are counts from the archived JSON events, not new local test runs.

## New findings and fixes in this pass

### F1 — P2: media Acquire can claim a replacement reservation

Location before fix: `go-runtime/internal/agentmedia/broker.go`, `Acquire`.
The function waits on the original record's readiness channel but then looks
up and claims whichever record currently occupies the same session ID. Unlike
the hardened data broker, it does not compare object identity after the wait.

Counterexample: attach the original media connection; pause acquisition after
it snapshots readiness and releases the lock; revoke that reservation; reserve
and attach a different generation and token under the same session ID; resume
the old acquisition. The old implementation claims the replacement.

Fix: capture the expected reservation and require the current pointer to match
under the broker mutex before changing `claimed`. The regression also verifies
that the new owner can still acquire the unconsumed replacement.

Test: `TestFollowupMediaAcquireRejectsReplacement`.
This is an internal capability-lifetime defect requiring same-ID replacement;
it is not an arbitrary unauthenticated cross-agent takeover claim.

### F2 — P2: revoking unready reservations does not wake their waiters

Locations before fix: data/media broker `Acquire`, `Revoke`, disconnect and
purge paths; data `RevokeSession`; media exact-record removal.
Removing a reservation neither signals its readiness wait nor provides a
revocation channel. Acquisition continues waiting until the caller's timeout,
even though the authority it awaits has already been revoked.

Fix: complete the existing readiness wait on attachment OR removal, under the
same mutex. Acquisition rechecks exact-record identity and existence after
waking. The common signalling helper is idempotent to prevent double close.
Active media still follows explicit call ownership rather than preparation TTL.

Tests: `TestFollowupMediaRevocationWakesAcquire` (revoke, disconnect, expiry)
and `TestFollowupDataRevocationWakesAcquire` (revoke, session, disconnect, expiry).
Expiry tests execute the real purge path; they do not claim a new autonomous
expiration timer. Existing callers normally use bounded acquisition contexts,
so the observed impact is avoidable waiting/resource retention, not proof of
an unbounded public goroutine leak.

### F3 — P2: a late failed data acknowledgement revokes a new reservation

Location before fix: `go-runtime/internal/agentdata/broker.go`, failed ready
acknowledgement in `ServeHTTP`.
The success path checks the exact record, but the failure path invokes
`Revoke(streamID)`. If the original reservation is replaced between attachment
and the write failure, this deletes the replacement rather than the old record.

Fix: remove and close the exact expected reservation; delete the map entry only
when it still contains that object. Never revoke a replacement by a reused ID.

Test: `TestFollowupDataAckFailurePreservesReplacement`. A fault-injected
WebSocket writer replaces the reservation at the acknowledged-connection
boundary and forces that old write to fail. This exercises the actual HTTP
handler with loopback sockets; no hardware is involved.

## Counterchecks and concerns not promoted to findings

- The changed ESP selection test now uses unoffered AES-192 because the offer
  already includes AES-128 and AES-256. No production selection validation was
  weakened by that test-only correction.
- Media's preparation deadline intentionally stops governing an attached call.
  Expiring all attached media at that deadline would introduce dropped calls;
  no such change is made.
- USB/IP has autonomous pairing expiration and deadline-bounded acknowledgements.
  The data/media expiry reports are not copied to USB/IP without an independent
  reproduction. No USB/IP defect is claimed in this review.
- The global agent credential epoch can conservatively reject unrelated pending
  handshakes on rotation. It does not disconnect unrelated established agents.
  This is not reopened as an authentication bypass.
- The paid-message receipt refreshes current runtime status rather than replaying
  an old process snapshot. The initial cross-generation concern remains closed.
- Repeated use of a browser session was investigated; its terminal removal and
  per-call ownership matter. No speculative late-EndStream finding is included.
- Waiting for genuinely noncancellable native work remains a documented
  limitation. Dropping the wait without transferring ownership is not a fix.

## Validation gates and final evidence

`review-counterexamples.sh` retains the eleven negative controls against
`f8a8c337`. `followup-counterexamples.sh` independently checks out `dde262a5`
and requires the nine exact new failing scenarios (four top-level tests,
including seven revocation subcases). It verifies the expected diagnostic
strings and rejects compilation failures, panics, races, missing scenarios,
and unrelated failures as invalid evidence.

The same new tests must pass on the PR source under the race detector, including
20 repetitions. The workflow also runs Core tests, reviewed-boundary race tests,
Provider tests, direct nested SWu/IKE/runtime tests, audio-helper tests, vet,
and the WebUI test/build/embedded-asset checks. All tests use fixtures or local
simulation. The final PR validation comment records run IDs and exact SHAs.

Local validation includes source/tree verification, Go formatting, shell syntax,
and patch whitespace. This sandbox has Go 1.23.2 and cannot resolve the Go
1.26 toolchain download host; full Go execution is performed by GitHub-hosted
runners, not falsely reported as a local run. No claim is made that this review
reruns the full signed macOS/Windows release packaging or carrier interoperability.
