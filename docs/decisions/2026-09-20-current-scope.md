# Current product decisions — 2026-09-20

Authority: the project owner's explicit corrections supplied in the September 20 review conversation.
This records product decisions, not new execution, hardware acceptance, or deployment evidence.
It supersedes conflicting **current** scope text in PR #4 / issue #3; archived original plans remain unchanged.

## ESIM_FINAL — accepted replacement, not another pending customization

Final direction: **standard physical eSIM profile deletion + retained deletion notifications + manual replay
with multiple explicit confirmations**. The owner accepted this replacement after abandoning the proposed
retain-profile/rename/disable/report-delete scheme. Do not reopen soft deletion as a product requirement,
call the accepted implementation a transition, or infer that operators permit an unsupported soft-delete operation.
Existing checks for legacy soft-deleted records are safety protections, not a request to restore that feature.

Relevant code already exists: `standard_delete.go`, `notification_archive.go`, the mounted eSIM page and
`DeletionNotifications.jsx`. Accepting the direction does not assert that a fresh destructive card test occurred.
No new physical deletion, notification replay, or credential/activation-code disclosure is authorized by this review.

## PRIMARY_AGENTS — current delivery scope

Windows, macOS and Linux Agents are the primary targets; each must support authenticated remote access.
Android unified reader support is **deferred and non-blocking for this delivery**, not implemented or cancelled.
Preserve existing per-platform capability gates; scope requirements alone do not prove every modem/backend is qualified.

When the user enables 4G, isolation is mandatory and fail-closed on **all three** primary platforms.
If isolation cannot be established/maintained, do not admit data, share the cellular interface with the host,
or fall back to the host/default route. Never override the user's stored 4G switch or enable an off switch
in order to make a test pass. An off/default-disabled capability is not evidence of working enabled isolation.

## MACOS_RELEASE — signing is not notarization

Notarization and use of `.p8` credentials are **excluded from this round and do not block delivery**.
Retain existing Developer ID/signing checks; do not describe signed builds as notarized.
Universal packaging is a separate future release consideration whose priority remains to be decided, not a
reason to reinstate notarization. The current arm64 release and its evidence remain separately represented.

## EVIDENCE_CONTINUITY — evidence is not reset by a new review

A prior report not independently revalidated in this review is **existing evidence to reconcile**, not proof
that the project has never been tested. Preserve its scope, date, source and known artifact before deciding
which *specific* gaps require tests. Contradictory or obsolete reports must be annotated, not silently erased.
Latest explicit owner decisions govern scope even before a stale ledger is updated. Redacted, traceable
machine-local evidence may update the ledger after provenance reconciliation; an inaccessible local note is
neither automatically true nor automatically invalid. CI verifies record structure, not physical events.

The supplied handoff reports that the local checkout was fast-forwarded to `19f966f` with no source diff;
it separately identifies `7f2e129` as the latest recorded production version. This session has not inspected
that machine or its running binaries. A repository merge and green CI do **not** change production evidence.
