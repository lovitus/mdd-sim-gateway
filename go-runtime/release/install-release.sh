#!/bin/sh
# Install and operate an immutable MDD Go release on a Linux systemd host.
# This entrypoint never builds source and never invokes Docker or Python.
set -eu

PROGRAM=${0##*/}
CORE_CONFIG=/etc/mdd/core.json
AUTH_FILE=/etc/mdd/auth.json
TLS_CERT=/etc/mdd/tls/server.crt
TLS_KEY=/etc/mdd/tls/server.key
UNITS="mdd-core.service mdd-provider-apply.service mdd-egress.service"

say() { printf '%s\n' "$*"; }
fail() { printf '%s: %s\n' "$PROGRAM" "$*" >&2; exit 1; }

usage() {
  cat <<EOF
Usage:
  $PROGRAM install /absolute/path/to/mdd-RELEASE
  $PROGRAM start
  $PROGRAM restart
  $PROGRAM status
  $PROGRAM stop
  $PROGRAM uninstall

install verifies and atomically installs an immutable Go release. On a fresh
host it also creates the initial administrator, TLS identity and Core config;
provide the administrator password on stdin (or enter it at the TTY prompt).
Installing over a complete existing configuration never starts or restarts a
service. start enables only Core, provider apply and country-egress services.
It also starts already-enabled, strictly identified Provider instances after
Core is ready. restart is the only command here that deliberately restarts the
three fixed services. stop quiesces the server but leaves enablement and the
independent endpoint Agent alone. uninstall stops and disables all packaged
units and removes only verified Go software; configuration, state, audit data
and the mdd account are preserved.

The packaged mdd-agent.service is an endpoint service and is never enabled on
the server automatically. Configure /var/lib/mdd-agent/config.json on a Linux
device before enabling it explicitly.

Legacy Python/Docker deployment is not reachable through this script.
EOF
}

require_host() {
  [ "$(id -u)" -eq 0 ] || fail "this command requires root"
  [ "$(uname -s)" = Linux ] || fail "only Linux is supported"
  [ -x /bin/systemctl ] || fail "/bin/systemctl is required"
  [ -d /run/systemd/system ] || fail "the host is not running systemd"
}

canonical_release() {
  [ $# -eq 1 ] || fail "install requires exactly one release directory"
  case "$1" in
    /*) ;;
    *) fail "release directory must be absolute" ;;
  esac
  command -v realpath >/dev/null 2>&1 || fail "realpath is required"
  resolved=$(realpath -e -- "$1") || fail "release directory does not exist"
  [ "$resolved" = "$1" ] || fail "release directory must be canonical (no symlink, dot segment, or trailing slash)"
  [ -d "$resolved" ] && [ ! -L "$resolved" ] || fail "release path must be a real directory"
  [ -f "$resolved/mdd-core" ] && [ -x "$resolved/mdd-core" ] && [ ! -L "$resolved/mdd-core" ] ||
    fail "release has no regular executable mdd-core"
  printf '%s\n' "$resolved"
}

bootstrap_state() {
  present=0
  for path in "$CORE_CONFIG" "$AUTH_FILE" "$TLS_CERT" "$TLS_KEY"; do
    if [ -e "$path" ] || [ -L "$path" ]; then
      [ -f "$path" ] && [ ! -L "$path" ] ||
        fail "host configuration is not a regular file: $path"
      present=$((present + 1))
    fi
  done
  case "$present" in
    0) printf '%s\n' fresh ;;
    4) printf '%s\n' existing ;;
    *) fail "partial host configuration found under /etc/mdd; refusing to overwrite or guess" ;;
  esac
}

path_present() {
  [ -e "$1" ] || [ -L "$1" ]
}

directory_has_entries_or_is_invalid() {
  directory=$1
  path_present "$directory" || return 1
  [ -d "$directory" ] && [ ! -L "$directory" ] || return 0
  for entry in "$directory"/* "$directory"/.[!.]* "$directory"/..?*; do
    path_present "$entry" && return 0
  done
  return 1
}

reject_orphaned_durable_state() {
  for path in /var/lib/mdd/events.db /var/lib/mdd/messages.db \
      /var/lib/mdd/messages.db.cellular-operations /var/lib/mdd/calls.db \
      /var/lib/mdd/catalog.db /var/lib/mdd/egress.db \
      /var/lib/mdd-egress-config/desired.json /etc/mdd/providers-current; do
    if path_present "$path"; then
      fail "fresh configuration is absent but durable state exists at $path; recover its matching configuration or explicitly archive/clean that state before retrying"
    fi
  done
  for directory in /var/lib/mdd/providers /etc/mdd/providers/releases; do
    if directory_has_entries_or_is_invalid "$directory"; then
      fail "fresh configuration is absent but durable state exists at $directory; recover its matching configuration or explicitly archive/clean that state before retrying"
    fi
  done
}

bootstrap_interactive() {
  candidate=$1
  saved_tty=$(stty -g) || fail "cannot read terminal settings"
  trap 'stty "$saved_tty" 2>/dev/null || true' EXIT HUP INT TERM
  printf 'Initial MDD administrator password: ' >&2
  stty -echo
  IFS= read -r administrator_password || {
    stty "$saved_tty"
    printf '\n' >&2
    fail "administrator password was not read"
  }
  stty "$saved_tty"
  trap - EXIT HUP INT TERM
  printf '\n' >&2
  [ -n "$administrator_password" ] || fail "administrator password must not be empty"
  printf '%s\n' "$administrator_password" | "$candidate" bootstrap-host
  administrator_password=
}

install_release() {
  require_host
  release=$(canonical_release "$@")
  state=$(bootstrap_state)
  [ "$state" != fresh ] || reject_orphaned_durable_state
  candidate=$release/mdd-core

  # The candidate validates its complete strict manifest before publishing the
  # immutable release. It only daemon-reloads systemd; it never starts services.
  "$candidate" install-release -source "$release"

  if [ "$state" = fresh ]; then
    if [ -t 0 ]; then
      bootstrap_interactive "$candidate"
    else
      "$candidate" bootstrap-host
    fi
    say "fresh host configuration created; run '$PROGRAM start' when ready"
  else
    say "release installed; existing configuration and running service PIDs were left unchanged"
  fi
}

require_configuration() {
  for path in "$CORE_CONFIG" "$AUTH_FILE" "$TLS_CERT" "$TLS_KEY"; do
    [ -f "$path" ] && [ ! -L "$path" ] || fail "host configuration is incomplete: $path"
  done
}

valid_provider_unit() {
  unit=$1
  case "$unit" in
    mdd-vowifi@line-*.service)
      identity=${unit#mdd-vowifi@line-}
      identity=${identity%.service}
      [ "${#identity}" -eq 32 ] || return 1
      case "$identity" in ''|*[!0-9a-f]*) return 1 ;; esac
      ;;
    *) return 1 ;;
  esac
}

append_provider_unit() {
  unit=$1
  valid_provider_unit "$unit" || fail "refusing unexpected Provider unit name: $unit"
  case " $MDD_PROVIDER_UNITS " in
    *" $unit "*) ;;
    *) MDD_PROVIDER_UNITS="${MDD_PROVIDER_UNITS}${MDD_PROVIDER_UNITS:+ }$unit" ;;
  esac
}

# Inventory is the union of loaded units, installed unit files and the current
# strict provider-config names. A disabled-but-loaded or enabled-but-inactive
# instance must not escape stop/uninstall merely because one systemd view omits it.
provider_units() {
  MDD_PROVIDER_UNITS=
  MDD_UNIT_LOADED=$(mktemp /run/mdd-provider-loaded.XXXXXX)
  MDD_UNIT_FILES=$(mktemp /run/mdd-provider-files.XXXXXX)
  trap 'rm -f "$MDD_UNIT_LOADED" "$MDD_UNIT_FILES"' EXIT HUP INT TERM
  /bin/systemctl list-units --all --full --plain --no-legend 'mdd-vowifi@*.service' >"$MDD_UNIT_LOADED"
  /bin/systemctl list-unit-files --full --no-legend 'mdd-vowifi@*.service' >"$MDD_UNIT_FILES"
  while IFS=' ' read -r unit _; do
    [ -n "$unit" ] || continue
    [ "$unit" = mdd-vowifi@.service ] && continue
    append_provider_unit "$unit"
  done <"$MDD_UNIT_LOADED"
  while IFS=' ' read -r unit _; do
    [ -n "$unit" ] || continue
    [ "$unit" = mdd-vowifi@.service ] && continue
    append_provider_unit "$unit"
  done <"$MDD_UNIT_FILES"
  if [ -d /etc/mdd/providers-current ]; then
    for config in /etc/mdd/providers-current/line-*.json; do
      [ -f "$config" ] || continue
      name=${config##*/}
      append_provider_unit "mdd-vowifi@${name%.json}.service"
    done
  fi
  rm -f "$MDD_UNIT_LOADED" "$MDD_UNIT_FILES"
  trap - EXIT HUP INT TERM
  printf '%s\n' "$MDD_PROVIDER_UNITS"
}

assert_inactive() {
  unit=$1
  state=$(/bin/systemctl is-active "$unit" 2>/dev/null || true)
  case "$state" in
    active|activating|reloading|deactivating) fail "$unit did not reach an inactive terminal state ($state)" ;;
  esac
}

assert_not_enabled() {
  unit=$1
  state=$(/bin/systemctl is-enabled "$unit" 2>/dev/null || true)
  case "$state" in
    enabled|enabled-runtime) fail "$unit remains enabled" ;;
  esac
}

start_services() {
  require_host
  require_configuration
  /bin/systemctl enable $UNITS
  /bin/systemctl start $UNITS
  providers=$(provider_units)
  started=0
  for unit in $providers; do
    if /bin/systemctl is-enabled --quiet "$unit"; then
      /bin/systemctl start "$unit"
      started=$((started + 1))
    fi
  done
  say "started Core, provider apply and country egress plus $started enabled Provider instance(s); the endpoint Agent was not started"
}

restart_services() {
  require_host
  require_configuration
  say "explicitly restarting Core, provider apply and country egress"
  /bin/systemctl restart $UNITS
}

stop_server_services() {
  require_host
  providers=$(provider_units)
  # Stop the only privileged process that can change Provider unit state before
  # taking down any line. Provider SIGTERM retains its normal physical hangup
  # and bounded stop contract.
  /bin/systemctl stop mdd-provider-apply.service
  for unit in $providers; do /bin/systemctl stop "$unit"; done
  /bin/systemctl stop mdd-core.service mdd-egress.service
  assert_inactive mdd-provider-apply.service
  for unit in $providers; do assert_inactive "$unit"; done
  assert_inactive mdd-core.service
  assert_inactive mdd-egress.service
  say "stopped Go server services; boot enablement and the independent endpoint Agent were left unchanged"
}

uninstall_software() {
  require_host
  installed_core=/usr/lib/mdd/current/mdd-core
  [ -f "$installed_core" ] && [ -x "$installed_core" ] || fail "no complete installed Go release was found"

  # Validate the complete managed layout before changing a service. The actual
  # removal re-acquires the same lock and repeats every check after systemd has
  # been quiesced, so this preflight is never treated as durable authority.
  "$installed_core" uninstall-release -check-only
  /bin/systemctl disable --now mdd-provider-apply.service
  providers=$(provider_units)
  for unit in $providers; do /bin/systemctl disable --now "$unit"; done
  /bin/systemctl disable --now mdd-core.service mdd-egress.service mdd-agent.service

  # Re-enumerate after stopping the apply helper. A late unit cannot hide
  # between the first inventory and the destructive transaction.
  providers=$(provider_units)
  assert_inactive mdd-provider-apply.service
  assert_not_enabled mdd-provider-apply.service
  for unit in $providers; do
    assert_inactive "$unit"
    assert_not_enabled "$unit"
  done
  for unit in mdd-core.service mdd-egress.service mdd-agent.service; do
    assert_inactive "$unit"
    assert_not_enabled "$unit"
  done
  "$installed_core" uninstall-release
  say "uninstalled verified Go software; /etc/mdd, /var/lib/mdd*, audit records and the mdd account were preserved"
}

certificate_spki() {
  openssl x509 -in "$1" -pubkey -noout |
    openssl pkey -pubin -outform DER 2>/dev/null |
    openssl dgst -sha256 -binary |
    openssl base64 -A
}

show_status() {
  require_host
  require_configuration
  command -v curl >/dev/null 2>&1 || fail "curl is required for pinned HTTPS status"
  command -v openssl >/dev/null 2>&1 || fail "openssl is required for SPKI verification"
  command -v timeout >/dev/null 2>&1 || fail "timeout is required for bounded TLS status"

  failed=0
  for unit in $UNITS; do
    state=$(/bin/systemctl is-active "$unit" 2>/dev/null || true)
    [ "$state" = active ] || failed=1
    printf '%-34s %s\n' "$unit" "${state:-unknown}"
  done

  status_port=${MDD_STATUS_PORT:-8443}
  case "$status_port" in
    ''|*[!0-9]*) fail "MDD_STATUS_PORT must be numeric" ;;
  esac
  [ "$status_port" -ge 1 ] && [ "$status_port" -le 65535 ] || fail "MDD_STATUS_PORT is out of range"

  remote=$(mktemp /run/mdd-status-handshake.XXXXXX)
  peer=$(mktemp /run/mdd-status-cert.XXXXXX)
  trap 'rm -f "$remote" "$peer"' EXIT HUP INT TERM
  if ! timeout 10s openssl s_client -connect "127.0.0.1:$status_port" -servername localhost \
      -CAfile "$TLS_CERT" -verify_return_error -showcerts </dev/null >"$remote" 2>/dev/null; then
    fail "TLS peer failed CA validation against $TLS_CERT"
  fi
  awk '/-----BEGIN CERTIFICATE-----/{copy=1} copy{print} /-----END CERTIFICATE-----/{exit}' \
    "$remote" > "$peer"
  [ -s "$peer" ] || fail "TLS peer did not present a certificate"
  expected=$(certificate_spki "$TLS_CERT")
  actual=$(certificate_spki "$peer")
  [ -n "$expected" ] && [ "$actual" = "$expected" ] || fail "TLS peer SPKI pin does not match the installed certificate"

  curl --fail --silent --show-error \
    --connect-timeout 5 --max-time 10 \
    --cacert "$TLS_CERT" \
    --resolve "localhost:$status_port:127.0.0.1" \
    "https://localhost:$status_port/healthz"
  printf '\nTLS SPKI sha256/%s\n' "$expected"
  rm -f "$remote" "$peer"
  trap - EXIT HUP INT TERM
  [ "$failed" -eq 0 ] || fail "one or more Go services are not active"
}

command=${1:-help}
[ $# -eq 0 ] || shift
case "$command" in
  install) install_release "$@" ;;
  start) [ $# -eq 0 ] || fail "start accepts no arguments"; start_services ;;
  restart) [ $# -eq 0 ] || fail "restart accepts no arguments"; restart_services ;;
  status) [ $# -eq 0 ] || fail "status accepts no arguments"; show_status ;;
  stop) [ $# -eq 0 ] || fail "stop accepts no arguments"; stop_server_services ;;
  uninstall) [ $# -eq 0 ] || fail "uninstall accepts no arguments"; uninstall_software ;;
  help|-h|--help) [ $# -eq 0 ] || fail "help accepts no arguments"; usage ;;
  *) usage >&2; fail "unknown command: $command" ;;
esac
