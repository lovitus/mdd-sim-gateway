# MDD Android Agent — native preview

This is a fresh native Android app, not a WebView or an exposed VPCD service. Android 9/API 28 or newer. The application ID is `com.lovitus.mddagent.preview`; it does not replace an unknown historical Android package.

## Recovery candidate

Native views use the official Material Components 1.14.0 library without
replacing the Java Activity/Service or Go business owners. The main call/send
action is outside the scroll area; the transport uses a single-selection
segmented control. Google Material icon provenance and Apache license copies
are packaged in `app/src/main/assets/licenses/`.

The PR #8 candidate adds durable original-call capabilities, exact terminal receipts,
multi-operation SMS uncertainty records and cursor-based catch-up. It requires the
matching Core, Provider and modem Agent changes; old components must fail visibly,
not be treated as an accepted recovery path. A normal authenticated login plus the
original private recovery capability is required after session replacement.

USB application selection ports the existing Go `agentsim/apdu.go` sequence:
bounded EF.DIR enumeration, full matching AIDs, then partial-name selection with
both supported response modes. Identity failures retain safe status codes;
read-only metadata repair has a three-attempt backoff and never verifies a PIN.

Native call controls follow both the call owner and audio availability, so mute,
speaker and DTMF appear when preparation finishes without changing tabs. The
retirement order follows the existing `webui/src/goCallCoordinator.jsx` release
path: close local audio before network cleanup. Exact terminal receipts still
decide call completion; media closure and a successful DELETE do not. The final
dispatch gate rejects a closed canary, and lifecycle callbacks do not run while
holding the audio monitor. Incoming cellular DTMF retains the same subject,
line, session, active-state and heartbeat checks as outbound DTMF.

`CallFlowTest` drives both transports through native call/answer/reject/mute/
speaker/DTMF/hangup/status controls using a TLS-only test peer. It checks a
paged-out incoming notification and the next incoming call without a tab cycle,
unknown end versus exact receipts, microphone closure before a blocked DELETE,
and zero start/answer after canary failure. The real Go Core test covers incoming
lease, binary canary, answer and DTMF with durable recovery association. These
new scenarios require candidate CI/device evidence and are not carrier tests.

SMS keeps the original recipient/body encrypted only while the operation is
unresolved (256 KiB aggregate body budget); a complete matching submission
receipt removes the retained body. A per-part event only records partial
submission evidence, never whole-message success or delivery. Manual checking
reuses cellular `reconcile_only` and exposes the Provider's existing durable
replay lookup through a separate receipt-only route. A missing receipt cannot
fall through to sending, and old records without a body are not reconstructed
from the current editor. The private-state size limit is checked before key
creation/publication, preserving the prior readable file on rejection.

Message operations refresh independently of the composer. Existing Core
conversation grouping and oldest-to-newest history pages are reused, including
archived lines outside the compact mobile directory. Modal pages are fenced by
the current account epoch. SMS catch-up uses one owner, with six bounded retry
attempts after a transient failure; ordinary snapshots cannot reset an exhausted
budget. A genuinely reconnected observer can reopen a network-failure budget,
while authentication/TLS/schema failures require a new owner or explicit retry.
One generic account-scoped Android notification summarizes catch-up; suppressed
permission and submitted-to-Android are distinct from receipt or user delivery.
Confirmed certificate-transition chains preserve same-account SMS records and
sync progress after successful authentication, without transferring call control.

Availability, pause, sharing and logout writes use one process-owned ordered
intent queue. Explicit Start persists availability; sticky/Activity restoration
only reads the saved choice. Cancelling reader sharing is available while paused.
Late enrollment responses cannot re-enable sharing after cancellation. Permission
actions disappear when the particular USB device is already authorized.

### Scoped device evidence (2026-09-22)

Candidate `e05a711217843a74035ae68d38bf4f2d433686e2` passed Android workflow
`35703328072` and full Go workflow `35705321939`. Its independent testbed APK
SHA-256 is `efefdc55e0acc472de845add2926a598581503d1a7254e9930dd9d12b767a11c`.
On the authorized API 35 phone, eight instrumentation tests passed. Normal native
login and explicit USB permission/sharing read a real APDU-level CCID card's
ICCID/IMSI/MCC/MNC; the Core and Reader page agreed on identity readiness.

The matching Linux Provider, isolated from production and connected to that
reader through authenticated Core/Agent transport, completed real AKA/IKE and
IMS registration through the owner-selected exit: four IKE requests, four
responses, zero timeouts. An external controller then killed only the testbed
process. At the 30-second sample it had a new PID and Agent generation, the same
card was ready, and IMS remained ready. These are scoped registration and idle
process-recovery observations, **not** a paid call, active-call recovery or SMS
delivery test. No paid action or PIN/profile mutation was performed.

Five native pages were captured in normal, 320dp/200% font and landscape modes.
The review found clipped keypad text and fullscreen landscape keyboard takeover;
candidate `e205f7c62250e5910146e622611f051bea4e3077` passed Android workflow
`35710403681`. Its four new ordered-intent device tests passed, and fresh physical
screenshots confirmed full keypad glyphs and visible primary controls beside the
landscape keyboard. One existing synthetic SMS UI test failed to find its second
confirmation; the captured active screen was the launcher. That test now waits
for the target dialog and clicks its actual Cancel button instead of injecting a
global Back key. Subsequent candidates completed those reruns; this did not
establish a diagnosed product or Android Back-navigation cause. The test SSH channel later closed remotely and
the scoped supervisor removed its Provider/SOCKS processes. Long-session,
three-mode battery, unstable-cellular and business acceptance remain open.
Raw device identifiers, credentials, captures and exact private environment paths
are retained outside Git in the task's evidence inventory.

Candidate `f70b21cb0d7d89d36656cca48f26a920e7f20324` passed Android workflow
`35746198344` and full Go workflow `35747625904` (attempt 2). The first full-Go
attempt failed before recording assertions because Chrome startup timed out;
no assertion was removed or weakened. All 33 QA instrumentation tests passed on
the authorized API 35 phone in 150.761 seconds. Its QA APK SHA-256 is
`bab08ea1bc90cdfb51946718fceff3ea00839844764d983e5245a66a0036f187`.
The normal preview was updated in place with its stable signing certificate;
remembered login inputs remained present. No production component was deployed.

### External process-recovery gate

`scripts/verify-process-recovery.mjs` controls only the exact CI-built `.preview.qa`
APK on one explicitly supplied ADB server/serial. It clears that disposable QA
package between cases, never the normal preview or earlier user-owned testbed.
The separate `go-runtime/tools/android-process-fixture` executable reuses the
repository's `coder/websocket` v1.8.15 and standard TLS/HTTP. Upstream's latest
release was checked on 2026-09-23; no dependency upgrade or new production mode
was needed. The helper is built by CI for Linux amd64 and Darwin arm64, not
included in a customer release.

Both transports exercise canary-held preparation, accepted-but-unacknowledged
start followed by remote termination, and unknown SMS A plus submitted B. The
controller uses normal UI login and checks the first-trust fingerprint against
the independent host peer. Only the TLS data port is reversed; authenticated
control stays on a different host loopback listener. Killing the exact QA PID
after HOME uses its own UID, and service reconnection must occur before the
controller reopens the Activity. The peer retains original identities and
execution counters outside the killed process. Receipt lookup cannot send.

Each foreground invocation covers one transport's three cases, with a
560-second work deadline and a 590-second hard cleanup bound. Both groups are
mandatory in CI. Fault injection is protocol-event-driven; after killing the PID,
recovery observation starts with a quiet 30-second wait and one bounded event
request. No background status task or repeated per-turn query is required.
On candidate `29e7f37`, API 28 passed all six cases. API 35 passed the original
33 tests but the external controller stopped at its window guard: global focus
is absent from the `dumpsys window windows` subsection on that API. Reading the
complete WindowManager dump fixes that compatibility issue without weakening
the exact QA-window requirement. Five cases then passed on the physical API 35
phone with the same CI APK/fixture; the sixth hit the combined work deadline.
Cleanup succeeded. Splitting the run into two bounded transport groups preserves
every assertion. The complete VoWiFi group then passed on that phone; combined
with the first run's three cellular cases, all six now have physical-process
evidence. The original 5/6 timeout report is retained, not rewritten as a pass.
Candidate `29d3619870e740b3ac153bf3eaf5b48baa959ffd` subsequently passed Android
workflow `35758632368`: API 28/35 each completed the original 33 tests and both
external-process groups, with all cleanup reports empty. Its App source and
build configuration are unchanged from f70b21c; actual APK identities remain
separately recorded rather than relabeling older physical artifacts.
Both physical runs used QA APK SHA-256
`e688a003aa34a31a7295a73a81cad0646b69b4e37a437d2c7fe12f0115239726`
and Darwin peer SHA-256
`c85c92bd867820a4dd2fd4a0038b6193ef6342ac0d37992072cb77962534fa1c`.
The first controller SHA-256 was
`4ed4426b476aa122a1d473dc7cf5d9e7778ef4e92e8f8271c5ee0b940c8bbb75`;
the grouped controller was
`53151face82f658fa609fe0f2f0cea13c5589977a709a6a510233ada8af4de82`.
These external cases are recorded separately from the original 33 tests.
Zero new lease/media/PCM proves no network media reconstruction, not independent
system microphone instrumentation. This gate does not establish real-carrier,
unstable-cellular, screen-off or battery acceptance.

Full-Go workflow `35761052890` on 29d3619 failed in the existing recording-browser
test before audio assertions: `window.runRecordingTest is not a function`.
The harness created a target page and then navigated again while accepting any
load event. It now creates a blank tab and matches the requested frame/loader's
lifecycle load, including events arriving before the navigate response. Focused
stale-loader/frame and early-event counterexamples are included; all real audio
assertions remain. The failure log does not identify the original loader order,
so it is not proof of a particular stale event. This harness correction still
required its own CI execution; no production recording code changed. Candidate
`2d2c610f10eb6af531681cdbdcf7f22411efea9f` subsequently passed Android workflow
`35762220201` and full-Go workflow `35763271113`, including the real recording
gate. Those successful results do not waive remaining field acceptance.

### Link And Original-Call Control Candidate

The next candidate clears a connection's old media challenge before creating a
replacement WebSocket. Otherwise the periodic evidence task can queue the old
challenge ahead of the new resume handshake. Original session, resume ticket
and epoch are retained; only the new claim installs a challenge.

The existing native CallFlow test peer now checks initial and resumed delayed
handshakes, rotating identities, deterministic one-request 503 rejection, fresh
capture/playback increments and mute preservation for incoming/outgoing calls
on both transports. A long outage closes local audio at the existing recovery
deadline without redial. Its unconfirmed remote end is an explicitly injected
guard failure, not normal ten-second guard behavior or carrier acceptance.

The external controller's separate `control` suite covers original-call access
after two actual PID deaths and normal UI reauthentication. New token/CSRF alone
cannot replace the original recovery capability. Each user end click receives
one host-side fixture permit; an automatic end fails the test. First execution
returns a lost-ack 502 while terminal lookup is gated with 503; the second
explicit request must reuse the original end ID and return the retained receipt
without executing again. Real Core cellular/Provider handler tests additionally
exercise original ownership, exact targets and durable terminal replay; the
Provider handler test does not replace outer Core login/authentication tests.

Candidate `de0a9a4` built successfully and its exact QA APK passed all 34 methods
on the authorized API 35 phone in 218.594 seconds, including the four new
short/long incoming/outgoing flows. QA APK SHA-256 is
`5155552a4f99d091f9e23735d270f8cdf3f52e1fea7878d77654ac9e5a842a11`.
Screenshots confirm restored audio controls and the retained explicit end action
when local audio closes without a confirmed remote end; they are not carrier
evidence. No normal preview/testbed data or device network switches changed.

Its Android workflow `35768334456` stopped at the new real-IPC Provider test,
so emulator/control jobs did not run. That test exposed a product defect:
the Core recovery consumer tried to match a direct `OperationError`, whereas
the real HTTP client returns `ResponseError` containing that failure. A genuine
`call_active` response therefore became unknown and could not reach recovery
end. The scoped correction checks HTTP 412, `not_ready`, `call_active` and `call`
on the existing wrapper. It does not change global error unwrapping, original
capabilities or Provider/card/generation fences. Independent wrong HTTP status,
kind, layer and code tests require zero end dispatch. The pre-fix failure is
preserved. Candidate `63ea7e0` passed the corrected real-IPC Core tests and build
in workflow `35770240109`. All four device jobs then stopped before emulator
startup because a stale runner APT index referenced a removed libxml2 package.
Their setup now refreshes the index, as the existing Core job already does;
no dependency or test gate was skipped.

Both external control cases passed on the authorized API35 phone using the same
de0 QA APK and 63e Darwin peer SHA-256
`87b573fd1c400208e1319352b5ab1abfb1efe0f46795359f8707e428165fd609`.
Controller SHA-256 is
`3c94b39932c4c9c9b641d375a60838613d61b7a48ab5ff43bd30b00daa055416`.
Each case observed two new App PIDs, two logins, one lease/media/start, two
explicit end requests, one simulated end execution and one lease cleanup.
No automatic end or new media was admitted; cleanup reports are empty.
These are real native UI/process observations against an external synthetic
peer, separate from the real Core IPC tests and not a carrier acceptance claim.
Candidate `990d929` passed the full-Go workflow `35773331819`. Android workflow
`35772122790` passed Core/build, API35's 34-method/process gate and API28's
control gate, but retained two failures: API28 read a null accessibility root
after cancelling a dialog, and API35's control case lacked a final-state capture.
Those failures were not rerun into a passing claim. Click synchronization now
waits for the exact QA control within the original pre-click budget; submission
is still clicked once. Controller failures retain the current QA image/XML and
timestamped peer state, with distinct recovery stages.

A targeted cold-Activity run on the same physical APK reproduced a real UI gap:
the original cellular call ended (end requests 2, execution 1, lease deletion 1),
but the selection reverted to unavailable VoWiFi and left Call disabled. The
fresh screenshot and peer terminal record are retained. This does not identify
the unrecorded final state of the earlier CI failure. Cold entry now seeds the
original line/card/transport from the pending record only on the first successful
load without a saved selection; saved mode-only drafts and later user edits win.

Review also found that a replaced RemoteCall could finish a delayed durable
transition and erase the replacement's record. The candidate invalidates old
workers before loading replacement state and checks ownership within the existing
store lock for every update/delete. Already admitted commits may finish; the
subsequent read sees that result. Native audio, queued starts, status callbacks
and UI notice publication retain their owner checks. Safety hangup still closes
local audio immediately and does not wait behind storage; a sent end remains
unknown until the current owner obtains an exact receipt. Fresh preparation is
busy, whereas a restored unresolved call remains eligible for reauthentication.
Rejected task admission releases its acquired wake lock and in-memory owner.

Four additional native methods cover both store-lock orderings, saved mode-only
selection and rejected admission (38 total). Candidate `8701773` passed all 38
on both CI emulator APIs and on the authorized API35 phone (239.78 seconds).
Its QA APK SHA-256 is
`77d3b99e844b56df7ec16fe125eced315383683f51077177156f0a9cb12bf7dc`.
Both cold-Activity control cases also passed on the phone using its Darwin peer
SHA-256 `9c18c12c3cc6411b9893000c8d1899dba4e51dc7452398c551ae708647138ce2`
and controller SHA-256
`64fd26d4692ebb3d92c5b07f7ba482b428769779777b426c76a23fc9e71763b2`.
The fresh post-fix screenshot retains cellular selection and an enabled Call
action after the exact original terminal receipt, without changing capabilities.

Workflow `35779811296` retained one remaining API28 external-controller failure
before any lease/start: the target control was not yet in the accessibility tree
after navigation. The current script uses the existing bounded UI waiter for
page/target readiness before a single tap or fill. This does not extend the
work/cleanup bounds. Its other control groups and both original process suites
passed. Full-Go `35781017907` separately failed at Chrome startup before recording
assertions. Both failures remain recorded; the final same-head gates are pending.

There is one external control case per transport. The original six process cases remain.
Each control invocation retains the 560/590-second bounds; a separate CI job
uses the already built QA artifacts and is a required preview dependency.
No device network switch, production guard, real SIM or paid action is changed.

Configuration publication uses checked file sync, rename, parent-directory sync and
exact encrypted readback. Original legacy ciphertext is retained. Unreadable state
is not first-time setup, and unresolved messages are not overwritten by the next send.
Provider disappearance and media closure are not call-end evidence. Recovery uses
the original media session and paid-operation identity; it never redials.

Debug/instrumentation now uses the separate `com.lovitus.mddagent.preview.qa`
package, labelled as QA-only. Do not run fixtures on the earlier `.testbed` package
once the owner has started using it; preserve that application's data. Normal
preview use belongs to `com.lovitus.mddagent.preview`, never the QA package.
Candidate tests, physical reader behavior and battery/long-session results must be
reported separately; implementation alone is not acceptance. See the
[development and acceptance plan](../docs/decisions/2026-09-22-android-recovery-remediation.md).

## Setup

1. Install the normal preview APK, open it, and enter the gateway HTTPS address, account name and password. Bare host/port input uses HTTPS. Remember-login stores the draft encrypted with Android Keystore; uncheck it or use Forget saved password to remove the durable password. Failures and tab changes do not clear current input.
2. On first connection, inspect the server/fingerprint confirmation before trusting it. The inspection-only TLS socket always aborts before application data; only the separately pinned login client can send credentials. A changed certificate requires explicit replacement, with old/new fingerprints displayed. Invalid dates and supplied-fingerprint mismatches are rejected. Manual fingerprint entry remains under collapsed advanced options.
2. Alternatively scan/paste the setup JSON below; inspect and explicitly confirm the origin/pin. The account still requires sign-in. Reader enrollment may be included only in a QR kept private.
3. Choose **Stay available** and grant notifications. Choose **Readers → Share attached readers**, then grant access to the particular OTG USB device. The gateway account must permit issuing the scoped Agent credential. Existing lines/SIM routing remain configured in gateway management.
4. Choose the exact line and **VoWiFi** or **Cellular modem** in Calls/Messages. A paid mutation requires a confirmation. Uncertain submissions are not retried. Resolve the existing call with Hang up; check SMS history before any deliberate new send.

```json
{"type":"mdd-agent-setup","version":1,"server":"https://gateway.example","certificate_sha256":"<64 hex digits>"}
```

Optional `agent_id` and `agent_token` must both be present. Do not put account passwords in QR codes, logs, screenshots or support exports. The app stores opaque sessions/CSRF/enrollment encrypted with Android Keystore AES-GCM, disables backup and exports no service. Sign out stops sharing locally; revoke the issued reader credential in gateway settings when retiring a device.

**Core compatibility:** this PR adds authenticated `/v1/mobile/ws` to Core. Build/run the matching Core preview before expecting native background events. Older Core returns a visible update-required state, not a fabricated empty/healthy screen. This is not a production deployment action.

## Presentation candidate

The U7 batch keeps the existing Activity/Service and Material Components 1.14.0.
App-owned status text resolves from English/Chinese resources at render time;
resource IDs are in-memory only. Business readiness, identities, phases and wire
commands remain machine fields. Local media closure is not carrier termination,
and mute/speaker state cannot overwrite uncertain business state.

Line identity wraps the full number and card suffix below its name, including
growing dropdown rows. Labels do not imply that IMS readiness covers the selected
route. Unchanged label lists keep their adapter while current capabilities still
refresh. Retained call display names/numbers do not enter any mutation request.
Delivery events use both kind/state; submitted parts remain explicitly parts.
Pinned Material icons, owner-driven checked state and accessible labels replace
ambiguous audio-toggle text. Identity details scroll; the main end action stays fixed.

Settings exposes installed version/code/source revision and a read-only diagnostic
dialog. Display and explicit share use one allowlisted snapshot: platform/version,
booleans, counts, observed network type and local call phase. No configuration,
endpoint, token, SIM identity, phone number, message body or raw error is exported.
Observed VPN/unknown is not proof of internet access or cellular egress.

Three new presentation methods are pending CI/device execution (41 total native
methods): English/Chinese five-page render checks, real multiline dropdowns at
320dp/200%, drafts and diagnostic redaction, plus 100%-font landscape keyboard and
live-call visibility. QA process-local resource scope is recorded and restored;
phone-wide language settings are not changed. Screenshots and usable-window bounds
are required, not merely view existence. Older completed recovery evidence remains
scoped and is not reset by this pending UI batch.

## Implemented paths

- Native Home/Calls/Messages/Readers/Settings screens, QR setup, exact line/route selection, generic private notifications, pause, safe diagnostic share and battery-settings guidance.
- USB host CCID **APDU-level, slot 0** readers (up to eight). Active-profile ICCID/IMSI/EF-AD reading and fixed USIM/ISIM AKA; no general APDU tunnel, PIN guessing, profile download, arbitrary card mutation or eSIM deletion. Interrupt changes invalidate the session generation; identity is re-read before AKA. TPDU-only readers and inaccessible/locked cards are not advertised as ready.
- OMAPI uses Android's standard access-controlled logical channel. Many devices/cards do not authorize ordinary applications; these are clearly unavailable. No root, hidden APIs, carrier-privilege bypass or misleading embedded-eSIM promise.
- Server VoWiFi and cellular-modem call paths use the existing exact-SIM leases, canary checks, operation IDs, guards, PCM protocol and resume tickets. Native AudioRecord/AudioTrack, audio focus, echo/noise effects when present, mute, speaker and DTMF. Mic starts only from visible Call/Answer with permission and an active foreground service. No emergency/location-service claim.
- Remote SMS send once, recent-message summaries, per-line history and durable forward-cursor catch-up. A durable pending operation marker precedes dispatch; network loss does not queue/resend a paid operation. Incoming notifications do not display message content on the lock screen. Initial historical SMS are displayed, not all re-notified.
- A phone-installed SIM uses **Android's system dialer**; its inbound cellular calls remain with the system phone app. An OTG eSIM reader has no radio: use server VoWiFi for its calls/SMS. This preview does not bridge the phone's own modem/SMS or provide host cellular forwarding.

## Sessions, power and recovery

Cookie/CSRF sessions slide through existing Core validation. Optional remembered credentials are an encrypted local form profile, not an automatic re-login loop. Revocation, TLS failure and server restart/expiry stop reconnection until sign-in. Sign out stops the session but preserves remembered form data; Forget saved password clears it without altering an active session. Reader enrollment is distinct from the administrator session. NetworkCallback changes replace sockets; epoch guards reject stale callbacks. Reconnect uses capped jittered backoff and never redials/resubmits messages.

Observer-only mode receives changed snapshots plus a 30-second heartbeat rather than polling the whole desktop page. Core shares a one-second read cache, limits Provider concurrency to eight and a two-second deadline, and caps compact mobile presentation at 128 lines / 50 messages with an explicit incomplete flag. The native directory search/pagination and exact-line lookup reach lines outside the compact feed; incoming events and SMS catch-up have independent contracts.

Reader mode must satisfy the current Core 10-second health interval; unchanged topology is omitted. No idle wake lock, exact alarm, heartbeat restart watchdog or automatic boot activation. A bounded partial wake lock is used only during a call. Pause stops links and sharing. After force-stop or reboot, open the app to resume. Android Doze/OEM power management may still delay inbound delivery despite a visible foreground notification. This is **not** a claim of guaranteed 24/7 reachability or measured battery life; screen-off/cellular handover tests are required on actual devices.

Call transport recovery resumes the same lease/ticket within a bounded window. It never acquires another paid call after failure. If media cannot recover, close audio and rely on existing server guards; keep the unknown call visible for explicit reconciliation/hangup.

## Build, tests and release

Pinned build: JDK 17, Gradle 8.13, Android Gradle Plugin 8.11.1, compile/target SDK 36. From repo root:

```sh
gradle -p android-agent testDebugUnitTest lintDebug assembleDebug
# Connected emulator/device, no SIM or paid call:
gradle -p android-agent connectedDebugAndroidTest
```

The workflow also runs Core tests and builds an installable, **non-debuggable test-signed** preview APK, verifies its signature/manifest and retains checksums and test reports. Unit tests exercise TLS pin mismatch/redirect refusal, same-origin paths, QR validation, call/SMS identities, CCID framing/bounds, fixed APDUs and jitter. Instrumentation exercises native setup, Keystore and real Android audio callbacks/canary/reconnect on a local TLS fixture, not a carrier.

The normal preview now uses a stable dedicated signing identity supplied through repository Actions secrets. Missing signing material fails the build; it never falls back to a disposable key. The certificate SHA-256 is `8b5e818fccfa6f6738cd53658b2df9ab57a014f9a5269decc4104d91894dd5e2`. Private signing material is backed up outside Git. Existing installations with another signature must not be uninstalled automatically; preserve their data and use a distinct authorized package for testing. Stable preview signing is not Play Store publishing, carrier qualification, notification-delivery or battery acceptance.

Certificate discovery adapts this repository's legacy `agent/android/.../VpcdClient.kt`
at `4368aaa^`, with explicit consent and a handshake-aborting inspection context
instead of silently trusting or resetting pins. OkHttp's official tags were checked
(latest observed `parent-5.5.0`); the required strict pinning/redirect controls exist
in the current pinned 4.12.0 API, so this fix does not add a dependency migration.
