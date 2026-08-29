# Go Agent desktop candidates

`build-macos-agent.sh` builds the PC/SC-only Go Agent into a caller-selected output directory.
It uses the pinned Fyne packaging tool to create the standard macOS app bundle and produces a
separate headless CLI from the same source tree. Both executables read the same owner-only
configuration and compete for the same literal-loopback singleton, so they cannot own PC/SC at
the same time.

On Windows, `mdd-agent modem-probe -sim-apdu-capability` is a local read-only
diagnostic. It acquires the same exclusive AT handle and runs only the three
standard test forms; it does not open a UICC channel or publish capability to Core.

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
SMS submission is durably idempotent at Core and Agent, and is rejected while a paid-call lease exists
so a long modem submit timeout cannot delay the 10-second call-safety hangup path. Modem AKA uses
the same paid-call/SMS coordinator and additionally requires a fresh physical `CLCC=idle` fact.

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

`mdd-core render-provider-configs -config CORE.json -output NEW-DIR -state-dir STATE-DIR -egress-status proxy-status.json`
renders one strict 0600 provider config per enabled catalog line plus a non-secret manifest. It
refuses an existing output directory, derives stable per-line IPC tokens from the Core local secret,
and uses `127.0.0.1:0`; each provider lets the OS allocate a loopback port and registers the actual
address with Core. The renderer resolves the line's semantic egress country only through a ready
host-loopback proxy in the host status contract. Missing, stale-format, docker-bridge-only, non-loopback
or invalid exits fail closed instead of silently using the host default route. The included
`mdd-vowifi@.service` is a bounded `systemd` template adapter, not a second business-state supervisor.
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

`mdd-release` assembles an immutable Linux release directory from the Core and Provider executables,
the two systemd units, and the complete corresponding AGPL Provider source and notice. It verifies the
target OS/architecture and Go build metadata without executing a release binary, records every file's
mode, size and SHA-256, and rejects extra or changed files when the installer loads the directory. A
Linux Agent executable is optional until the headless Linux hardware adapter is complete.

As root, install a verified directory with the same `mdd-core` executable that runs the service:

```sh
mdd-core install-release -source /absolute/path/to/mdd-release-ID
```

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
