#!/bin/bash
# Engine runtime child. /entrypoint.sh starts the admission supervisor before this script, so
# PIN/SWu/P-CSCF initialization, its helpers and Asterisk all remain in one supervised process
# group for the entire container lifetime.
set -u

export MDD_RUNDIR="${MDD_RUNDIR:-/run/mdd-sim-gateway}"
mkdir -p "$MDD_RUNDIR" /logs /etc/asterisk

log() { echo "[entrypoint] $*"; }

# The bind-mounted run directory survives an ``unless-stopped`` restart while Docker keeps the
# same container id. Publish the outer supervisor's one canonical process incarnation before any
# callback can fire, then discard stale discovery files.
python3 /usr/local/bin/pcscf_state.py init-run "$MDD_ENGINE_RUN_ID" || {
  log "could not publish Engine run id"; exit 1;
}
rm -f "$MDD_RUNDIR/pcscf" "$MDD_RUNDIR/pcscf.applied" \
      "$MDD_RUNDIR/pcscf-discovery.json"

# --- 1. Render configs from /config/instance.json --------------------------------
log "rendering configs..."
python3 /usr/local/bin/render.py || { log "render failed"; exit 1; }
# shellcheck disable=SC1091
set -a; . "$MDD_RUNDIR/engine.env"; set +a
export USIM_PIN USIM_READER USIM_READER_INDEX USIM_READER_PORT USIM_IMSI MDD_ID MANAGER_URL MANAGER_EVENT_TOKEN MDD_RUNDIR
export PIN_USIM_READER
export SWU_SOURCE SWU_EPDG SWU_APN SWU_MCC SWU_MNC SWU_IMEI SWU_IMEISV SWU_CHILD_REKEY_MINUTES SWU_IDR_MODE SWU_CP_MODE SWU_CP_MODE_ORDER
export SWU_ACCEPT_EPDG_ESP_REKEY

# --- 2. Start PIN keeper and wait for the SIM to be usable ------------------------
# pin_keeper holds CHV1 verified for ami_usim's SIP IMS-AKA. swu_ike verifies the PIN itself
# in its own connection for EAP-AKA, so both auth paths work on PIN-enabled SIMs.
log "starting pin_keeper (reader=${PIN_USIM_READER:-$USIM_READER})..."
USIM_READER="${PIN_USIM_READER:-$USIM_READER}" python3 -u /usr/local/bin/pin_keeper.py &

wait_pin() {
    for _ in $(seq 1 30); do
        st=$(python3 -c "import json;print(json.load(open('$MDD_RUNDIR/pin_status.json'))['state'])" 2>/dev/null || echo "")
        case "$st" in
            VERIFIED|PIN_DISABLED) log "PIN state: $st"; return 0 ;;
            WRONG_PIN|PIN_BLOCKED) log "PIN problem: $st - continuing (manager will surface)"; return 1 ;;
        esac
        sleep 1
    done
    log "PIN keeper did not reach VERIFIED in time - continuing anyway"
    return 1
}
wait_pin || true

# --- 3. Bring up the SWu (python IKEv2/IPsec) tunnel, supervised ------------------
log "starting SWu IKEv2 tunnel (epdg=$SWU_EPDG apn=$SWU_APN reader=$USIM_READER_INDEX port=${USIM_READER_PORT:-none})..."
rm -f "$MDD_RUNDIR/swu.ctl" "$MDD_RUNDIR/swu_status.json"

# swu_ike is very chatty (per-packet IKE decode dumps). Send ITS stdout+stderr ONLY to the IKE
# log through log_capture.py. All pipeline members inherit this runtime process group.
(
  backoff=4
  while true; do
    log "swu_ike starting"
    python3 -u /usr/local/bin/swu_ike.py \
        -m "${USIM_READER_INDEX:-0}" \
        -s "$SWU_SOURCE" \
        -d "$SWU_EPDG" \
        -a "${SWU_APN:-ims}" \
        -I "$USIM_IMSI" \
        -M "$SWU_MCC" \
        -N "$SWU_MNC" \
        -E "${SWU_IMEI:-}" \
        -V "${SWU_IMEISV:-}" 2>&1 | \
      python3 -u /usr/local/bin/log_capture.py \
        --current "$MDD_RUNDIR/charon.log" \
        --archive-dir /logs/ike
    rc=${PIPESTATUS[0]}
    log "swu_ike exited (rc=$rc); reconnecting in ${backoff}s"
    sleep "$backoff"; backoff=$((backoff*2)); [ "$backoff" -gt 60 ] && backoff=60
  done
) &

# --- 4. Wait for the tunnel, then (re)render pjsip with the discovered P-CSCF ------
log "waiting for SWu tunnel to establish..."
for _ in $(seq 1 90); do
  st=$(python3 -c "import json;print(json.load(open('$MDD_RUNDIR/swu_status.json'))['state'])" 2>/dev/null || echo "")
  [ "$st" = "CONNECTED" ] && { log "SWu tunnel CONNECTED"; break; }
  sleep 1
done

log "waiting for P-CSCF discovery..."
for _ in $(seq 1 30); do
  [ -s "$MDD_RUNDIR/pcscf-discovery.json" ] && break
  sleep 1
done
# Selection, explicit-address rendering and applied commit are one file-locked transaction.
bootstrap=$(python3 /usr/local/bin/pcscf_state.py \
  render-bootstrap "$MDD_ENGINE_RUN_ID" /usr/local/bin/render.py) || {
  log "P-CSCF bootstrap render transaction failed"; exit 1;
}
kind=""
addr=""
if [[ "$bootstrap" == "none" ]]; then
  kind="none"
elif [[ "$bootstrap" =~ ^(fresh|fallback)[[:space:]]([^[:space:]]+)$ ]]; then
  kind="${BASH_REMATCH[1]}"
  addr="${BASH_REMATCH[2]}"
else
  log "invalid P-CSCF bootstrap result"; exit 1
fi
case "$kind" in
  fresh) log "fresh P-CSCF rendered: $addr" ;;
  fallback) log "gated previous-run P-CSCF rendered: $addr (awaiting fresh confirmation)" ;;
  none) log "no P-CSCF discovered yet - continuing (manager will surface tunnel state)" ;;
  *) log "invalid P-CSCF bootstrap result"; exit 1 ;;
esac

# --- 5. Start USIM<->AMI bridge and Asterisk -------------------------------------
log "starting ami_usim bridge..."
python3 -u /usr/local/bin/ami_usim.py /usr/local/etc/ami_usim.ini &

log "starting Asterisk..."
# Replace the runtime group leader. The admission supervisor retains the original PGID and owns
# bounded termination/reaping for Asterisk and every initialization helper.
exec asterisk -f
