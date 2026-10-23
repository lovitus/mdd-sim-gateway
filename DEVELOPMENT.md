# Development and reproducible validation

Current product scope and acceptance are versioned in [docs/status/README.md](docs/status/README.md).
A green build or running PID alone does not prove production deployment. Preserve scoped local and historical
reports for provenance reconciliation; not revalidated by this review does not mean never accepted.
The supported build/install surfaces are Go binaries plus the embedded React UI and manifest-listed native helpers.
Docker, Python Control, VPCD and Asterisk are retired; they are not local development prerequisites.

## Toolchain and native dependencies

### Linux DHCP IPv4 claim lifecycle

October28 original source review accepted f4a3359/tree49a18953. Run40625230150
qualified the full DHCP lifecycle in57.80s: actual cancellation/malformed wire,
fresh exact SIM generation, static recovery and exact same-name replacement.
Receipt complete=true, mainExit=0, cleanupErrors=[], host namespaces/mount unchanged.
Archive `archive/2026-10-28/linux-dhcp-tail-qualified` retains source and one-time
route-proof red/green. The unrelated Core job stopped before recording assertions
because Chrome did not start within its existing15s limit; cause remains unproven.
Remove only the completed PR52-only proof step; retain permanent tests/full E2E.
Product, topology and timeouts are unchanged. Final normal CI and original review
remain required before merge; no production or paid hardware acceptance implied.

October27 owner authorized the fixed successor's route-loss repair. Preserve
c797b05/tree27f49252 and run39986314729 as the actual red: bound cellular sockets
used an unowned on-link route after private-default deletion, without observed
host-default leakage. Add owned-route realms and exact nft classid matching only
for DHCP; retain static/bootstrap behavior. Recheck realm ownership, complete
inventories, numeric interface identity, publication/readback and exact deletion.
The original fixture also checks existing TCP/UDP at that deletion boundary.
Reader timeout remains unconfirmed (both diagnostic suites passed); no speculative
reader edit, production change or paid operation. Full qualification is pending.
The completed implementation review requires iproute2's read-only binary
`route save table all`, `rule save` and `address save` inventories: zero exit,
empty stderr, bounded complete output and strict numeric-object decoding.
This preserves DONE-only interruption detection missing from the pinned Go
transport; mutations still use its numeric-ifindex codec and exclusive ACKs.
The archived PR52-only hosted counterexample step restored the old flat
permit acceptance and zero-exit-only completion behavior and proved named
runtime failures. It is removed after qualification; permanent regressions remain.
The original route-loss red is retained rather than replayed.
Run40475039191 stopped at the unexported `RTA_NH_ID` constant before DHCP
qualification. The revised candidate uses Linux UAPI value30 and includes the
fixed source review's tagged-test import and nft numeric-string realm fixes;
neither that failed build nor source review alone qualifies this batch.
Source-approved fecef696/tree5c96475d failed run40476145065 at the original T2
broadcast ACK assertion; realm counterexamples passed red/green, later lifecycle
was not reached. Retain this red at `archive/2026-10-27/linux-dhcp-t2-red`.
The same reviewer accepted DHCP-only raw broadcast with a frozen lease source,
separate broadcast-capable receive wrapper and the existing exchange write
deadline. T1/data realm checks and original E2E assertions/timeouts are unchanged.
Run40479004518/treeeeec76fa passed T2 and route-only withdrawal, then stopped
at the fixture's slash-form realm argument; use the same full uint32 decimal.
Its reader diagnostic exposed a prior-response/deferred-slot-release assumption;
the fixed review accepted fresh-connection saturation, preserving all original
capacity/scope/time limits. Earlier undiagnosed reader timeouts remain unconfirmed.
Run40482148703/tree3e035ff9 passed reader, permit and realm checks, then failed
the short-lease address assertion. IPv4-only iproute2 output omits addressless
links; owner/reviewer approved all-family readback of the original interface,
strictly no IPv4 and bounded diagnostics. No product or timeout change; actual
cleanup and the remaining lifecycle still require the normal hosted result.

The fixed successor accepted NEXT_BATCH_992c4fb on October 23. Reuse
insomniacslk/dhcp at c6bb46ad3f3a for Ethernet-only MM method3 IPv4, with
claim-owned bootstrap, T1/T2 renewal, expiry, socket invalidation and existing
owner recovery. Preserve static IPv4, user leases/quotas, active-call guards,
configuration, pins and policy. No Core/wire changes, host DHCP scripts, main
routes, resolv.conf writes or automatic enabling. PPP/IPv6/raw-IP DHCP and
non-systemd managed modem remain separate gaps, not this batch's qualification.

Failure checklist: bootstrap exposes ordinary traffic; wrong interface or SIM
renews; DHCP extends user lease/quota; expired sockets revive after same-card
reconnect; malformed options or stale replies publish; late worker downs a new
interface; borrower/persistent owner cannot recover; MM cleanup bypasses a call;
worker shutdown deadlocks. Hosted real DHCP/veth/netns tests must exercise the
production guard, routes and sockets in the fixed Agent cgroup, with a working
alternate default route. Use compiled old-method3 refusal once, then acquisition,
T1 unicast/T2 broadcast, NAK/expiry, owner cleanup and static regression. No
production, paid tests, PR51 qualification replay or frozen PR48 changes.

References: [MM IP setup](https://modemmanager.org/docs/modemmanager/ip-connectivity-setup-in-lte-modems/),
[DHCP upstream](https://github.com/insomniacslk/dhcp/tree/c6bb46ad3f3ada83ff1d61d5b57ed57a590289fd),
[RFC2131](https://www.rfc-editor.org/rfc/rfc2131),
[packet sockets](https://man7.org/linux/man-pages/man7/packet.7.html).
Include BSD-3-Clause and the nclient4 gVisor-derived Apache-2.0 notice in the
existing source/license inventory; do not upgrade unrelated dependencies.

`tools/test-linux-dhcp.mjs` invokes the tagged existing-package Go E2E under the
fixed `mdd-agent.service` cgroup on disposable hosted ARM64. Three real namespaces
and veth paths exercise the original guard, DHCP server/packet exchange, marked
routes, TCP/UDP WebSocket borrowing, DNS and original durable policy recovery.
Only MM/AT and USB-parent sysfs stand in for hardware; a narrow fixture adapter
substitutes that parent check while calling the original contract/netdev guard.
The peer is outside the Agent cgroup. Run39942634074 qualified the compiled old
method3 entry refusal and the failed-inventory cleanup red/green counterexample;
their one-time machinery is archived at `archive/2026-10-23/linux-dhcp-counterexamples`.
Current full CI/source review are
pending; this is not physical modem or production evidence. DNS changes invalidate
the old claim and rebuild through the original owner; unadvertised renewal DNS
inherits only that same validated lease. DHCP MTU/hostname/search/NTP options are
not applied to the host. The R1-R5 source review is addressed in the same batch:
datagram DNS semantics, voice-protected partial MM cleanup, expiry before permit
publication, failed-link-inventory ownership and the approved negative traffic /
lease cases. Run39939080172 did not qualify DHCP; its E2E failed and root-only
scratch files also blocked artifact upload. The revised harness emits the result
and exports readable receipts before deleting only its own scratch directory.
Run39942634074 reached DORA, then failed permit publication because the fixed
cgroup path was unavailable after `ip netns exec` replaced sysfs. The reviewed
second harness correction preserves the existing cgroup2 mount with read-only
binds in private mount namespaces, validates exact process membership, and
compares the host's full cgroup mountinfo before/after cleanup. No new cgroup2
mount, fake hierarchy or weakened guard is allowed. Current R3/R5 refinements
serialize expiry decisions with renewals and independently qualify route/permit
withdrawal using chain-specific drop counters and a fenced packet observer.
If this second correction still fails, preserve evidence and stop automatic
test repairs. This does not qualify borrower lifecycle or physical hardware yet.

### Linux ARM64 standalone delivery

The owner-authorized successor fixed reviewer approved `LINUX_AGENT_PLAN_4297696`
at main42976962. Complete the existing Agent package with its pinned malgo0.11.26
helper, original guard/Agent units, source/license inventory and fresh standalone
placement. Reuse the existing MM staticIPv4 and serial AT/SIM paths; serial has
no data backend. Do not modify Core, the full-server updater, runtime identity,
policy, guard or reader/cron contracts. No dependency upgrade is needed.

Failure checklist: missing helper discovered after guard mutation; helper -h
mistaken for ALSA initialization; foreign/Core/unit/drop-in ownership overwritten;
preflight succeeds but a target changes before install; config ready:false treated
as valid; explicit config loses adjacent state or violates the original sandbox;
new placement auto-starts hardware; a hosted kernel prerequisite is skipped as
passed; zero-device readiness mistaken for physical modem/audio/no-leak acceptance.

Native ARM64 hosted qualification must extract the final tar independently, use
the real CLI/helper/installer, validate default and explicit config paths and
refusals, and explicitly start the original unit on an empty disposable host to
observe guard persistence, VHCI authorization and fixed cgroup. Missing hosted
prerequisites fail qualification. Candidate a891f5d / Go39907297621 passed the
complete final-tar journey, including actual old-package/installer counterexamples,
original guard loading and hosted ancestor-permission restoration. The exact
proof is retained at archive/2026-10-23/linux-agent-qualified; temporary old-source
dependencies are removed from normal CI, not the current behavior assertions.
No local tests/builds or production probes. Final cleanup CI/review and public
delivery remain separate; physical hardware and unsupported bearer paths remain
unqualified. Exact evidence is recorded in docs/branches.md.

References: [ModemManager IP connectivity](https://modemmanager.org/docs/modemmanager/ip-connectivity-setup/),
[malgo0.11.26](https://github.com/gen2brain/malgo/tree/v0.11.26),
[systemd execution sandbox](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html),
[USB authorization](https://docs.kernel.org/usb/authorization.html).

### October 22 completion goal

The owner requests completion of the remaining implementation and as much missing
acceptance as can be automated, plus usable Agent colors and configuration. This
is an ongoing goal, not a claim that the first delivery batch closes all work.
Preserve the existing ledger identities and accepted evidence. The implementation
queue includes Windows least-privilege service separation, native/embedded Linux
modem and missing bearer support, and macOS Universal packaging. The acceptance
queue includes update restart continuity, eSIM location/operation recovery,
archive/restore, communication ownership, country exits and donor budgets.
Long-duration/hardware cases may be attempted only with the required environment;
unsupported or unavailable evidence must not become a pass. Historical incident
causes remain unknown unless new evidence identifies them. Android local modem,
abandoned soft deletion, notarization and landscape remain excluded. Existing
paid-operation, destructive-action, production-maintenance and isolation gates
still apply; the goal is not permission to repeat paid tests or interrupt leases.

The first batch addresses the existing Fyne desktop Agent, not a new framework:
readable local/Core/device status, structured device rows, separate diagnostics,
clear primary and destructive actions, and the existing three setup methods.
Use Fyne 2.8.1's existing theme, AppTabs and Form APIs (the current upstream
release, checked October 22). Existing configuration identity, pin, private
permissions, pending setup and capability defaults remain authoritative. A saved
configuration must never be labeled active until the actual runtime uses it.
Android's Material client remains in the same goal for a separate actual-screen
check and interaction pass; completing desktop work does not close Android work.

Failure checklist: gray disabled-looking primary controls; unknown/off interpreted
as healthy; local runtime mistaken for Core or call readiness; topology loss
leaving stale device health; periodic refresh replacing edits or selection;
credential/pin leakage through diagnostics; save silently starting hardware or
rotating identity; existing config bypassing edited inputs; repeated destructive
actions or an old result updating a newer dialog. Use the existing GUI test suite
and GitHub-hosted jobs for behavioral red/green and qualified artifacts, followed
by actual native window checks. Do not run local builds or duplicate accepted
hardware/paid tests.

References: [Fyne themes](https://docs.fyne.io/extend/custom-theme/),
[AppTabs](https://docs.fyne.io/container/apptabs/),
[upstream releases](https://github.com/fyne-io/fyne/releases).

### Android status and configuration follow-up

The October23 independent client batch starts at main `1bfcf9a`; the original
reviewer approved its plan (`dcaa936f`) and implementation (`d6055e50`).
Reuse Material 1.14.0 and existing Views/UiLabels: separate Home directory warnings
from ready counts, reveal a proven original reply card without reordering or
changing selection, and color actual draft persistence outcomes. Editing clears
stale saved feedback. No Core, credentials, account/identity guards, confirmation,
USB recovery, user intent or hardware behavior changes.

Failure checklist: partial/offline facts inherit ready green; unavailable service
keeps old colors; long reply lists hide the checked card or select an unknown one;
a refresh overrides the user's selection; failed/stale writes look saved or
replace unsaved input. Three native regressions use existing TLS/Activity fixtures.
Archived API35 proof compiled old MainActivity with the new tests, observed three
named behavioral failures, then restored exact candidate bytes; API28/35 each
passed44 methods in Android39856193041. Temporary proof is removed; normal tests
and AndroidX screenshot collection remain. Inspected API35 portrait images are
UI evidence, not phone, carrier, long-session or USB qualification; current phone
verification remains owner-skipped. PR48 stays separate and frozen pending direction.
Final Android39860851491 and source cleanup review passed; full Go39860851473
was cancelled during Windows dependency preparation, not product compilation.
The reviewed CI-only increment makes that existing step observable and bounded:
strict update180s, install300s, each with15s TERM-to-KILL grace; nonzero status
stops the job unchanged. No retry, mirror/cache/package change or larger job budget.
The next normal hosted run qualifies this candidate; it does not repeat the archived
Android red proof or prove the earlier cancellation's root cause.
Diagnostic Go39868592415 located the current failure in Azure-mirror package
downloads (install300s/124 after update9s/0). The reviewed follow-up validates the
runner's existing mirror-file layout and removes only its exact Azure entry,
preserving official HTTPS archive/security fallbacks, source checksum and signing.
No timeout expansion or automatic retry; unexpected layout fails. Reference:
[corresponding runner image](https://github.com/actions/runner-images/blob/ubuntu24/20261012.334/images/ubuntu/scripts/build/configure-apt-sources.sh).
References: [ListView selection](https://developer.android.com/reference/android/widget/ListView#setSelection(int)),
[AndroidX test output](https://github.com/android/android-test/blob/main/runner/monitor/java/androidx/test/platform/io/PlatformTestStorage.java).

Read the exact Go language/toolchain requirements from `providers/vowifi-go/go.mod`, `go-runtime/go.mod`
and each nested module. Current CI uses Go 1.26.x and Node 24. Run `npm ci`, not an unconstrained dependency upgrade.

### macOS Universal packaging

Original plan review `d84e2d8f` approves this October23 batch on main `5c4e1a9`.
Keep Fyne 2.8.1/tools 1.7.2, libusb 1.0.30 and lwIP 2.2.1. Build host-native
packaging tools with explicit arm64/amd64 child targets, then combine all six
Mach-O files with Apple lipo and sign from inner components to the App. Both
inputs must share source/tree/version/resources/signing; retain the license union.
The complete package requires macOS15+, with the original Team for signed builds.

Failure checklist: host architecture leaks into Intel output; one helper stays
thin; input resources or source disagree; unsigned or wrong-Team output replaces
production; an incompatible package stops the old Agent before rejection; a new
archive is uploaded but omitted from the download index. No runtime or user-state
changes. Existing arm64 filenames/installer and all normal gates remain.

Hosted E2E forces each final slice through real CLI/App config init/show in private
temporary paths, both audio helpers through -h and both cellular helpers through
exact missing-argument exit2. Inherited media/config environments are removed.
It checks unchanged inputs/config and installer refusal before deployment state.
Intel execution is Rosetta, not Intel hardware acceptance. Qualified03f5c77 /
Go39885181111 proves the old publisher's missing-Universal assertion and the old
installer's acceptance of an incomplete helper; evidence/tag are in docs/branches.md.
Temporary proof is removed; no local tests run. Final5180618 passed original
review, Go39886954243 and Android39886954195; merge42976962 shares tree4d7f0ffc.
Main39888741520 qualified original-Team signed packages and both final slices;
original final review2ac1684a accepted the exact artifacts (PR50 receipt6356021902).
Publication, installation and Intel physical-hardware acceptance remain separate.
Do not change existing v2.0.0 tags/assets or repeat paid/deferred tests.

References: [Apple Universal binaries](https://developer.apple.com/documentation/apple-silicon/building-a-universal-macos-binary),
[pinned Fyne builder](https://github.com/fyne-io/tools/blob/v1.7.2/cmd/fyne/internal/commands/build.go),
[Apple synthesized designated requirements](https://developer.apple.com/library/archive/technotes/tn2206/),
[Apple lipo argument parser](https://github.com/apple-oss-distributions/cctools/blob/main/misc/lipo.c).

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

## Shared exits and donor budgets

The current batch retains one shared sing-box executor and the existing Xray
bridge. Country-local preparation/render errors omit only that country's route;
temporary donor-route changes require the existing drained apply. Runtime
confirmation binds the attempt, full config identity and fresh executor status.
The apply helper retains its exact maintenance set durably and recovers that
original attempt after interruption, without acquiring a new lease or publishing
new desired state. Revoked donor credentials are not rollback candidates.

Donor budgets use the existing egress store, keyed by ICCID. Reservation precedes
Agent admission, renewal keeps its cap, and unknown Stop retains ownership.
Interrupted unobserved allowance is explicitly uncertain, not measured usage.
Reset requires confirmation and no outstanding owner. Limits never enable 4G,
roaming or borrowing. Earlier/carrier-billed usage is not reconstructed.
Slow sibling I/O cannot block the existing lease keeper. Revoked owners are not
renewed; a dead bridge is not permission to replace the healthy shared process.
Maintenance cleanup persists each original owner's progress independently;
budget confirmation belongs to the original mounted editor and frozen revision.
Initial budget loading is serialized with editor operations. Unchanged consumer
recovery leaves its healthy bridge untouched. A checked rejection whose old donor
is revoked finishes the already-drained attempt only after confirmed pair stop;
unknown Stop still retains protection. This terminal failure is not exit health.

Failure checklist: one failed country strands healthy leases; stale status
acknowledges a new apply; helper restart releases another generation's lease;
changed temporary credentials bypass maintenance; reset/rename/restart refunds
an unknown owner; measured bytes are confused with an unused reservation.
Focused hosted coverage includes real helper/executor publication and listener
activation, Core SOCKS TCP/UDP through the Agent manager, durable budget restart
and admission/reset concurrency, and the mounted network page. Proxy, modem and
carrier peers are synthetic; only the existing field observations are physical
evidence. No new paid acceptance is implied.

## Agent control-link status

The owner's missing-connection-warning report is independent of Android and of
the underlying transport fault. The Agent's authenticated local `/v1/status`
includes `core_connection` from its existing WSS worker. `running` still means
local hardware ownership; `connected` means Core acknowledged the Agent hello,
not that any SIM, voice path or data session is healthy. CLI readers and the desktop
GUI can distinguish connecting, connected, disconnected, retrying and stopped.
Older services without this optional field display unknown, never connected.

Failure boundaries: an offline Core must not stop local isolation; stopping the
runtime must not leave a retained connected indication; a pending reconnect must
show its existing retry deadline; status must not disclose tokens or raw transport
errors. The GUI displays a persistent colored warning, not repeated modal dialogs.
This change does not alter reconnect timing, TLS, Core endpoints, notifications,
user switches or hardware policy. Original failure details remain in local logs.

PR #14 source 38f1191 passed the full signed workflow 36088021096 and retained
isolated macOS/Windows GUI connected, retrying and reconnected checks. This is
not production rollout or repair of the underlying transport cause. The September
29 integration retains those four runtime files byte-for-byte and repairs the
local HTTP compatibility boundary: only requests with
`X-MDD-Agent-Core-Connection: 1` receive the optional field. Older requests keep
the strict pre-extension format on status, start/stop and transition errors.
New clients request the field and tolerate its absence on older services without
weakening strict decoding or authentication. The bounded mixed-version regression
and exact hosted result are recorded separately in docs/branches.md.

## Source, artifact and running-instance identity

The N10/N11/N12 update candidate keeps a Core-owned maintenance sidecar separate
from root-worker progress. Persisted exact Provider identities and lease transitions
precede external effects; the replacement Core restores admission before listening.
The target manifest must declare update maintenance contract 1. All three fixed
services must run the target Core image and record ordered restart receipts before
the original leases are released. Unknown activation never triggers automatic
rollback, restart or a second install. `mdd-updater -reconcile` only completes the
original pre-activation cleanup/release or worker receipt; uncertain activation
requires diagnosis. The first upgrade from a Core without this contract remains
an operator-controlled deployment, not an in-app self-update acceptance claim.
Failure boundaries include active/preparing media, in-flight writes, late/replaced
Providers, lost drain/release acknowledgements, unreadable worker status and a
released sidecar with a missing worker receipt. Candidate verification is tracked
in `SYSTEM_UPDATE_CONTINUITY`; no deployment or hardware acceptance is implied.

Worker publication reads use the same Core Store lock as request publication.
Missing completion receipts retain the current request, including released leases.
A post-rename sync failure retains the new revision and blocks effects until exact
readback and directory sync succeed; explicit reconciliation never retries activation.
The existing installation file-lock primitive also covers the entire detached worker
invocation, so explicit reconciliation cannot cancel an active download. Corrupt worker
progress does not prevent verified original maintenance cleanup; reopened request
publication rechecks file and directory durability before granting preparation.

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

## Scope/acceptance ledger changes

Owner scope decisions live in `docs/decisions/`; `docs/status/acceptance.json` separates current requirements,
implementation, scoped historical evidence and current review coverage. Original criteria remain immutable history,
but workstreams may complete, be accepted, superseded, deferred or cancelled with rationale and evidence.
Existing reports not revalidated this round are reconciled before requesting new field tests.

```bash
node tools/repository-check.mjs --write
node tools/repository-check.mjs
node tools/repository-check.test.mjs
node tools/acceptance-policy.test.mjs
node tools/acceptance-evolution.test.mjs
```

The normal CI runs the scope/transition regressions through the existing repository-check.test.mjs gate.
`node tools/verify-acceptance-baseline.mjs <checkout-at-19f966f>` additionally proves the four old-ledger failures without changing production code. Its dated `acceptance-regressions.mjs` assertions describe the original correction snapshot only; they are not permanent current-state gates. Normal tests exercise documented future completion, changed scope and reconciled historical evidence through the full checker without freezing today's counts or statuses.
This check does not perform SIM deletion/replay, paid operations, hardware tests or deployment.
