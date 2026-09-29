# Android client delivery boundary

Authority: the owner's September 24 instruction to continue the client-focused
proposal, without subagents or unrelated work. This supersedes conflicting Android
recovery plans, not the desktop product requirements or historical evidence.

- Base this delivery on PR #7 (`c639dd2`). Preserve the existing Android usability
  improvements where compatible; do not replace them with the old setup-only UI.
- Keep Core, Provider and WebUI at this baseline. Reuse existing reader enrollment,
  media, call and SMS APIs. A server change needs an actual incompatible request
  and a separately justified minimal adaptation.
- Fix indefinite network recovery in Android: bounded backoff without an attempt
  limit, reauthentication with explicitly remembered Keystore-encrypted login,
  and recovery from malformed messages. Do not bypass certificate validation,
  revoked reader credentials, rejected passwords or the user's pause/share intent.
- Use browser-equivalent call lifetime. App process loss does not acquire a new
  server-side recovery capability. Never automatically redial or resend SMS.
  Verify existing server cleanup behavior; do not assume every channel has a
  ten-second timeout merely because the review says so.
- Exclude PR #8 server durable receipts and the uncommitted device-pairing system.
  Preserve those branches and dirty files; exclusion is not permission to erase.
- Retain Calls, Messages, Readers, Home status and Settings usability. No landscape
  project, new authentication product or additional server recovery framework.

Failure checklist: transport loss, silent socket, session expiry, rejected saved
password, changed certificate, malformed message, stale reconnect after Pause,
and uncertain paid submission. Validation uses GitHub workflow and the separately
authorized handset. This document is scope, not evidence that those checks passed.

## Current cursor

### September 29 Android communication UX (qualified; PR close-out)

New owner request after the reviewed rollout: improve Home status/actions/colors,
compact Calls device/SIM selection with separate VoWiFi and cellular indicators,
complete Messages navigation and default reply selection. Android is also a
standalone communication client: several phones may sign in without local readers.
Reader sharing remains optional and independent of server-line communication.
This is a new client-focused batch, not a reversal of PR #12/#13 acceptance.

Baseline: `cc07e6d`, installed v91. Read-only production history contains 127
events across 18 conversations (120 received, six submitted, one delivery).
The mobile snapshot contains only the newest 50; 77 retained events are outside
that window. All 127 are reachable with existing conversation/history APIs, and
none carries historical `card_id`. Raw evidence remains private; receipt SHA-256
`f64fd6e4111b629bc718293b33f7b96738daec97e9d867a92d19b961159b2b0b`.
This proves retained history beyond the preview, not that every carrier message
ever sent reached Core. No SMS/call/replay or configuration change was performed.

Plan and failure checklist:
- Home prioritizes connection and server communications, uses a persistent-intent
  switch, actual operation-readiness counts, compact optional reader status and
  recent events. Enabled configuration is not green business health. No reader or
  unsupported OMAPI is not an error for a communication-only device.
- Calls and Messages reuse one compact line-row component. VoWiFi and cellular
  have separate colored dots plus text based on each page's operation readiness.
  Missing/stale/offline facts must not stay green; selection must retain exact
  line/card identity and never silently switch a user-selected transport.
- Messages opens on all retained conversations, not a send form hiding a limited
  event preview. Existing scoped, cursor-based history loads older pages on demand.
  Show peer, body preview, SIM/line context, transport and localized time; preserve
  unknown submission recovery and explicit send confirmation. Old account callbacks
  must not populate the new account, and live snapshots must not discard old pages.
- Reply preselects a unique exact original line/card match, from an explicit
  historical card or Core's existing cellular event hash of card ID and message
  fingerprint. Live readback verifies this existing contract for 110 cellular
  received records; it does not apply to VoWiFi or legacy fingerprints. Unknown
  historical identity still requires an active selection of a current SIM,
  accurately labeled, and revalidation after all dialogs. Changed/removed or
  ambiguous identity cannot fall back to the first list item.
  Cancellation preserves the draft; reply prepares a draft and never sends.
- Multi-client calls retain the existing Core/Provider/browser lease arbiter.
  Exact start/answer rejection codes preserve a separate not-admitted fact even
  when own-session cleanup fails. A shared incoming call ID is not ownership:
  no `/end` before an exact accepted/active call-and-operation receipt. Unknown
  answers stay unknown; late acceptance after cancellation cannot reopen audio.
  Only this client's media lease is cleaned. No new server call owner or automatic
  paid retry. No-reader communication, rejected/unknown admission and a normal
  accepted incoming-call control are scoped non-paid hosted checks.

Reuse the existing Material 1.14.0 Views dependency (current stable release),
native Material switches, buttons and list rows; no Compose/framework migration.
References: [Material release](https://github.com/material-components/material-components-android/releases/tag/1.14.0),
[switch component](https://github.com/material-components/material-components-android/blob/master/docs/components/Switch.md),
[Android accessibility](https://developer.android.com/guide/topics/ui/accessibility/views/apps-views).
Keep 48dp touch targets and status text alongside color. Portrait is the target;
landscape/endurance remain accepted deferrals. Reuse the original ChatGPT reviewer.

First candidate `75d69d9` passed build/lint/unit/Core/WebUI jobs in hosted
[36533452043](https://github.com/lovitus/mdd-sim-gateway/actions/runs/36533452043).
On both API 28 and 35 the unchanged baseline main sources executed four precise
behavioral failures, while the normal accepted-call control passed. The candidate
passed all five new native checks; the full suite had 22 passes and one failure
on each API. That failure is the existing unauthenticated-tabs test expecting a
write form immediately on Messages, now deliberately the conversation inbox.
Its navigation is updated to enter Write; password retention, tab identity and
security assertions remain unchanged. The run is failed, not an accepted release.

The original reviewer confirmed the principal UI/identity/ownership implementation
and found UX-R1: a shared failed/ended history row could override live Provider
call ownership. The client now preserves active/pending observation and in-flight
answers, and does not treat failed shared incoming history as terminal proof.
UX-R2 strengthens the fixture to account for every mutation, including unexpected
SMS/start/reject requests, and adds the late exact answer/cancellation interleaving.
Both follow-up counterexamples use the existing TLS fixture; temporary verification
now runs only these two new baseline cases, retaining the original four receipts.
At that checkpoint the follow-up was not built and no new APK was installed;
the subsequent qualification and native readback below supersede that status.

The reviewer approved UX-R1/UX-R2 code at `e50f939`, pending execution. Follow-up
[36535217963](https://github.com/lovitus/mdd-sim-gateway/actions/runs/36535217963)
built successfully but did not qualify: the late-receipt baseline test failed,
then its resource cleanup interrupted a still-blocked HTTP callback; throwing an
AssertionError from that background callback crashed instrumentation before the
second baseline method ran. This is not valid red evidence and the fixed suite
did not execute. Release the test barrier before Service/fixture teardown and
report barrier errors on the test thread; product logic and assertions are not
changed to make this pass. Both failed runs and their artifacts remain available.

Qualification completed on candidate
`f8a6a8ca09cc1d3993a79165d6431f7b815bccab`, tree
`59e1617286f4ff08b81d40a80fddaccebd30a019`, in hosted
[36536473242](https://github.com/lovitus/mdd-sim-gateway/actions/runs/36536473242).
API 28 and 35 each executed both additional baseline failures with the exact
incorrect `/end` count and premature null-owner assertions, then passed all 25
native tests with zero failures/errors/skips. The 21 JVM tests, build/lint,
scoped Core/Agent race/contracts and WebUI checks passed. Lint reported zero
errors and 45 warnings, not zero warnings. This Android workflow does not
represent a new full-repository Go qualification. API 35 artifact `11019212075`
digest `7e664d3aace27bb056f2c1e8cb0ebff0e8d9b505602649c2107464158e1ea655`;
API 28 artifact `11018659090` digest
`71801dd50c1c5c80db8c5af29a934ae841e2583a42ca2a90cc524f8a4ce422e5`.
The exact late-receipt test no longer crashes during teardown. This is synthetic
gateway/native-client behavior, not a new carrier or physical multi-phone test.

Signed v95 APK SHA-256:
`e1bcfaddcff29a7a6dd4645a0dd7c9f5cbf3e2a58da88bf65839145ac62ff40f`.
Its certificate remains the existing stable preview identity. The old installed
v91 APK was retained and its hash matches the earlier receipt. Final delivery
removes the temporary workflow input/call/script, preserving permanent gates,
product/test source and all hosted receipts. The original reviewer independently
downloaded and hashed the artifacts, parsed baseline/candidate XML and formally
closed UX-R1/UX-R2 and automatic acceptance on September 29. No new product or
test-matrix expansion was requested.

The authorized API 35 phone was updated with `install -r`, retaining the package
UID, login, certificate pin, availability and reader-sharing intent. All five
native pages were opened and inspected. Home showed actual operation-ready counts;
Calls showed separate colored VoWiFi/cellular states and exact line context.
Messages displayed all 18 retained conversations. A real 71-event conversation
loaded its first 50 and then all 71 through Older messages, including older records.
Reply to an existing received cellular event preselected the proven original SIM;
the explicit SIM/recipient confirmations prepared the correct route and recipient
with an empty body, without activating Send once. Readers retained the identified
USB card and distinguished unavailable OMAPI. Settings displayed v95/source
`f8a6a8ca09cc`. The installed APK was pulled back and its hash exactly matched the
qualified artifact above. The test-only recipient was cleared and prior selection
restored. There were no new calls, SMS sends, notification replays, permission
resets, server changes or sharing/network intent changes.

Private field-summary receipt SHA-256:
`e36be8b76741b559543781919ff1b661c42b9be0f6aac1ea7b4fa1c59aea298b`.
Raw screenshots, UI trees and message content remain outside Git. This is native
navigation/history/reply acceptance, not a new carrier or two-physical-client
contention test. Existing long-session and physical-network deferrals remain.
Non-blocking visual follow-ups are retained in the existing postponed ledger:
scroll the preselected reply row into view in long lists, and distinguish Home's
partial-observation note from the ready-count color. The correct selection and
warning text are present; these do not expand this qualified delivery.

PR [#15](https://github.com/lovitus/mdd-sim-gateway/pull/15) contains this single
complete batch. Integrated Android workflow
[36539491925](https://github.com/lovitus/mdd-sim-gateway/actions/runs/36539491925)
and full Go workflow
[36539492495](https://github.com/lovitus/mdd-sim-gateway/actions/runs/36539492495)
passed at head `d00575ed30c5208782159cba509bc86fb5e2e92a`. Their PR merge source
`050760e345336f33e7851c532cc2a7006edd54a0` has the same tree
`5f51e89d31ae09c1a35423931905b01407f74621`. Both final native XML reports again
contain 25 tests and zero failures/errors/skips. The original reviewer approved
normal merge subject to the existing gates and branch protection, finding only
a stale Android README status/link. That text is now synchronized; README changes
do not change app sources, tests, build inputs or the installed APK.

Unique next step: complete the authorized normal PR merge, without bypassing
branch protection. PR #15 is the authoritative merge receipt; do not infer merge
from this pre-merge record. The final record-only head preserves the qualified
runtime/test source and is not relabeled as either executed CI source or v95's
build source. No new APK, carrier test or server deployment belongs to close-out.

### September 29 reviewed merge and rollout

The owner authorized merge, deployment and updating the newly specified handset,
conditional on review, with issues returned to the same ChatGPT reviewer. No
previously delivered Android feature was withdrawn. The earlier PR #8 pairing and
server call-recovery exclusion and accepted endurance deferrals are unchanged.

PR #12 was normally merged at `0b663b9a1f04149a6c5eb771e2b1f7f699ddd867`;
its tree exactly matches reviewed head `7df49d4`. Signed Android workflow
[36520354543](https://github.com/lovitus/mdd-sim-gateway/actions/runs/36520354543)
passed. APK v91 SHA-256 is
`020ad0882c62660a726b91f288dd494e600fad463d5869a53d15bda848faa21c`.
Signature lineage, source and installed version were verified. The newly
authorized API 35 phone was updated from its existing v54 with `install -r`,
preserving package UID/data, remembered login, certificate pin, availability and
reader-sharing intent. The old APK and raw evidence remain private.

All five native pages were actually opened after upgrade. They showed real
catalog/history and v91/source readback, not mock data. Normal USB permission for
the attached reader was granted without changing sharing intent; native Readers
then read the exact card, and Core independently reported current card routing,
IMS and messaging readiness. This is startup/route verification, not a new paid
call/SMS or audio acceptance. No reset, uninstall, SIM/PIN change or replay occurred.
Core remains the already deployed `b6f3a4c`; its runtime matches the PR #12 merge,
so an identical Core is not restarted merely to change its revision label.

The same reviewer found one remaining Provider PR #13 defect: new receive reports
used the initial call-server registration after maintenance/recovery changed it.
The correction reads profile, binding and transport once from the existing runtime
registration owner and uses that snapshot to construct and send the RP report.
Existing call-dialog bindings, durable ingress, bare SIP response ordering,
stable message identity and paid retry policy are unchanged. Failure checklist:
stale B1 route/security, mixed B2/B3 transport, unavailable registration, persistence
or response-write failure. Tests cover the concrete B1-to-B2 regression using real
WireSIPFlow packets; temporary hosted validation reverts only snapshot selection
to the frozen B1 source, preserving compiled tests and wiring.

Hosted [36522474384](https://github.com/lovitus/mdd-sim-gateway/actions/runs/36522474384)
qualified candidate `a682e8d2203bcc5e8c552d812fb7690b1a7be8ac`, tree
`3a3c14a2913108827a5fd0c2bd480ff9b752fb4e`. The B1 old-behavior reversion
executed and failed both recovery subcases with the retired-transport error; it
is not a claim of running unchanged `9e0ec67`. Restoring only snapshot selection
passed 31 focused test/subtest results, plus the existing real registrar B1-to-B2
test, under race detection with zero skips. The artifact contains source IDs,
exact reversion patch and red/green JSON; artifact digest is
`ce99ca26c218baac32e954b2b4523f54bf5327c9047e0f560b5e8a57459ae9f5`.
The temporary hosted entry point is removed; permanent CI gates are unchanged.

Full integrated [36522731598](https://github.com/lovitus/mdd-sim-gateway/actions/runs/36522731598)
passed for reviewed head `657ee1f71d9ab25a4d7b4564be74dd8160b998aa`.
Its PR-merge artifact source `2fcfda83bd51a9747e7d874a8e91b103ccb6516e`
has the identical tree `bad38021a06a9846dfd49e4db425cc5298667057`.
The original reviewer independently inspected the exact code, tree equality,
reversion patch and JSON artifacts and formally closed the only P2. Core full
race, Provider/upstream, liveness, production graph/context and release gates
passed. Suite-level Xray and liveness opt-in skips were followed by passing
dedicated jobs; the simulator helper is invoked only by that liveness harness.
The opt-in real-systemd test was not enabled and is not claimed as passed.

Linux artifact `11013364589` ZIP SHA-256:
`4ae31509610803f53d75112858d4307eb410ccf28ea08d73d16128ac5bd54a7f`.
All 22 manifest entries were verified. Archive SHA-256:
`9bd75175505de9a016431e21b95a6c38dc501a995b3cbdd6ef770b82246af231`.
Deployed Provider SHA-256:
`2f0f55f9ef6f08f7865eb322aec85f1459757a7bd63084ef1d4738fb4aa5db8b`.

The first rollout failed its deployment-script check because systemd omitted an
empty EnvironmentFiles array. It was still under maintenance and rolled back all
seven process binaries and the exact prior trial overrides, released its lease
and did not rewind live databases. Fresh readback confirmed the original two
ready lines. The script normalized that optional empty array, moved environment
comparison before process start and checked the base unit hash. The second
explicit attempt reused the byte-verified artifact, acquired fresh idle/maintenance
ownership and completed at September 29 05:07:46 UTC.

Seven already-running Provider processes now use the verified binary, checked via
their actual PID executable, arguments, configuration/state path, user/environment
and stable PID. The shared Provider entry point also governs future starts; this
was not limited to a single-line trial. No previously stopped process or disabled
VoWiFi intent was enabled. Only the six known, byte-checked trial ExecStart
overrides were removed after backup; unknown drop-ins would have stopped cutover.
Old binaries, configuration and hashed state snapshots remain available; rollback
does not restore those snapshots over newly written business records.

Both previously ready lines recovered IMS/messaging readiness; absent cards and
disabled lines remain honestly unavailable. All maintenance leases were released.
Independent final readback found the catalog and notification configuration exactly
unchanged, Core binary unchanged and Core/Agent/egress/apply PIDs unchanged.
There were zero new paid calls, SMS sends or manual replays. Readiness is not a new
carrier-delivery or acoustic test. The earlier `9e0ec67` receive-report 202 remains
scoped historical evidence, not a claim of another report accepted after rollout.

Private operator evidence SHA-256 (raw material stays outside Git): first failed
rollout `d86dc080dbba234170b03132fabfab27fd061b65814bdbb4c24a890a47997dac`;
successful rollout `e1f11f05576cf783e815363e4ddd49f4a84ad743a99bdcab7dc737f4c006f5d0`;
independent final readback `587251460981f30da4548014df3b24cdb4980a540ecc36df5eebc27b5e40159f`.
These are traceable operator reports, not reviewer-operated hardware tests.
After Provider deployment, the phone's Calls and Messages pages independently
showed ready routes; Home retained the read card, availability, history and no
unresolved message/call recovery. No send or call control was activated.

Close-out: the final records-only head preserves the qualified runtime, tests,
build inputs and permanent workflows; it does not claim a new CI execution or
relabel the deployed binary's source. PR #13 is the authoritative normal-merge
receipt. No new implementation, paid test or endurance gate belongs to this batch.
The pre-existing dirty Provider record updates were absorbed by workstream identity,
not by replacing the newer Android ledger; original dirty worktrees remain intact.

### September 29 PR #12 review correction (closed history)

The owner authorized continuing the concrete review in the existing ChatGPT
conversation and publishing the changes for that same reviewer. N1/N2/N3 form one
client-only correction batch; no Core, Provider, desktop, pairing or durable server
call-recovery expansion. The September 27 scoped physical acceptance and accepted
endurance deferrals remain intact. This entry supersedes older next-step text.

Status: N1/N2/N3 are closed after code review and hosted regression validation.
The original reviewer independently checked the exact source and both emulator
XML artifacts, including the two additional N3 findings. At this review checkpoint
the draft was unmerged and undeployed; the rollout section above supersedes that
earlier deployment state.

- N1: persist whole-submission certainty separately from failed delivery display.
  Exact accepted responses resolve the submission budget in either arrival order;
  failure details remain red. Migrate legacy `submitted` before observations change
  its display state. Legacy `failure_observed` without an independent confirmation
  remains unknown; part history cannot reconstruct a lost whole response. Unknown
  fields do not fall back to legacy success. No automatic SMS retry.
- N2: the actual history contract lacks `card_id`. Label its historical identity
  unknown and current catalog information as current, not as the original SIM.
  Reply requires an explicit current-line item selection and confirmation, keeps
  draft replacement confirmation, then rechecks that exact line/card. Trusted
  historical card IDs still require the original card. Sending continues to bind
  `expected_card_id` to the frozen intent and revalidate before dispatch.
- N3: recover unreadable local settings only after two confirmations. Preserve all
  readable original bytes and a presence/SHA-256 manifest in the private no-backup
  directory; verify copies before replacement. Material changes invalidate consent.
  Readable recovery candidates and known in-memory pending operations block reset.
  Unknown unreadable records require an explicit warning, not a claim of no remote
  work. A durable reset marker prevents old-state/XML fallback after interruption.
  Capture storage ownership when intents are queued, invalidate old writers and
  UI/Service callbacks, and publish only paused, credential-free state. Preserve
  the existing Keystore key; unavailable keys or failed archival remain errors.

This reuses the earlier repository client F6 recovery mechanisms, not its excluded
server changes. Android's [Keystore contract](https://developer.android.com/privacy-and-security/keystore)
and [no-backup directory guidance](https://developer.android.com/identity/data/autobackup)
support keeping keys non-exportable and recovery material private. Retained
ciphertext is not a guarantee that a lost key or damaged file is recoverable.

Pre-fix counterexample: existing workflow
[36513116448](https://github.com/lovitus/mdd-sim-gateway/actions/runs/36513116448),
temporary verification-only commit `4142facfab919cc86abc022f5622fd89d56c60bf`
on runtime baseline `86cb9de`. Build/JVM/lint and Core checks passed. Both API 28
and API 35 executed 16 methods: 13 original passes, exactly three new behavioral
failures and zero skips. N1 lost resolved status after delivery failure; N2 lacked
historical/current identity separation; N3 never exposed a recovery action after
key-only initialization loss. No missing-symbol compilation failure is counted.
The existing JVM assertion that a whole-confirmed failed delivery is unresolved
was corrected because it encoded the N1 defect, not to hide an unknown submission.

The correction adds native TLS/UI and encrypted-state regression checks, including
128-entry/body limits, both receipt orders, conservative legacy migration,
confirmation-time card rebinding, cancellation, key-only/marker-only/partial-new
recovery, material changes, queued old writers and known unresolved backup state.
First candidate `a2f4bd8` was pushed as one complete implementation/test/documentation
commit. Android workflow [36514247706](https://github.com/lovitus/mdd-sim-gateway/actions/runs/36514247706)
built and passed JVM/lint/Core checks. API 28/35 each passed 15 of 16 native methods:
N1 and N2 passed; N3 did not finish (API 28 line 124; API 35 reached the later
scenario at line 143). The fixture did not drain asynchronous
Activity.onStop draft persistence before removing its files; the correction waits
for the existing intent queue before corruption, without changing the assertion.
Also preserve reset-commit provenance across ordinary clear, so it remains readable
with the durable marker. At that stage, post-fix validation and review remained
pending; the final results below now supersede that gate. Full Go workflow
`36514247965` exceeded one 600-second observation window; its terminal state was
not observed, not classified as a code failure. No paid call/SMS, notification
replay, hardware mutation or production deployment has been performed.

The original reviewer accepted N1/N2 at `a2f4bd8` and identified two additional N3
boundaries: retained Activity login drafts need their own storage generation, and
intent admission/construction must read that generation without waiting on the
I/O lock. Native regression methods now reproduce those exact interleavings with
bounded barriers, not orientation/layout work. The complete batch candidate is
used for hosted pre-fix evidence under the September 29 owner exception; it is not
a new test-only delivery commit or a request to merge a known-red candidate.
The correction makes generation snapshots volatile/nonblocking while
retaining locked owner checks, and binds retained login drafts and their writes
to their source generation. Normal Activity recreation still keeps a valid draft;
reset-era retained credentials cannot be saved under a newer storage owner. This
is credential/lifecycle protection, not landscape compatibility work.

Hosted counterexample candidate `724564dd694a67bec06c45e64f6d3cb6de5d9cc8`
ran in [36516219462](https://github.com/lovitus/mdd-sim-gateway/actions/runs/36516219462).
Both API levels executed 18 methods: 16 passed, exactly the two new N3 regressions
failed, zero skipped/errors. The retained draft assertion observed the old fixture
password after reset; the main-thread heartbeat could not run until the disk lock
was released. The original N3 method now passed completely after draining the
fixture queue, including stale material, old writers and unresolved backup guards.
These are behavior failures on compiled code, not inferred source checks. N1's
JSON serialize/reparse coverage is not an Android process-death acceptance claim.
The corresponding production changes preserve locked writes, add lock-free epoch
snapshots and bind login drafts/readbacks/login saves to their original epoch.
Availability admission also obtains the current store wrapper if the Service was
recreated during a reset; it does not automatically enable availability or sharing.
The candidate history is consolidated into one delivery commit above `86cb9de`;
prior run/head evidence stays recorded here.

Final qualification: reviewed head `33f8440181e51aaa3724920fcf45b17cf9ea66f2`
and PR test merge `5eea6e757ec23fadd79e9fa18932ed1aa258eb8e` have the identical
tree `132a159d0a129d203a10af343d5105dc15169c1b`. Android workflow
[36517184960](https://github.com/lovitus/mdd-sim-gateway/actions/runs/36517184960)
succeeded. API 28 and API 35 each executed 18 methods, with zero failures, errors
or skips; all five `ReviewRegressionTest` methods completed. The Core/Agent race
artifact contains 221 passing tests and no skips. Build, JVM, lint, WebUI and
repository checks passed. Preview signing/publication was not selected for this
PR event, so this is not a newly signed or installed APK.

The downloaded emulator ZIP hashes match GitHub's artifact digests:

- API 28: `e773a4378dfdbe9370146146b1c85df4aea903d1a210f90000f241179a23d2b5`.
- API 35: `a12ac974c06f09b2f744fad3429f6d6b508cf2203dc36b2060472d0080f5f0b1`.

Full Go workflow [36517185239](https://github.com/lovitus/mdd-sim-gateway/actions/runs/36517185239)
also succeeded at that exact head/tree. The retained logs include all 82 Linux
Core race-tested packages, the production graph/context checks and real loopback
Xray verification. Non-verbose module output does not establish zero opt-in test
skips or new hardware acceptance. Raw logs/XML remain private.

The original ChatGPT reviewer independently verified both ZIP hashes, all 18 XML
methods on each API and their five regression methods, then formally closed
N1/N2/N3 including both follow-up N3 findings. Activity recreation is not process
death; the main-thread barrier is not a physical slow-flash measurement. Existing
physical and endurance evidence retains its original limits. Final publication
only updates this cursor, README, ledger and generated summaries; runtime code,
tests and build configuration remain identical to the qualified source above.
The documentation head is not claimed to have rerun those tests.

Unique next: owner review of the complete correction in draft PR #12; no remaining
N1/N2/N3 implementation or automated-validation blocker. Separately review PR #13
before any integrated release: merging or redeploying #12 alone does not carry the
trial Provider fixes. PR #14 is not an Android merge prerequisite. No automatic
merge, signing, APK installation, production rollout or paid revalidation.

### September 27 GitHub review publication

The owner now requires all non-sensitive work and records to be available through
the GitHub review plugin. This supersedes the earlier local-only/no-stage direction
for this execution record. The complete redacted narrative is published with the
Android README, owner decisions, acceptance ledger, generated TODO/postponed files
and repository instructions in one documentation delivery batch on PR #12.
Earlier references to an unpublished cursor or pending documentation are historical.
The unrelated dirty recovery worktree is not part of this PR and is not imported.

Review entry points: [Android behavior and acceptance](../../android-agent/README.md),
[current owner decisions](2026-09-21-android-agent.md),
[ledger and source boundaries](../status/README.md), and
[deferred follow-up](../../postponed-tasks.md). The current milestone below governs
the historical experiments and old next-step instructions further down this file.
PR #12 remains draft for owner review; publication does not authorize a merge.

| Runtime or dependency | Qualified source / build | Delivery boundary |
| --- | --- | --- |
| Signed Android v85 | `a1bf28b850c81cb050289195e361b0dd15475a31`; [workflow 36292625372](https://github.com/lovitus/mdd-sim-gateway/actions/runs/36292625372) | Installed once with retained data. APK SHA-256 `787f21a99e5038000a849afb15d671343a7aa5ccde460283412023c15a390d25`. |
| Minimal Core support | `b6f3a4c32a6c2456544474751835d880034a0efe`; [full Go workflow 36298445701](https://github.com/lovitus/mdd-sim-gateway/actions/runs/36298445701) | Rollback-backed production deployment. Core SHA-256 `08185d363a50b3873fd77c0b81c157f6a055c59a7bc23f792ae15c1656fb643a`. |
| Separate Provider fixes | `9e0ec6772f9e7c34c365e2dcce8694446c087a17`; [PR #13](https://github.com/lovitus/mdd-sim-gateway/pull/13), [workflow 36264788240](https://github.com/lovitus/mdd-sim-gateway/actions/runs/36264788240) | One authorized line trial, not general Provider deployment or part of this Android code diff. |
| Separate desktop warning | `38f11912c7774368bc7d7984998fd771e576f244`; [PR #14](https://github.com/lovitus/mdd-sim-gateway/pull/14) | Isolated platform evidence only; no production rollout and not an Android delivery gate. |

The publication batch changes documentation only. These CI results qualify their
exact source revisions, not a newly tested documentation head. No build, hardware
test, paid operation or deployment is repeated for publication. Reviewers can
inspect code, decisions, failure history, reported results and remaining limits
in GitHub without relying on an inaccessible local summary.

Private credentials, host addresses/aliases, full card/phone identifiers, raw
logs/configuration/audio and device captures are not uploaded. The narrative is a
redacted field report, not independently replayable raw hardware evidence. Retained
private evidence names identify the underlying receipts without publishing their
contents or claiming they are accessible to the plugin. An exact private backup
of the pre-publication execution record is retained with SHA-256
`a13d1e19d58fd4a2725cc38219c0ae64e132a74b93245d33ddb04ef425a4d9af`;
that hash identifies the report, not a runtime artifact or a passed hardware test.

### September 27 Android preview stage closed for owner review

The owner's latest direction narrows this stage to the Android Agent PR and
accepts implemented-but-not-endurance-qualified recovery. Main reader/call/SMS
and native usability evidence below is sufficient for this preview milestone.
Do not continue desktop isolation archaeology or build new acceptance matrices
to keep this Android task open. ANDROID_PREVIEW_CLOSE is recorded in the existing
Android decision document and generated postponed ledger.

Frozen runtime artifacts remain Android v85 / a1bf28b and Core b6f3a4c. PR #12 is
still draft and unmerged for owner review. The separately tracked Provider PR #13
is still a scoped trial; desktop PR #14 is not an Android review/deployment gate.
Long-duration USB/OEM/battery validation, extreme handover, historical USB timeout
causality and meaningful desktop follow-up remain explicit non-blocking backlog.
Do not weaken any safety gate, certify an unrun test or claim the whole repository
complete. Existing fault hints and bounded/manual recovery remain part of delivery.

Unique next: owner reviews PR #12 and its separate Provider dependency; respond
to concrete review findings or a newly reproduced main-flow defect. No further
paid test, fault injection, rebuild, redeployment or documentation-only micro-commit
is required for this staged close. Preserve the local post-CI documentation and
the separate dirty recovery worktree. Earlier cursor entries below are historical;
their old next-step text does not reopen this milestone.

### September 27 main-flow delivery readback

The network-extremes decision below is applied and is not an open prerequisite.
The subsequent read-only delivery check found signed v85 still in installed PID
5937 with its foreground availability service. Production Core is still b6f3a4c
with the qualified binary SHA-256; six Agents and ten desired lines are present,
with no active call/media/data/raw ownership. The exact Android card's route,
IMS and messaging facts are current; VoWiFi call/SMS admission is ready. These
readbacks do not substitute for the retained actual voice and self-SMS receipts.

Twenty-five existing action-related Core readbacks now span 04:05:23 through
06:40:09 UTC, 154.78 minutes. They retain the same Android process generation,
reader session, identified card and desired catalog, with fresh last-seen reports.
There are five distinct control-connection timestamps, including the Core rollout
interval, so this is sampled continuity/recovery, not unbroken socket uptime.
The installed App PID and final service PID also match. The current screenshot
is black and the filtered USB log is empty; neither is positive UI/error-free
evidence. No unlock, App restart, replug or reader/network fault was induced.

A separate single read-only history request at 06:45:41 UTC returned the same ten
events for the exact attached-card line as the earlier post-rollout mobile
snapshot. No new event IDs appeared. This extends the scoped history/dedup
observation without sending or replaying any SMS; it does not directly inspect
Telegram or prove future carrier behavior. The temporary normal login logged out.
Private evidence: android-usb-events-20260927/closure-mainflow/{continuity,
sms-summary,sms-history}.json and service.stdout; mobile-core-rollout/
closure-mainflow.json and closure-sms-history.stdout. Raw records remain private.

PR #12 b6f3a4c and PR #13 9e0ec67 remain open drafts. PR #14 38f1191 is open and
review-ready. All three are mergeable against the same current base, with no
submitted review. No CI rerun, commit, merge, deployment or paid test occurred.
Only post-CI evidence/owner-decision documents remain dirty in this worktree;
preserve the large local cursor and the separate dirty recovery worktree.

Unique next: owner review of these existing delivery candidates, then respond to
actual review findings. Do not substitute another healthy snapshot, short idle
loop, data-SIM question or document-only micro-commit for that handoff. Historical
USB timeout causality and all-day USB/OEM/battery qualification remain unproven;
do not erase those limits or claim whole-project completion. Independent main-flow
defects, if actually reported/reproduced, still take priority over review waiting.

### September 27 owner clarification: network extremes are non-blocking

The owner explicitly excluded extreme network-switching validation as a blocker
for the main workflow. The earlier data-SIM question is resolved: leave actual
Wi-Fi/cellular handover unverified and non-blocking, without asking again or
altering the phone's network. Ordinary reconnect/session recovery remains required
and retains its existing evidence. See ANDROID_NETWORK_EXTREMES in the Android
decision document. This does not defer unrelated reader/call/SMS defects or claim
that a missing test passed.

Unique next: finish the current main-flow PR delivery record for the deployed
Android/Core and separate Provider fixes. Preserve remaining USB/battery limits
and actual failure evidence without turning this deferred network subcase into
a new gate. Owner PR review still precedes merge; it is not a reason to stop
independent main-flow work. No new paid test, network experiment, rebuild or
deployment is required for this scope correction.

### September 27 owner-requirement closure audit

The owner's answer, "both sides could hear", qualifies the interrupted incoming
call's two-way connectivity. Do not repeat it. The corresponding history, native
Answer action, fallback termination and final idle readback remain recorded below.
This audit changes no source, deployment, device setting or paid-operation grant.

| Original requirement | Current evidence | Remaining boundary |
| --- | --- | --- |
| Attached reader registration and keepalive | Signed v85 identifies and shares the original card; retained Core reports agree on identity and freshness. | One startup write timeout recovered automatically. The historical USB fault's cause is not established. |
| Calls using the attached eSIM, directly if simple or through Core | USB reader has no radio; the existing server VoWiFi path has actual outgoing speech evidence. Phone-installed subscriptions use the system dialer. | Restricted OMAPI and phone-native subscription calling are not hardware-qualified on this handset. Do not introduce a new direct-modem bridge. |
| Calls and SMS for server SIMs/modems | Scoped VoWiFi/modem outgoing calls, modem incoming Answer/two-way speech, one self-SMS, history and exact-card Reply are accepted. | Carrier/route cases are not interchangeable. UK incoming remains owner-deferred; native Hang up was accepted on an earlier outgoing call, not the latest incoming attempt. |
| Long-session keepalive | Fifteen existing v85 readbacks span about 67 minutes with the same reader session/card/process generation, including one control reconnect. Session-loss renewal and silent-connection recovery have separate physical evidence. | Sampled continuity is not gap-free or overnight qualification. Successful session lookup extends the existing sliding timeout; no artificial twelve-hour wait is required to repeat renewal proof. |
| Recovery on unstable cellular networking | Physical socket reset, isolated-Core silence/restart and malformed-frame recovery are recorded. NetworkCallback and capped reconnect remain client-owned and respect Pause. | The owner made extreme switching validation non-blocking on September 27. Actual Wi-Fi/cellular-data handover remains unverified, not failed or passed; no phone data SIM is required to continue the main workflow. |
| Battery-friendly operation | No idle wake lock; bounded deep-IDLE entry/exit passed. The deployed timestamp-only traffic fix reduced the observed idle cost as recorded below. | Different-duration traffic/CPU samples do not establish all-day battery life or every OEM's background behavior. |
| Usable pages and feedback | All five native pages and scoped call controls, history, Reply, state colors and reader-action feedback have physical evidence. | Do not expand into landscape, all audio-route permutations or a new UI framework. |

PR #12 is the draft Android delivery at b6f3a4c; #13 is the separate draft
Provider fix at 9e0ec67. Both remain unmerged for owner review. PR #14 is the
review-ready desktop warning fix at 38f1191, with isolated Mac/Windows acceptance
but no production rollout. Android v85, Core b6f3a4c and the single-line Provider
trial are already deployed; repository merge is a separate fact. The five dirty
documents contain post-CI evidence, including this large local cursor, not hidden
unsubmitted runtime changes. Preserve them without a documentation micro-commit.

The owner has now resolved the missing-data-SIM question: extreme switching
validation is non-blocking. Do not repeat that question or request a data SIM to
continue the main workflow. The clarification above is the current next action.
No current code defect is inferred solely from an unqualified endurance scenario.
Long-duration USB/battery acceptance and historical root causes remain explicit;
no overall-completion claim, automatic merge, desktop rollout, paid retry or new
network experiment follows from this audit. Earlier next-step paragraphs below
are historical and do not restart completed checks.

### September 27 timestamp-only mobile traffic correction

Prior turn made progress: the actual v85 background sample found about 19 MB in
eight minutes, and four live Core frames isolated timestamp-only 150 KB snapshots.
The candidate now copies only the digest input and clears readiness ReceivedAt/
ExpiresAt there. It neither mutates the shared snapshot nor strips wire fields.
Freshness/expiry is recomputed by the original reducer, with every other field,
identity, generation, sequence, call/SMS event and timestamp still significant.
No compression, auth, client lifecycle, Provider or operation policy changes.

Failure checklist: redundant frames persist; shared cache is mutated; stale facts
are hidden; business/identity changes are suppressed; original snapshot clocks
are lost. Two compiled regressions failed with the normalization loop removed,
then passed under race with it restored. The WebSocket case uses real replay
confirmation and expiry, not a mocked stream. Its first attempt had an incomplete
enabled-line SIM fixture; preserve that setup failure but exclude it from red
proof. Private mobile-regression-{red,red-valid,green}.json retain exact output.

Commit b6f3a4c32a6c2456544474751835d880034a0efe, tree
b5e8eef5f3045e469f83f20d2530727c5d12d38f, is pushed to the existing draft PR #12.
It includes this minimal transport fix and the accumulated public field records;
this large local cursor is excluded. Exact-head Go workflow 36298445701 passed.
The Linux archive matched GitHub's SHA-256 and the Core binary matched its
manifest and clean build provenance. Only mobile.go/mobile_test.go differ in Go
from the previous production source. Stable Android v85 was not rebuilt/reinstalled.

Production Core now runs b6f3a4c, SHA-256
08185d363a50b3873fd77c0b81c157f6a055c59a7bc23f792ae15c1656fb643a.
The maintenance/idle-guarded rollout retained all unrelated service PIDs, six
Agent process generations, desired lines and notifications. Maintenance resumed.
The new offline Core/config backup was copied and SHA-256 verified:
a135cfaa203d99be647065dbc95881f2a872f000c4ca5bb4f038b604a904b3c4.
Prior Core and binary link remain available for rollback. No Provider, desktop
Agent, phone package, user switch or paid operation was changed.

The actual post-rollout stream supplied one initial 149949-byte snapshot and two
heartbeats in a bounded 65-second observation, 150143 bytes total. There were no
timestamp-only updates. A separate two-minute background handset sample received
about 0.01 MB (rounded system counters), compared with about 19 MB in the earlier
eight-minute pre-fix sample. CPU rose 151 ms over 120.578 seconds, versus 6999 ms
over 480.681 seconds before. App PID remained 5937; service/card/desired state and
wake-lock counters were unchanged, final Core facts fresh and call/media idle.
Do not turn the different-duration samples into laboratory power/battery claims.

Actual Readers and Home were inspected after authorized unlock: Gateway online,
the same identified card, sharing/availability retained, no reader needing action
and no call recovery. Preserve the initially black locked-screen capture as such.
The Home activity list records a later short reader-link disconnect/reconnect;
do not claim uninterrupted socket uptime or diagnose its cause from that label.
All one-shot collectors, private observer sockets/sessions and tunnels ended.
Private evidence: mobile-core-build/, mobile-core-rollout/, mobile-payload-after/,
battery-idle-after/ and native mobile-traffic-{unlocked,home} captures in
provider-evidence-36225153970. No micro-commit for post-CI acceptance documents.

Unique next: reconcile the cumulative owner requirements and remaining PR/release
boundaries using these completed receipts. Do not rerun passed traffic, incoming,
malformed/session-loss, USB lifecycle or bounded Doze checks, restart/reinstall a
healthy client, or generate another paid test. PR #12 stays draft for owner review;
separate Provider/desktop PRs retain their own scope. Long-duration battery/OEM
qualification, historical startup errno cause and restricted OMAPI are not proved
by these scoped checks and must not be silently converted into completion.

### September 27 USB packet receive correction and bounded Doze qualification

The actual reader advertises 16-byte bulk endpoints. Before this correction UsbCard asked USBFS
for 65,546 bytes even for a ten-byte slot-status response. Retained field evidence
contains a read -12 and repeated write -110 failures; neither establishes their
common cause. Android denies shell access to raw sysfs descriptors. Preserve that
denial rather than interpreting its text as a malformed USB descriptor.

Adapt only the packet-sized CCID receive/assembly approach from OpenEUICC's
UsbCcidTransceiver (1c70ca7a701adc0047c4d6c12579a9b072a3a244). Leave native
transport, writes, reset budget, PIN/AKA, Core, Provider and paid operations alone.
Keep the existing overall deadline, extension limit, response size and exact
slot/sequence/type checks. The relevant failure checklist is full-sized final
USB packets, a trailing zero packet before the next response, split headers,
truncated/oversized/trailing frames, negative native results and reader closure.
The one-time compiled pre/post check is complete: both tests failed with the old
oversized read restored (allocation refusal and full-packet timeout), then passed
with endpoint-sized reads. See private ccid-regression-{red,green}.json. No new
CI machinery or source-matching test was added. Whole deadline, extension and
identity gates remain in UsbCard; only bounded byte assembly moved into Ccid.
Commit a1bf28b850c81cb050289195e361b0dd15475a31, tree
4afb4a2a4278cee7beadc5c667c5caecf2990d16, passed the complete signed Android
workflow 36292625372 (21 JVM, 13 native fixtures on each API 28/35, 219 scoped
Core/agentlink race cases; no failures/skips). Source/tree, archive hashes, stable
signer, non-debuggable manifest and all four native ABIs were checked. APK SHA-256
787f21a99e5038000a849afb15d671343a7aa5ccde460283412023c15a390d25; one retained-data
v85 install completed 04:04:35 UTC. Core fd8fc0b and the separate Provider trial
were not changed. Draft PR #12 is the same delivery line; no merge occurred.

One initial power-on write -110 persisted, then bounded automatic reset identified
the original card. Native Home and Readers showed need-action zero. A subsequent
eight-minute background observation retained App PID/service with no additional
first-scan fault/reset. Core 04:15:23 UTC confirmed unchanged card/desired lines,
current Agent/card route/IMS/messaging facts and no active call/media ownership.
There was no replug, manual reset, paid call/SMS, PIN or configuration change.

Do not overstate the idle sample: the phone woke about 15.9 seconds after the
ordinary sleep command and was Awake/ACTIVE at the planned seven-minute endpoint.
That sample supplies no continuous sleep/deep-idle/battery acceptance. No screen-keeping
app, shared ADB, power exemption or network was modified. The observer ended and
the App was returned to Readers. The earlier incoming two-way speech acceptance
below remains complete and must not be repeated for this USB task.

Private evidence: android-usb-events-20260927/packet-build/{integrity,install,
ci-summary,idle}.json, idle-{before,locked,after} logs/power/service records,
core-rollout/{before-packet-sleep,after-packet-idle}.json and native packet-v85
captures in provider-evidence-36225153970. Local build-tool path correction during
artifact inspection did not rerun or weaken CI.

A separate official device-idle check then entered forced deep IDLE for four
minutes with the screen off and the availability service active. App PID, Agent
process generation, reader session and socket connection were unchanged; the
subsequent Core report advanced and was 227 ms old. This is evidence of the same
live attachment across this interval, not merely a cached card label. Empty
filtered log buffers are not independent proof that no error occurred.

Cleanup required more than unforce: the forced flag cleared, but the phone stayed
in natural IDLE. A motion event and authorized screen unlock restored
mForceIdle=false, mScreenOn=true and mState=ACTIVE. Native Readers then showed the
original card, sharing on and need-action zero. Final Core at 04:37:22 UTC showed
fresh card routing/IMS/messaging, idle call/media owners and unchanged desired
state/revision. No battery exemption, screen-keeping app, network, other device,
App process, user switch or paid operation was changed. All temporary observation
ended. Four-minute deep-idle entry/exit is now exercised; long idle/battery life
and the initial write-timeout cause remain unqualified.

Private evidence: android-usb-events-20260927/idle-followup/{doze-result,
doze-identity-comparison}.json, doze-sequence.stdout, doze-final-state.stdout,
doze-restoration-state.stdout and after-unlock-state.txt; core-rollout/
after-doze-restored.json; native doze-final-readers XML/PNG in
provider-evidence-36225153970. Retain the initially black/keyguard captures and
natural-IDLE cleanup intermediate rather than labeling them successful App UI.

Session evidence reconciliation: the retained isolated-Core silence and restart
receipts both show automatic recovery in the same QA process. The restart receipt
also shows changed encrypted session state. Current renewSession and the session
rejection/reconnect decisions in Link retain that logic; Link's later changes add
bounded diagnostics. This preserves invalid-session recovery evidence, not a new
elapsed twelve-hour expiry test. The Core timeout is sliding on successful session
lookup; a healthy observer is not a fixed-twelve-hour expiry experiment.

The first extra malformed-message attempts did not execute their cases and were
NOT accepted.
The installed CI QA v56 hash matched its retained artifact. Its encrypted state
had changed since the old isolated-Core test because later QA work used production;
that state was therefore backed up before any UI change. First attempt could not
reach the QA controls while locked. After authorized unlock, the second attempt
reached the local synthetic login endpoint but the old UI helper did not submit
the intended fixture credentials; the actual screenshot shows its 401 rejection.
Zero malformed cases ran. Do not attribute this harness/setup failure to Link or
silently accept any credential to make it pass. Both attempts restored the exact
encrypted-state bytes, force-stopped only QA, removed their reverse mapping and
closed their local listener. Stable v85 kept its PID; final production readback at
04:57:55 UTC was idle with unchanged revision, desired catalog and notifications.
Private protocol-hil*/result.json and native captures retain the failures and
cleanup. An earlier fixture help invocation unexpectedly started its loopback
server; its exact owned PID was terminated, without a phone connection.

The input issue was subsequently narrowed to Android's autofill dialog taking
focus. Field-focus/readback checks caught it; the later successful login was
covered by the separate system save-password dialog. Those are retained harness
failures, not Link failures. The final bounded run dismissed those specific
dialogs without disabling the password manager or saving the fixture password.
The local peer checked the exact temporary credentials; no auth gate was relaxed.

At 05:11 UTC all four actual QA observer cases ran: invalid JSON after a valid
snapshot, unexpected message type, unsupported schema, and heartbeat before the
first snapshot. Each connection closed and the client made the next connection;
the fifth received a valid snapshot. Native Home showed online and restored
activity in the same QA process. Readers confirmed sharing off. This used the
already installed CI QA f627204 (SHA-256 eee94a54625a03c4f597d62dd4362b19fa4f2a605e201df57a571cd01b572672),
whose malformed-message decisions/reconnect flow are unchanged in a1bf28b apart
from diagnostic recording. It is scoped mechanism qualification, not fresh full
v85, carrier, reader-link, red/green or elapsed-session-expiry acceptance.
The exact prior QA encrypted bytes were restored and QA was stopped. Stable v85
kept its PID. The final 05:12:21 UTC production check was idle; temporary TLS peer,
reverse mapping and UI capture were removed. No paid operation, SIM command,
production change, rebuild or reinstall. Private evidence is protocol-hil-dialog-complete/
result.json and native XML/PNG, with previous attempts retained alongside it.

Existing production receipts also supply a 66.98-minute sampled reader interval:
04:05:23 to 05:12:21 UTC, fifteen readbacks, unchanged Agent process generation,
reader session and card, fresh heartbeats and unchanged desired catalog. There
was one observed control reconnection at 04:21:58; do not call this unbroken
socket uptime. These were existing action-related readbacks, not a new poller.
See private retained-session-span.json. They do not prove absence of every
between-sample outage, overnight endurance or measured battery efficiency.

A single 480-second background cost sample then retained stable v85 PID, card,
availability service and desired catalog. CPU increased 6.999 seconds, foreground
activity increased zero, and recorded wake-lock counters were unchanged. Final
Core routing/IMS/messaging facts were fresh and call/media ownership idle. This
used read-only charged statistics without resetting history, forcing idle,
changing exemptions/power/network settings or generating a paid action. It
returned to the App and left no sampler running. Evidence: battery-idle/result.json
and before/after dumps; core-rollout/{before,after}-battery-idle.json.

The same sample recorded about 19 MB received and 0.46 MB sent. A separate bounded
read-only observer on the actual Core captured four roughly 150 KB snapshots at
about three-second intervals. Across all three comparisons, only readiness fact
received_at/expires_at changed. Ten lines occupied 120780 bytes (119593 in
operations) and repeated message history another 29099 bytes. No active call or
incoming event was present. This identifies a real mobile-stream traffic defect:
mobileState hashes complete volatile fact timestamps, defeating intended
unchanged-state suppression. It does not prove every UID byte had that cause or
provide a laboratory battery estimate. The diagnostic session logged out and its
socket/tunnel closed; raw frames and result remain private in mobile-payload/.
No Core or client source/configuration was changed for this observation.

Unique next: address the reproduced timestamp-only mobile-snapshot traffic issue
with a separately justified minimal transport adaptation. Client-only suppression
after receipt cannot reduce transmitted bytes; preserve live freshness, exact
identity, incoming/SMS changes and every server operation guard. Do not broaden
this into pairing, server call recovery, compression/security policy or a new
notification transport. Do not repeat passed malformed-message, session-loss,
incoming or USB-event cases, or prolong short healthy loops. Startup write-timeout
cause, long-duration/battery qualification and restricted OMAPI remain distinct
limitations. Keep PR #12 draft for owner review and the large local cursor/post-CI
evidence out of micro-commits.

### September 27 authorized rollout and resumed incoming acceptance

The owner explicitly authorized deployment and resuming the interrupted test.
Core fd8fc0bbb7f53365fcc0d371540d22da811eca00 is now running in production;
Android v84 was already installed and was not reinstalled. The Core binary hash
is 4a4dbfc6494b66b24d0aa1d6e54bc755c33bd4bd7b494b0c06db6311fe48c255.
Only the Core executable link and Core process changed. Provider, apply-helper,
egress and desktop Agent PIDs were preserved; six pre-rollout Agent generations
reconnected. Desired lines, notifications and configuration files were unchanged.
The offline Core/config backup was copied back and SHA-256 verified:
ce7cf9a347471136afa250340173c817a8362d529a70e688694e5428906f0414.
Maintenance was resumed. The independent Provider trial remains unchanged.

One owner-assisted mainland incoming call then reached both real mobile/browser
streams with the same exact event/card identity. Actual native XML/PNG showed
Answer and Decline. Native Answer was clicked; history records answered at
03:27:58.788315620 UTC and ended at 03:28:44.732022495 UTC (45.944 seconds).
The owner explicitly confirmed both phones could hear each other. This qualifies
incoming display, answer and two-way speech connectivity, not audio quality or
long-call durability. No outgoing call or SMS was sent by this task.

Retain the automation failure: the in-call screenshot timed out after six seconds,
so the planned native Hang up was not executed. The independently armed device
fallback stopped the same App process at 38 seconds; the first readback still
showed the session, and the later Core/Agent-backed history confirmed it ended.
The in-call XML also showed audio reconnecting. Do not relabel this as successful
native Hang up or as a proved network cause. The owner was told to hang up and not
redial; no automatic retry occurred. App reopening restored saved availability,
sharing and the identified USB card. Final Core at 03:32:49 UTC had zero active
calls/media sessions and current reader/IMS/message readiness; native Home later
showed no incoming or call-recovery controls. Another line's intervening incoming
call was not answered or rejected by this task. All observers/guards/tunnels ended.

Private evidence: android-usb-events-20260927/core-rollout/{summary,backup,
incoming-cleanup-readback,after-client-restored}.json; owner-incoming-after-core/
{incoming-event,incoming-native,during-call,result} files; bounded Android/Core
logs; final native captures in provider-evidence-36225153970. The real owner
response confirms two-way audio; counters alone were not used as speech proof.

Unique next step: retain this completed rollout/incoming connectivity evidence,
keep PR #12 draft for owner review, and address only the remaining scoped USB
durability/long-session acceptance. Do not repeat CI, APK install, Core rollout,
paid SMS or this passed voice-connectivity check. Native Hang up was not exercised
in this incoming attempt; preserve earlier scoped outgoing native-hangup evidence.
Keep the large local cursor and post-CI evidence changes out of a micro-commit.

### September 27 combined incoming/USB event-recovery batch

The latest owner request explicitly adds automatic reader checking/recovery at
major events while the UI or foreground service is active, stopping on explicit
exit/Pause and respecting sharing-off. It supersedes the Core-only next step
below. Commit fd8fc0bbb7f53365fcc0d371540d22da811eca00, tree
191ffff334992c449c4af0450dded1b78c13c08d, is pushed to draft PR #12. Full Go
36289239524 and signed Android 36289241552 passed. Installed Android is now v84;
production Core remains 8d0c4c6. The owner now explicitly authorizes deploying the
qualified minimal Core fix and returning to the interrupted native incoming
answer/audio/end scenario. Fresh production preflight confirms the exact old
binary, ten saved lines, five connected Agent generations and no active call/data
ownership. Preserve the independent Provider trial and all user switches.

Delivered source: the minimal Core incoming-source binding correction below, plus
Android activity resume, screen-on/unlock, idle exit, USB/permission, reader-link
reconnect and call-completion scans. Existing readerIO owns all hardware work;
events coalesce, reset episodes retain the shared 30-second cooldown and two
attempt limit. A new event can reopen an exhausted episode; unsupported device
shapes/native reset stay manual. Pause/share-off/destroy reject queued ownership;
local call/SMS defer open/reset. No automatic USB permission bypass or PIN/AKA,
paid call or SMS replay. No new timer or permanent wake lock.

Failure checklist: duplicate wake events, exhausted reset budget, event during
pending retry, event during live call/SMS, stale queued work after Pause/exit,
permission loss and unsafe composite reset. One-time pure-policy counterexample
temporarily disabled only event intake while preserving its compiled API: all
three new behavioral tests failed; restored code passed seven policy tests.
Private receipt: android-usb-events-20260927/state-regression.json. Core's four
real-WebSocket counterexamples also failed before and passed after its correction.
Those pre/post checks are separate from the subsequent full CI and field results.

APK SHA-256 0cd5c6de726af7c44b7e33f140834c7cde6a7504d90c5e8db2ec161f577db952,
stable signer unchanged, all four ABIs, source/tree and archive digests checked.
19 JVM, 13 native fixtures on each API 28/35, 219 scoped Core/agentlink race cases
pass without failures/skips. Installed once at 02:56:31 UTC, retained all data.
Actual background wake kept the notification and same App PID, reopening two
reset attempts after the exhausted budget; both failed. Unlock/foreground return
then identified the original card without a replug/restart. Core independently
confirmed current card route, IMS and messaging readiness. Pause stopped service
and notification; a new sleep/wake added no USB log, reopening remained paused.
Explicit resume restored the original availability/sharing and automatically
recovered the card. Final native Readers and Core at 03:07:05 UTC agreed, no calls
active. No new paid call/SMS or PIN action. Underlying transport -110 and -12 remain
unresolved; no hardware/ENOMEM root-cause claim or deep-idle/battery acceptance.

Evidence: private android-usb-events-20260927/{workflow-results,apk-integrity,
ci-summary,install,background-wake,foreground-recovery,pause-wake}.json, bounded
USB/service logs and v84 native XML/PNG in provider-evidence-36225153970. Core
readbacks are receive-report-524bd33/{after-v84-foreground,after-v84-intent-restored}.json.
All observation processes exited; no sampler or network change remains.

Unique next step: deploy the authorized Core-only artifact with an offline backup
and rollback, then coordinate one guarded incoming answer/audio check.
Do not repeat the delivered APK CI/install, paid SMS, or simple USB wake checks.
Keep the large local cursor and post-CI evidence delta out of a micro-commit.

### September 27 actual incoming failure and minimal Core correction

The owner's test reached the mainland modem twice at 02:15:30-02:16:16 and
02:16:40-02:17:25 UTC. Both durable call records ended missed. Android displayed
no Answer control. The single mobile-event observer completed its bounded window
without an actionable incoming event; it never clicked Answer or armed the
device-side force-stop fallback. No outgoing call or SMS was sent by this task.
The observer, transient SSH tunnel and marker are cleaned up. The initial tunnel
startup failure is retained separately, not disguised as a successful test.

Exact source diagnosis: WithWebUI incorrectly installed CellularCallFacts from
the static UI handler; WithCellularMedia only mounted routes. The production
mainline baseline and this branch share the defect. Thus call history receives
Agent events while both mobile and browser snapshots omit their event source.
The minimal fix moves that three-line binding into WithCellularMedia. No API,
authentication, call ownership, SIM fences or data/user switches are changed.
This is a justified existing-interface wiring repair, not PR #8 scope expansion.

One-time pre/post regression: the new real-WebSocket test failed all four cases
(mobile/browser, with/without WebUI) on the unfixed source, then passed in 2.334s
after the binding move. It also verifies removal when the event ends. Full CI
and production deployment are not yet complete; physical answer/audio remains
failed/unaccepted. The phone remains installed v83; no APK upgrade is required
by this Core-only correction. The USB recurrence remains a separate open issue.

Private receipts: android-evidence-36281446154/owner-incoming-once-tunnel and
owner-incoming-missing-answer.json; native XML/PNG under the existing
provider-evidence-36225153970/owner-incoming-no-answer-20260927 files. Caller,
destination, card identity, hosts and raw output remain private.

Unique next step: freeze this minimal fix and its regression/evidence updates,
run the full GitHub workflow once, then obtain the production rollout decision
before another owner-assisted incoming attempt. Do not ask the owner to redial
the still-unfixed production service or restart Agents as a substitute.

### September 27 owner cooperation readiness, 02:03-02:05 UTC

The owner has replied and asked to verify the cards before coordinating a test.
This supersedes the prior no-reply/blocked prerequisite below. Read-only Core
checks selected the exact mainland line and full card identity from the retained
receipt, not the previously incorrect number suffix. The current modem reports
SIM ready, PIN not required, AT ready, UNICOM home registration and signal 16%.
Cellular call/SMS admission is ready; there are no media sessions or active calls.
The data connection remains disconnected and protected; no user switch changed.
This establishes prerequisites, not successful incoming answer or two-way audio.

The Android app is still PID 22420 and Gateway online. Current user 0 has microphone
and notification permission. Native Calls was opened and the exact mainland card
and Cellular route selected; the actual screenshot shows route ready. No dial or
answer action occurred. Private receipts are under android-evidence-36281446154:
owner-cooperation-preflight-20260927.json, owner-cooperation-modem-topology.json
and owner-cooperation-device-state.json. Native XML/PNG are under the existing
provider-evidence-36225153970 with owner-cooperation labels.

The USB giffgaff card is NOT currently ready: native Readers reports write -110
and failed slot_status recovery -5, automatic reset stopped, identity unconfirmed.
Core independently reports card_not_present with IMS/tunnel stopped. Do not call
this a healthy card or ask the owner to dial it. No restart, USB reset/replug,
paid call/SMS, deployment, or source change was performed. This recurrence is new
field evidence, not proof of hardware, sleep or software root cause. Its Core
receipt is receive-report-524bd33/owner-cooperation-card-preflight-20260927.json.

Unique next step: coordinate one mainland incoming call from the owner's other
phone, which does not depend on the failed USB reader. Keep MDD Calls foreground,
arm the ownership-safe bounded hangup protection before giving the start signal,
collect native answer/audio and final idle evidence. No guard is armed and no
test is currently running. Do not repeat paid tests or re-open deferred UK
incoming/network work; separately retain the USB recurrence for diagnosis.

### Previous September 27 native acceptance prerequisite audit

The previous goal turn made concrete progress: v83 diagnostics were qualified,
installed and field exercised. This continuation is a read-only prerequisite
audit, not another completed product gate. The fresh Core sample at 00:42:53 UTC
found the exact mainland modem/card present, Agent connected, cellular voice/SMS
ready, no media sessions and no active calls. VoWiFi intent is uninitialized for
this mainland SIM; that is not a reason to reject its separate cellular route.
Private receipt: android-evidence-36281446154/mainland-incoming-readiness.json.
An initial number-suffix selector matched no catalog row and failed before any
action; the fresh catalog and exact line/card selection corrected that query.
No call, SMS, USB action, deployment, production switch or source edit occurred.

Source review found the current cellular incoming occurrence/card fences and
VoWiFi call-ID paths intact. This is not native incoming answer/audio evidence.
The existing incoming-cooperation request remains pending; do not repeat it or
invent a synthetic event and label it physical acceptance. UK incoming and shared
network root-cause work stay owner-deferred. PR #14's isolated warning acceptance
is complete; production rollout and draft/review merge decisions still belong to
the owner. No new paid destination is inferred from the standing outbound grants.

The proposed old-PR F6 followup was withdrawn before editing: the newer September
26 scope explicitly prohibits reopening it. Preserve its unique old-PR content,
not a new implementation. All five current dirty files remain only the existing
post-CI evidence documentation plus this local-only cursor; no micro-commit.

Unique next step: resume native incoming answer/audio at the already-requested
owner cooperation, or act on an actual owner review/deployment decision or new
causal USB evidence. Do not repeat readiness queries, short sleep observations,
CI/install, paid calls or F6 work to simulate progress. There is no live job or
sampler to wait on. The same owner-cooperation/review gate has now persisted for
three consecutive goal turns after the preceding real progress. The second turn
made no product progress; this third turn performed one sleep-first, bounded PR
read, not another device or CI poll. PRs #12/#13/#14 remain OPEN at 4e2f4d8,
9e0ec67 and 38f1191, respectively, with no submitted reviews; #12/#13 remain draft.
Private receipt: android-evidence-36281446154/closure-review-gates.json.
No new owner cooperation reply or authorized autonomous acceptance step exists.
Mark the goal blocked, not complete, to stop automatic status-only continuations.
Resume at an actual changed prerequisite; do not repeat the existing requests or
send another Telegram reminder for the same gate. All acceptance gaps remain.

### September 27 bounded Android USB errno diagnostics

Continue from the field recurrence below, not another replug/reset loop. AOSP
UsbDeviceConnection JNI returns the USBFS ioctl result and loses errno. Correctly
filtered native logs show connection cleanup only after failure; they do not
establish whether hardware, firmware, Android or client lifetime caused it.
The existing OpenEUICC implementation and the preserved bounded-read worktree
were inspected; neither supplies evidence to justify a transport/retry rewrite.

This client-only batch keeps one USBDEVFS_BULK ioctl per existing
transfer on the owned descriptor, preserving endpoint/buffer/deadline semantics
and returning negative errno. It records the first scan failure's class, phase,
CCID command number, result and duration once per unhealthy episode. No card
identifier, APDU contents, credential, paid retry, PIN/AKA replay, wake lock or
server change is added. Native-load failure fails closed. Home/Readers now
distinguish response-read and native-library failure from write failure.

Failure checklist: negative and partial transfer outcomes, JNI/buffer bounds,
native-load failure, no fallback transfer, existing serialized fd ownership,
bounded diagnostic volume and no payload logging. Existing reset budget and
scan/recovery behavior are unchanged. No new automated test or red/green claim.

Delivered commit 4e2f4d8993fd4a0bb1f4739b11f36d84ed113c24, tree
55a11a4f27cb6a73f4ba317058d6ec05bb3a891d. Full signed workflow 36281446154 passed;
16 JVM tests, 13 native fixtures on each API 28/35, 214 Core/agentlink race cases,
no failures/skips in those reports. Four artifact digests, source/tree, all four
native ABIs, APK signature and non-debuggable package were checked. Version 83
APK 2fdaa32a5e9fb2ea2794f51aefaea551db9575da1d545540ddd6329db2a7544b was
installed once at 00:19 UTC after fresh all-call idle evidence, retaining data
and saved availability/sharing. No extra App restart or reset command was issued.

Real new-path evidence: at 00:19:16 UTC the first scan failed on write command 98
(CCID power-on), result -110 (ETIMEDOUT), elapsed 2016 ms. At 00:19:28 the existing
automatic reset identified the original card. Native Home/Readers were inspected.
One bounded, sleep-first read-only observation ran 00:21:45-00:31:16 UTC (572 s,
five backoff samples), with the same App PID and no new first-scan failure.
Final Readers retained the same card and sharing; Core at 00:31:39 confirmed
current route/IMS/message readiness and zero active calls. The earlier transient
swu_authentication_failed recovered without an extra intervention. History
remained ten events. This is a short successful sample, not established USB root
cause, deep-idle/battery stability, new voice or complete SMS acceptance.

Evidence: private android-evidence-36281446154/{artifact-integrity,apk-integrity,
ci-summary,install,first-recurrence}.json, usb-installed.log, observe-*.log;
provider-evidence-36225153970/v83-{home-installed,readers-installed,
readers-after-observation}.{xml,png}; receive-report-524bd33/
{before-v83-install,after-v83-installed,after-v83-observation}.json. The single
observation process has exited; no persistent sampler was installed.

Unique next step: return to the cumulative native-client acceptance/PR closure
register below. USB first-error diagnostics are delivered and field exercised;
do not repeat this CI, install, reset/replug request, paid action or the qualified
Provider rollout. Further USB changes need new causal evidence, not another
identical waiting cycle. Preserve long-idle/battery and native incoming/audio
gaps; do not silently reopen owner-deferred incoming/network diagnosis. PR #12
stays draft for owner review. Post-CI README/ledger results remain a docs-only
local delta for the next coherent batch, not a micro-commit. The public PR body
records the current artifact/field result; older cursor sections are history.

### September 27 owner replug and receive-report field verification

The owner confirmed replug; the previous waiting gate is resolved. Android remains
74e97c5 / signed v82, workflow 36263260716. The original App PID was still running.
Opening the existing activity showed a new attachment requiring USB permission.
Native Allow/OK recovered card identity ending 1522 with sharing/availability
unchanged. Core confirmed the exact card route and message/IMS readiness. This
was not an App restart, reset command or additional paid operation.

Provider PR #13 candidate 9e0ec6772f9e7c34c365e2dcce8694446c087a17 is NOW deployed
to the same single-line trial using its retained qualified artifact; no new CI.
Tree 905c7c429387555fd7fe9d1f150bd822c2074e6a, full workflow 36264788240.
Archive d2f2697272c617b47dc008a167fe167e64fa47733a2003b426417086a7e63cb0;
running Provider 8d71b89cb09ac98e78315e9197a178a8c3d5355ec27307bfed0d095b63c54bd1.
The owned maintenance lease was released. Old binary/overrides/state backup are
retained; desired configuration, shared Provider and unrelated PIDs are unchanged.
Core remains 8d0c4c6. Neither PR #12 nor #13 is merged; PR #14 stays review-ready.

Old 524bd33 received two new report 403 responses after card recovery. At
2026-09-26 23:37:08 UTC the candidate's own PID recorded a real report 202 for the
already-pending carrier SMS. Core history stayed at ten events, with no new event
across this delivery/process generation. This is scoped live receive-report and
durable-dedup acceptance. No new outbound SMS, call, PIN or manual replay occurred.
Native Home, Readers, Messages and history were inspected; prior received/sent
body, sender, own number/card and Reply remain visible. Old duplicates are retained.

Contrary evidence: the reader failed again about eight minutes after permission
recovery. At 23:40:28 UTC automatic reset identified the card; at 23:40:57 the
second reset failed at slot_status. The same App PID remained; the phone was
Awake, not deep/light device-idle, and not externally powered in this sample.
Only the installed preview process appeared in the scoped MDD/CCID process check.
Native Readers showed write -1 and exhausted recovery; Core at 23:43:35 reported
card_not_present/stopped and no active calls. USB durability is still FAILED;
cause remains unproven. Cancel the planned quiet observation: silence while the
reader is unavailable is not evidence that online carrier retries have ceased.
No extra App restart/reset or repeated replug request was issued.

Evidence: private report-context-9e0ec67/{remote-receipts,field-receive-initial.json},
receive-report-524bd33/{owner-replug-permission-granted,after-report-context-9e0ec67,
post-replug-usb-recurrence}.json, android-evidence-36263260716/owner-replug-*, and
provider-evidence-36225153970/v82-*-after-report-context plus owner-replug captures.
One failed logcat timestamp quoting attempt is retained separately; the corrected
read supplied the two actual recovery entries. It was not a product failure.

Unique next step: diagnose the first USB transfer/lifecycle failure using this new
recurrence, not another identical reset or replug cycle. Any added diagnostics
must stay client-only and bounded, with no PIN/AKA replay or sensitive payloads.
The Provider receipt fix is qualified/deployed/field exercised; do not redo it.
Preserve incoming/audio/OEM/battery and owner-review gaps. Public acceptance and
generated summaries have a local docs-only field update pending the next batch;
do not create a micro-commit or claim the full objective complete. Earlier entries
below are historical. This resumed turn made concrete field/deployment progress.

### September 27 successful-but-unstable USB recovery correction

The v80 recurrence below exposed a second software dead end: a successful reset
clears resetResult, so fresh write failures before the 60-second healthy rearm
could never use the remaining reset allowance. ReaderHub's serialized sequence
was reviewed against the retained field logs; the physical cause of USB failure
is still unknown. The client-only correction permits that one delayed followup,
with two fresh consecutive write failures and the original 30-second cooldown.
At most two resets remain allowed in one unstable episode. Permission, shape,
claim, reset, power-on and identity failures do not acquire a new retry. PIN/AKA,
SMS, calls, stored intent and Core are untouched. Home/Readers distinguish a
short-lived successful recovery from a failed reset and exhausted attempts.

The new behavior regression compiled and failed on unchanged aeb0004 with
"A short-lived successful reset must not permanently disable recovery". All four
focused UsbRecovery policy tests then passed. The old unsafe-stage test's empty
success case was an incorrect permanent-latch expectation and is replaced by
this explicit regression; actual unsafe-stage exclusions remain. No USB hardware
acceptance is inferred from the policy test. Private red/green receipts are under
codex-audit-tmp/mdd-usb-recurrence-20260927. The signed build/CI, installation and
actual automatic recurrence acceptance are pending. Local cursor stays untracked
by this delivery; do not stage this document's accumulated execution history.

Unique next step: freeze this complete client recovery/UI/regression/docs batch,
run one signed Android workflow, then qualify/install the exact artifact and
inspect native Home/Readers on the already authorized handset. Do not repeat the
Provider rollout, old sleep sample, paid SMS or DTMF call. PR #12 remains draft.

Qualification update: 74e97c5ed2fdedc3e69663880d11bdba33bf27ad, tree
110efc70127129d2dec03ec263c5e96494a0e0cf, passed signed workflow 36263260716.
All four artifact archive digests, source/tree, APK SHA-256 and stable signer were
verified, including local apksigner/package inspection, not a local build.
APK 64726df2a4e6f2b029eede84afdad9417dd154986de09a4665b923d2ecd9306a is
non-debuggable versionCode 82. CI: 16 JVM tests, 13 native fixtures on each API
28/35, and 214 Core/agentlink race tests, with no failures or skips in those reports.
One in-place update at 18:52 UTC preserved app data/availability/sharing after a
fresh Core idle check. Before install, v80 again showed real write -1 and an
absent card. No extra manual restart or replug was used after installation.

Real v82 evidence: first reset failed at slot_status 18:52:54, delayed second
reset reached identified at 18:53:25 in the same PID, then fresh writes failed
again before healthy rearm. Native Readers at 18:54:54 showed the new unstable /
attempts-exhausted detail and red USB state. Core at 18:55:27 again showed
card_not_present, with no active calls. Bounded followup/exhaustion is exercised;
durable usable recovery FAILED. Do not restart the app repeatedly or claim that
passing policy tests fixed the underlying USB failure. This was not a new sleep
experiment. Physical cause remains unknown. Evidence is android-evidence-36263260716
and provider-evidence-36225153970/v82-{home-installed,readers-followup}.

New SMS field evidence also supersedes the earlier quiet sample: six automatic
RP report attempts between 18:25 and 18:48 UTC returned SIP 403. Core message
history grew from nine to ten, with exactly one new stable-identity received
event across those retries. This supports deduplication (and the documented
one-time old/new identity overlap), not successful delivery acknowledgement.
No additional outbound SMS was sent. Evidence: receive-report-524bd33/
before-v82-install.json and after-receive-observation.json. The current next step
is narrowly diagnosing the rejected RP report from existing protocol/source
evidence while retaining the failed USB acceptance and awaiting physical recovery;
do not rebuild/deploy or replay paid actions just to collect another sample.

The subsequent bounded investigation confirmed a concrete Provider adaptation
omission: buildSMSReport did not pass Profile.AccessNetworkInfo/VisitedNetworkID,
unlike the already adapted upstream outgoing SMS path. The shared builder uses
bare IEEE-802.11 unless those DialogRequestConfig fields are supplied; the live
configuration has a non-default access value. The registered-flow packet test and
adapter configured-context case both compiled and failed on unchanged 524bd33,
then passed under race with the two existing fields propagated. Empty-context,
gateway, durable/write-failure and rejected-report controls remain unchanged.
This is not proof of the carrier's exact 403 cause. The separate PR #13 followup
will receive one full GitHub qualification; no Core/Android/config change or SMS
resend is included. Private network-context-{red,green}.{log,json} and
report-network-context.txt preserve evidence. A single deduplicated user request
asks for reader/OTG replug after the v82 exhausted recovery; do not repeat it.

### September 27 one SMS received, duplicate ingress contained

The owner granted the fresh post-SMSC-fix permission. Exactly one native Android
submission was confirmed at 17:14:53.936 UTC on September 26, message ID
`5c161bce-32a2-4453-bec4-16bf2e82bd82`; this one-shot permission is now consumed.
Core retained one submitted event and the matching self-received body. Native
history showed sender, own SIM/number, body and transport; Reply revalidated and
prefilled the original card/recipient with an empty body, without sending again.
No separate positive delivery report arrived; actual self-receipt is the evidence.
There were subsequently several incoming transactions for the same content and
SMS timestamp, with different carrier Call-IDs. The owner confirmed three, then
four identical Telegram notifications. Do not call the SMS chain fully accepted.
The Provider operation ledger contains one completed send, not multiple sends.

The first preflight had failed on a real v80 USB recurrence. A logged successful
reset was followed by unavailable/read-write errors; native Refresh did not fix
it. One explicit idle-safe App restart recovered the exact card and SMS route.
That is manual recovery, not acceptance of automatic sleep recovery. A later
native screen again briefly showed card_not_present. Preserve this contrary
evidence instead of repeating the previously passed eight-minute sleep sample.

The owner explicitly allowed temporarily pausing this affected VoWiFi line to
stop repeated ingress. Its existing maintenance lease was acquired and its exact
Provider unit stopped. PID zero/inactive, unchanged desired line/config and
unchanged unrelated service PIDs were verified. Telegram and other lines were
not altered. The lease intentionally remains held until the correction is ready.
Private pause/resume identity: provider-smsc-20260926/pause-duplicates-result.json
and remote /root/mdd-sms-duplicate-pause-20260927/resume-identity.json. Do not
resume blindly or create another lease. SMS evidence is the same private root's
self-sms-after-fix; UI captures are provider-evidence-36225153970/sms-postfix-*.

Provider PR #13 now contains the complete receive-correction batch `524bd33`,
tree `a9251f2cb9ecbf8a75ce611d42cd44dc575b7abe`, pushed without rewriting history.
The real userspace SIP regression and stable-notification counterexamples failed
on d69bdbb, then passed in the focused race run. RP-ACK is now a separate MESSAGE
to the asserted IP-SM-GW after durable ingress/SIP response, with In-Reply-To;
existing Core event identity deduplicates new-format carrier retransmissions.
Core/Android/user settings are unchanged. This is a demonstrated implementation
mismatch and strong explanation, not a captured carrier-side root-cause verdict.
New-format first replay can appear once beside old-format history; no history is
deleted. Incomplete upstream multipart assembly is not claimed crash-durable.
Private red/green receipts are provider-smsc-20260926/sms-report-{red,green}.txt.

Full workflow 36260170582 passed for this head. GitHub merge artifact
36bd191f6d4610073bea95bc484e65c4e6ad39bc has the exact candidate tree above.
New wire/identity tests, full Core/Provider/upstream race and existing UI gates
passed; opt-in systemd/Xray/subprocess skips remain distinct from separate jobs.
Private archive SHA-256: dbba55e8c378da097c3a64dcd7d6b7653d975122eca427c3486166edc6124c6c.
Provider SHA-256: 866bd8fb1e4b2a071b7a100ce7a49fe7c73be61d1a3cf4621d60cc90e89359ec.

Deployment attempt 1 stopped before mutation because the stopped Provider's
status API returned 412 provider_unavailable. Attempt 2 started the correct binary
and released the exact lease, but the runtime stayed stopped because the Android
card route had been absent since 17:27:33 UTC. It restored the prior paused state
and held the original lease. Both failures are retained, not qualified as success.
The handset was awake/powered with the same v80 PID and enumerated USB device;
native Readers showed write -1 and idle-safe replug advice. No sleep/hardware
root cause is inferred. A fresh no-active-call check preceded ONE manual restart
of the MDD App. At 18:17:05, the exact card route was current again.

The same unchanged CI artifact was then reused, not rebuilt. Candidate startup,
persisted original lease and running executable hash were verified before resume.
The original lease is NOW RELEASED and the affected Provider is restored at
524bd33. Desired line/config, shared binary and unrelated PIDs are unchanged.
Do not restore the old duplicate-producing Provider or reacquire the pause lease.
At 18:22:58 UTC, after one 180-second quiet observation, the exact card, tunnel,
IMS and SMS/call admission were ready, with no active call. Message history stayed
at nine records and no RP report was triggered: the carrier did not redeliver.
Therefore the report's real carrier acceptance and cessation of future retries
remain unverified; this is not permission to send another SMS.
Evidence: provider-smsc-20260926/receive-report-524bd33, especially
remote-receipts-card-restored/result.json, observation-summary.json and the two
failed rollout receipt directories. No sampler, firewall or temporary listener
was installed. Private release/rollback artifacts and the narrow trial override
are intentionally retained.

Unique next step: address the demonstrated v80 USB recovery recurrence in the
client-only PR #12, without repeating the passed Provider CI/deployment or paid
SMS/DTMF calls. One reset can succeed but a renewed write failure before the
60-second healthy rearm leaves no followup; evaluate the actual caller sequence
with the retained field evidence before changing the bounded policy. Repeated
manual App restarts are not a product fix. Native incoming cooperation remains
pending; PR #14 is already review-ready. Provider PR #13 remains draft/unmerged.

### September 27 Windows native warning acceptance

The owner accepted the existing AnyDesk connection. The desktop-access blocker
is resolved; do not ask for that authorization again. An actual logged-in Windows
desktop was available and the production Agent remained at its original process,
binary and configuration. No production service or network policy was changed.

The existing `38f1191` / workflow `36088021096` Windows package was reused, with
all 128 manifest entries verified again. A private temporary identity and
loopback-only forward isolated the test from production credentials. The test
ran with zero readers and modem disabled. Computer Use launched the packaged GUI
on the authorized desktop and inspected its actual native rendering, not only
local API output. At 16:49:28 UTC on September 26, the window showed green
connected with the explicit not-call-readiness caveat. Closing only the owned
forward produced a yellow disconnected/automatic-retry message and retry time,
observed at 16:50:26. Restoring that forward returned the same window to green
connected at 16:52:25. Runtime and GUI PIDs stayed unchanged through all phases.
The real local-control observations agree with all three displayed states.

Two preceding interactive attempts timed out at the diagnostic driver's 120-second
GUI checkpoint before confirmation. Both failures and complete cleanup are
retained. Only that driver checkpoint was corrected to use the existing overall
eight-minute bound; no product check, build, CI gate or source was weakened.
The completed test finished within the bound. Native screenshots were viewed in
this task through AnyDesk; do not claim that local PNG files were saved.

Temporary credential revocation, scheduled-task removal, test runtime/GUI exit,
loopback-listener closure and directory removal completed. An independent SSH
read at 16:53:43 confirmed those resources gone and the original production PID
and config hash unchanged. A separate final preflight confirmed the original
binary and no remaining GUI. Private evidence: agent-warning-36088021096/
windows-gui-Pm7Vkn, gui-authorized-20260927.json and gui-after-20260927.json.
The two driver failures are windows-gui-pFGknQ and windows-gui-yQktvP.

This completes R04's isolated Windows connection-warning GUI acceptance, together
with the previously retained Mac GUI and Windows runtime/API checks. It is not a
production rollout or service-button acceptance: install/start/stop/uninstall
buttons were deliberately not invoked against the production service. PR #14
at `38f1191` was subsequently marked ready for owner review, without merging or
deploying it; Android and Provider PRs remain draft. Android, Provider and Core
deployments did not change.
The previous blocked audit is superseded by this concrete progress.

A single post-desktop read at 16:58:10 UTC found Android v80 still at its installed
PID, powered/Dozing but not device/light idle, with no MDDUSB recovery event in
the retained log. Core at 16:58:28 reported the exact card and both test lines
ready/idle. This was a read of the natural intervening period, not a new sleep
experiment or proof of continuous health, second-reset recovery or battery life.
Evidence is android-evidence-36249215118/after-windows-natural.json plus its raw
log/power files and the existing readback root's v80-after-windows-native-gui.

Unique next step returns to the pending native incoming-answer cooperation and
one newly requested post-SMSC-fix self-SMS authorization. The earlier one-shot
permission is consumed; the new async request and one deduplicated notification
ask for at most one paid SMS with no automatic resend. No reply or SMS yet.
Do not repeat either request, Windows GUI transitions, the successful RTP-DTMF
call or unchanged reader observations, and do not restart old PR F6.

### September 26 Windows GUI access prerequisite

The preceding goal turn made progress: a real answered call exercised RTP DTMF
and ended safely. This continuation did not complete another product gate. It
rechecked only the missing Windows native GUI access, without repeating runtime
tests or creating another build/deployment. Computer Use could enumerate apps;
one RustDesk selection again failed with tool error -10005. The existing AnyDesk
destination reached its actual secure authorization dialog, requiring its separate
password or confirmation on the remote machine. SSH access is not that password;
no credential was guessed, copied from another account, saved or bypassed.

One new async request asks the owner to accept that desktop connection and keep
the authorized Windows account logged in/unlocked. One deduplicated Telegram
notification was accepted. Do not repeat either request. The older incoming-call
cooperation question also remains pending. No production service, network,
permission, device policy or paid operation changed. Native GUI acceptance is
still unverified; an authorization dialog is not a successful desktop connection.

Unique next step: continue the existing isolated Windows GUI acceptance when the
owner accepts, or the native incoming-call flow when its pending reply arrives.
Do not fabricate activity with repeated authentication checks, paid retries,
old-PR expansion or another unchanged sleep sample. Three consecutive goal turns
after the RTP-call progress now lack an executable remaining acceptance step.
The second turn's one bounded observation confirmed the same live authorization
dialog; it was a verified prerequisite check, not product progress. No owner
reply has arrived for desktop acceptance or incoming-call cooperation. The third
turn does not poll the same dialog again or resend either request.

The blocked threshold is met: remaining Windows GUI and native incoming acceptance
need owner interaction, another SMS needs fresh specific authorization, and the
additional USB hardware-recovery path needs a real eligible recurrence. Draft PRs
still require owner review. None is an overall-completion claim. Mark the goal
blocked to stop automatic no-progress continuations; resume at the affected gate
when its actual prerequisite changes. Preserve all existing work and evidence.

### September 26 bounded post-v80 RTP DTMF acceptance

The owner asked whether work had entered a loop. Acknowledge the previous scope
drift and repeated observations: do not resume old PR F6, unchanged sleep samples,
another build/install, or already-passed isolated Agent transitions. This turn
completed one remaining paid check with a new diagnostic basis, not a blind retry:
the previous attempt stopped before dialing because v77 lost its reader route;
the newly installed v80 had recovered the exact card and fresh preflight was ready.

At 15:15:12 UTC the existing bounded synthetic-media client started the authorized
UK call. It answered at 15:15:15, accepted one digit through `dtmf_rtp`, and ended
at 15:15:43 after explicit hangup. The independent 40-second exact-call guard
exited successfully, observed idle and did not need to force termination. There
were 1,137 post-start downlink frames, including 826 signal-bearing frames. This
is a real carrier call and successful RTP event-send result, with synthetic
uplink/file downlink; not native microphone/speaker, incoming answer, or proof
that the carrier IVR acted on the digit. No redial or SMS occurred.

The terminal readback at 15:17:37 UTC retained the exact card and fresh call/SMS
readiness; both authorized lines had no active/pending call or media sessions.
No software, desired intent, App installation, Core or service was changed.
Raw one-shot attempt, tone result, audio, guard and final receipts are private
under `provider-smsc-20260926/dtmf-after-v80-one-call/`; the before/after Core
readbacks use the existing owner-incoming-and-sms evidence directory.

Unique next step: respond to the already-pending incoming-cooperation reply or
another genuinely available remaining prerequisite. Windows native GUI still
needs an interactive desktop; one-shot SMS authority remains consumed. The USB
second-reset hardware path needs an actual eligible recurrence, not another
unchanged sleep sample. Do not repeat this successful call merely to improve
coverage. Keep all PRs draft and overall acceptance incomplete.

### September 26 single-line rollout and real v77 recovery failure

The preceding status-only turn was no progress. This turn resumed the already
authorized affected-line Provider trial, not the old PR F6 storage-recovery branch.
At 14:16:55 UTC, `d69bdbb` was deployed under the existing maintenance/rollback
guard. Artifact source `e86f932` has the exact candidate tree; archive SHA-256 is
`46155ae711f06d70aba363776e7b6167ec2fa8883b8cc3f531f915813b694bea`, running Provider
SHA-256 `b1c423bab25a2521ef72307e44369b8fc0ce0723016246f096b77a657e6bf06d`.
The old trial/state backup remains; config, desired line, shared link and other
Core/Agent/egress/Provider PIDs were unchanged. No Core change, PR merge or SMS.

One authorized post-fix DTMF trial used the existing synthetic-media client and
independent exact-call hangup guard. At 14:21:24 it returned HTTP 412,
runtime_not_running, before an answered call or a tone. Final state was idle.
Core diagnostics show card/hardware/route loss at 14:21:23, preceding the Provider
stop, not evidence of a Provider SMSC regression. At 14:21:39 signed v77 PID 32467
logged `result=-5 stage=slot_status`; attachment/permission survived. This is the
previously missing real v77 failure, not an unchanged observation. Wake-only
readback was Awake but still failed. No redial or tone was attempted again.

The one followup QA comparison failed before hardware reset because jdb could
not evaluate a compound null expression. It was stopped, its forward removed
and QA force-stopped; the original preview resumed. At 14:29:38 that same v77
code, with a new PID/budget, automatically reset and identified the card. Do not
claim the failed debugger comparison passed or count App restart as a fix.
The new client-only correction permits one delayed additional reset after a
failed slot-status handshake and continuing fresh write failures, at least 30
seconds between reset starts, with an absolute two-reset episode cap. Other
failure stages, unknown APDUs and paid requests do not gain retries; rearm still
requires 60 seconds of healthy scans. Home/Readers distinguish pending from
exhausted recovery; logs retain the underlying exception class without raw APDUs.

The focused real-policy counterexample compiled and failed before changing the
old one-reset behavior; only its timestamp parameter was added for compilation.
All three policy tests passed after the correction. The unused hardware constructor
is a trap, not a replacement for the tested policy. Raw evidence is in the external
`codex-audit-tmp/mdd-usb-retry-20260926` directory. No local Android build/full suite.
The coherent nine-file recovery/UI/test/docs batch is committed/pushed as
`aeb0004` in existing draft PR #12; this local-only cursor is excluded.
Exactly one signed workflow `36249215118` passed for full head
`aeb00049df128d41e6f6fa63aa1740e10d132f7e`, tree
`4bbe7150b56bd5ba07272b6657712995130cc050`, after one quiet wrapper wait.
Reports confirm 15 JVM tests, 13 native tests on each API 28/35 and 214 scoped
Core/agentlink race tests, with zero reported failures/skips. Independently
verified stable-signed non-debuggable v80 was installed once in place. APK hash:
`da906e67f52b86e5ee36521baeac42dcc4900a2728d995fc669cdddd4451fbff`.
Before installation the original v77 card was unavailable again. v80 PID 32474
recovered by its first reset at 14:52:54 UTC and fresh exact-card Core call/SMS
readiness was verified at 14:54:17. Actual Home and Readers preserved the account,
availability, sharing and expected card, with need-action zero.

From 14:56:08 to 15:04:08 UTC, one 480-second screen-off sample made no intermediate
handset queries. It retained the same PID and exact card; both authorized lines
were call/SMS ready and idle. The powered phone was Dozing but not device/light
idle. No new USB recovery event occurred, so this passes that short screen-off
sample, NOT the added second-attempt hardware path or battery/endurance. Post-wake
native Readers again showed the original card and sharing. New failure states
were not fabricated for screenshots. Raw CI/install/recovery/screen-off receipts
are private under `android-evidence-36249215118`; native UI remains in the existing
`provider-evidence-36225153970` root. PR #12 is updated to this exact candidate
and scoped evidence; all drafts remain unmerged.

Unique next step: resume remaining incoming/DTMF/desktop acceptance only when its
specific prerequisites are available. The one incoming-cooperation question is
still pending; do not ask it again. USB followup qualification requires a real
eligible recurrence, not repeated unchanged sleep samples. Do not resume F6,
rebuild/reinstall v80 or issue another paid call without a new diagnostic reason.

Rollout, failed call, USB/wake and diagnostic cleanup receipts are private under
`provider-smsc-20260926/`. The earlier Windows GUI preflight found only a disconnected
desktop session; no GUI candidate was launched there. Its native GUI gap remains.

### September 26 remaining-work closure and sleep/USB recovery

The owner requires all non-deferred items from the latest audit completed and
verified under the existing authorization. The audit was progress: it identified
an actual call-history presentation omission and stale R12/R13 records, not only
a status restatement. Preserve incoming answer, DTMF, Agent warning, bounded
phone recovery/battery acceptance and PR reconciliation as unfinished work.

Exact-head workflow `36225153970` returned SUCCESS through the global status tool.
At 08:56:20 UTC, the existing maintenance/rollback path deployed `4ca9315` to the
single authorized UK Provider. Archive SHA-256:
`071fb7c388cd897f8768272d4e6db8fc52b00763e2c4f8f36432c592c0120404`;
running Provider SHA-256:
`d2c2f06fb23334925f27427df1b9c2747b1c80beca92be10a6d922b9ca3f2a12`.
The previous overrides, binaries, state and configuration are retained. Other
Provider, Core, Agent, egress and apply PIDs remained unchanged. At 08:57:18 UTC,
the exact card and SMS readiness were present, with both test lines idle.

The handset was system-locked. The owner supplied device-unlock credentials and
authorized coordinate entry; unlock succeeded. No credential is retained here.
The native Home then showed real USB write -1, the Readers badge required action,
and Messages subsequently blocked on card_not_present. The permission dump still
grants this reader to the installed preview UID; ordinary reopening is already
being attempted by the current reader owner. No new SMS was dispatched. The
owner now reports this after each sleep and explicitly requests software recovery,
including USB reauthorization if appropriate. Do not misclassify it as proven
hardware damage or silently defer it. Scope recovery to the selected reader;
preserve unknown operations and never replay PIN, AKA, calls or messages.

The bounded transport comparisons are now complete. Ordinary open/claim still
returned write -1 with permission retained. Endpoint GET_STATUS succeeded once
with no halt bit, then failed on a later comparison. setInterface and
release/setConfiguration/reclaim did not recover bulk I/O. A single authorized
device port reset at 09:28 UTC succeeded, after which GetSlotStatus wrote/read
10 bytes with the matching CCID sequence. No PIN/AKA or paid command was used in
the comparison. At 09:28:31 UTC the original signed preview had rediscovered the
exact card and Core call/SMS readiness was true; both test lines remained idle.
The existing QA process and its debugger/forward were removed after each run.
Earlier debugger-driver failures/one debugger-induced ANR are not product evidence.

The first integrated candidate `40cc721` passed exact-head GitHub CI
`36233459087` and signed v73 was installed over v72, preserving account and
sharing/availability. APK SHA-256:
`750bf83704b3d077910b108d12e99360d78c1c8ab6c545e43f2e64759e3b0c3a`.
Three minutes screen-off reached Android Dozing, retained the same process and
exact-card Core readiness, and recovered the native Readers view after unlock.
This powered-handset sample is not deep-idle or battery acceptance.

A planned endpoint-halt fixture was NOT injected: the first driver attempt failed
while loading a debug class, and the next already found a real write -1. In the
resulting v73 trial the native reset logged result=0 once but subsequent scans
still failed and Core correctly remained card_not_present. Do not count that as
automatic recovery. The first implementation closed the reset handle and deferred
power-on to a later scan; this differs from the successful same-handle comparison.
The current correction retains one descriptor through reset/reclaim/GetSlotStatus/
power-on and immediate fresh identity. Its reset budget and user intent are unchanged.
Another diagnostic confirmed native reset and slot-status response; an attempted
nested debugger method call hit its own breakpoint and is not a successful card-read
assertion. At 10:06:17 UTC Core was again exact-card call/SMS ready and the next
diagnostic saw a healthy card. All QA processes/forwards were cleaned up; no PIN,
AKA or paid test was dispatched. This first integrated failure is retained as
the pre-fix counterexample; qualification of the correction follows below.

The qualified USB candidate is `02eeb2d14e0e7a8c884c601b249a19a623e920cb`, tree
`aecf15744f930bcac11b61c72f627871771ac6b5`, pushed to existing draft PR #12.
Its signed Android workflow `36234941184` returned SUCCESS for the exact head.
The first global-wrapper wait exited 3 (`unable to read run state`), an external
status-read failure rather than a CI failure. The bounded second wait returned
the terminal success under the owner's maximum-three-check allowance.
The initial push had a GitHub TLS connection failure, and an erroneously dispatched
old-head workflow `36234842065` was cancelled and its terminal cancellation verified
through the global wrapper before the new run. The second bounded push succeeded.
No artifact from the cancelled run was installed or used as evidence.
Recovery uses an NDK USBFS operation on Android's permission-granted
descriptor, not the hidden Java reset method used only in the diagnostic probe.
It is limited to a single-interface CCID device, two consecutive write failures,
one reset per failure episode and rearm after 60 seconds healthy; old interrupt
I/O is cancelled and identity/generation must be freshly observed. No server,
PIN/AKA retry, paid action, wake lock or user switch change is included.

The signed, non-debuggable v75 APK was installed over v73, preserving account,
availability and sharing. APK SHA-256:
`940bcaa2e540e66709fc29fd20655bb3494e949f307960cf0a5367aad2139285`;
verified signer SHA-256:
`8b5e818fccfa6f6738cd53658b2df9ab57a014f9a5269decc4104d91894dd5e2`.
Immediately before installation, v73 had again shown real USB write -1 and Core
card_not_present at 10:19:14 UTC. No synthetic endpoint fault was injected.
After the normal installation/launch, v75 recovered automatically: one native
reset result=0 at stage=identified, fresh card identity in Readers, and exact-card
Core call/SMS readiness at 10:20:38 UTC. The process remained unchanged during
automatic recovery; no subsequent App restart, debugger, replug or phone reboot
was needed. This is actual pre-fix failure/post-fix recovery, not proof from a
successful reset return code alone. Both test lines remained idle.

From 10:22:02 to 10:25:05 UTC, a single 180-second screen-off sample made no ADB
requests during the wait. The powered handset reached mWakefulness=Dozing, but
mDeviceIdleMode and mLightDeviceIdleMode were false. Core still reported the
exact card and call/SMS readiness before waking, with no calls or media sessions.
The App process stayed the same. The one logged reset is the earlier recovery,
not an additional reset triggered by this sleep sample. After unlocking, actual
Home and Readers screens showed the original card, need action 0 and unchanged
sharing/availability. This qualifies short screen-off/wake behavior only, not
long deep-idle, OEM network handover or battery endurance.

The same installed batch's actual Calls and call-history dialog were inspected.
History now shows direction/peer, colored status, own line/number/card and local
time; the existing missed and ended records rendered without overlap. No new
call was placed. Line metadata is current catalog context, not an immutable
historical SIM snapshot. The existing OMAPI access-denied status remains separate
from the healthy USB reader; no OMAPI acceptance is claimed.

Raw rollout/USB/UI evidence is private under `provider-evidence-36225153970/`,
`android-evidence-36233459087/` and `android-evidence-36234941184/`; the latter
contains install, automatic-recovery and screen-off receipts. Android is signed
v75 / `02eeb2d`; Core stays `8d0c4c6`; PRs #12/#13/#14 remain draft/unmerged.
The one fresh UK self-SMS was executed once through v75's native confirmation at
10:31:34 UTC. It received SIP acceptance and a submitted receipt, followed by
delivery/failed at 10:31:35 with SIP 200 and RP cause 38, network out of order.
There is no received self-message. Both lines remain ready and idle. This is not
the earlier SIP 403 location rejection, and the exact underlying cause is not
established. Authorization is consumed and marked so before dispatch; no retry.
Evidence/operation identity is retained under the existing private
`provider-evidence-36220464366/owner-incoming-and-sms/` v75-self-sms files.

The actual Android summary incorrectly remained submitted after that delivery
failure. Selected-line history displayed a separate failed report without its
recipient/body. The screenshot named v75-self-sms-synced is a history dialog,
not evidence that the Sync button was pressed. The new client-only candidate
adapts the existing WebUI history join by line/transport/message/part, updates
the encrypted local receipt from existing snapshots/history, retains the real
failure, and prevents a late submission response from overwriting it. Orphan
reports remain visible; no server, message resend or polling change is included.
Two real journal-state regressions failed on the unmodified 02eeb2d source and
passed after this correction. The one-time check used real MessageJournal/Json
with unused Android/TLS dependency traps, not a full Android or authentication
test. Its evidence is in the external mdd-message-receipts-20260926 directory.
The client receipt batch is committed and pushed as
`c83563ecfa55ddfa3738db564b403bc7d3bb57bf`, tree
`2b0bde00204298bb598bf7a3bfdfc742516548c9`, in existing draft PR #12.
Its single signed Android workflow `36236818373` passed at the exact head through
one global-wrapper wait. Artifact source/tree, SHA-256 and stable signer were
independently verified; v76 was installed in place. APK SHA-256:
`83b0f2ed6e2cacbeba81e0654b0e211ff9aba857fa4db9d85579adccf1423f1d`.
All 12 unit tests and 13 native fixture tests on each API 28/35 passed without
skips, as did the scoped Core/agentlink race run without skips. These are not
carrier or battery acceptance. Actual v76 Messages now marks the existing
operation failure_observed with sender/recipient/line/card/body and RP cause 38.
History joins the two events into one failed message with its original context;
the old message remains separate. Opening Home and returning to Messages retains
that status. No additional SMS was sent. Receipts/screens are private under
android-evidence-36236818373 and the existing provider-evidence-36225153970 UI root.

Before the SMS APK upgrade, a new v75 USB failure was captured. Core lost the card
at about 10:36 UTC; at 11:00 the same App PID and awake, powered handset showed
USB write -1 plus recovery -5. The retained log buffer no longer held that original
reset line. Code maps several recovery exceptions to -5, so this does not yet
prove that the native reset ioctl itself returned EIO. v76 retains the same USB
implementation and remains card-unavailable after upgrade. The earlier successful
recovery and 180-second sample remain scoped facts, not a durability claim.
The first diagnostic driver failed before USB comparison: unavailable QA state,
then an untriggered worker breakpoint, then the debugger's own null-value error.
After two driver changes it was stopped; these are not hardware results. System
USB still enumerated the reader and permissions; shell access to its sysfs power
attributes was denied. Neither is proof that firmware or USB power is healthy.

The next comparison used stock debugger breakpoints in the existing task-owned
QA v56, with the production App stopped only after exact-line idle readback.
QA gateway connections were retired and only local reader scanning enabled in
memory; saved availability/sharing was not changed. A real GetSlotStatus write
returned -1. On the same handle, release succeeded, Android's reset succeeded,
non-force reclaim succeeded, and GetSlotStatus wrote/read 10 bytes with sequence
78. Power-on returned a 22-byte ATR and the original card matched. Debugger,
forward and temporary debug-app setting were removed; QA was force-stopped and
normal signed v76 resumed. At 11:24:21 UTC Core again saw the exact card with
call/SMS readiness and both lines idle. No PIN/AKA request or paid operation was
issued by the comparison. This is transport recovery evidence, not v76 automatic
recovery or a proven root cause. The existing private readbacks are
usb-stage-owned-runtime-preflight and usb-stage-owned-runtime-restored.

The current correction bounds read-only status writes after a successful reset:
at most three attempts with 250/500 ms backoff, same handle, no partial-write or
response-error retry, no power-on/identity/PIN/AKA replay. It also records the
precise failing stage and releases an interface only if this handle claimed it.
Firmware resume timing is a hypothesis based on the controlled recovery, not a
confirmed diagnosis. No new automated test or local full build was run. The
client-only batch is now committed/pushed as
`3d8148e3c6191d52cc41b9a3fc6eeed8268db188`, tree
`f9edc232309d2bf47eb12c4a87de7011936a0842`, in existing draft PR #12.
One unchanged workflow `36238994751` passed for that exact head using one global
wrapper wait. All 12 JVM tests, 13 native tests on each API 28/35, and 214 scoped
Core/agentlink race tests passed with no skips. Those are existing gates; no
new automated red/green test or hardware recovery proof is inferred from them.
Signed non-debuggable v77 was installed once over v76 at 11:42 UTC; source/tree,
SHA-256 and established signer were independently verified. APK SHA-256:
`e64b0a5fa0f66314a20a6d5e9a7371ef3cddd3da2e8828c7327cbc59babac055`.
The original account, availability and reader-sharing intent were retained.

The actual initial and post-wake Readers screens showed one identified USB card,
the original identity, gateway online and need action 0. From 11:43:46 to
11:51:46 UTC the handset was screen-off for 480 seconds without intermediate ADB
requests. PID remained 32467; Android was powered/Dozing, not deep device-idle.
No new USB recovery log was produced. At the terminal readback, Core's Agent,
hardware, card and card-route facts were all fresh/ready, but the VoWiFi runtime
was closing/in recovery backoff. Therefore the original end-to-end sample remains
FAILED in screen-off.json; do not rewrite it as an all-green sleep acceptance.
This was not a reproduced USB failure. A separate bounded post-wake observation
at 11:55:01 found the runtime, IMS and call/SMS readiness restored automatically,
with both lines idle. No service, switch, PIN or paid operation was changed.
The narrow journal showed incoming content-validation 400s, not the cause of the
runtime transition; neither USB nor carrier causality is inferred from them.

Evidence is private under android-evidence-36238994751 (CI, manifests, install,
screen-off, narrow journal and later recovery), plus existing native screenshots
in provider-evidence-36225153970. QA/debugger/owned forward/debug-app settings were
cleaned before installation. The new bounded handshake retry was not triggered
by a real USB failure in this sample, so its physical corrective effect and long
sleep durability remain unqualified. Unique next step: on the next real USB
recurrence use v77's exact stage, without blind resets or relabeling a Provider
failure as a reader failure; otherwise resume the remaining audit items below.
Incoming answer/audio, carrier DTMF, desktop warning
deployment, long-session/battery gaps and PR reconciliation remain open. Do not
repeat earlier paid outgoing checks or treat USB recovery as Android completion.

R04's signed macOS package at `38f1191`, from successful workflow `36088021096`,
has now been physically exercised in its native GUI. Deep/strict signing verification
passed for the existing Developer ID. A temporary hardware-free Agent identity and
an owned SSH forward isolated the experiment from production Agent credentials and
services. Authenticated local status and the actual GUI agreed on connected,
retrying after closing only that forward, automatically reconnected, stopped by
the GUI, and started/connected again. All observations used the same GUI PID.
The retry warning remained distinct from runtime-running and call readiness.
Native screenshots were viewed in this task, not saved as local screenshot files.
Private receipts are under `agent-warning-36088021096/mac-native-U3BCpE/`.
Cleanup revoked the temporary credential, stopped the owned App and tunnel, and
independently confirmed processGone/controlClosed/tunnelClosed. This is signed
macOS isolated status/GUI acceptance, not production rollout, Windows acceptance,
or a fix for the underlying WSS transport loss. Continue with Windows R04 coverage
using the existing artifact and deployment ownership checks; no new CI is needed.

The subsequent Windows read-only preflight confirmed the existing service at
`11fdd722`, the original process and configuration, two ready modems with data
connections inactive, and no physical PC/SC reader. No candidate was started or
installed there. The first preflight output was truncated because PowerShell
serialized Get-Content metadata; the corrected string-only read succeeded. This
was a diagnostic serialization failure, not an Agent failure. RustDesk could not
be inspected after two bounded UI attempts. AnyDesk reached the exact authorized
host but required its separate login; no password was guessed or reused. Its
connection request is no longer pending. Windows native GUI acceptance therefore
remains unverified, not a reason to restart shared networking or production Agents.
Receipts: private `agent-warning-36088021096/windows-current-preflight.json`;
macOS evidence was posted to draft PR #14, comment `5846219246`.

At 12:25:35 UTC, a single Android read found the same v77 PID, no new USB recovery
event, and powered/Awake rather than Dozing. At 12:25:47 the authenticated Core
read reported both authorized lines ready and idle. Do not call this another sleep
test or continuous health between samples. Private receipts are `natural-idle.json`
and its power/USB logs in `android-evidence-36238994751`, plus the existing
readback root's `v77-natural-idle-observation`. No paid operation, PIN, restart,
sharing change, Core change or new build was performed during these checks.
The active USB follow-up remains evidence-driven: retain the next real v77 failure
stage before changing recovery again. Do not create repeated sleep/reset loops.

The next goal turn completed R04's actual Windows packaged runtime/API coverage.
The first isolated launch through SSH failed before readiness (runtime start
deadline). The Smart Card service was actually running, while the native system
diagnostic in that SSH context returned ERROR_ACCESS_DENIED. Preserve that failed
experiment rather than blaming transport. A second isolated comparison used the
same LocalSystem account as the production Agent and the production timing
configuration, through a uniquely named manual-only scheduled task. No system
permission, production service or network setting was modified. Both experiments
verified the same 128-entry CI manifest and had no readers or enabled modem path.

The second comparison passed connected, disconnected/retrying, automatic
reconnection, explicit runtime stop and start/connected with the same test PID.
Only its private loopback forwarding sockets were interrupted. Reconnection was
observed on the second backoff sample, not immediately. The production Agent PID,
configuration hash, two modem policies and inactive data sessions were unchanged.
The test credential was revoked; the child, private config/directory, proxy/control
listeners and once-only scheduled task were removed. An independent post-read
confirmed the task/listeners/directory gone and both production Agent and Smart
Card service PIDs unchanged. Private evidence is under
`agent-warning-36088021096/windows-isolated-YkUdZq` (failed SSH-context comparison)
and `windows-isolated-RAWDUl` (successful service-account comparison and cleanup),
plus `windows-after-isolated.json`. Windows native GUI and production rollout
remain unverified; do not restart the real Agent just to obtain a green test.
Unique next step returns to incomplete call/SMS acceptance and PR reconciliation;
do not repeat these packaged link-transition tests or consume another SMS.

The read-only PR/worktree reconciliation found that old PR #9/#10 contain useful
Provider cleanup fixes absent from current PR #13: post-2xx ACK/SDP failures lose
the dialog handle, wrappers can discard cleanup ownership, and Backend cleanup
mistakes a nil error for an accepted BYE. Port these existing F1/F3 fixes into
PR #13 only, retaining the current DTMF and receive paths. Do not import durable
Core call receipts or device pairing. A real SIP/media/wrapper/Backend regression
has been added but has not yet been run; no production source edit or validation
claim exists for this batch. Next: focused pre-fix counterexample, scoped port,
focused green, then one complete existing GitHub workflow. No paid fault injection.
The private inventory is `branch-reconciliation-20260926-TO6l10/inventory.json`.
The old recovery worktree still has 69 dirty files; no branch, PR or worktree was
removed, closed, merged or reset. Do not describe the repository as fully clean.

The cleanup port is now `bc7dd364c76582d2f2e4b9c14d275f1b07d7961c`, tree
`2c9aad97bda81ee1cf9701f97a91ee1a406115dd`, committed once and pushed to existing
draft PR #13. All six new regression scenarios compiled and failed on unmodified
`4ca9315`, then passed under race detection after the port. The focused existing
call/media checks also passed. The old media test's nil-return assumption was
explicitly changed to an idempotent cleanup handle, retaining exactly one BYE.
Public provenance and M37 were updated without changing its pending-hardware
status. Raw red/green commands/results are private in
`provider-call-cleanup-20260926/`. Full CI is not yet confirmed. Production remains
`4ca9315`, Android v77 and Core `8d0c4c6`; no paid action or deployment occurred.
Next obtain the exact-head full workflow result through the global status tool,
then update the existing PR evidence. Do not merge or deploy merely because CI passes.

The existing PR-triggered full Go Runtime workflow `36244088192` passed. The first
global-wrapper wait timed out at 600 seconds; the second returned SUCCESS for the
exact `bc7dd36` head under the owner's maximum-three-check allowance. No manual
duplicate workflow was dispatched. Artifact/log checkout ref is GitHub's synthetic
merge `fe0bdc45099f60c9c4826d8fd7322f884735f2c0`; its tree was independently read
from Git and exactly matches candidate tree `2c9aad97bda81ee1cf9701f97a91ee1a406115dd`.
Completed logs confirm all six new cases, full Linux Core/Provider/upstream race,
WebUI graph/context checks, real-process loopback recovery and fresh Linux install,
restart, uninstall/reinstall. The standalone subprocess-only liveness fixture was
skipped as intended; its separate chain passed four scenarios. Non-verbose race
logs do not prove zero individual skips. macOS output is development-signed only.
Logs/artifact metadata, red/green hashes and original PR body are retained in the
same private cleanup evidence directory. Existing draft PR #13 now has the exact
candidate, CI and actual earlier SMS/trial facts; the stale unused-SMS claim is gone.
Provider worktree is committed; no merge, rollout, phone action or paid test was
performed by this cleanup batch. The next action returns to v77's natural USB
recurrence/readback and the remaining client acceptance, not another CI or cleanup port.

At 13:23:31 UTC the single target-limited readback found signed v77 at the same
PID 32467, powered/Dozing with both deep/light device-idle flags false, and no new
USB recovery log. At 13:23:41, exact-card Agent/card/card-route/hardware facts were
ready, available and fresh (received 13:23:33, expiry 13:24:03); both authorized
lines were ready and idle. This is one current observation, not continuous health
or a reproduced/reset v77 failure. No wake, reset, paid action, sharing change or
server mutation was used. Evidence is `after-provider-pr-check.*` in the existing
v77 private root and `v77-after-provider-pr-check.*` in the existing readback root.
The new post-reset handshake retry and long deep-idle/battery behavior still need
real evidence; the exact next failure stage remains the USB diagnostic cursor.

At 13:36:05 UTC, one target-limited read found v77 still at PID 32467,
powered/Dozing with deep/light device-idle false and no new MDDUSB recovery line.
The 13:36:53 authenticated Core read independently found the exact card, fresh
ready hardware/card/card-route facts and both authorized lines ready/idle. This
is another point observation, not continuous or deep-sleep acceptance. No reset,
wake, new APK or paid action was performed. Private receipts are
`android-evidence-36238994751/usb-followup-1336.*` and the existing readback root's
`v77-usb-followup-1336.*`. Android's permission API only returns success again when
permission already exists; it does not reset USB transport. The retained failure
already showed granted permission, so repeated permission requests are not the
proposed transport fix. Keep v77's bounded same-handle recovery and await a real
failure stage rather than adding repeated resets or a wake lock.

Old PR #9/#10 reconciliation is now classified. Their F1/F3 cleanup ownership was
ported to `bc7dd36`; durable terminal-call receipts remain part of the explicitly
excluded Core recovery architecture. Current Android's hub/link/epoch scan ticket
already fences stale publication; its architecture does not contain the old
automatic HTTP SMS-catch-up latch, and ordinary TLS transport failure is retryable.
Do not port those obsolete implementations just because the source differs.
F6 manual recovery from permanently unreadable encrypted configuration remains
unique and unabsorbed in the old branches. PR #10's browser-recording fixture
timing change is also unique; no current reproduction justifies mixing it here.
No old branch, PR, worktree or dirty file has been removed or silently declared
absorbed. Preserve these remaining unique changes for the final reconciliation.

The read-only SMS investigation found a concrete configuration-loss bug: the live
catalog has an SMSC, and Core/Provider/prepared identity retain it, but the registrar
does not pass it to IMSSMSTransport, producing an empty RP-DATA destination. This
is not yet proof of the historical RP cause 38. The minimal Provider-only fix uses
the existing SMSC encoder; it changes no Core/client API, routing or retry policy.
On unmodified `bc7dd36`, both configured address cases and the real UDP
REGISTER/MESSAGE decode failed with an empty destination; the unset control passed.
After the one-field adaptation all passed under race detection, including related
identity and registration-recovery checks. Private logs are
`provider-smsc-20260926/red.log` (SHA-256
`b82633e0fdf11dc87202df53cc716c7d000e6c1e928a3e705d03387751410bfb`)
and `green.log` (SHA-256
`2ac084bd33e0942f57b7baf7b996178c16765ee93021df1c6e7630713d34fa48`).
The adapter, regression, provenance and incident ledger are committed/pushed once
as `d69bdbbc8f20c126f2075a9e8c03ba8048e776d1`, tree
`3c1a8c1c6a27a33bd7800676ae72d511d8e6e5da`, in existing draft PR #13.
Its normal PR workflow is the only full-build qualification; no manual duplicate
was dispatched. Full workflow `36246200299` returned SUCCESS on the second global
wrapper wait after the first timed out at 600 seconds. Its checkout is merge
`e86f932276cccdd55ac97d4c09eba925795f1355`; Git independently confirms the exact
candidate tree above and main `8d0c4c6`/candidate `d69bdbb` parents. Completed logs
confirm the SMSC regressions, full Linux Core/Provider/upstream race, graph/context
gates, four real-process recovery scenarios and fresh installation lifecycle.
The standalone subprocess liveness fixture skips as expected; non-verbose logs
do not enumerate every skip. No upstream verbose skip was reported. macOS PR
output is development-signed, not a production Developer ID replacement.
Logs, artifact metadata and the previous PR body are private in the same SMSC
evidence directory; the CI archive SHA-256 is
`aa15d19af93d58617806f5646205c4a0480c99c278d32e2ce6fc894718fb12dc`.
Draft PR #13 now records this exact candidate, CI and consumed SMS authority.
No rollout, merge, further phone mutation or paid action occurred. The next action
returns to native incoming cooperation and remaining client acceptance, with USB
changes conditional on a real v77 recurrence/stage rather than another unchanged
polling/sleep loop. The one pending cooperation question is not a reason to stop
independent work or ask again. Preserve the unique old-PR storage-recovery work
and the open Windows GUI/deployment gaps.

### September 26 owner incoming feedback and fresh one-shot SMS authorization

The owner now reports normal ringback when calling the mainland line and says
they also sent it a message. Correlate those attempts with actual incoming call
and SMS records and native presentation; ringback alone is not Android answer or
audio acceptance. The UK incoming call still cannot connect. Inspect the actual
attempt once against the deployed receive-path evidence; if the cause remains
unknown, defer this issue as explicitly requested rather than blocking all work.

The owner explicitly authorizes ONE fresh UK self-SMS. This supersedes the older
consumed-SMS restriction only for this new attempt. Confirm the exact existing
card/self-number, preserve the original response and operation identity, and do
not retry an unknown or failed submission. No new outgoing call, deployment,
switch change or PIN action is needed.

Fresh production evidence at 06:40:52 UTC found the mainland incoming call at
06:30:30-06:31:16 (`missed`) and received SMS at 06:32:28. Both were actually
opened in native v72 history. SMS displayed sender, receiving line/card, body and
Reply. This is not incoming answer/audio acceptance. The UK Provider recorded
parsed INVITEs at 06:32:51 and 06:33:23, each with successful 100 and 488 writes.
The no-arrival diagnosis is superseded. Handler completion has no error, pointing
to the local media acceptance branch; exact SDP/codec was not retained. Defer
further incoming testing as authorized rather than requesting repeated calls.

No fresh SMS has been submitted: both Core readiness and actual native Send are
blocked by `inbound_messaging_failed`. A real-adapter/runtime regression reproduced
request-local errors poisoning global messaging, overwriting a genuine queue
failure, and clearing a terminal receive failure. One scoped Provider fix separates
these lifetimes and retains persistence/transport protection; no Core/client change.
The first fixture lacked Via and was rejected before dispatch, so it is not counted
as the reproduction. The corrected wire fixture fails on pre-fix code and passes
after the fix. An isolated hash-verified Homebrew static dependency was used solely
for this one-time red/green; no system install or production build occurred locally.

Candidate `4ca9315f516a162e0c2193d8182a5a8b47fb4946`, tree
`485ab706ae5568573be71441c62c6beadf623881`, is committed/pushed to draft PR #13,
with source, regression and public provenance in one batch. The single unchanged
GitHub workflow is `36225153970`. Its global status tool completed the 600-second
wait with exit 124/timeout, not a terminal CI result. No second query, duplicate
workflow, candidate deployment or SMS submission was made. Production remains
`494cf899`, Android v72 and Core the minimal baseline. No waiting process remains.
Next on authorized continuation: obtain the exact-head CI terminal result through
the global tool, then a guarded single-line trial and the one authorized self-SMS.
The fresh authorization is unused; do not apply the old consumed-SMS restriction
or ask the owner to authorize it again. Raw/UI evidence is private under
`provider-evidence-36220464366/owner-incoming-and-sms/`; no automatic paid retry.

### September 26 owner-confirmed incoming failures

The owner attempted both incoming calls: the UK line did not connect and the
mainland modem number reported powered off. One fresh read at 04:56 UTC found
both exact cards attached and no active/pending calls. The deployed UK Provider
executable was independently rehashed and still matches the diagnostic trial.
Its bounded journal read contains no parsed INVITE; this does not establish
carrier non-delivery or prove that the parser/transport accepted incoming data.

Read-only modem diagnosis used an independently enumerated sibling Modem port,
not the Agent-owned AT port, and freshly matched both equipment and card. Radio
is fully on; CS/PS/EPS report registered; the operator VoLTE MBN is selected,
IMS capability enabled and the IMS PDP context active. No voice call was present.
Unconditional and unreachable forwarding query inactive. Incoming barring returned
OK without a status payload, so it remains unknown. Unsupported IMS status queries
returned ERROR, not a made-up registration result. CEER lacks an attempt timestamp
and is not assigned to this incoming call. No Agent restart, configuration change,
4G enablement, paid operation or extra PIN/AKA command was performed.

Source investigation found a separate concrete Provider gap: only the dialed SIP
TCP connection was serviced. No Contact listener or peer-initiated Security-Agree
SA pair existed. The two new real userspace-network regressions fail against the
pre-fix source (connection refused after REGISTER, valid peer SA timeout) and pass
with the fix. This is a transport reproduction, not proof of this carrier's path.
The one-time local red/green check explicitly selected registrar/security source;
the initial whole-package attempt failed for missing AMR headers and is not PASS.
Formal builds/full race remain GitHub-only. The scoped patch owns listener/child
connections in the existing flow, matches the P-CSCF peer and closes on reset,
adds the negotiated second ESP pair, and preserves Core/client/user switches.

Candidate `494cf899fd34694cfa06c7f187fed8b76b1ed68c`, tree
`956701495b61db1f6b0c7710cf4875a2b435b7e6`, was pushed to existing draft PR #13.
One go-runtime workflow `36220464366` was dispatched. The earlier global status
wait timed out without a terminal result. On the owner's explicit continuation,
one resumed global-wrapper wait returned SUCCESS for the exact `494cf899` head.
No duplicate workflow was dispatched. The Linux archive SHA-256 is
`82b5c8ab461d18b80bcf1fc2b02f9210c0b347fa9c52f4020a459866d0714def`.
At 05:43:10 UTC this candidate completed the already authorized single-line trial;
the running Provider SHA-256 is
`de48f14a5bf55115877e9efa716a26cec23fc2a0d47c26f2e8aa60f5b1c1f8bd`.
The earlier `62f3ecd` instance override remains beneath the new override for
rollback. Both binaries, original unit and state backup are preserved. Exact-card
readiness and released maintenance were read back. Shared Provider/configuration,
Core, Agent, egress, provider-apply and other Provider PIDs were unchanged.
The candidate is NOT merged; this supersedes the older production-version cursor.
Private evidence: `provider-evidence-36208582822/owner-*`, `166-voice-*`,
`166-supplementary-query.*`, `redgreen-path.txt`. SMS rejection, both incoming-call
incidents and historical USB failure stay open, not replaced by this new patch.

### September 26 restrained automatic validation

The owner reaffirmed the existing designated numbers/methods and restrained
automatic validation. Do not ask for the same authorization again. No change to
the destination allowlist, one-time SMS scope, switches or PIN authorization.

The actual v72 Calls page was opened and the existing UK card/VoWiFi route and
authorized destination were confirmed. Its microphone check stopped before any
carrier submission: 980 queued and 978 returned frames, but zero signal frames
and PCM peaks 22/22. This is a low-signal observation, not proof of a microphone
hardware defect. The gate remained unchanged. Core confirmed no call or media
session; the independent handset guard was cancelled only after that readback.

One network-side call per authorized line then used explicitly labeled synthetic
speech, normal authenticated leases/WSS and a PCM file sink, not a fake microphone
or claimed physical speaker. A server-resident deadline guard was armed before
each dispatch, independently of the local process. The normal end was requested
at 30 seconds; the 40-second guard found the owned call already ended.

- UK VoWiFi: answered 05:55:09 UTC, ended 05:55:36 UTC, about 27 seconds connected.
- Mainland cellular: answered 05:56:59 UTC, ended 05:57:29 UTC, about 30 seconds
  connected. Core reported `terminal_confirmed=true`. A fresh, identity-checked
  sibling-port query subsequently found no voice entry in CLCC; the two unchanged
  mode-1 entries are data contexts and were not terminated.
- Both final Core states were idle. Both captures contain non-silent downlink,
  with selected audio windows distinct from the repeated synthetic uplink.
  This accepts scoped API/carrier outgoing connectivity, not handset acoustics,
  inbound carrier delivery, subjective audio quality, DTMF or SMS.

The first private diagnostic preparation used an incorrect media-path assumption;
the next exposed a Host/Origin mismatch. Both occurred before any carrier dispatch,
and their receipts are preserved separately. The unused lease was explicitly
revoked. These are test-driver defects, not product or carrier failures. The UK
capture kept its initial canary filename because the Provider does not emit a
second started message on binding a call; its raw zero `callFrames` classification
is retained with an analysis correction. No extra call was placed for capture
labeling. The cellular capture marks the accepted start response explicitly.

Evidence: `provider-evidence-36220464366/rollout-result.json`, `remote-receipts/`,
`network-giffgaff/`, `network-mainland/`, `audio-analysis.json`,
`166-voice-query.stdout`; the no-carrier native attempt is under
`android-evidence-36079716911/provider-494cf899-giffgaff/`.
Private numbers, credentials, raw configuration and audio stay outside Git/PRs.

Unique next step: diagnose the remaining incoming path against an actual externally
originated attempt and the new Provider's bounded receive logs, without repeating
these paid outgoing checks or treating them as incoming acceptance. The mainland
incoming issue and retained carrier SMS refusal remain open. Do not invent an
unapproved cross-card paid destination, resend the consumed one-time SMS, weaken
the native audio gate, redeploy the completed trial or hold unrelated work for a
repeat authorization. Android remains v72 and Core remains the minimal deployed
baseline. PR #12/#13 remain drafts, not merged or fully accepted.

### September 26 reader continuity reconciliation

The preceding goal turn made progress: exact-head CI succeeded, the authorized
single-line Provider was deployed, and both designated outgoing paths connected
and terminated with independent guards. Do not repeat those calls as a goal loop.

One later production read at 06:06:25 UTC matched the original shared card and
the v72 Agent process generation recorded at 00:26:07 UTC. The reader name is
unchanged, but its session generation and connection timestamp changed before the
02:08:43 snapshot. The later two snapshots retain the same reader generation and
connection timestamp; at the latest read that connection was approximately 4 hours
23 minutes old, last_seen was fresh, and the line was ready and idle.

Actual Home activity displays the Reader Agent disconnect/reconnect at 09:43
handset time. Its diagnostics reports retained `http_0_EOFException`, one reader,
Gateway and reader connection both online, and no local call. This supports
observed same-process recovery and later continuity, not an established cause of
the earlier reader-session change. It is not proof of uninterrupted APDU service,
physical USB power behavior, natural idle expiry, OEM endurance or battery savings.
No restart, refresh, network change, share toggle, PIN/AKA replay or paid operation
was performed in this reconciliation. Evidence is
`provider-evidence-36220464366/reader-continuity.json` and `continuity-ui/`, with
the underlying three Core snapshots referenced in that private assessment.

The current external-input gate is one actual incoming attempt against the newly
deployed receive-path fix. A single coordination question asks the owner to call
the known UK card once from another phone, without answering or repeated dialing,
and report the observed result. This is not a request to reauthorize the standing
outgoing tests. One deduplicated Telegram notice was accepted; do not resend it or
the question. No incoming observation job is running. On the owner's attempt,
read the bounded receive journal for that interval and correlate it with the
actual native/Core incoming state. Do not poll while awaiting that external event,
invent a cross-card paid destination, or replace missing incoming evidence with
another outbound/reader/UI pass. Goal remains incomplete, not accepted.

### Owner unlock without reader replug: current execution

Latest owner authorization supersedes the deployment question: proceed with the
single-line Provider trial and roll back on failure; do not ask again. It does not
authorize PR merge, unrelated service changes or automatic reuse of the one-time
self-SMS. Execute the verified CI artifact under the existing idle/maintenance
guard, retaining the old binary, base unit and state backup. The private foreground
rollout driver is `provider-evidence-36208582822/rollout.cjs`; never rerun it blindly
after interruption, inspect its result/remote receipts first.

AUTHORIZED TRIAL NOW DEPLOYED: at 2026-09-26 02:11:01 UTC, the affected line's
Provider is `62f3ecd0cb4fb2dedddabe27e634a16283ce461a`, executable SHA-256
`c9666a6ca34ff6963795c35271a9c5d969eee63ef23e80f4421672f42c16df1f`.
Only the instance-specific `90-mdd-diag-62f3ecd.conf` ExecStart override was added.
Shared Provider link, desired line/config bytes, Core/Agent/egress/provider-apply
and all other running Provider PIDs stayed unchanged. The maintenance lease was
resumed and exact-card readiness was separately confirmed at 02:11:44 UTC with
no active/pending call or media session. No new SMS/call, PIN, switch or PR merge.

The first attempt checked `/proc/PID/exe` immediately after Type=simple start and
failed its identity check; it removed its override, but its equally immediate
rollback check also failed, retaining maintenance. A subsequent actual read proved
the old executable SHA matched the preserved rollback and the staged candidate
matched the CI manifest. We do not assert which bytes the initial check observed.
The controlled second attempt reused that exact held lease and existing candidate,
waiting 30 seconds before checking exec identity. It succeeded and released the
lease. Both attempts and old state backup hash are retained privately, not erased.
Private receipts: `provider-evidence-36208582822/rollout-result-attempt2.json` and
`remote-receipts-attempt2/`. The old Provider is the preserved release recorded in
`rollback-identity.json`; remove only this trial override under maintenance to
return to it. Do not rerun either rollout attempt.

Current next step is actual post-change incoming evidence, not another deployment
authorization gate. Logs now identify parsed INVITE arrival, numeric response and
write failure. Incoming/SMS remain unaccepted until real carrier evidence exists;
registered/ready is not that evidence. The consumed one-time SMS is not resent.

Post-rollout native capture showed the unlocked Calls page, Gateway online, the
expected card and selected VoWiFi route ready. No call was initiated. The owner
was asked to place one incoming call from another phone (not another deployment
approval). One foreground journal observation used 30/60/120-second waits and
ended without parsed INVITE events. No owner confirmation of an attempt arrived
during that window, so this is not a reproduced post-change incoming failure and
must not be attributed to the carrier. No further automatic observation loop.
Evidence: `provider-evidence-36208582822/ui/after-provider-rollout.png` and
`incoming-observation-result.json`. On owner attempt feedback, read the bounded
journal for that actual interval; do not rerun the one-shot watcher or redeploy.

The owner explicitly unlocked the phone without reconnecting the eSIM reader,
requested a power/sleep/software diagnosis, and requested USB status and reconnect
advice on Home and Readers. This supersedes the prior unlock block. Do not keep
asking for replug or attribute the historical fault to hardware without evidence.

The recorded project ADB server was still owned by this task; its target entry
was absent, while one bounded TCP check succeeded. One target-specific connect
restored ADB without changing the shared server or keys. Ordinary MDD launch
restored saved availability/sharing. Native Readers showed one identified USB
reader and the original card, without a new permission grant, refresh, PIN action
or physical replug. A Core read at 00:10:35 UTC confirmed the exact card and ready
line, with no active/pending call or media session.

Read-only USB/power evidence shows the reader enumerated, power-role source,
data-role host, phone awake, and device/light idle false. Retained USB log output
does not cover the historical failure. These facts do not prove prior power loss,
sleep-triggered disconnect, or a fixed software transport defect. The upgrade's
stopped service is established separately; opening MDD restored it. Historical
USB write -1 remains unclassified, not silently marked non-software or resolved.
Raw evidence is private under `android-evidence-36160451309/after-unlock`.

v71 real SMS presentation regression is now verified: original-record check
changed the exact local failed operation to `failure_observed`; native history
showed `Part 1 · Failed` in red with original body/line/card/peer. No SMS was sent.
Older response detail was already discarded and is not reconstructed. This does
not accept carrier SMS or incoming calls.

Current batch adds one shared USB status renderer to Home/Readers, using existing
attachment, permission, card-identity, link and failure observations. Only USB
failure suggests an idle-safe reader/OTG replug; permissions and user pause/share
intent retain their own explanations. OMAPI failure is separate from USB health.
No I/O reset, wake lock, boot activation, Core or Provider change. Existing tests
are unchanged; no new automated red/green claim.

Delivered `88bea7dac8efa8fd96cdfd9d62b5938a6dacd7fc`, tree
`f0580b56cfe3096436a6800a628d06f9c4fb9455`, through existing draft PR #12.
One signed workflow `36204102346` passed exact-head. The normal preview signer and
source matched; APK SHA-256 is
`c3b9bc51f09225ed5b77a53b75f5b90310f12d6904ab314f9a0ce0667445aa48`.
Pre-install keyguard was not showing/input-restricted and Core was idle with the
exact card. In-place installation succeeded without data clear; normal app launch
restored the reader. Core at 00:26:07 UTC confirmed exact-card readiness and no
active/pending call or session. No physical replug, switch, PIN or paid action.

All five v72 native pages were opened and inspected. Home/Readers USB status text
matches exactly, is green, identifies the original card, and does not present
replug advice for healthy hardware. The independent OMAPI denial remains visible.
Settings shows v72/source `88bea7dac8ef`. Messages retained the failed local receipt;
actual history shows the original failed part, body and line in red. Screenshots
and XML are private under `android-evidence-36204102346/ui`. The error-only replug
hint is compiled but NOT physically failure-injected; no hardware fault is invented
to claim that branch accepted. Historical write -1 remains unclassified.

The lock prerequisite is resolved. Preserve the prior recovery/paid-call evidence.
Remaining: carrier location rejection and incoming-call failure, historical USB
failure cause, wider natural-idle/OEM behavior. Provider draft PR #13 includes the
CI-passed header fix and DTMF candidate but still awaits review before merge/deploy.
The owner corrected the premature stop for PR review: carrier SMS rejection and
incoming-call failure remain blocking product defects, not a completed batch ready
for acceptance. PR #13 is only a candidate; its SMS header propagation has not been
shown to fix the carrier rejection and is not an incoming-call fix. Continue
diagnosis and necessary scoped development before presenting this as ready.

Source reinspection confirms the evidence gap at the incoming boundary:
`IMSInboundWireServer.handleRequest` can return 400/420 before dispatch, and
`IncomingCallController.RoundTripInvite` can return 480/486/488 before creating
pending state. The service adapter retains MESSAGE handler faults, but does not
retain those INVITE results. These are possible paths, not observed responses in
the owner's attempt. Empty call history cannot distinguish upstream non-delivery
from local rejection. Do not attribute either defect without corresponding evidence.

Unique next step: close the incoming request/response evidence gap and check the
SMS candidate against the actual rejected request contract, keeping the two faults
separate. Do not rerun the completed v72 UI/recovery batch. The existing owner
review gate still applies to merge/deployment, but does not prohibit read-only
diagnosis or scoped candidate development. The prior merge/deploy question excludes
new paid SMS/calls; do not repeat it or automatically reuse the consumed self-SMS.

September 26 follow-through: Provider draft now contains
`62f3ecd0cb4fb2dedddabe27e634a16283ce461a`, tree
`b8d5210b580f574fc4e7117439cac4e5b2adebad`. The existing service adapter logs
parsed INVITE receipt, numeric responses and streaming response-write failures
with process-local request sequences. It never records carrier Call-ID, numbers,
headers, SDP or raw errors. Non-streaming responses are labeled prepared, not
written. No SIP handling, call guards, Core or Android behavior is changed.
Parser failures before the adapter remain outside this observation; absent logs
alone still do not establish a carrier defect. The public ledger now tracks both
incidents explicitly as unresolved rather than treating CI as functional acceptance.

One workflow `36208582822` passed for this exact head, observed through one
blocking global status-wrapper run. Existing tests are unchanged; no new automated
red/green claim. No source
build/test was run locally. `git diff --check` and ledger generation succeeded.
One read-only production baseline at 01:26:52 UTC found the exact card ready and
no active/pending call or session. This is readiness only. Private receipt:
`android-evidence-36079716911/owner-autonomous-giffgaff/inbound-diagnostic-baseline.json`.
No new paid request, restart, network change or deployment. Preserve this candidate
as diagnostic support, not an incoming-call fix or a solved SMS rejection.

The Linux CI artifact was downloaded to private `provider-evidence-36208582822`.
Archive SHA-256: `e26d5f1803575d28a1fc879cd0179a1572c80b4e72033b6f85239d3a37f9f14f`.
The manifest names the exact source above; Provider bytes and source archive were
independently checked against their manifest size/hash. Provider SHA-256:
`c9666a6ca34ff6963795c35271a9c5d969eee63ef23e80f4421672f42c16df1f`.
Existing PR #13 body was updated, explicitly diagnostic and not functionally accepted.

A distinct scoped authorization question has been sent: temporarily deploy this
candidate to the affected line only, while idle, retaining rollback; no PR merge,
Core change or automatic paid call/SMS. This is not a renewed request to stop for
general PR review. Await this specific live-deployment authorization before making
the disruptive change, and do not repeat the question. Historical SIP evidence
cannot be reconstructed from the currently empty journal. No new source change
should pretend to resolve that missing runtime evidence.

Read-only rollout preparation at 01:43:12 UTC confirmed the affected Provider
has a dedicated instance unit, no existing drop-ins, stdout to journal and stderr
inherited. Its ExecStart uses the shared Provider executable, so replacing that
shared link is outside the one-line scope. An authorized candidate trial must use
an instance-specific override and retain/remove only its own override on rollback;
preserve the base unit and all other instances. The existing maintenance contract
rejects active/pending calls, registration, SMS dispatch and start/stop transitions;
recheck and acquire that guard at execution time, not from this old snapshot.
Private read-only receipt: `provider-evidence-36208582822/live-unit.json`.
No production file, service or lease was changed. The targeted deployment question
remains unanswered; this is the second consecutive turn with that gate, not an
authorization inferred from automatic continuation. No duplicate question or CI.

### September 26: SMS failure diagnosis and client presentation batch

The owner's one self-SMS exception is consumed. Do not resend or redial to verify
presentation. The retained failed operation reports `message_send_failed`, layer
`messaging`, detail `Forbidden - Service not allowed in this location`. The
matching history event has `kind=submitted` and `state=failed`; kind alone is not
submission success. No inbound SMS was observed in the prior bounded window.

Read-only production inspection found the configured IMS access-network statement
contains `country=GB`. One HTTPS query through the exact Provider SOCKS endpoint
returned a GB geolocation. This tests HTTPS/TCP exit only, not the existing ePDG
UDP association or the carrier's own classification. No node, switch, registration
or production service was changed. Private evidence: `giffgaff-self-sms/location-config.json`
under the installed v70 evidence root recorded below.

Source diagnosis found `messaging.IMSSMSTransport.SendSMSPart` builds its dialog
without copying `Profile.AccessNetworkInfo` / `Profile.VisitedNetworkID`; the
shared dialog builder only uses explicit dialog fields and defaults PANI to
`IEEE-802.11`. REGISTER uses the configured profile. This is a concrete adaptation
gap, not proven attribution for this carrier rejection or the incoming call.
No speculative Provider or Core change is included in the Android batch.

The current client batch preserves structured HTTP code/layer/detail, stores a
bounded dispatch error in the encrypted receipt, colors failed history events
red, and records matching failed submissions as `failure_observed` rather than
successful submission. Failed/partial records remain unresolved and retain
payloads; no resend or retry mechanism is added. Existing tests are unchanged.
Delivered client commit `7abd80ecac244686ff4af45a60ebf519ff5bc5ce`, tree
`a400bafa650ec1a8ef33c236f3e5bdb4233d30fc`, is pushed to the existing draft PR #12.
One signed workflow `36160451309` passed for that exact head, using the required
blocking global backoff wrapper. No duplicate run or new automated red/green test.
The matching stable-signed non-debuggable v71 was installed with `install -r` and
no data clear. SHA-256:
`e40f19e5c50eb93da9fab81a43f69907252e551fd8bcf20ff21a67146e2ea2c5`.
Private artifacts: `android-evidence-36160451309`; CI signature and local hash match.
Pre-update exact-card readback was ready and idle, with zero active/media sessions.

Physical regression is NOT complete. UI launch/capture saw OEM AOD; window policy
reported secure keyguard showing. One wake attempt did not expose the app. An
unlock request and one deduplicated Telegram notification were sent. Do not bypass
the lock, loop captures, resend the SMS or ask again without new owner input.
Unique next step: after owner unlock, read the existing failure in native history
and use original-record reconciliation, then address the isolated Provider header
gap without a new paid operation or treating incoming calls as repaired.

The header gap is now implemented in the already-existing Provider draft PR #13,
not mixed into the client PR and not a new worktree. Candidate
`0a67a9609b9779c1d869623c2e5273c092a76a14` (tree
`dc6171e1661004557f0fc3724f64a2a04e827fdb`) adds only the two profile-to-dialog
field assignments and upstream adaptation notes. Core, credentials, routing,
registration and paid retry behavior are untouched. No new tests or fabricated
red/green evidence. The existing full Go workflow `36162069372` is the one current
candidate run. It succeeded for the exact head, observed once through the blocking
global wrapper. No duplicate workflow, Provider deployment or additional paid test.
The ten Android unit tests in the downloaded artifact report zero failures/errors/
skips; existing emulator/Core jobs also passed, not new physical SMS acceptance.
Next: retain the Provider candidate for review, finish read-only native history
acceptance when the owner unlocks, and do not spend another SMS/call automatically.

### Post-update physical readback: service absent while launch awaits unlock

The next goal turn classified the preceding turn as progress (two code candidates,
exact-head CI and Android installation), not completed acceptance. One purpose-
specific post-update Core read at 16:51:16 UTC found `card_not_present` and stopped
VoWiFi state, with no active/pending call or media session. A target-scoped Android
service read found no AgentService/foreground service. The pre-update ready-card
snapshot must not be represented as the current post-upgrade state.

Evidence: `owner-autonomous-giffgaff/after-v71-update.json` under the v67 private
evidence root, and `android-evidence-36160451309/after-update-services.txt`.
The existing manifest has no package-replaced/boot receiver; MainActivity restores
saved availability from onStart/binding/storage callbacks. No claim that Core or
Mesh failed is supported by this stopped local service. No new automatic boot/
background activation feature is introduced to work around the current lock.

Physical recovery remains pending owner unlock and ordinary MDD launch. Preserve
saved availability/share intent; verify actual Agent/card reappearance before
claiming the upgrade usable. Do not issue repeated UI probes, change switches,
invoke an unexported service, bypass keyguard, or repeat paid acceptance. The existing
unlock request remains the sole requested owner action.

Blocked audit: the same secure-lock prerequisite has persisted across three
consecutive goal turns, including the installation/diagnosis turn. The preceding
turn made progress by identifying the absent local service and correcting the
post-update acceptance record; it was not a live-job wait. A final single delayed,
target-scoped read at 16:54:04 UTC confirms keyguard showing, secure and input
restricted. Evidence: `android-evidence-36160451309/blocked-unlock-audit.json`.
No CI job, monitor or command remains running. Both candidate workflows are
terminal-success, not a reason to continue polling. Further real reader/page
acceptance requires ordinary owner unlock; paid retries and unreviewed Provider
deployment are not substitutes. Mark the goal blocked, not complete, and stop
automatic continuations until the owner supplies that external-state change.

Branch: `codex/android-production-adaptation`, based on merged main `8d0c4c6`.
Current candidate and installed stable preview: v72 /
`88bea7dac8efa8fd96cdfd9d62b5938a6dacd7fc`, draft PR #12.
Minimal production Core remains merged `8d0c4c6`.
The later dated evidence below supersedes earlier candidate/deployment entries.
Active owner requests are cumulative. The new UI report does not cancel transport,
call, message or recovery work. Use this single checklist for the remaining batch:

Latest owner direction: defer unresolved network root-cause work and perform calls
autonomously using the existing exact-card/number authorization. This supersedes
the microphone-participation wait and the older network-only next-step paragraphs.
Two native attempts have now been completed, one per authorized line, with no
automatic retry. See the autonomous-call result below before considering any
further paid action. The owner-replug check below restored actual Android card
readback and Core readiness. The USB failure's root cause and recurrence remain
unresolved; this is not another shared-Mesh experiment or a reason to redial.

Client control batch `2e38080` is pushed to draft PR #12. Signed workflow
`36079716911` passed for the exact head. The matching non-debuggable v67 was
installed without clearing data. APK SHA-256:
`10eae01ca2334b49a0a4588a5e4cc553b93af5623f0112cdb4b3789bc49b7c91`.
Only Android code and its README changed. The automatic PR trigger was
suppressed for this commit in favor of this single full signed dispatch; no test
or workflow gate was removed. Existing tests ran unchanged; no new regression
test or red/green result is claimed.

Physical v65 page audit opened Home, Calls, Messages, Readers and Settings. Idle
keypad digit insertion worked and the original draft was restored. Settings
diagnostics and aggregate conversations opened; the self-SMS history displays
own/peer numbers, card, body and Reply. This is read-only display evidence, not a
new paid send. Explicit icon colors and in-dialog DTMF pending/acknowledged/failure
feedback are implemented in the candidate. The v67 physical check below supersedes
the earlier pending active-call check.

### v67 physical result and remaining fault

One new authorized giffgaff call passed preparation and showed all three in-call
icons clearly against their backgrounds. Opening the keypad sent nothing; tapping
one digit produced a real request and a red in-dialog `HTTP 500: dtmf_failed`.
The Provider's retained failure for that exact call/digit/duration says
`IMS rejected DTMF with SIP 405 Method Not Allowed`. This is not repaired DTMF and
not evidence that the button failed to dispatch. A read-only scan recovered the
matching completed JSON record from the operation store; it was not an atomic
database snapshot or a replay. No request was repeated.

The local diagnostic machine lost its direct Core route during the call. The
independent API guard failed; its native UI fallback closed the keypad and hung up.
A separately authenticated read from the Core host then confirmed exactly one
matching call, ended after approximately 39 seconds, with no active or pending call.
Do not claim the independent API end succeeded. No fresh human voice-quality or
carrier-side DTMF acceptance was obtained.

The new package's Calls, Messages, Readers and Settings were inspected physically;
Home opened after upgrade and retained the connection. History and Reply were
exercised without sending: reply selected the original card/transport and peer.
The mainland route was red/unavailable with Agent/hardware/card reasons, so no
mainland call was attempted. Reader sharing intent remained on with the same card;
settings shows v67 and its exact source. Private evidence remains under
`android-evidence-36079716911/ui` and `call-giffgaff`, including the failed guard,
native fallback, post-call readback and retained tone failure.

Remaining, not cancelled: Provider DTMF compatibility; Windows Agent WSS flapping
and missing warning; mainland call acceptance; the earlier second-call timeout
cause; incoming/weak-network/endurance subcases. A successful UI batch does not
close these. The subsequent isolated candidate below supersedes the earlier
instruction to inspect RTP-DTMF versus INFO; do not redo that completed source
investigation. No repeated paid tests without new diagnostic justification and a
reliable independently reachable hangup path.

### Isolated outbound DTMF candidate

The Provider defect cannot be repaired by changing Android's existing HTTP request:
the retained failure proves that request reached the Provider. A separate worktree
on branch `codex/vowifi-rtp-dtmf` was created from current `origin/main` (`8d0c4c6`). It is
not another Android copy and contains no client/Core changes. Candidate `53cb34e`
offers telephone-event and reuses the pinned upstream packet encoder on the actual
media bridge; audio/event writes share the existing sequence space. Failed RTP
transmission does not trigger duplicate INFO fallback. Inbound B2BUA is unchanged.

Draft PR #13: https://github.com/lovitus/mdd-sim-gateway/pull/13 .
Full existing Go Runtime workflow `36082098149` succeeded for exact source
`53cb34e73186a816717d082f674b165f6a18d439`. Its first bounded observation timed
out; the second observed success in the same run within the owner's maximum of
three checks. No replacement workflow was started. No workflow changes,
local build/test, production deployment or further paid call occurred. New
automated red/green coverage is not claimed; the specific negotiated-event wire
and concurrency gap must remain visible in the draft review. The field failure
is pre-fix evidence, not proof that the candidate works. The current installed
Android v67 and production Core/Provider versions remain as recorded above.

Next active step returns to the mainland Windows Agent's WSS/transport failure
and missing warning, using existing private trace evidence before any reversible
experiment. PR #13 remains a review candidate, not a deployed fix or justification
to keep calling. It does not close the remaining Android/mainland acceptance gaps.

### Cumulative owner issue register

The owner's latest correction requires carrying every unfinished issue forward.
These stable IDs are the current checklist, not a new workstream or authorization
to expand scope. A new report adds to this register; only scoped evidence or an
explicit owner decision closes an existing row. Earlier dated next-step paragraphs
are historical. Network root-cause work is now owner-deferred; the active next step
uses the autonomous-call and USB-failure evidence below without another redial.

| ID / owner-visible problem | Verified status | Remaining work / closure condition |
| --- | --- | --- |
| R01 Second giffgaff call fails after about 20 seconds | The earlier native attempt ended after about five answered seconds with matching card-route loss and USB write failure. Later client fixes retain termination detail and add bounded event-driven USB recovery; v85 recovered its startup fault and retained scoped session evidence. A separate later VoWiFi/RTP call is recorded in R09. | The original twenty-second timeout and historical USB fault causes remain unknown. Do not leave the completed error-display/recovery work labeled unimplemented, or describe later calls as proof those causes are eliminated. No repeat paid test. |
| R02 Mainland modem call fails after about 20 seconds | The v67 outgoing attempt passed preflight, answered and ended through native Hang up after about 27 seconds. After the minimal Core incoming binding fix, actual native Answer and 45.944 answered seconds were recorded; the owner confirmed both phones heard speech. Final sessions were empty. | Outgoing dial/end and incoming Answer/two-way connectivity have separate physical evidence. Incoming native Hang up did not run after a screenshot timeout; the independent fallback ended that attempt. Historical timeout cause and audio quality are not claimed; no repeated paid check. |
| R03 Windows Agent repeatedly disappears | Runtime and reconnect loop are alive; bounded Mesh comparison did not establish recovery. The owner explicitly deferred network root-cause work. | Deferred, not fixed. Preserve cleanup and loss evidence; do not make network diagnosis a prerequisite for all remaining application work or repeat shared-network experiments. |
| R04 Agent gives no useful connection warning | Candidate 38f1191 / review-ready PR #14 passed full CI and artifact integrity. Retained Mac GUI and Windows LocalSystem runtime/API checks are now joined by actual Windows native GUI connected/colored-retrying/reconnected observations with unchanged GUI and runtime PIDs. Independent cleanup confirmed the temporary identity, tasks, processes, listeners and files removed, with production PID/binary/config unchanged. Failed SSH-context and GUI-driver attempts are retained. | Isolated warning acceptance is complete. Owner review and production deployment remain open; service-install/start/stop/uninstall GUI buttons were not exercised. Do not repeat passed transitions or equate warning repair with transport recovery. Keep this out of Android/Core changes. |
| R05 Received/sent SMS omit own number, peer, card or body; Reply lacks context | Prior v63/v67/v76 evidence remains. The newly authorized v80 self-SMS was submitted once and actually received; native history showed sender, own card/number, body and transport. Reply revalidated the same card and peer and left the body empty without sending. RP 38 history remains visible for the earlier failed attempt. | Display, reply-prefill and failure-receipt correction verified for these cases. Delivery-report confirmation remains distinct from actual self-receipt. No extra SMS is authorized for screenshots; duplicate ingress is tracked separately in R13. |
| R06 Normal and abnormal states need distinguishable colors | Client presentation changes installed. v67 shows red unavailable-route and DTMF error states. | Page review must distinguish sampled states from complete coverage. Do not claim every success/error state was exercised. |
| R07 In-call mute/speaker/keypad controls are unreadable | v67 active-call screenshot confirms all three icons visible against their backgrounds. | Visual defect verified fixed. This is not acceptance of every mute/speaker audio-routing mode. |
| R08 Idle number entry and in-call keypad are confused or unresponsive | Idle digits insert correctly; opening the live keypad sends nothing; one explicit live digit dispatched and displayed a real error. | Preserve those distinct outcomes. Never send DTMF just because the keypad opened. Actual carrier tone delivery remains R09. |
| R09 Carrier rejects live DTMF | Original SIP 405 and the later pre-dial USB failure remain preserved. With d69bdbb trial-deployed and the newly fixed v80 reader ready, one justified call answered, accepted one digit through dtmf_rtp and ended after about 30 seconds. The independent guard and final readback confirmed idle. | Real call and RTP event transmission are exercised; independent carrier IVR recognition and native-client success display are not claimed. No INFO fallback, redial or SMS. Do not repeat the call merely to improve coverage. |
| R10 Every native page and relevant action needs real inspection | Home, Calls, Messages, Readers and Settings opened physically; history, Reply, diagnostics and keypad actions have scoped evidence. Actual incoming controls and native Answer now have physical evidence as recorded in R02. | Audio-route permutations and other unexercised stateful actions remain explicit. Do not turn a page-open check into whole-page acceptance or start landscape work. |
| R11 Reader registration, recovery, long sessions and incoming calls | Signed v85 / a1bf28b passed CI 36292625372 and is installed. Packet-sized CCID reads retain exact protocol limits. The initial write -110 recovered automatically. Lifecycle recovery and Pause/share intent have scoped v84 evidence. A separate four-minute forced deep-IDLE interval retained App/Agent/reader/socket identity and fresh Core reports; cleanup restored ACTIVE and actual native Readers. Incoming Answer/two-way speech is recorded in R02. | Long idle/battery life and the initial write-timeout cause are not proven. Earlier failed v82 durability evidence remains. Do not repeat short loops, paid checks, App restarts/replugs or PIN/AKA merely to improve coverage. |
| R12 PR, server and deployment completeness | Core b6f3a4c / full Go CI 36298445701 is rollback-backed and production-deployed; signed Android v85 / a1bf28b is unchanged. Incoming acceptance is retained and actual idle mobile traffic is qualified in R14. Provider 9e0ec67 / CI 36264788240 remains the single-line trial. PR #14 has isolated Mac/Windows warning acceptance and is review-ready without production rollout. PRs #12/#13 remain draft; all three are unmerged. | Native incoming connectivity is accepted within R02's bounds. Extreme network-switching validation is explicitly non-blocking; do not require a phone data SIM. Long-duration USB/battery behavior and historical root causes retain their separate evidence limits; PR review and separate desktop rollout are delivery decisions. Do not use an unnamed client checklist to repeat completed work. RTP-send evidence is not carrier IVR recognition. Post-CI evidence stays local and in the PR body, without a docs-only micro-commit. No branch cleanup or overall completion claim. |
| R13 Owner-authorized giffgaff self-SMS diagnostic | One native outbound submit/completed send and actual self-receipt remain the only fresh paid SMS. After physical reader recovery, old 524bd33 returned two more report 403 responses; deployed 9e0ec67 received SIP 202 at 23:37:08 UTC for the pending carrier delivery. Core history remained ten events with no new event across the candidate delivery/generation change, and still ten after the v83 observation. No SMS/replay or Telegram change occurred. | One real carrier receive-report acceptance and scoped durable deduplication are proven. Preserve old duplicates and earlier RP 38/outgoing 403 failures. The subsequent USB loss and SWu recovery limit any continuously-online quiet-period claim; they do not invalidate the recorded 202. Do not reuse consumed SMS grants or describe old incoming duplicates as multiple paid sends. |
| R14 Battery-friendly operation: timestamp-only mobile snapshots | b6f3a4c passed full CI and is deployed. Both compiled counterexamples were red/green; the real post-rollout 65-second stream contained one initial snapshot and two heartbeats, with no timestamp-only update. Stable v85 received about 0.01 MB and used 151 ms CPU over two background minutes, versus about 19 MB/6999 ms CPU over the earlier eight-minute sample. Native Home/Readers and final Core showed the same card, online availability and idle calls. | The reproduced duplicate-snapshot traffic defect is fixed and field-qualified. Preserve rounded counters and different-duration sample boundaries; this is not all-day battery or uninterrupted-socket acceptance. APK, Provider, desktop Agents, switches and paid operations were unchanged. |

A fresh read-only Windows check at 2026-09-25 01:45 UTC found the same Agent PID
still running. The preceding twenty-minute event window contained two handshake
EOFs and four read EOFs. This confirms R03 remains current, not its root cause or
the cause of R01/R02. Evidence is private under
`bettbox-agent-route.xhDBb1/continuation-window.json`. No service, route, proxy,
credential, user switch or paid operation changed. Source inspection also confirms
the current client uses certificate-pinned TLS and automatic reconnect. A possible
TLS record-size/path interaction is only a hypothesis; do not weaken TLS or alter
production settings to turn that hypothesis into a purported fix.

### Transport diagnostic correction: SNI is not equivalent to the Agent

The next finite diagnostic batch found a material confounder in the earlier
standalone TLS probes. Those probes sent `localhost` SNI; the actual Agent uses
an IP URL with Go's default IP-host behavior, without that SNI. An adjacent
certificate-verified TLS 1.3 comparison on the Windows host returned HTTP 200 in
85 ms with no SNI and a 240-byte ClientHello, failed before TLS with `localhost`
SNI, and returned HTTP 200 in 116 ms without SNI and an 1824-byte ClientHello.
Certificate-chain and hostname verification remained enabled in all comparisons.
The large probe used extra ALPN entries, not the Agent's hybrid key exchange;
this comparison is not proof of all Go TLS behavior or continuous WSS health.

Before identifying that difference, a 35-second Core metadata capture recorded
no fresh SYN during the failing SNI probe, although other established connections
continued carrying traffic. A separate Windows NIC capture showed its handshake
and 258-byte ClientHello acknowledged on the local TUN, followed by a close.
Together with the later controlled SNI comparison and saved destination-sniffing
configuration, this explains why a standalone TCP success followed by these probe
failures was not valid evidence of an Agent transport failure. It does not establish
where the genuine Agent EOFs originate. Do not use these SNI probes to justify a
Core TLS change, removal of hybrid encryption, MTU change or shared mesh restart.

The Windows capture started only after checking there was no existing monitor or
filter. It used the exact Core destination/port and 64-byte packet truncation;
afterward the monitor was confirmed stopped and its only temporary filter removed.
The Core capture ended under its own bounded timeout with zero kernel capture
drops. No persistent capture, network configuration change, restart, paid operation
or production-code change occurred. Raw evidence is private in
`bettbox-agent-route.xhDBb1`: `tls-size-result.json`, `tls-sni-result.json`,
`tls-ingress-result.json`, and `windows-egress-result.json`, including cleanup.

Unique next action: correlate the actual Windows Agent WSS socket and disconnect
with Core admission/keepalive, using its real no-SNI connection rather than generic
`localhost` probes. R03/R04 and the mainland native-call prerequisite remain open;
the SNI diagnostic correction does not close or cancel any cumulative issue.

### Actual Agent trace: application sends continue during downstream loss

The subsequent paired passive capture followed the real running Agent, not a
synthetic TLS client. Process/socket snapshots identified the Agent's TUN socket
and Bettbox's corresponding physical-interface connection. The Agent PID remained
unchanged. Three successive ten-second exchanges showed inbound 26-byte records
delivered through TUN and 30-byte replies forwarded onto the physical interface.
Encrypted record lengths are metadata, not decoded WebSocket payload evidence.

At the failing exchange the Agent still emitted a 178-byte application record.
Bettbox forwarded it onto the wired interface; Windows retransmitted the same TCP
sequence range several times over roughly five seconds without an advancing ACK.
The interface then received resets from the gateway path, after which Bettbox
closed the TUN stream and the Agent logged read EOF and its scheduled retry.
A subsequent reconnect logged handshake EOF. This counterexample does not support
an Agent that stopped sending or a Bettbox failure to forward those specific bytes.
It narrows this observed loss to the transport beyond Windows' physical interface,
without identifying a particular gateway, mesh node or implementation defect.

The simultaneous Core capture also recorded stalled delivery and closure of a
separate Mac connection. One loopback-proxied stream had a compatible timing and
record-size pattern, but address rewriting and differing host clocks prevent
claiming a proved one-to-one mapping to the Windows flow from metadata alone.
Do not claim a decoded ping/pong mismatch or raise Core's timeout as a proven fix.
No paid call was placed on this intermittently failing route.

Private evidence: `bettbox-agent-route.xhDBb1/agent-wss-result.json`,
`agent-wss-windows.json`, `agent-wss-core.txt` and associated stderr/cleanup records.
Both captures stopped; Windows reports no active monitor and no packet filters;
the Core capture exited normally with zero kernel drops. There was no restart,
configuration change, software deployment or persistent instrumentation.

The actual-socket correlation step above is now complete within these limits.
Next: inspect the gateway/mesh forwarding boundary using the captured sequence
loss, without repeating generic TLS probes or restarting shared networking to
hide the symptom. Any disruptive change to shared mesh routing needs its own
bounded scope and rollback. All R01-R12 statuses remain as recorded, with R03
more narrowly diagnosed but not repaired.

### Shared-network decision requested; Android live readback retained

The Core kernel journal has no entries in the observed loss window. The Mesh
process has no journal unit and its stdout/stderr go to `/dev/null`; no missing
proxy log is treated as proof of no proxy error. Physical-interface counters show
no errors/drops. The TUN has large cumulative historical drop counts, but one
bounded sixty-second delta observed zero new drops and an empty qdisc backlog.
Those cumulative counters do not justify changing its MTU or queue length.
Evidence: private `loss-window-system.json`, `mesh-process-shape.json` and
`mesh-tun-delta.json` under the existing transport investigation directory.

The owner was asked once whether to authorize a maximum five-minute,
auto-rollback-protected Core-host EasyTier KCP/QUIC proxy-off comparison. It may
briefly reconnect other Agents, so it is outside the earlier exact-route/Bettbox
experiment. No such change has been made. The request explicitly preserves SIM,
4G and borrowing intent and requires no active calls before any disruption.
The authorized notification tool accepted one deduplicated notice; do not repeat
the question or notification while the answer is pending. The alternative offered
is to leave shared networking unchanged and continue application work.

Meanwhile the authorized handset remains ADB-accessible using the unchanged
project-isolated server. Its installed preview foreground Service is running and
was created more than one hour earlier; this does not imply an uninterrupted WSS
session or battery/endurance acceptance. The real Readers page shows sharing on,
one attached CCID reader, no reader needing action and the expected card suffix.
An independently authenticated production Core read at 02:19 UTC contains that
same Android Agent and card, with a fresh heartbeat; its current connection began
at 02:10 UTC. The Windows Agent is also present in that single read with two modems,
but this does not override the captured flapping or authorize another paid check.

Evidence is private under `android-evidence-36079716911`: foreground Service dump,
`current-core-agents.json`, and actual `continuation-ui/tab_readers` XML/screenshot.
No share toggle, login, PIN, call, SMS, restart or installation was performed.
R11 retains this fresh reader/UI-to-Core consistency evidence without claiming
incoming-call, silent-drop, OEM or long-duration acceptance. PR #13 source review
did not establish a new defect; its existing wire/concurrency and carrier acceptance
gaps remain open, and no additional CI, merge or deployment was performed.

### Cumulative follow-through and completed background check

The owner again explicitly requires preserving earlier requests when new issues
arrive. R01-R12 remain the authoritative cumulative register. A successful page
check, newer call or artifact build cannot close a different historical failure.
R04 is still an unfixed application-status defect, not something a network repair
automatically resolves. Record implementation, installed version and scoped real
verification separately before closing any row. The shared-network decision gates
only that disruptive experiment, not independent application work.

On September 25 from 02:24 to 02:28 UTC, the existing native app was moved to the
background with Home after a production read confirmed no active or unresolved
call. Three observations, separated by 30/60/120 seconds, found the same app PID,
foreground Service, Agent generation and shared reader/card session, with fresh
production Core heartbeats. The activity was not foreground during those samples.
Returning through its launcher restored the existing Readers activity without
restarting the process. No screen-off/Doze forcing, radio change, share switch,
PIN, fresh APDU operation, call or SMS was performed.

Private receipt: `android-evidence-36079716911/background-reader-result.json`,
with the associated service/activity dumps and Core reads. This establishes only
short background-UI reader continuity; cached card identity and heartbeats do not
prove a new AKA exchange, Doze survival, battery efficiency or long endurance.
Those R11 gaps remain open.

The already downloaded PR #13 Linux archive was checked against its schema-4
manifest: source `53cb34e73186a816717d082f674b165f6a18d439`, all 22 members'
sizes and SHA-256 values matched. Archive SHA-256:
`ef567062af647ae98e0c09310b47aca861eeac240660cb4a88f4e71901cae904`.
Provider binary SHA-256:
`4d62a29e4647cc29a070c54763998612a57013beeb1e5e98ffe48a621e702ed4`.
Private receipt: `vowifi-dtmf-36082098149/artifact-integrity.json`. This is artifact
integrity evidence only, not a deployment or carrier DTMF acceptance. PR #12 and
PR #13 were both confirmed open, draft and unmerged at their recorded heads.

Current next step: address the remaining control-link diagnosis and application
warning separately. Do not repeat the pending shared-network permission request,
already completed paid tests or successful CI. Preserve R01/R02 historical timeout
uncertainty and R09's undeployed candidate; no overall completion is claimed.

### Authorized Mesh comparison and independent Agent warning candidate

The owner explicitly approved the previously requested bounded Mesh debugging.
This supersedes the pending-permission blocker above; do not ask again. One
comparison ran from 02:46:58 UTC on September 25, with an independently scheduled
server rollback at 270 seconds. The existing startup script and cron were left
unchanged. Only the temporary Core-host Mesh process had KCP/QUIC proxy output
disabled and KCP/QUIC proxy input rejected, using options confirmed by that exact
binary's help. No routing/TLS/Agent/Core product configuration was changed.
Two authenticated reads immediately before mutation found no active/unresolved
calls. No paid operation occurred during the comparison.

The 30-second sample showed Windows and Android temporarily absent. Both had
returned by the 90-second sample, preserving their Agent process generations;
the 210-second sample retained the same new connections and physical card facts.
Windows had two ready modems. Android retained the same identified card/session.
This demonstrates automatic reconnect within this disturbance, not the root cause
of prior flapping: restarting the Mesh is a confounder, and the observation is short.

The first rollback verification did not confirm readiness within its two-second
window and recorded `original Mesh restoration not verified`. The immediate
fallback restoration call succeeded at approximately 02:50:33 UTC, before the
five-minute bound. It verified one running Mesh process with the original command
hash and an unchanged startup-script hash. Do not report the first check as passed
or infer why it failed. Subsequent cleanup independently verified the original
process, stopped the timer, confirmed the transient service inactive and removed
all four owned remote helper/state/lock files and their directory.

At 02:52 UTC all six expected Agents were present again after restoration. The
Windows Agent PID was unchanged; its two logged disconnects correspond to the
comparison's two Mesh transitions, with no additional event in that retained
window. Android had reconnected again with the same process/card generation.
The actual native Readers screenshot showed Gateway online, sharing still enabled,
one identified CCID reader and no reader requiring action. No login, App restart,
share toggle, PIN or SIM mutation was used. This supplements R11's real recovery
evidence but does not close R01/R02 or prove sustained transport recovery.

Private evidence: `bettbox-agent-route.xhDBb1/mesh-bounded-result.json`,
`mesh-bounded-recovery.json`, `mesh-bounded-cleanup.json`, `mesh-post-core.json`,
`mesh-post-windows.json`, and `android-evidence-36079716911/mesh-restored-ui`.
Original failure output remains retained. No diagnostic resource remains running.

Separately, the already reported local warning defect has a coherent Agent-only
candidate on branch `codex/agent-connection-warning`, commit `38f1191`, draft PR #14:
https://github.com/lovitus/mdd-sim-gateway/pull/14 . The worker's existing hello
acknowledgement supplies connected state; a disconnect callback runs before waiting
for hardware-operation drain. The authenticated local status and desktop GUI now
distinguish control connection from runtime ownership. Retry policy, hardware
isolation and Core remain unchanged; no server alert subsystem was introduced.

Formatting/diff checks passed; existing tests were not changed and no new automated
red/green coverage is claimed. One full workflow was dispatched with required
Developer ID signing: `36088021096`, pending result. The commit suppresses the
duplicate automatic PR trigger in favor of that full explicit workflow; no CI gate
was removed. There has been no merge or Agent deployment.

The global wrapper subsequently returned success for exact
`38f11912c7774368bc7d7984998fd771e576f244` on its first bounded observation.
Downloaded Windows and macOS archive checks matched all 128 and 72 SHA-256 entries
respectively. The macOS App passed static deep/strict codesign verification with
the original Team `8WPJLUNLY8`; notarization is not claimed. Windows remains the
existing unsigned-development distribution. Archive hashes and receipts are in
private `agent-warning-36088021096/artifact-integrity.json` and draft PR #14.
No local source test/build or additional workflow was run. Packaged state-transition
and GUI acceptance remain unverified, not implied by artifact integrity.

A read-only check at 03:04:49 UTC found all six Agents present with fresh heartbeats.
The restored-config Windows connection had remained unchanged since 02:51:08 UTC;
its PID was unchanged and the retained event query had no new disconnect after the
02:50:36 rollback transition. Thus there is about thirteen minutes of post-restore
connection evidence with the original KCP/QUIC settings enabled. This does not
support a claim that disabling those settings is a proven remedy. No permanent
network change is justified by this experiment; the recurrent transport root cause
remains unestablished. The original media-preflight timeouts are still separate.
Receipts: `mesh-post-core-after-ci.json` and `mesh-post-windows-after-ci.json` in
the existing private investigation directory.

### Native mainland attempt after the authorized comparison

The fresh prerequisite at 03:12:56 UTC had the exact card ready and the Windows
connection unchanged for about 22 minutes. One native attempt was confirmed at
03:16:30.611 UTC. Preparation failed at the existing twenty-second audio deadline,
before carrier dispatch. The final reads contain no matching call and zero media
sessions. The helper's `paidAttempt` means UI confirmation, not proven dispatch
or billing. Its native-end click also does not prove hangup: the preceding
screenshot already shows preparation failed and idle controls.

Retained audio evidence: 1099 capture callbacks, 996 queued frames, 976 received
frames, 648 played frames, zero outbound/inbound signal frames, PCM peaks 38/38.
The existing signal rule requires at least eight samples above amplitude 128 in
a frame; Core readiness also requires signal-bearing frames. The cellular
preflight echoes captured PCM through Core, so received PCM is not independent
proof of modem downlink audio. This is evidence of insufficient observed signal,
not proof of denied microphone permission or a transport-free path. The owner
was not coordinated to speak during this attempt. No threshold was weakened,
audio synthesized, or automatic second attempt made.

The recurrent Windows EOF at 03:16:50 overlaps the preflight deadline. Windows
subsequently retried and rejoined without a process restart; Core's unit journal
has no entries in the retained 03:16-03:21 UTC window. These facts cannot determine
which event caused the other. Keep R02 and R03 separate instead of attributing all
historical audio timeouts to the network. Android remained admitted with the same
reader generation in the post-attempt snapshot.

The old cellular test guard cannot be reused: a separate administrator login has
a different session subject and cannot hang up the handset-owned media session.
This attempt instead armed a bounded independent device-side fallback, retaining
normal native hangup as the primary path. The fallback was cancelled after idle
readback, its marker removed, and the App PID stayed unchanged. No force-stop was
performed. No guard process remains.

The actual phone UI's truncated notice opens the complete error in its existing
dialog when tapped. This was exercised, visually checked, and dismissed. It does
not justify another layout rewrite. Private evidence is in
`android-evidence-36079716911/mainland-after-mesh`, including UI, final readbacks,
guard outcome and bounded Core journal. Matched Windows events are in the existing
Mesh investigation's `mesh-post-windows-call-window.json`.

Current next step: coordinate actual microphone participation before any further
signal-dependent mainland attempt, with a fresh exact-card prerequisite and the
same bounded ownership-safe termination protection. Continue transport diagnosis
from retained evidence; do not repeat passive probes or paid calls without new
diagnostic input. Do not reinstall or merge drafts solely to proceed. Keep all
R01-R12 gaps and the warning/Provider candidate deployment boundaries explicit.

### Follow-up source audit while microphone participation is pending

Reviewed Agent request dispatch, Core keepalive, modem event acceptance and
acknowledgements. Ordinary hardware requests run in workers rather than blocking
the Agent read loop. Some event persistence/default-update paths remain synchronous;
there is no retained stack or timing evidence implicating them in this outage.
The earlier paired actual-socket trace already shows Agent bytes leaving the
Windows NIC before loss. Do not repeat that trace or claim a new deadlock from
source structure alone. No timeout or production source was changed.

A read-only version command on the deployed Mesh executable returned
`3.0.16-4-391c191c`. The corresponding owner-fork commit resolves to
`391c191c3d8b477c3b7e8ef19e87ae3cba5c9504` in `lovitus/EasyTier`
(subject: `fix: satisfy connector clippy gate`). This establishes a source lead,
not binary reproducibility or the root cause. Official upstream's v2.6.4 release
notes and issue #2356 concern different published versions and cannot establish
that replacing this fork would repair the current loss. No mesh upgrade,
downgrade, further proxy change, new packet capture or paid action was performed.
Use the exact fork identity for any subsequent network-owner investigation:
https://github.com/lovitus/EasyTier/commit/391c191c3d8b477c3b7e8ef19e87ae3cba5c9504 .
Reference only, not a diagnosis:
https://github.com/EasyTier/EasyTier/releases/tag/v2.6.4 and
https://github.com/EasyTier/EasyTier/issues/2356 .

### Continuation gate after the failed audio preflight

Historical gate, superseded by the owner's subsequent autonomous-test instruction
and the real results below. Do not ask for microphone participation again merely
to repeat these calls.

The microphone-participation question remains unanswered across three consecutive
goal turns, including the original failed-attempt turn. The preceding turn made
limited progress by identifying the deployed Mesh fork; it did not resolve the
live acceptance prerequisite. Local final receipts still show the attempt ended,
zero matching calls/sessions and a successfully terminated fallback guard.
There is no running acceptance job to wait on. Do not turn automatic continuations
into repeated status reads, unchanged reports, synthetic success or paid retries.

The full goal is blocked, not achieved: resume the retained exact-card/native
voice check when the owner can participate, then address the remaining scoped
recovery gaps. The pending question does not request paid-call authorization again.
Core deployment and existing real reader/SMS evidence stay accepted only within
their documented scopes. The shared-Mesh comparison is finished and rolled back;
it is not a continuing monitor or permission for permanent network changes.
Draft candidates remain unmerged/undeployed as recorded. No new source change,
build, CI, device mutation, notification or repeated permission request was made
by this gate audit.

### Owner-authorized autonomous native calls

The owner deferred unresolved network investigation and explicitly requested
autonomous dialing using the previous information. One attempt per line was made
on the unchanged installed preview versionCode 67. No synthetic microphone data,
threshold change, Core/Provider change, SMS, DTMF or additional deployment occurred.
Exact SIM, route and target were checked in the real native confirmation dialog.
Each attempt had an independent device-side time limit and a native end deadline;
both guards finished CANCELLED, neither force-stopped the app, and neither left a
call or media session active. App process identity stayed unchanged.

- Mainland cellular: started 13:59:56.778857701 UTC, answered
  13:59:56.838301211 UTC, ended 14:00:24.191806248 UTC. Native Hang up was clicked.
  The real screen showed accepted call, connected audio and visible call controls.
  This proves this attempt's preparation/dial/answer/end path, not independently
  recorded speech content or a diagnosis of the earlier timeout.
- Giffgaff VoWiFi: started 14:02:07.575623343 UTC, answered
  14:02:10.769297801 UTC, ended 14:02:15.934011216 UTC. It ended before the scripted
  hangup deadline; no native hangup was submitted by the helper. Core diagnostics
  recorded card_not_present at 14:02:13.043656443 UTC, then stopped tunnel/IMS/voice
  and deregister_failed at the same timestamp as the call end. The native Readers
  page reports USB command write failed (-1), unknown outcome and one attached
  CCID reader needing action. Thus brief answered state is not sustained success.

One explicit native reader Refresh preserved the same write failure. Android's
USB dump still enumerates the CCID device; the scoped USB log query contains no
matching disconnect event. Neither fact identifies cable, host driver, device
power nor application logic as root cause. The earlier shared-Mesh uncertainty
must not replace this new, directly visible local USB failure. No PIN verification,
SIM mutation, sharing toggle, restart or repeated refresh was performed.

Both end states are idle. The phone's giffgaff notice is only `Call ended` despite
the independently retained loss sequence. Preserve this presentation gap for
client-first investigation; do not expand Core solely to explain it. No further
paid attempt is needed to examine the captured failure.

Private evidence: `android-evidence-36079716911/owner-autonomous-mainland`,
`owner-autonomous-giffgaff` and `owner-autonomous-ui`. The generated
`owner-autonomous-summary.json` records counts, exact call times, guard outcomes
and SHA-256 hashes of the call receipts and screenshots. Typed diagnostic API
entries, not the empty scoped journal query, establish the loss/stop timeline.
Overall goal remains incomplete; current reader usability and remaining recovery
gaps are not waived by the owner's decision to defer network root-cause work.

### Client-only terminal media detail correction

Source inspection found the presentation gap: the audio-ended callback records a
reason in transient state, but terminal history reconciliation retires the call
with a generic ended message. Candidate `c18c944` retains the first observed
post-submission media reason across that retirement. Explicit user hangup sets
the existing ended flag before closing audio, so a late callback cannot turn it
into a newly recorded media failure. Normal remote closure has neutral detail;
known failures keep error presentation. Confirmed terminal detail does not still
claim that remote termination needs confirmation.

Only RemoteCall presentation, both existing locales and the Android README changed.
No Core, media gate, ownership, reader implementation or call lifetime changed.
XML parsing and diff checks passed; no local source build/test was run and no new
automated regression or red/green result is claimed. The physical v67 generic-ended
notice is the retained pre-fix observation, not post-fix acceptance.

One independently deliverable diagnostic correction was pushed to existing draft
PR #12. The local cumulative cursor was excluded. Full Android workflow
`36146095057` was dispatched once for this exact head; result is pending. Its
normal duplicate PR trigger is suppressed in favor of this explicit full run;
workflow gates are unchanged. Do not redispatch or merge. Follow this run via the
global bounded wrapper, then verify its artifact before any installation.

USB comparison evidence already exists for an earlier identical failure: both
initial power-on and GetSlotStatus bulk OUT returned -1 while GET_CONFIGURATION
succeeded. That narrows the failing interface but does not prove endpoint stall,
physical damage or an application defect. Do not repeat that comparison or add
blind USB reset/AKA replay code. The current failing reader remains unaccepted.

The global wrapper returned success for `36146095057` on its first bounded wait,
with exact head `c18c9441905dd3df45ae97f1b31c165b854fdd32`. No second workflow ran.
Downloaded source/tree match local committed tree
`926010dfe181c6cb1fb0753350c05ad1d0172ad0`. APK SHA-256 matches its manifest:
`653ddb5fa344e5aedda17fb405f10de7a49dfd8097005e800a6a987c4f63b671`.
Local aapt verifies the expected package, versionCode 70 and non-debuggable
manifest. CI's v2 signature report matches the established preview signer. Local
apksigner was not run because no registered local Java runtime was found; do not
claim it was. Android PackageInstaller accepted the in-place update without
uninstalling or clearing data, enforcing compatibility with the installed signer.

The fresh pre-update Core receipt had zero active calls/media sessions. The real
app reopened online; Settings displays v70 and source c18c9441905d. Sharing intent
is still on. Readers still shows USB write -1 and one device needing action, so
normal package replacement did not repair the reader. No additional call, SMS,
PIN action or network change was performed. The actual post-fix terminal-detail
path has not been exercised by another paid call and is not claimed accepted.

Private receipts: `android-evidence-36146095057` (artifact, integrity, install and
native UI); pre-update idle receipt remains in the prior autonomous-giffgaff
directory as `before-update-c18.json`. One preliminary evidence-path validation
rejected an out-of-scope helper directory before any remote action; the actual
idle read used its already-authorized evidence root. This was not a Core error.

The owner confirmed the physical reader/OTG replug. Actual v70 UI first showed the
reader requiring USB access, rather than another failed transfer. The exact
attached reader's native permission dialog was accepted. Without changing sharing,
restarting the process or refreshing again, Readers then showed one identified
reader, zero needing action and the expected card suffix 1522. The separate OMAPI
access-denied notice remains; it does not invalidate this USB CCID readback.

One production read at 2026-09-25 14:31:54 UTC confirmed the expected card match,
enabled line, readiness with no blocking reasons, zero active calls/media sessions
and no active/pending Provider call. Evidence: private
`android-evidence-36146095057/after-replug` (before/permission/after native UI) and
`android-evidence-36079716911/owner-autonomous-giffgaff/after-owner-replug-v70.json`.
The physical intervention recovered current usability; neither USB root cause nor
recurrence prevention is established. No call, SMS, PIN verification, network or
sharing switch change was made. The owner-replug waiting gate is now resolved.

Current next step: continue the cumulative client acceptance work with the retained
evidence; do not automatically redial to recheck this recovery or label the v70
terminal-detail path physically accepted. Keep R01/R09 and incoming/endurance gaps
open. Do not re-ask for microphone participation, reopen owner-deferred network
diagnosis or repeat the failed refresh. Draft PRs remain unmerged.

### v70 bounded screen-off reader continuity

The preceding goal turn made progress by verifying physical replug recovery; it
was not a waiting or no-progress turn. A distinct, previously unverified primary
mobile case was checked next: ordinary screen sleep with the real shared card.
No radio change, forced device-idle mode, repeated socket reset or paid operation
was used. The one-shot foreground script used one 60-second timer, then requested
wake in its cleanup path and exited. No background sampler or persistent setting
was installed.

From 2026-09-25 14:34:27 to 14:35:34 UTC, before/asleep/after receipts retained the
same app PID, Agent process generation, reader session generation and expected
identified card. Core last_seen advanced during sleep. All three reads reported
line readiness and zero active/pending calls or media sessions. The initial power
state was unplugged/Awake, the sleep sample was Dozing, and the immediate wake
sample was Awake. PowerManager's Dozing value is not proof of Android device-idle
Doze, battery efficiency, uninterrupted APDU transfer or long-session acceptance.

Native post-wake captures showed MIUI AOD, not the application. A second ordinary
wake request did not supply an app-page screenshot. No lock bypass was attempted;
return-to-app UI acceptance is therefore not claimed. This does not contradict
the same-process, fresh-heartbeat and exact-card server receipts.

Private evidence: `android-evidence-36146095057/screen-off/result.json`, power and
service snapshots, and native captures; the three complete production readbacks
are `screen-off-v70-{before,asleep,after}.json` in the existing private
autonomous-giffgaff directory. The one-use script is private and not a repository
test framework. No source change, build, CI, deployment, commit or paid retry was
needed. R11 gains short screen-off continuity evidence only; USB recurrence,
incoming calls, natural long idle and the other cumulative gaps remain open.

### Remaining incoming-call gate and source review

The previous turn supplied new short screen-off evidence; do not repeat it as
substitute progress. Read-only review of UsbCard/ReaderHub confirms serialized
command ownership and failure-driven connection disposal. Existing evidence still
does not identify why bulk OUT began returning -1. No speculative reset, timeout
extension, AKA replay or new server behavior is justified by this review.

Reviewed the actual incoming notification, exact-line/card answer and hangup paths.
This is source review, not a real incoming-call pass. On the authorized handset,
the current Android user is 0, the installed package is v70, and that user's
POST_NOTIFICATIONS and RECORD_AUDIO grants are both true. A separate user's denied
grants in the same package dump are not this installation's test result. Scoped
private receipt: `android-evidence-36146095057/incoming-permissions.txt`.

The existing recurring paid-call authorization lists outbound test destinations,
not a call to the owner's giffgaff number. One permission question now asks whether
to place one mainland-modem-to-giffgaff incoming test, with Android answer and end
within 50 seconds, or leave incoming acceptance to the owner. No call has been
placed and no automatic redial is authorized by this question. Upon approval,
verify both exact cards and idle state and arm independently reachable sender
hangup before dialing; then retain real incoming UI and both terminal readbacks.
Do not duplicate the question or substitute a synthetic incoming event as physical
acceptance. Existing authorized outbound call/SMS evidence remains preserved.

PR #12's body was still describing v67 and the mainland call as unverified. It was
updated in place to v70/exact artifact, the later per-route call evidence, physical
replug and screen-off results, and the remaining draft/acceptance boundaries. A
readback at 2026-09-25 14:44:32 UTC matched the submitted body exactly; the PR stayed
OPEN/draft at c18c944. Private receipt: `pr12-review-body-receipt.json`. No source
commit, merge, CI or deployment occurred. The one incoming-target authorization
question remains unanswered; no repeated notification or paid operation was made.

Historical execution gate: awaiting the existing incoming-test destination decision.
This same gate has persisted through three goal turns: the permission/readiness
review, PR-body correction, and this audit. The preceding turn was progress
(corrected public delivery state), not a live-job wait. All tool sessions from
those checks completed; there is no CI, installation, call or sampler to poll.
The only tracked dirty file remains this intentionally local cursor. Mark the goal
blocked, not complete; do not resume deferred network work, merge review drafts,
repeat paid outbound calls or extend already completed short observations to keep
automatic continuations running. Resume at the existing decision when the owner
responds. All other unresolved acceptance rows remain preserved, not waived.

### Owner-assisted incoming attempt did not connect

The owner resumed and offered to place the incoming call themselves, superseding
the prior waiting gate. The exact catalog number/card was checked privately. The
production preflight at 2026-09-25 15:04:36 UTC reported the expected card ready,
with no active/pending call or media session. The owner then reported an unlocked
phone and an unreachable announcement when dialing that number. This is a failed
real incoming attempt, not completed incoming alert/answer/end acceptance.

The 15:06:59 UTC production read still showed IMS registered (200), fresh tunnel
traffic, the expected card, no active/pending call and no new history entry for
this line. Typed diagnostics show preceding IMS registration failures and recovery
at about 15:04:18 UTC. This sequence does not prove that earlier recovery caused
the owner's failure. The scoped Provider journal for 14:58-15:08 UTC was empty;
an empty journal is not evidence that no INVITE arrived. Source review confirms
early rejection can precede creation of the pending call, so absence of history
cannot distinguish carrier non-delivery from Provider pre-ringing rejection.

The unlocked handset initially showed another foreground app. Launching MDD and
opening Calls succeeded and displayed Gateway online. That later UI state is not
evidence of whether a notification was delivered during the attempt. No re-dial,
network change, service restart, production deployment or media request occurred.
One follow-up asks for the originating carrier and whether the announcement was
immediate or followed ringing, explicitly without another call. Preserve this
question; do not repeat the superseded outgoing-to-owner authorization request.

Private evidence: `owner-incoming-preflight.json` and
`owner-incoming-unreachable.json` in the existing autonomous-giffgaff evidence
directory; `android-evidence-36146095057/owner-incoming` contains actual UI captures
and the empty scoped Provider journal. Next: use the owner's call-side observation
to narrow the incoming failure, retaining the missing SIP evidence explicitly.

The owner clarified that the originating carrier was also giffgaff, with no
ringback before the unreachable announcement. This narrows the observed phase,
not the root cause. Inspection of the retained current Agent topology found one
matching card (suffix 1522) and matching IMSI, but no reader-reported MSISDN. The
number supplied to the owner came from the saved catalog, not a fresh card or
carrier readback. Neither a correct nor an obsolete catalog number is proved by
that absence. An explicit account-number check for this physical card was asked
once, without another call or account/USSD/balance access.

Source inspection confirms REGISTER advertises MMTel audio, the inbound handler
is connected, and pre-ringing rejection paths include unavailable/busy and media
negotiation failure. These are possible paths, not observed SIP responses from
this attempt; do not label the failure as codec rejection or a carrier defect.
Current next step: reconcile the owner's account-number confirmation with the
exact card before considering another assisted attempt. Keep the missing inbound
wire evidence explicit and do not change Core or deploy speculative Provider fixes.

### One owner-authorized self-SMS: explicit service-location rejection

The owner confirmed the physical card/number and explicitly authorized one
self-addressed diagnostic SMS. This supersedes the account-number confirmation
gate; do not ask the same question again. One short ASCII marker was entered in
the actual Android Messages page, with exact card/recipient and VoWiFi selection
checked in the native confirmation. An exclusive attempt receipt was written
before the single confirmation click. No API send, retry, other-line SMS, balance
query or switch change occurred.

The retained event at 2026-09-25 15:21:21 UTC has kind submitted but state failed.
The bounded 30/60/120-second readback loop ended without a matching received SMS.
Native UI showed Submission unconfirmed, not success. The helper's initial
submitted=true summary meant only that a kind=submitted event existed; it was
not a successful-submit assertion. Its output was clarified, and assessment.json
explicitly supersedes that ambiguous summary while preserving the original data.

The matching completed Provider operation was recovered by the exact request
fingerprint (message ID, recipient and body, using the source operationKind
encoding). Its failure is kind failed, code message_send_failed, layer messaging,
detail "Forbidden - Service not allowed in this location". The extraction read
bounded live database bytes and decoded the matching complete JSON; it is not an
atomic database snapshot. An earlier search by message ID alone found no record,
because the completed failure stores the fingerprint rather than that ID in its
JSON. No database write, replay or service restart was performed.

Private evidence: `android-evidence-36146095057/giffgaff-self-sms`, including the
plan, attempt, native screenshots, three history reads, exact operation evidence
and assessment.json. The one-time SMS authorization is consumed. The caller/card
details remain private. This rejection does not prove a wrong number, a particular
egress-country mismatch or the cause of the owner's failed incoming call. Next:
check the existing VoWiFi service-location/registration configuration against this
specific rejection, without automatic paid retries or general Mesh investigations.

### September 25 scoped transport investigation and UI continuation

The owner explicitly authorized continued diagnosis and a scoped Bettbox routing
experiment. The earlier blocked conclusion is superseded; it is not permission to
drop this work when a new UI issue arrives.

Saved Bettbox configuration already includes the mesh CIDR DIRECT rule. Its live
external controller is disabled; no process-name rule was injected or falsely
claimed applied, and no proxy/Agent process was restarted. A temporary exact-Core
wired route bypassed TUN for a bounded 30/60/120-second observation. WSS EOF persisted
and all three Core samples were absent. The exact temporary route was removed;
Agent and Bettbox PIDs were unchanged. This does not prove a rule failure.

A secondary route through the already authorized Mac and the same Core host's LAN
address restored diagnostic SSH access with strict existing host-key checks. A
240-second, source/port-scoped MSS experiment was applied and removed by a remote
trap. Agent samples included a connection replacement; the capture only covered
native packets from the gateway, not mesh userspace-proxied sockets. Therefore this
does not establish either MTU repair or a definitive rejection of the MTU hypothesis.
Matching published mesh CLI was hash-verified and used read-only because installed
CLI and running Core versions differ. The temporary CLI and directory were removed;
the MSS rule was independently confirmed absent. No service/config upgrade occurred.

Independent Windows certificate-pinned TLS probes failed on some paths, but a later
adjacent pair through TUN and explicit local proxy both returned HTTP 200. This
supports intermittent transport trouble, not a consistently broken process rule.
No paid operation, SIM mutation, 4G/borrowing switch, restart or product code change
was performed in this investigation. Raw configuration, host identities, captures,
hashes and cleanup receipts remain in the private `bettbox-agent-route.xhDBb1`
evidence directory; no credentials belong in the PR or this cursor.

Next batch: repair and physically audit the native controls and page workflows,
while retaining the transport and mainland-call gaps above. Only resume the guarded
paid check after exact Agent/card readiness and an independent hangup path are
established. A new UI issue never cancels an earlier unresolved owner request.

Original dirty recovery
work remains untouched in its original checkout and branches.

The native UI, login/certificate interaction, reader attention, local diagnostics
and audio-focus fixes are imported from committed `7d14084`, not the dirty pairing
candidate. Calls and SMS validate identity through existing catalog and projection
APIs. Directory search is client-side. Incoming cellular records are adapted from
the existing snapshot. Message history uses the existing pagination API.

Removed runtime dependencies on mobile directory endpoints, server call recovery,
SMS receipts and the new message sync protocol. Unknown SMS is checked using GET
history, never resubmitted. Calls have no persistent recovery owner; local status
checks and history use existing APIs. Added a native call-history entry.

Reconnect and remembered-login renewal are reapplied to the imported client.
Reader rejection and TLS identity failures stay terminal. Pause/share intent is
not enabled by renewal. Existing login/UI/intent tests were reused and their
fixtures adapted to baseline routes. New malformed-frame and renewal test candidates
are preserved privately, excluded from the commit until red/green is established.
They are not test evidence. Existing tests remain enabled.

Source review found existing cellular heartbeat timeout and Provider default guard
timeout of ten seconds. This proves cleanup code exists, not a fresh runtime or
physical hangup test. Workflow signing reuses the existing stable preview identity,
restricted to approved branches/dispatch; PR runs do not receive signing secrets.

`git diff --check` and ledger regeneration passed. No baseline changes exist under
`go-runtime`, `providers` or `webui`. Initial candidate `223f279` passed the
build and Core jobs in GitHub run `35989682776`. Both API28 and API35 ran 13
instrumentation cases: 12 passed, one failed. The shared failure was the old
DeviceTest expecting synchronous setup and the retired SharedPreferences file.
Its committed `7d14084` counterpart waits for asynchronous setup and verifies the
actual encrypted file; that missing test adaptation is now included. This changes
the stale storage expectation, not the encryption requirement.
Corrected candidate `f627204f53e74c1350a11ebff034e92176a1bee8` passed GitHub run
`35990853200`. Draft PR #11 remains open against `codex/android-v2`; it is not
merged. The Android workflow covers build, lint, unit tests, API28/API35
instrumentation, Core/agentlink race tests and existing WebUI checks. The same
head subsequently passed the existing full Go Runtime workflow `36002250550`,
including full Linux Core tests/race/vet and release validation. Neither run
covers the excluded new recovery regressions. No workflow changes or tagged
release were made for this additional validation.

The downloaded preview APK matches its SHA-256 manifest:
`458998be95dafc252a27f24870c1cd8c4cf5c941c9e4737a8525fef11fc0787c`.
Its recorded source is this exact head; the CI signature report has the expected
stable preview signer. The authorized Android 13 handset now has this exact
preview installed; there was no MDD package on it before installation. No paid
operation or production deployment was performed.

The matching Linux release archive is retained privately. Its source revision
matches this head and the Core binary matches the manifest SHA-256:
`dfa49dc900d0b336d13fd7edb505657c56418edfc09ebfc0fb2cfaf88ed1a2e9`.
The owner subsequently authorized temporary isolated Core validation and minimal
mainline integration once validation passes. This exact Core was started with an
empty store, separate credentials/certificate and loopback-only listeners. Actual
login, WSS `mobile.snapshot` and logout-induced close `4401` passed. This is Core
interface evidence, not Android recovery evidence. The bounded foreground process,
SSH tunnel, remote files and listeners were removed afterwards. Production was
not changed. The existing matching mainline ancestry was confirmed; no PR #8
pairing or durable-receipt changes are included.

The owner supplied a replacement handset address. The existing isolated ADB
server connected to that sole target; authorization and shell access succeeded.
The previous target was not operated. Target details remain private. A separate
attempt to download emulator evidence ended with GitHub API EOF, not a test
failure; those screenshots and final XML counts were not freshly inspected.

Native login and explicit certificate confirmation succeeded. Home, Calls,
Messages, Readers and Settings were each clicked and their screenshots inspected.
The unshared attached reader has an attention badge and USB/sharing prompts.
After restarting the preview app, the saved session remained; Sign in again
showed the remembered masked password and reconnected without reentry. This is
remembered-login evidence, not expired-session automatic-renewal evidence.

The configured server entry returns HTTP404 for `/v1/mobile/ws`. Calls and Messages
therefore remain offline with an explicit error, not proven usable. Reader sharing
was left off; no SIM operation, call or SMS was performed. Screenshots and the
handset result are private. Two login-return helper captures failed; a subsequent
standalone screenshot and hierarchy dump succeeded, without an asserted cause.

On resumption, the authorized handset's ADB transport became offline. TCP connected,
but target-only reconnection and a fresh isolated server both failed. A second
already-installed ADB version also failed. Trace showed outgoing CNXN and read
failure without incoming AUTH/CNXN or an explicit local permission/route error.
The OS route and proxy connection metadata show this target traverses the existing
TUN/private-subnet proxy path: request bytes were sent and no response bytes were
recorded. This narrows the fault but does not identify a root cause or prove that
the handset is offline. No route, proxy, permission, shared key or shared ADB server
was changed; temporary comparison servers were removed. Raw evidence is private.

The owner subsequently supplied a replacement mesh endpoint. The same project-owned
ADB server connected, reported `device`, and executed a read-only Android 13 shell.
The earlier transport blocker is resolved for the new endpoint; its cause on the
previous path remains undiagnosed. No previous target or shared server was changed.

The same-source CI QA APK was installed separately from the stable-signed preview.
Native login to the matching, empty isolated Core and explicit certificate trust
succeeded. With that Core suspended for 80 seconds, the app detected lost contact;
after resumption it returned online without clicks, process restart or changed
encrypted session state. Restarting only the isolated Core then invalidated its
in-memory sessions. The app returned online automatically in the same process and
persisted changed encrypted state without a new login interaction. This verifies
remembered-login renewal after server-side session loss, not waiting twelve hours
for natural idle expiry or carrier recovery. Reader sharing remained off throughout.

Private evidence: the existing task-private `core-hil.njDetI` record contains
`silence-result.json`, `restart-result.json` and the corresponding native screenshots.
Both tests used the existing CI artifacts, not a local build or a modified Core.
The temporary Core, SSH tunnel, reverse mapping and remote files were removed;
listeners were checked absent. Production and the stable preview's settings were
not changed. No SIM operation, paid call or SMS occurred.

PR #11 was retargeted to main and merged as one squash delivery:
`8d0c4c6e3fa32073b7abcdf3f9f85ccef4473c89`. It includes the PR #7 baseline,
client adaptation and validation ledger. The final source head `98abe76` differs
from tested `f627204` only in documentation. Post-merge workflow `36010202284`
passed for the exact mainline commit. One bounded status wait timed out; the second
returned success. No extra workflow was dispatched. Original recovery branches,
their dirty files and the project ADB server were preserved.

The mainline integration is complete, not the Android development and physical
acceptance objective. The owner subsequently authorized deployment of the minimal
Core support and real handset/reader validation. This supersedes the earlier
production-deployment restriction for this batch only; excluded PR #8 expansion
remains excluded. This receipt remains local pending the next coherent delivery.
Malformed-frame regression and natural idle-expiry coverage remain unverified;
the private unexecuted regression candidates remain excluded. Real card/carrier
operations, background/OEM endurance and battery qualification remain separate.

### Production and physical validation follow-up

Production Core now runs the exact merged source
`8d0c4c6e3fa32073b7abcdf3f9f85ccef4473c89`, from successful mainline workflow
`36010202284`. Release archive SHA-256:
`03f90d62cf26cb5a7336d7b14b9b8ee6a180fd8b367af03a6bf89445d9980574`.
Core binary SHA-256:
`1745733a5d47dd3b93b204f6a771d175d1367fb81539c1cec2d32bc6a5b54b08`.
The online backup endpoint rejected the existing event store as too large before
any deployment. Instead, the maintenance-guarded deployment stopped Core and its
apply helper, preserved configuration and all top-level state files in an offline
backup, then used the release installer. No backup limit or data was removed.
Backup SHA-256:
`aacce839e6a5f14633a53a3515c8a1cc8a5fbc9c00921a473dcfbd4712f62142`.
Provider binaries/processes, remote Agents and egress processes were preserved;
the owned maintenance lease was released. The catalog and notification settings
were unchanged. Deployment evidence remains in the private deployment record.

The stable-signed handset preview connected to production without a new login.
Native Calls displayed ten real catalog lines, and Messages displayed actual
received history. This establishes data access, not successful calling or sending.
The client snapshot omits displayed phone numbers and its capability explanation
expects a different readiness field; these adaptations remain outstanding.

The attached APDU-capable USB reader enumerates and USB permission succeeds, but
the first reader power-on write fails before identity, PIN or AKA. The stable
preview and same-source QA package both fail to expose a reader to Core. After
the owner physically reconnected the reader, a fresh permission grant and bounded
retest reproduced the failure. A QA debugger captured the exact exception again:
`USB write failed; outcome unknown`. This is not proof of a physical defect;
the cause of the USB transfer failure remains unresolved. Separately, ReaderHub
overwrites the USB diagnostic with an OMAPI failure, producing a misleading page.
The debugger and its forward were removed, both test sharing sessions were
stopped, and QA was force-stopped. No PIN, profile change, paid call or SMS occurred.

Current acceptance is partial. The presentation follow-up is now committed as
`a350439e12efaab3493b3c7a7fec57cfc3752e63` in draft PR #12, without changing Core.
Exact-head Android workflow `36031653657` succeeded. The first two bounded status
reads failed to retrieve status; the third returned success. No replacement
workflow was dispatched. PR creation subsequently triggers its normal checks;
their status is not inferred from the dispatch result.

The stable-signed, non-debuggable APK (version code 59) matches source/manifest and
the existing preview signer. SHA-256:
`a981d9544b7611b86f9f82f4761e8a53f31dd8a7796901ce753755164f984ef9`.
It was installed as an update without clearing app data. Native Calls and Messages
now show the catalog phone number. The unavailable route displays real blocked
layers and facts. Readers now displays USB write failure independently of OMAPI,
both in the summary and the actual USB row, instead of waiting indefinitely.
These are pre-fix/post-fix physical UI observations, not newly added unit tests.
Sharing was stopped after the bounded read-only reader test. No paid operation or
PIN/profile mutation occurred. Screenshots and XML remain private.

Next: resolve the reader transfer failure and finish real call/reader recovery
validation with existing safety guards. Independent cellular hangup must use the
call-owning session: another administrator login has a different subject and is
not a valid fallback. The native client already selects the correct cellular
`hangup` route; no route fix is needed. Empty-store recovery, green CI and merged
PR status do not substitute for that remaining acceptance.

### Production session recovery and native call attempt

The existing CI QA package from `f627204` was used for a scoped production check;
its RemoteCall, NativeAudio and CallPlan sources are unchanged in `a350439`.
This is not full acceptance of the newer stable-signed APK. Only the QA-owned
session was captured privately to provide independent subject-fenced hangup;
temporary plaintext handset files and debugger forwards were removed.

An initial debugger-assisted confirmation did not reach the intended breakpoint,
and a UI capture timed out. QA was stopped before proceeding; production readback
remained idle. No cause was established for that instrumentation failure.
The subsequent attempt ran without a debugger, with microphone permission granted
through the actual Android prompt and an independent bounded hangup guard armed
before confirmation. At the 20-second observation, Core had a media lease in phase
`ready` while the app still displayed Preparing call audio. By the guard deadline
the session was already absent; no matching call-history record existed. QA was
stopped and a later readback again found no matching session. This is an incomplete
audio-preflight attempt, not evidence of a successful carrier call or voice path.

Important distinction: cellular session phase `ready` is set when the media lease
is established, not when `canaryReady` is true. Both client and Core use the same
media-ready message names. Do not diagnose a protocol mismatch from that phase.
Input signal, playback and bidirectional canary evidence need further inspection;
do not weaken the gate, manufacture samples, or repeatedly redial. The owner was
asked about availability to provide microphone speech and whether the same USB
reader has worked with another app on this handset; replies remain pending.

On the actual production Core with its real catalog, invalidating only the QA
login caused automatic reauthentication. After one bounded observation the PID
was unchanged, encrypted state changed, Gateway online returned, no login prompt
appeared, and the native selector showed ten real lines. This verifies production
session-loss recovery, not a cellular-network handover or a natural 12-hour expiry.
Evidence is retained in the private `android-production-adaptation-36031653657`
record under `call-physical` and `recovery-physical`. QA was force-stopped afterward
and the stable preview was restored to the foreground. Reader sharing remained off.

Next remains actual reader and call acceptance, beginning with the unresolved USB
transfer and audio-canary evidence. Do not rerun the completed deployment or treat
this recovered observer session as successful SIM authentication or a paid call.

### Bounded USB comparison and remaining external evidence

One additional fixed CCID/control comparison used the existing QA package, without
PIN, AKA or profile operations. USB GET_CONFIGURATION returned one byte with value
1. CCID GetSlotStatus (0x65, slot 0) bulk OUT returned -1, like the earlier initial
IccPowerOn write. This rules out a failure specific to the power-on request, not
Android USB compatibility, firmware or the physical connection. It does not prove
a hardware defect. The debugger and owned forward were removed, sharing stopped,
and QA force-stopped. Private evidence: `usb-control/result.json`.

The reader failure has persisted across the post-reconnection, installed-candidate
and protocol-control goal turns. UI adaptation and production authentication
recovery have been completed in the meantime; neither supplies missing reader or
call acceptance. Further repeated power-on/dial attempts would not distinguish the
remaining causes. Await the already-requested same-handset reader-app comparison
and availability for a controlled microphone-speech check. Do not repeat those
questions or silently reduce acceptance to empty-store/observer-only tests.

### Owner reboot recovered the attached USB reader

After the owner rebooted the handset, the same installed stable-signed version 59
read the attached giffgaff identity (card suffix 1522). No APK or source change,
PIN submission, profile mutation or further USB reset was used. USB permission
was granted through the actual system prompt and reader sharing was explicitly
enabled. The native Readers page now reports one identified reader and no pending
USB action; the separate OMAPI access-denied condition remains visible.

Production Core received the current Android Agent attachment and exact card
identity. Its endpoint association resolves to the existing line 1 and reports
VoWiFi call/SMS readiness. The provider snapshot independently reports running,
IMS registered, voice/messaging ready, and no active/pending call. These establish
reader recovery and current registration, not a completed call or proof that this
registration freshly performed AKA through the recovered attachment. The original
bulk-OUT failure's root cause remains unproven; reboot recovery alone does not
distinguish handset USB state, firmware or physical connection causes.

Private evidence: the existing adaptation record's `after-reboot` native XML/PNG,
the production snapshot `android-production-20260925.ZjvJWQ`, and
`android-reader-audio-preflight.Mj0lm7`. The old offline QA reader observation is
not a second live owner. Stable-app sharing remains enabled for the current
reader-backed validation; QA remains stopped. The owner offered microphone
cooperation, and a single bounded native VoWiFi call is being prepared with
independent exact-call hangup. No paid call has been placed in this follow-up.

Next: coordinate the owner's speech, perform one guarded native call, and record
actual audio and terminal readback. Do not repeat deployment or reopen the
superseded request for another reader app before using this new recovery evidence.

### Native reader-backed VoWiFi call and owner listening check

The owner confirmed readiness to speak and listen. The stable preview's microphone
permission was granted through the actual Android system dialog. Exactly one
giffgaff VoWiFi call then ran from the native Calls page with an independent
exact-call hangup guard. No QA sharing, debugger or substitute audio client was
used. Native audio preflight completed before dispatch. At the bounded observation
the provider reported the exact call active and Android displayed audio connected.
The owner explicitly confirmed hearing remote speech.

The independent end request returned `accepted: true`, `code: ended`; the provider
returned to idle, durable history recorded an answered call ending after about
29.4 seconds, and Android returned to the dial pad with Call ended. A final Core
readback still identified the same stable-app reader and line association. No
redial or paid SMS occurred. Private evidence remains under the existing adaptation
record's `vowifi-after-reboot`, with the reader follow-up in
`android-reader-after-call.WFLzPE`. Temporary guard login was logged out and its
CSRF curl file removed; no debugger, background watchdog or test server remains.

Scope: successful native outbound establishment, microphone-to-server preflight,
owner-confirmed audible carrier downlink and terminal readback. This does not
independently prove carrier-side receipt of the owner's speech or fresh AKA using
the recovered reader; the provider already had an IMS registration. Incoming
calls, paid SMS, physical weak-network transitions and long-session/OEM/battery
qualification remain separate. Do not report full Android acceptance or merge
the draft solely from this one call. Existing merged Core and draft PR #12 source
remain unchanged. Next: complete scoped remaining reader/session recovery
acceptance without repeated paid calls; preserve this completed call evidence.

### Owner follow-up: call failure feedback and usable message records

The owner reported subsequent native giffgaff and mainland-modem call failures,
missing peer/own-SIM/body information, no reply action, an unusable DTMF entry,
and indistinguishable status colors. The new current batch is limited to these
client usability defects. A read-only production snapshot showed no new giffgaff
carrier call after the accepted controlled test, while the handset displayed the
generic no-call notice. The original exception was discarded by the old client;
do not retrospectively assert silence, modem failure or a stale lease as its cause.
The mainland modem readiness was available with no active cellular session, which
is not call acceptance. No new automatic call or SMS was made.

Candidate changes retain pre-dispatch API/audio reasons through cleanup, keep
canary guards and no-redial semantics, expose a digits-only in-call keypad that
sends nothing when opened, share incoming/history message presentation and exact
card-fenced reply prefill, and keep bounded encrypted sent-body/own-number receipts.
Resolved preview bodies may age out under the existing budget; unresolved payloads
must never be evicted. Existing already-purged body data is recovered for display
only from matching message/line/transport history. Submitted remains distinct from
delivered. Native status colors supplement rather than replace state text.

Failure checklist: cleanup must not erase the original failure; cancellation must
not be reclassified as carrier success; reply must not use another SIM/account;
draft replacement requires confirmation; opening the keypad must not send DTMF;
retained resolved bodies must not prevent new submissions or evict unknown payloads.
No new unverified automated tests are being claimed. Pre-fix physical/UI evidence
is retained privately; existing GitHub gates plus post-build native checks remain
required. The long cursor stays local and is excluded from the delivery commit.

Owner reaffirmation: client adaptation must prefer client-only changes. Do not
change Core to make an Android workaround convenient or expand deployment risk.
Any proven server-contract defect needs a separately explained, minimal scope.
This batch has no Core, Provider, deployment or workflow changes.

Batch `127ee51c363e1bd824bb6fe64039adc49cd970d3` is pushed to the existing draft
PR #12. Exact-head signed workflow `36042579860` succeeded. Stable preview v61
APK SHA-256: `afa3f08439c06a0b92874b62c62971aaa635a88c61fce832640094048b2a2ee1`.
The signer matches the installed preview. Artifact identity and signature have
been checked; installation and native post-fix UI checks are the current next step.

### Final client UI candidate and honest call boundary

Version 61 was installed preserving data and checked on all five native pages.
Reply on a real received message selected its original mainland SIM/card suffix,
cellular transport and peer, without sending. Readiness/error colors were visually
verified; history contained the owner's newly sent body. This exposed an omission:
the legacy local-receipt fallback skipped numbered submitted events, even when
history retained their body. The subsequent correction preserves each part label
instead of pretending missing parts form a complete original payload.

Final source is `d2de7274fb4a57ad1db352e00f89c01fa186c9c6`; signed workflow
`36044092398` passed. Stable-signed v63 APK SHA-256:
`231a7aa81ac78532ab15cc8920378fae6b40ec301a49c5a597aac6098e7c78f3`.
It is installed without clearing app data. Final native receipt readback displays
both numbers, own-card identity, numbered submission state and the retained sent
body. Raw before/after XML/PNG remains in private `android-ux-36042579860` and
`android-ux-36044092398` records. No paid SMS or call was generated in this batch.
Core is still the previously deployed `8d0c4c6`; it was not changed or restarted.

The owner reported that both failed call attempts waited about 20 seconds. This
matches the audio-preflight deadline but does not prove the missing original
exception. The current candidate preserves API/audio reasons and prompts for
microphone speech, without weakening canary checks. Do not claim that later
VoWiFi/cellular failures are fixed merely because reason display is fixed.
No new live-call keypad acceptance, carrier-side uplink evidence or fresh AKA is
claimed. Next: use the visible error from a coordinated, frequency-limited native
retry to finish the call diagnosis, staying client-only wherever possible. The
existing user authorization still limits automated paid tests to its named lines
and numbers; the mainland modem is not added to that authorization by this report.

### Follow-up readback before coordinated call diagnosis

Read-only production snapshot `android-production-20260925.vK5Eog` confirms the
unchanged Core revision, ten real lines, no unfinished call history and no active
or pending provider calls. The current Android Agent report and device projection
agree on process generation, reader session generation and exact card association;
the attached giffgaff reader is identified on Core, not merely locally. This is
current reader enrollment/report evidence, not fresh AKA or network-loss recovery.
Native v63 Home reports Gateway online, zero unresolved messages and no call
recovery; Readers shows one identified shared CCID reader. OMAPI denial remains
visible and separate from the working USB reader. Private native evidence is in
`android-ux-36044092398/followup`.

Source inspection confirms the cellular preflight requires actual signal in both
directions in addition to capture/playback counters. Its lease phase alone does
not prove readiness. The matching twenty-second client deadline cannot determine
which prerequisite failed in the old attempt. No call, SMS, network toggle, SIM
mutation or service restart was performed for this readback. Owner availability
has been requested for one guarded giffgaff diagnostic call with microphone speech;
no automated mainland-modem call is authorized. Physical network handover, live
keypad acceptance and the reported subsequent-call failures remain unverified.

Historical Core/Provider journal readback for the successful/failed-call window
is retained privately in `android-call-log-readback.EVHDxU`. It yielded eleven
Core TLS handshake errors and no attributable media/canary failure record. Those
handshake errors cannot be linked to the owner's failed call and must not be
presented as its cause. Historical logs do not recover the exception discarded
by the old APK. Further call diagnosis requires the already-requested coordinated
native retry; do not repeat journal reads, redial or change Core to fill this gap.

### Autonomous owner-authorized native checks on v63

The owner subsequently authorized autonomous giffgaff calls to its existing test
target, a mainland-modem service-number call, and one SMS to that SIM's own number.
Exact targets and one-shot guards are retained only in the private
`android-v63-authorized-scope.json` and `android-ux-36044092398` records. This
supersedes the wait for microphone availability, not the paid-operation limits.

One native attempt per line reproduced `audio_check_timeout` before carrier
dispatch, with the detailed failure visible after cleanup. Both final provider/
cellular readbacks were idle; neither produced a new carrier call-history entry.
The independent end guard was armed before each confirmation. During the mainland
attempt, Android reported the preview app owning communication focus, active
8-kHz recording/playback, unmuted microphone and non-silenced recording. AudioFlinger
had read 78,240 input samples and written 76,320 output samples at the snapshot.
These counters prove running local audio engines, not non-silent PCM, delivery over
WSS or successful canary. The common preflight failure is reproduced; its missing
signal/transport prerequisite remains unproven. No Core change or gate weakening.

One short ASCII self-SMS was submitted through the native confirmation. Production
history records submitted and received events for its exact body and original
line, about 2.365 seconds apart. Native selected-line history shows both events,
own number/card and complete body; replying to that newly received event confirms
draft replacement and prefills the exact original SIM, cellular route and peer,
with an empty body. No reply or duplicate SMS was sent. Submission remains labelled
distinct from a delivery report; the separate received event supplies this test's
arrival evidence. Private evidence: `sms-self/result.json`, `reply-result.json`,
native XML/PNG and both guarded `call-*/result.json` records.

No source, APK, server, user network switches or reader-sharing intent changed in
this batch. SMS main-flow acceptance is now real, not only an old-history readback.
Calls remain unaccepted on this candidate; further work must diagnose actual
canary evidence client-side before any additional paid retry. Physical network
handover and active-call keypad acceptance also remain outstanding. Do not wait
again for renewed authorization to the already-specified test targets.

### Current handset connectivity prerequisite

On the next owner-requested continuation, the sole authorized ADB target was no
longer present. Its existing isolated server PID and listening port were verified
unchanged. A target-limited TCP connect with a five-second connection deadline
timed out; the route still points through the existing Mesh interface. Production
Agent inventory independently had no matching Android Agent at the readback time.
Private evidence: `android-link-readback.JdlmWZ`; project ADB target record updated.
This does not identify power, Mesh, permission or application code as the cause.
No alternative device, server restart, key change or network reset was attempted.
The initial TCP utility lacked a working connect deadline and was explicitly
terminated by its exact owned PID; no diagnostic process remains running.

Source comparison confirms NativeAudio is unchanged between the existing QA
candidate and installed v63. Existing Core canary counts require non-silent PCM,
not merely Android's running recorder/player state. Neither the previous audio
sampling nor the timeout identifies the missing prerequisite. Next remains a
no-carrier client audio evidence check once the same handset connectivity returns;
do not change the server or inject fabricated microphone evidence in its absence.
The owner was asked to restore connectivity or supply the replacement address,
not to repeat call authorization. All prior successful SMS and failed-call evidence
remain valid; the new connectivity failure is a separate observation.

### Connectivity returned; bounded client diagnostic correction

The single bounded connectivity wait returned reachable on its first scheduled
probe. Target-limited ADB reconnect restored shell access; the stable app PID was
unchanged. Its observer page is online, but reader-sharing intent is still enabled
while the reader connection remains absent from Core. This is not successful
reader recovery. The mainland-modem Agent is also currently absent, so its native
call button is correctly disabled. New read-only references: `network-return`,
`android-link-return.kc0C0c` and `android-audio-preflight.uTWh08`.

A no-carrier debugger setup against the previously installed QA package confirmed
reader sharing off and armed a pre-dispatch breakpoint. No audio attempt ran because
the real selected route is unavailable. QA was stopped, its debugger exited and its
single forwarding rule removed; the stable app was restored without restarting it.

The observed reader UI defect is addressed in the current client-only batch:
transport errors are retained separately from card-scan status and rendered instead
of a generic waiting message. Bounded failure codes exclude remote arbitrary text
and credentials. Audio timeout details add real capture/queued/received/played and
signal/peak counters, not generated PCM. Failure checklist: retain errors through
cleanup; distinguish queue acceptance from server receipt; never change the gate,
call retries, credentials, sharing intent or server; do not retain audio samples.
Existing GitHub gates and physical retesting are required. No new automated test or
root-cause/repair claim is made merely from adding observable evidence.

### v65 signed delivery and current acceptance

Exact-head GitHub Android workflow `36071051226` succeeded for `0978e81`.
The installed non-debuggable v65 APK matches its artifact manifest SHA-256
`717e6ad156d7232833ea263c0f4b35c44f8841ec5e9fbdd8101dd70b4c04d088`.
Its APK signature matches the established preview signer. Installation preserved
app data. No production Core/Provider code, service, user network switch or sharing
intent changed. No new automated regression or local build/test is claimed.

One diagnostic-purpose giffgaff native attempt under the owner's standing authority
passed the real microphone canary, dispatched a carrier call, and ended with the
independent guard and native hangup. Provider and history readback confirmed
ended/idle. There is no new independent remote-voice or carrier-uplink confirmation
for v65. This success does not explain the earlier v63 timeouts: the new code
retained evidence, not an established audio root-cause fix. No further mainland
call or SMS was attempted. Private evidence:
`android-evidence-36071051226/call-giffgaff` and `native`.

Read-only recovery preflight first received an empty HTTP reply. One delayed
readback succeeded: no active call or provider drain, the exact Android Agent and
reader card were present. Match by the enrolled Agent ID, not optional host
platform metadata: the Android topology does not supply that field. Native Home
and Readers were visually checked; gateway online, sharing enabled, correct card
identified. Their normal/attention colors were visible. App PID stayed unchanged
through this readback; it is not evidence of surviving an injected interruption.

The app UID owns two established Core sockets on the handset. The server sees
rewritten Mesh endpoints, so neither socket can be safely matched to the intended
server connection from the available evidence. No connection was killed, no
firewall rule installed, and no network/service restart performed. Injection and
automatic reader reconnection remain unverified, not failed or passed by inference.
Private evidence: `android-reconnect-preflight.LTgPgI` (HTTP failure) and
`android-reconnect-preflight2.BMAxTy` (successful readback/socket scope).

### Production socket-loss recovery completed on v65

Further read-only process ownership resolved the earlier ambiguity: exactly two
handset sockets to Core belonged to the native preview UID. Exactly two server-side
proxy sockets belonged to EasyTier; the other three Core sockets were separately
accounted for by the local Linux, Windows and Mac Agents. The exact four-Agent
inventory, target card, app PID, idle state and five-socket set were rechecked by
the one-shot guard immediately before injection. It aborted on any mismatch.

One reset targeted only those two established proxy sockets. At the first bounded
30-second observation, Core had a new connection for the same Android Agent process
generation and exact card. Native Readers displayed gateway online, sharing still
enabled and the same identified SIM; app PID was unchanged. Other three Agents
retained their connection times and process generations. Call history did not
change, all calls remained idle, and no SMS or call was sent. No firewall rule,
background task, service restart, app restart or intent change was used. Private
script `android-v65-reconnect-once.cjs` refuses a second injection; result, socket
ownership, before/after JSON and visually checked UI are in
`android-evidence-36071051226/reconnect`.

This establishes production observer AND reader recovery from established TCP
connection loss. It is not radio handover, silent-packet-loss, expired-session,
OEM/battery or long-session acceptance, and does not explain the old terminal
reader loss for which the old client discarded the close reason.

Historical `android-diag-idle-recheck.yM7PaR/maintenance.json` shows the selected
line's tunnel/IMS stopped before v65 installation; the post-install provider state
shows running, fresh IKE traffic and IMS registration before the accepted call.
Current production broker configuration and Agent inventory resolve its exact
card uniquely to Android. This supports a rebuilt reader-backed runtime, not an
independently captured historical AKA/APDU exchange. Targeted provider journals
contained no exchange evidence. No additional registration or restart was forced.
Read-only references: `android-fresh-aka-preflight.mqLqZ7` and
`android-final-route-readback.nWwFdp`. Core remains exact merged `8d0c4c6`.

Unique next step: resolve the mainland modem's absent Agent attachment before its
remaining native call check. Latest projection reports `agent_target_not_found`,
`hardware_not_found`, `card_not_present`, and `cellular_voice_unavailable`; this is
a current prerequisite failure, not an explanation for its earlier audio timeout.
Do not redial, weaken readiness or modify Core to bypass it. Retain completed SMS,
giffgaff call and production socket-recovery evidence without repeating them.
Active-call keypad, incoming call, unexplained old audio timeouts and broader
weak-network/long-session acceptance remain open; the draft is not overall accepted.

### Mainland prerequisite narrowed to recurrent control-transport loss

Read-only checks on the previously recorded Windows host succeeded over SSH.
The configured Agent identity and Core URL match the deployment record. SCM and
the local authenticated control API report the existing Agent running; the process
has not been restarted. Its local validated topology contains both expected modems,
including the exact mainland card, with AT and policy ready and data disconnected.
An independent bounded TCP connect to Core succeeded. Thus Core's absent Agent/
hardware projection must not be described as a powered-off host or unplugged SIM.

Windows Application events show ongoing automatic reconnect attempts, with repeated
WSS EOF, handshake EOF and occasional TLS handshake timeouts, using bounded
30/60/120-second retry delays. This is recurrent transport loss, not proof of a
stopped retry loop or bad credentials. The selected route to Core uses Bettbox TUN;
the Agent has no machine-level HTTP proxy variables. This identifies the path,
not a demonstrated Bettbox defect. A contemporaneous server SSH journal read also
failed, so no server-side error or rejection is inferred from the missing log.
The earlier paid-test window has no retained matching Agent event; these current
errors do not establish the cause of that old media-preflight timeout.

All raw evidence is private: `android-mainland-agent-check.9aRj9u`, including
service/config identity, authenticated status/topology, route, process and event
readbacks. No restart, reinstall, credential change, paid operation, network rule,
4G/borrowing switch or server code change was performed. The owner was asked once
whether to permit a narrowly scoped Windows Bettbox-to-Core route correction or
restore that path themselves; do not ask again or silently change global routing.

Unique next step: follow the owner's route decision, then confirm stable exact
Agent/card readiness before the remaining mainland native call check. The Android
reader/observer production reset pass, SMS acceptance and giffgaff call evidence
remain complete within their recorded scopes and must not be repeated gratuitously.

### Owner correction: do not infer proxy exclusion failure

The owner states proxy exclusion should already exist and asks why connection loss
does not produce an Agent warning. A TUN route alone cannot establish which proxy
rule/action matched; the earlier route observation does not invalidate that claim.
No routing change is authorized or required by this observation. The earlier route
choice must not be treated as the sole blocker or asked again.

The single bounded production observation completed: absent, then the exact Windows
Agent/card ready, then absent again. This is recurrent loss with successful admission
in between, not permanently stopped reconnect or missing physical hardware.
Private evidence: `android-mainland-route-wait.kCwAOr/result.json`; no paid operation.
Agent events during the same window report read EOF and later handshake EOF. Core
journal also contains handshake EOF from a rewritten local peer address, which
cannot independently identify the originating Windows connection or cause.

The local `agentcontrol.Snapshot` exposes runtime lifecycle only; `running` means
local ownership/background loops started and does not include outbound WSS state.
`agenthost.Worker` logs disconnect/retry errors without updating that snapshot.
The current notification coordinator's host-alert reconciliation consumes only Core
host sections (swap, power, route, disk, temperature, systemd), not remote Agent
connection transitions. These explain the missing local connection warning and
why host-alert configuration is not evidence of remote-disconnect notification
coverage. They do not establish the transport fault's origin. Do not silently add
a Core alert subsystem to the client-focused PR to address the observation.

Unique next step: correlate the actual connection termination/handshake failure and
the connection-warning gap, leaving the owner's proxy exclusion and device switches
unchanged. Preserve completed acceptance; do not use the brief ready sample to
justify a paid call on a demonstrably unstable control route.

### Bounded passive trace; remaining live acceptance blocked

One 180-second passive Core capture retained textual TCP metadata only (no payload
dump or pcap). Its bounded timeout exited normally; a subsequent process check
found no matching capture remaining. No network rule, route, service or switch
changed. Evidence: `android-agent-transport-trace.6Au8jr`.

A complete observed TCP handshake was followed only by client sequence 1321:1484.
The server's cumulative ACK stayed at 1 with SACK 1321:1484 until its ten-second
TLS read timeout. The leading 1320 bytes were not delivered to that server TCP
stream. A second peer exhibited a similar gap. The kernel reported zero capture
drops; the server ACK/SACK independently confirms the receive gap. These are
transport observations before WSS admission, not proof of which proxy rule or
implementation caused the gap. Packet size/MTU handling is a hypothesis only.

Another established stream received a 25-byte server record and a TCP ACK, but no
client application data before a server close five seconds later; client data
arrived after closure. This is consistent with the existing bounded ping timeout,
not a decoded TLS/WebSocket proof. Contemporaneous Windows events show handshake
timeout/EOF and read EOF; Core logs confirm incomplete TLS reads for the captured
flows. Address rewriting prevents assigning every captured flow to Windows with
certainty. No fabricated per-Agent attribution is claimed.

The same unstable mainland control-link prerequisite has persisted across more than
three consecutive goal turns. Local hardware, runtime and retries are alive; the
remaining paid native check cannot safely proceed on the observed flapping route.
Previously completed production reader recovery, SMS and giffgaff call acceptance
are preserved. This goal is blocked, not complete: await recovery of the control
path or explicit scope for a reversible path-level investigation. Do not repeat
status probes, notifications or the existing permission question automatically.
Do not hide the loss with service restarts, weaker media/TLS checks or a new Core
feature in the Android PR. The local connection-warning gap remains recorded,
not silently implemented as scope expansion.
