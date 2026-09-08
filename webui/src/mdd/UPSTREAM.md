# Customized MDD User Interface

Source: this repository, immediately before the first Go runtime rewrite.
Revision: ec620942e93edbbb567398acda4c0dffe1d8f375
First Go rewrite: 95c38bbca2ef87559d988e57a38ecc50b02ba685

The customized frontend source files were copied together, retaining the original pages,
selectors, interactions, styles and translations. This directory is now mounted
by the working-tree and production entrypoints. Feature parity and acceptance
remain incomplete; mounted pages alone are not proof of restored behavior.

Before mounting, replace the legacy HTTP/WebSocket adapter with the existing Go
contracts and adapt the customized PCM call interface to the Go call coordinator.
Do not restore Python, Docker or a second call owner. Preserve exact line/card
identity and the user's latest aggregation and data-switch requirements. eSIM
deletion remains excluded pending the final interactive phase.

## Go Adapter Work

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

XHTTP still requires the deferred Xray capability-boundary decision. Automatic
failure attribution/reselection remains unfinished; it is not supplied by ordinary
subscription parsing or explicit application. Do not represent either as working
or generate paid hardware traffic to manufacture acceptance evidence.

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

## Recovery diagnostics and persistence boundary

Known IMS start failures retain the IMS layer; an unconfirmed tunnel state is
not relabeled as a confirmed tunnel failure. A failed start exposes an opaque,
stable failure identity independent of snapshot-read sequence numbers.

The ported exit policy has a once-per-failure entry point. Its ledger is stored
with optimistic revision checks in the existing events database metadata, not
the desired-state catalog or another database. Duplicate writes do not advance
the revision, and permanent event-line purge removes and fences this state.
This is the persistence/diagnostic foundation, not completed automatic failover:
the live recovery controller and safe node-selection integration remain open.

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
