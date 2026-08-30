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

install verifies and atomically installs an immutable Go release. On a fresh
host it also creates the initial administrator, TLS identity and Core config;
provide the administrator password on stdin (or enter it at the TTY prompt).
Installing over a complete existing configuration never starts or restarts a
service. start enables only Core, provider apply and country-egress services.
restart is the only command here that deliberately restarts running services.

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

start_services() {
  require_host
  require_configuration
  /bin/systemctl enable $UNITS
  /bin/systemctl start $UNITS
  say "started Core, provider apply and country egress; this command did not directly start a Provider template instance"
}

restart_services() {
  require_host
  require_configuration
  say "explicitly restarting Core, provider apply and country egress"
  /bin/systemctl restart $UNITS
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
  help|-h|--help) [ $# -eq 0 ] || fail "help accepts no arguments"; usage ;;
  *) usage >&2; fail "unknown command: $command" ;;
esac
