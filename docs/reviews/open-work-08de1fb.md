# Issue #3 implementation follow-up (baseline 08de1fb3)

Scope is the owner's current request to fix open issues. The repository has one open umbrella issue (#3),
not a newly reopened Android, soft-eSIM-delete or notarization task. Keep the September 20 owner decisions.
No production host, live SIM, paid operation, notification replay or user configuration is touched.

## Implemented code changes

1. Browser-local opt-in stereo recording through the existing call owner. Exact call is rechecked after
   consent, mute is reflected in the local channel, media disconnect/end stops recording, no automatic
   resumption occurs, final encoder data is retained before explicit download, and bounded/discard behavior
   is tested. See docs/features/local-recording.md. This is implementation plus automated validation,
   not new real-line/participant acceptance.
2. Windows cutover waits capture only the SCM-owned process handle before stop, not every mdd-agent by
   name. Process exit uses the native wait; SCM state uses bounded ServiceController.WaitForStatus (which
   itself still polls inside .NET). Failures dispose handles. Rollback refuses to alter/restart the service
   while candidate process ownership is unconfirmed. Running-image hash is read from the exact service PID.
3. macOS cutover uses the exact per-user launchd label, not global process-name matching/termination.
   Readiness is checked immediately and requires running state. Backoff is capped by the absolute deadline;
   failure rollback first confirms candidate exit. No new watchdog or process-wide periodic restart is added.
4. Topology validation gains a fixed-schema field path for ambiguous reader identity/ATR/detail failures
   and modem data readback failures, propagated through the authenticated local API/client. Stable rule
   messages remain unchanged; no raw values are included and authorization remains required. Other rules
   retain the honest broad `topology` field rather than inventing a more precise diagnosis.

## Historical evidence reconciliation and remaining limits

The eight historical records were read at their indexed line ranges. Their scope supports these **reported**
results: Linux exact bearer loss/recovery without a host main-table route; selected browser pages and state
readback; Windows/macOS metadata and macOS per-user restart; on-host/cross-host Linux WSS relay; earlier
outgoing/bidirectional audio plus incoming-event UI; macOS non-paid audio/readback and arm64 signing.
They do not by themselves supply raw capture/binary provenance for this reviewer, a current deployment of
this patch, or the missing incoming answer, in-call DTMF, mute, multi-client contention, active-call link
loss, cross-vendor/device/reboot and full isolation/tampering scenarios. Preserve the reports, do not reset
past acceptance, and request only the particular uncovered scenario after reconciling its existing evidence.

The connected desktop tool reports no online device in this session. The source archive is sufficient for
implementation, not for observing that workstation or modem. No field acceptance is invented. The original
MM forced-close and topology_invalid causes remain unresolved; new diagnostics are not retrospective proof.
SIM continuity already incorporates authoritative absence and native event epochs; an indistinguishable
unplug/reinsert without any native event cannot be inferred from identical samples. The relevant between-
samples HIL case remains open, not 'fixed' by changing the user's switches or aggressively polling hardware.

## Deliberately not claimed complete

The Windows least-privilege companion/service redesign and fully event-driven SCM/macOS startup waiting
remain separate architecture work. The scoped fixes above remove concrete process-selection/rollback and
coarse-delay defects but do not implement that entire redesign. Universal scope is still a future decision;
Android deferred; notarization/.p8 excluded; physical eSIM delete/retained notifications/manual replay final.
The standing maintained-upstream/large-file work is not a finite bug to close by an arbitrary rewrite.

## Test and delivery record

Recording units, shell timing/ownership checks and syntax checks run locally. Local Chromium navigation is
blocked by administrator policy; that failed attempt is not a browser pass and the policy is not changed.
Real-browser validation belongs in the unmodified-policy CI runner. Go 1.26/native and platform execution
belongs in GitHub CI for this sandbox. Exact source identity, CI outcomes and any failures will be reported
in the PR/issue and retained evidence, not predeclared here.
