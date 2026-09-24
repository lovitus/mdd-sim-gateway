# Android Preview Follow-up — 2026-09-24

## Source, CI and Artifact

- Source commit: `a9b952dd02b4e716ea45f29c0adfa53259129542`.
- Source tree: `246db96c9ec9d4112e7521047b4041a679619f92`.
- GitHub Actions run `35944321813` completed successfully for that exact commit.
- Android API 28 and API 35 instrumentation reports each show 49 scheduled cases, 0 failures, 0 errors and 1 skipped case. The skipped case is `PresentationTest.landscapeKeyboardAndActiveCallKeepThePrimaryActionReachable`, deferred under the owner's landscape-scope decision; the other 48 cases passed on each API.
- The process-recovery evidence reports pass for cellular and VoWiFi modes on API 28 and API 35 (three scenarios per mode/API, no cleanup failures). These use a synthetic peer and process death; they are not carrier, radio-handover or battery acceptance.
- Preview APK version code `49`; SHA-256 `e6d852aa9e4252665ff1af3e93b244b616e935ac156afc38971b690510d963a0`.
- APK signature verification succeeded with one v2 signer. Signer certificate SHA-256: `8b5e818fccfa6f6738cd53658b2df9ab57a014f9a5269decc4104d91894dd5e2`.

## Authorized Device Check

- Installed the signed preview in place on the authorized API 35 handset, upgrading version code `47` to `49`. The installed signer matched; no uninstall or app-data clear was performed. Device serial is omitted.
- Opened Home, Calls, Messages, Readers, Settings and the Diagnostics view in the native app.
- Home's local Android USB/ReaderHub assessment showed one attached reader and no local Reader action pending. This is not a Core topology acknowledgement. Separately, the Core mobile directory returned HTTP 404, so no lines were available.
- Calls and Messages both displayed the missing Core mobile endpoint and an empty line directory. No call or SMS was initiated.
- Readers showed one attached USB CCID reader. The local reader/SIM state was not treated as proof that Core had received a current snapshot.
- Settings and Diagnostics reported Android API 35, preview version 49, Reader Agent connected, mobile Gateway unavailable, reader sharing intent enabled and one locally reported reader. The reader count comes from the app's local service snapshot; Agent-link connectivity is transport status. Neither is a Core topology readback. No settings or sharing switches were changed.
- No PIN/APDU/profile operation, download, paid call/SMS or network interruption was performed.

## Limits

- This confirms the current-head build, the recorded emulator/process-recovery suites and a normal-scale native page check only. The production Core still lacks the mobile endpoint observed by the app; Calls/Messages remain unusable against that production route until Core is updated and separately accepted.
- Wi-Fi loss, cellular handover, automatic reconnect on physical radio changes, background/OEM behavior, OMAPI hardware access and battery consumption remain unverified on a real handset. The synthetic recovery tests do not close those gaps.
- No production deployment, PR merge, carrier operation or eSIM mutation occurred.

This report supplements, and does not replace, the scoped 2026-09-23 device evidence.
