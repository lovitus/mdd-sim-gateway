# Development and reproducible validation

Current product scope and acceptance are versioned in [docs/status/README.md](docs/status/README.md).
Local notes, a green build, historical screenshots and a running PID are not production acceptance.
The supported build/install surfaces are Go binaries plus the embedded React UI and manifest-listed native helpers.
Docker, Python Control, VPCD and Asterisk are retired; they are not local development prerequisites.

## Toolchain and native dependencies

Read the exact Go language/toolchain requirements from `providers/vowifi-go/go.mod`, `go-runtime/go.mod`
and each nested module. Current CI uses Go 1.26.x and Node 24. Run `npm ci`, not an unconstrained dependency upgrade.

For Debian/Ubuntu development of the full Core/Agent GUI, audio helper and VoWiFi Provider:

```bash
sudo apt-get update
sudo apt-get install -y build-essential pkg-config libpcsclite-dev libasound2-dev \
  libgl1-mesa-dev xorg-dev libopencore-amrnb-dev
pkg-config --exists libpcsclite alsa opencore-amrnb
```

`opencore-amrnb` is a cgo link dependency of the Provider media packages, not merely an optional test.
PC/SC and ALSA belong to the native Agent/audio boundary; a source build needs their development headers,
while a customer should use the reviewed release's declared runtime requirements instead.
A missing pkg-config library is an environment/build failure, not a passing or skipped Provider suite.

On macOS, install Xcode Command Line Tools and, for local Provider media tests, the explicit Homebrew dependencies:

```bash
brew install pkg-config opencore-amr
export PKG_CONFIG_PATH="$(brew --prefix opencore-amr)/lib/pkgconfig${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"
pkg-config --exists opencore-amrnb
```

PC/SC/CoreAudio use the platform frameworks. Do not install Linux ALSA packages on macOS or infer that
successful Darwin builds qualify experimental Modem functionality. The cellular companion's libusb/lwIP
versions and hashes are pinned in [agent/cellular-io/THIRD_PARTY.md](agent/cellular-io/THIRD_PARTY.md).
Windows build/install/driver and signing details are exercised in `.github/workflows/go-runtime.yml`;
a Linux race run cannot replace those Windows-native tests.

## Isolated test commands

From a clean checkout with the above toolchain:

```bash
node tools/repository-check.mjs
node tools/repository-check.test.mjs
(cd webui && npm ci && npm run test:all && MDD_KEEP_EMBED_SOURCE=1 npm run build:go)
git diff --exit-code -- go-runtime/internal/webui/assets
go -C go-runtime test -count=1 -timeout 5m ./...
go -C go-runtime test -race -count=1 -timeout 5m ./...
go -C go-runtime vet ./...
go -C providers/vowifi-go test -race -count=1 -timeout 5m ./...
go -C providers/vowifi-go vet ./...
GOWORK=off go -C providers/vowifi-go/upstream/vowifi-go test -race -count=1 -timeout 5m ./engine/swu/... ./runtimehost/...
GOWORK=off go -C providers/vowifi-go/upstream/vowifi-go vet ./engine/swu/... ./runtimehost/...
GOWORK=off go -C agent/call-audio-helper test -race -count=1 ./...
bash go-runtime/scripts/test-liveness-chain.sh
git diff --check
```

Go's parent-module `./...` does not select the separately rooted upstream module's own tests; test it explicitly.
The liveness script uses a separate Provider fixture and loopback-only peers; it does not contact a carrier.
Tests requiring actual systemd, pinned Xray or real devices are opt-in. Record their skip reasons separately;
release installation and Xray CI jobs can provide independent acceptance, but a module skip is not a pass.
Do not run paid SMS/calls, eSIM mutations or disruptive hardware tests without specific authorization.

`npm run test:source-graph` parses the production import graph and rejects orphaned runtime modules and
contract tests importing them. Literal worklet/dynamic imports are included; unsupported dynamic loaders fail closed.
`test:shared-i18n` renders the actual mounted React language context used by global call controls.
Use `node tools/repository-check.mjs --write` to regenerate TODO summaries after editing the acceptance ledger;
CI checks the generated files, original-criterion preservation and evidence-path integrity.

## Source, artifact and running-instance identity

Prefer verified immutable releases when a source rebuild is unnecessary. For a rebuild, record the reviewed commit,
source tree, toolchain, commands, test evidence and artifact SHA-256. Release manifests and exact Provider source/license
archives remain mandatory. Do not create a production artifact by copying edited files into a running installation.
Verify the installed receipt, release manifest and executable revision on the actual host; commit time and process-active
status alone do not prove which version is running. `install`, `start` and explicit `restart` remain distinct operations.

For coordinated rollout, preserve protocol compatibility, exact Agent credentials/pins, desired line intent and ownership.
After an authorized rollout, observe actual control and runtime facts; do not equate a configuration write or HTTP success
with working audio/data or confirmed carrier termination.

## Temporary evidence and safety

Keep captures, screenshots, private receipts, credentials and test databases outside the checkout. On machines configured
with an external-volume task root, respect that choice and set task-local TMPDIR/GOTMPDIR there; use stable shared caches
through MDD_SHARED_CACHE_ROOT or package-manager defaults, not an unbounded new cache per task. Remove only files listed
in the task's own inventory. Never clean broad directories, unknown sockets or other agents' processes by name.

Public evidence must be redacted and bounded. Credentials belong in owner-only product configuration or stdin, never source,
argv examples, logs or bundles. Separate the claims: source verified, automated tests passed, artifact built, installed on a
named authorized host, and real data-plane acceptance. New evidence updates the tracked ledger, not a hidden progress file.
