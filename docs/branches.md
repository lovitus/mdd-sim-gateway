# Branch disposition and integration record

October19 PR45 `codex/linux-policy-recovery` is integrated, not a merge queue.
Approved head9bf6571 and normal merge90fde687 share tree1df9481c with deployed
artifactbab742e. Original final review and full CI39212919170 passed; only the
authorized Linux Agent was upgraded, with original donor/exit restored. Exact
field boundaries are in DEPLOYMENT.md. Proof601d32e/39212493452 is retained at
`archive/2026-10-19/linux-policy-qualified`; production/permanent tests remain
identical after removing the temporary proof entry. Earlier failures and review
baseline remain at `archive/2026-10-19/linux-policy-initial-proof`
(ed84fab/39210883341) and `archive/2026-10-19/linux-policy-proof-initial-green`
(8727add/39211207680). These are evidence only, not alternative merge candidates.

October18 PR44 `codex/formal-release-downloads` is integrated, not a merge queue.
Original final review approved `e9de823`; normal Go `39108641098` and Android
`39108640988` passed. Normal merge `9a139f81647a83224f04b8c30f06279176061bfb`
shares the approved complete tree `32bcc859197f28c650f80f0766ea53e6104ca4f3`.
Fixed `android-preview-v2.0.0` and `v2.0.0` point to that merge. Signed Android
`39109678131` and Go `39110411762` published their releases; all public assets
were downloaded and verified, and Go is Latest. Project policy forbids moving
these tags or replacing published assets; GitHub reports `immutable=false`, so
this is not a platform-lock claim. Runtime/installer behavior, production devices and deferred
tests are unchanged; publication is not fleet deployment. See DEPLOYMENT.md.

Hosted proof `42e4f7b` / `39107209751` passed six behavioral reds and the restored
Go/Android publication matrix; `archive/2026-10-18/formal-release-qualified`
retains it. Temporary proof workflow removed before the single delivery commit.
Original final review rejected `0ec4d65` despite green normal Go `39107348206`
and Android `39107347994`: the fixture omitted real draft lookup and empty
starter semantics, and preview handoff omitted SHA256SUMS. These are corrected
at `6a89191` / proof `39108506631`, archived as
`archive/2026-10-18/formal-release-review-qualified`; old source remains at
`formal-release-before-review`. The temporary proof branch/workflow is not a
merge queue. Final normal CI and re-review above apply to the cleaned delivery head.

PR40 `a42cb98` passed original final review, normal Go `39102084430` and Android
`39102084411`; owner-authorized normal merge `26e1997` shares tree `632ef673` with
artifact source `0a4cfd93`. Qualified source is preserved at
`archive/2026-10-18/linux-arm64-reader-delivery`; previous `ca3e63b` remains at
`archive/2026-10-18/linux-arm64-reader-before-main`. Integration retained main
`4d0764a` without rewriting history. This branch is integrated, not a merge queue.
No redeployment, device restart or paid/card operation occurred; the ARM64 host
retains the original installed artifact below. Exact artifact hashes are in PR40.

Original PR40 delivery from main `6b3fddc`: reviewed
candidate5552ebe and same-tree artifact83af89c3 passed CI38860462972 after one
unchanged failed-job rerun; archive/2026-10-16/linux-arm64-reader-qualified retains
that source. The one authorized new-host reader deployment and actual Chrome
acceptance are recorded in DEPLOYMENT.md. Those exact-source records remain valid,
not qualification of the new integrated build. Do not restore old branches.

PR43 delivery: `codex/android-usb-access-recovery`, based on `e1207aa`, fixes
Android's missing automatic USB permission request using the existing reader
recovery owner. Original plan review approved the bounded scope. Behavioral proof
`a29d26b` / `39096747457` passed and is retained at
`archive/2026-10-18/android-usb-access-qualified`. The original review's three
lifecycle/UI/input corrections passed `1bf4bd7` / `39097903093` (three new reds,
all eight restored flows green), retained at
`archive/2026-10-18/android-usb-access-review-qualified`.
Temporary proof files are removed; original source review approved `1bf4bd7`.
PR43 initial normal runs `39098244861`/`39098244880` failed: API35 checked Service
destruction before its asynchronous IPC completed; an unchanged Core donor-apply
test reported runtime unavailable. The fixture now waits for actual destruction
without weakening its assertion. Final `3ceada0` passed original review, normal
Android `39099064731`, Go `39099064725`, and signed Android `39099884933`.
Normal merge `3108bea` has the same tree `c25caed5`; approved source is retained
at `archive/2026-10-18/android-usb-access-delivery`. This branch is integrated,
not another merge queue. Owner-authorized v178 update restored the actual missing
USB permission flow and original card/IMS readiness; see DEPLOYMENT.md for scope.

PR41 `0ababe4` / normal full CI `39015455331` and original final review passed;
normal merge `160c52a` and deployed `aa89ce241f5f` share tree `c9cede66`.
`archive/2026-10-17/agent-setup-delivery` preserves the reviewed head; the source
branch is integrated, not another merge queue. Real enrollment and restart reuse,
cleanup and scoped limitations are recorded in [DEPLOYMENT.md](../DEPLOYMENT.md).
Earlier `1382a64` / `38890810681`, `63476b7` / `38890087413`, and `2a78d41` /
`39015231214` proof tags remain evidence only. PR40 integration is recorded above.

PR42 `ba6fc803` passed original review, Go `39013478171` and signed Android
`39013483690`, then merged normally as `dbf6baa`. Exact-phone v174 installation
and actual Chrome Detect/Save/Enable reached IMS200 with the original line,
IMEI/country and all unrelated catalog rows preserved. This is startup, not voice
or endurance acceptance. Authorized Core Web-copy deployment and donor restoration
completed; see the [receipt](https://github.com/lovitus/mdd-sim-gateway/pull/42#issuecomment-6231895384).
Proof `57b49d5` / `39012883696` remains at
`archive/2026-10-17/android-sim-qualified`; earlier proof tags are evidence only.

Current delivery: PR39 `d267b527`, original source/final cleanup reviews and normal
full CI `38731761585` passed, including real Xray namespace coverage. Normal merge
`521067f` and deployed artifact `756558dc` share tree `5b3ad521`. Core/apply/egress
and nine Providers updated; six original Agent generations and user settings kept.
`codex/egress-donor-recovery-budget` is integrated, not another merge queue.
Proof `a82a9df` / `38730592952` remains at
`archive/2026-10-15/egress-budget-qualified`; do not restore its temporary workflow.
Actual Chrome budget refresh passed; live fault injection and budget writes/exhaustion
remain unverified. The owner confirmed intentional HKmobile removal and accepted the
other HK authentication rejection; HK registration is no longer a restoration gate.
Both current GB cards were ready in pinned readback and actual Chrome at02:38UTC.
Clear authentication-rejection copy remains a frontend follow-up, not another rollout.
Failed private deployment attempts and exact
scope are retained in the [receipt](https://github.com/lovitus/mdd-sim-gateway/pull/39#issuecomment-6206543093).

Previous delivery: PR38 `59f8dd2`, original review and full signed `38705192004` attempt 2
passed; normal merge `0d74354` shares tree `5fbdb983`. Core and the exact signed
reader Mac are deployed. Actual new SIM-only verification/Enable reached IMS200;
later interruption, authorized legacy Wi-Fi cron pause and bounded automatic
recovery are separately recorded in the [receipt](https://github.com/lovitus/mdd-sim-gateway/pull/38#issuecomment-6188828740).
Continuous-ready duration did not pass; no paid-operation acceptance is claimed.
The source branch is integrated; proof branches/tags below are evidence only.

PR38 history: `codex/reader-sim-readback` from `cda6b03`, implementing the
original reviewer's approved explicit SIM-identity scope. Full eSIM readback is
unchanged; scoped dispatch requires negotiated capability. Hosted proof
`058a0fe` / `38704067945` passes all nine behavioral counterexamples, restored
Agent/Core race suites, real HTTP adapters and current-card/adjacent browser
journeys. Its exact UI is imported and temporary proof removed. The original
review found one pre-existing readback-overload panic path, now corrected and
covered by the same WSS test. Final review/full signed CI/rollout are recorded above.
Normal `38704451287` at `d9b4491` failed the existing Agent-host exact capability
assertion, which still expected three features. The real Manager now advertises
the reviewed fourth `reader-sim-identity-v1` feature; the assertion is updated to
require all four, without weakening production or test gates. Both failed jobs
reported this same test. Source is retained at `archive/2026-10-15/reader-sim-normal1`.

Proof archives (evidence only, never merge these temporary workflows):
`archive/2026-10-15/reader-sim-proof1` (`58aef3d`, `38702040756`, WSS fixture
missing URL path); `reader-sim-proof2` (`16b834b`, `38702300514`, Detect fixture
missing present flag); `reader-sim-qualified1` (`e82f112`, `38702702304`, passed);
`reader-sim-overload-proof1` (`fdbd683`, `38703751348`, all restored suites passed,
but the proof expected panic text instead of the observed lost response/offline
failure); `reader-sim-qualified` (`058a0fe`, `38704067945`, passed). The last change
corrected only that proof marker, not the permanent test or product expectation.

PR37 final `6c0177c` / full normal `38696701616` and original review passed;
normal merge `fd1847a` shares complete tree `562205eb` with deployed `a1755279`.
Core-only cutover restored six original Agent generations and unchanged unrelated
processes/configuration. Actual current-card browser startup reached an existing
readback failure, not acceptance; exact boundary and retained first admission
failure are in the [receipt](https://github.com/lovitus/mdd-sim-gateway/pull/37#issuecomment-6186935747).
The activation/proof branches are integrated or archived evidence, not merge queues.

PR37 follow-up source `eeb8472` is retained at
`archive/2026-10-15/current-card-cancelled-apply-proof`; hosted `38696184712`
proves cancelled admitted-apply bookkeeping red/green and both mounted current-card
and adjacent editor journeys green. Exact hosted assets are imported and the
temporary proof workflow removed. Normal `38694169307` failed on an obsolete Save
button locator; its fixture now uses the actual label/read contracts without
weakening assertions. `89967b8` is retained at `current-card-normal1` under the
same dated archive prefix. The proof branch is evidence only, not a merge queue.

Current-card activation source `35c3cb2` is retained at
`archive/2026-10-15/current-card-source-proof`; hosted `38693765905` proves the
three original-review counterexamples red and the corrected mounted journey
green. The exact hosted UI is imported; the temporary proof workflow is removed.
Earlier `e7e9ba8` / `38690177573` and cancelled `683fba1` / `38691912859` remain
at `archive/2026-10-15/current-card-proof1` and `current-card-proof2` respectively.
These tags preserve evidence, not merge queues. Final normal qualification,
source approval and production startup acceptance are separate pending gates.

PR36 source `96e7dd9` is retained at
`archive/2026-10-14/reader-setup-source-approved`; original source review passed.
Hosted `38542327466` proves the pending-claim behavioral red and complete green
setup/existing browser journeys; only stale-embed gates failed. Same-source merge
`834dad6` supplies the exact UI now imported. Earlier counterexamples remain at
`185c736` / `38538977486` and `9b0a3df` / `38540681863` with their archive tags.
Final `df63ab6` / normal CI `38543615650` and original cleanup review passed;
PR36 merged normally at `8fe8dbe`, same complete tree as deployed `022c7e9`.
Only Core restarted, six original Agent generations restored; configurations,
unrelated processes and pending application state preserved. Production browser
clicks/new-card startup remain unaccepted, not superseded by API readback.
[Deployment receipt](https://github.com/lovitus/mdd-sim-gateway/pull/36#issuecomment-6165782776).

## October 14 integration and rollout

Final PR35 source `431fa6b` / full Go CI `38516014673` and original review passed;
normal merge `77f23e0` shares tree `b7f33640` with artifact source `261d8e7`.
Controlled Core/updater rollout and read-only field reconnection passed; original
maintenance released, watcher enabled/waiting with no pending request or worker.
Production browser clicks and business mutations remain unverified. The exact
[rollout receipt](https://github.com/lovitus/mdd-sim-gateway/pull/35#issuecomment-6161777301)
and DEPLOYMENT.md supersede the pending-rollout statements in the history below.

PR34 merged normally at `202e5206c47210e3ebd51a9ac5de0d9a3cd74152` after final
source `639f55a` / full Go CI `38509927202` and original-reviewer approval.
PR29-33 are closed as integrated, not abandoned; do not merge their old heads again.
Field cutover rolled back to the retained previous Core: update maintenance IPC
uses `provideradmin.Authenticate`, but the worker sent a different credential header.
The follow-up changes only that client header and makes its recovery tests use the
real production middleware. A GET/POST credential table checks unchanged durable
state and admission. Qualified `c59056d` / full Go CI `38514560874` is retained at
`archive/2026-10-14/update-auth-qualified`: old valid GET/POST fail with 401, then
the credential table and both original recovery tests pass under race, with no skips.
Test-merge `679ffcd` has the same tree `5fada26`. Initial proof `38514002965`
failed before tests because shallow checkout lacked a baseline object; the archived
`update-auth-first-proof` is not a behavioral counterexample. The qualified proof
withdraws only the header fix. Final cleanup removes temporary proof steps/script,
with runtime and permanent tests unchanged; final normal CI and rollout remain.
Original state/configuration and maintenance were preserved; no paid/SIM action.
First-upgrade installation uses canonical managed links and the existing installer;
the watcher stays inactive until the corrected worker is qualified and deployed.

The owner authorized one combined PR29-33 delivery, normal protected merge and
controlled GitHub-artifact rollout with rollback retained and scoped field tests.
Base: `9a9aa783a5681ec203cd977b06c9582979dc7331`. Reviewed source/normal Go CI:

| PR | Exact source | CI |
|---|---|---|
| 29 | `c200dc5e1c05239f8d1f3636f99342c768fa8779` | 38345314313 |
| 30 | `254c140cff85467dfd9b0ca530f97425fff6a76f` | 38383637312 |
| 31 | `b579963d5bc17786f9c2859295fa7da01d5b9f28` | 38501900200 |
| 32 | `1152132f0ec39198d8ca02e9a744a26685bb8c12` | 38403121778 |
| 33 | `e531446ffbf54bc7da92c23aec373d423cab19e0` | 38427350573 |

Shared conflicts preserve all three Agent dispatch guards, card-fenced intent with
update admission, original cleanup ownership, current-view navigation with original
paid-operation identity, and separate new-card/archive actions. Permanent workflow
gates are combined without restoring temporary proofs; workstreams retain their IDs.
The embedded UI is generated from the complete integrated frontend on GitHub,
not selected from one PR. PR34 final normal CI/cleanup review and merge are complete;
the corrected-worker rollout receipt remains pending. Do not restore the old PRs.
PR34 source candidate `b5d3102` is retained by
`archive/2026-10-14/integrated-source-before-embed`. Hosted run `38505387105`
passed the integrated browser flows and produced the combined UI; its two failed
jobs stopped at the expected old embedded-resource diff, not a waived gate.
Generated resources are imported from its exact-tree merge `58f08fad` before final
normal qualification. Source tests and workflow gates remain unchanged.
Cross-review found one eSIM/update cleanup interlock. The correction adds read-only
retained eSIM/host checks after admission closes and before update Provider drains;
the two-case regression preserves original refresh and subsequent update admission.
Qualified `728b788` / full Go CI `38508488826` is retained at
`archive/2026-10-14/integrated-qualified`: both compiled behavioral counterexamples
fail, then both original-recovery/next-update cases pass under race detection.
Test-merge `c66b4cf` has the same tree `a4e82c2`; hosted/embed app SHA-256 is
`4c0f979d8d4ef71fac12fd2446b53de2a454517e4d17a07eb09ba04e07651b90`.
Original source cross-review passed. First proof `eec8a45` / `38507888477` retained
at `integrated-interlock-first-proof` used schema 1 in a Provider fixture; correction
uses the actual protocol constant without changing runtime or assertions.
Only temporary proof script/steps are removed for final normal CI; permanent tests
and product trees remain identical to the qualified source. No field claim yet.

## Current cursor: September 29 reconciliation

Owner request: coordinate with the existing reviewer, merge useful completed work,
selectively port useful remaining changes, retire superseded branches, and continue
from clean main. This is repository governance, not deployment authorization,
new feature scope or a request to repeat accepted paid/hardware tests.

Inventory baseline: `fa349d18665fbe9a75b0a7b4d8c10868d3c714d1`.
GitHub reported 31 remote heads and open PRs #7, #8, #9, #10 and #14.
The table is the frozen pre-cleanup inventory, not a claim those refs still exist.
PR #17 is the authoritative merge and post-merge cleanup receipt; this versioned
record binds each decision to its original source. It is not a live branch listing.
The integration candidate is based on this main, not any old Android draft.
It reuses PR #14's four runtime files byte-for-byte from 38f1191 and ports only
the recording-progress timing hunk from PR #10. Existing runtime/tests are not
rewritten. The old recording navigation helper is not part of that timing hunk;
its separate historical proof remains in the archive, not a claimed fresh failure.
Initial same-version hosted validation 36582298475 passed at e51d97d, but the
reviewer identified a mixed-version local-control defect: older strict clients
reject the unsolicited core_connection field. The correction keeps old response
formats unless the new client requests X-MDD-Agent-Core-Connection: 1, including
status, start/stop and transition errors. Strict decoding and authentication stay
unchanged. Failure modes: breaking old local Core/CLI clients, falsely claiming an
old service is connected, leaking metadata through unauthenticated requests, or
filtering only GET while leaving transition responses incompatible.
One bounded contract regression covers this boundary. The reviewer approved the
implementation and eight-case regression design at 2e45bab. Complete signed
[workflow 36584798641](https://github.com/lovitus/mdd-sim-gateway/actions/runs/36584798641)
passed for exact source 2e45bab3c9a644335cb265b4635ba3f2297e7a5f, tree
40f720b54b4ee7d7677d2abeb9e15bdb0574af55. Artifact 11040419933 has SHA-256
7307be49884e516bc4f3371a6fa76714ab898acb349023a0ffb51ab786842d12;
its downloaded ZIP digest and original JSONL/stderr were independently read back.
The e51 API/client reversion compiled and produced exactly four legacy unknown-field
failures (stopped, running, stop, cancelled-request timeout), with no other failed
subcase. Restored code passed all eight subcases under race detection; zero fail/skip
or race/panic/build-error reports in that scoped evidence. This is HTTP/strict-schema
validation, not old-binary installation or a hardware hang test. The two new-client
subcases also verify that the extension header without Authorization still gets 401.

The full workflow preserves existing Core/Provider/upstream race, real-browser audio,
graph/context, installation and platform build/signing gates. Its non-verbose full
suites are not evidence that every opt-in hardware case ran. Prior macOS/Windows
isolated warning acceptance remains scoped to its original source/artifact.
No production deployment, phone update, paid test or hardware mutation occurred.

The final record/cleanup head removes only the temporary counterexample script and
workflow steps, restoring the long-term workflow exactly. Runtime and permanent tests
stay byte-identical to 2e45bab; final documentation is not a new CI or artifact source.
The original reviewer approved the archive-backed disposition and navigation deferral.
Normal matched-head merge remains subject to actual branch protection, never admin bypass.

## Execution receipt boundary

Old PR #7/#8/#9/#10 are closed unmerged with per-PR replacement/exclusion comments.
PR #14 is to be closed only after #17 reaches main, as integrated through #17 with
the compatibility correction, not merged a second time. All archive tags have been
peeled and verified against the frozen commit SHAs. The original 30 branch refs
are removed only with exact-old-SHA leases after integration; unexpected new work
stops deletion. The temporary #17 branch is removed after its normal merge.
The post-merge comment on [PR #17](https://github.com/lovitus/mdd-sim-gateway/pull/17)
records the actual merge SHA, final remote inventory and local clean-main readback.
Archive-backed history remains recoverable; main is the sole continuing product line.

Before deletion, all 30 non-main heads were retained as annotated tags:
`archive/2026-09-29/<original-branch-name>`. These are evidence/rollback only,
not releases or approved merge candidates. Each tag resolves to the exact SHA below.
Private unsubmitted worktree evidence was backed up separately, not in public tags.

## Rules for subsequent work

October13 new-card evidence only: `archive/2026-10-13/new-card-qualified`
retains6e63731/CI38342258088 (compiled behavioral counterexamples followed by
fixed race/API/Chromium passes). Initial tags `new-card-initial-proof` and
`new-card-first-proof` retain1998fc7/38340413097 (new fixture root mismatch) and
ef465e8/38340756689 (green before offline-stop/late-save review corrections).
The first cleanup imported the hosted app.js and removed its temporary proof.
`new-card-review-baseline` retainsae29abb before the final review corrections;
CI38342967333 reached the bounded wait timeout, not acceptance. Later corrections
require their own proof. These tags are not releases or branches to restore.
`new-card-review-proof` retains1811eea/CI38344633611: actual WebSocket disconnect,
late recovery response and transition-lock counterexamples fail on ae29abb, then
fixed race and Chromium flows pass. Both failed jobs reject only stale embedded
UI. Cleanup imports that generated app.js and removes the temporary job/script;
runtime/tests are unchanged. Final review/full CI/rollout remain separate.

October13 communication evidence only: `archive/2026-10-13/communication-initial`
retains e11cebb/CI38374418897. Proof passed; full CI and final source review did not.
It is superseded by PR30's corrected candidate, not a release or merge candidate.
`communication-fixture-before-expand` retains69712c0/CI38378206324: the browser
read a collapsed receipt section, so its SMS_VIEW failure is invalid evidence.
`communication-fixture-before-new-draft` retains49a2a3c/CI38378980213: SMS/call
regressions pass; the allowance fixture failed to leave the retained SMS draft.
`communication-qualified` retains36f530f/CI38379711514: compiled old Core and
browser regressions fail as intended; fixed allowance race and full Chromium pass.
Cleanup imports its hosted assets and removes temporary proof, not runtime/tests.
`communication-before-history-guard` retains609afc6/CI38380931145 (full CI pass,
one remaining scoped-history refresh race found by original reviewer).
`communication-history-qualified` retainsfcb1f71/CI38382538282: old history
scope race reproduced, fixed full Chromium passes; only stale embed rejected.

PR31 evidence only: `archive/2026-10-14/esim-operation-qualified` retains
ac2c230/Go CI38500419306 success (12 behavioral counterexample invocations,
fixed affected-package race/adapter/Chromium passes). Final runtime/permanent tests
match this source; temporary proof removed. Final-head review/CI and any later
merge/deployment are separate PR receipts. Earlier failed fixtures remain history,
not approval to restore them.

October13 archived-line evidence, not a release or merge queue:
`archive/2026-10-13/line-archive-qualified` retains20c516f/CI38401181375, full success
with compiled old/withdrawn deletion/backup failures, old-page assertions and fixed
race/browser passes. Runtime/permanent tests remain unchanged after proof cleanup.
`line-archive-before-receipt-recovery` retains32a8077/38397898267 and
`line-archive-reviewed` retainsad37f44/38400205694; neither passed full qualification.
Final-head normal CI, merge and deployment remain separate receipts.

October13 `codex/update-maintenance-continuity` is an independent candidate from
main9a9aa78 for N10/N11/N12. It does not reopen PR29/30/32 or the frozen PR31 fixture
decision. Qualified6b718e7 is retained at
`archive/2026-10-14/update-maintenance-qualified` (Go CI38425248981 and original
source review passed). Temporary proof cleanup retains runtime/permanent tests;
final normal CI follows on PR33. This is not merge or deployment approval.

October12 display evidence only: `archive/2026-10-12/hardware-imei-readback-proof`
retains6afc927/CI38197352338 (old rendered-page readback assertion, fixed Chromium
flow passes; full CI rejected stale embed). `hardware-imei-readback-qualified`
retainsaef240a/CI38199470829 (invalid-response assertion red/full browser green,
legacy14-digit compatibility; stale embed failure only). Final source/tests match
aef240a; generated UI synchronized and temporary proof removed. The intermediate
c7ada5b/CI38197980812 passed but needed read validation, retained as
`hardware-imei-before-read-validation`;2b5e4dc/38198905250 is retained as
`hardware-imei-invalid-read-proof`. These are not releases or merge candidates.

October11 Devices/Hardware follow-up evidence only: `archive/2026-10-11/reader-imei-proof`
retains68fc85d/CI38112817108 (old reader identity rejection, fixed adapter passes;
full run failed stale embed). `reader-imei-initial-proof` retains7c54b26/CI38112281345.
Final source/tests match68fc85d; generated UI included and temporary workflow removed.

October11 IMEI card-binding evidence, not releases or merge candidates:
`archive/2026-10-11/imei-initial-proof` retains72afaff/CI38074031050
(missing binding entry red/fixed green; full run failed stale embedded assets).
`archive/2026-10-11/imei-provision-fixture` retains41627ef/CI38075143335
(invalid operation-ID fixture; not a behavioral counterexample).
`archive/2026-10-11/imei-qualified` retains8f02c8b/full CI38075676057 success,
including two compiled conflicting-command dispatch failures, a passing control
and all three corrected race passes. Final runtime/permanent tests are identical;
temporary proof is removed. Final-head review/CI and deployment remain separate.

October9 presentation evidence, not releases or a merge queue:
`archive/2026-10-09/esim-location-proof` retains ca59961/CI37916088950
(rendered baseline red/fixed green; full run failed stale embedded assets).
`archive/2026-10-09/line-visibility-qualified` retains fb7dd44,
full Go37917549682 and Android37917541611 success with API28/35 red/green.
Final runtime/tests match the qualified source; temporary proof is removed.
Review, final-head CI, actual UI inspection and rollout remain separate receipts.

PR24 evidence only, not releases: `archive/2026-10-08/hold-continuity-initial`
retains cde59de (CI37765592425, incorrect test close-code expectation);
`archive/2026-10-08/hold-continuity-qualified` retains80e8c42 (CI37767495996,
two compiled red/four race green). Final runtime/tests match the latter;
temporary proof removed. Final CI/review/normal merge are in the PR24 receipt.

PR23 immutable evidence tags (not releases or future merge candidates):
`archive/2026-10-08/inbound-hold-initial-qualified` = `7f605d0b1de567adaedc1c1202045d42de3da19a`, CI37753859505;
`archive/2026-10-08/inbound-refresh-qualified` = `e56e470e954cbfb5fb9fe01ed025236005dfd2fe`, CI37755633663;
`archive/2026-10-08/inbound-hold-qualified` = `a2cce0ac7fc73c9f091344e606ab8067f445a625`, CI37757483079.
Final runtime/permanent tests match the last tag; temporary proof entries are
removed. Final-head CI/reviews, normal merge and deployment remain separate.

October 8 incoming-media/modem-facts candidate: immutable evidence tag
`archive/2026-10-08/inbound-policy-qualified` ->
`cc7b5b5af2bb02ff1928aca3d55a69d1750722b8` preserves the one-time hosted
counterexamples and exact green runtime/tests. Final delivery removes only the
temporary workflow/script and records qualification; do not restore those tools
or treat this evidence tag as a release. Full CI/review and merge are separate.
`archive/2026-10-08/inbound-policy-reviewed` retains14524a09e7e84728b2e30bdcfae32270c9bcb16a:
full CI37724548919 passed, but independent AMR review rejected that head.
It is the compiled behavioral baseline for the review correction, not a merge
candidate. Preserve the Linux/readiness evidence without treating its AMR
contract as approved.
`archive/2026-10-08/inbound-amr-qualified` retains
`fbe7882f970a0a73644d1f8b79a36a67600dd992` (tree36698db177db13f19a1a896d9e02481d845bbc1b):
full workflow37734542838 succeeded, with the formal-answer/real-frame behavioral
proof and independent implementation review. The final PR22 cleanup preserves
runtime/permanent tests exactly, removes only temporary qualification steps/script,
and restores the normal workflow. The archive is evidence, not a release or a
branch to restore. Final-head CI, normal merge gates and deployment remain separate.
`archive/2026-10-08/inbound-policy-transition-reviewed` preserves
`8e4b9e531fbe97f9ec11a8800196ab6471622296`, whose full CI37736073811 passed.
The fixed reviewer subsequently identified a stored-off intermediate-observation
readiness gap. This is the compiled baseline for that bounded correction, not
approval to merge the superseded readiness condition.
`archive/2026-10-08/inbound-policy-transition-qualified` preserves
`98a0831d0f4b9800d1ab061bb2fdc95aaa6a9b73` (tree797b4a15e6cf6822f2d1dba3d439c192716c0de7).
Full37738205318 succeeded after one failed-job retry for Chrome startup. Linux
and Windows each prove five compiled transition subcase failures on8e4b9e5 and
58 restored policy race passes. Final cleanup removes temporary proof entries,
not runtime/permanent tests; normal CI is restored exactly. This tag is evidence,
not a release or a separately mergeable branch.

October 3 Linux acquisition batch: immutable evidence tags
`archive/2026-10-03/linux-acquisition-initial-counterexamples` ->
`446b910dc6910baadb266349bb183b6ed04e4aea` and
`archive/2026-10-03/linux-acquisition-qualified` ->
`91947fc993d708f72649ecaceae5c2c61cb07207` preserve the hosted behavioral
counterexamples and temporary verification entry. Final delivery excludes that
entry. Neither tag is a release, production qualification, or future merge queue;
do not restore the initial rejected candidate or reintroduce temporary CI tools.
The batch PR records its final exact-head review and merge disposition.

September 30 follow-up: [PR #18](https://github.com/lovitus/mdd-sim-gateway/pull/18)
normally merged the reviewed call-capability recovery at
`e30a45fb666fa2f2ec5eba7596df4fecb331513a`, from exact head
`b71b81c507d21f0b9be853c5998dde58e7783c6d`. Its tree equals that merge.
The qualified signed source and temporary behavioral proof remain at immutable
`archive/2026-09-30/agent-call-capability-qualified` ->
`bca3dced5c8c2d8dc6cb90fc6c5d778cc3b4378b`. The delivery branch is retired
after its matched-head merge; current main includes the fix, so do not restore or
remerge the archived qualification entry. Deployment evidence is in DEPLOYMENT.md.

- Start new product work from current `origin/main`, not an archived branch or old worktree.
- Non-ancestry does not prove a missing feature. Reconcile squashes, selective ports
  and current architecture before proposing a merge.
- Do not restore excluded device pairing/durable Core call recovery, abandoned eSIM
  soft deletion, temporary patch-injection/publishing workflows or encoded audit payloads.
- Any later useful archival fragment needs a current requirement, bounded diff,
  provenance, applicable validation and review on a new branch from main.
- Preserve scoped acceptance and postponed work. Closure does not mean every experiment
  shipped, every hardware scenario passed, or any production binary changed.
- Never move archive tags. Check exact heads before deletion; unexpected new work stops cleanup.
  Keep credentials, private topology, raw logs and unsubmitted backups out of Git.

## Frozen remote inventory

| Original branch | Exact pre-cleanup head | Disposition and provenance |
|---|---|---|
| `audit/thirdparty-ci-transfer` | `258cd861d4f8c5c3c7e84ebba0cab333510d54b8` | Evidence-only audit/preparation/transport branch; tagged payloads are not product merge candidates. |
| `audit/thirdparty-review-3e6d5db` | `92aad5a9ca2426a75775754ecc3a775301b07df4` | Evidence-only audit/preparation/transport branch; tagged payloads are not product merge candidates. |
| `codex/agent-connection-warning` | `38f11912c7774368bc7d7984998fd771e576f244` | Selected into PR #17 plus the local-control compatibility repair; close #14 after integration, never merge twice. Not deployed. |
| `codex/android-client-focused` | `98abe76688c0b50c34db14208e24f32d1e7ba401` | Merged via [PR #11](https://github.com/lovitus/mdd-sim-gateway/pull/11), merge `8d0c4c6e3fa32073b7abcdf3f9f85ccef4473c89`; retire branch. |
| `codex/android-dial-prefix` | `dbc20951a83875a227d4307bcbf553ea4a95fd16` | Merged via [PR #16](https://github.com/lovitus/mdd-sim-gateway/pull/16), merge `fa349d18665fbe9a75b0a7b4d8c10868d3c714d1`; retire branch. |
| `codex/android-production-adaptation` | `7df49d4dc2c61da5890bcc53cce49f8606e4d6c2` | Merged via [PR #12](https://github.com/lovitus/mdd-sim-gateway/pull/12), merge `0b663b9a1f04149a6c5eb771e2b1f7f699ddd867`; retire branch. |
| `codex/android-recovery-remediation` | `7d1408462a21750ef0ab5a6144589ae5e0259efa` | Superseded by PR #11/#12/#15/#16; excluded Core architecture must not return. |
| `codex/android-status-messages-ux` | `7e9e9f5fb032228b8723f85408207d2d479a6802` | Merged via [PR #15](https://github.com/lovitus/mdd-sim-gateway/pull/15), merge `67aedcbbaa2eb2690c45c7e33685af95653eeaa9`; retire branch. |
| `codex/android-v2` | `c639dd2b77007a023e2ce44214f1a79503bb5dc3` | Superseded by PR #11/#12/#15/#16; excluded Core architecture must not return. |
| `codex/android-v2-amend` | `708aa83ad6ebeb5d2e2252a8c81f5d44000fd18c` | Superseded by PR #11/#12/#15/#16; excluded Core architecture must not return. |
| `codex/audit-acceptance-scope-19f966f` | `5e17cbd1a073ec4eb7915cf7593d697671b676a6` | Evidence-only audit/preparation/transport branch; tagged payloads are not product merge candidates. |
| `codex/audit-android-8fc2` | `486e83488ffb98fd36793a2347862b69f8b90075` | Evidence-only audit/preparation/transport branch; tagged payloads are not product merge candidates. |
| `codex/audit-open-work-08de1fb` | `dde7ea964e99014221f213038815e88838ee26b1` | Evidence-only audit/preparation/transport branch; tagged payloads are not product merge candidates. |
| `codex/audit-pr8-fixes` | `751f8eadcce544be52ddf59c60340436679f052f` | Historical PR #8/#9/#10 repair/counterexample family; selective map below, never wholesale merge. |
| `codex/audit-pr8-repairs` | `b83f852a795bec853820a9c7ff435407521d85d7` | Historical PR #8/#9/#10 repair/counterexample family; selective map below, never wholesale merge. |
| `codex/pr8-fix-amend` | `274983dcce6aec72b6836b12e6641c9000b1bd2c` | Historical PR #8/#9/#10 repair/counterexample family; selective map below, never wholesale merge. |
| `codex/pr8-repair-prepared` | `1c0d1ddfbe6180fa45655f60154f459a2964c083` | Historical PR #8/#9/#10 repair/counterexample family; selective map below, never wholesale merge. |
| `codex/pr8-repair-source` | `5eb71fc426f09b48dc471997ede64d1fbdc647d9` | Historical PR #8/#9/#10 repair/counterexample family; selective map below, never wholesale merge. |
| `codex/pr8-review-fixes` | `274983dcce6aec72b6836b12e6641c9000b1bd2c` | Historical PR #8/#9/#10 repair/counterexample family; selective map below, never wholesale merge. |
| `codex/review-acceptance-scope` | `a19b7e657152a7ba580acd737b699cfba76d57fe` | Merged via [PR #5](https://github.com/lovitus/mdd-sim-gateway/pull/5), merge `08de1fb3e1d157f7f82ee5568bd98f5e57735f30`; retire branch. |
| `codex/review-main-7f2e129` | `39f7a7ffc8f301859c68ae171d5dd27a02e27e33` | Evidence-only audit/preparation/transport branch; tagged payloads are not product merge candidates. |
| `codex/review-open-work` | `3ced930d6d8b0c5275b41f254d490baab13c527a` | Merged via [PR #6](https://github.com/lovitus/mdd-sim-gateway/pull/6), merge `8fc2c29d3bb575f054115a435a40e81036aa9ba9`; retire branch. |
| `codex/review-open-work-candidate` | `3ced930d6d8b0c5275b41f254d490baab13c527a` | Ancestor of main through PR #6; retire duplicate delivery reference. |
| `codex/review-open-work-ledger` | `ba8befe649684f95661459fa8889933de9a4bf73` | Ancestor of main through PR #6; retire duplicate delivery reference. |
| `codex/review-postmerge-7f2e129` | `06d837f4b98f7fbaf0e5ebc318966c40f4099e31` | Evidence-only audit/preparation/transport branch; tagged payloads are not product merge candidates. |
| `codex/review-pr8-fixes` | `4e600f171cf5f30d871952f95c8ae01ad99741a1` | Historical PR #8/#9/#10 repair/counterexample family; selective map below, never wholesale merge. |
| `codex/review-pr8-resumed-candidate` | `4e600f171cf5f30d871952f95c8ae01ad99741a1` | Historical PR #8/#9/#10 repair/counterexample family; selective map below, never wholesale merge. |
| `codex/review-prack-fix-7f2e129` | `29dfde817509f2f09d81e17fd7908eb9df869f99` | Merged via [PR #2](https://github.com/lovitus/mdd-sim-gateway/pull/2), merge `3e6d5db7657e468eedc0c31316bb97c04b2b599b`; retire branch. |
| `codex/review-thirdparty-cleanup` | `afc628933da32235a2a6762d9edf63cc63a19aa6` | Merged via [PR #4](https://github.com/lovitus/mdd-sim-gateway/pull/4), merge `19f966f7d673a074e7218680d6c0ed8b1d530e1c`; retire branch. |
| `codex/vowifi-rtp-dtmf` | `aa5e44b8832729580171f6bef7035a47971a8117` | Merged via [PR #13](https://github.com/lovitus/mdd-sim-gateway/pull/13), merge `cc07e6d906ec0d2cc00d6c7f917a2ca7f18482b7`; retire branch. |
| `main` | `fa349d18665fbe9a75b0a7b4d8c10868d3c714d1` | Integration baseline; retain. |

## Old Android repair map

Closing the stacked old PRs is not a claim that all their code was merged.

| Historical item | Current disposition |
|---|---|
| Native Android baseline/UI from PR #7/#8 | Superseded by PR #11 and later PR #12/#15/#16. PR #11 head and its squash merge have identical trees despite non-ancestor commit identities. |
| F1 accepted-dialog cleanup and F3 positive End/owner retention | Selective port `bc7dd36`, merged via Provider PR #13. Preserve exact call identity and no automatic paid retry; do not port again. |
| F2 durable rejection/terminal receipts and F3 persistence architecture | Excluded by client-first scope, not a missing approved merge. Do not restore the old receipt protocol or pairing implementation. |
| F4 stale reader publication | Superseded by current AgentService hub/link/epoch scan ownership and main-loop publication. Do not replace the Service with its old variant. |
| F5 former automatic HTTP SMS catch-up recovery | That latch is absent from the current architecture. Preserve current retry/certificate fences, not the obsolete loop. |
| F6 unreadable encrypted-settings recovery | Reused and completed by PR #12 N3: double confirmation, verified private archive, key retention, old-writer fences and pending-operation protection. The September 26 'unabsorbed' note is superseded. |
| PR #10 browser-recording fixture timing | Selected from 4e600f1 for this integration: wait for encoded bytes rather than a 300 ms sleep, retain decoded stereo/mute and bounded failure checks, use a 2 s limit with a duration assertion rather than racing a 1 s encoder cadence with 150 ms. No runtime timing changes; existing scenarios, not new tests or a newly claimed pre-fix run. |
| PR #10 recording navigationBarrier | Evidence-only/deferred, explicitly not ported. Source 4e600f1, webui/tests/recordingBrowser.mjs, blob 756beb4910a231e5b582ad01902fb55982d1b22b. It binds frameId/loaderId and preserves event-order controls. The reviewer accepts deferral absent a current navigation failure; the existing test still fails on timeout/evaluation errors. Do not call it absorbed or prove the race absent. |
| PR #8 audit/prepared/duplicate candidates | Tagged source/proof only; do not restore their old workflow or architecture. |

Current source and qualification receipts remain in
[Android scope](decisions/2026-09-24-android-client-scope.md),
[acceptance ledger](status/README.md), and the PRs.
Historical next-step text is not a current branch queue.

## Preservation and local workspaces

A verified private Git bundle retains every pre-cleanup ref and each worktree HEAD.
Bundle SHA-256: `1e7f32207b25c235f2f6de6d4b0c2202b9d61bbe1829974ce7da1a143bcce5b2`.
Initial private inventory SHA-256:
`a9c155a78a0d0fa9cb94144ba8d0553b049b62ab45748ebf66a0e722e498d7b3`.

Seven worktrees were inspected; four had 69, 3, 9 and 3 changed/untracked files.
Exact file copies, staged/unstaged binary patches, modes and SHA-256 manifests
were preserved privately, with every copied file verified. No unsubmitted file
was silently committed or declared accepted.

The recovery workspace contains excluded pairing/receipt work and old client
variants. The reader/reauth workspaces are historical variants, not active queues;
current bounded USB reads, client reauthentication and publication must not be
replaced just to absorb their diffs. The Provider workspace's three remaining
changes are historical ledger records; PR #13 reconciled them by workstream identity.
Ignored runtime material remains in place. No blanket clean/reset or filesystem removal.

## Historical September 20 classification

At `3e6d5db`, PR #2 had merged the PRACK product fix.
The review-main branch retained HTTP-500 counterexamples; review-postmerge retained
negative controls and dependency evidence. The latter advanced from `910de2f`
to the full SHA above. These proof workflows are not missing product code.
