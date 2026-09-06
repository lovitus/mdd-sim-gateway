#!/bin/sh
# Source-checkout convenience entrypoint. Production releases carry the same
# installer beside the immutable mdd-<revision> directory.
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
exec "$root/go-runtime/release/install-release.sh" "$@"
