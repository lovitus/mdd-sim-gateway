# PR8 recovery review repairs

Baseline: `ffb469798c047ba0bc2b7aa41b30348030b32801`.
This is a repair of the existing PR8 implementation, not another Android client.
No automatic merge, deployment, paid action or live SIM operation is authorized by these tests.
Owner-deferred landscape and unqualified carrier/power cases stay as documented.

| Review | Change | Specific regression |
|---|---|---|
| F1: lost accepted-dialog cleanup | Keep a concrete MediaCall after unconfirmed BYE; retain the final SIP dialog on post-2xx ACK/SDP failure; normalize typed nil before interface conversion; never retry start with a retained owner | `TestPR8AcceptedMediaFailureRetainsExactCleanupAcrossAllLayers`, `TestPR8MediaFailureWithRejectedByeReturnsCleanupHandle`, `TestPR8AcceptedAckOrSDPFailureKeepsDialogForBye`, `TestPR8CleanupHandleSurvivesClassificationAndPreventsStartReplay` |
| F2: definitive rejected start stays unknown | Only a received final non-2xx response creates `confirmed_rejected` evidence. Store under original call/start/session; Core keeps capability/card/generation fences and durable rejected outcome; Android distinguishes rejection from hangup | `TestPR8RejectedCallHasDurableOutcomeWithoutAnotherInvite`, `TestPR8RecoveryReadsRejectedOutcomeAndKeepsCapabilityFence`, `TestPR8RejectionEvidenceDoesNotIncludeTransportOrLocalErrors` |
| F3: Stop drops terminal receipt | Require Accepted, save exact terminal before retiring owner or closing runtime, retain timestamp/source on write retry, skip another BYE once confirmation is known | `TestPR8StopPersistsCallTerminalEvenWhenRuntimeCloseFails`, `TestPR8StopRetainsReceiptRetryWithoutRepeatingConfirmedBye`, `TestPR8StopRequiresAcceptedTerminalResult` |
| F4: stale reader publication | Hardware I/O stays on reader executor; one main-looper owner serializes share/invalidation/immutable observation publication; revalidate hub+epoch at commit | `Pr8ReaderPublicationTest`, `Pr8ReadRecoveryTest` |
| F5: TLS loss permanently stops catch-up | Share cert/peer rejection classification with Link. Ordinary SSL transport failure uses bounded read-only retries, not SMS dispatch | `SmsFlowTest.pr8TlsReadLossRecoversAfterObserverReconnectWithoutSending`, `Pr8ReadRecoveryTest`; existing auth-stop tests remain |
| F6: unreadable bootstrap lacks an exit | Expose Retry and a separate two-confirmation reset on storage fault and Settings. Before explicit reset preserve raw encrypted files in a checked private archive and retain the key; reject known/readable pending calls. Reset never proves remote termination | `Pr8StorageRepairTest` |

The added discovery/cleanup bugs include post-acceptance ACK/SDP failure, nil-pointer/interface conversion, and Stop treating Accepted=false as successful. Terminal persistence-only retry remains with the original bounded call guard, not a new watchdog.

## Validation boundaries

New tests are source at commit time; execution results belong to the exact CI head/artifacts and PR report.
The Go negative-control script copies only baseline-compatible tests to the pinned baseline and requires the expected assertion failure, rejecting compilation errors, panics and races as proof. Positive tests exercise the actual Backend, runtime wrapper, IMS dialog/media stack and a synthetic carrier. One test traverses the actual userspace UDP SIP fixture. No production host network routes are installed.

Android regressions include real Service->Link publication, controlled TLS failure during actual message catch-up, native Keystore/file failure shapes, and clicks through the two confirmation dialogs. These are QA/emulator operations, not physical card or paid-call tests. Existing external process-death and control suites remain enabled.

## Compatibility and limitations

`confirmed_rejected` extends the draft v1 receipt source enum. Core and Provider from this repair must be upgraded together; older strict validators fail closed rather than inventing terminal proof. Existing terminal receipts remain readable. Only exact observed final responses are terminal; timeout, missing state, media closure or arbitrary HTTP errors are not proof.

Explicit reset is last-resort user intent, never an exception handler. It disconnects local readers/observer and cancels old callbacks, archives ciphertext privately without exporting it, and replaces current configuration only after archival verification. Disk/Keystore failure can still prevent reset; retry then reports preservation, not success. A missing/inaccessible old key cannot magically decrypt archived data. No uninstall instruction or automatic destructive fallback is introduced. There is no remote-end action in reset.

The previously documented certificate-rotation limitation for a pending call remains: do not silently release an old recovery capability to a replacement pin. Supporting that transition requires separately verified trust continuity. Earlier field evidence is retained within its original scope; this patch claims no new hardware acceptance.
