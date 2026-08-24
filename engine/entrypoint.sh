#!/bin/bash
# Immutable outer Engine entrypoint. It becomes the admission supervisor before any PIN, SWu,
# P-CSCF, AMI or Asterisk child exists; engine-runtime.sh owns all subsequent initialization.
set -u

# Generate exactly one canonical lowercase UUID for this Engine process incarnation. Python is a
# required image dependency, so there is no incompatible timestamp/PID fallback.
MDD_ENGINE_RUN_ID="$(python3 -c 'import uuid; print(uuid.uuid4())')" || exit 1
export MDD_ENGINE_RUN_ID

# No environment marker can select a child path: this file is always the outer supervisor.
exec python3 -u /usr/local/bin/admission_gate.py \
  --iid "${MDD_ID:-}" --engine-run-id "$MDD_ENGINE_RUN_ID" \
  supervise -- /bin/bash /engine-runtime.sh
