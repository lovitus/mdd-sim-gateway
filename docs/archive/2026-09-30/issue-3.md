# Issue #3 Before September 30 Reconciliation

Historical snapshot only; the statements below are preserved as written, not current
scope, deployment or acceptance instructions. Current status belongs to the
[versioned ledger](../../status/README.md), [deployment receipts](../../../DEPLOYMENT.md)
and [live issue](https://github.com/lovitus/mdd-sim-gateway/issues/3).

- Captured on: 2026-09-30.
- Original issue body last updated: `2026-09-21T14:51:50Z`.
- Original title: Current delivery scope, acceptance evidence reconciliation and remaining product work.
- Original body SHA-256 (UTF-8, before the archive's final newline):
  `ecdf70f22182b96308651cb01cd682c3fa04a998defb3d69effe7e5345c69f2c`.
- Source: https://github.com/lovitus/mdd-sim-gateway/issues/3.

The draft, old main, artifact, deployment and branch assertions below were later
superseded. Their historical CI/evidence scope is not erased or claimed as rerun.
The original recording implementation statement was already correct.

---

## Current owner priority — native Android reprioritized on 2026-09-21

The owner's new request supersedes the earlier Android deferral: implement a friendly native Android reader/remote-call/SMS agent, validate it, open a **draft** PR and release a test APK. **[PR #7](https://github.com/lovitus/mdd-sim-gateway/pull/7) remains draft and unmerged.** [Android preview c639dd2b7700](https://github.com/lovitus/mdd-sim-gateway/releases/tag/android-preview-c639dd2b7700) is published from `c639dd2b77007a023e2ce44214f1a79503bb5dc3`, tree `cfbdabc80e6be86f937d58dd4c8ba6d7b47c661d`. Do not treat the earlier deferred status as the current owner decision, or this preview as a production rollout.

**Main remains `8fc2c29d3bb575f054115a435a40e81036aa9ba9` from merged PR #6.** Its exact post-merge CI #414 / [35518915013](https://github.com/lovitus/mdd-sim-gateway/actions/runs/35518915013) passed. No live server/workstation has been upgraded by the Android work. The preview requires PR #7's matching Core `/v1/mobile/ws` implementation; old/main Core reports update-required. Matching draft Core binaries are available in [PR CI 35612651014](https://github.com/lovitus/mdd-sim-gateway/actions/runs/35612651014).

## Android draft delivery and remaining qualification

- [x] New native setup/Home/Calls/Messages/Readers/Settings, HTTPS and explicitly verified optional pin, QR/paste setup, encrypted Keystore sessions, private notifications and foreground Pause/Stay available.
- [x] Existing Agent hello/health/AKA protocol for supported APDU-level USB CCID slot-0 readers and access-controlled OMAPI channels; bounded exact-card operations and sequenced unchanged health.
- [x] Native server VoWiFi/cellular call paths, incoming answer/reject with existing identities, mute/speaker/DTMF and bounded same-lease media resume; server SMS single submission with persisted uncertain outcome and no automatic paid-operation replay.
- [x] Network callbacks, capped jittered reconnect, session revocation, 30-second socket liveness and no idle wake lock. This is not proof of Doze/OEM reachability or battery life. Reader mode retains the server's 10-second health contract.
- [x] APK built/linted/tested, signed as a non-debuggable **preview**, installed/launched on API 28/35 and published. **10 JVM tests; four instrumentation tests on each API; 176 Core/Agent race tests** passed in [Android run 35612644279](https://github.com/lovitus/mdd-sim-gateway/actions/runs/35612644279). Emulator tests cover setup/Keystore, actual Android audio callbacks and resume, reconnect/session revocation, scoped reader hello/unchanged health. These are loopback/synthetic fixtures, not physical carrier acceptance. The release APK smoke test is distinguished from debug instrumentation.
- [x] Ordinary final-head [main/release CI 35612651014](https://github.com/lovitus/mdd-sim-gateway/actions/runs/35612651014), [review regressions 35612650619](https://github.com/lovitus/mdd-sim-gateway/actions/runs/35612650619) and [liveness 35612650611](https://github.com/lovitus/mdd-sim-gateway/actions/runs/35612650611) all passed. APK/source/license/checksum evidence is attached to the prerelease; no signing private key is published.
- [ ] Qualify physical USB/OMAPI devices and access rules, real line/media/message behavior, cellular handover and screen-off/OEM/battery behavior before production approval. The preview does not supply FCM push, default-dialer replacement, phone-radio/SMS bridging, arbitrary TPDU support or direct cellular radio operation from a USB reader. Phone-installed SIM calling is a system-dialer handoff. Stable owner signing/update continuity remains before a production Android release.

APK SHA-256: `6fe4209ac18dbdf24168eac2b7c1601cc70c10b5b33581b7adf80ae22fce94bd`; disposable preview certificate, not production. Future differently signed previews may require uninstall. [Current Android decision record in draft](https://github.com/lovitus/mdd-sim-gateway/blob/c639dd2b77007a023e2ce44214f1a79503bb5dc3/docs/decisions/2026-09-21-android-agent.md). PR #7 is not to be merged automatically just because its CI and APK release passed.

## Previously completed and merged — preserved

[PR #6](https://github.com/lovitus/mdd-sim-gateway/pull/6) retains the complete previous implementation/test history and source hashes. Main tree `002923066aee61c6df20fe16d0d71978c5ee9b9d` preserves the reviewed 1,359-file source and embedded assets.

- [x] Browser-local recording: existing call owner, explicit consent/exact-call check, stereo local/received channels, mute exclusion, final encoder chunk before Save, bounded output/finalization, stop on disconnect/failure/close, discard on logout/page exit. No upload or persistent browser storage. Native Chromium synthetic recording proof is not real-call/every-browser acceptance.
- [x] Windows cutover: exact SCM PID/native handle captured before stop, bounded exit wait, handle disposal, owned image hash and confirmed candidate release before rollback. ServiceController waiting still polls internally; wider architecture remains open.
- [x] macOS cutover: exact user launchd label/PID, confirmed exit, immediate readiness and deadline-capped backoff, candidate release before rollback. No global process-name termination or periodic restart.
- [x] Private topology diagnostic fields: fixed paths/indices for selected rules through authenticated local APIs, without raw SIM/PIN/APN values; unchanged validation rules. Historical incident cause remains unexplained.
- [x] Ledger: recording implementation is completed; M51 preserves reported audio evidence and separates uncovered field cases. All 63 original criteria, eight historical reports and archived hashes remain preserved. PR #4 cleanup and PR #5 corrected eSIM/notarization/evidence policies remain incorporated.

## Other work genuinely remaining — not silently closed

- [ ] **Installer architecture:** wider Windows least-privilege service/companion design and fully event-driven startup/state waiting; scoped ownership fixes are not those whole designs.
- [ ] **Scoped historical-evidence reconciliation:** retain reported remote-Agent, outgoing/audio, incoming-event UI, browser, bearer/isolation/recovery and deployment results. Match artifacts/environments; identify only missing or contradictory incoming-answer, DTMF, multi-client contention, in-call disconnect, cross-page, cross-vendor and no-leak cases before new authorized tests. Do not restart every previous test from zero.
- [ ] **Identical-observation SIM reinsertion:** authoritative native event/generation evidence and targeted between-samples hardware qualification. Identical samples alone do not prove uninterrupted attachment.
- [ ] **Historical MM forced-close / topology_invalid causes:** unresolved. Recovery success and new field diagnostics do not establish retrospective causes. Current private D-Bus is distinct from an old mmcli stderr path.
- [ ] **Standing maintenance:** large ownership boundaries and maintained upstream fork; not itself a demonstrated release blocker or justification for a wholesale rewrite.

Lack of an online authorized physical device in these reviews limits new observation; it does not revoke earlier acceptance. The eight reports retain source-text hashes, scope and limitations. Main's mixed-criterion acceptance counts remain 19 evidence-review-pending, 18 automated-contract, 13 pending-hardware subcases, two pending-product, 11 not-applicable; these are not independent bug counts. Android's new scope overlay is in draft PR #7, not already merged main.

## Governing decisions

**Final eSIM direction remains physical profile deletion + retained deletion notifications + multiple-confirmation manual replay.** Do not reopen abandoned keep-profile/report-delete soft deletion. **Notarization/.p8 remain excluded this round; signing is preserved; Universal packaging remains a separate future decision.** Android is now reprioritized by the later explicit request, as above.

Windows/macOS/Linux authenticated remote Agents and compulsory fail-closed user-enabled 4G isolation remain requirements: no host sharing/default-route fallback, overriding stored intent or enabling an off/default-disabled feature to pass a test. Latest owner decisions govern stale scope; a reviewer not revalidating evidence does not mean it never existed. [Previous standing decision record](https://github.com/lovitus/mdd-sim-gateway/blob/8fc2c29d3bb575f054115a435a40e81036aa9ba9/docs/decisions/2026-09-20-current-scope.md).

## Historical verification links and operational boundaries

PR #6 final-head runs [35518096181](https://github.com/lovitus/mdd-sim-gateway/actions/runs/35518096181), [35518096021](https://github.com/lovitus/mdd-sim-gateway/actions/runs/35518096021), [35518095972](https://github.com/lovitus/mdd-sim-gateway/actions/runs/35518095972), then exact main **35518915013** passed. Retained PR #6 evidence counted 1,149 ordinary Core top-level passes, 205 selected boundaries, 176 Provider, 1,371 upstream, five audio-helper and all four separate-process recovery cases, with full Linux Core race in a separate step; 696 repeated boundary, 120 paid-call stress and 80 capability-stress executions. All 20 prior hardening and seven liveness counterexamples paired with patched passes. These historical counts are not newly relabelled as Android test counts.

PR #6 post-merge liveness SHA-256 `35f7641b0f079491d222c9eb34fdb38afc92940f01df098af0a79e89bdd2556e`; implementation patch SHA-256 `be0b5bb3923a72aa8657c3ba6d7301cb671409febbf4fb0909ffaa6bb56779d7`. PR #5 retains acceptance-policy correction proofs; PR #4's original scope errors remain explicitly superseded rather than silently erased.

Repository merge, draft APK publication, workstation synchronization and production deployment are distinct. No live SIM mutation, deletion-notification replay, paid SMS/call, production upgrade/restart or desktop Agent setting change occurred in the Android delivery. Emulator/synthetic/CI installer operations are not physical acceptance. Audit-only proof/publication branches remain outside main; do not claim only main exists or merge them wholesale. Umbrella issue remains open.
