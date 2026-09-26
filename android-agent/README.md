# MDD Android Agent — native preview

This is a fresh native Android app, not a WebView or an exposed VPCD service. Android 9/API 28 or newer. The application ID is `com.lovitus.mddagent.preview`; it does not replace an unknown historical Android package.

## Setup

1. Install the preview APK, open it, and enter the gateway **HTTPS origin**, account name and password. Remembered login is encrypted using Android Keystore; disable Remember or use Forget login to remove the saved password. For self-signed servers, independently verify the displayed **leaf certificate SHA-256** before accepting it. Certificate changes require explicit confirmation; there is no trust-all toggle or credential-bearing redirect.
2. Alternatively scan/paste the setup JSON below; inspect and explicitly confirm the origin/pin. The account still requires sign-in. Reader enrollment may be included only in a QR kept private.
3. Choose **Stay available** and grant notifications. Choose **Readers → Share attached readers**, then grant access to the particular OTG USB device. The gateway account must permit issuing the scoped Agent credential. Existing lines/SIM routing remain configured in gateway management.
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
scans rearm recovery for a later fault. If that reset fails specifically during
the read-only slot-status handshake and fresh command writes still fail, one
additional reset is permitted at least 30 seconds after the first attempt.
There is no third reset, and permission, shape, claim, native reset, power-on and
identity failures do not gain a retry. Continuing failure does not cause a reset
loop. Home/Readers distinguish the pending followup from exhausted recovery and retain the
unplug/OTG advice. No wake lock, root, hidden Java API, server change, permission
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
uses real UsbRecovery policy, not USB hardware; Android CI and actual automatic
followup acceptance are still pending. The physical sleep-fault cause is unknown.

Call history now displays direction, peer, line name/number/card when present,
localized colored status and local time. Missing catalog details remain unknown;
the UI does not invent an immutable historical SIM snapshot.

Recent/history messages show peer, line name, own number when known, card suffix,
transport, time and body. Reply revalidates the original line/card and fills that
SIM, transport and peer into the existing composer; it does not send automatically.
An existing draft needs confirmation before replacement. Alphanumeric senders,
missing identities and changed cards are not guessed into another recipient/SIM.
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

Cookie/CSRF sessions slide through existing Core validation. Expired administrator sessions can renew using remembered encrypted login, without enabling availability or reader sharing. Rejected passwords, certificate identity failures and revoked reader credentials require user action. Reader enrollment is distinct from the administrator session. NetworkCallback changes replace sockets; epoch guards reject stale callbacks. Reconnect uses capped jittered backoff without an attempt limit and never redials/resubmits messages.

Observer-only mode receives changed snapshots plus a 30-second heartbeat rather than polling the whole desktop page. Core shares a one-second read cache, limits Provider concurrency to eight and a two-second deadline, and caps mobile presentation at 128 lines / 50 messages with an explicit incomplete flag. Existing routes remain available through gateway management for larger installations.

Reader mode must satisfy the current Core 10-second health interval; unchanged topology is omitted. No idle wake lock, exact alarm, heartbeat restart watchdog or automatic boot activation. A bounded partial wake lock is used only during a call. Pause stops links and sharing. After force-stop or reboot, open the app to resume. Android Doze/OEM power management may still delay inbound delivery despite a visible foreground notification. This is **not** a claim of guaranteed 24/7 reachability or measured battery life; screen-off/cellular handover tests are required on actual devices.

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
