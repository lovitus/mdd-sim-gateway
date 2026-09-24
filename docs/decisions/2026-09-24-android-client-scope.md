# Android client delivery boundary

Authority: the owner's September 24 instruction to continue the client-focused
proposal, without subagents or unrelated work. This supersedes conflicting Android
recovery plans, not the desktop product requirements or historical evidence.

- Base this delivery on PR #7 (`c639dd2`). Preserve the existing Android usability
  improvements where compatible; do not replace them with the old setup-only UI.
- Keep Core, Provider and WebUI at this baseline. Reuse existing reader enrollment,
  media, call and SMS APIs. A server change needs an actual incompatible request
  and a separately justified minimal adaptation.
- Fix indefinite network recovery in Android: bounded backoff without an attempt
  limit, reauthentication with explicitly remembered Keystore-encrypted login,
  and recovery from malformed messages. Do not bypass certificate validation,
  revoked reader credentials, rejected passwords or the user's pause/share intent.
- Use browser-equivalent call lifetime. App process loss does not acquire a new
  server-side recovery capability. Never automatically redial or resend SMS.
  Verify existing server cleanup behavior; do not assume every channel has a
  ten-second timeout merely because the review says so.
- Exclude PR #8 server durable receipts and the uncommitted device-pairing system.
  Preserve those branches and dirty files; exclusion is not permission to erase.
- Retain Calls, Messages, Readers, Home status and Settings usability. No landscape
  project, new authentication product or additional server recovery framework.

Failure checklist: transport loss, silent socket, session expiry, rejected saved
password, changed certificate, malformed message, stale reconnect after Pause,
and uncertain paid submission. Validation uses GitHub workflow and the separately
authorized handset. This document is scope, not evidence that those checks passed.

## Current cursor

Branch: `codex/android-client-focused`, based on `c639dd2`. Original dirty recovery
work remains untouched in its original checkout and branches.

The native UI, login/certificate interaction, reader attention, local diagnostics
and audio-focus fixes are imported from committed `7d14084`, not the dirty pairing
candidate. Calls and SMS validate identity through existing catalog and projection
APIs. Directory search is client-side. Incoming cellular records are adapted from
the existing snapshot. Message history uses the existing pagination API.

Removed runtime dependencies on mobile directory endpoints, server call recovery,
SMS receipts and the new message sync protocol. Unknown SMS is checked using GET
history, never resubmitted. Calls have no persistent recovery owner; local status
checks and history use existing APIs. Added a native call-history entry.

Reconnect and remembered-login renewal are reapplied to the imported client.
Reader rejection and TLS identity failures stay terminal. Pause/share intent is
not enabled by renewal. Existing login/UI/intent tests were reused and their
fixtures adapted to baseline routes. New malformed-frame and renewal test candidates
are preserved privately, excluded from the commit until red/green is established.
They are not test evidence. Existing tests remain enabled.

Source review found existing cellular heartbeat timeout and Provider default guard
timeout of ten seconds. This proves cleanup code exists, not a fresh runtime or
physical hangup test. Workflow signing reuses the existing stable preview identity,
restricted to approved branches/dispatch; PR runs do not receive signing secrets.

`git diff --check` and ledger regeneration passed. No baseline changes exist under
`go-runtime`, `providers` or `webui`. Initial candidate `223f279` passed the
build and Core jobs in GitHub run `35989682776`. Both API28 and API35 ran 13
instrumentation cases: 12 passed, one failed. The shared failure was the old
DeviceTest expecting synchronous setup and the retired SharedPreferences file.
Its committed `7d14084` counterpart waits for asynchronous setup and verifies the
actual encrypted file; that missing test adaptation is now included. This changes
the stale storage expectation, not the encryption requirement.
Corrected candidate `f627204f53e74c1350a11ebff034e92176a1bee8` passed GitHub run
`35990853200`. Draft PR #11 remains open against `codex/android-v2`; it is not
merged. The Android workflow covers build, lint, unit tests, API28/API35
instrumentation, Core/agentlink race tests and existing WebUI checks. The same
head subsequently passed the existing full Go Runtime workflow `36002250550`,
including full Linux Core tests/race/vet and release validation. Neither run
covers the excluded new recovery regressions. No workflow changes or tagged
release were made for this additional validation.

The downloaded preview APK matches its SHA-256 manifest:
`458998be95dafc252a27f24870c1cd8c4cf5c941c9e4737a8525fef11fc0787c`.
Its recorded source is this exact head; the CI signature report has the expected
stable preview signer. The authorized Android 13 handset now has this exact
preview installed; there was no MDD package on it before installation. No paid
operation or production deployment was performed.

The matching Linux release archive is retained privately. Its source revision
matches this head and the Core binary matches the manifest SHA-256:
`dfa49dc900d0b336d13fd7edb505657c56418edfc09ebfc0fb2cfaf88ed1a2e9`.
The owner subsequently authorized temporary isolated Core validation and minimal
mainline integration once validation passes. This exact Core was started with an
empty store, separate credentials/certificate and loopback-only listeners. Actual
login, WSS `mobile.snapshot` and logout-induced close `4401` passed. This is Core
interface evidence, not Android recovery evidence. The bounded foreground process,
SSH tunnel, remote files and listeners were removed afterwards. Production was
not changed. The existing matching mainline ancestry was confirmed; no PR #8
pairing or durable-receipt changes are included.

The owner supplied a replacement handset address. The existing isolated ADB
server connected to that sole target; authorization and shell access succeeded.
The previous target was not operated. Target details remain private. A separate
attempt to download emulator evidence ended with GitHub API EOF, not a test
failure; those screenshots and final XML counts were not freshly inspected.

Native login and explicit certificate confirmation succeeded. Home, Calls,
Messages, Readers and Settings were each clicked and their screenshots inspected.
The unshared attached reader has an attention badge and USB/sharing prompts.
After restarting the preview app, the saved session remained; Sign in again
showed the remembered masked password and reconnected without reentry. This is
remembered-login evidence, not expired-session automatic-renewal evidence.

The configured server entry returns HTTP404 for `/v1/mobile/ws`. Calls and Messages
therefore remain offline with an explicit error, not proven usable. Reader sharing
was left off; no SIM operation, call or SMS was performed. Screenshots and the
handset result are private. Two login-return helper captures failed; a subsequent
standalone screenshot and hierarchy dump succeeded, without an asserted cause.

On resumption, the authorized handset's ADB transport became offline. TCP connected,
but target-only reconnection and a fresh isolated server both failed. A second
already-installed ADB version also failed. Trace showed outgoing CNXN and read
failure without incoming AUTH/CNXN or an explicit local permission/route error.
The OS route and proxy connection metadata show this target traverses the existing
TUN/private-subnet proxy path: request bytes were sent and no response bytes were
recorded. This narrows the fault but does not identify a root cause or prove that
the handset is offline. No route, proxy, permission, shared key or shared ADB server
was changed; temporary comparison servers were removed. Raw evidence is private.

The owner subsequently supplied a replacement mesh endpoint. The same project-owned
ADB server connected, reported `device`, and executed a read-only Android 13 shell.
The earlier transport blocker is resolved for the new endpoint; its cause on the
previous path remains undiagnosed. No previous target or shared server was changed.

The same-source CI QA APK was installed separately from the stable-signed preview.
Native login to the matching, empty isolated Core and explicit certificate trust
succeeded. With that Core suspended for 80 seconds, the app detected lost contact;
after resumption it returned online without clicks, process restart or changed
encrypted session state. Restarting only the isolated Core then invalidated its
in-memory sessions. The app returned online automatically in the same process and
persisted changed encrypted state without a new login interaction. This verifies
remembered-login renewal after server-side session loss, not waiting twelve hours
for natural idle expiry or carrier recovery. Reader sharing remained off throughout.

Private evidence: the existing task-private `core-hil.njDetI` record contains
`silence-result.json`, `restart-result.json` and the corresponding native screenshots.
Both tests used the existing CI artifacts, not a local build or a modified Core.
The temporary Core, SSH tunnel, reverse mapping and remote files were removed;
listeners were checked absent. Production and the stable preview's settings were
not changed. No SIM operation, paid call or SMS occurred.

Next action: complete the authorized minimal mainline integration via PR #11,
including the PR #7 baseline and this client adaptation in one squash delivery.
Preserve the excluded recovery branch and its dirty tree. Do not deploy production.
Malformed-frame regression and natural idle-expiry coverage remain unverified;
the private unexecuted regression candidates remain excluded. Real card/carrier
operations, background/OEM endurance and battery qualification remain separate.
