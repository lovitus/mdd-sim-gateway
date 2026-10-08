# Branch disposition and integration record

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
