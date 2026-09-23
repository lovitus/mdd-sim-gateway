# Upstream source and MDD patch

The September 22 Android recovery batch adds an identity-checked outbound
carrier-BYE observation. It reuses the existing upstream SIP header/dialog
parsers and requires the exact Call-ID, local tag, remote tag and BYE CSeq.
The final dialog is published before ACK is externally observable and is not
reinserted afterward; a deterministic ACK callback test injects the racing BYE.
The MDD wrapper consumes that signal to close media and retain a scoped terminal
receipt in its existing operation store. Media EOF and a missing active-call
snapshot remain insufficient termination evidence. Confirmed startup cleanup
uses the same receipt boundary; an unaccepted cleanup is not marked terminal.
`git ls-remote` on 2026-09-22 still returned upstream HEAD `1e9c6e6adbfc`.

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

### 2026-09-23 review repair candidate

The voicehost outbound result now distinguishes an established final-2xx dialog
from media readiness and a protocol-confirmed final rejection. Final dialog tags
are stored before fallible ACK/SDP handling; media/runtime wrappers retain exact
cleanup handles through errors. This feeds the existing Provider cleanup owner
and precise terminal receipts, not a second dialer or recovery watchdog. Added
synthetic SIP/Backend regressions preserve one INVITE through rejected BYE cleanup
and durable rejection lookup. Validation status is tracked in the repair PR;
physical carrier acceptance is not inferred from these fixtures.
