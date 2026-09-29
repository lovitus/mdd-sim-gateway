# MDD Android Agent — native preview

This is a fresh native Android app, not a WebView or an exposed VPCD service. Android 9/API 28 or newer. The application ID is `com.lovitus.mddagent.preview`; it does not replace an unknown historical Android package.

## Current preview milestone

The subsequent dial-prefix correction is qualified at `609c588` in hosted
`36544499252`; signed v97 is installed and actual zero-key long/short touches
were checked without calling. The number row no longer has an apparent fixed
`+`: users explicitly enter the prefix, including by long-pressing zero.
Existing international, explicit `00` and service-code parsing is unchanged.

The communication-UX batch is qualified: Home and separate call/SMS route status,
complete retained conversations, provable original-card reply selection, and
multiple Android communication clients without local readers. Sharing is optional.
Hosted run `36536473242` qualified source `f8a6a8c`; the original reviewer closed
UX-R1/UX-R2. Signed v95 is installed with data retained and five-page, older-history
pagination and reply-preparation readback. No paid operation was performed.
PR #15 integrated Android and Go gates also passed at `d00575e`; later record-only
edits do not relabel the APK or test source. The v91 milestone below remains closed.
See the [current cursor](../docs/decisions/2026-09-24-android-client-scope.md#current-cursor).

September 29 review correction: all three client defects are fixed and the original
reviewer has closed N1/N2/N3. Whole-confirmed SMS submissions retain their confirmation despite
delivery failure; legacy unknown receipts remain protected. Historical messages
without a card identity require explicit current-SIM selection for Reply. An
unreadable-storage page now offers twice-confirmed, privately archived local reset,
refusing known pending operations and leaving availability/sharing off. Reset also
invalidates retained login drafts without blocking UI admission on storage I/O.
At reviewed source `33f8440`, Android workflow `36517184960` passed all 18 methods
on both API 28 and 35, including all five behavioral regressions; zero failed,
errored or skipped. Pre-fix runs `36513116448` and `36516219462` establish their
counterexamples. Full Go workflow `36517185239` also passed. Subsequent record-only
closure preserves that runtime/test/build source; it is not a fresh test result.
The owner subsequently authorized merge/deployment after review. PR #12 is merged
at `0b663b9` and signed v91 (`7df49d4`, workflow `36520354543`) is installed on the
newly authorized phone with data/UID retained. All five native pages, remembered
connection settings, USB permission/card identity and Core card routing were read
back. No additional paid call/SMS was made. This changes no server API or paid retry.
See the [current rollout cursor](../docs/decisions/2026-09-24-android-client-scope.md#september-29-reviewed-merge-and-rollout).

The September 27 Android preview milestone is complete within its recorded scope
and its PR #12 review/merge is complete. Signed v91 supersedes the earlier v85 /
`a1bf28b` phone evidence; minimal Core `b6f3a4c` is unchanged. Reader sharing, scoped
call/SMS paths, five native pages, ordinary reconnect and bounded USB recovery
have the evidence described below. The separately reviewed Provider fix in
PR #13 passed full workflow `36522731598` and is deployed from artifact source
`2fcfda83bd51` to the existing Provider processes/shared entry point. Core and
desktop Agents were not redeployed; no new paid test or endurance claim is added.

The owner accepts missing long-duration USB/OEM/battery validation for this stage;
extreme network-switching validation is also non-blocking. Failures remain visible,
automatic recovery is bounded, and Home/Readers retain manual recovery advice.
Unproven startup-timeout causes and deferred tests are not claims of success.
See [the existing postponed work](../postponed-tasks.md) and
[ANDROID_PREVIEW_CLOSE](../docs/decisions/2026-09-21-android-agent.md#android_preview_close---september-27-owner-delivery-decision).
Desktop isolation/installer work is outside this Android delivery. Older candidate
notes below preserve evidence history, not instructions to repeat finished work.

## Setup

1. Install the preview APK, open it, and enter the gateway **HTTPS origin**, account name and password. Remembered login is encrypted using Android Keystore; disable Remember or use Forget login to remove the saved password. For self-signed servers, independently verify the displayed **leaf certificate SHA-256** before accepting it. Certificate changes require explicit confirmation; there is no trust-all toggle or credential-bearing redirect.
2. Alternatively scan/paste the setup JSON below; inspect and explicitly confirm the origin/pin. The account still requires sign-in. Reader enrollment may be included only in a QR kept private.
3. Choose **Stay available** and grant notifications. Calls and Messages can use server lines without a local reader. Optionally choose **Readers → Share attached readers**, then grant access to the particular OTG USB device; the gateway account must permit issuing the scoped Agent credential. Existing lines/SIM routing remain configured in gateway management.
4. Choose the exact line and **VoWiFi** or **Cellular modem** in Calls/Messages. A paid mutation requires a confirmation. Uncertain submissions are not retried. Resolve the existing call with Hang up; check SMS history before any deliberate new send.

```json
{"type":"mdd-agent-setup","version":1,"server":"https://gateway.example","certificate_sha256":"<64 hex digits>"}
```

Optional `agent_id` and `agent_token` must both be present. Do not put account passwords in QR codes, logs, screenshots or support exports. The app stores opaque sessions/CSRF/enrollment encrypted with Android Keystore AES-GCM, disables backup and exports no service. Sign out stops sharing locally; revoke the issued reader credential in gateway settings when retiring a device.

**Core compatibility:** this PR adds authenticated `/v1/mobile/ws` to Core. Build/run the matching Core preview before expecting native background events. Older Core returns a visible update-required state, not a fabricated empty/healthy screen. This is not a production deployment action.

### Production adaptation follow-up

The minimal Core support was merged in `8d0c4c6` and deployed from successful
workflow `36010202284`. The physical handset now receives real catalog lines and
message history. This does not establish call, reader or carrier acceptance.

The client fetches display numbers from the existing catalog on reconnect and
opening Calls/Messages. Numbers are joined by both line and card identity; live
readiness still comes from the snapshot, and mutations revalidate the exact line.
Unavailable routes display the existing `blocked` layers and corresponding `facts`.
USB scan failures stay visible independently of OMAPI failures, including on the
affected reader row. No new Core routes, paid retries or user-intent changes are
introduced.

Physical pre-fix evidence reproduced missing numbers, omitted readiness reasons
and a USB failure masked by an OMAPI error. Reconnecting the reader did not resolve
its first power-on write failure. The presentation fixes must still pass GitHub
and physical retesting; they do not claim to repair that USB transport failure.

## Implemented paths

### Native call and message feedback

Preparation failures retain their stage and actual API/audio error after cleanup,
rather than replacing every failure with "No call was started". The existing
audio canary still requires microphone signal; its in-call preparation view now
explicitly asks for speech. Carrier dispatch, unknown outcomes and no-redial guards
are unchanged. The in-call keypad opens without sending anything; only an explicit
digit press sends DTMF for the still-active original call.

Call controls explicitly color their icons in enabled, checked and disabled states,
independently of the Material theme's filled-button defaults. The in-call keypad
shows the pending digit and the gateway acknowledgement or original failure in
the dialog. Only one tone request is in flight; no tone is queued or retried.
Acknowledgement is checked against the existing call/session response, not treated
as proof that the carrier played a tone. DTMF feedback does not replace call state.

Audio timeout details preserve capture callbacks, locally queued frames, returned
frames, playback counts and PCM signal/peak measurements without retaining audio.
Queue acceptance is not server receipt, and these counters do not establish voice
quality. The existing Core signal threshold and carrier-dispatch gate are unchanged.
After an already-submitted call loses audio, a matching terminal history record
must not erase the observed media-close reason. Known media failures remain red;
a normal remote WebSocket close retains neutral audio detail without pretending
that it identifies the carrier or hardware cause. Once the call end is confirmed,
the detail no longer incorrectly says remote termination still needs confirmation.
An explicit user hangup does not acquire a failure from a late audio callback.
The pre-fix field UI reduced an unexpectedly ended call to only `Call ended`.
This presentation correction changes no call lifetime, API, reader or recovery
policy. It does not repair the separately observed USB write failure. Existing CI
must qualify the candidate; no new automated red/green coverage is claimed.
Reader transport state is retained separately from local card-scan state so a scan
cannot hide a failed connection. Home/Readers show that state; diagnostic sharing
contains only bounded failure codes, never raw response bodies, URLs or credentials.

Home and Readers also share a USB-specific status renderer: current attachment,
permission, saved availability/sharing, local card identity and Agent link are
distinct. USB transfer failures stay visible alongside a link failure and suggest
reconnecting the reader/OTG adapter only when no call or SIM operation is active.
The advice does not diagnose a power, OS-sleep or software cause. An unrelated
OMAPI denial does not color this USB-specific status as a failed USB reader.
USB permissions are requested only when missing, never treated as proof that the
transport works. The sleep-related failure recurred with permission still granted:
reopening, selecting the interface and resetting its configuration did not repair
bulk writes. A single owned-device port reset restored a valid CCID slot-status
response; the original signed client then reidentified the card and Core became
ready. This proves a software recovery path, not the origin of the sleep fault.

The client now cancels the old interrupt request before releasing its connection.
Two consecutive failed USB writes permit a port reset for that failure episode,
only on a single-configuration, single-interface APDU-level CCID reader with
existing USB permission. Composite devices and interfaces owned elsewhere are
not reset. Reset, reclaim, CCID slot-status handshake and power-on retain the same
descriptor; fresh card identity then creates the new attachment generation.
The first integrated candidate closed the reset handle and waited for the next
scan: it reproduced a successful reset return followed by failed card reads.
That native return alone is not recovery. The corrected path follows the working
field comparison without the intervening close/idle gap;
PIN/AKA, SMS and call requests are never replayed. Sixty seconds of successful
scans rearm recovery for a later fault. One delayed followup is available if the
first reset fails during the read-only slot-status handshake, or succeeds but
fresh command writes fail again before sustained health rearms the budget.
Both cases require two consecutive write failures and at least 30 seconds since
the first attempt.
There is no third reset, and permission, shape, claim, native reset, power-on and
identity failures do not gain a retry. Continuing failure does not cause a reset
loop. Home/Readers distinguish the pending followup from exhausted recovery and
retain the unplug/OTG advice. No wake lock, root, hidden Java API, server change, permission
grant or availability/share intent change is added.

The small JNI boundary uses the Linux `USBDEVFS_RESET` operation on the descriptor
returned by Android's public `UsbDeviceConnection.getFileDescriptor()`, following
[AOSP USB host](https://android.googlesource.com/platform/system/core/+/refs/heads/android13-release/libusbhost/usbhost.c)
and the [libusb unrooted Android model](https://github.com/libusb/libusb/blob/master/android/examples/unrooted_android.c).
No USB discovery or device-path opening occurs in native code. Signed v75 at
`02eeb2d`, qualified by workflow `36234941184`, recovered a real recurrent write
failure without a replug or an additional App restart during recovery. Fresh card
identity and Core readiness were checked, not just the reset return code. A
powered-handset 180-second screen-off/wake check retained the same process and
card; Android device-idle was not entered. Long deep-idle/battery acceptance is
still open. No automated red/green test is claimed for this hardware-specific path.

A later real failure on v75/v76 supersedes any durability inference from that
short sample. Permission and attachment survived, but bulk writes failed again.
A bounded, exclusive diagnostic reset again recovered slot status, power-on and
the original card without replugging; the production client subsequently became
ready. That controlled diagnostic is not automatic-recovery acceptance.
The first correction retained the same reset budget and descriptor, but permitted
at most three GetSlotStatus write attempts after reset, with 250/500 ms backoff.
Only zero/negative writes of this read-only command are eligible. Partial writes,
response/framing failures, power-on, identity, PIN and AKA are never retried there.
This addresses a possible firmware-resume gap, not an established cause of sleep
failure. Recovery now reports its exact stage rather than attributing every -5
to the reset ioctl. Claimed-interface ownership is retained through cleanup.
Signed v77 passed exact-head workflow `36238994751`, but a later real screen-off
failure reached `slot_status` and remained unavailable after waking. The one-reset
budget stayed exhausted. A diagnostic driver failed before issuing its reset;
after cleanup/reopening, the same v77 software recovered the exact card with a
new reset budget. That is not proof that an App restart is a product solution.
The bounded followup above addresses this observed recovery dead end without an
App restart, permanent wake lock or paid-operation retry. A focused policy test
compiled and failed with the old one-reset behavior (only its timestamp parameter
was added), then all three policy tests passed after the change. This counterexample
uses real UsbRecovery policy, not USB hardware. Signed v80 / `aeb0004` passed
workflow `36249215118`; a short field sample did not establish durable recovery.
Later, a successful reset was followed by renewed write failures before the
60-second healthy rearm. Refresh did not recover the reader; a manual App restart
did. The v80 policy incorrectly excluded this successful-but-unstable case from
its second recovery attempt. The new regression compiled and failed on v80,
then all four focused policy tests passed after the correction. The old test's
expectation that a completed reset could never get a followup was replaced by
this observed sequence; unsafe failure-stage exclusions remain unchanged.
Signed v82 / `74e97c5` passed full workflow `36263260716`. The real delayed second
reset identified the card, then writes failed again and the native UI correctly
reported exhaustion. A subsequent owner replug and USB permission grant restored
the exact card without restarting the App, but it failed again after about eight
minutes while awake in the same process. Durable USB recovery is still failed;
the cause is not established and is not attributable solely to phone sleep.

The current client-only diagnostic batch retains the original negative errno
from one `USBDEVFS_BULK` ioctl on the already-owned descriptor. It follows
[AOSP libusbhost's bulk request](https://android.googlesource.com/platform/system/core/+/refs/heads/main/libusbhost/usbhost.c);
the [framework JNI wrapper](https://android.googlesource.com/platform/frameworks/base/+/refs/heads/main/core/jni/android_hardware_UsbDeviceConnection.cpp)
otherwise exposes just `-1`. Endpoint, payload, timeout, serialized ownership and
the number of transfers are unchanged; there is no fallback or replay. Partial
writes remain unknown outcomes. Home/Readers distinguish write, response-read
and missing-native-transport failures. The first scan failure in each unhealthy
episode records only its class, phase, CCID command number, result and duration,
not card identifiers, APDU data or credentials. This is a diagnostic change, not
a USB root-cause repair. Signed v83 / `4e2f4d8` passed workflow `36281446154`, with
all four native ABIs packaged and the stable signer verified. The retained-data
update captured a real CCID power-on write timeout (`-110`, about 2016 ms), then
the bounded reset reidentified the original card. One 572-second read-only sample
retained the same process and card without a new failure. Native Home/Readers
and final Core routing/IMS/message readiness were checked; no new paid action or
extra reset/replug was used. This is not long-idle/battery or root-cause acceptance.
No new automated red/green claim is made for this native diagnostic boundary.

### Event-driven USB recovery

The September 27 owner requirement is to maintain the attached reader while the
app is foreground or its availability notification/service is active, checking
and repairing it at meaningful lifecycle events until the user stops it. Existing
Pause, sign-out and sharing-off choices remain authoritative; this does not turn
sharing back on or silently grant Android USB permission.

Activity resume, screen-on/unlock, leaving device idle, USB attach/detach/permission,
reader-link reconnection and local call completion request a serialized fresh
scan. Events coalesce into the existing reader-I/O owner and health schedule;
there is no additional polling loop or permanent wake lock. An event reopens an
exhausted recovery episode only after the existing 30-second cooldown. Each
episode still allows at most two resets; events during a pending retry are
consumed by that retry. An unsupported device shape or missing native reset
implementation remains a visible manual-recovery case.

Both read and write transport failures during a read-only scan may qualify for
recovery. Opening/resetting is deferred during the client's call or SMS operation;
queued scans check the original owner before touching hardware. Reader I/O and
AKA remain serialized. No PIN/AKA command, SMS or dial is automatically replayed.
Healthy readers are checked, not reset. Unrecoverable physical/firmware faults
still need the displayed reconnect guidance, not a fabricated healthy state.

Three new `UsbRecoveryTest` regressions failed with the old ignored-event
behavior (same compiled API), then all seven policy cases passed after enabling
event recovery. They cover exhausted budgets, event coalescing/cooldown and
transient versus unsupported reset stages. Signed v84 / `fd8fc0b` passed the full
[Android workflow](https://github.com/lovitus/mdd-sim-gateway/actions/runs/36289241552)
and [Go workflow](https://github.com/lovitus/mdd-sim-gateway/actions/runs/36289239524).
Source/tree, archive digests, APK hash, stable signer and four native ABIs were
independently checked before one retained-data installation. Reports contain 19
JVM tests, 13 native fixtures on each API 28/35, and 219 scoped Core/agentlink race
cases without failures/skips; those counts do not describe the whole Go matrix.

Actual handset evidence: the exhausted reader received two new bounded attempts
after an ordinary background screen-off/wake event with the notification active.
Both attempts failed; unlock/foreground return then reidentified the original
card in the same App process, without replug or restart. Native Home/Readers and
Core independently showed the recovered card route and IMS/message readiness.
Pause stopped the service/notification; another sleep/wake added no USB activity,
and reopening the UI remained paused. Explicit resume restored the original
sharing/availability and again recovered the reader automatically. Final native
and Core checks agreed, with no active calls and no new paid call/SMS or PIN action.

This qualifies the event-rearm and stop-intent paths, not elimination of the
underlying intermittent transport fault: write timeout `-110` and one read `-12`
were retained. Kernel diagnostics were inaccessible; neither a hardware cause
nor kernel allocation pressure is proven. Deep-idle/battery durability remains open.

The implementation follows Android's existing USB permission/owned-connection
lifecycle, not a new USB stack: [USB host](https://developer.android.com/develop/connectivity/usb/host)
and [device-idle transitions](https://developer.android.com/reference/android/os/PowerManager#ACTION_DEVICE_IDLE_MODE_CHANGED).

### Bounded CCID packet reads

The CCID receiver now follows the endpoint-sized bulk-read and declared-length
assembly approach used by
[OpenEUICC's UsbCcidTransceiver](https://github.com/estkme-group/openeuicc/blob/1c70ca7a701adc0047c4d6c12579a9b072a3a244/app-common/src/main/java/im/angry/openeuicc/core/usb/UsbCcidTransceiver.kt).
The upstream is GPL-3.0. This is a small Java adaptation of its transport approach, not an import of its
TPDU, retry, voltage-selection or payload logging behavior. MDD retains its
existing five-second whole-response deadline, eight-extension limit, 65,536-byte
body bound and exact slot/sequence/type validation. Up to three leading zero
packets are consumed without resending any command. Negative native results
still fail immediately with their original errno; writes and reset policy do
not change.

The actual reader's bulk endpoint is 16 bytes. Previously each read requested up
to 65,546 bytes, including tiny slot-status responses. Endpoint-sized reads avoid
that large native allocation and do not require another short packet after a
complete full-packet CCID response. Retained -12/read and -110/write observations
justify this correction, but do not prove it fixes the underlying sleep/USB
fault. Two deterministic behavioral tests were red with the old oversized native
request restored (the API still compiled), then green with packet-sized reads.
They cover full/partial packets, leading zero packets, native allocation failure,
header/body bounds and original read failures. These are transport-model tests,
not physical USB evidence.

Signed v85 / `a1bf28b` passed
[workflow 36292625372](https://github.com/lovitus/mdd-sim-gateway/actions/runs/36292625372):
21 JVM tests, 13 native fixtures on each API 28/35 and 219 scoped Core/agentlink
race cases, without failures or skips. Source/tree, archive hashes, the unchanged
stable signer, non-debuggable manifest and four native ABIs were checked before
one retained-data install. Core and Provider were not redeployed.

Initial USB power-on still had one write `-110` timeout; the existing bounded reset
then reidentified the original card. Native Home/Readers agreed. The following
eight-minute background observation retained the same App process and foreground
service with no additional first-scan failure/reset. Final Core showed the same
card, current reader route and IMS/messaging readiness, unchanged desired lines
and no active calls/media. No replug, manual reset, paid call/SMS or PIN action was
performed. This is scoped recovery evidence, not proof the write fault is fixed.

The attempted seven-minute screen-off interval was interrupted by the phone waking
about 15.9 seconds after sleep; its final state was Awake/ACTIVE. It is not a
continuous screen-off, deep-idle or battery test. No screen-keeping app, network,
device-idle exemption or power setting was changed to force a pass. The App was
returned to Readers with saved availability/sharing intact; the observer exited.

A subsequent official Android device-idle check held deep `IDLE` with the screen
off for four minutes. App process, Agent generation, reader session and socket
connection were unchanged; the next Core heartbeat advanced and was fresh.
Cleanup first cleared the forced flag, then a motion event and authorized unlock
restored `ACTIVE` with the screen on. Actual native Readers and final Core agreed
on the same card and live route, with idle call/media owners and unchanged desired
state. No App restart, reader replug, battery exemption, persistent power/network
change, user-switch change or paid operation was used. This qualifies that bounded
deep-idle entry/exit, not long-duration availability, battery life or the original
write-timeout cause. The procedure follows
[Android's Doze testing guidance](https://developer.android.com/training/monitoring-device-state/doze-standby).

### Cellular incoming event dependency

The September 27 owner-assisted attempt reached the cellular modem and produced
missed-call history, but neither the native client nor the independent mobile
stream received an actionable incoming event. Core bound its existing cellular
event source inside `WithWebUI`, where the static page handler does not implement
it, instead of inside `WithCellularMedia`. The minimal correction moves that
binding; it changes no authentication, call lifetime, SIM fences or user intent.
There is no safe client-only substitute for the missing exact incoming event.

`TestCellularMediaIncomingReachesBrowserAndMobileStreams` exercises both real
WebSocket endpoints, with and without WebUI. All four cases failed with missing
incoming data before the correction and passed after it, including removal of
the ended event. Full GitHub workflows for `fd8fc0b` passed and the owner authorized
its Core-only production rollout. The running binary and offline backup hashes
were verified; unrelated processes, saved lines, notification settings and
configuration stayed unchanged. All connected Agent generations reconnected and
the maintenance lease was resumed. Source comparison against the old `8d0c4c6`
confirms only the three-line binding move
and its regression test changed in Go/Provider/WebUI. The Android preview is v84;
its event-recovery result above is separate from incoming-call acceptance. No
additional outgoing call or SMS was sent to investigate the incoming defect.

One actual owner-assisted incoming call reached both mobile/browser event streams
with matching event/card identity. Native Answer/Decline were visible, Answer was
clicked, and the owner confirmed two-way speech. Durable history records 45.944
answered seconds and a terminal end; subsequent native/Core checks were idle.
This qualifies incoming answer and speech connectivity, not audio quality.

The planned native Hang up was not exercised: an in-call screenshot timed out,
and the independent device fallback stopped the same App process. The first
readback was still active; later history/session readback confirmed the end.
In-call XML showed audio reconnecting; its cause is not established by the
screenshot timeout. Reopening the App restored saved availability/sharing and
the identified card. No automatic redial or paid SMS followed. Earlier outgoing
native-hangup evidence remains separate from this fallback-terminated attempt.

Call history now displays direction, peer, line name/number/card when present,
localized colored status and local time. Missing catalog details remain unknown;
the UI does not invent an immutable historical SIM snapshot.

Recent/history messages show peer, transport, time and body. Catalog names/numbers
are explicitly current configuration, not an immutable historical SIM snapshot.
Without a historical card ID, Reply requires choosing a current SIM and confirming
that new intent; with a trusted ID, the original card must still match. After any
draft replacement confirmation the exact selected identity is rechecked. Reply
fills the existing composer and never sends. Missing or changed identities are
not silently substituted; normal send confirmation and expected-card checks remain.
Submission receipts retain the original content and own-number snapshot in the
existing encrypted, bounded store. Under its existing byte budget only resolved
local previews are evicted; unresolved payloads remain intact. Older already-purged
bodies are displayed only when an exact matching retained event supplies them.
Numbered submitted parts retain their part labels when displayed in old receipts;
missing parts are not silently reconstructed into an apparently complete message.
Submitted is not displayed as delivered. State text keeps its meaning and adds
green/amber/red distinctions across the existing native pages.

Failed SMS events retain their machine `state`, even when their event `kind` is
`submitted`. They display as red failures rather than successful submissions.
Checking the original local receipt records an observed failure without inferring
that no multipart segment was delivered; the payload remains unresolved and no
automatic resend is introduced. Dispatch errors retain bounded gateway code,
layer and detail in the existing encrypted receipt and native notice. Previously
discarded error details cannot be reconstructed from history that lacks them.
Field evidence reproduced `kind=submitted, state=failed` and an omitted gateway
reason. The existing CI suite and read-only physical history inspection qualify
this presentation fix; no new automated red/green test is claimed. This change
does not repair carrier registration, location rejection or incoming-call routing.

A later real send received SIP acceptance followed by `delivery/failed`, RP cause
38 (`network out of order`). The native local receipt incorrectly remained
submitted, while history rendered the delivery event as a separate row without
the original recipient/body. The client now adapts the existing WebUI
`historyAdapter.js` correlation: exact line, transport, message ID and part attach
the latest delivery report to its submission. Orphan reports remain visible when
an older submission page has not been loaded; raw events are not changed.
The original account-scoped encrypted receipt also observes failed delivery
events after submission. A late submission response cannot erase that failure.
Existing snapshots/history provide the observations, without another polling loop
or automatic send. Repeated unchanged events do not cause storage writes.

The two focused journal regressions both failed on `02eeb2d` with incorrect
`submitted`/`unknown` states and passed after the fix. That one-time Java check
used the real journal/JSON code and unused dependency traps, not Android, TLS or
carrier mocks presented as physical acceptance. Normal GitHub tests still qualify
the full App. The physical pre-fix event remains available for read-only UI
qualification of the new signed APK; no additional paid SMS is needed. RP cause
38 is an observed network receipt, not proof of its underlying cause or successful
delivery, and the authorized one-shot attempt has been consumed.

Pre-fix physical evidence: a subsequent call cleared to a generic no-call notice
without a new carrier history entry; message records omitted own-SIM information
and reply actions, and successful local receipts discarded their body. These are
observed UI/receipt defects, not proof of the unavailable original audio failure
cause. This follow-up changes no Core or Provider code. Existing CI and physical
UI checks must qualify the candidate; no extra paid call/SMS is implied by them.

- Native Home/Calls/Messages/Readers/Settings screens, QR setup, exact line/route selection, generic private notifications, pause, safe diagnostic share and battery-settings guidance.
- USB host CCID **APDU-level, slot 0** readers (up to eight). Active-profile ICCID/IMSI/EF-AD reading and fixed USIM/ISIM AKA; no general APDU tunnel, PIN guessing, profile download, arbitrary card mutation or eSIM deletion. Interrupt changes invalidate the session generation; identity is re-read before AKA. TPDU-only readers and inaccessible/locked cards are not advertised as ready.
- OMAPI uses Android's standard access-controlled logical channel. Many devices/cards do not authorize ordinary applications; these are clearly unavailable. No root, hidden APIs, carrier-privilege bypass or misleading embedded-eSIM promise.
- Server VoWiFi and cellular-modem call paths use the existing exact-SIM leases, canary checks, operation IDs, guards, PCM protocol and resume tickets. Native AudioRecord/AudioTrack, audio focus, echo/noise effects when present, mute, speaker and DTMF. Mic starts only from visible Call/Answer with permission and an active foreground service. No emergency/location-service claim.
- Remote SMS send once and latest-50 message events. A durable pending operation marker precedes dispatch; network loss does not queue/resend a paid operation. Incoming notifications do not display message content on the lock screen. Initial historical SMS are displayed, not all re-notified.
- A phone-installed SIM uses **Android's system dialer**; its inbound cellular calls remain with the system phone app. An OTG eSIM reader has no radio: use server VoWiFi for its calls/SMS. This preview does not bridge the phone's own modem/SMS or provide host cellular forwarding.

## Sessions, power and recovery

### Current qualification boundary

The owner confirmed two-way speech on the resumed incoming modem call. Together
with the separately recorded outgoing voice, self-SMS, history/Reply, five native
pages and bounded recovery cases, this supplies scoped preview acceptance, not a
claim that every carrier or hardware permutation is qualified. The current APK
and minimal Core fixes are deployed; draft PRs still require owner review.

Actual Wi-Fi/cellular-data handover remains untested. On September 27 the owner
made extreme network-switching validation non-blocking for the main workflow;
a phone-native data SIM is not a prerequisite for continued delivery. This does
not remove ordinary automatic reconnection or session recovery, whose existing
physical evidence is retained. Long-duration USB/OEM/battery qualification and
the historical write timeout cause remain unverified and are deferred from this
preview's delivery gate under ANDROID_PREVIEW_CLOSE; short samples do not close
them. A new reproducible main-flow failure still warrants investigation.

Cookie/CSRF sessions slide through existing Core validation. Expired administrator sessions can renew using remembered encrypted login, without enabling availability or reader sharing. Rejected passwords, certificate identity failures and revoked reader credentials require user action. Reader enrollment is distinct from the administrator session. NetworkCallback changes replace sockets; epoch guards reject stale callbacks. Reconnect uses capped jittered backoff without an attempt limit and never redials/resubmits messages.

A bounded physical QA check exercised invalid JSON after a snapshot, unknown
message type, unsupported schema and heartbeat-before-snapshot against a local
synthetic TLS peer. All four connections closed and recovered automatically;
the fifth valid snapshot restored native online state in the same App process.
This used the retained CI QA `f627204`; its relevant Link control flow is unchanged
in `a1bf28b` except diagnostic recording. It is scoped recovery evidence, not a
fresh full-v85 or carrier test. Reader sharing stayed off; prior encrypted QA
state was restored byte-for-byte, QA stopped, and temporary resources removed.
Earlier locked/autofill/save-password setup failures remain separate from the
completed cases. No password-manager setting, production configuration or paid
operation was changed. Existing isolated-Core restart evidence separately covers
automatic remembered-login renewal, not a natural twelve-hour expiry experiment.

Twenty-five retained production readbacks now span about 155 minutes on v85 with
the same Agent process generation, reader session and identified card, fresh
heartbeats and unchanged desired catalog. Five control-connection timestamps
appear across the interval, including the Core rollout; installed and final App
PIDs match. This is sampled continuity/recovery, not gap-free monitoring, overnight
endurance or measured battery life. A final read-only history request returned
the same ten event IDs for this reader's line as the earlier post-rollout snapshot,
without a new SMS or replay. It does not directly qualify Telegram or future
carrier delivery. The final black screenshot and empty filtered USB log are not
positive UI or error-free evidence.

One subsequent eight-minute background sample retained the same v85 process,
reader/card, foreground service and desired state. App-UID CPU increased by
6.999 seconds with no foreground-activity time or wake-lock-stat change. This is
a scoped runtime-cost measurement, not measured battery life. Wi-Fi accounting
also increased by about 19 MB received. A separate read-only mobile stream sample
found roughly 150 KB snapshots every three seconds with only readiness
`received_at`/`expires_at` changing. The old digest therefore defeated
unchanged-state suppression on live health updates. The current Core candidate
excludes only those two readiness receipt clocks from a copied comparison, not
from the transmitted snapshot or shared cache. Actual freshness, producer/card
identity, observation sequence, incoming calls and SMS changes remain significant;
authentication and the heartbeat cadence are unchanged. A real WebSocket/replay
regression and the semantic-change matrix both failed with normalization removed,
then passed with race detection after restoration. The first WebSocket attempt
had an incomplete catalog fixture and is not counted as defect reproduction.
Full Go workflow `36298445701` passed at `b6f3a4c`. Its verified Core was deployed
with idle/maintenance guards and a checked rollback backup; unrelated processes,
six Agent generations, desired lines and notifications were preserved. The real
post-rollout stream sent one initial snapshot and two heartbeats in 65 seconds,
with no timestamp-only snapshot. Stable v85 then received about 0.01 MB in two
background minutes and used 151 ms of CPU, versus about 19 MB and 6.999 CPU seconds
in the prior eight-minute sample. Counters are rounded; these are scoped runtime
and traffic measurements, not a laboratory power or all-day battery qualification.
Actual Home/Readers and final Core confirmed the same card, online state and idle
call/media ownership. A later short reader-link reconnect remains recorded; no
gap-free socket uptime is claimed. No power setting, client intent, APK, Provider,
desktop Agent or paid operation changed. Temporary collectors and sessions ended.

Observer-only mode receives changed snapshots plus a 30-second heartbeat rather than polling the whole desktop page. Core shares a one-second read cache, limits Provider concurrency to eight and a two-second deadline, and caps mobile presentation at 128 lines / 50 messages with an explicit incomplete flag. Existing routes remain available through gateway management for larger installations.

Reader mode must satisfy the current Core 10-second health interval; unchanged topology is omitted. No idle wake lock, exact alarm, heartbeat restart watchdog or automatic boot activation. A bounded partial wake lock is used only during a call. Pause stops links and sharing. After force-stop or reboot, open the app to resume. Android Doze/OEM power management may still delay inbound delivery despite a visible foreground notification. This is **not** a claim of guaranteed 24/7 reachability or measured battery life. Physical screen-off evidence retains its recorded limits; extreme cellular-handover validation is unverified and owner-designated non-blocking, not a main-flow release gate.

Call transport recovery resumes the same lease/ticket within a bounded window. It never acquires another paid call after failure. If media cannot recover, close audio and rely on existing server guards; keep the unknown call visible for explicit status checks/hangup. Process death follows browser-equivalent semantics: no persistent call owner is restored. After reopening, use Call history. This client does not require PR #8 pairing, call recovery, receipt or message-sync endpoints.

## Build, tests and release

Pinned build: JDK 17, Gradle 8.13, Android Gradle Plugin 8.11.1, compile/target SDK 36,
NDK r30 (`30.0.16248370`). From repo root:

```sh
gradle -p android-agent testDebugUnitTest lintDebug assembleDebug
# Connected emulator/device, no SIM or paid call:
gradle -p android-agent connectedDebugAndroidTest
```

The workflow also runs Core tests and builds an installable, **non-debuggable test-signed** preview APK, verifies its signature/manifest and retains checksums and test reports. Unit tests exercise TLS pin mismatch/redirect refusal, same-origin paths, QR validation, call/SMS identities, CCID framing/bounds, fixed APDUs and jitter. Instrumentation exercises native setup, Keystore and real Android audio callbacks/canary/reconnect on a local TLS fixture, not a carrier.

Authorized workflow dispatches use the existing stable preview signing identity; pull-request builds do not receive its secrets. Never uninstall a differently signed installed app to bypass verification. Do not install with real administrator credentials on untrusted devices. Field compatibility, notification delivery and battery acceptance remain separate from the build.
