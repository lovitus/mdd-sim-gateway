# Android Preview Device Check — 2026-09-23

## Artifact and Environment

- Tested source: `630502f8813d4df6e65c6ca593f2b1d2b4469634`.
- GitHub Actions run: `35853880056` passed for that exact source revision.
- APK: `mdd-agent-preview.apk`, version `0.1.0-preview`, version code `47`.
- APK SHA-256: `cd000a43a2294a6bc2b54fb989775de760b49ddff815ded3d7591daa40dc5af8`.
- Signer SHA-256: `8b5e818fccfa6f6738cd53658b2df9ab57a014f9a5269decc4104d91894dd5e2`.
- Device: Mi MIX 2S, Android API 35, portrait. The serial is intentionally omitted.
- Installed in place over version code `36` with `adb install -r`; installed and candidate signer digests matched. No uninstall or app-data clear was used.

## Observed Behavior

- Opening the updated app restored its foreground Agent service and retained the existing gateway configuration and reader-sharing intent.
- Home, Calls, Messages, Readers and Settings were opened on the handset. Home showed the native mobile endpoint returning HTTP 404, retrying, and zero available lines. Calls and Messages showed the same endpoint blocker and no lines. No call or SMS was started.
- Diagnostics showed the mobile gateway connection offline while the Reader Agent connection was online, with one attached USB CCID reader. This distinguishes the two links; they do not share one online flag.
- Stopping reader sharing while the reader remained attached displayed the Readers badge `1` and “need action: 1”. Confirming sharing restored the Reader Agent connection, cleared the badge and returned “need action” to `0`. The final sharing intent is enabled.
- No PIN/APDU operation, profile change, download, paid call or paid SMS occurred.

## Limits

- The production Core route used by the mobile observer is still missing in the recorded production release; the handset receives HTTP 404 for `/v1/mobile/ws`. The current repository contains the corresponding Core route, but this draft PR has not been deployed. This explains the empty Calls/Messages directory on this handset; it does not establish that a transient radio loss was injected or recovered on hardware.
- A real Wi-Fi/network interruption was not injected because this handset's ADB control path uses that same Wi-Fi connection and there is no out-of-band recovery path. The hardware default-network callback and cellular handover therefore remain unverified. Controlled transport/retry tests are separate CI evidence and are not presented as a radio-handover test.
- USB sharing and the attention badge were exercised, but SIM authentication, OMAPI, screen-off recovery and battery consumption were not tested here.

This report applies only to the artifact and device check above. It is not production deployment or carrier-operation acceptance.
