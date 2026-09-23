# PR8 review repairs — implementation candidate

Baseline: `ffb469798c047ba0bc2b7aa41b30348030b32801`. This repair is stacked on PR #8,
not merged main. The owner's subsequent request authorizes code, PR and automated
CI work; it does not authorize production deployment, live SIM mutation or paid
calls/messages. Existing landscape deferral and optional encrypted remembered
login remain unchanged. This document is an implementation map, **not a test-pass
or physical-acceptance claim**. Exact executed results belong in the repair PR.

## Findings and implementation

| Finding | Repair | Regression boundary |
|---|---|---|
| F1 (pre-existing P1) | Preserve an exact cleanup-only IMS dialog through SDP/ACK failure and both wrappers; attach it to the existing Provider guard, never a new dial | Actual SIP/dialog, IMS adapter, runtime wrapper and Backend together; non-2xx BYE followed by confirmed cleanup, one INVITE |
| F2 | Protocol-confirmed final rejection becomes a retained original call/operation/session receipt; Core preserves `outcome=rejected`; Android displays rejection, not a fictitious hangup | Real final rejection, read-only lookup, Provider/Core reopen, durable-write failure and native dial/return UI |
| F3 | Stop checks the End acceptance and persists exact proof before closing runtime; persistence retry cannot repeat an already-confirmed BYE | Stop, deregistration failure, false/no-error End and failed terminal store |
| F4 | Hardware scans stay on readerIO; immutable results are committed on the same main-thread lifecycle owner as share/pause with exact hub/epoch/scan token | Old completion after ownership replacement cannot publish or clear the new scan |
| F5 | SMS read recovery uses the same certificate-identity classifier as Link, not all SSLException instances | Actual Service catch-up with one injected TLS read error; unchanged snapshot, no SMS send |
| F6 | Unreadable bootstrap has an explicit double-confirmed reset path that archives raw encrypted material and preserves keys; no automatic reset | Missing/partial bootstrap files, corrupt bytes, real reset UI, stale writers and known unresolved-operation refusal |

Additional review fixes: retain final dialog before ACK construction/write; avoid
Go typed-nil conversion of a MediaCall pointer to VoiceCall; require positive End
acceptance during Stop; reject old storage wrappers and queued login drafts after
reset; validate ownership marker bytes and repair only a missing marker after
ciphertext authentication. Failed policy admission must not poison healthy writes.

## Storage compatibility and destructive-operation boundary

Normal valid state retains the existing encrypted schema/alias. Only an explicitly
confirmed reset creates a separately named key with an authenticated key header.
The original keys are never deleted. Raw base/new/owned/backup/legacy files are
copied, synced and verified under the app's no-backup `private-state-recovery/`
before replacement. At most eight archives are admitted; failures preserve the
existing files and refuse automatic overwrite. These private archives are not
uploaded or included in diagnostics. Their presence does not imply remote calls
ended, or that a lost original key can be recovered.

Readable unresolved calls and SMS refuse reset. An unreadable state can be reset
only with the explicit warning and typed RESET confirmation; the old bytes/keys
remain in escrow, not falsely declared resolved. Users need authorized gateway
reconciliation for anything whose outcome cannot be recovered. Older APKs cannot
read the post-reset authenticated header and must not be advertised as supported
downgrades. No user installation was reset to test this code; instrumentation is
restricted to the disposable `.qa` package.

## Validation and scope

`pr8-review-regressions.yml` requires six source-compatible test definitions to
fail **behaviorally** against the original ffb source, then requires patched tests
three times with race detection. Android's ordinary workflow includes the new JVM,
Service/UI/storage tests and the existing external process-death suite on API28
and API35; the full Go/Provider/upstream workflow also applies to the stacked PR.
New tests have been authored; execution status is reported separately in the PR.
Synthetic SIP/PCM, scheduler injection and QA storage fixtures do not constitute
carrier, physical-reader, RF-handover, screen-off or battery acceptance.

The optional original-call recovery across an explicitly changed gateway
certificate remains outside these six repairs. The existing pin fence is retained;
do not remove it to make a changed server appear authorized. Existing field
reports remain scoped to their original source/artifact/environment, not erased.
