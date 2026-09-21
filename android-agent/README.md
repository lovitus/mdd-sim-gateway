# MDD Android Agent — native preview

This is a fresh native Android app, not a WebView or an exposed VPCD service. Android 9/API 28 or newer. The application ID is `com.lovitus.mddagent.preview`; it does not replace an unknown historical Android package.

## Setup

1. Install the preview APK, open it, and enter the gateway **HTTPS origin**, account name and password. Passwords are not stored. For self-signed servers, verify the **leaf certificate SHA-256** via an independent trusted server console and paste it. Certificate changes fail closed; there is no trust-all toggle or credential-bearing redirect.
2. Alternatively scan/paste the setup JSON below; inspect and explicitly confirm the origin/pin. The account still requires sign-in. Reader enrollment may be included only in a QR kept private.
3. Choose **Stay available** and grant notifications. Choose **Readers → Share attached readers**, then grant access to the particular OTG USB device. The gateway account must permit issuing the scoped Agent credential. Existing lines/SIM routing remain configured in gateway management.
4. Choose the exact line and **VoWiFi** or **Cellular modem** in Calls/Messages. A paid mutation requires a confirmation. Uncertain submissions are not retried. Resolve the existing call with Hang up; check SMS history before any deliberate new send.

```json
{"type":"mdd-agent-setup","version":1,"server":"https://gateway.example","certificate_sha256":"<64 hex digits>"}
```

Optional `agent_id` and `agent_token` must both be present. Do not put account passwords in QR codes, logs, screenshots or support exports. The app stores opaque sessions/CSRF/enrollment encrypted with Android Keystore AES-GCM, disables backup and exports no service. Sign out stops sharing locally; revoke the issued reader credential in gateway settings when retiring a device.

**Core compatibility:** this PR adds authenticated `/v1/mobile/ws` to Core. Build/run the matching Core preview before expecting native background events. Older Core returns a visible update-required state, not a fabricated empty/healthy screen. This is not a production deployment action.

## Implemented paths

- Native Home/Calls/Messages/Readers/Settings screens, QR setup, exact line/route selection, generic private notifications, pause, safe diagnostic share and battery-settings guidance.
- USB host CCID **APDU-level, slot 0** readers (up to eight). Active-profile ICCID/IMSI/EF-AD reading and fixed USIM/ISIM AKA; no general APDU tunnel, PIN guessing, profile download, arbitrary card mutation or eSIM deletion. Interrupt changes invalidate the session generation; identity is re-read before AKA. TPDU-only readers and inaccessible/locked cards are not advertised as ready.
- OMAPI uses Android's standard access-controlled logical channel. Many devices/cards do not authorize ordinary applications; these are clearly unavailable. No root, hidden APIs, carrier-privilege bypass or misleading embedded-eSIM promise.
- Server VoWiFi and cellular-modem call paths use the existing exact-SIM leases, canary checks, operation IDs, guards, PCM protocol and resume tickets. Native AudioRecord/AudioTrack, audio focus, echo/noise effects when present, mute, speaker and DTMF. Mic starts only from visible Call/Answer with permission and an active foreground service. No emergency/location-service claim.
- Remote SMS send once and latest-50 message events. A durable pending operation marker precedes dispatch; network loss does not queue/resend a paid operation. Incoming notifications do not display message content on the lock screen. Initial historical SMS are displayed, not all re-notified.
- A phone-installed SIM uses **Android's system dialer**; its inbound cellular calls remain with the system phone app. An OTG eSIM reader has no radio: use server VoWiFi for its calls/SMS. This preview does not bridge the phone's own modem/SMS or provide host cellular forwarding.

## Sessions, power and recovery

Cookie/CSRF sessions slide through existing Core validation; passwords are never retained. Revocation, TLS failure and server restart/expiry stop reconnection until sign-in. Reader enrollment is distinct from the administrator session. NetworkCallback changes replace sockets; epoch guards reject stale callbacks. Reconnect uses capped jittered backoff and never redials/resubmits messages.

Observer-only mode receives changed snapshots plus a 30-second heartbeat rather than polling the whole desktop page. Core shares a one-second read cache, limits Provider concurrency to eight and a two-second deadline, and caps mobile presentation at 128 lines / 50 messages with an explicit incomplete flag. Existing routes remain available through gateway management for larger installations.

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

No stable owner signing key is supplied. Preview signing is ephemeral/test-only and is not a production upgrade chain. Do not install with real administrator credentials on untrusted devices. A later independently signed preview may require uninstalling the old preview; export no secrets to work around this. Production APK signing, field compatibility, notification delivery and battery acceptance are still open.
