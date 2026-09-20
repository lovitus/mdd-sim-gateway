# Opt-in browser-local call recording

Recording is available in the global controls of an **active** VoWiFi or cellular browser call.
It is owned by the existing Go call coordinator; changing pages does not create another call or recorder.
The button is absent when the browser lacks MediaRecorder. No recording starts automatically.

## Consent, content and lifetime

Every recording requires a fresh explicit in-page confirmation that the required participant consent has
been obtained. MDD cannot determine consent for remote participants; it does not silently add a recording
announcement or send a carrier operation. Do not record without the necessary consent.
The local microphone is the left channel and received playback is the right channel. Local mute also
mutes the recording's microphone channel. This captures browser audio, not inaccessible upstream speech
or an independent server-side call archive. Both channels are recorded only from the time of consent.

Stop recording finalizes the codec output; **Save recording** is an explicit local browser download.
The recording stops at call teardown, media failure/disconnection, or the 30-minute duration bound.
Reconnecting media never silently starts another recording. Saving after call end remains possible while
this login/page lives. Only one recording is retained; save/discard it before starting another.
A canceled consent dialog, a call replaced while the dialog is open, or a logged-out/closed call cannot start
capture. Discard releases buffered audio and pending object URLs, not the live microphone/call resources.

Audio remains in this browser's memory. It is not sent to Core, uploaded, placed in localStorage/IndexedDB,
or attached to call history. Unsaved audio is discarded on logout, page close or unmount; it is not crash-
recoverable. Downloaded files are managed by the user/OS and may be unencrypted—delete or protect them as
needed. Filenames contain a timestamp, not numbers, SIM identities or credentials.

The application retains at most **32 MiB** of encoded chunks and stops at **30 minutes**. These are limits
on application-retained output and wall time, not a claim to control a browser encoder's internal buffers
or background-tab scheduling. Oversize/error/empty/finalization-timeout recordings are discarded rather
than presented as a playable, arbitrarily truncated codec file. A recording failure does not hang up a call.
The native encoder receives a 1-second timeslice; finalization has a bounded 3-second timeout.

## Validation

`npm run test:recording` uses controlled encoder/graph/timer fixtures for consent, mute, stereo routing,
last-chunk finalization, limits, timeouts, discard, stale callbacks, explicit saving and exact-edge cleanup.
`npm run test:recording-browser` uses a real Chromium/Web Audio/MediaRecorder with loopback synthetic tones:
decode both channels, verify mute, duration bound and call-close finalization. It requires Chrome/Chromium
(or `MDD_TEST_CHROME`) and performs no microphone, modem, paid call or external network operation.
The normal Linux Core CI runs it. This is not hardware/carrier acceptance or qualification of every browser.
