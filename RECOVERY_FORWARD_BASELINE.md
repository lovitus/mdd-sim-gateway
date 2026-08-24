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
