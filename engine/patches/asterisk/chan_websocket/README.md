# Asterisk 20.7 media-WebSocket backport

This directory contains a GPL-2.0-only aggregate patch for the pinned sysmocom
`20.7.0` Asterisk fork. It is applied after the existing AMR cherry-pick and MDD
IMS/SMS/admission patches. It does not replace, modify, or disable AudioSocket.

Target source:

- sysmocom Asterisk: `d231cb2c658545773fcd5eb9de787219b9ef6566`
- existing AMR cherry-pick: `f1b60dcd9568c4045512fd0d8b619b9fb91a7f35`
- aggregate patch SHA-256:
  `376de91aff682a31c3f865476415419d947364b200e9c21b20f1ffb58bf884aa`

The aggregate contains these upstream changes:

- `a6dca5bf3` — WebSocket client Basic-auth prerequisites
- `04a3e854d`, `63656c1f8` — reusable `res_websocket_client`
- `df33c52a1`, `e0552b1c8`, `989b9a24e`, `5d1d5dbfe`, `1ec000631`,
  `4541b0aea`, `3feee6dfb`, `8c31864b5`, `6212c0b37`, `0f3e9fa56`,
  `5231c7386`, `bbff0a9c7`, `1514409cd`, `e6f5a091b`, `aa1d725d7`,
  `57b601e270`, `00d4d47be`, `4bb3ecb7f`, `68170c14c`, `0a0542533`
  — `chan_websocket` and its fixes through the current stable 20-series code
- `0330d3734` — release a failed WebSocket session reference
- `f14ea0897` — reject a null requestor without dereferencing it
- `9173b0f52` — bounded client-handshake timeout; only its three-file timeout
  behavior is adapted because the upstream commit also assumes the later proxy
  and keepalive subsystem

20.7 compatibility changes are deliberately narrow:

- add the direct WebSocket channel technology without changing AudioSocket
  sources or its existing ExternalMedia behavior;
- keep the WebSocket close code in `websocket_pvt` because 20.7 predates the
  generic technology-hangup-cause accessors;
- add only the result/status string helpers required by the backported driver;
- bound HTTP Upgrade response reads without importing the later proxy stack.
- store 20.7's existing `ast_websocket_client_options.timeout` beside the
  private client state so the HTTP Upgrade timer uses the same configured bound
  as newer branches;
- reject the complete configured URI plus per-call parameters above 160 bytes
  before the vulnerable stack allocation.
- pass URI parameters as call-local input to `connect()` instead of mutating the
  shared sorcery configuration object used by concurrent calls.

The product uses the channel technology directly through AMI Originate. ARI
ExternalMedia and outbound Stasis are not used, so their resource, schema, and
Stasis prerequisite hunks are deliberately excluded from the aggregate patch.

Not included:

- the post-20.20 leftover-buffer change (`26551a4dc`), because MDD sends only
  exact 320-byte slin frames and never enables bulk media buffering;
- the 20.21 proxy/keepalive subsystem (`d8df20731`) and its dependent PONG fix;
  the gateway uses direct Engine-to-Control WSS, server PING/PONG, fresh PCM
  evidence, and the independent 10-second Asterisk call timeout;
- a full sysmocom rebase onto Asterisk 20.20, which is a separate foundation
  migration and not part of the browser-media feature.

Every release build must verify the patch with `git apply --check` before
application, build both `chan_audiosocket.so` and `chan_websocket.so`, and run
the isolated no-charge WebSocket Echo/Redirect gates. Runtime product behavior
uses only `chan_websocket`; AudioSocket remains an upstream compatibility module,
not a selectable MDD media backend.
