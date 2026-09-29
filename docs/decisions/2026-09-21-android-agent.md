# Android native agent — 2026-09-21

Authority: the project owner's explicit request to reimplement Android from scratch and deliver a draft PR and tested APK.
This supersedes only the earlier Android deferral. Physical eSIM deletion, desktop Agent isolation, excluded notarization and all safety/acceptance boundaries remain unchanged.

## ANDROID_NATIVE

Android is now a current workstream: friendly setup, attached eSIM reader registration/keepalive, server-routed outgoing/incoming calls and SMS, long sessions, cellular-network recovery and battery-conscious operation. A phone-installed subscription may use the system dialer; an external USB reader is not a cellular radio. No carrier/security restrictions may be bypassed to claim direct calling.

The requested delivery is a **draft PR and preview APK**, not permission to merge, deploy production, place paid calls/SMS, delete profiles or claim physical acceptance. Use synthetic/emulator tests and explicitly report unsupported readers, restricted OMAPI access and Android background limits. Test signing is distinct from the owner's stable production signing identity.

## ANDROID_NETWORK_EXTREMES - September 27 owner clarification

Extreme network-switching validation is non-blocking and must not delay the main
reader, call, message or client-delivery workflows. Keep the unperformed physical
Wi-Fi/cellular handover case explicitly unverified; do not require a phone-native
data SIM or another owner decision before continuing the main workflow.
Ordinary automatic reconnection and session recovery remain required. This changes
the acceptance priority, not user switches, recovery safeguards or test results.

## ANDROID_PREVIEW_CLOSE - September 27 owner delivery decision

This PR's primary target is the Android Agent preview, not completion of every
MDD workstream. The owner permits a staged close when the main workflow is usable.
Existing reader sharing, call/SMS, page feedback and ordinary recovery evidence
support that milestone. PR #12 remains a draft for owner review; staged closure
does not authorize merging it or the separate Provider/desktop PRs.

Long-session and recovery mechanisms are implemented, but overnight USB/OEM sleep,
battery life and extreme physical network switching are not fully qualified.
The owner accepts those validation gaps for this stage because failures remain
visible, bounded automatic recovery exists, and the user can inspect status and
follow manual recovery advice. Keep the historical USB write-timeout cause
unresolved; do not relabel missing validation as passed or guarantee availability.
Ordinary reconnect, exact ownership, user Pause/share intent, certificate checks
and no automatic paid retry remain required.

Record these gaps and meaningful non-Android directions in the existing generated
postponed-tasks.md through docs/status/acceptance.json. Desktop enabled-4G isolation
evidence gaps, installer privilege/wait work, identical reinsert qualification
and historical ModemManager/topology root causes are separate follow-up work,
not Android PR gates. Their product requirements and existing safety protections
remain in force; deferral neither permits traffic leakage nor overrides switches.
Resume them only under their own later scope or a newly reproduced main-flow
failure. No additional fault injection, paid test, build or deployment is needed
just to prolong this stage. The next action is owner review of the Android
candidate and its separately tracked Provider dependency.

## GitHub review record delivery

The owner's subsequent September 27 instruction requires all non-sensitive work
and records for this Android delivery in GitHub, including the execution record,
so the review team can use the GitHub plugin. Publish the documentation together
on the existing PR rather than leaving post-CI decisions only in the local tree.
This supersedes earlier local-only handling of that record. Preserve the unrelated
recovery branch and its dirty work. Credentials, private host/card/phone details and
raw logs/captures remain private; publish redacted results, exact CI/source/artifact
references, failure history and qualification limits instead. Documentation
publication does not authorize a merge, runtime change or repeated paid test.
