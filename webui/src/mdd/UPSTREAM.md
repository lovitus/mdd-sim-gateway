# Customized MDD User Interface

Source: this repository, immediately before the first Go runtime rewrite.
Revision: ec620942e93edbbb567398acda4c0dffe1d8f375
First Go rewrite: 95c38bbca2ef87559d988e57a38ecc50b02ba685

The customized frontend source files were copied together, retaining the original pages,
selectors, interactions, styles and translations. This directory is now mounted
by the working-tree entrypoint. It is unfinished implementation, not a delivered
UI or proof of feature parity.

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
The working-tree entrypoint now uses this App and its stylesheet. Production has
not been changed; remaining contracts and acceptance still block release.

The copied App now instantiates the existing Go coordinator once for both transports
and its global incoming-call overlay. Softphone retains the customized page structure,
but delegates signalling/media/mute/DTMF/hangup to that owner. Unknown start and
unconfirmed teardown remain visible; leaving the page does not close a second local
phone or pretend the hardware is idle. The old recording buttons were backed by
unimplemented stubs and are not presented as a working recording feature.
Call history defaults to all lines with line-scoped deletion; the independent plus
key is dial-only, never a DTMF tone. WebSocket snapshots supersede older REST reads.
These changes remain unmounted and unverified against hardware until the full batch
is ready. Source-shape checks are not browser or call acceptance evidence.

Messages now retains the copied conversation/bubble layout with all-line history.
Conversation identities include line, transport and peer; replies and deletion use
that exact scope. Expanded history keeps its server cursors and refreshes loaded
pages rather than replacing them with the newest page. Delivery reports correlate
per part and retain their real error fields. Cellular submission history now keeps
the request body after the existing operation-identity validation; old missing
bodies are not fabricated and retries do not submit again to the Agent.
