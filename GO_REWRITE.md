# MDD Go Runtime Architecture

## Current Status

The Go runtime (`mdd-core`, `mdd-provider-apply`, `mdd-egress`, `mdd-vowifi`, `mdd-updater`, `mdd-agent`) and the embedded React WebUI are the sole production and distribution surfaces of MDD VoWiFi SIM Gateway. Legacy Python Control, Docker/Compose orchestration, Asterisk/chan_websocket patches, standalone `agent/go-agent` Card Agent, TCP VPCD (`35963`), and Android VPCD APK surfaces have been permanently retired and are blocked from reintroduction by `tools/repository-check.mjs`.

Implementation status and hardware acceptance boundaries are tracked in [docs/status/README.md](docs/status/README.md). Detailed September–October 2026 migration provenance and design notes are preserved in [go-rewrite-history.md](docs/archive/2026-11-27/go-rewrite-history.md).

---

## Component Ownership

1. **`mdd-core` (`go-runtime/cmd/mdd-core`)**:
   - Single public HTTPS/WSS entrypoint (`8443` by default) serving the embedded React WebUI, admin/management APIs, Agent control/media/data WSS multiplex, and same-origin browser PCM audio relay.
   - Owns durable bbolt stores for line catalog, eUICC deletion notification outbox, SMS conversation history (immutable sequence-cursor pagination), call history, gap-aware availability timelines, and host health alerts.
2. **`mdd-provider-apply` (`go-runtime/cmd/mdd-provider-apply`)**:
   - Privileged local helper that reconciles desired `mdd-vowifi@<line>` systemd instances and coordinates durable drain/maintenance leases across updates and configuration changes.
3. **`mdd-vowifi` (`providers/vowifi-go`)**:
   - One isolated long-lived process per enabled VoWiFi SIM line, running SWu/IKEv2, an in-memory gVisor userspace TCP/IP stack (`netstack`), IMS-AKA registration with carrier backoff/holdoff preservation, SIP SMS, and RTP/AMR-NB voice bridging without host TUN devices or policy routes.
4. **`mdd-egress` (`go-runtime/cmd/mdd-egress`)**:
   - Loopback-only country exit executor managing one shared `sing-box` process, optional Xray bridges, donor traffic-budget accounting, and persistent loopback SOCKS5 relays (`cellular-relays.json`) that decouple borrowed cellular lease switches from shared `sing-box` restarts.
5. **`mdd-updater` (`go-runtime/cmd/mdd-updater`)**:
   - Root update worker that verifies release bundle manifests and permissions, coordinates Core/Provider maintenance drains, switches `/usr/lib/mdd/current`, restarts `Type=exec` core units in order, and records durable progress receipts.
6. **`mdd-agent` (`go-runtime/cmd/mdd-agent`)**:
   - Unified cross-platform client for Windows, macOS, and Linux owning local PC/SC & eUICC sessions, qualified cellular modems (AT/ModemManager/MBN/raw-USB handoff), audio helpers, local owner-only SIM PIN storage, and fail-closed 4G network isolation.

---

## Core State & Safety Invariants

- **Single Authoritative Owner**: Every credential, hardware attachment, paid carrier operation, borrowed data lease, and active call has exactly one owner and monotonic generation/sequence identity.
- **Server-Time Freshness & Explicit Availability**: Fact expiry is evaluated against server receive time rather than potentially skewed client clocks. Expired facts transition to `unknown` rather than fabricated `stopped` or `healthy` states; admission checks explicit `available` predicates per required layer.
- **Unknown Outcomes Never Auto-Retry Paid/Card Effects**: Uncertain SMS submission, USSD/call setup, SIM PIN verification, or eUICC profile mutation outcomes retain locks/receipts and require read-only state reconciliation rather than automatic replay.
- **Consistent Per-Store Backups**: Durable ZIP backups stream read-only `bbolt` transactions (`Tx.WriteTo`) within bounded 128 MiB budgets and exclude raw credentials/tokens.
