# Customized MDD User Interface

Source: this repository, immediately before the first Go runtime rewrite.
Revision: ec620942e93edbbb567398acda4c0dffe1d8f375
First Go rewrite: 95c38bbca2ef87559d988e57a38ecc50b02ba685

The customized frontend source files were copied together, retaining the original pages,
selectors, interactions, styles and translations. This directory is now mounted
by the working-tree and production entrypoints. Feature parity and acceptance
remain incomplete; mounted pages alone are not proof of restored behavior.

The mounted UI replaces the legacy HTTP/WebSocket adapter with the existing Go
contracts and adapts the customized PCM call interface to the Go call coordinator.
Do not restore Python, Docker or a second call owner. Preserve exact line/card
identity and the user's latest aggregation and data-switch requirements. eSIM
deletion remains excluded pending the final interactive phase.

## Go Adapter Work

User-requested advanced diagnostics now aggregates the existing DiagnosticsV1
contracts inside the copied Diagnostics page. A manual read-only run checks
browser capabilities and existing Core, Agent, Provider, exit and line facts.
Unknown, stale and intentionally disabled states remain distinct. It does not
probe hardware, change switches, restart components, send notifications or place
carrier calls. Browser media is a separate explicit no-charge action using the
existing coordinator. Reports omit device/line identities and raw API payloads.
This is a bounded smoke-check surface, not an automatic audio-quality project or
evidence that every device's physical functions were exercised.

The interface comparison uses static API references in this copied tree against
the active `../api.js`. A matching name is not assumed to mean matching fields.

| Original module | Go adaptation required |
| --- | --- |
| App / selectors | Preserve the ten-page shell and stable selection; map Go snapshots and incoming-call events. |
| Messages | Adapt threads/messages/delete/send/ack to durable Go history and submission IDs; preserve the copied loading and stale-response guards. Add the user-requested aggregate scope. |
| Softphone / callCoordinator / browserMedia | Keep original dialpad, call controls and history; bind operations and events to the single Go call coordinator and its media leases. Do not run both owners. |
| UnifiedPages devices | Map hardware, policy, SIM discovery and raw ownership without changing saved switches. |
| UnifiedPages network | Map saved profile/exit configuration and explicit test results; closed borrowing or 4G intent fails the test without changing settings. |
| UnifiedPages notifications | Map redacted credential views and explicit secret patches; empty unchanged inputs preserve credentials. Verify real event intake, not merely test delivery. |
| UnifiedPages system | Map backups, maintenance, scoped Agent credentials and Go update states; never restore shared credentials or container lifecycle. |
| SimConfig | Map exact reader/card/session, catalog CAS, provision/reprovision and PIN proofs; no index-based card writes. |
| Esim | Adapt typed EID/profile operations and download receipts. Keep deletion unavailable for the deferred interactive phase. |
| Logs / VowifiHistory / AllowancePanel | Use existing Go diagnostic, availability and allowance APIs; preserve missing-data semantics. |

No copied module is counted as restored until it is wired and exercised through
the corresponding user actions against actual Go responses.

The original API module now delegates exclusively to the Go adapter. The history
adapter maps original thread/message/call/log fields while preserving backend
identities and typed statuses. Scoped clearing uses a Go transaction over the whole
selected line rather than only the visible page, and rejects active call records.
The working-tree and production entrypoints use this App and its stylesheet.
Remaining contracts and acceptance still block a claim of full restoration.

The SIM form retains the original fields while separating catalog saves from
explicit Agent PIN actions. New-card Save reaches the existing disabled-draft
claim/save adapter; modem identity refresh reads fresh Go telemetry and rejects
a changed card. Reader readback remains the existing hardware operation. Pending
readback cannot fill a different selected SIM form. The original two-panel layout
stacks on narrow screens; actual 390px and 1280px browser measurements are recorded
in the recovery cursor. These corrections do not prove new-card provisioning or
PIN hardware acceptance. The bound hardware label now comes from the typed device,
including empty readers, rather than a saved enumeration index. Card readback and
PIN feedback are cleared on identity changes; late PIN responses cannot replace
the newly selected card's feedback. Empty readers do not offer PIN operations.
Actual browser acceptance covered the production Linux empty reader and switching
back to an existing SIM without issuing card commands.

User scope decision (2026-09-08): retain the existing balance/data-allowance
query implementation, but exclude it from current development and proactive
acceptance. Only perform its manual validation when the customer explicitly
requests it. Missing allowance-rule or verification evidence is not a blocker
for the remaining frontend work; do not remove the feature.

The copied App now instantiates the existing Go coordinator once for both transports
and its global incoming-call overlay. Softphone retains the customized page structure,
but delegates signalling/media/mute/DTMF/hangup to that owner. Unknown start and
unconfirmed teardown remain visible; leaving the page does not close a second local
phone or pretend the hardware is idle. The old recording buttons were backed by
unimplemented stubs and are not presented as a working recording feature.
Call history defaults to all lines with line-scoped deletion; the independent plus
key is dial-only, never a DTMF tone. WebSocket snapshots supersede older REST reads.
Hardware acceptance remains tracked separately in the current recovery cursor.
Source-shape checks are not browser or call acceptance evidence.

The original manual call-stability entry was still calling a missing coordinator
method. Its ec620942 timer/observer workflow is now adapted to the same Go call
owner, with one VoWiFi call, exact hangup, bounded setup and terminal readback.
Duration comes from the exact durable call record rather than including the
verification delay. Missing termination/duration is not a pass, and there is no
automatic cellular fallback. Deterministic tests do not place carrier calls;
real stability acceptance remains separate and subject to the roaming-cost limits.

Browser acceptance exposed a missing-input-device error whose detail disappeared
with the toast. The call page now retains that error, awaits microphone acquisition
before allocating a media lease, and cancels pending acquisition and media tests
on explicit cancellation, route changes or logout. Late microphone streams are
closed and cancelled tests are never counted as passed. The earlier in-app browser
limitation is not a current missing-device conclusion. Production Chrome passed
the no-charge media check and one authorized outgoing call reached active and
ended through the copied page. Exact history and idle Provider readback confirm
termination, not waveform quality, clipping, dropout or bidirectional speech quality.

Messages now retains the copied conversation/bubble layout with all-line history.
Conversation identities include line, transport and peer; replies and deletion use
that exact scope. Expanded history keeps its server cursors and refreshes loaded
pages rather than replacing them with the newest page. Delivery reports correlate
per part and retain their real error fields. Cellular submission history now keeps
the request body after the existing operation-identity validation; old missing
bodies are not fabricated and retries do not submit again to the Agent.

## Network and form-contract batch

The next batch preserves the original `sim_iccid` field in network forms and only
offers modem SIMs for data borrowing. Missing inventory does not erase a saved
binding. Save/apply/readback failures remain distinct; application must confirm
the exact saved revision. Allowance forms submit their observed revision instead
of silently fetching a newer revision before overwriting concurrent edits.

Existing outbound selection is ported from `host/mdd_orchestrator.py` at the source
revision above. Only the selected UDP-capable outbound and named detour dependencies
are imported. Source listeners, routing and DNS settings are not imported. The
source file must be readable by the executor; its digest participates in explicit
Apply, and a changed file cannot satisfy an older application request.

Clash subscription conversion and last-good caching use the same source module.
Country keyword token matching, UDP filtering, TLS/REALITY and stable node-name
selection are retained. Refresh failure retains a usable cache. A background pool
change does not restart the active process: explicit Apply first acquires the
existing Provider maintenance leases, publishes a new generation, and resumes
only after runtime confirmation. Unknown publication/application retains the
leases for recovery through the existing maintenance page.

The XHTTP boundary decision is resolved: the Go executor owns an independent
Xray process. Subscription and first-hop manual VLESS/XHTTP conversion, private
loopback bridges, paired activation/rollback, bridge-only recovery and isolated
profile testing are implemented. CI exercised real Xray REALITY/XHTTP UDP in a
loopback-only namespace. This is not evidence of production XHTTP node quality.
Automatic exit recovery has a separate durable ledger and guarded apply path;
real fault-switching acceptance remains distinct from parsing and CI evidence.
Current deployment and workflow identities are maintained only in the recovery
cursor. Do not generate paid traffic or manufacture failures to fill evidence gaps.

## Remaining Original UI Gaps

The serial-only batch directly adapts `ec620942`'s profiled USB/AT discovery and
host ModemManager switching. It reuses the existing AT owner, persistent Linux
guard, helper transport, atomic config writer, Provider maintenance and systemd
executor. The General hardware control now has a save-and-switch path with exact
host/Core binding, retained user switches, loaded-config readback and rollback.
This is not a general remote-Agent switch. A validation Agent cannot be adopted
through a conventional config filename. The batch passed CI. The user-authorized
Linux migration and exact production host binding are now complete, and Chrome
shows the original hardware control. No mode transition was performed; binding
and rendering do not prove switching acceptance.

The current new-device-defaults batch connects the original General controls to
CAS preferences, imports the original device defaults without overwriting Go
choices, and uses the existing Agent policy, draft, preparation, provision and
Provider apply paths. New automatic Provider apply is restricted to one added
line and cannot publish unrelated saved changes. MNC length comes from an
explicit EF_AD read; the original MCC-country JSON is copied unchanged. This
batch passed complete CI and deployed settings save/readback acceptance. Real
new-device automatic provisioning remains unverified. Older disabled-defaults
descriptions are historical, not outstanding implementation work.

The following are confirmed by comparing `ec620942` with the mounted
`views/UnifiedPages.jsx` and its Go adapter. This is a correction to the existing
module matrix, not a claim that all other original actions have passed acceptance.

| Original action | Current boundary | Required closure |
| --- | --- | --- |
| New modem 4G/VoWiFi defaults | Deployed in a57bdb0. Original persisted defaults were restored through the production browser; save and re-entry passed, with existing lines and notification configuration unchanged. | Real new-modem automatic provisioning remains unverified. Do not repeat the completed settings-save acceptance. |
| VoWiFi-only hardware mode | Implementation passed full CI. Linux Agent migration and exact production host binding are complete; Chrome displays the original control. | Real mode switching remains unverified. Current auto mode and all device switches were preserved. |
| Web bind/port/certificate paths | Saved startup settings are editable through the existing helper; the production browser saved/read back unchanged values. | Changed-value persistence/backup has CI coverage; no production port/certificate change was performed. Domain/self-signed fields remain actual certificate information, not an automatic certificate-issuance feature. |
| Retry count and interval | Original inputs, Go persistence, per-line overrides and continuous-failure window are connected; the production page saved/read back 3/40. | Fault-window behavior is covered by CI, not a manufactured production failure; exit-recovery strike counts remain separate. |
| Rekey default | Go catalog/provider and original form are already connected. | Retain existing evidence; do not treat this as another missing implementation merely because it appears beside disabled retry fields. |
| eSIM deletion customization | Intentionally unavailable. | Only after all other original functions, through the user-requested interactive final phase. |

Opening all main routes and tabs verifies navigation and rendering only. It does
not prove saving, actions, persistence, notification delivery, calls, SMS or
hardware behavior. Keep missing implementation separate from missing acceptance.
Agent platform delivery (including Android readers and persistent modem capture)
also cannot be certified by this frontend matrix.

Source verification for the two remaining General settings controls:
`ec620942 control/app/device_state.py:23,302` defaults to cellular=false,
VoWiFi=true, flight=false, roaming=false; partial default edits retain the other
values and apply only to future hardware IDs. The earlier all-false Go fallback
was corrected in a57bdb0 and covered by CI. Production browser acceptance restored
the old persisted false/true/false/false defaults; existing device choices were
not overwritten. This does not substitute for new-device hardware acceptance.
`ec620942 host/mdd_orchestrator.py:1339,1637-1663,3171` implements serial mode by
switching discovery/bridges as well as stopping/disabling ModemManager. Current
`go-runtime/internal/linuxmodem/prober_linux.go:77` opens ModemManager to discover
devices. Merely adding a helper service-stop action would remove discovery and
does not restore the original feature. A serial discovery/owner path must exist
before wiring that old checkbox; readers and remote Windows/macOS are unaffected
by this original Linux-host control. These are unimplemented requirements, not
new user decisions or reasons to claim the remaining settings work complete.

The original retry controls are consumed by `ec620942 control/app/main.py`
`_health_recovery_due` and `apply_health`: `max * interval` bounds a continuous
failure window, rather than counting HTTP failures or exit-selection strikes.
The old fallback is max=3 and interval=40 seconds, with minimums 1 and 5.
Recovery still requires an exact idle generation and no maintenance owner;
healthy status resets the window, while missing cards/PIN failures follow their
own handling. Restoring these controls must retain those consumers and gates,
not merely expose existing Go backoff fields under the old labels.

The original Web settings are startup settings: `ec620942 control/run.py:164-191`
reads bind/port at process start, prefers an existing configured certificate/key
pair, and otherwise creates/reuses the private self-signed pair. Saving the old
form does not change an already-running listener. The `domain` field also feeds
legacy manager URLs; it is not automatically a certificate SAN. An adapter must
not claim that saving display metadata changed TLS or the live port. This
remaining backend gap does not authorize a new general TLS-management framework.

## eSIM information and one-time download tracking

The original default SM-DP+ and free-NVM fields map to optional Agent information
with separate availability flags. Read-only inventory uses the existing
euicc-go address/Info2 APIs; operation admission does not add these queries.
Older Agents remain readable without inventing zero capacity. Core must accept
the optional fields before the corresponding reader Agents are upgraded.

Before a download POST, the browser stores only its reader/EID/operation-ID
reference, never an activation or confirmation code. An unknown response is
observed through the original operation, not resubmitted. An older completed job
cannot overwrite this reference. Explicitly stopping local tracking does not
cancel a card operation, and the UI warns that a new request could duplicate it.
Results from a different reader/EID do not populate the currently selected card.
Short-screen download dialogs scroll internally and remain cancellable.

These source contracts still require batch CI and actual non-destructive field
readback. No profile installation or deletion is implied by the implementation.

Explicit chip reading now uses a read-only EID inventory refresh, including an
empty eUICC, through the existing Agent session and inspection functions. Older
Agents with a current ICCID use the existing reader-readback operation instead;
cache loading never triggers this refresh. Notification read failure remains
visible without discarding successful profile readback. Core must precede new
Agent deployment. CI and one actual empty-chip browser refresh are recorded in
the recovery cursor; this does not certify profile writes or every reader host.

## Recovery diagnostics and persistence boundary

Known IMS start failures retain the IMS layer; an unconfirmed tunnel state is
not relabeled as a confirmed tunnel failure. A failed start exposes an opaque,
stable failure identity independent of snapshot-read sequence numbers.

The ported exit policy has a once-per-failure entry point. Its ledger is stored
with optimistic revision checks in the existing events database metadata, not
the desired-state catalog or another database. Duplicate writes do not advance
the revision, and permanent event-line purge removes and fences this state.
This foundation was subsequently connected to the recovery workflow described
below. Real fault-switching acceptance remains distinct from implemented
controller and node-selection integration; do not rebuild the foundation as a gap.

## Browser acceptance follow-up

The copied call history stages the original line, transport and number without
dialling; current-line readiness must not prevent selecting another line's record.
Hardware readiness is not a claim that this browser's audio has been verified.
eSIM profile availability distinguishes a confirmed empty inventory from missing
facts, and the download form reflects the Go API's required IMEI contract.

Settings read independent Go domains separately. A notification or catalog read
failure does not blank unrelated settings; the affected save remains unavailable,
and authentication failures are not converted into partial success. Missing audio
values remain absent rather than becoming a saved default. Runtime listener facts
populate the original separate address and port fields without exposing key paths.
Validation and deployment evidence for this batch is tracked in the recovery cursor;
source adaptation alone does not establish carrier-call or profile-write acceptance.

## Recovery workflow adaptation

The old failover policy and main.py health gates feed generation-fenced durable
decisions in the existing events database. IKE transport counters are separate
from the old retransmit input. Only complete unanswered bootstrap evidence is
eligible; user pins, healthy shared-exit peers, active calls and maintenance
remain gates. Runtime selections do not overwrite the saved pin policy.

The existing apply helper executes an exact recovery target under its usual
lock, revision checks and maintenance leases. Requests persist before dispatch,
use the same identity on retry and require target-node readback. An unresolved
request retains protection and blocks destructive line cleanup. The original
verification page shows its status and offers one explicit retry after the
automatic budget, never a clear-protection shortcut.

The original line_unrecoverable notification uses the existing channels and
deduplication store, with atomic event outbox publication. Existing Go channel
settings do not silently subscribe to the newly supported event; legacy import
preserves the original explicit choice. Deployment and real acceptance remain
separate evidence gates in the recovery cursor.

## Administrative audit settings

The security page's audit switch and trusted proxy list are ported from
ec620942 main.py `_audit_client`, `audit_mutations` and `_write_audit_record`.
The Go adapter uses the existing versioned preferences store. Trusted proxy
prefixes affect only the audit client address, never authentication or limits.
Public mutation responses are recorded without bodies, headers, query strings
or concrete path parameters. Authentication routes retain only fixed action
names. Audit storage is bounded in events.db and included in existing backups;
the original security page exposes manual history reads without polling.

## Update networking

The original update_check.py selection, candidate ordering and proxy resolution
are adapted to the current Go stores. Existing Go installations retain direct
networking until the user changes the original form. Auto mode tries direct
then eligible proxy-library entries; explicit selection never silently becomes
direct. Country-backed entries require a confirmed runtime generation.

The successful route's non-secret identity is recorded with the update request.
The updater re-resolves the same profile/country and verifies configuration and
preference revisions before downloading. Proxy credentials are not serialized,
and local-token policy checks do not follow redirects. Metered cellular-SIM
update downloads remain excluded pending an explicit cost policy. Tests for
proxy fallback use the existing SOCKS server library and a trusted fixture CA,
not external traffic or relaxed TLS verification.

## Management safety and certificate readback

Provider preflight and the locked maintenance entry both reject ringing calls,
not just active calls. Refused maintenance does not acquire a lease.
The original Web/security fields read the certificate actually loaded by Core:
self-signature, DNS names and expiry, with missing evidence remaining unknown.
This does not expose key paths, enable certificate editing or equate a
non-self-signed certificate with a validated trust chain. Support bundles keep
certificate identifiers redacted.
