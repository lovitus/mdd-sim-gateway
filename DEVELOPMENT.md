# Development and Reproducible Validation

This guide covers toolchain requirements, local and CI validation commands, core architectural invariants, and versioned acceptance ledger maintenance. Historical per-PR design notes and CI trial logs from October–November 2026 are archived in [development-pr-notes-2026-10-to-11.md](docs/archive/2026-11-27/development-pr-notes-2026-10-to-11.md).

---

## 1. Toolchain and Native Dependencies

- **Go**: `1.26.x` (read exact module requirements from `go-runtime/go.mod`, `providers/vowifi-go/go.mod`, `providers/vowifi-go/upstream/vowifi-go/go.mod`, and `agent/call-audio-helper/go.mod`).
- **Node.js**: `24.x` (`npm ci` inside `webui/` and root `node tools/*.mjs`; never run unconstrained dependency upgrades).
- **Linux native headers** (for CGO audio/PC-SC/AMR-NB tests): `libpcsclite-dev`, `libopencore-amrnb-dev`, `libasound2-dev`, `libgl1-mesa-dev`, `xorg-dev`, `pkg-config`.
- **Android preview**: Java `17` (Temurin), Gradle `8.13`, Android SDK `36` / Build-Tools `36.0.0` / NDK `30.0.16248370`.
- **macOS packaging**: macOS `15+`, Xcode Command Line Tools (`lipo`, `codesign`), Fyne `v2.8.1`, static `libusb 1.0.30` and `lwIP 2.2.1` for Universal/arm64 bundles.
- **Temporary & cache directories**: Keep build/test temporary files and caches outside `/tmp` on constrained workstations by setting `TMPDIR`, `GOTMPDIR`, `GOCACHE`, and `GOMODCACHE` to a dedicated external-volume directory, and clean temporary task directories after validation completes.

---

## 2. Isolated Validation Commands

Run from a clean checkout before submitting changes:

```bash
# 1. Versioned acceptance ledger & repository hygiene gates
node tools/repository-check.mjs --write
node tools/repository-check.mjs
node tools/repository-check.test.mjs

# 2. WebUI import-graph, unit/contract suites, and embedded Go asset parity
(cd webui && npm ci && npm run test:all && MDD_KEEP_EMBED_SOURCE=1 npm run build:go)
git diff --exit-code -- go-runtime/internal/webui/assets

# 3. Go Core & Unified Agent module tests, race detector, and vet
go -C go-runtime test -count=1 -timeout 5m ./...
go -C go-runtime test -race -count=1 -timeout 5m ./...
go -C go-runtime vet ./...

# 4. VoWiFi Provider, patched upstream SWu/runtimehost, and call-audio-helper
go -C providers/vowifi-go test -race -count=1 -timeout 5m ./...
go -C providers/vowifi-go vet ./...
GOWORK=off go -C providers/vowifi-go/upstream/vowifi-go test -race -count=1 -timeout 5m ./engine/swu/... ./runtimehost/...
GOWORK=off go -C providers/vowifi-go/upstream/vowifi-go vet ./engine/swu/... ./runtimehost/...
GOWORK=off go -C agent/call-audio-helper test -race -count=1 ./...

# 5. Loopback VoWiFi liveness chain & patch formatting
bash go-runtime/scripts/test-liveness-chain.sh
git diff --check
```

- Parent-module `./...` does **not** traverse nested Go modules (`providers/vowifi-go/upstream/vowifi-go`, `agent/call-audio-helper`, `go-runtime/third_party/euicc-go`); always invoke them explicitly.
- Tests requiring real `systemd`, root namespaces, Windows SCM (`MDDAgent`), or pinned Xray binaries run in dedicated GitHub Actions jobs (`.github/workflows/go-runtime.yml`, `android-agent.yml`, `review-regressions.yml`, `vowifi-liveness.yml`). Never treat a skipped test as hardware or system acceptance, and never trigger paid carrier calls/SMS or live SIM/eSIM mutations during automated testing.

---

## 3. Core Runtime Boundaries and Invariants

### Shared Exits, Donor Budgets, and Borrowed Lease Relays
- `mdd-egress` manages one shared `sing-box` executor alongside persistent per-country loopback SOCKS5 cellular relays (`cellular-relays.json`, mode `0600`).
- Borrowed cellular lease renewal, loss, or donor replacement updates only that country's relay upstream and drains superseded flows without restarting `sing-box` or disturbing unrelated VoWiFi tunnels. When no valid lease is present, the relay fails closed (`ready=false`, `proxy_port=0`) and never falls back to direct host routing.
- Donor traffic budgets in the egress store reserve quota before admission, retain ownership on uncertain `Stop`, and never auto-enable 4G, roaming, or borrowing.

### Cellular Voice and Data Borrowing Coexistence
- On qualified Linux ModemManager/AT modems (static IPv4 and Ethernet MM DHCPv4 with owned route realms and `nftables` cgroup/mark isolation), cellular voice/SMS and borrowed data coexist under explicit owner coordination without releasing physical isolation guards (`mdd-cellular-guard.service`).

### System Update Continuity and Fresh Bootstrap Identity
- Linux server units (`mdd-core.service`, `mdd-egress.service`, `mdd-provider-apply.service`) use `Type=exec` and embed semantic `VERSION` metadata so `/v1/system/update/check` can compare published releases deterministically.
- Fresh server bootstrap generates P-256 ECDSA self-signed TLS certificates (for NSS/Chrome compatibility), while preserving existing server certificates and Agent pins across upgrades.
- `mdd-updater` validates archive permissions and full `manifest.json` digests before staging, coordinates Core maintenance sidecars and Provider lease drains before service cutover, and refuses automatic rollback on uncertain activation.

### Unified Agent Control-Link and Scoped Windows Local Management
- Local `/v1/status` distinguishes local hardware runtime state (`running`) from Core WSS session state (`core_connection`: `connecting`, `connected`, `disconnected`, `retrying`, `stopped`) via `X-MDD-Agent-Core-Connection: 1`.
- On Windows, `MDDAgent` remains the sole `LocalSystem` hardware/isolation owner:
  - When zero smart card readers are present and Windows stops trigger-started `SCardSvr`, `pcscmonitor` verifies via SCM (`StartType != Disabled`, `Stopped`) and SetupAPI (`0` present reader devices/interfaces) before reporting an empty ready reader inventory, reconnecting automatically on reader arrival.
  - Administrators may opt in a specific ordinary-user SID via `mdd-agent service-access -operator-sid <SID> -confirm-connection-authority`. This exposes only redacted state and allowlisted CAS connection edits over a verified local named pipe (granting only `PROCESS_QUERY_LIMITED_INFORMATION | SYNCHRONIZE` on the active service process for peer verification, restored on revoke/shutdown), applied through Core-controlled restart.

---

## 4. Release Provenance and Ledger Maintenance

- **Release Tag Order (`tools/publish-release.mjs`)**: Both tags must point to the exact same verified `main` commit. Push `android-preview-v<VERSION>` first and wait for the prerelease assets (`7` files) to publish; then push `v<VERSION>` so `Go Runtime CI and Release` verifies the matching Android preview and publishes the `8` Go release assets as `Latest`.
- **Acceptance Ledger**: Edit `docs/status/acceptance.json` (never hand-edit generated `TODO.md`, `TODO_MACOS_AGENT.md`, `postponed-tasks.md`, or `docs/status/README.md`), then run `node tools/repository-check.mjs --write` and `node tools/repository-check.test.mjs`. Keep private host addresses, credentials, phone numbers, ICCIDs, and raw logs out of Git history and tracked files.
