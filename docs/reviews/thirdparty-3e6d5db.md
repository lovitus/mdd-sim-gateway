> Scope correction (2026-09-20): the historical review below is retained as an audit record, not the current product backlog. The owner's [latest decisions](../decisions/2026-09-20-current-scope.md) accept physical eSIM deletion/retained notifications/confirmed manual replay, defer Android, exclude notarization/.p8 this round, and preserve prior field evidence. Conflicting open-item conclusions below are superseded; use [the corrected ledger](../status/README.md).

# Independent disposition of the third-party rewrite report

Reviewed baseline: `3e6d5db7657e468eedc0c31316bb97c04b2b599b`, complete tree
`f0f6b038519bdce6520ea805c4c77c5aecd0535a`. Overall tracker: GitHub issue #3.
This is a repository/source and isolated-test review, not fresh live hardware or carrier acceptance.
Final head, CI runs and retained evidence are recorded in the linked PR/issue rather than fabricated before execution.

## Confirmed and corrected repository findings

**R1 — P2: status/documentation drift.** Multiple active documents delegated current acceptance to an ignored,
machine-local progress note. Deployment instructions contradicted delivered Linux support, retained retired VPCD/Asterisk
paths, and incorrectly said there was no macOS LaunchAgent installer. Development guidance still prescribed Docker builds
and omitted native codec/PCSC/ALSA dependencies. README advertised undelivered Android support and unqualified architectures.
These are real onboarding/operational risks, but the supplied report's blanket P1 label is not evidence of a runtime outage.

The versioned `docs/status/acceptance.json` now preserves and individually classifies all 59 general and four macOS unchecked
criteria, with exact original text, rationale, source/test references and separate implementation/acceptance fields. The old
documents are hash-preserved under docs/archive/2026-09-20 and explicitly non-authoritative. Root TODO summaries are generated.
Repository tests reject missing criteria, invented hardware acceptance without required evidence metadata, stale generated
summaries, private-note references in active docs, and restoration of retired resources. These checks establish consistency,
not the physical truth of a field report. Root AGENTS.md is now a short tracked instruction file, not a copy of unknown local rules.

**R2 — P2: unmounted UI and tests of retired modules.** A Babel AST graph from src/main.jsx found 83 JS/JSX modules,
49 reachable and 34 unreachable, containing 6,355 physical lines (about 36.7% of JS/JSX lines). The supplied 35/6,480 counts
are not reproduced on this baseline. `src/api.js` is definitely live through the adapter/coordinator and must not be removed.
There were ten identified test-only references to unreachable source across eight test files; simple filename grep also counts
comments and assertions that a retired module stays absent, so it is not a reliable dead-test measurement.

The cleanup removes the old V1 shell/views and unused second call/media owners, ports useful safety assertions to mounted
modules, and wires the existing tested toast helper instead of maintaining a separate identical timer. Only two exclusively
retired-helper test files are removed. Every remaining dialog invocation is still AST-checked for await and forbidden native
browser dialogs; the obsolete >=80 call-site count is replaced by scanning the complete production graph, not by dropping the
safety checks. SMS identity is checked in the actual mounted sender and adapter, including persistence before submission.
The final module graph has 49 reachable modules and no orphan/test-only source references.

**R3 — P2: an actual user-visible locale bug found during verification.** The mounted main entry provided mdd/i18n.jsx,
but global Go call controls imported the different root i18n.jsx context. Rendering with Chinese selected therefore produced
`en:Cancel`, not `zh:取消`. A real React server-rendered context test reproduces the wrong value on the pinned original source;
changing the consumer to the mounted context fixes it. The root duplicate context is removed, and missing mounted Reject/Hang up
translations are added. Tests check Chinese and English Cancel/Unknown/Answer/Reject/Hang up. This is more than comparing two dictionaries.
The obsolete voice-self-test success translation was not copied back: current audio readiness must remain independently proved.

**R4 — P2: orphan legacy source/data and misleading dependency inventory.** There are 20 Jinja templates on this baseline,
not 24. Unused engine Asterisk patches/templates and old Control resources are removed after checking actual consumers.
The country JSON and APN XML have byte-identical live Go embedded copies; both are retained and hash-checked. AOSP textproto has
no current consumer. Removal/provenance manifests record paths and original hashes; old code remains in pinned Git history.
Historical notices and the Asterisk license text are preserved, while current notices distinguish retired components from actual
release dependencies. MIT lineage, AGPL Provider source/notice obligations, native-helper and Windows driver notices remain.
Process separation alone is not asserted as license-compliance proof.

**R5 — P2: missing automated review scope.** Normal CI now validates the ledger and its negative tests, parses production
reachability, renders the shared language context, and runs the entire Linux go-runtime module under the race detector. Existing
selected-boundary, Provider/upstream, liveness, installation and release gates remain. The new baseline gate reproduces the exact
original orphan graph and language assertion using tests only, not patched production source. Compilation/import failures do not
count as reproducing the language defect. Documentation/tools changes select the normal workflow, not only Go code changes.

## Claims corrected or not established

| Report claim | Independent disposition |
|---|---|
| A 324-line root AGENTS.md proves conflicting repository rules | Root AGENTS.md was not tracked. Its alleged machine-local content and private progress bytes cannot be audited from main. New tracked rules point to the ledger; private notes are not copied. |
| Extra review branches mean product fixes are unmerged | PR #2 was merged. Two remaining branches retain audit-only workflows/negative controls, not missing production patches. Branches are classified in docs/branches.md; no unmerged evidence is deleted to manufacture zero divergence. |
| Reviewer's local main is three commits behind | Machine-local state cannot be established from GitHub remote refs. This task uses the pinned current remote tree. |
| Linux isolation is absent | nftables/device-group/cgroup/socket-mark isolation is implemented and wired into Linux data ownership. The literal netns plan is not implemented, and the complete HIL no-leak matrix is not established. Both facts remain distinct. |
| Session-level CallCoordinator is missing | mdd/App.jsx already mounts one root Go session coordinator. Removing unmounted copies prevents a second owner; it is not delivery of a previously absent coordinator. |
| TURN is a missing current runtime dependency | That task targeted retired Asterisk/WebRTC; current browser media is same-origin WSS. Proxy/NAT/IPv6 live acceptance remains separate and is not claimed complete. |
| Recording is only an untested implementation | The mounted call UI has no delivered recording feature. Removing a retired stub does not deliver recording; it remains an explicit product gap. |
| Empty directories need Git cleanup | Git tracks files, not empty directories. Local empty-directory residue does not mean unmerged runtime code. |
| MM stderr is a current reproducible production flaw | Historical diagnostic evidence was lost; current Linux MM uses private D-Bus. The actual forced-close cause and old topology_invalid field remain unresolved, not newly reproduced bugs in this pass. |
| All race tests passing proves no concurrency defect | It tests executed interleavings for data races; ordering/identity bugs can exist without a Go data race. Prior deterministic ordering tests are retained. |
| Five failures per 60 seconds describes the full login policy | The code retains attempts over 15 minutes, throttles after five with a 60-second last-failure cooldown, and separately bounds peers and concurrent derivations. The shorthand omitted important behavior. |
| File size, panic/skip counts, commit volume prove defects | They are review leads/maintenance measures, not standalone functional findings. No broad ownership refactor or removal of valid skips/panics was performed merely to lower counts. |

## Reclassified acceptance rather than false closure

The 63 inherited criteria comprise 31 implemented, 14 partial, seven needing verification, six superseded designs and five
standing policies. Independently, acceptance is 18 automated-contract, 31 pending-hardware, three pending-product and 11
not-applicable/superseded-policy entries. These are mixed original criteria, not 63 independent absent features.

Required Android unified-protocol support, recording, custom eSIM deletion (explicitly last and interactive), remaining installer
privilege/event-wait work, universal/notarized packaging and cross-device/platform/HIL acceptance are not completed by this PR.
macOS modem-disabled defaults remain unchanged. The indistinguishable same-SIM reinsertion window and historical transport causes
remain explicit uncertainty. No live SIM write, paid SMS/call or production process operation is performed.

## Reproducibility and evidence boundaries

Baseline source came from the retained Actions archive and matched the full Git tree. The baseline npm-ci dependency audit used
Node 24 in run 35494074103; local checks used Node 22.16.0 with that exact lockfile dependency tree. Final Node-24 PR/main jobs are
the authoritative build/embedding check. Local Go 1.23 cannot execute the required Go 1.26 module, so new Go race claims require
actual GitHub-hosted execution and must not be inferred from local linting or the third party's Mac report.

The new source-graph fixtures cover imports/reexports/literal dynamic modules/worklets, comment false positives, orphan/test-only
modules, missing imports and unsupported dynamic loaders. Unsupported TypeScript/alias loading fails explicitly until graph support
is added. Ledger tests use disposable copies; no production notes or user files are modified. Final generated UI assets are checked
against the reviewed tree, and only the tested cleanup branch is proposed for integration. Overall issue #3 stays open for genuine
unfinished product/field work even after repository corrections are merged.
