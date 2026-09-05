# Go Agent desktop candidates

`build-macos-agent.sh` builds the unified Go Agent into a caller-selected output directory. It uses
the pinned Fyne packaging tool to create the standard macOS app bundle and produces a separate
headless CLI from the same source tree. Both executables read the same owner-only configuration and
compete for the same literal-loopback singleton, so they cannot own PC/SC or a modem at the same
time. The default remains `modem_enabled=false`, so a fresh configuration is PC/SC-only.

When macOS modem management is explicitly enabled, the package's fixed C companion claims each
supported raw-USB modem without detaching a kernel network driver, runs PPP and lwIP inside the
companion, and exposes only typed sockets to the Go Agent. It never creates a host cellular
interface, route, DNS resolver or local proxy listener. The Go side reuses the same typed AT,
call/SMS/PIN/AKA, paid-call lease and media contracts as Windows. GUI and CLI runtimes inspect and,
when needed, request macOS microphone permission at startup; the usage description is present in
the app bundle and embedded in the signed CLI. The bundled audio helper opens only the exact
full-duplex CoreAudio endpoints matched to that modem.

On Windows, `mdd-agent modem-probe -sim-apdu-capability` is a local read-only
diagnostic. It acquires the same exclusive AT handle and runs only the three
standard test forms; it does not open a UICC channel or publish capability to Core.
`mdd-agent modem-probe -sim-pin-status` likewise reads only `CPIN`, `QCCID` and
the Quectel PIN1 retry counter; it never submits a credential.

`build-windows-agent.sh` produces a headless `mdd-agent.exe` for SCM service and CLI use plus a
window-subsystem `MDD Agent.exe` tray manager. The GUI locates and registers only its sibling
headless executable as the service, while both use the same configuration and literal-loopback
singleton. Building the GUI requires a MinGW-w64 compiler because Fyne uses CGO; the resulting
executables do not require Go, MinGW, Python or Node on the Windows machine.

The Windows default remains `modem_enabled=false`. Enabling it adds read-only MBN hardware facts
and an auxiliary AT owner that keeps one exclusive COM handle per exact MBN equipment ID. The
separate persistent `modem_sim_apdu_enabled` switch also defaults false. When true, discovery adds
only the non-mutating `CCHO=?`/`CGLA=?`/`CCHC=?` capability tests; a real logical channel can then be
used only by the fixed ICCID-fenced USIM/ISIM AKA operation and is always closed. Discovery does not
dial, send SMS, change PIN/data state or reset a device. MBN voice class, AT call-signalling, SMS and
SIM AKA capabilities remain separate facts. The current Go slice exposes typed call/status/media,
PDU-mode SMS list/send and typed AKA over the existing Agent WSS; raw AT, DTMF and general APDU
operations are not exposed.

Raw whole-Modem passthrough is an optional, default-off Windows/Linux Agent capability. It requires
an explicit persistent binding to the current source Agent ID, modem equipment identity, inserted
ICCID and Linux importer Agent; changing any one of those facts invalidates the binding instead of
silently following a USB model, port or serial. SagerNet sing-usbip exports/imports the complete USB
device, and sing-mux carries all USB/IP logical streams inside one authenticated, one-time-token WSS
session on the existing public listener. No separate USB/IP TCP listener is opened. Windows and
Linux may be sources; only Linux may be an importer. The imported device then enters the ordinary
Linux Modem adapter rather than a reduced raw-specific call/SMS implementation.

The Linux importer enables a persistent boot-before-network guard on first modem takeover. VHCI
interfaces start unauthorized; every imported parent is durably identified and quarantined, strict
NetworkManager/udev rules keep cellular netdevs unmanaged, and an isolated nftables table drops host
output, unsolicited input and forwarding in both directions. Explicit data borrowing accepts only
ModemManager static IPv4 bearers and only sockets from `mdd-agent.service` carrying the exact session
mark and bearer interface; it never writes the host main/default route. DHCP, PPP and IPv6 bearer
setup remain typed fail-closed pending a proven implementation. The socket cgroup boundary requires
cgroup v2 and kernel/nftables socket-cgroup support; absence is an activation error, not a fallback.
PC/SC/eUICC readers continue to use the existing typed remote-card path and never enter raw USB.

SMS submission is durably idempotent at Core and Agent, and is rejected while a paid-call lease exists
so a long modem submit timeout cannot delay the 10-second call-safety hangup path. Modem AKA uses
the same paid-call/SMS coordinator and additionally requires a fresh physical `CLCC=idle` fact.
Configured Modem PIN1 values are accepted only from stdin and are redacted from config views. A
locked SIM is attempted only when its exact ICCID and at least two remaining attempts are known. A
failed, timed-out or interrupted attempt is durably blocked for that card's configuration revision;
only explicitly resetting that ICCID's PIN permits one new attempt. PUK, PIN2 and network locks are
not automated.

Core also owns one independent 0600 `notifications.db`. Real-time VoWiFi Provider SMS and authoritative
VoWiFi incoming-call snapshots write source outboxes in the same bbolt transaction as their business record;
each source freezes the current Provider CardID, and mutable catalog names/numbers are used only while that CardID
still matches. Core commits a deterministic notification event before acknowledging that source. Cellular SMS list imports
deliberately do not create notifications, and cellular incoming calls have no authoritative producer yet.
Complete, non-stale System Status transitions produce host alerts, while exact CardID/date/timezone fences
produce 3/2/1-day activation reminders. Startup seeds existing host/reminder facts without replaying them.

Webhook, Telegram and PushPlus each have one serial worker. Requests use fresh non-HTTP/2 connections and at
most three attempts, limited to failures proven to occur before the notification request was written. Any
timeout, disconnect, restart or configuration change after write is terminal `uncertain` and is never blindly
resent. Secrets use keep/clear/replace patches and are never returned by the API or written to delivery history.
`mdd-core import-notifications -config CORE.json -source PRIVATE-LEGACY.yaml` imports the old private settings
only into an empty notification store while Core is stopped or before its first start; it performs no channel
test and sends no historical event.

Core exposes Agent management, browser state and browser media WebSockets as separate paths on
one public HTTP(S) listener and port. Media intentionally remains a separate WebSocket connection
on that listener so a delayed PCM frame cannot head-of-line block management heartbeats. The
headless macOS Agent must run as a real daemon for unattended LAN access; on macOS 15, merely
detaching an SSH child with `nohup` can lose the SSH local-network-privacy exemption.

Authenticated browser/CLI VoWiFi mutations also use that same public listener under
`/v1/lines/{line}/vowifi/`. They remain CSRF-protected HTTP requests instead of sharing the media
WebSocket, and Core forwards them only to the current generation's literal-loopback provider IPC.
The provider registration heartbeat and media routing do not imply runtime, IMS, voice or SMS
health. The provider reports a separately authenticated complete snapshot to Core's loopback
listener; Core persists changed facts plus a bounded checkpoint and projects them to browser state
WSS. An unchanged snapshot refreshes only its explicitly covered layers, never browser-media facts.

Core also owns one 0600 bbolt line catalog containing only desired line/SIM/IMS/network fields and
the stable ICCID binding. It is exposed read-only through the authenticated management API and the
existing browser state WSS. `mdd-core import-legacy -config CORE.json -source config.yaml -egress-desired desired.json`
reads the old YAML and the legacy control plane's already-computed
effective country selection without modifying either file, validates the complete active-line batch,
and imports it in one transaction only when the new catalog is empty. Both source hashes are retained
in the private import receipt. Old Asterisk credentials, port blocks, container state, runtime markers,
PINs, Agent IDs and process/session generations are intentionally excluded.
The catalog does not supervise or restart provider processes.
Catalog GET responses carry the global revision as a strong `ETag`. An authenticated PUT to
`/v1/catalog/lines/{lineID}` requires both the existing CSRF contract and an exact `If-Match` value;
stale writers receive 412 without changing the catalog. PUT changes desired configuration only: it
does not render, apply, start, stop, or restart a provider. There is deliberately no destructive
line-delete endpoint in this first contract; a line can be disabled and retained for audit/rollback.
The embedded Settings page edits existing lines through this same contract. Saving only advances the
catalog revision; applying that saved revision remains a separate explicit administrator action.

When `provider_apply.enabled` is configured, the same authenticated HTTPS listener exposes
`GET/POST /v1/system/provider-config`. The public Core remains the unprivileged `mdd` process and
forwards only the typed current-catalog revision over a root-owned local Unix socket to
`mdd-provider-apply.service`. That helper is a second mode of the same `mdd-core` executable, not a
second control plane. It accepts no command, path, unit name, or rendered payload from the browser;
all privileged paths come from the owner-only Core configuration. Registration, health, hotplug and
page refresh never call it. The Settings-page button is the only browser trigger and the existing
revision precondition, drain, journal and rollback rules still apply.

`mdd-core render-provider-configs -config CORE.json -output NEW-DIR -state-dir STATE-DIR -egress-status /run/mdd-core-egress/proxy-status.json`
renders one strict 0600 provider config per enabled catalog line plus a non-secret manifest. It
refuses an existing output directory, derives stable per-line IPC tokens from the Core local secret,
and uses `127.0.0.1:0`; each provider lets the OS allocate a loopback port and registers the actual
address with Core. The renderer resolves the line's semantic egress country only through a ready
host-loopback proxy in the host status contract. Missing, stale-format, docker-bridge-only, non-loopback
or invalid exits fail closed instead of silently using the host default route. The included
`mdd-vowifi@.service` is a bounded `systemd` template adapter, not a second business-state supervisor.
`mdd-egress.service` is the unprivileged country-exit executor. The privileged typed apply helper
publishes only `/var/lib/mdd-egress-config/desired.json` as root:mdd 0640; the release installer
creates its parent root:mdd 0750. The executor validates a candidate with the installed sing-box,
then owns that child directly and publishes only loopback SOCKS endpoints as mdd:mdd 0640. It owns no
TUN, host route, resolver, modem, reader, Provider or container state. A child crash is recovered with
bounded in-process backoff; it does not restart Core, Providers, agents or the executor service.
Desired schema v2 hashes only the proxy configuration, so an unrelated catalog revision does not
reload sing-box. The reader accepts schema v1 only to permit a reversible migration from the old host
orchestrator. An unavailable desired file leaves a still-running last-known-good child in place while
marking the requested update unavailable; a rejected or failed reload restores the preceding checked
generation. The status projection never silently substitutes the host default route.
A deployment switches `providers-current` only after validating a
complete new directory, changes the installed configs to `mdd:mdd` mode 0600, then enables the
manifest's instances. Tokens are not placed in environment variables or the world-readable unit file.
The template intentionally remains compatible with systemd 219 instead of requiring its newer
credential and state-directory features.

Before any explicit apply, deployment tooling queries Core's authenticated literal-loopback
`/v1/provider/apply-preflight`. It returns the current catalog revision and, for every desired line,
the current Provider generation, runtime condition and exact `active_call` (or an explicit absent/
unreachable code). All lines are probed concurrently under one five-second budget. This endpoint is
read-only and is not exposed on the public listener.

Each rendered manifest includes the SHA-256 of every complete provider JSON file. The read-only
planner validates those hashes and the shared provider schema, fetches the live preflight, and emits
a deterministic added/changed/removed plan:

```sh
mdd-core plan-provider-apply -config CORE.json -candidate NEW-DIR -current CURRENT-DIR
```

It fails closed on a catalog revision race, an active call, a missing/unreachable changed provider,
or a provider that exists outside the supplied current manifest. Planning does not switch a symlink,
write a receipt, invoke systemd, or start/stop/restart a process.

A point-in-time preflight alone is not authority to stop a paid line: a call could start between the
snapshot and `systemctl stop`. For each changed or removed running provider, the apply adapter must
therefore acquire the exact-revision loopback `/v1/provider/apply-drain` lease first. The provider
persists that lease in its existing 0600 bbolt state across process generations. It refuses drain while
a call, message send, or runtime transition is active; once drained, it rejects only new call/message/
runtime-start operations while still allowing call end and runtime stop. A partial multi-line drain is
released before Core reports failure. `/v1/provider/apply-resume` requires the exact same lease. These
maintenance endpoints are not public and ordinary registration or health changes never invoke them.
The added maintenance status is VoWiFi IPC schema v2; a v1 provider is rejected instead of being
silently treated as drain-capable.

The explicit Linux apply command consumes that plan and lease contract. It is part of the single
`mdd-core` executable and is never invoked by normal Core startup, registration changes, health
events, hotplug, or browser refresh:

```sh
mdd-core apply-provider-configs \
  -config /etc/mdd/core.json \
  -candidate /etc/mdd/providers/releases/REVISION \
  -current-link /etc/mdd/providers-current \
  -receipt-dir /var/lib/mdd-system/provider-apply \
  -provider-binary /usr/lib/mdd/current/mdd-vowifi
```

It requires root, a root-owned `systemctl` and Provider binary, an existing root-owned 0700 receipt
directory, and candidate files owned by the `mdd` account with exact 0700/0600 modes. It acquires a
non-blocking host lock, revalidates the current link and candidate after the lock, writes and fsyncs
each external step before executing it, then performs only the plan's stop/disable/link/enable/start
changes. Before the commit boundary, failure restores the previous link and service state and releases
the exact drain. After candidate Providers have registered, drain release is the commit boundary; a
partial release leaves the candidate installed and an `applied_resume_incomplete` receipt that blocks
another apply for explicit recovery instead of guessing a replay. Receipts contain no tokens or raw
command output.

The public deployment boundary remains one TCP port, certificate and HTTP(S)/WSS reverse-proxy rule.
Browsers and Agents use typed logical paths/connections on that listener. PCM keeps a separate WSS
connection on the same listener because RFC 6455 is ordered over TCP and the browser WebSocket API has
no backpressure; combining audio and management into one physical ordered stream would let delayed
audio block heartbeats. This does not expose RTP/UDP or require users to confirm an interface IP.

Core and the headless Agent remain single Go executables. The optional Fyne GUI is an adapter over the
same Agent controller and may carry GUI dependencies. The AGPL VoWiFi Provider remains a separate Go
executable for its license and process-failure boundary; it is not another control plane.

## Versioned Linux release installation

`mdd-release` assembles an immutable Linux release directory from the Core, headless PC/SC Agent and
Provider executables, their systemd units, and the complete corresponding AGPL Provider source and
notice. It verifies the target OS/architecture and Go build metadata without executing a release
binary, records every file's mode, size and SHA-256, and rejects extra or changed files when the
installer loads the directory. The Agent unit is packaged but is not enabled on a server by the
installer; an endpoint administrator must first create its owner-only configuration and enable it
explicitly.

As root, install a verified directory with the same `mdd-core` executable that runs the service:

```sh
mdd-core install-release -source /absolute/path/to/mdd-release-ID
```

Before installation, a host operator can perform a read-only release and persistence check:

```sh
install-release.sh preflight /absolute/path/to/mdd-release-ID
```

This validates the complete manifest, exact source revision metadata, artifact digests, target
architecture, and persistent-state boundary. It does not create accounts, files, links, or
systemd state and does not start or reload services.

The installer validates the entire release and host persistence boundary before creating the fixed
`mdd` system account. It then stages the immutable directory under `/usr/lib/mdd/releases`, atomically
switches `/usr/lib/mdd/current`, maintains stable executables in `/usr/libexec/mdd`, installs the unit
links, writes root-only receipts under `/var/lib/mdd-system/release-install`, and runs only
`systemctl daemon-reload`. It deliberately does **not** enable, start, stop or restart a service. An
already-running process therefore continues using its original executable until an operator performs
an explicit service action. If daemon reload and automatic link rollback cannot both complete, recover
the recorded previous link explicitly:

```sh
mdd-core recover-release-install
```

The standard service state remains owned by `mdd` under `/var/lib/mdd`; release/apply receipts remain
root-owned outside that tree. On Linux kernels affected by the known bbolt/ext4 `fast_commit` defect,
installation fails before mutation when the state filesystem has that feature enabled. Runtime bbolt
opens do not repeat this privileged block-device inspection.

The packaged `install-release.sh` is the complete host lifecycle entrypoint. `stop` first quiesces the
privileged apply helper, then every strictly identified Provider instance, then Core and country egress;
it deliberately leaves the independently managed endpoint Agent alone and does not change enablement.
`uninstall` additionally stops and disables the packaged Agent, performs the same strict manifest/link/
ownership preflight again under the release lock, removes only verified software links, units and release
directories, and reloads systemd. It preserves `/etc/mdd`, every `/var/lib/mdd*` data or audit tree and the
`mdd` account. There is intentionally no purge option.

The GitHub workflow publishes standard downloadable archives for the Linux release and desktop Agent
packages, and keeps them short-lived for its internal cross-job gates. A pushed `v*` tag
is published only after the Linux fresh-host lifecycle and both desktop Agent builds pass; the GitHub
Release contains the Linux server tar, Windows Agent, macOS Agent and one `SHA256SUMS` file.

Provider configuration rendering reads a typed catalog snapshot from the running Core's authenticated
literal-loopback API. It never opens the live catalog bbolt file beside Core. A newly initialized empty
catalog starts at revision 1, so a no-line shadow installation can render and apply a valid empty
manifest without weakening the positive-revision apply contract.

The script deliberately requires a non-system `TMPDIR` and never writes a package into the Git
worktree. With no `--identity`, it creates an ad-hoc signed development candidate. Supplying a
Developer ID identity performs timestamped hardened-runtime signing; notarization is a separate
release action and is not silently triggered by a local build.

Example:

```sh
export TMPDIR=/path/to/external/task-tmp
./release/build-macos-agent.sh --output /path/to/artifacts/MDD-Agent-macOS
```

Windows example from a build host with MinGW-w64:

```sh
export TMPDIR=/path/to/external/task-tmp
./release/build-windows-agent.sh \
  --cc x86_64-w64-mingw32-gcc \
  --output /path/to/artifacts/MDD-Agent-Windows-amd64
```
