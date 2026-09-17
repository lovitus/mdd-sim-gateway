# Customized MDD User Interface

Source: this repository, immediately before the first Go runtime rewrite.
Revision: ec620942e93edbbb567398acda4c0dffe1d8f375
First Go rewrite: 95c38bbca2ef87559d988e57a38ecc50b02ba685

The customized frontend source files were copied together, retaining the original pages,
selectors, interactions, styles and translations. This directory is mounted
by the working-tree and production entrypoints. The frontend port/adaptation
goal is delivered with the acceptance scope below; this is not certification
of every platform, hardware combination or failure branch.

## Delivery And Acceptance

### Retired Incident Branch

The 22 commits unique to `incident/vpcd-multislot-2633d7e` were checked by
changed path and behavior before retiring the branch. Its source remains in
the maintainer's verified private Git bundle; it is not a pending merge of
the retired Python/Docker transport into the Go runtime.

| Legacy changes | Current disposition |
| --- | --- |
| Python VPCD slot allocation, heartbeat, ATR caching, framing and T0/T1 negotiation (`control/app/main.py`, `sim.py`, entrypoint) | Replaced by native Agent PC/SC discovery and identity-scoped reader operations (`internal/pcscmonitor`, `agentreader`, `agentsim`, `agentlink/reader_readback`). Virtual dummy slots and port-number matching must not be reintroduced. |
| Offline reader/SIM retention and selector names (`SimConfig`, `SimSelector`, `UnifiedPages`) | Mounted `mdd` pages use durable device history and exact line/card association. `goV1Adapter`, `mddLineAdapter` and `mddHardwareAdapter` tests cover offline/stale identity and rejected ambiguous selections. |
| eSIM cached view, same-name/slot collision and fallback (`Esim`, `lpa.py`) | `mdd/esimAdapter` keys readers by Agent plus reader identity; its tests preserve cached profiles without making cached/offline cards writable or issuing an APDU refresh. No slot-index cache alias is restored. |
| Android wake lock, reconnect and multi-slot legacy client | Retained as reference only. The retired VPCD-only Android client is not compatible with the current authenticated Agent protocol; Android support remains explicitly unfinished in `postponed-tasks.md`, not silently counted as ported. |
| Old generated WebUI and Python tests | Superseded by mounted Go adapters, embedded UI generation and current contract tests. No legacy build output or Python runtime is merged. |

Linux connection switches now display fresh bearer observations, not the
existence of a retained connection owner. Disconnected and unknown states do
not change the saved user switch. Automatic recovery uses the existing
serialized policy reconciler and its backoff, preserving call/borrow ownership.

| Scope | Inspected evidence | Boundary |
| --- | --- | --- |
| Original page shell and selection | `src/main.jsx` mounts the copied App/CSS; recorded browser traversal covers ten main pages, device tabs and settings/diagnostic/notification tabs, including 390px layouts. | Navigation evidence is separate from the action receipts below. |
| Calls | The copied dialer uses the single Go coordinator. Existing Chrome evidence includes an answered 37.275-second call, ended history and terminal provider readback. | Do not repeat paid calls. Subjective audio quality is user-manual. |
| Messages | Original conversations and all-line scope are wired; the original uncertain SMS was reconciled to `cellular_sms_submitted`, HTTP 200, with zero new sends. | Do not resend diagnostic SMS or claim all carriers were exercised. |
| Devices and SIM configuration | Real Windows automatic claim/readback/provision, exact line identity, disabled data switches, masked IMEI and live rekey display were verified. | CN-SIM VoWiFi is excluded by user decision; non-CN SIMs may still use CN exits. |
| Recycle bin | Real Chrome archive/restore of the authorized stopped line returned 200 twice; the restored record matched its original, and observed history lists were unchanged. | The chosen line had no call history; populated-history boundaries retain CI evidence. |
| Network | Existing profile/subscription/explicit-apply adapters, preserved switch semantics and the recorded real direct-entry failover were verified. | Two entries of one upstream are not independent redundancy; metered-data and restoration-delay limitations remain recorded. |
| Notifications | Credential-preserving configuration/readback and existing delivery receipts were inspected; ed5f702 resolved contradictory capability metadata and disabled-event explanations. | Unsupported original number-change production is not invented; subscriptions are not silently enabled. |
| System settings | Actual audit, update-network and voice-setting HTTP 200 receipts, re-entry readback, Web original-value save and durable backup downloads exist. | No production port/certificate replacement or destructive whole-state restore was manufactured. |
| eSIM | Real nickname, download and authorized standard-deletion/retained-notification evidence exists; delete notification was acknowledged and retained. | Acknowledgement is not proof of renewed download entitlement. Do not repeat destructive profile operations. |
| Diagnostics | Actual page refresh, filtered log download and advanced read-only report export were recorded. | Synthetic and failure fixtures are not presented as production fault injection. |
| Additional Linux acceptance | Same-host discovery, relayed WSS, disconnect/Agent-stop isolation and the authorized corrected cold boot passed with zero observed WWAN bytes. Temporary samplers were removed. | Relayed WSS is not a second physical Linux host; this does not certify every native voice/data path. |

Exact private receipts and deployment hashes are referenced in the existing
`TODO_CURRENT_RECOVERY.md`; no secrets or raw evidence are copied here. The
current frontend goal is distinct from the broader Go/platform project and its
existing postponed work. Balance/allowance, CN-SIM VoWiFi and automatic audio
quality testing remain excluded as explicitly requested by the user.

The mounted UI replaces the legacy HTTP/WebSocket adapter with the existing Go
contracts and adapts the customized PCM call interface to the Go call coordinator.
Do not restore Python, Docker or a second call owner. Preserve exact line/card
identity and the user's latest aggregation and data-switch requirements. eSIM
deletion subsequently entered the user-approved interactive phase. The current
standard deletion and retained-notification behavior is documented below; old
soft-delete-only notes are not the current product contract.

## Go Adapter Work

The ed5f702 closing batch aligns notification supported/unsupported metadata and
shows reasons on unavailable event controls without changing subscriptions.
Runtime presentation uses fresh failures only, distinguishes expired evidence
from a current fault, and exposes deduplicated per-layer reasons and original
codes in the copied device views. Deliberately stopped lines remain stopped,
not failures. Full CI and production browser/API readback passed; stale-data
branches are fixture evidence, not an induced production outage. No notifications,
calls, SIM operations or data-switch changes were used for this acceptance.

The a814850 presentation batch restores the copied hardware panel's
`imei_masked` contract using `ec620942 control/app/main.py::_masked_identifier`.
It also preserves rekey facts in the final device mapping instead of overwriting
them with a second `vowifi` property. SIM, message, call, maintenance and eSIM
action translations and message-delete accessible names are completed without
changing requests, confirmations, permissions or device switches. Full CI and
production browser readback verified the masked modem identity, a live line's
zero-minute rekey value, SIM action labels and eight real message conversations.
The candidate SIM form also passed a 390px layout check. No paid, deletion, PIN
or rekey operation was repeated; prior action evidence remains separate.

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

The 2026-09-10 mounted-import review followed 43 relative modules from `mdd/App`
and found no unresolved names among 122 `api` member references. This is only a
name-level check, not behavioral parity evidence. The copied legacy
`mdd/callCoordinator.jsx` and `mdd/browserMedia.js` are not mounted; their retired
API names must not trigger restoration of a second call owner. The disabled old
eSIM Replay placeholder likewise must not be enabled: current deletion replay
uses `DeletionNotifications` and its confirmed retained-archive contract.

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
| Esim | Typed EID/profile operations and download receipts are adapted. User-approved standard deletion records card outcome separately from durable notification delivery; the old soft-delete write entry is retired. |
| Logs / VowifiHistory / AllowancePanel | Use existing Go diagnostic, availability and allowance APIs; preserve missing-data semantics. |

No copied module is counted as restored until it is wired and exercised through
the corresponding user actions against actual Go responses.

The copied-UI recycle-bin flow subsequently passed real Chrome acceptance with
the authorized stopped SIM-1111 record: archive confirmation, presence in the
recycle bin, restore, and the displayed stopped outcome. Both requests returned
200; independent readback matched the original record and other line settings,
with unchanged observed call and conversation arrays. No profile or history
deletion occurred. The selected line had no call history, so populated-history
preservation is covered by existing code/tests rather than claimed as this HIL.

Provisioning candidates refresh on exact Agent/card-session changes and provide
an explicit read retry. Unresolved modem operations retain their original request
in tab-scoped session storage before dispatch, excluding PINs and tokens. Re-entry
reads the existing Go operation receipt; only unknown receipts permit read-only
reconciliation, and only terminal receipts clear the pointer. This does not replay
provisioning or replace the durable Go ledger. It is tab continuity, not discovery
of every operation from another browser or hardware acceptance.

The original API module now delegates exclusively to the Go adapter. The history
adapter maps original thread/message/call/log fields while preserving backend
identities and typed statuses. Scoped clearing uses a Go transaction over the whole
selected line rather than only the visible page, and rejects active call records.
The working-tree and production entrypoints use this App and its stylesheet.
The acceptance boundaries above prevent interpreting the frontend delivery as
universal hardware or failure-path certification.

## User-defined eSIM soft deletion

Superseded by the user's standard-deletion decision. Historical markers and
records remain readable, but new soft-delete POSTs return 410. The physical
DeleteProfile workflow records intent before dispatch, never automatically
repeats an uncertain deletion, and tracks on-card deletion separately from
notification capture and HTTP acknowledgement. A manual recovery action reads
the original eUICC and archives its retained notifications without deleting again.
Original notification payloads and confirmed replay attempts remain durable.
No UI claims atomicity between card deletion and remote delivery, or equates
HTTP 204 with permission to download on a different device. All actual deletion
testing remains restricted to the explicitly authorized BetterRoaming profile.
The paragraphs below document the retired soft-delete behavior, not the new path.

The confirmed marker is `[MDD-DELETED]`. Soft deletion requires a disabled
profile and three explicit confirmations, persists a local event before marking,
and only renames the profile. Core and the owning Agent block enable while that
marker remains. Only an explicit user nickname edit can remove it; no automatic
enable or physical delete is introduced.

Signed deletion notifications are a separate resource. Reading existing deletion
notifications archives their original payload and SHA-256 in events.db before
any replay is offered. Replay requires two warnings plus exact ICCID entry and
the archive hash; its attempt is persisted before a single send. The original
payload survives acknowledgement, failure, unknown outcome, restart and line
cleanup. Duplicate operation IDs do not send again. Card notification removal is
not part of replay, and ordinary delivery/removal rejects deletion events.

A local marker does not generate a genuine signed deletion notification. The UI
must show when none exists, never substitute an install/disable event or claim
operator acknowledgement. The newly authorized BetterRoaming profile is the only
current hardware deletion-test target; existing business profiles remain intact.

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

The copied SMS composer's click and Enter paths now share the same nonblank,
exact-card and transport-readiness admission. IME composition Enter (including
keyCode 229 at the composition boundary) never submits. Readiness changes are
included in the page's semantic memoization without bringing heartbeat timestamps
back into rendering. Recipient whitespace normalization retains an existing retry
identity; stored-receipt reconciliation remains separate and does not resend.

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
shows the original hardware control. Subsequent production serial/auto switching
passed with the original configuration restored; evidence is recorded in the
recovery cursor as linux-mode-verification.BfFEcV. Binding and rendering alone
were not used as switching evidence.

Real Linux switching exposed an extra Go-only restriction on empty modem profile
lists. The original ec620942 USB whitelist returns no devices for an empty list;
Core and Agent now retain that behavior. No default VID/PID or broad serial probe
is substituted. Actual Linux serial/auto switching passed after that correction.
The original ModemManager startup policy was restored (active but disabled), so
the displayed services_mismatch is retained rather than silently enabling it.

The current new-device-defaults batch connects the original General controls to
CAS preferences, imports the original device defaults without overwriting Go
choices, and uses the existing Agent policy, draft, preparation, provision and
Provider apply paths. New automatic Provider apply is restricted to one added
line and cannot publish unrelated saved changes. MNC length comes from an
explicit EF_AD read; the original MCC-country JSON is copied unchanged. This
batch passed complete CI and deployed settings save/readback acceptance. On
2026-09-10, a real newly attached Windows modem passed automatic claim, EF_AD
identity preparation, readback and hardware provisioning, with one successful
provision attempt and browser verification. Automatic Provider/IMS startup did
not pass: no CN exit is configured, the candidate contains no added Provider,
and the single-added-line apply gate correctly refuses that plan. Existing
lines and data switches were preserved. Older disabled-defaults
descriptions are historical, not outstanding implementation work.
The user subsequently excluded CN-SIM VoWiFi: that conditional startup test is
no longer an acceptance blocker. This restriction is about the SIM, not the exit;
non-CN SIMs may still use a CN exit.

The following are confirmed by comparing `ec620942` with the mounted
`views/UnifiedPages.jsx` and its Go adapter. This is a correction to the existing
module matrix, not a claim that all other original actions have passed acceptance.

| Original action | Current boundary | Required closure |
| --- | --- | --- |
| New modem 4G/VoWiFi defaults | Real Windows first discovery, persisted defaults, automatic claim, identity preparation/readback and hardware provision passed in f7bc552; browser SIM/status/4G/VoWiFi views were checked. All previous line settings and disabled data switches were preserved. | CN-SIM Provider/IMS startup was subsequently excluded by the user. Preserve the original failed restricted-apply receipt; do not retry it or infer VoWiFi readiness. Non-CN SIMs remain eligible to use CN exits. |
| VoWiFi-only hardware mode | Implementation and serial/auto switching previously passed. The relocated EC20 passed real Linux discovery and original-line association; same-host/relayed WSS and disconnect/Agent-stop isolation were exercised. After fixing the udev PATH defect in 3e0f6df, a separately authorized second reboot passed automatic cold-boot grouping before Agent start, with zero WWAN bytes and preserved settings. | The relay is not a second physical Linux host; paid voice/SMS and enabled cellular-data paths were not exercised. Do not repeat the completed mode-switch or cold-boot tests, or claim Linux has no modem. |
| Web bind/port/certificate paths | Saved startup settings are editable through the existing helper; the production browser saved/read back unchanged values. | Changed-value persistence/backup has CI coverage; no production port/certificate change was performed. Domain/self-signed fields remain actual certificate information, not an automatic certificate-issuance feature. |
| Retry count and interval | Original inputs, Go persistence, per-line overrides and continuous-failure window are connected; the production page saved/read back 3/40. | Fault-window behavior is covered by CI, not a manufactured production failure; exit-recovery strike counts remain separate. |
| Rekey default | Go catalog/provider and original form are already connected. | Retain existing evidence; do not treat this as another missing implementation merely because it appears beside disabled retry fields. |
| eSIM deletion and notification recovery | The user-selected standard deletion path has passed real browser deletion, original-card notification recovery and a single receiver HTTP 204 acknowledgement on the authorized test profile. Original payload/hash and the attempt remain stored. | Network-loss/restart cases have isolated CI evidence, not production fault injection. HTTP acknowledgement is not proof of renewed download entitlement. Do not repeat deletion or notification sending without a new purpose and applicable confirmation. |

Opening all main routes and tabs verifies navigation and rendering only. It does
not prove saving, actions, persistence, notification delivery, calls, SMS or
hardware behavior. Keep missing implementation separate from missing acceptance.
Agent platform delivery (including Android readers and persistent modem capture)
also cannot be certified by this frontend matrix.

Linux boot-guard execution was subsequently exercised with the real modem.
The test exposed a systemd ordering wait: the guard runs Before=NetworkManager
but synchronously queued that daemon's reload. In 6a5c7a4 it calls the existing
NetworkManager D-Bus configuration reload directly. Full CI and the real oneshot
service passed, with zero WWAN bytes, no cellular addresses/routes, retained
drop rules, unchanged catalog and unchanged business-service PIDs. This proves
the boot service execution path, not a full host reboot.

The subsequent authorized reboot confirmed that the guard finishes before
NetworkManager, but exposed a separate hotplug-helper failure: udev supplies
device properties without PATH, so the helper could not locate ip. No WWAN
bytes, addresses or routes were observed; its missing group was repaired before
restoring the Linux Agent. Commit 3e0f6df resolves tools from standard directories
only when PATH is missing and leaves the process environment unchanged. CI,
the real minimal-environment helper and an exact udev event passed. A subsequent,
separately authorized second reboot confirmed the automatic group before Agent
startup, guard completion before NetworkManager, zero WWAN bytes and unchanged
catalog, notifications and disabled data switches. Both temporary boot observers,
links and scripts were removed; only private evidence and backups remain. This
does not certify paid calls or a second physical Linux Agent host.

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
by this original Linux-host control. These were the source requirements for the
now-delivered serial discovery/owner and mode-switch batch, not outstanding
implementation work. Physical modem acceptance remains separate.

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

Deletion records now appear as compact rows inside Notifications. The explicit
details dialog contains the original profile/provider names, pre-delete nickname,
identity, notification hashes and delivery attempts. Names are captured before
deletion; legacy missing names may be derived from the matching durable download
receipt with provenance shown, never guessed from an ICCID. Names are display
metadata only: commands remain bound to EID, ICCID, operation and archive hash.

New downloads can explicitly opt in to retaining activation/confirmation codes
in the existing mode-0600 server database. These values are not separately
encrypted and are sensitive backup contents. Lists return only presence flags;
an authenticated, CSRF-protected, exact-identity confirmation is required to
reveal them. They are not added to localStorage, diagnostics or automatic retries.
Explicit recovery from pre-existing operator records is labelled as such and
cannot overwrite conflicting saved codes. Possession of saved codes does not
prove that the operator permits another installation.

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

These source contracts passed batch CI and actual non-destructive field readback
as recorded in the recovery cursor. Download installation acceptance is explicitly
postponed by the user because no new activation code is available. No profile
installation or deletion is implied by the implementation.

Explicit chip reading now uses a read-only EID inventory refresh, including an
empty eUICC, through the existing Agent session and inspection functions. Older
Agents with a current ICCID use the existing reader-readback operation instead;
cache loading never triggers this refresh. Notification read failure remains
visible without discarding successful profile readback. Core must precede new
Agent deployment. CI and one actual empty-chip browser refresh are recorded in
the recovery cursor; this does not certify profile writes or every reader host.

Non-deletion controls consistently use the Agent's profile-management capability;
download admission uses the selected secure element's download capability. Cache
failures remain visible rather than appearing as empty chip information. Read
generations fence cache and post-operation refreshes when the reader/card session
changes. These changes do not enable deletion or certify production profile writes.

One authorized nickname change and restoration was exercised through the original
page and confirmed by Agent inventory. It exposed a post-write refresh window:
successful mutations already ask the Agent to refresh its card session. The page
now reads reported inventory after profile mutations rather than immediately
issuing another hardware refresh. Existing card snapshots update matching EID
profiles without added polling; stale reports are ignored. Notification inventory
has a separate read flag, so an unqueried list is never presented as confirmed empty.

Profile disable/enable acceptance exposed a card-session transition that briefly
reported a default SIM identity without an EID. For a known eUICC's requested
post-write refresh, the Agent now retains a same-attachment refresh requirement
and lets the existing reader-worker backoff reopen after failed inspection.
A new physical session clears that requirement. This does not reset the card,
replay a profile mutation, or apply the eUICC requirement to ordinary SIMs.

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
facts. The download form and Core/Agent contracts now restore ec620942's optional
IMEI behavior: omitted IMEI uses the existing lpac-compatible TAC-only device
information, without generating an IMEI. Explicit IMEIs retain validation. The
minimal euicc-go patch, source version and license are recorded in
`go-runtime/third_party/euicc-go/MDD-PATCH.md`.

The authorized Google test-profile attempt reached real authentication without
an IMEI. The selected empty eUICC received RSP subject/reason 8.8.2/3.1: the SM-DP+
does not support its proposed CI public keys. No profile was installed. This is
not proof that downloads from compatible issuers fail, nor installation or
deletion acceptance. The original generic failure receipts remain unchanged;
new failures preserve structured codes through HTTP decoding and the durable
job, with the production page displaying the specific reason. Existing business
profiles and pre-existing profiles named TEST remain outside deletion authority.

Settings read independent Go domains separately. A notification or catalog read
failure does not blank unrelated settings; the affected save remains unavailable,
and authentication failures are not converted into partial success. Missing audio
values remain absent rather than becoming a saved default. Runtime listener facts
populate the original separate address and port fields without exposing key paths.
Validation and deployment evidence for this batch is tracked in the recovery cursor;
source adaptation alone does not establish carrier-call or profile-write acceptance.

## Recovery workflow adaptation

The upstream typed IKE authentication rejection is retained as
`swu_authentication_failed` instead of collapsing into a generic SWu open error.
The existing recovery ledger counts that failure once as a non-exit cause and
does not select another proxy. This improves failure attribution; it does not
certify automatic failover in the production fault-injection scenario.

The failed production injection also exposed failures before IKE: all proxy
connections to the configured DNS resolvers were unreachable. The existing
resolver now retains typed evidence only when every resolver connection fails;
successful connections, DNS answers and cancellation are not classified that way.
Provider and Core carry this pre-IKE exit failure into the existing three-strike
policy. Integration coverage does not replace a successful hardware recovery run.

Direct-entry DROP testing exposed the resolver's internal timeout path bypassing
that typed evidence. Per-resolver connection progress now survives until the
deadline: only attempts with no established connection can blame the proxy.
Caller cancellation/deadlines and established DNS connections remain excluded.

Recovery preflight and lease resumption use the full authenticated Core preflight
path. A pending selection can be retired, without deleting its history, after a
newer user configuration is both published and runtime-confirmed and fresh,
identity-matched Provider observations show no remaining maintenance hold.
Saving a configuration alone never clears an uncertain selection.

Production direct-entry fault acceptance subsequently confirmed one automatic
IP-to-domain selection and IMS readiness on the alternate entry (same upstream,
not independent redundancy). Restoring the original configuration succeeded,
but one line needed a single explicit runtime start after the recovery delay.
The recovery cursor retains both outcomes; successful switching must not be
presented as proof of fully automatic recovery after configuration restoration.

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
