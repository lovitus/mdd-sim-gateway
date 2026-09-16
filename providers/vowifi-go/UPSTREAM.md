# Upstream source and MDD patch

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
