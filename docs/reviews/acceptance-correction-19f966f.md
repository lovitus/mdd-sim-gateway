# Acceptance review correction — 2026-09-20

Baseline: `19f966f7d673a074e7218680d6c0ed8b1d530e1c`, complete tree
`49935012e44af8e006a01b6795f26a3c94851885` (reconstructed from the retained CI source archive).
Scope: the four follow-up review claims and the owner's explicit corrections. No runtime behavior,
SIM operation, deployment, local-user workspace or physical device is changed by this correction.

## Confirmed findings

| ID | Priority / area | Evidence at baseline | Correction |
|---|---|---|---|
| A1 | P1 requirement governance | ESIM_DELETE called physical deletion/retained notification transitional and reopened old customization. | Record the owner's accepted final physical-delete/retained-notification/multiply-confirmed-manual-replay direction. No abandoned soft-delete requirement. |
| A2 | P2 validator lifecycle | Required workstream IDs had only nonterminal allowed states. | Keep IDs as historical identity but allow completed, accepted, superseded, deferred and cancelled with reason, decision/evidence references. |
| A3 | P2 release scope | MAC02 and generated macOS summary grouped absent notarization with actual product gaps. | Exclude notarization/.p8 this round; preserve signing, separate future Universal packaging. Android is separately deferred/non-blocking. |
| A4 | P2 evidence continuity | M50 explicitly described earlier outgoing/audio acceptance yet was generically pending_hardware; active instructions rejected local evidence as authoritative input. | Preserve indexed, scoped historical reports and introduce evidence_review_pending. Reconcile old evidence before new tests; latest owner decisions override a stale ledger. |

A1 is a requirement/status error, not a claim that physical deletion newly malfunctioned. The service already
routes standard physical deletion, retains notification archives and requires multiple confirmations for replay;
the retired soft-delete endpoint is not restored. Old safety checks on legacy records are left intact.

## Source of truth and scope

[Current decisions](../decisions/2026-09-20-current-scope.md) record the user-supplied final direction.
[The ledger](../status/README.md) preserves all original 63 requirement texts and immutable source hashes.
A `currentRequirement` overlay explains changed scope without rewriting history. Workstream acceptance based
on a user decision is not hardware acceptance. Validators permit future supported transitions without requiring
edits to the allowed-state list or removing inconvenient IDs.

Windows/macOS/Linux authenticated remote Agents remain the primary requirement. Enabled 4G must be isolated
and fail closed; no sharing/fallback to the host and no overwrite of a user's saved switch. Existing capability
or default-off gates remain unchanged. Android is deferred, not implemented/cancelled; notarization and .p8 are
excluded, not falsely completed. Universal packaging priority is separate and is not inferred from notarization.

## Historical evidence preserved

Eight scoped report records point to exact source lines and excerpt hashes: Linux bearer/deployment recovery,
Chrome page acceptance, Linux readback and macOS user-domain restart, Linux WSS/disconnect isolation, previously
reported outgoing call/audio and incoming-event UI, macOS readback/audio self-test, macOS signing, and the latest
user-supplied workspace/production handoff. Eighteen previously pending-hardware criteria now explicitly carry
existing evidence to reconcile. M51 still records genuine missing recording implementation alongside prior audio
reports; MAC02 retains earlier signing while splitting current scope. Specific cross-device, incoming answer,
DTMF, active-call failure and complete no-leak matrix gaps are not automatically closed.

Report excerpt hashes prove the source text, **not** authenticity of inaccessible physical captures. The initial
classification is reported_not_revalidated; verified existing evidence can later be recorded with metadata,
without pretending a test was newly rerun. No prior test is erased, and no new field pass is manufactured.

The supplied team handoff says its local checkout was fast-forwarded to 19f966f and production was last recorded
at 7f2e129. Those are retained as **reported facts**; this session has not inspected that machine or running binary.
The new repository commit must not be described as a production deployment.

## Verification

`tools/acceptance-regressions.mjs` works against either checkout: all four named assertions fail for their
intended reasons on pinned baseline and pass after correction. The old validator's refusal of documented
terminal states is exercised directly, not inferred from missing imports or a schema-version mismatch.

`tools/repository-check.test.mjs` now also runs the new policy/continuity suite in the existing normal CI gate.
Positive fixtures cover all five terminal/deferral states and a later owner decision changing scope. Negative
fixtures cover missing resolution/evidence, unknown decisions/states, dropped historical links, changed source
excerpts, path/range violations and unsupported hardware claims. Existing archived-criteria/provenance/import
checks remain. Synthetic hashes in tests are explicitly fixtures, never asserted as real device evidence.

No test thresholds are relaxed and no existing runtime, eSIM confirmation, call, credential, recovery, isolation,
paid-operation or release gate is changed. Final source identity and exact CI results belong in the PR and issue
#3 evidence record; this document does not predeclare a remote pass.

## Resumed final-check correction

The first PR candidate allowed future transitions in `validateLedger`, but its normal test entry still
asserted today's status counts, `M50=evidence_review_pending`, Android deferral and eSIM acceptance.
A synthetic future ledger with documented completions and an existing hardware-evidence record passed
validation but failed the normal test suite (`Missing expected exception` in the old missing-evidence
fixture). This would have recreated the same lifecycle problem outside the validator.

The normal suite now builds deliberately invalid fixtures independently of the live ledger, copies
reconciled-evidence report paths, derives counts from the data, and tests grouping against each record's
actual state. Dated original-snapshot assertions remain an explicit audit helper, not a perpetual CI gate.
`acceptance-evolution.test.mjs` runs the full normal entry point in a disposable checkout with all five
supported terminal/deferral states, an extra workstream, changed scope and verified existing evidence.
Only the recursive self-test is suppressed in that child; the real ledger, policy, negative and rendering
checks all run. Synthetic future decisions/hashes exist only in the test fixture and are not project claims.

This fix changes checker tests and documentation only. The current owner decisions and their historical
report index are unchanged. The complete original and corrected failure/pass records are retained in the
PR verification evidence.
