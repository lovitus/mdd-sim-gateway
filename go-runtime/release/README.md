# Go Agent macOS candidate

`build-macos-agent.sh` builds the PC/SC-only Go Agent into a caller-selected output directory.
It uses the pinned Fyne packaging tool to create the standard macOS app bundle and produces a
separate headless CLI from the same source tree. Both executables read the same owner-only
configuration and compete for the same literal-loopback singleton, so they cannot own PC/SC at
the same time.

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
existing browser state WSS. `mdd-core import-legacy -config CORE.json -source config.yaml` reads the
old YAML without modifying it, validates the complete active-line batch, and imports it in one
transaction only when the new catalog is empty. Old Asterisk credentials, port blocks, container
state, runtime markers, PINs, Agent IDs and process/session generations are intentionally excluded.
The catalog does not supervise or restart provider processes.
Catalog GET responses carry the global revision as a strong `ETag`. An authenticated PUT to
`/v1/catalog/lines/{lineID}` requires both the existing CSRF contract and an exact `If-Match` value;
stale writers receive 412 without changing the catalog. PUT changes desired configuration only: it
does not render, apply, start, stop, or restart a provider. There is deliberately no destructive
line-delete endpoint in this first contract; a line can be disabled and retained for audit/rollback.

`mdd-core render-provider-configs -config CORE.json -output NEW-DIR -state-dir STATE-DIR`
renders one strict 0600 provider config per enabled catalog line plus a non-secret manifest. It
refuses an existing output directory, derives stable per-line IPC tokens from the Core local secret,
and uses `127.0.0.1:0`; each provider lets the OS allocate a loopback port and registers the actual
address with Core. The included `mdd-vowifi@.service` is a bounded `systemd` template adapter, not a
second business-state supervisor. A deployment switches `providers-current` only after validating a
complete new directory, changes the installed configs to `mdd:mdd` mode 0600, then enables the
manifest's instances. Tokens are not placed in environment variables or the world-readable unit file.
The template intentionally remains compatible with systemd 219 instead of requiring its newer
credential and state-directory features.

The script deliberately requires a non-system `TMPDIR` and never writes a package into the Git
worktree. With no `--identity`, it creates an ad-hoc signed development candidate. Supplying a
Developer ID identity performs timestamped hardened-runtime signing; notarization is a separate
release action and is not silently triggered by a local build.

Example:

```sh
export TMPDIR=/path/to/external/task-tmp
./release/build-macos-agent.sh --output /path/to/artifacts/MDD-Agent-macOS
```
