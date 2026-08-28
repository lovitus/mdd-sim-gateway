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
health; those facts must arrive through the separate authoritative state path.

The script deliberately requires a non-system `TMPDIR` and never writes a package into the Git
worktree. With no `--identity`, it creates an ad-hoc signed development candidate. Supplying a
Developer ID identity performs timestamped hardened-runtime signing; notarization is a separate
release action and is not silently triggered by a local build.

Example:

```sh
export TMPDIR=/path/to/external/task-tmp
./release/build-macos-agent.sh --output /path/to/artifacts/MDD-Agent-macOS
```
