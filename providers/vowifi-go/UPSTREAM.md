# Upstream source and MDD patch

## Established-call cleanup ownership

Reconciliation of old PR #9/#10 found missing F1/F3 safety fixes in the current
Provider. This batch adapts the existing implementation from PR #9 at `274983d`
(including `d005080`), without its separate Core durable-call receipts or pairing
changes. Existing source licenses, paid-operation identity, guard/backoff, DTMF
and inbound handling are retained.

The final 2xx dialog is now stored before ACK/SDP work. A media-start error returns
the original cleanup handle through both wrappers; it cannot trigger registration
recovery/redial while that handle exists. Media closes locally, but the Backend
keeps the call busy until cleanup is positively confirmed. Runtime stop, explicit
hangup and failed start all check the actual End result, not only a nil error.
Existing confirmed BYE idempotency and the original-call cleanup guard are reused.

The failure checklist is final-2xx/unusable media, invalid SDP, lost ACK write,
rejected cleanup, new-call admission during cleanup, and runtime shutdown before
confirmed termination. The real SIP/media/wrapper/Backend regression failed on
unmodified `4ca9315` in all six scenarios and passed with the scoped port under
`-race`. The synthetic SIP peer rejects two BYEs and accepts the original call's
third; only one INVITE is allowed. It never contacts a carrier. The existing media
test's nil-handle expectation was corrected because even already-confirmed cleanup
now returns an idempotent handle; its wire trace still requires exactly one BYE.
The focused one-time check used the same isolated opencore-amr 0.1.6 static library
as the SMS counterexample. Full build/race qualification remains GitHub-only.
This is not production deployment, incoming audio acceptance or Core restart recovery.

## SMS request-local failure containment

The deployed receive-path trial recorded two parsed incoming INVITEs and locally
written 488 responses. This narrows that incident to local media negotiation;
the exact rejected SDP was not captured, so no codec-specific field cause is
claimed. The owner permits deferring further incoming diagnosis. A separate
modem incoming attempt and received SMS were found in Core and the native app;
the call was missed, not answered or acoustically accepted.

The UK SMS readiness was blocked by `inbound_messaging_failed`. Its old handler
assigned every individual MESSAGE error to the whole-channel fault, including
malformed RPDU, unsupported content and a failed response write. That could keep
outgoing SMS disabled until another incoming request happened to succeed. The
original request's error was not retained; the field trigger is still unknown.

The existing SIP parser and transaction responses are unchanged. Only actual
durable publication establishes or clears the queue fault; peer input rejection
does neither. A terminal receive-loop error remains independently blocking and
cannot be erased by an in-flight successful message. The existing wire-flow owner
still closes a failed response socket and owns registration recovery. Numeric
MESSAGE status and content/persistence failure booleans use the process logger,
without content, addresses, headers, identities or raw errors. Core, Android,
carrier location, desired state and paid retry policy are unchanged.

`TestInboundRequestFailureDoesNotPoisonMessaging` exercises both real adapter
entry points, upstream MESSAGE handling, runtime readiness and one captured
outbound transport submission. On the unmodified source it fails for invalid
RPDU, unsupported content, lost durable-failure identity, erased terminal failure
and a response-write error. The same test passes after this fix. The first test
fixture omitted required Via and was rejected before MESSAGE handling; that run
is retained but is not the counterexample. The corrected fixture did not change
any response expectations. This one-time local red/green used the hash-verified
Homebrew opencore-amr 0.1.6 static library in an isolated task directory, without
a system installation. Full build/race remains the unchanged GitHub workflow.

The owner-authorized self-SMS was subsequently submitted once after the `4ca9315`
trial deployment. SIP accepted it, followed by delivery failure with RP cause 38
(network out of order); there was no received self-message. This differs from the
earlier SIP 403 and does not establish the carrier's root cause. The authorization
is consumed. This patch is not carrier SMS acceptance and does not authorize an
automatic resend or PR merge.

## Peer-initiated incoming TCP

The registered TCP connection was the only SIP receive path. A new connection to
the advertised Contact port was refused; the userspace Security-Agree installer
also installed only the UE-initiated SA pair. Thus registration success did not
prove that a peer-initiated incoming connection could reach the INVITE adapter.
This is a reproduced implementation gap, not proof of the field carrier's path.

The same flow now owns an optional userspace TCP listener. It accepts only the
connected P-CSCF address and, for Security-Agree, its negotiated client port.
It reuses the existing parser, streaming handler, response builder and write lock.
Reset/deregistration closes the listener and accepted connections; no host listener,
second registration owner, paid retry, Core change or user switch is introduced.
The installer adds the other negotiated ESP pair with independent sequence/replay
state and plaintext rejection. The local gVisor listener follows gonet.ListenTCP
with reuse-address (not reuse-port), matching the existing caller-bound dial and
allowing a Contact listener beside an established connection on the same port.

Reference: [3GPP TS 33.203, section 7.1](https://www.etsi.org/deliver/etsi_ts/133200_133299/133203/18.00.00_60/ts_133203v180000p.pdf).
The upstream main wire-flow source was checked; the fix remains a local adaptation,
not an unreviewed wholesale upstream replacement. UDP behavior is not expanded.

The two new regression tests use the real in-memory network and transport/security
implementations. Against the pre-fix source overlay, a successful TCP REGISTER is
followed by connection refusal, and the other valid ESP pair times out. Both pass
with the fix, including ringing response and connection closure. This one-time
red/green check selected only the registrar/security files; an earlier package
attempt failed because the local AMR development library is absent and is not a
test result. The existing full GitHub workflow remains the build/race gate.
The exact `494cf899` GitHub workflow subsequently passed and that artifact was
trial-deployed to the one affected line. The later 488 observations above prove
parsed incoming arrival, not a successfully ringing or answered call. The separate
SMS rejection remains unaccepted.

## Incoming pre-ringing diagnostics

A real incoming call failed before ringback, with no pending call or call-history
entry. Those absences do not prove carrier non-delivery: SIP validation and local
availability/SDP checks can reject INVITE before pending state exists.

The MDD service adapter now logs receipt of parsed INVITEs, numeric response
status and streaming response-write failure through the existing process logger.
A process-local sequence correlates the records without carrier Call-ID, phone
numbers, URI, SDP, headers or raw error strings. Non-streaming results are explicitly
`response_prepared`, not delivered; a successful streaming write is not a carrier
acknowledgement. `failed=false` describes handler/write completion, not call success:
a locally rejected call can have a successfully written 4xx response.

This does not alter SIP responses, availability checks, call ownership, routing or
recovery. It does not capture parser failures before the adapter, so an absent
receipt still is not proof of carrier fault. Repeated INVITEs receive separate
local sequence numbers; no packet-level deduplication or wire capture is claimed.
The incident remains unresolved until exact-version runtime evidence distinguishes
non-arrival at this boundary from local rejection. Existing inbound/IMS tests are
retained; no new red/green regression or real incoming acceptance is claimed.

## SMS registered network context

A real single-part SMS returned `Forbidden - Service not allowed in this location`.
That response is retained as failed, not successful submission. Source inspection
found that REGISTER carries the configured profile's access/visited network
headers, while `IMSSMSTransport` constructed MESSAGE dialogs without them. The
shared dialog builder consequently used its bare `IEEE-802.11` fallback. It does
not inherit these fields automatically from `DialogRequestConfig.Profile`.

The local adaptation copies those two existing profile fields into the SMS dialog.
It uses the same registered context, not a fabricated country or a new location
policy. Empty values retain the existing builder defaults. Authentication, route
sets, transport ownership, redirect handling, paid-operation identity and retry
policy are unchanged. No registration, SIM, Core or user configuration is changed.

This is a concrete propagation correction, not proof that the carrier's rejection
was caused by this omission. It also does not explain or repair the separately
reported incoming-call failure. Existing messaging/IMS suites cover the wire path;
no new red/green regression result or post-fix carrier acceptance is claimed.
The one authorized self-SMS must not be repeated automatically for validation.

## Outbound DTMF media ownership

An actual native outbound call reached `SendDTMF`, but the carrier rejected the
SIP INFO fallback with 405. The MDD PCM bridge owns that call's RTP socket; the
upstream dialog relay is absent, so querying its RTP sender cannot send an event.

The outbound wrapper now offers 8 kHz telephone-event alongside its existing
audio codec and honors the answer's payload and event set. It reuses the pinned
upstream `BuildRTPDTMFSequence`, including terminal event repetitions, on the
existing userspace media socket. Packet writes share the audio SSRC and sequence
space; the event timestamp stays fixed while microphone audio continues. No new
SIP stack, host-network fallback, audio synthesizer or dependency is introduced.
The wire contract is [RFC 4733](https://www.rfc-editor.org/rfc/rfc4733.html).

Failure boundaries: no accepted event payload means the existing fallback stays;
a failed or partially sent RTP event must not fall back to INFO and duplicate a
digit. Cancellation and bridge shutdown stop pending event packets. This change
does not modify inbound B2BUA DTMF, call ownership, Core authentication or recovery.
CI covers the existing media/IMS/protocol suites. The pre-fix physical error is
retained separately; new carrier RTP-DTMF acceptance is still required before a
production success claim. No new automated red/green result is claimed here.

## Existing Transport Patches

IKE diagnostic datagrams include late, duplicate and rejected candidates.
They are not mutually exclusive successful outcomes of the sent requests.
The IPC contract preserves them unchanged and only bounds timed-out exchanges
by requests sent; duplicate traffic must not disable status reporting or Core
recovery. Regression cases retain the real September 20 counters (10/5/6 and
9/5/5), plus the existing wire test's three requests and nine datagrams.

The September 18 liveness integration also reports an ordinary in-flight
REGISTER refresh as owned maintenance, including when its prior lease expires.
This does not invalidate a still-valid registration merely because a refresh
is running. Success and cancellation release that observation; local retry
backoff is not reported as an active network transaction.

`upstream/vowifi-go` is a complete tracked-source snapshot of
`github.com/boa-z/vowifi-go` at commit
`1e9c6e6adbfcd9667695149d5ecb0f71cd062f07` (pseudo-version
`v0.0.0-20260709161034-1e9c6e6adbfc`). On 2026-08-30, the repository HEAD
reported by `git ls-remote` was the same commit.

MDD keeps this source local because the reviewed upstream API hard-coded
`net.Dialer` inside its SIP flow. The local patch is deliberately limited to:

- optional standard `DialContext` and local-bind `DialContextLocal` seams on
  wire REGISTER, request and shared flow transports;
- propagation of that seam from `WireIMSRegistrar`;
- use of the same seam for DNS queries, including prepared P-CSCF candidates;
- TLS wrapping and handshake after a caller-provided raw TCP dial;
- an optional transport-owned Security-Agree installer that receives the
  actual connected endpoints before the flow switches to the protected ports;
- null encryption and HMAC-MD5-96 support in the upstream ESP codec, alongside
  its existing AES-CBC and HMAC-SHA1-96 support;
- the proven MMTel INVITE feature tags on `Supported` and `Contact`, without
  changing dialog ownership or recovery behavior.
- cancellation of a context-aborted pending INVITE through the upstream's
  existing RFC SIP CANCEL transaction, with an explicit confirmed/unconfirmed
  result so MDD never reports an uncertain paid call as ended.
- serialization of each refresh/recovery/Security-Agree registration episode,
  so a concurrent refresh cannot reinstall a stale protected transport and a
  recovering registration is never published as ready, with a bounded default
  exponential retry when the carrier did not provide a longer cooldown;
- a live snapshot callback on the initial registration result, so Provider
  readiness follows maintenance recovery instead of retaining the first 200;
- RFC 3262 `Require: 100rel` forwarding only for numbered 101--199 responses,
  preserving PRACK without allowing arbitrary protected response headers;
- abortive close only on explicitly caller-bound IMS TCP flows, allowing an
  immediately replaced Security-Agree generation to reuse its negotiated
  local/remote tuple instead of inheriting gVisor TCP TIME-WAIT;
- RFC 2409 MODP group 2 support used only by MDD's bounded IKE compatibility
  retry after an ePDG rejects the modern group 14 proposal; it is never the
  default and is not selected by MCC/MNC hard-coding.
- a committed CHILD-SA SPI observer on `PacketSession`, so the MDD peer-IKE
responder matches DELETE against the installed child rather than a candidate or
  a retired child. The observer receives copied SPI identifiers, not key material.
- opt-in transactional CHILD rekey: PFS through the existing DH implementation,
  exact-wire bounded retransmission, atomic packet-SA installation, authenticated
  old-SA DELETE validation, and a five-second old-inbound receive grace. Unset
  options retain the upstream adapter behavior; MDD does not enable a disabled
  rekey schedule merely by supplying the transaction hooks.

When these seams are nil, the original host-network and Security-Agree behavior
is unchanged.
MDD's `internal/ims` wrapper always supplies the SWu in-memory stack and rejects
custom transports, resolvers, local binding and security-plan installers whose
network provenance cannot be proved. This is fail-closed; it does not fall back
to the host network.

The upstream source remains AGPL-3.0 and retains its original license and
notices. Do not replace this snapshot without reviewing and replaying the small
userspace-dial patch against the new exact commit.

MDD outer-UDP evidence records completed writes, received response datagrams,
and response-wait timeouts in the wrapper, without modifying upstream IKE
algorithms. These are not retransmit or authentication-success counters.
Failed startup retains an immutable copy in the optional IPC runtime evidence;
new startup clears it. Core must accept the new fields before this Provider is
upgraded. Missing evidence from an older Provider remains unknown.

The shared outer-UDP adapter matches response SPI, exchange type, Message ID
and request/response roles before handing a datagram to upstream IKE parsing
([RFC 7296 sections 2.1-2.2](https://www.rfc-editor.org/rfc/rfc7296.html#section-2.2)).
It reuses upstream `ikev2.ParseHeader` and the identity checks in `auth.go`,
and carries forward the duplicate-response guard from the retired MDD
`ec620942:engine/swu_ike.py:_accept_create_child_response`. This does not replace
upstream cryptographic, Notify, COOKIE or INVALID_KE validation, and it does not
repeat SIM AKA on another endpoint after selection. Transport evidence counts
received IKE candidate datagrams, including ignored mismatches, not successful
authentication. On 2026-09-16, the upstream HEAD was still `1e9c6e6adbfc`.

The authenticated peer-request path reuses upstream IKE protection and
INFORMATIONAL parsing/planning. It adapts the original MDD `ec620942` functions
`handle_INFORMATIONAL_request`, `handle_pcscf_restoration`,
`encode_device_identity_notification_data`, and its 16-response replay cache.
DPD and COOKIE2 replies share the existing NAT-T socket without blocking an
outbound transaction. Exact retransmissions receive the same encrypted bytes.
DELETE acknowledges the paired installed SPI before invalidating the old
tunnel. Unknown/retired child deletes cannot take down the current child.
P-CSCF restoration echoes zero-length CFG_REPLY attributes, then applies the
authenticated addresses as generation-fenced session observations for the next
idle-only recovery; the live ESP tunnel and active call are not interrupted.
Core and Provider share the explicit IMS rebind predicate, without attributing
this event to tunnel or country-exit failure. The restoration
it does not rewrite desired configuration or use the host network. Configured
IMEI/IMEISV uses the legacy BCD format; absent IMEISV derives configured SVN 00
from the configured IMEI, never an invented hardware readback or global identity.
Peer-initiated CREATE_CHILD_SA follows the original MDD default classification:
additional bearers receive NO_ADDITIONAL_SAS, unknown ESP rekey SPIs receive
INVALID_SPI, and known ESP/IKE rekey requests receive NO_PROPOSAL_CHOSEN.
The existing SA is untouched. The original peer ESP in-place acceptance branch
was experimental and disabled by default; peer IKE in-place acceptance was not
implemented. Do not describe either as a fully enabled legacy capability.
Fragmented peer requests and
network-supplied recovery backoff remain separate compatibility boundaries.

MDD's proactive CHILD transaction adapts `ec620942:engine/swu_ike.py` functions
`state_ue_rekey_child`, `_install_child_workers`, `_start_child_delete`,
`_accept_child_delete_response`, and `_child_delete_tick`. It reuses upstream
DH, PRF+, SA validation and ESP implementations. Explicit refusal keeps the
existing SA; an unanswered or invalid transaction after submission is not
reported as healthy. DELETE is sent only after new inbound/outbound SAs are
installed, and the old inbound replay window is retained through confirmation
and the five-second grace. The independent proactive IKE-SA replacement timer
is not implied by this CHILD transaction implementation.

Proactive IKE replacement adapts the original `state_ue_create_sa` and
`generate_new_ike_keying_material` path using upstream DH, PRF and proposal
validation. The old IKE SA authenticates CREATE_CHILD_SA and its own DELETE;
new SPIs/keys reset the request counters while existing CHILD SAs are inherited.
The peer responder keeps an old-key teardown window; an old IKE DELETE must
not close the new association or replay configuration side effects.
CHILD and IKE maintenance share one scheduler and serialized control ownership.
The independent IKE period is durable configuration, exposed alongside CHILD
in the settings page. Absent/zero remains disabled on upgrade. The retired
implementation's 600-minute IKE default is an available explicit setting, not
permission to silently enable a new timer on existing production lines.

Initial IKE_AUTH uses the same bounded exact-wire retransmission wrapper as
rekey. Retries resend the already protected packet; they do not call the SIM
again or regenerate EAP answers. The full authenticated test drops the first
response at every AUTH round and verifies four completed exchanges, eight wire
attempts and exactly one SIM AKA call. This is loss tolerance, not evidence
that a historical carrier timeout was definitely packet loss.

CI directly race-tests and vets the nested module's `engine/swu/...` and
`runtimehost/...`; parent-module tests alone do not execute these suites.
The unoffered ESP key-length regression selects AES-192, since MDD's existing
offer includes both AES-128 and AES-256. Production selection checks are unchanged.

## MDD Operation Ownership

The service wrapper keeps paid SMS receipts in `paid-message-operations-v1`
inside its existing Bolt database, independently of process-lifecycle records.
Each operation is bound to the line, Provider, configured SIM and request
fingerprint; execution generation is retained as metadata. Reopening the store
must not authorize another send. Pending receipts remain outcome-unknown.
Legacy SMS records lack a reliable SIM binding, so their operation IDs become
unknown tombstones; the original records are retained for reconciliation.
Do not retry them with fresh operation IDs to bypass this protection.

Manual registration owns an in-flight slot until completion. Drain, stop and
new paid operations cannot race it. Failed cleanup retains the previous runtime
until local release is confirmed; a later start cannot overwrite that owner.
These changes are in the MDD service wrapper, not the upstream protocol stack.

## Liveness and registration recovery integration (review of 0c3a64ca)

The Provider's existing usernet session now drives the upstream
`AdvanceIKELiveness` scheduler independently of CHILD/IKE rekey maintenance.
Zero rekey periods remain disabled. Silence schedules a bounded authenticated
DPD exchange; it does not by itself establish peer death. A failed synchronous
probe consumes its failure budget once. Contention with another IKE control
exchange defers an unsent probe without consuming a message ID or failure.
Fresh authenticated peer requests also update the inbound observation; cached,
retired-SA and unauthenticated requests do not.

Liveness failure is reported through the existing Provider health path. It does
not close the stack from the maintenance worker. The existing Core reconciler
owns budgeted, identity-fenced, idle-only line-session cleanup and rebuilding;
no Agent/Core/Provider process restart policy is introduced. Existing intent,
maintenance, active-call, pending-incoming-call and release-confirmation guards
remain authoritative.

Registration status expires at the negotiated lease deadline. Recovery and
close use a context-aware operation gate. Concurrent manual recovery reports
current progress or a pending retry rather than queueing behind maintenance or
claiming registration succeeded. A carrier Retry-After deadline is separate
from ordinary local backoff and survives initial failure and session stop/start
within the same Provider process. It is not a new durable cross-process store.
Core cannot use its outer retry budget to bypass that deadline.

Optional typed health observations expose authenticated inbound/DPD evidence,
registration expiry, safe failure categories and the existing retry schedule.
The device list and line detail share one projection; hardware online is not
VoWiFi readiness. This is observability, not a second watchdog.

The regression script `go-runtime/scripts/test-liveness-negative.sh` checks
seven exact pre-fix failures. `go-runtime/scripts/test-liveness-chain.sh` runs
real Core reconciliation against a separate Provider fixture over loopback IPC,
with an authenticated IKE peer and a synthetic SIP registrar. It covers both
packet loss and IMS lease expiry with healthy DPD, without real SIM operations
or paid traffic. These simulations do not identify the external cause of the
original packet interruption or replace real-carrier acceptance testing.
