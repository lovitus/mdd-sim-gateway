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

The final `mdd-vowifi` process will keep the packet session, userspace IP stack,
IMS, SMS and voice in this module. Core will exchange authenticated lifecycle,
typed state and call/message operations with that process. Decrypted inner IP
packets will not be tunneled through Core IPC and no host TUN or route will be
created.

Current tests use fake SIM and tunnel sessions only. They make no host-network
connection, APDU request, call or message. IMS, service IPC and real operator
validation remain unimplemented.
