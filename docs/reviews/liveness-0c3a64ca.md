# MDD post-merge review and VoWiFi recovery patch

## Maintainer integration notes

The submitted patch and its 34 evidence hashes were independently verified;
applying it to the recorded baseline produces the reviewed tree exactly.
Integration additionally covers an ordinary REGISTER refresh that is still
in flight when its previous lease expires, without marking a valid lease
unregistered solely because it is refreshing. Explicit disabled intent remains
visible when a stopped Provider has no health telemetry; absent measurements
are not converted into fabricated values.

The recovery workflow is callable from the normal Go release workflow, and
Linux release artifacts depend on its successful fault-chain gate. The original
local validation below remains historical evidence, not a claim that the
integrated candidate has already passed GitHub or live-carrier acceptance.

Review date: September 18, 2026. Target: `lovitus/mdd-sim-gateway`.

## Delivery status

**Implemented and tested locally; not published as a new GitHub PR.** The
GitHub actions exposed in this resumed session support reading repositories,
workflow logs and artifacts, but do not expose commit/push/PR writes. Plugin
and CLI discovery did not yield an authenticated publishing route. This is a
session capability limitation, not a claim that the repository owner lacks
permission. The deliverables include a git-format patch, a ready PR description,
a proposed GitHub Actions workflow, and local test evidence. No new-head
GitHub Actions success is claimed.

Main was checked at `0c3a64ca6fa7292b4f3c0fedef9fc2b4b993590c`, tree
`c8b94af335a3e68c3f9a075690517bd1e6f9f222`. The existing remote branch
`codex/review-liveness-0c3a64ca` remains at
`608715f30b0d6945e763bb0d482b01a4058510c1`; its three changes only prepared
offline build inputs. This patch is based on main, not those temporary workflows.

## 1. Post-merge verification

PR #1 was merged as `3ff38663dc7d3e819c5aaa0cd248a13c8fa9ce6a`.
I obtained its 20 changed filenames from GitHub and compared every file from
the previously tested PR source (`26fea7c8`) with the current main source
artifact. **All 20 matched byte-for-byte.** See `merge-verification.json` for
per-file hashes. The current main baseline's entire Git tree also matches the
GitHub tree above. No loss or unintended alteration of the PR #1 changes was
found in this comparison.

This verifies the merge result, not that every repository feature is defect-free.
The new patch deliberately extends Provider backend health/recovery while
preserving the previous call-lifetime, exact-reservation and authentication fixes.
The complete existing suites were rerun against the patched tree.

## 2. Finding ledger and counterchecks

Six defect families were addressed. Seven directly comparable pre-fix tests
cover them because cancellation and close have separate counterexamples.
The shared UI/health work below is an additional integration requirement,
not inflated into an eighth independently reproduced defect.

### L1 — P1 availability: the existing IKE liveness scheduler was never driven

Locations: `providers/vowifi-go/internal/usernet/stack.go`,
`internal/provider/provider.go`, new `internal/provider/liveness.go`.

The stack started existing rekey maintenance but did not schedule
`AdvanceIKELiveness`. With both rekey timers disabled, the presence of the
upstream liveness implementation did not make the Provider actively detect a
silent dead peer. The new loop only drives upstream decisions; it does not add
another failure policy, process watchdog, periodic restart, or independent
recovery owner.

A terminal DPD failure is published through existing health reporting rather
than closing the userspace stack from the liveness goroutine. Core retains
idle-only teardown/rebuild authority. Fresh authenticated peer IKE requests
also update the inbound observation outside the peer mutex; cached replay,
retired-SA traffic and unauthenticated input do not reset current liveness.

Countercheck: silence alone is not proof of failure; keepalive write errors or
one failed probe alone do not tear down the session. Contention with rekey is
not peer death. Zero rekey settings remain unchanged. The same-process fixture
checks these boundaries with a real packet-session scheduler and crypto.

Negative control: `TestLivenessReviewStackDrivesMaintenanceWithoutPrematureClose`.

### L2 — P2: a failed synchronous DPD probe consumed its budget twice

Locations: upstream `engine/swu/ike_liveness.go`, `packet_session.go`,
`ike_tunnel_manager.go`.

The probe completion recorded failure, then a later deadline advance could
account for the same outstanding probe again. The patch marks completion
accounted and distinguishes unsent/deferred probes from failed wire attempts.
Each DPD operation is bounded by the configured probe timeout. Parent
cancellation does not count as an unresponsive peer. A busy IKE control owner
returns a typed deferred result without reserving a message ID.

Countercheck: authenticated successful DPD still updates liveness, forged
responses do not, and the configured three-failure budget actually requires
three failed attempts in the fault-chain test.

Negative control: `TestLivenessReviewCountsEachFailedProbeOnce`.

### L3 — P1 availability: cached IMS status could outlive its registration lease

Locations: upstream `runtimehost/imsregistrar.go`, new
`runtimehost/registration_expiry.go`.

A cached registered flag could remain true after the negotiated expiry. Reads
now derive registration validity from the actual registration timestamp and
lease, preserve the original expiry evidence, and do not extend it merely by
reading status. Expired registrations enter the existing maintenance recovery
path. Failed refresh outcomes are not represented as still registered.

Countercheck: a healthy DPD session does not establish IMS registration. The
second chain scenario retains successful DPD while crossing the actual
synthetic SIP registration expiry and verifies separate unavailable IMS state.

Negative control: `TestLivenessReviewExpiredRegistrationIsNotReady`.

### L4 — P2: manual recovery and close could wait behind an uncancellable mutex

Locations: upstream `runtimehost/imsregistrar.go`, new
`runtimehost/operation_gate.go`; Provider `internal/service/upstream.go` and
`backend.go`.

The upstream operation owner used a blocking mutex, and the Provider added
another blocking recovery mutex. The patch introduces a lazy zero-value,
context-aware operation gate for the existing upstream owner. Manual recovery
uses nonblocking admission: an existing owner returns current progress, and a
future retry returns the existing schedule. A cancelled request cannot leave
an unbounded wait on that maintenance gate. Close respects its deadline and
joins maintenance without holding the operation gate that maintenance needs.

Backend/UI outcomes distinguish `ims_recovering`, `ims_retry_wait` and
`ims_registered`. The actual browser route was traced through
`providercontrol.prepareOperation` and `UseCurrent`: concurrent control calls
share a read-side route lock, and the operation result is returned directly,
so the new top-level `code` is not lost behind a response wrapper. This does
not claim to redesign every route-replacement lock in the application.

Countercheck: another REGISTER is not dispatched when the owner is busy;
ordinary stop/start/drain/call admission guards are preserved. Close may still
report a deadline error for a truly uninterruptible underlying operation;
there is no unsafe abandonment of resource ownership.

Negative controls:
`TestLivenessReviewCancelledManualRecoveryDoesNotWaitForOwner` and
`TestLivenessReviewCloseHonorsDeadlineWhileMaintenanceBusy`.

### L5 — P1 availability: persistent IMS failure was not admitted to Core recovery

Locations: `go-runtime/vowifiipc/recovery.go` and
`internal/runtimereconcile/reconciler.go`.

The recovery predicate covered failed runtime/degraded tunnel and the existing
peer P-CSCF case, but not stable blocked IMS failure or expired registration.
Those typed states now feed the existing continuous-failure budget, not a new
timer. Transient IMS faults remain the IMS maintainer's responsibility. Core
rechecks identity, current intent, maintenance, active/pending calls and the
carrier/registering barrier before dispatching idle-only recovery.

Countercheck: unknown or stale observations are not generic restart reasons;
DPD success does not mask IMS lease failure. The existing budget must elapse
before the line is rebuilt. Active calls, pending incoming calls, disabled
intent, maintenance and changed identity are not bypassed.

Negative control: `TestLivenessReviewPersistentIMSRequestsBudgetedRecovery`.

### L6 — P2: initial IMS failure and outer recovery could lose Retry-After

Locations: upstream `runtimehost/imsregistrar.go` and `types.go`; Provider
`internal/service/upstream.go`, `backend.go`, new `health.go`; Core recovery
predicate/admission.

Initial registration failure discarded the carrier holdoff. The patch carries
a separate RetryAfterUntil through the upstream result and typed failure,
remembers it across line-session cleanup/start in the same Provider process,
and prevents Core or manual recovery from bypassing it. A manual user stop
is still allowed; a subsequent session start respects the holdoff.

Countercheck: ordinary local backoff is not treated as an indefinite outer
recovery veto. Carrier holdoff and in-flight registration are the barriers.
No unbounded extension of a completed holdoff is introduced. This is not a
new persistent cross-process retry store: restarting the Provider is outside
the recovery strategy and cross-process holdoff persistence is not claimed.

Negative control: `TestLivenessReviewInitialRegisterPreservesRetryAfter`.
Additional tests cover same-process stop/start and current recovery admission.

## 3. Shared truthful observations

Optional typed `RuntimeHealth` carries authenticated inbound and DPD-success
times, missed-probe count, registration validity/expiry/status, safe failure
category, failure count, in-progress state, next attempt and carrier holdoff.
Provider layers and health are derived from one registration snapshot. Raw
private error text is not exposed.

Core publishes a read-only projection of its existing failure window and retry
schedule. It does not create a second recovery controller. The WebUI uses the
same `runtimeHealthView` and component for list and detail; device online and
VoWiFi ready/recovering/failed/off remain separate. Stale or missing typed
health is unconfirmed rather than invented success. Manual REGISTER feedback
reflects the returned outcome code rather than always announcing success.

Validation here covers adapter values, shared component wiring, stale-state
handling, and the production embedded build. It is not a claim of an actual
browser session against a live carrier line.

## 4. Validation method

The patch was reviewed against pinned main, and independently constructed
negative controls were run against a disposable checkout of that baseline.
Only the regression tests were copied to the old source. The verification
script requires ordinary test failures with the expected test names and
messages; build errors, panics, race reports and unrelated failures do not
count. All seven expected failures were observed. Downloadable JSON evidence
independently pairs each with a passing result from the patched full suites.

The fault-chain runner builds a separate Provider fixture process under the
race detector and invokes it through the real HTTP IPC API/client from the
real Core reconciler. It uses the real Backend, upstream PacketSession,
authenticated IKE exchanges, usernet scheduler and runtimehost registration
maintainer. The carrier-facing components are test-only loopback fixtures,
not production transport-policy changes.

Scenario A: stop DPD replies -> three failed probes -> unavailable tunnel ->
existing Core budget -> confirmed line cleanup -> fresh line session -> IMS
registered. Scenario B: keep authenticated DPD healthy -> synthetic SIP 503
failure -> cross the actual one-second registration expiry -> unavailable IMS
-> existing Core budget -> cleanup/rebuild -> IMS registered.

Both rekey schedules are asserted disabled. The Provider process generation
stays unchanged while the runtime start counter advances. The fixture restores
its injected fault when opening the new session; this is a controlled test
assumption, not proof that a real carrier outage ends on reconnection.

Core's injected clock advances the existing valid retry budget; it does not
change the production default. No live SIM, paid SMS or call is used.

## 5. Local results

Counts below are passed top-level test executions, excluding nested subtests.
Repetitions are not reported as distinct test definitions.

| Suite | Passed | Scope/qualifications |
|---|---:|---|
| Core full | 1,147 | 82 packages; real Xray/systemd opt-in tests skipped; chain opt-in run separately |
| Selected Core race boundaries | 197 | 9 packages |
| Provider full race | 176 | 10 packages; child-process entrypoint used by separate chain runner |
| Nested upstream SWu/IKE/runtime race | 1,369 | 14 packages; no skips |
| Audio helper race | 5 | 1 package |
| New Core stress, 20 repetitions | 60 | Top-level executions |
| New Provider stress, 20 repetitions | 120 | Top-level executions |
| New upstream stress, 20 repetitions | 200 | Top-level executions |
| Cross-process full chain | Both cases pass | Dead tunnel and healthy-DPD/failed-IMS |
| Negative controls | 7 intended failures | All paired with patched passes |

Selected and full module vet, module verification, Go formatting, shell syntax,
patch whitespace, complete WebUI tests, production build and repeated embedded
asset hash comparison passed. Core/Provider/upstream full suites were rerun
after the final initial-Retry-After propagation fix.

Environment: Linux amd64, pinned Go 1.26.3, CGO enabled, the repository's
verified module versions, native PC/SC/AMR and other local build dependencies.
Local Node is 22.16.0; the proposed workflow selects Node 24. Node-24 execution
of this new patch is not claimed. The pinned offline inputs were obtained
through existing GitHub Actions artifacts, not from a user production device.

Reproducible commands (normal CI-compatible development environment):

```bash
go -C go-runtime test -count=1 -timeout 3m ./...
go -C go-runtime test -race -count=1 ./adminauth ./agentlink ./mediaauth ./providercontrol ./internal/agentdata ./internal/agentmedia ./internal/runtimereconcile ./vowifiipc ./providerfacts
go -C providers/vowifi-go test -race -count=1 -timeout 3m ./...
GOWORK=off go -C providers/vowifi-go/upstream/vowifi-go test -race -count=1 -timeout 3m ./engine/swu/... ./runtimehost/...
go -C agent/call-audio-helper test -race -count=1 ./...
bash go-runtime/scripts/test-liveness-chain.sh
go -C go-runtime vet ./...
go -C providers/vowifi-go vet ./...
GOWORK=off go -C providers/vowifi-go/upstream/vowifi-go vet ./engine/swu/... ./runtimehost/...
(cd webui && npm ci && npm run test:all && MDD_KEEP_EMBED_SOURCE=1 npm run build:go)
```

A final harness review caught a filename overlap with the old `review_*`
negative-control copy glob. New service tests were renamed to `liveness_*`;
the five files selected by the old glob were then verified unchanged. The new
negative-control parser also permits Go module-download notices while still
rejecting unexpected output and non-test failures. All seven negative controls
were rerun after that correction.

The negative-control script requires a separate disposable checkout of exact
`0c3a64ca`; it intentionally modifies that checkout with test files. Do not
run it against a working tree containing uncommitted work.

## 6. Existing CI failure: recorded, not explained away

The pre-patch preparation branch run `35244148884` failed
`TestIMSInboundWireServerRoutesPrackAfterReliableProvisional` at
`ims_inbound_wire_test.go:333`, reporting a timeout waiting for a client request.
The preparation commit changed only a workflow, not the affected runtime.
I retained the failure events and reran the exact test 20 times under the race
detector locally: all passed. The patched full upstream suite also passed.

**The reason for the intermittent CI failure remains unresolved.** Local
passes do not prove it harmless, and I did not silently extend or remove its
timeout. No new GitHub run for the runtime patch could be initiated through
the read-only actions available in this session.

## 7. Scope limits and final counterchecks

- No new GitHub PR, push, merge, deployment or release was performed for this
  patch. A local git-format commit and workflow definition are deliverables,
  not remotely verified execution.
- No live carrier/browser-line acceptance or real SIM modification was done.
  Remote Desktop reported no device during the interrupted work. No paid
  action was used to obtain test evidence.
- The real Xray/network-namespace and real-systemd opt-in tests were not run;
  signed Windows/macOS packaging was not repeated.
- The pre-existing noncancellable-native-operation reconnect limitation is
  unchanged. A bounded Go context cannot forcibly interrupt arbitrary native
  code while safely preserving its resource ownership.
- The patch does not persist carrier holdoff across a Provider process restart.
  Its session-recovery path intentionally never restarts that process.
- Fresh valid traffic, not mere hardware presence, establishes tunnel evidence.
  DPD liveness and IMS readiness remain distinct. Silence alone is not a
  declaration of death.
- Earlier paid-operation receipts, call protections, scoped authentication and
  exact reservation lifetime fixes are retained; no cleanup authority was
  transferred to the new maintenance driver.
- The external cause of the original packet loss remains **unknown**. The
  observed wiring/accounting defects do not prove an operator, carrier, exit,
  firewall or network fault.

## References

- Repository main: https://github.com/lovitus/mdd-sim-gateway/tree/0c3a64ca6fa7292b4f3c0fedef9fc2b4b993590c
- Merged PR: https://github.com/lovitus/mdd-sim-gateway/pull/1
- Existing pre-patch CI failure: https://github.com/lovitus/mdd-sim-gateway/actions/runs/35244148884
- RFC 7296 section 2.4 (authenticated liveness / silence): https://www.rfc-editor.org/rfc/rfc7296.html#section-2.4
- RFC 3261 section 21.5.4 (503 and Retry-After): https://www.rfc-editor.org/rfc/rfc3261.html#section-21.5.4
- RFC 5626 (SIP outbound flow maintenance/recovery): https://www.rfc-editor.org/rfc/rfc5626.html
