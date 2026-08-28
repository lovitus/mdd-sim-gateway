# MDD Go runtime rewrite

Status: architecture research and dependency-neutral core implementation in progress.

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
  stable version tag and is AGPL-3.0, so adoption is a product/license decision rather than an
  automatic dependency update.
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
5. Native Go VoWiFi trial on one non-production line. The AGPL dependency decision must be made
   before this slice; a clean-room permissive implementation remains the slower alternative.
6. Real SMS, inbound/outbound call, bidirectional audio, hotplug and long-duration validation.
   Remove legacy components only after their replacement passes and rollback evidence is saved.

## Acceptance boundaries

- No status transition restarts a container or process.
- No stale or inferred label is presented as a live fact.
- Every user action reports which layer accepted or rejected it and retains the original error.
- Multi-device hotplug preserves card identity (EID/ICCID/profile identity), never reader order.
- A real call requires signalling plus measured bidirectional media; `Registered` is never call
  health evidence.
- Browser disconnect protection physically hangs up the exact call after 10 seconds and does not
  influence registration, service startup, messaging or unrelated lines.
