# MDD vowifi-go provider

This is a separate AGPL Go module pinned to one reviewed `vowifi-go` commit.
It is not imported by `go-runtime` and it is not deployed yet.

The implemented slice adapts the upstream IKE/SWu manager to a mandatory
userspace `PacketTunnelReadSession`. It rejects kernel mode, TUN-only sessions,
incomplete tunnels and canceled opens, closing rejected upstream resources with
a bounded timeout. Packet and DNS slices are copied across the adapter boundary.
SIM AKA is injected through the upstream `AKAProvider`; reusable SIM secrets do
not enter Core.

The SWu packet session now feeds the MIT WireGuard-Go `tun/netstack` adapter,
which embeds gVisor TCP/IP entirely in memory. MDD runs two bounded packet
pumps and exposes TCP, UDP and DNS through Go `net.Conn`/`net.PacketConn`
interfaces. Packet-pump failures stop this provider stack and remain visible;
they do not restart a process or container. Tests connect two in-memory stacks
and complete real TCP, UDP and DNS exchanges without an OS interface.

The module now carries a complete source snapshot of the pinned upstream and a
narrow optional `DialContext` patch. `internal/ims` forces upstream SIP and DNS
through the in-memory SWu stack and rejects alternate network transports. A
wire-level fake P-CSCF test resolves the server over userspace DNS, completes
REGISTER, and observes the deregistration REGISTER with `Expires: 0`. The
upstream default remains unchanged when no dialer is injected.

The registered flow also drives the upstream dialog state machine. When a
P-CSCF requests IMS Security-Agree over UDP, `internal/imssec` installs a
port- and address-selected ESP transport pair directly in the SWu packet pump.
It supports the 3GPP null/AES-CBC encryption and HMAC-SHA1-96/HMAC-MD5-96
combinations without a host TUN, route, raw socket or second IKE control plane.
A linked wire test proves the initial plaintext REGISTER, protected challenge
response, protected deregistration, selector enforcement, authentication and
replay rejection. A separate dialog test observes REGISTER, INVITE, ACK, BYE
and deregistration in order. This is signalling and physical hangup evidence
only; it is not operator or media health.

`internal/media` now reserves IMS RTP/RTCP ports on that same in-memory stack
and bridges static PCMU/PCMA to MDD's existing 8 kHz, 20 ms, 320-byte s16le
browser PCM frames. The remote literal-IP endpoints may be applied after the
SIP SDP answer without reopening the offered local ports. A linked-stack test
proves bidirectional non-silent samples, RTCP sender reports, bounded queue
pressure, cancellation and no RTP after media Close. Media loss never owns
call hangup or process recovery.

`internal/ims.StartMediaCall` now binds that Bridge to one outbound SIP dialog.
It advertises already-reserved userspace ports, applies a validated PCMU/PCMA
answer, and closes media after BYE without hiding a BYE failure. An accepted
dialog with unusable SDP receives a separate bounded BYE. Failed BYE remains
retryable; local media closure is never reported as remote hangup or billing
completion. A linked fake P-CSCF/RTP test proves bidirectional non-silent media
and that no RTP is emitted after a successful BYE.

The `cmd/mdd-vowifi` Go executable now keeps the packet session, userspace IP
stack and IMS registration in this module. It reads a private strict JSON
configuration, persists mutating-operation idempotency in bbolt, and serves only the
provider-neutral authenticated loopback IPC. Its Agent AKA client also uses an
authenticated literal-loopback Core broker, which forwards the high-level
operation through Core's existing public Agent WSS. Neither local IPC is a new
public deployment port. Decrypted inner IP packets are not tunneled through
Core and no host TUN or route is created.

The provider configuration contains only the stable UICC ICCID, not an Agent
name or process/insertion generation. For every AKA challenge Core resolves
that ICCID against the current typed Agent topology and forwards it with the
exact process and card-session generation. Reinsertions and moving a card to
another Agent therefore do not require rewriting or restarting the provider;
no match is `card_offline`, and multiple live matches fail closed as
`card_identity_ambiguous`.

After registering its route, the executable reports a complete typed runtime,
tunnel, IMS, IMS-voice and messaging snapshot to Core's authenticated loopback
facts path. It reports immediately and on the same bounded refresh cadence as
route registration. A later reporting failure does not stop or restart the
provider; Core expires the facts by server receive time. Route registration
itself still conveys no health.

Current tests use fake SIM, tunnel and P-CSCF sessions only. They make no
host-network connection, APDU request, paid call or message. IMS Security-Agree
over TCP/TLS, IPv6 extension-header selectors, inbound voice/re-INVITE flows,
SRTP and real operator validation remain unimplemented. Outbound SMS uses the
registered IMS transport through the idempotent local operation API. Inbound
SMS and delivery reports use the same long-lived protected SIP flow, a durable
provider outbox and Core message store; linked fixtures do not substitute for
carrier validation.

The executable now also terminates Core's authenticated same-host media relay
at `/v1/media/{session}`. The Core relay preserves WebSocket message boundaries
without inspecting content. A new provider session first runs a no-charge
protocol-v1 PCM loopback: two non-silent 320-byte capture/playback frames plus browser
evidence are required before it becomes ready. This proves the browser WSS
transport only.

The service backend now binds only that exact ready and connected session to
`StartMediaCall`. Start/end idempotency is persisted before side effects, one
line has at most one call owner, and a call accepted by IMS but not attachable
to the browser is ended immediately. Live PCM then bypasses Core parsing and
flows between the browser session and the userspace RTP bridge. A rotated
resume ticket permits reconnection without replacing the call owner.

The 10-second call guard reuses only exact call identity and browser
application activity; registration, tunnel, process, and line health are not
inputs. A disconnect or absence of PCM/evidence beyond the bound sends BYE,
whereas a reconnect inside the bound preserves the call. Explicit runtime
stop and process shutdown attempt BYE before deregistration or socket teardown.
No real operator call has been made by this implementation batch.

A subprocess integration test now runs the same `runWithFactory` process/IPC
path as the shipped executable while keeping the fake factory in `_test.go`.
The parent acts as Core's single public WebSocket relay and fake stable-card
AKA broker; the child owns in-memory SWu stacks plus fake P-CSCF/RTP peers. It
proves card identity resolution, runtime start, browser canary, durable
call start, REGISTER/INVITE/ACK, bidirectional non-silent PCM/RTP, explicit
BYE, runtime stop, and graceful process exit. Production `run` always builds
the real `UpstreamFactory`; there is no runtime fake flag or test endpoint.
