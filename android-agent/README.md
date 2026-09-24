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

Pinned build: JDK 17, Gradle 8.13, Android Gradle Plugin 8.11.1, compile/target SDK 36. From repo root:

```sh
gradle -p android-agent testDebugUnitTest lintDebug assembleDebug
# Connected emulator/device, no SIM or paid call:
gradle -p android-agent connectedDebugAndroidTest
```

The workflow also runs Core tests and builds an installable, **non-debuggable test-signed** preview APK, verifies its signature/manifest and retains checksums and test reports. Unit tests exercise TLS pin mismatch/redirect refusal, same-origin paths, QR validation, call/SMS identities, CCID framing/bounds, fixed APDUs and jitter. Instrumentation exercises native setup, Keystore and real Android audio callbacks/canary/reconnect on a local TLS fixture, not a carrier.

Authorized workflow dispatches use the existing stable preview signing identity; pull-request builds do not receive its secrets. Never uninstall a differently signed installed app to bypass verification. Do not install with real administrator credentials on untrusted devices. Field compatibility, notification delivery and battery acceptance remain separate from the build.
