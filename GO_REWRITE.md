# MDD Go runtime rewrite

Status: architecture research and the first eight isolated Go runtime slices are implemented; none is deployed.

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

## Acceptance boundaries

- No status transition restarts a container or process.
- No stale or inferred label is presented as a live fact.
- Every user action reports which layer accepted or rejected it and retains the original error.
- Multi-device hotplug preserves card identity (EID/ICCID/profile identity), never reader order.
- A real call requires signalling plus measured bidirectional media; `Registered` is never call
  health evidence.
- Browser disconnect protection physically hangs up the exact call after 10 seconds and does not
  influence registration, service startup, messaging or unrelated lines.
