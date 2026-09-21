# Android native agent — 2026-09-21

Authority: the project owner's explicit request to reimplement Android from scratch and deliver a draft PR and tested APK.
This supersedes only the earlier Android deferral. Physical eSIM deletion, desktop Agent isolation, excluded notarization and all safety/acceptance boundaries remain unchanged.

## ANDROID_NATIVE

Android is now a current workstream: friendly setup, attached eSIM reader registration/keepalive, server-routed outgoing/incoming calls and SMS, long sessions, cellular-network recovery and battery-conscious operation. A phone-installed subscription may use the system dialer; an external USB reader is not a cellular radio. No carrier/security restrictions may be bypassed to claim direct calling.

The requested delivery is a **draft PR and preview APK**, not permission to merge, deploy production, place paid calls/SMS, delete profiles or claim physical acceptance. Use synthetic/emulator tests and explicitly report unsupported readers, restricted OMAPI access and Android background limits. Test signing is distinct from the owner's stable production signing identity.
