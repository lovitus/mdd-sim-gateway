#!/bin/bash
# Fresh standalone installation only. Existing server/Agent owners are never upgraded.
set -euo pipefail
export LC_ALL=C
export PATH=/usr/sbin:/usr/bin:/sbin:/bin
umask 022

fail() { printf 'mdd-agent installation: %s\n' "$*" >&2; exit 1; }
usage() {
    printf '%s\n' 'usage: install-linux-agent.sh preflight|install|start-preflight --candidate DIR [-config FILE]' >&2
    exit 2
}
action=${1-}; [ "$#" -gt 0 ] && shift
case "$action" in preflight|install|start-preflight) ;; *) usage ;; esac
candidate=
config=/var/lib/mdd-agent/config.json
while [ "$#" -gt 0 ]; do
    [ "$#" -ge 2 ] || usage
    case "$1" in --candidate) candidate=$2 ;; -config|--config) config=$2 ;; *) usage ;; esac
    shift 2
done
[ -n "$candidate" ] || usage
[ "$(uname -s)/$(uname -m)" = Linux/aarch64 ] || fail 'requires native Linux ARM64'
[ "$(id -u)" = 0 ] || fail 'standalone managed installation requires root; reader/cron deployment remains separate'
for tool in jq sha256sum readelf ldd timeout realpath stat find sort cmp systemctl systemd-analyze \
    udevadm nft ip modprobe busctl; do
    command -v "$tool" >/dev/null || fail "missing prerequisite: $tool"
done
candidate=$(realpath -e -- "$candidate")
[[ "$config" =~ ^/[a-zA-Z0-9_./-]+$ ]] || fail 'config must be an absolute path without whitespace or systemd specifiers'
[[ "$config" != *'/../'* && "$config" != *'/./'* && "$config" != *'//'* ]] || fail 'config path must be canonical'
case "$config" in
    /var/lib/*/*|/opt/*/*) ;;
    *) fail 'managed config/state must be under /var/lib or /opt; the original unit protects /home, /etc and /usr' ;;
esac

# Check every existing component without following user-controlled symlinks.
trusted_path() {
    local current=$1 mode
    while [ "$current" != / ]; do
        [ ! -L "$current" ] || fail 'installation/config path contains a symlink'
        if [ -e "$current" ]; then
            [ "$(stat -c %u -- "$current")" = 0 ] || fail 'installation/config path must be root owned'
            mode=$(stat -c %a -- "$current")
            (( (8#$mode & 0022) == 0 )) || fail 'installation/config path is writable by another account'
        fi
        current=$(dirname -- "$current")
    done
}

validate_package() {
    local line name binary loader found expected inventory
    [ -s "$candidate/SHA256SUMS" ] || fail 'missing package checksums'
    found=$(timeout 30 find "$candidate" ! -type d ! -type f -print -quit) || fail 'cannot scan package file types'
    [ -z "$found" ] || fail 'package contains a link or special file'
    while IFS= read -r line; do
        [[ "$line" =~ ^[0-9a-f]{64}'  ./'[a-zA-Z0-9_./@+-]+$ ]] || fail 'invalid checksum entry'
        name=${line:68}
        [[ "$name" != *'..'* && "$name" != /* && "$name" != SHA256SUMS ]] || fail 'unsafe checksum path'
    done < "$candidate/SHA256SUMS"
    expected=$(cut -c 69- "$candidate/SHA256SUMS" | sort) || fail 'cannot read checksum inventory'
    inventory=$(cd "$candidate" && timeout 30 find . -type f ! -path ./SHA256SUMS -printf '%P\n' | sort) || fail 'cannot completely scan package inventory'
    [ "$expected" = "$inventory" ] || fail 'checksum inventory is incomplete or duplicated'
    (cd "$candidate" && sha256sum --check --strict --status SHA256SUMS) || fail 'package checksum mismatch'
    for name in mdd-agent mdd-call-audio-helper mdd-agent.service mdd-cellular-guard.service \
        install-linux-agent.sh BUILD.txt ELF.txt DYNAMIC-LIBRARIES.txt README.txt MODEM_AGENT.md \
        LICENSE NOTICE THIRD_PARTY_LICENSES.md mdd-source.tar.gz; do
        [ -s "$candidate/$name" ] || fail "missing package component: $name"
    done
    [ -d "$candidate/THIRD-PARTY-LICENSES/call-audio-helper" ] || fail 'missing audio helper licenses'
    [ -d "$candidate/THIRD-PARTY-LICENSES/go-cli" ] || fail 'missing Agent licenses'
    revision=$(sed -n 's/^source_revision=//p' "$candidate/BUILD.txt")
    tree=$(sed -n 's/^source_tree=//p' "$candidate/BUILD.txt")
    [[ "$revision" =~ ^[0-9a-f]{40}$ && "$tree" =~ ^[0-9a-f]{40}$ ]] || fail 'invalid package source identity'
    grep -Fxq 'platform=linux/arm64' "$candidate/BUILD.txt" || fail 'package platform mismatch'
    for binary in mdd-agent mdd-call-audio-helper; do
        [ -x "$candidate/$binary" ] || fail "not executable: $binary"
        readelf -h "$candidate/$binary" | grep -Eq 'Machine: +AArch64' || fail "wrong ELF architecture: $binary"
        loader=$(readelf -l "$candidate/$binary" | sed -n 's/.*Requesting program interpreter: \(.*\)]/\1/p')
        [ -n "$loader" ] && [ -x "$loader" ] || fail "missing ELF loader: $binary"
        if ! libraries=$(ldd "$candidate/$binary" 2>&1) || [[ "$libraries" == *'not found'* ]]; then
            fail "missing dynamic dependency: $binary"
        fi
    done
    timeout 10 "$candidate/mdd-call-audio-helper" -h >/dev/null 2>&1 || fail 'audio helper cannot execute'
    timeout 15 "$candidate/mdd-call-audio-helper" -backend alsa -mode list 2>/dev/null |
        jq -e '.ok == true and .backend == "alsa" and .version == 4' >/dev/null || fail 'ALSA backend cannot enumerate; no audio hardware claim is made'
    manifest=$(sha256sum "$candidate/SHA256SUMS" | cut -d' ' -f1)
    release=/opt/mdd-agent/releases/$revision
}

validate_environment() {
    [ -d /run/systemd/system ] && [ -f /sys/fs/cgroup/cgroup.controllers ] || fail 'managed modem requires systemd with cgroup v2'
    [ -S /run/udev/control ] || fail 'udev is not running'
    timeout 10 modprobe --dry-run vhci_hcd >/dev/null 2>&1 || fail 'vhci_hcd module is unavailable (also required for native USB managed mode)'
    timeout 10 nft list tables >/dev/null 2>&1 || fail 'cannot read netfilter; root/kernel capability is missing'
    timeout 10 busctl --system list >/dev/null 2>&1 || fail 'system bus is unavailable'
    for directory in /etc/NetworkManager/conf.d /etc/udev/rules.d /etc/systemd/system/multi-user.target.wants; do
        trusted_path "$directory"
        [ -d "$directory" ] || fail "prepare a root-owned 0755 directory before installation: $directory (required by the original service sandbox even without NetworkManager)"
    done
    # These are observations only. In particular, neither guard apply nor its
    # VerifyContract is a read-only preflight, so do not invoke either here.
}

validate_state() {
    local state=${config%/*}/state item mode scan_fd scan_pid
    trusted_path "$state"
    [ -e "$state" ] || return 0
    [ -d "$state" ] && [ "$(stat -c %a "$state")" = 700 ] || fail 'existing adjacent state must be a private 0700 directory'
    exec {scan_fd}< <(timeout 30 find "$state" -mindepth 1 -print0)
    scan_pid=$!
    while IFS= read -r -d '' item; do
        [ ! -L "$item" ] && [ "$(stat -c %u "$item")" = 0 ] || fail 'foreign or linked adjacent state'
        mode=$(stat -c %a "$item")
        if [ -d "$item" ]; then
            [ "$mode" = 700 ] || fail 'existing state subdirectory must be 0700; no permissions were changed'
        else
            [ -f "$item" ] && [ "$mode" = 600 ] && [ "$(stat -c %h "$item")" = 1 ] || fail 'existing state file must be private regular 0600 with one link'
        fi
    done <&"$scan_fd"
    exec {scan_fd}<&-
    wait "$scan_pid" || fail 'cannot completely scan adjacent state'
    [ ! -e "$state/raw-modem-handoffs.db" ] || fail 'existing raw recovery state requires its original deployment/recovery owner, not fresh installation'
}

validate_config() {
    trusted_path "$config"
    validate_state
    config_state=needs_config
    modem=false
    backend=auto
    if [ ! -e "$config" ]; then
        [ "$action" != start-preflight ] || fail 'configuration is missing; run config setup first'
        [ "$config" = /var/lib/mdd-agent/config.json ] || fail 'explicit -config must already exist; it will not be created or moved'
        return
    fi
    [ -f "$config" ] && [ "$(stat -c %a -- "$config")" = 600 ] || fail 'config must be a regular root-owned 0600 file'
    [ ! -e "$config.setup-pending" ] && [ ! -L "$config.setup-pending" ] || fail 'finish the existing configuration setup before managed installation'
    local view
    view=$(timeout 10 "$candidate/mdd-agent" config show -config "$config") || fail 'cannot validate existing configuration'
    if ! jq -e '.ready == true' <<< "$view" >/dev/null; then
        [ "$action" != start-preflight ] || fail 'configuration is not ready; config show reports the precise problem'
        return
    fi
    config_state=valid
    modem=$(jq -r '.settings.agent.modem_enabled' <<< "$view")
    backend=$(jq -r '.settings.agent.modem_backend // "auto"' <<< "$view")
    if jq -e '.settings.agent.raw_usb_source_enabled == true or .settings.agent.raw_usb_importer_enabled == true' <<< "$view" >/dev/null; then
        fail 'raw USB requires its existing separately qualified deployment; this entry only qualifies native MM/serial'
    fi
    [ "$modem" = true ] || return 0
    local mm
    mm=$(timeout 10 systemctl show ModemManager.service -p LoadState -p ActiveState -p UnitFileState) || fail 'cannot verify ModemManager ownership'
    if [ "$backend" = serial ]; then
        if ! grep -Fxq LoadState=not-found <<< "$mm"; then
            grep -Fxq ActiveState=inactive <<< "$mm" &&
                grep -Eq '^UnitFileState=(disabled|masked)$' <<< "$mm" || fail 'serial requires absent or inactive and disabled/masked ModemManager; no service was stopped'
        fi
    else
        grep -Fxq ActiveState=active <<< "$mm" || fail 'ModemManager must already be active for the MM backend'
    fi
}

validate_guard_ownership() {
    local item tables
    for item in /var/lib/mdd-agent/cellular-guard-enabled /etc/udev/rules.d/90-mdd-cellular-guard.rules \
        /etc/NetworkManager/conf.d/90-mdd-cellular-guard.conf; do
        [ ! -e "$item" ] && [ ! -L "$item" ] || fail 'existing guard state; this entry is only for placement and first startup'
    done
    tables=$(timeout 10 nft --json list tables) || fail 'cannot observe guard table ownership'
    jq -e '.nftables | type == "array"' <<< "$tables" >/dev/null || fail 'invalid netfilter observation'
    if jq -e '.nftables[] | select(.table.family == "inet" and .table.name == "mdd_cellular_guard")' <<< "$tables" >/dev/null; then
        fail 'an existing cellular guard owns this host'
    fi
}

unit_paths=(/etc/systemd/system /run/systemd/system /usr/lib/systemd/system /lib/systemd/system)
config_dropin=/etc/systemd/system/mdd-agent.service.d/50-mdd-agent-config.conf
dropin_content() {
    printf '[Service]\nExecStart=\nExecStart=/usr/libexec/mdd/mdd-agent run -config %s\n' "$config"
}
validate_ownership() {
    local unit dir loaded fragment dropins item expected found
    for item in /opt /usr/libexec /etc/systemd/system /var/lib/mdd-agent; do trusted_path "$item"; done
    [ ! -e /usr/lib/mdd ] && [ ! -L /usr/lib/mdd ] || fail 'full-server installation owns this host layout'
    for unit in mdd-core.service mdd-agent.service mdd-cellular-guard.service; do
        loaded=$(timeout 10 systemctl show "$unit" -p LoadState -p ActiveState -p FragmentPath -p DropInPaths) || fail 'cannot inspect loaded unit ownership'
        grep -Eq '^ActiveState=(inactive|failed)$' <<< "$loaded" || fail 'existing service is active; installation never stops an owner'
        fragment=$(sed -n 's/^FragmentPath=//p' <<< "$loaded")
        dropins=$(sed -n 's/^DropInPaths=//p' <<< "$loaded")
        if [ "$action" = start-preflight ] && [ "$unit" != mdd-core.service ]; then
            [ -n "$fragment" ] && [ "$(realpath -e -- "$fragment")" = "$release/$unit" ] || fail 'loaded unit does not belong to the exact installed package; run daemon-reload after placement'
            expected=
            if [ "$unit" = mdd-agent.service ] && [ "$config" != /var/lib/mdd-agent/config.json ]; then expected=$config_dropin; fi
            [ "$dropins" = "$expected" ] || fail 'foreign or changed service drop-in'
        else
            grep -Fxq LoadState=not-found <<< "$loaded" && [ -z "$fragment$dropins" ] || fail 'existing or unknown systemd owner'
        fi
        for dir in "${unit_paths[@]}"; do
            item=$dir/$unit
            [ ! -e "$item.wants" ] && [ ! -e "$item.requires" ] || fail 'existing unit dependency overrides require administrator review'
            if [ "$action" = start-preflight ] && [ "$dir" = /etc/systemd/system ] && [ "$unit" != mdd-core.service ]; then
                [ -L "$item" ] && [ "$(readlink "$item")" = "$release/$unit" ] || fail 'unit link ownership changed'
            else
                [ ! -e "$item" ] && [ ! -L "$item" ] || fail 'existing unit file; refusing replacement'
            fi
            if [ "$action" = start-preflight ] && [ "$item.d" = "${config_dropin%/*}" ] && [ "$config" != /var/lib/mdd-agent/config.json ]; then
                trusted_path "$config_dropin"
                found=$(timeout 30 find "$item.d" -mindepth 1 -maxdepth 1 -printf x) || fail 'cannot completely scan drop-in directory'
                [ "$found" = x ] &&
                    cmp -s "$config_dropin" <(dropin_content) || fail 'explicit config drop-in changed'
            else
                [ ! -e "$item.d" ] && [ ! -L "$item.d" ] || fail 'existing drop-in directory; refusing replacement'
            fi
        done
    done
    # Do not infer ownership merely from a service name or an inactive unit.
    for dir in "${unit_paths[@]}"; do
        for item in "$dir/service.d" "$dir/mdd-.service.d"; do
            [ ! -e "$item" ] || fail 'global/prefix service overrides require explicit administrator review'
        done
    done
    for item in /proc/[0-9]*/exe; do
        exe=$(readlink "$item" 2>/dev/null || true)
        case "$exe" in */mdd-agent|*/mdd-agent\ \(deleted\)) fail 'an Agent process already owns this host; no process was stopped' ;; esac
    done
    validate_guard_ownership
    if [ "$action" = start-preflight ]; then
        trusted_path /opt/mdd-agent
        trusted_path "$release"
        found=$(timeout 30 find /opt/mdd-agent \( ! -user root -o -perm /022 \) -print -quit) || fail 'cannot scan installed package ownership'
        [ -z "$found" ] || fail 'installed package ownership or permissions changed'
        [ "$candidate" = "$release" ] || fail 'startup preflight must use the installed version directory'
        [ -L /usr/libexec/mdd/mdd-agent ] && [ "$(readlink /usr/libexec/mdd/mdd-agent)" = "$release/mdd-agent" ] || fail 'fixed executable link changed'
        jq -e --arg r "$revision" --arg m "$manifest" --arg c "$config" \
            '.revision == $r and .manifest == $m and .config == $c' /opt/mdd-agent/install.json >/dev/null || fail 'installation receipt does not match config/package'
    else
        for item in /opt/mdd-agent /usr/libexec/mdd; do
            [ ! -e "$item" ] && [ ! -L "$item" ] || fail 'existing deployment or guard state; fresh installation only'
        done
    fi
}

validate_package
validate_environment
validate_config
validate_ownership
if [ "$action" = preflight ]; then
    jq -n --arg c "$config_state" '{status:"preflight_ok",config:$c,started:false,guard_applied:false}'
    exit
fi
if [ "$action" = start-preflight ]; then
    systemd-analyze verify "$release/mdd-agent.service" "$release/mdd-cellular-guard.service" >/dev/null 2>&1 || fail 'unit verification failed'
    jq -n --arg b "$backend" --argjson m "$modem" '{status:"startup_preflight_ok",modem_enabled:$m,backend:$b,started:false,guard_applied:false,connected:false}'
    exit
fi

# Create-only publication: a stale preflight never grants replacement rights.
# A shell stat/unlink pair cannot prove ownership across concurrent replacement.
# Never roll back published objects here; leave inert partial placement for review.
trap 'rc=$?; if [ "$rc" -ne 0 ]; then printf "%s\n" "Placement failed. Nothing was started. Published objects and the inert version directory were retained; an administrator must inspect ownership before retrying." >&2; fi' EXIT
validate_ownership
mkdir -m 0755 /opt/mdd-agent
mkdir -m 0755 /opt/mdd-agent/releases
mkdir -m 0755 "$release"
cp -R --no-dereference -- "$candidate/." "$release/"
candidate=$release
validate_package
chmod -R go-w "$release"
jq -n --arg r "$revision" --arg m "$manifest" --arg c "$config" \
    '{revision:$r,manifest:$m,config:$c}' > /opt/mdd-agent/install.json
chmod 0600 /opt/mdd-agent/install.json
[ -d /usr/libexec ] || mkdir -m 0755 /usr/libexec
mkdir -m 0755 /usr/libexec/mdd
ln -sT -- "$release/mdd-agent" /usr/libexec/mdd/mdd-agent
for unit in mdd-agent.service mdd-cellular-guard.service; do
    ln -sT -- "$release/$unit" "/etc/systemd/system/$unit"
done
if [ "$config" != /var/lib/mdd-agent/config.json ]; then
    mkdir -m 0755 "${config_dropin%/*}"
    (set -o noclobber; dropin_content > "$config_dropin")
fi
timeout 30 systemctl daemon-reload
printf '%s\n' 'Installed, not enabled or started. Complete config setup, then run start-preflight against the installed directory.'
