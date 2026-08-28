# MDD Go runtime rewrite

Status: architecture research and the first sixteen isolated Go runtime slices are implemented; none is deployed.

## Outcome

MDD will migrate to a layered Go runtime. The rewrite is not a line-for-line translation of
the current Python and shell implementation. Protocol facts, operation readiness, recovery,
process lifecycle, and call billing safety are separate owners and cannot overwrite each other.

The existing production runtime remains the rollback baseline until a Go slice passes the same
real device and carrier flow. A shadow reader may observe legacy data, but it cannot place calls,
send messages, change a SIM, restart an Engine, or alter host networking.

## Evidence gathered

- The current runtime contains about 84,000 lines of Python, shell, PowerShell and frontend
  JavaScript. `control/app/main.py` alone is about 13,900 lines; `engine/swu_ike.py` is about
  6,700 lines. State and recovery concerns cross these files rather than having one owner.
- A real `reg_unanswered` observation on 2026-08-28 caused Control to stop and replace an idle
  giffgaff Engine even though the SWu tunnel was connected. This proves that registration state
  currently has destructive process-lifecycle authority it must not have.
- Asterisk 20 exposes fixed `retry_interval` and `fatal_retry_interval`, not a general exponential
  registration policy: <https://docs.asterisk.org/Asterisk_20_Documentation/API_Documentation/Module_Configuration/res_pjsip_outbound_registration/>.
- `github.com/boa-z/vowifi-go` implements broad Go SWu/EAP-AKA, IMS-AKA, SIP, messaging and media
  primitives. Its full test suite passed at commit `1e9c6e6adbfcd9667695149d5ecb0f71cd062f07`, but its
  own readiness document says real device, operator and production use are not proven. It has no
  stable version tag and is AGPL-3.0. The product decision is to use it only behind the isolated
  `mdd-vowifi` provider boundary, preserving its license/source obligations and not importing its
  control plane into Core.
- Importing the whole VoHive application would replace the current monolith with another large
  application (about 105,000 Go lines). It is useful as a reference, not as MDD's control plane.
- gVisor netstack exposes `net.Conn`/`net.PacketConn` adapters, and WireGuard's Go implementation
  demonstrates a fully in-memory netstack. The SWu decrypted packet boundary can therefore feed
  an in-memory TCP/IP stack directly. Inner IMS SIP, DNS and RTP need no TUN device, host default
  route, policy route, container address, or user-confirmed browser IP.

## Target ownership

1. `mdd-core`: configuration, durable topology, API, event stream and operation coordination.
2. `mdd-agent`: Windows/macOS/Linux hardware discovery, PC/SC, modem access and audio. One binary
   provides service, CLI and GUI/tray clients over a local authenticated control socket.
3. `mdd-vowifi`: one long-lived runtime per SIM, with SWu, userspace IP, IMS, SMS and voice as
   in-process layers. A failed registration retries inside this runtime; it cannot restart its
   process or container.
4. Web UI: consumes typed facts and operation readiness. Display labels never become admission
   inputs.

## State contract

- Every fact names one layer, one authoritative source, one generation, a monotonic generation
  epoch and source sequence, a source observation time, a server receive time and an expiry.
- A different source cannot overwrite a layer. An older sequence or older generation cannot
  overwrite current evidence.
- Freshness uses the server receive time, not a possibly skewed Agent/Engine wall clock. Stale
  evidence becomes `unknown`; it is not rewritten as stopped, failed or unsupported.
- Each layer publishes both a display condition and an explicit `available` bit. Operations use
  the latter and their own required layer set; no global green/red state gates every feature.
- Hardware, SIM, cellular access, SWu tunnel, IMS registration, messaging, media and active call
  are independent layers.

## Recovery contract

- A provider `Retry-After`/backoff value is honored exactly.
- Other recoverable failures use a capped exponential delay in the same layer and process.
- Domain recovery actions are limited to probe, retry, reauthenticate, reconnect a transport, or
  reopen a hardware handle. There is no domain action named restart-container or restart-process.
- OS service restart is reserved for a process crash. Repeated crashes are bounded by the service
  manager and surfaced as a process fault; they are not reported as a carrier fault.
- Active calls are not torn down by registration, tunnel-health or display-state changes. The
  billing safety guard only targets the exact call when the browser explicitly hangs up/closes or
  its heartbeat is absent for more than 10 seconds.

## Migration slices

1. Dependency-neutral Go state/recovery/call-safety core with replay tests.
2. Read-only shadow adapter for current Agent/Engine events; compare projections without changing
   production.
3. Go API/store and WebSocket event stream, retaining legacy protocol adapters.
4. Go cross-platform Agent, first PC/SC-only on macOS and existing Windows modem behavior, then
   Linux and modem enablement by explicit capability.
5. Native Go VoWiFi trial on one non-production line through the approved isolated AGPL provider.
   Its exact upstream commit remains pinned and independently replaceable behind the boundary.
6. Real SMS, inbound/outbound call, bidirectional audio, hotplug and long-duration validation.
   Remove legacy components only after their replacement passes and rollback evidence is saved.

## Read-only shadow

`go run ./cmd/mdd-shadow -snapshot PATH` under `go-runtime` reads a previously saved legacy
`/api/snapshot` response and prints the Go facts and per-operation readiness. The command has no
URL, credential, call, message, hardware or recovery option, so running it cannot change a live
system. Its legacy adapter deliberately ignores display labels such as `Working` and consumes only
machine facts and explicit device capabilities.

The shadow keeps cellular data, cellular voice and cellular SMS as separate layers. A working data
connection therefore cannot fabricate call or SMS readiness. It also remembers generation epochs:
after a new Engine/card generation is observed, a delayed snapshot from an already seen old
generation cannot replace current facts.

## Direct event contract and replay

The versioned Go event contract assigns every layer to exactly one role: `mdd-core`, `mdd-agent`,
or `mdd-vowifi`. A role cannot publish another role's facts. Producer processes report their own
generation and sequence, while `mdd-core` assigns the durable epoch and receive time. Before live
ingestion, the core must explicitly authorize the exact line/role/producer/generation binding;
replaced Agents and VoWiFi runtimes therefore cannot publish themselves back into service.

`go run ./cmd/mdd-replay -events PATH` replays durable NDJSON records through the same reducer and
operation catalog. It has no live-system client or mutation operation. The replay and legacy shadow
now share one operation catalog, so page projections, replay diagnostics and later admission checks
cannot silently grow different required-layer lists.

`go run ./cmd/mdd-core -events PATH` starts the first dependency-neutral Core slice. It only accepts
a loopback listen address and exposes `GET /healthz`, `GET /v1/lines`, and
`GET /v1/lines/{lineID}`. There is no write route. Facts are re-evaluated against current time on
every request, so an old ready fact becomes unknown after its TTL without a restart or timer-driven
state mutation. SIGINT/SIGTERM perform a bounded graceful shutdown and exit successfully.

NDJSON remains a portable replay/export format, not the selected transactional live store. Go's
`os.File.Sync` can flush a completed write, but a power loss can still leave a custom append format
with a torn tail. The selected store is bbolt: the smaller pure-Go/MIT option with ACID single-writer
transactions. Its documented ext4 fast-commit caveat remains an explicit deployment preflight,
not a hidden durability claim. NDJSON remains export/replay only.

## Transactional event store

`internal/events.BoltStore` pins bbolt v1.5.0, the latest version verified at implementation time.
The trusted Core `Activate` operation replaces the exact line/role producer binding, allocates the
layer epoch and appends the first event in one write transaction. Subsequent producer `Accept`
operations can append only under that exact durable binding. A failed transaction therefore cannot
leave a replacement binding without its first record.

Event IDs are durable idempotency keys: an exact retry returns the original receipt time and epoch,
while reuse for different content is rejected. A generation that has already been replaced cannot
be activated again. Records replay in commit order through the same reducer and can be exported as
NDJSON without making NDJSON the live store. The database is forced to mode 0600, its parent
directory is synced on initial creation on Unix, unknown schema versions fail closed, and no bbolt
mmap-backed byte slice escapes a transaction. The store does not claim to neutralize bbolt's
documented ext4 `fast_commit` limitation; deployment must detect or exclude that filesystem mode.

## Userspace VoWiFi boundary

The provider-neutral `internal/vowifi` contract makes the no-host-routing requirement structural:

- the outer dialer opens only the physical ePDG packet transport (directly or through the selected
  country egress);
- the SWu provider exposes decrypted inner IP packets and the assigned addresses/DNS/MTU;
- an in-process network stack exposes only Go `DialContext`, `ListenPacket`, and `LookupNetIP`;
- IMS receives that userspace network, not an interface name, route table, namespace or TUN handle;
- SIM AKA stays behind an Agent authenticator and does not hand reusable SIM secrets to Core.

Opening a session is one bounded ePDG → userspace stack → IMS attempt. A failure returns the exact
stage and closes only resources acquired by that attempt in reverse order. Retry/backoff remains in
the recovery package; there is no process/container restart operation. The same contract can host
either the researched AGPL provider or a later clean-room provider without changing Core or IMS.

## One Agent runtime, three frontends

`internal/agentcontrol.Controller` owns the only hardware runtime generation. The eventual OS
service, CLI and GUI/tray are clients of this Controller rather than three implementations of Agent
behavior. Duplicate start while starting/running/stopping returns a conflict. An unexpected worker
exit becomes a visible `failed` state and is not restarted. A manual start after failure creates one
new generation. If stop times out, state remains `stopping` and a replacement runtime is forbidden
until the old worker actually exits, preventing two processes from owning the same modem/card.

This controller intentionally contains no service-manager, tray, PC/SC or modem package. Those are
adapters around one lifecycle and cannot introduce their own retry/restart policy.

The shared local management transport is an authenticated HTTP API on one fixed literal loopback
address. Binding that address is the cross-process singleton: a service host and GUI host cannot
both own it. `status`, `start`, and `stop` have one typed contract and are used by the same client.
The client refuses DNS names, non-loopback targets and HTTPS-to-local configuration, so it cannot
send the local bearer token to a remote address. Authentication compares fixed-size SHA-256 values
in constant time; failures return only `unauthorized` and never echo the token. A hung stop returns
timeout while the Controller remains `stopping`, preserving the no-second-owner rule.

## PC/SC attachment monitor

`internal/agentreader` reconciles one cancellable card session per present attachment. Reader names
are only transient carrier keys; durable identity remains EID/ICCID/profile data read by the session.
Removal cancels only that reader session, insertion starts only that session, and a PC/SC card-event
generation change replaces a stale session even when the ATR is unchanged. Session transport errors
retry with the shared capped exponential policy inside the worker; they do not restart the Agent.

`internal/pcscmonitor` uses the latest available `github.com/ebfe/scard` revision for native
WinSCard/pcsc-lite calls. Known readers wait through `SCardGetStatusChange`; cancellation uses
`SCardCancel`. An empty reader set and reader-list additions are detected by a bounded re-list period,
avoiding both an uninterruptible watcher and dependence on the platform-specific PnP pseudo-reader.
Two identical reader models remain distinct when PC/SC provides distinct attachment names; those
names are never promoted into card identity.

## Agent WSS and SIM AKA boundary

The public Agent transport is one outbound WebSocket connection mounted on the same HTTPS/WSS
listener used by the browser and API. Browser and Agent machines necessarily use separate physical
connections, but deployment exposes one protocol and port; an Agent behind NAT, VPN or a forwarded
localhost path never needs an inbound listener. The maintained zero-dependency
`github.com/coder/websocket` v1.8.15 was selected over the deprecated `x/net/websocket`; it has
first-class contexts, `net/http.Client` integration for the existing certificate-pin transport and
RFC close/ping handling. Plain `ws` is accepted only on a literal loopback address for tests.

`agentlink` is not a general event bus or APDU tunnel. Its only hardware operation is a versioned
`AuthenticateAKA` request tied to exact Agent process generation, card session generation and
numeric ICCID. RAND/AUTN and the one response body are bounded and never enter status/error fields;
Core may relay but must not persist or log them. Unknown/trailing JSON, stale generations, duplicate
Agent connections and mismatched response identities fail closed. A late/duplicate response after a
bounded Core timeout is inert and does not disconnect otherwise healthy Agent operations. A protocol ping every 10 seconds
with a bounded pong wait supplies connection health without owning any restart. At most 16 requests
may execute concurrently on one Agent.

`internal/agentsim` adapts the existing hotplug reconciler to native WinSCard/PCSC. It keeps one
shared card handle per live attachment, withdraws the session generation before waiting for an
in-flight operation, and serializes each card with `SCardBeginTransaction`/`EndTransaction`. ICCID
is read from EF_ICCID; a clear file-not-found remains a visible blank attachment, while transport or
unexpected status errors return to the existing bounded exponential retry instead of leaving a
half-ready session. Application selection scans every EF_DIR record for USIM/ISIM before using the
standards partial-AID fallback. The only commands exposed internally are selection, read-only
identity, configured PIN verification and AUTHENTICATE; no remote caller can issue profile/delete/
write APDUs. A rejected configured PIN is tried at most once per card/PIN hash in one Agent process;
changing the PIN permits one new attempt. Durable cross-restart PIN-attempt history and EID/profile
topology remain later Agent-host work.

An integrated real-WebSocket/fake-PCSC test proves Core WSS → exact Agent generation → exact card
generation/ICCID → PC/SC transaction → AUTHENTICATE response. Separate tests cover removal, blank
eUICC carrier visibility, wrong identity, transport/status classification, missing-Le correction,
PIN retry containment and failed transaction release. These are fake-card tests: no real SIM was
queried and the Agent executable/server route is not assembled or deployed yet.

Primary implementation references:

- <https://github.com/coder/websocket/releases/tag/v1.8.15>
- <https://www.etsi.org/deliver/etsi_ts/102200_102299/102221/18.01.00_60/ts_102221v180100p.pdf>
- <https://www.3gpp.org/FTP/tsg_t/TSG_T/TSGT_07/Docs/PDFs/TP-000014.pdf>
- <https://learn.microsoft.com/en-us/windows/win32/api/winscard/nf-winscard-scardbegintransaction>
- <https://learn.microsoft.com/en-us/windows/win32/api/winscard/nf-winscard-scardtransmit>

The provider-side half of this route uses `agentlink.BrokerClient` over authenticated literal-loopback
HTTP. Core owns `BrokerAPI` and is the only component that sees the Agent connection table; the AGPL
provider supplies exact Agent/process/card generations but cannot open or replace a remote Agent
connection. Both the URL and the server-observed peer must be literal loopback, redirects are disabled,
the bearer is compared as a fixed-size hash, and strict JSON/size/timeout limits match the WSS operation.
Agent-originated typed failures survive the local hop; broker/generation/offline failures stay distinct.
A real HTTP → Core broker → WSS → fake Agent round trip passes, so the later service does not need a
second public protocol or port.

## Isolated AGPL VoWiFi provider

`providers/vowifi-go` is a separate Go module pinned to upstream pseudo-version
`v0.0.0-20260709161034-1e9c6e6adbfc`, exact commit
`1e9c6e6adbfcd9667695149d5ecb0f71cd062f07`. Its files carry an AGPL notice and
the Core module does not import it. Complete AGPL license/source packaging remains a mandatory gate
before distribution or deployment.

The first adapter constructs the real upstream IKE manager with an Agent-backed AKA provider and
forces every open request to `DataplaneModeUserspace`. It accepts only a ready
`PacketTunnelReadSession`; kernel, TUN-only, incomplete and canceled sessions are rejected and
closed with a bounded cleanup context. Packet payload and DNS slices are copied rather than exposing
upstream ownership. Tests use fake tunnel/SIM dependencies and compile the real upstream constructor;
they perform no APDU, network, message or call action.

The final process boundary will be above the userspace stack and IMS, not between SWu packets and the
stack: decrypted IP stays inside `mdd-vowifi`, while Core receives typed state and authenticated
operations. Later slices below now implement the userspace stack and IMS registration binding; the
service/IPC executable and production operator path remain unimplemented and must not be reported as
deployed or functional VoWiFi.

## In-memory TCP/IP stack

The maintained candidates were tested rather than selected by name alone. The old
`github.com/google/netstack` repository is archived in favor of gVisor; Tailscale's integration is
coupled to its control plane; `github.com/noisysockets/netstack` is an older gVisor fork. Direct
gVisor and WireGuard-Go use the same underlying stack, but direct gVisor has no stable module API.
The selected thin adapter is the latest verified WireGuard-Go module
`v0.0.0-20260522210424-ecfc5a8d5446`, whose `tun/netstack` package pins a complete, compiling gVisor
revision and exposes TCP, UDP and DNS without an OS TUN. An isolated compile/race probe confirmed
`Device.File()==nil` on Go 1.26.3.

`providers/vowifi-go/internal/usernet` connects that in-memory device directly to the SWu
`PacketTunnelReadSession` with one pump in each direction. It exposes context-aware TCP dial,
TCP listen, UDP dial/listen and DNS lookup. Closing or one pump failure cancels and closes only this
stack and packet session; the exact direction/error is observable and there is no recovery action
that restarts a process or container. Config/address/DNS and packet slices do not escape ownership.
Two linked fake SWu sessions complete real TCP echo, UDP request/response and DNS A lookup tests
entirely in memory.

The stack tracks exported UDP packet sockets used by the media boundary. Shutdown first cancels and
closes these children, then closes the netstack device and SWu packet session; this prevents an RTP
or RTCP writer racing a closed gVisor notification channel. SIP/DNS connections keep their original
types and ownership rather than passing through a new generic wrapper.

## Userspace IMS registration

The reviewed upstream HEAD still hard-coded `net.Dialer` inside SIP wire flows. Rather than copy its
SIP state machine, the isolated provider includes the exact upstream source and a narrow optional
`DialContext` seam propagated through REGISTER, request, persistent flow and DNS resolution. Nil
keeps upstream behavior unchanged. `internal/ims` supplies only `usernet.Stack.DialContext` and
rejects custom transports, resolvers, local binding and security-plan installers, so an incomplete
userspace setup fails closed instead of leaking IMS traffic to the host.

A wire-level fake P-CSCF test resolves its FQDN through DNS carried by linked SWu packet sessions,
receives the initial REGISTER/200, then receives deregistration REGISTER with `Expires: 0` when the
registration closes. The provider and full upstream protocol suites pass compile, vet and tests.
The upstream compatibility selftest is not reported as passed: its Bash script requires `mapfile`,
while the validation Mac has Bash 3.2. Operator security association, inbound SIP/RTP sockets,
voice/SMS operations and service IPC remain separate acceptance slices.

## Userspace IMS dialog signalling

`internal/ims.NewOutboundAgent` only constructs a voice dialog agent from a machine-confirmed
registration that has a contact identity and voice transport. It does not configure media and does
not turn registration into call readiness. A second wire test completes REGISTER, INVITE, ACK, BYE
and deregistration in order through the userspace stack. The successful BYE is physical signalling
evidence; no call/media success is emitted because no bidirectional RTP/PCM sample exists.

IMS Security-Agree remains a real portability gate. 3GPP TS 33.203 requires ESP transport mode for
this protection. The current upstream implementation installs Linux XFRM, while gVisor still marks
ESP header handling as unimplemented. The researched MIT `n0madic/go-ipsec` project has tested
userspace ESP, but its package is internal and wraps complete inner IP datagrams for an IKEv2 VPN;
it cannot be imported as an IMS transport-mode SA. MDD therefore keeps SecurityPlanInstaller
fail-closed and does not pull in another IKE control plane. Relevant primary sources:

- <https://portal.3gpp.org/desktopmodules/Specifications/SpecificationDetails.aspx?specificationId=2277>
- <https://github.com/n0madic/go-ipsec>
- <https://github.com/google/gvisor/issues/3912>

## Userspace IMS media boundary

`providers/vowifi-go/internal/media` terminates only the IMS-side RTP/RTCP sockets on the SWu
in-memory stack. Its other side is the existing browser contract: 8 kHz mono, 20 ms, 160 signed
little-endian samples (320 bytes). It uses the already pinned Pion RTP/RTCP parsers and the latest
reviewed BSD-3-Clause `zaf/g711` v1.4.0 implementation for static PCMU/PCMA. The newer Pion RTP
v1.10.5 was checked; its changes are unrelated VP9/H265/extension work, so the provider retains the
v1.10.2 version already proven by the pinned VoWiFi upstream instead of creating unrelated churn.

The Bridge reserves local userspace RTP/RTCP ports before an INVITE and applies the literal-IP
remote endpoints later from the SDP answer, without reopening sockets or using host DNS. Browser
PCM is paced into RTP at 20 ms; a bounded 100–2000 ms queue drops stale/overflow media and records
it without owning call termination. A short PCM gap sends encoded silence and marks the next real
talkspurt. Incoming RTP accepts only the negotiated peer/codec, tracks sequence loss and wrap, and
keeps the bounded browser playback queue current. RTCP sender reports include packet/octet counts
and the received stream's extended sequence. Parent cancellation or explicit Close stops both
sockets and all pumps.

Linked fake SWu stacks now prove non-silent PCM→PCMU→RTP and RTP→PCMU→PCM, RTCP reporting, delayed
SDP endpoint installation, queue pressure isolation, cancellation, sequence wrap, and no RTP after
Close. Repeated race testing also closes an active packet writer before the underlying netstack
device. Protocol references:

- <https://www.rfc-editor.org/rfc/rfc3550.html>
- <https://www.rfc-editor.org/rfc/rfc3551.html>
- <https://github.com/pion/rtp>
- <https://github.com/zaf/g711>

## Userspace outbound call lifecycle

`internal/ims.StartMediaCall` is the thin owner between the existing upstream SIP dialog and the
media Bridge. It reserves the userspace RTP/RTCP ports first, builds one PCMU or PCMA 20 ms offer,
sends INVITE over the registered userspace flow, validates the answer, and applies only literal-IP
remote endpoints. RTCP mux, rejected/non-bidirectional media, a different codec/payload and
unsupported packetization fail closed. Per RFC 3264/3605, an absent explicit RTCP endpoint resolves
to the RTP address and RTP port plus one.

The bounded INVITE context does not become the media lifetime. An explicit lifetime may stop the
Bridge, while normal call termination sends BYE and then closes media. Closing local media never
pretends the remote dialog or billing ended: rejected/failed BYE is returned exactly and a later
caller may retry it. If the peer accepts the SIP dialog but its SDP cannot be used, the wrapper
performs a separate bounded BYE before returning the media error.

One linked fake P-CSCF/RTP peer now proves REGISTER → INVITE/ACK → bidirectional non-silent
PCM/RTP → BYE → no RTP after close → deregistration. Separate tests prove the accepted-dialog
cleanup path and a rejected BYE followed by a successful retry. The UDP fixture now deduplicates
SIP transactions while still responding to legal retransmissions; counting retransmissions as
new stages had caused repeat-gate-only deregistration timeouts. Ten race-enabled repetitions pass.
This remains fake-network evidence: operator Security-Agree, SRTP, service IPC, inbound calls and
real-device/carrier validation are not implemented, so this is not a production call-health claim.
Primary offer/answer references:

- <https://www.rfc-editor.org/rfc/rfc3264.html>
- <https://www.rfc-editor.org/rfc/rfc3605.html>

## VoWiFi service IPC contract

`go-runtime/vowifiipc` is the public, provider-neutral contract between Core and the future
`mdd-vowifi` executable. The alternatives were checked before implementation: Connect-Go v1.20.0
and gRPC-Go v1.81.1 provide mature generated streaming RPC, but protobuf/code generation and HTTP/2
would couple both Go modules before this low-rate control surface needs streaming; HashiCorp
go-plugin v1.6.0 also makes the host launch/kill the plugin, which conflicts with the rule that
domain state cannot own process lifecycle. The selected transport reuses the already proven Agent
pattern: standard-library HTTP/JSON on a literal loopback connection with no new dependency.

The versioned strict schema carries only lifecycle, typed provider snapshots, outbound call/end and
message operations. It has no type capable of carrying SWu packets, RTP/RTCP, PCM, SIM secrets or
message bodies in status/error output. Provider snapshots identify line, producer, process
generation, monotonic sequence and source observation time; invalid or fabricated `ready` facts
fail before Core can consume them. Mutations require stable operation IDs, and call/message
business IDs are returned unchanged. The backend contract requires durable idempotency before a
real side effect; an ambiguous crash result must be reconciled rather than automatically replayed.

Every protected request uses a minimum 32-byte bearer compared by fixed-length SHA-256 in constant
time. Both URL validation and the server's observed `RemoteAddr` require literal loopback, redirects
are disabled, JSON rejects unknown/trailing fields, request/response sizes are bounded, operation
timeouts are explicit, and machine failure kind/layer/retry delay survive the round trip. A real
child test process serves the API and proves status → start → call → busy conflict → end → message
→ stop over TCP; invalid authority, unknown fields, oversized bodies and invalid snapshots fail
closed. Ten race-enabled repetitions pass. This is the service transport and contract only: the
real AGPL provider backend/executable, durable operation store and production wiring remain the next
slice, so no call/message capability is claimed.

References evaluated:

- <https://github.com/connectrpc/connect-go>
- <https://github.com/grpc/grpc-go>
- <https://github.com/hashicorp/go-plugin>
- <https://pkg.go.dev/net/http>

## Acceptance boundaries

- No status transition restarts a container or process.
- No stale or inferred label is presented as a live fact.
- Every user action reports which layer accepted or rejected it and retains the original error.
- Multi-device hotplug preserves card identity (EID/ICCID/profile identity), never reader order.
- A real call requires signalling plus measured bidirectional media; `Registered` is never call
  health evidence.
- Browser disconnect protection physically hangs up the exact call after 10 seconds and does not
  influence registration, service startup, messaging or unrelated lines.
