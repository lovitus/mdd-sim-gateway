# Repository work, current decisions and acceptance

Read docs/decisions/2026-09-20-current-scope.md, docs/status/README.md and acceptance.json before changing scope.
Latest explicit owner decisions govern requirements; a stale ledger or historical checklist must be corrected,
not used to override those decisions. Root TODO summaries are generated; original plans remain historical.

Final eSIM direction is physical deletion + retained deletion notifications + multiply-confirmed manual replay.
Do not reopen abandoned soft deletion/customization. Android is reprioritized by the owner as a draft native app / preview APK;
read docs/decisions/2026-09-21-android-agent.md. Do not merge its draft or claim physical acceptance from emulator tests.
Windows/macOS/Linux remote Agents are the current priority. Enabled 4G must be isolated and fail closed on all
three: no host sharing or default-network fallback, no override of stored user intent, no automatic enabling.
Notarization and .p8 credentials are excluded this round; preserve signing and track Universal work separately.

Preserve scoped existing deployment, browser, isolation and recovery reports. Not revalidated by this review
is not never tested or revoked acceptance. Reconcile source, artifact, environment and missing subcases before
requesting new tests. Local reports can supply traceable evidence or newer decisions; migrate redacted references
into the ledger instead of rejecting them merely because they are local. Unavailable evidence remains explicitly
unverified, not fabricated or silently erased. Repository merge/CI, workspace synchronization and production
version are separate facts. CI checks metadata and consistency, not the physical truth of field reports.

Use one authoritative owner per credential, hardware attachment, paid operation and call lifetime. Preserve exact
SIM/Agent/equipment/generation identity, unknown outcomes, active-call/maintenance guards, disabled rekey choices
and carrier holdoffs. Do not restore Python/VPCD/Asterisk, duplicate UI/call owners, automatic paid retries or
process-wide restarts to hide a missing state transition.

Implement fixes with deterministic pre-fix counterexamples when possible. Run DEVELOPMENT.md checks, including
production graph/context tests and full Linux Core race tests; inspect skipped cases and exact CI head/tree.
No live SIM mutation, notification replay, paid SMS/call or disruptive deployment without specific authorization.
Do not clean unknown files, devices, processes or branches; preserve unique evidence before branch cleanup.

Update the versioned ledger with current scope, reasons, decision references and evidence; regenerate with
`node tools/repository-check.mjs --write`. Preserve workstream identities while allowing supported completed,
accepted, superseded, deferred or cancelled transitions. Product decisions and hardware acceptance are distinct.
