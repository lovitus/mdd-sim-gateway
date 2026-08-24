# Forward-recovery baseline

This branch separates recovery evidence from later bug fixes. Its first commit
reconstructs the source that accompanied the production runtime immediately
before the failed rollback attempt; it is not a feature or cleanup commit.

## Provenance

- Git parent: `49ac6e57dcc022d4f037f7b0d6bd037eab409479`.
- Preserved source archive SHA-256:
  `ca7d48e21e74844249c9c350cd2578520e6785dde7adeb48b437916f1cc8e370`.
- Preserved image archive SHA-256:
  `b4980ae29ab9edf1db939cc31dae3748df08d07e6b34e7e1a0b83ba7cd2dc97d`.
- Control runtime image:
  `sha256:36229f7fd4fa7059435c06d8bd36234509ac8d951d09a5c6c634ad48460f371c`.
- `control/` is reconstructed from that running image. Its 45 non-venv files
  match the preserved image manifest; `control/app/main.py` is
  `a2f046eb6ed0fc379769ab4738c06d45cdc503793537679e3884e62bb5269de5`.
- Other source and tests are reconstructed from the preserved source archive.

Runtime data, container images, virtual environments, dependency caches,
generated Agent binaries, and unrelated temporary artifacts are excluded.
Recovered macOS Agent source is evidence only: the new full macOS Agent remains
deferred and this baseline does not authorize its build or deployment.

Subsequent fixes must be separate commits with their own tests and review. A
dirty worktree or failed rollback artifact must never be deployed as this
baseline.

## Forward commit ledger

- `b68243f`: preserve an Engine generation for non-destructive exit actions.
- `00c3c2f`: close reviewed failover-plan, active-call evidence and lifecycle
  race gaps.
- `e19582c`: fence physical-card-loss side effects before either Uvicorn
  listener closes its VPCD WebSockets, and coordinate both listeners on one
  process signal.
- `519abad`: add a two-second Uvicorn connection-drain bound and shutdown phase
  evidence. This commit alone was rejected because it printed from the Python
  signal handler.
- `b77623e`: remove that signal-context I/O. This is the reviewed forward
  candidate and current production Control source revision.

Each item is intentionally a separate commit. Failed deployment evidence is
not squashed into the source baseline, so `git log` continues to distinguish
reconstruction, functional repair, a rejected implementation, and its repair.

## Controlled deployment record

The first `e19582c` deployment preserved the running line 1 and line 7 Engine
container generations, but an ordinary Control recreate exposed a second
defect: Uvicorn 0.52.0 waited indefinitely for live WebSocket tasks and Docker
sent SIGKILL after its ten-second grace period. The Engine generations still
remained unchanged because the new shutdown fence worked. That observation is
the reason for the separate `519abad`/`b77623e` follow-up rather than an amend
or an unrelated refactor.

`b77623e` was independently reviewed and exercised with real Uvicorn 0.52.0:
HTTP-only and dual-listener processes with intentionally stuck WebSockets
entered lifespan cleanup and exited in about 2.5 seconds. An actual empty-data
FastAPI lifespan with all ten background-task owners completed every logged
idle cleanup phase in about 2.2 seconds.

Production now runs a Control image labelled with full source revision
`b77623e949aacea44dfcddeea34f3fd855966381`. It was switched from the known
hanging `e19582c` Control only after all running Engines reported exactly zero
active channels/calls and the durable paid-work queries were all zero. The two
pre-existing Engine generations and restart counts were preserved. The TLS
certificate pin and Engine image were not changed. No real call, SMS, APDU or
AT operation was used as a deployment test.

## Evidence and open gates

- Focused lifecycle/status/failover tests: 234 passed, 35 subtests passed.
- Full-suite candidate body: 36 failed, 1287 passed, 70 subtests passed. The 36
  failed node IDs exactly match the recovered pre-change candidate; the suite
  also retains a pre-existing post-summary thread hang and is bounded by an
  external timeout.
- Raw deployment logs, container inspection, run-marker snapshots and hashes
  stay in the private recovery area on the production host. They must not be
  copied into this worktree.
- A dedicated production graceful-recreate proof for `b77623e` remains open.
  It must preserve the outgoing container log before Compose destroys it and
  prove exit code zero within ten seconds while every pre-existing Engine ID
  and restart count remains unchanged.
- Line 1 developed a local PC/SC interruption followed by IMS authentication
  rejection when VPCD reconnected. Its durable recovery marker correctly
  records `pcsc_service_unavailable`, but the bounded recovery has not restored
  registration. This is a separate diagnosis; do not hide it by rebuilding the
  Engine or fold it into the Uvicorn timeout repair.
- Old research and failed-rollback worktrees remain read-only and unarchived
  until the two gates above are closed. Archive them only with per-worktree
  base/purpose/status manifests and patch or tar evidence; never delete them
  merely to make the active worktree list look clean.
