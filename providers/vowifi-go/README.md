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

For a P-CSCF that does not request IMS Security-Agree, the registered flow now
also drives the upstream dialog state machine. A wire test observes
REGISTER, INVITE, ACK, BYE and deregistration in order over the userspace stack.
This is signalling and physical hangup evidence only; it is not media health.

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
configuration, persists lifecycle idempotency in bbolt, and serves only the
provider-neutral authenticated loopback IPC. Its Agent AKA client also uses an
authenticated literal-loopback Core broker, which forwards the high-level
operation through Core's existing public Agent WSS. Neither local IPC is a new
public deployment port. Decrypted inner IP packets are not tunneled through
Core and no host TUN or route is created.

Current tests use fake SIM, tunnel and P-CSCF sessions only. They make no
host-network connection, APDU request, paid call or message. Operator IMS
Security-Agree, inbound SIP/media listeners, SIP-dialog/media lifecycle
handling for inbound/re-INVITE flows, SRTP, the public Core route/browser media
WSS, SMS operation and real operator validation remain unimplemented. Until
the direct browser media and messaging paths exist, those mutating IPC methods
return typed `not_ready` and cannot perform a paid action.

The executable now also terminates Core's authenticated same-host media relay
at `/v1/media/{session}`. The Core relay preserves WebSocket message boundaries
without inspecting content. A new provider session first runs a no-charge
protocol-v1 PCM loopback: two non-silent 320-byte capture/playback frames plus browser
evidence are required before it becomes ready. This proves the browser WSS
transport only. It is not yet attached to `StartMediaCall`, and cannot originate
a carrier call in this slice.
