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
The corrected candidate still requires a full green run. No APK installation,
paid operation or deployment is claimed.

Next action: run the frozen candidate through GitHub, inspect failures without
expanding scope, then perform authorized handset checks. Keep the missing
regression evidence explicit. Preserve original branch evidence; do not claim
this candidate has inherited its acceptance.
