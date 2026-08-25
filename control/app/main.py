"""
main.py - MDD Sim Gateway control surface (FastAPI).

Serves the management REST API + WebSocket live feed + the built WebUI, and (for the
browser softphone) proxies provisioning. Runs natively or in a container; talks to
engine containers via the Docker SDK (engine.py) and Asterisk AMI (ami.py). HTTPS with
an auto-generated self-signed cert by default.
"""
from __future__ import annotations

import asyncio
import base64
import glob
import hashlib
import hmac
import ipaddress
import json
import logging
import math
import os
import random
import re
import struct
import time
import uuid
from urllib.parse import quote, unquote
from datetime import datetime
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError
from contextlib import asynccontextmanager, contextmanager, suppress

from fastapi import FastAPI, WebSocket, WebSocketDisconnect, HTTPException, Request
from fastapi.responses import JSONResponse, FileResponse, Response
from fastapi.staticfiles import StaticFiles

from . import config as cfg
from . import (store, engine, status as status_mod, sim, card, notify_push, lpa, auth,
               estkme, usbreader, egress, device_state, operations, update_check, cellular_sms,
               sysinfo, failover, carrier_id, allowance, cellular_call, vpcd_slots,
               remote_modem, call_media, firmware_matrix, control_lifecycle)
from . import sms_content
from .modem_registry import (
    ModemConflict, ModemTimeout, ModemUnavailable, call_contract_reason,
    registry as modem_registry,
)
from .agent_health_registry import registry as agent_health_registry
from .media_admission import registry as media_admission
from . import media_ingress
from .sip_media_proxy import SipMediaRewriteError, rewrite_engine_sdp
from .version import VERSION
from .ami import AmiClient, OneShotAmiSession, StaleAmiGeneration
from .runtime import RuntimeRegistry

STATUS_OK_GRACE_SECONDS = 20
STATUS_POLL_FAST_SECONDS = 4.0
STATUS_POLL_HEALTHY_SECONDS = 15.0
STATUS_CACHE_MAX_AGE_SECONDS = max(60.0, 2 * STATUS_POLL_HEALTHY_SECONDS
                                   + STATUS_OK_GRACE_SECONDS)
# Once Asterisk has completed its own bounded REGISTER transaction and explicitly reports no
# response, another two minutes of same-session retries cannot repair the stale carrier-side
# P-CSCF/ESP state.  Rebuild promptly, but leave enough time for the diagnostic worker to capture
# and remove the expected container generation before auto-start runs.  Per-line rate limiting
# prevents a broken carrier from turning this fast path into a rebuild loop.
REG_UNANSWERED_RECOVERY_DELAY_SECONDS = float(
    os.environ.get("MDD_REG_UNANSWERED_RECOVERY_DELAY", "10"))
REG_UNANSWERED_MIN_INTERVAL_SECONDS = float(
    os.environ.get("MDD_REG_UNANSWERED_MIN_INTERVAL", "300"))
CELLULAR_MEDIA_PREPARE_TTL_SECONDS = float(
    os.environ.get("MDD_CELLULAR_MEDIA_PREPARE_TTL", "90"))
# A WebSocket send normally only queues bytes locally.  If Engine/TCP backpressure prevents that
# for this long, release the cross-process P-CSCF admission lock and close the stale bridge.
SOFTPHONE_UPSTREAM_SUBMIT_TIMEOUT_SECONDS = 5.0
# Conditions that are genuinely measured but routinely spike for one sample. Starting a
# container on a memory-tight box pages a batch back in; that is the cost of the operation,
# not a problem anyone can act on. Only a rate that holds across consecutive polls is one.
SUSTAINED_ALERT_CODES = {"swap_pressure"}
SUSTAINED_ALERT_SAMPLES = int(os.environ.get("MDD_SUSTAINED_ALERT_SAMPLES", "3"))
# Connectivity timeline shown per line. The window follows how much history has actually
# accumulated so a fresh install is not stretched across an empty two-day axis.
LINE_HISTORY_MAX_SECONDS = 2 * 24 * 3600
LINE_HISTORY_MIN_SECONDS = 3600
LINE_HISTORY_PRUNE_INTERVAL_SECONDS = 3600
# Comfortably below store.LINE_STATE_CONTINUITY_SECONDS, so throttled writes still read back
# as one uninterrupted observation.
LINE_STATE_WRITE_INTERVAL_SECONDS = 30
USIM_RECOVERY_SCAN_SECONDS = 1.0
_line_state_written: dict[str, tuple[str, float]] = {}

logging.basicConfig(level=logging.INFO,
                    format="%(asctime)s %(levelname)s %(name)s %(message)s")
log = logging.getLogger("vowifi.main")

WEBUI_DIR = os.environ.get("MDD_WEBUI", os.path.join(
    os.path.dirname(os.path.dirname(__file__)), "webui", "dist"))


def _unsafe_spa_path(full_path: str) -> bool:
    decoded = str(full_path or "")
    for _ in range(3):
        decoded = unquote(decoded)
    return ("\\" in decoded or "\x00" in decoded or
            any(part in {".", ".."} for part in decoded.split("/")))


def _carrier_identity(value) -> dict:
    """Extract locally-held SIM matching attributes without inventing missing values."""
    nested = (value.get("carrier_identity") if isinstance(value, dict)
              else getattr(value, "carrier_identity", None)) or {}
    result = {key: nested[key] for key in ("spn", "gid1", "gid2") if key in nested}
    for key in ("spn", "gid1", "gid2"):
        raw = value.get(key) if isinstance(value, dict) else getattr(value, key, None)
        if raw is not None:
            result[key] = str(raw)
    return result


def _carrier_identity_update(value) -> dict:
    identity = _carrier_identity(value)
    result = {"carrier_identity": identity} if identity else {}
    mnc_len = value.get("mnc_len") if isinstance(value, dict) else getattr(value, "mnc_len", None)
    if mnc_len in (2, 3):
        result["mnc_len"] = int(mnc_len)
    return result


def _carrier_description(inst: dict | None, card_info: dict | None,
                         cellular: dict | None = None) -> dict:
    """Resolve a safe display value; never return IMSI, ICCID, SPN or GID."""
    inst, card_info = inst or {}, card_info or {}
    identity = {**inst, **{key: value for key, value in card_info.items()
                           if value not in (None, "")}}
    identity["carrier_identity"] = _carrier_identity(card_info) or _carrier_identity(inst)
    resolved = carrier_id.lookup(identity) or {
        "name": "", "home_network": "",
        "plmn": "-".join(filter(None, (str(identity.get("mcc") or ""),
                                          str(identity.get("mnc") or "")))),
        "match_source": "mccmnc", "database": "none",
    }
    current = str((cellular or {}).get("operator") or "").strip()
    if current.casefold() in {"--", "unknown", "none", "n/a"}:
        current = ""
    return {**resolved, "current_network": current}


def _device_egress_view(inst: dict | None, live_identity: dict | None,
                        available_countries: list[str] | None = None,
                        egress_state: dict | None = None) -> dict:
    """Build one line's country/exit view without depending on a cached reader row.

    The saved line is matched by ICCID before this helper is called and remains authoritative.
    Live Agent/card identity only fills fields that are absent from that line; it never mutates
    the saved configuration or overrides an operator-selected country.
    """
    stored, live = inst or {}, live_identity or {}
    identity = dict(live)
    identity.update({key: value for key, value in stored.items()
                     if value not in (None, "")})
    detected = egress.country_for_mcc(identity.get("mcc"))
    override = egress.normalize_country(stored.get("proxy_country"))
    country = override or detected
    state = egress_state if isinstance(egress_state, dict) else egress.status()
    exit_state = (state.get("exits") or {}).get(country, {}) if country else {}
    if available_countries is None:
        configured = (cfg.get_settings().get("proxy") or {}).get("exits") or {}
        available_countries = sorted(
            code for code, value in configured.items()
            if isinstance(value, dict) and value.get("enabled", False))
    return {
        "node": (state.get("lines") or {}).get(str(stored.get("id") or ""), {}).get(
            "node") or "",
        **{key: exit_state.get(key) or ""
           for key in ("pinned_node", "pin_mode", "selection", "last_change",
                       "pinned_cooldown_seconds")},
        "country": country,
        "detected_country": detected,
        "override": override,
        "available_countries": available_countries,
    }


def _client_card_info(value: dict) -> dict:
    """Card monitor view for authenticated clients, enriched with carrier/profile name."""
    info = dict(value)
    spn = value.get("spn")
    if not spn and isinstance(value.get("carrier_identity"), dict):
        spn = value["carrier_identity"].get("spn")
    if spn:
        info["spn"] = spn
        info["profile_name"] = spn
    # Lookup carrier display name
    try:
        desc = _carrier_description(None, value)
        if desc.get("name"):
            info["carrier"] = desc["name"]
    except Exception:
        pass
    return {key: item for key, item in info.items()
            if key not in {"carrier_identity", "gid1", "gid2"}}



def _client_cards(values: list[dict] | None = None) -> list[dict]:
    return [_client_card_info(value) for value in (values if values is not None
                                                    else hub.cards_list())]


def _modem_identity_for_reader(reader_name: str | None) -> dict | None:
    """Identify a modem only from its generated VPCD reader name, never from SIM ICCID."""
    reader_name = str(reader_name or "")
    hardware_id = device_state.vpcd_modem_hardware_id(reader_name)
    if not hardware_id:
        return None
    for path in glob.glob(os.path.join(cfg.DATA_DIR, "modems", "*.json")):
        try:
            with open(path, encoding="utf-8") as handle:
                identity = json.load(handle)
            if str(identity.get("hardware_id") or "") == hardware_id:
                imei = cfg.normalize_imei(identity.get("imei", ""))
                if len(imei) == 15:
                    return {**identity, "imei": imei}
        except (OSError, ValueError, TypeError):
            continue
    # The generated reader can outlive bridge metadata across an unplug/restart.
    # Its name still contains the stable physical id, which is sufficient to keep
    # the empty virtual slots grouped under the original offline modem.
    return {"hardware_id": hardware_id, "slots": 1}


def _with_detected_imei(cards: list[dict]) -> list[dict]:
    """Annotate native readers and collapse a modem's internal VPCD slots into one device."""
    enriched = []
    consumed = set()
    modem_identities = []
    assignment_names = {}
    try:
        with open(os.path.join(cfg.DATA_DIR, "orchestrator", "hardware-state.json"),
                  encoding="utf-8") as handle:
            hardware_state = json.load(handle)
        assignment_names = {
            str(device_id): str(value.get("name") or "")
            for device_id, value in (hardware_state.get("assignments") or {}).items()
        }
    except (OSError, ValueError, TypeError):
        pass
    for path in glob.glob(os.path.join(cfg.DATA_DIR, "modems", "*.json")):
        try:
            identity = json.load(open(path, encoding="utf-8"))
            if identity.get("hardware_id"):
                modem_identities.append(identity)
        except (OSError, ValueError, TypeError):
            pass
    for original in cards:
        if original.get("name") in consumed:
            continue
        card_info = dict(original)
        # Generated VPCD reader names carry the stable hardware id. SIM ICCID is deliberately
        # not considered: the same SIM may move to a native reader while an offline modem's
        # metadata still contains that card's last identity.
        hardware_id = device_state.vpcd_modem_hardware_id(card_info.get("name"))
        identity = next((x for x in modem_identities
                         if str(x.get("hardware_id")) == hardware_id), None)
        if hardware_id and not identity:
            identity = {"hardware_id": hardware_id, "slots": 1}
        if identity:
            imei = cfg.normalize_imei(identity.get("imei", ""))
            if len(imei) == 15:
                card_info["imei"] = imei
                card_info["imei_source"] = "modem"
            count = max(1, int(identity.get("slots") or 1))
            hwid = str(identity.get("hardware_id") or "")
            siblings = [dict(c) for c in cards if
                        device_state.vpcd_modem_hardware_id(c.get("name")) == hwid]
            siblings.sort(key=lambda c: (c.get("index") is None, c.get("index") or 0))
            if len(siblings) > 1:
                consumed.update(c.get("name") for c in siblings[1:])
                card_info = {**siblings[0], **card_info}
                card_info["hardware_kind"] = "modem"
                card_info["hardware_id"] = identity.get("hardware_id") or identity.get("modem")
                card_info["display_name"] = (assignment_names.get(hwid)
                                             or "Cellular modem")
                card_info["virtual_slots"] = [
                    {"index": c.get("index"), "name": c.get("name")} for c in siblings[:count]]
        else:
            card_info["hardware_kind"] = "reader"
        card_info["country"] = egress.country_for_mcc(card_info.get("mcc"))
        enriched.append(card_info)
    return enriched


def _next_instance_id() -> str:
    """Return the lowest unused numeric line id without assuming ids are contiguous."""
    used = {str(item.get("id")) for item in cfg.list_instances()}
    candidate = 1
    while str(candidate) in used:
        candidate += 1
    return str(candidate)


def _card_draft_record(info: dict, iid: str) -> dict:
    """Build, but do not persist, one stopped line draft."""
    iccid = str(info.get("iccid") or "").strip()
    binding = cfg.get_iccid_imei_binding(iccid)
    bound_imei = binding.get("imei") if binding else ""
    identity = _modem_identity_for_reader(info.get("name")) or {}
    reported_imei = cfg.normalize_imei(info.get("imei", ""))
    imei = bound_imei or (reported_imei if len(reported_imei) == 15 else "") or identity.get("imei") or ""
    mcc, mnc = str(info.get("mcc") or ""), str(info.get("mnc") or "")
    provisioning_state = "ready" if (len(imei) == 15 and info.get("imsi")) else "draft"
    return {
            "id": str(iid),
            "name": cfg.default_instance_name(mcc, mnc, iccid),
            "provisioning_state": provisioning_state,
            "iccid": iccid,
            "imsi": str(info.get("imsi") or ""),
            "mcc": mcc,
            "mnc": mnc,
            **_carrier_identity_update(info),
            "imei": imei,
            "imei_source_device_id": binding.get("imei_id") if binding else "",
            "reader": f"imsi:{info['imsi']}" if info.get("imsi") else "",
            "reader_index": int(info.get("index") or 0),
            "reader_port": str(info.get("reader_port") or ""),
            "smsc": str(info.get("smsc") or ""),
            "proxy_country": "",
            "enabled": False,
            "apn": "ims",
            "idr_mode": "apn",
            "cp_mode": "auto",
            "sip": {**cfg.carrier_sip_defaults(mcc, mnc, iccid),
                    "listen_addr": "0.0.0.0", "transport": "udp", "external": [],
                    "webrtc": {"enable": True}},
            "debug": {"asterisk": False, "charon": False},
        }


def _ensure_card_draft(info: dict) -> dict | None:
    """Persist a safe stopped draft; hotplug's fenced path uses the atomic helper directly."""
    iccid = str(info.get("iccid") or "").strip()
    if not iccid:
        return None
    existing = cfg.instances_by_iccid(iccid)
    if len(existing) == 1:
        return existing[0]
    if len(existing) > 1:
        return None
    proposed_iid = _next_instance_id()
    try:
        inst = cfg.upsert_instance_unique_iccid(
            _card_draft_record(info, proposed_iid), require_iid_absent=True,
            unique_name=True)
    except (cfg.InstanceIdentityConflict, cfg.InstanceIdConflict):
        current = cfg.instances_by_iccid(iccid)
        return current[0] if len(current) == 1 else None
    egress.publish()
    return inst



def _exit_ledger_path() -> str:
    return os.path.join(cfg.DATA_DIR, "exit-failover.json")


def _load_exit_ledgers() -> dict:
    try:
        with open(_exit_ledger_path(), encoding="utf-8") as handle:
            loaded = json.load(handle)
        return {str(k): v for k, v in loaded.items() if isinstance(v, dict)}
    except (OSError, ValueError, AttributeError):
        return {}


class Hub:
    """Holds AMI clients per instance and broadcasts events to WebSocket clients."""
    def __init__(self):
        self.ami: dict[str, AmiClient] = {}
        self.ami_generation: dict[str, str | None] = {}
        self._ami_locks: dict[str, asyncio.Lock] = {}  # per-instance ami_for serialisation
        self.runtime = RuntimeRegistry()
        self.status_wakeup = asyncio.Event()
        self.clients: set[WebSocket] = set()
        self.cards: dict[str, dict] = {}     # reader NAME -> detected card/reader info
        self.scanned = False                 # card_monitor completed its first scan
        self.probe_quarantine_blockers: set[str] = set()
        self.probe_quarantine_state_unknown = False
        # Serialise route selection and submission per line. In particular, two concurrent
        # ``auto`` requests must not both decide that the preferred route is unavailable and
        # submit the same user action through different transports.
        self.sms_send_locks: dict[str, asyncio.Lock] = {}
        # The task, not an HTTP connection, owns one complete per-line SMS decision and submit.
        # Cancelled callers leave an uncertainty tombstone so a retry cannot cross transports
        # and submit a second chargeable message after losing the first response.
        self.sms_submission_tasks: dict[str, dict] = {}
        # Per-line exit failover ledger. Persisted: a control-plane restart must not
        # re-announce a give-up it already reported, nor re-walk an exhausted pool.
        self.exit_ledgers: dict[str, dict] = _load_exit_ledgers()
        self.health: dict[str, dict] = {}    # per-instance retry/health tracking
        # Serialises the final no-call check/removal with every server-originated call admission
        # on the same Asterisk line. It is intentionally per line: one broken carrier must not
        # stall unrelated calls.
        self.engine_recovery_locks: dict[str, asyncio.Lock] = {}
        # Monotonic user/lifecycle intent per line. A queued background recovery captures the
        # value that scheduled it and must stand down after any explicit start/stop changed it.
        self.engine_lifecycle_epoch: dict[str, int] = {}
        self.engine_recovering: set[str] = set()
        # Separate from health recovery: a durable P-CSCF generation fence survives Control and
        # same-container Engine restarts, and must never be cleared by a healthy IMS sample.
        self.pcscf_rebinding: set[str] = set()
        self.pcscf_rebind_result: dict[str, dict] = {}
        # Kept outside health: a successful registration resets health, but must not erase the
        # anti-churn interval for the next stale-session failure on the replacement container.
        self.reg_unanswered_recovery_at: dict[str, float] = {}
        self.status_cache: dict[str, dict] = {}  # background sampled; HTTP never probes devices
        self.status_sampled_at: dict[str, float] = {}  # last authoritative status observation
        # Runtime lifecycle snapshots are not authoritative IMS samples.  They only mask a
        # previously healthy sample immediately after Docker says an Engine stopped or changed,
        # until the event-woken status poller records the new real state.
        self.status_transitions: dict[str, dict] = {}
        self.status_runtime_epoch: dict[str, int] = {}
        self.status_publish_locks: dict[str, asyncio.Lock] = {}
        self._pushed_calls: set[int] = set() # call-record ids already push-notified (dedupe)
        # Per-reader serialization for PC/SC APDU access (sim.read_card / PIN / lpac).
        # lpac opens SCARD_SHARE_EXCLUSIVE; concurrent connect/APDU on the same reader
        # fails with sharing violations or corrupts eUICC sessions.
        self.reader_locks: dict[str, asyncio.Lock] = {}
        self.lpa_busy: dict[str, bool] = {}  # readers currently owned by an LPA op
        self.lpa_downloads: dict[str, dict] = {}  # reader_name -> active download handle
        self.hotplug_starts: set[str] = set()  # debounce duplicate modem VPCD slots
        self.usim_recovery_diagnostics: set[tuple[str, str]] = set()
        # Remote VPCD transport observations are not physical-removal authority.  These
        # structures hold only bounded, recomputable health-v2 evidence joins.
        self.remote_loss_candidates: dict[str, dict] = {}
        self.remote_loss_inflight: set[tuple] = set()
        self.remote_loss_completed: set[tuple] = set()
        self.remote_reader_seen: set[tuple[str, str, str, str]] = set()
        # When each line last became healthy, so a failure can be attributed. A line that
        # carried IMS for a long time and then broke is not evidence against its exit node.
        self.ok_since: dict[str, float] = {}
        # Latest host snapshot, sampled by host_health_poller so HTTP never shells out.
        self.host_snapshot: dict = {}
        self.host_alerts: list[dict] = []
        # Shared with the acknowledgement endpoint. The poller owns condition lifecycle;
        # the API only marks currently visible items handled.
        self.host_alert_state: dict | None = None

    def cards_list(self) -> list[dict]:
        """Reader/card entries sorted by current PC/SC index (the UI display order)."""
        cards = sorted(self.cards.values(),
                       key=lambda c: (c.get("index") is None, c.get("index") or 0,
                                      c.get("name") or ""))
        return _with_detected_imei(vpcd_registry.enrich_cards(cards))

    def reader_lock(self, name: str) -> asyncio.Lock:
        if name not in self.reader_locks:
            self.reader_locks[name] = asyncio.Lock()
        return self.reader_locks[name]

    def health_for(self, iid: str) -> dict:
        return self.health.setdefault(str(iid), {
            "fail_start": None, "retry_count": 0, "frozen_code": None,
            "frozen_reason": None, "last_state": None, "next_retry_at": None,
            "auto_retrying": False, "retry_delay": None,
            "recovery_blocked_generation": None, "recovery_blocked_until": None,
            "recovery_blocked_reason": None,
        })

    def reset_health(self, iid: str):
        iid = str(iid)
        self.health[iid] = {"fail_start": None, "retry_count": 0, "frozen_code": None,
                                 "frozen_reason": None, "last_state": None,
                                 "next_retry_at": None, "auto_retrying": False,
                                 "retry_delay": None,
                                 "recovery_blocked_generation": None,
                                 "recovery_blocked_until": None,
                                 "recovery_blocked_reason": None}
        self.status_cache.pop(iid, None)
        self.status_sampled_at.pop(iid, None)
        self.status_transitions.pop(iid, None)

    def status_epoch(self, iid: str) -> int:
        return int(self.status_runtime_epoch.get(str(iid), 0))

    def bump_status_epoch(self, iid: str) -> int:
        iid = str(iid)
        epoch = self.status_epoch(iid) + 1
        self.status_runtime_epoch[iid] = epoch
        return epoch

    def status_epoch_current(self, iid: str, epoch: int) -> bool:
        return self.status_epoch(str(iid)) == int(epoch)

    def status_publish_lock(self, iid: str) -> asyncio.Lock:
        iid = str(iid)
        if iid not in self.status_publish_locks:
            self.status_publish_locks[iid] = asyncio.Lock()
        return self.status_publish_locks[iid]

    async def drop_ami(self, iid: str):
        """Tear down and forget the AMI client for an instance. MUST be called whenever the
        engine container is stopped or recreated (stop/start/reprovision): the client's
        panoramisk Manager auto-reconnects forever, so a client left pointing at a removed or
        recreated container keeps dialing it — and if the new container has a different AMI
        secret (or the docker IP was reused by another line) it floods that Asterisk with
        'failed to authenticate' every few seconds. close() sets the client's closed flag which
        neutralises the pending reconnect."""
        c = self.ami.pop(str(iid), None)
        self.ami_generation.pop(str(iid), None)
        if c:
            await c.close()

    def recovery_lock(self, iid: str) -> asyncio.Lock:
        iid = str(iid)
        if iid not in self.engine_recovery_locks:
            self.engine_recovery_locks[iid] = asyncio.Lock()
        return self.engine_recovery_locks[iid]

    def lifecycle_epoch(self, iid: str) -> int:
        return int(self.engine_lifecycle_epoch.get(str(iid), 0))

    def bump_lifecycle_epoch(self, iid: str) -> int:
        iid = str(iid)
        epoch = self.lifecycle_epoch(iid) + 1
        self.engine_lifecycle_epoch[iid] = epoch
        return epoch

    async def runtime_changed(self, iid: str, runtime: dict, _action: str) -> None:
        """Retire stale runtime-derived status immediately and wake the sampler."""
        iid = str(iid)
        generation = runtime.get("container_id")
        transition = self._runtime_transition_status(iid, runtime, _action)
        drop_ami = (
            not runtime.get("running")
            or self.ami_generation.get(iid) not in (None, generation)
        )
        async with self.status_publish_lock(iid):
            self.bump_status_epoch(iid)
            self.status_cache.pop(iid, None)
            self.status_sampled_at.pop(iid, None)
            if transition:
                self.status_transitions[iid] = {
                    "status": transition,
                    "observed_at": time.monotonic(),
                }
            else:
                self.status_transitions.pop(iid, None)
            await self.broadcast({
                "type": "engine", "instance": iid, "event": "runtime_changed",
                "running": bool(runtime.get("running")),
                "generation": str(generation or ""),
                "engine_run_id": str(runtime.get("engine_run_id") or ""),
                "webrtc_host_port": runtime.get("webrtc_host_port"),
                **({"status_transition": transition} if transition else {}),
            })
            if transition:
                await self.broadcast({"type": "status", "instance": iid, **transition})
        if drop_ami:
            await self.drop_ami(iid)
        self.status_wakeup.set()

    def _runtime_transition_status(self, iid: str, runtime: dict, action: str) -> dict | None:
        inst = cfg.get_instance(str(iid))
        if not inst:
            return None
        detail = {
            "engine_action": str(action or ""),
            "engine_generation": str(runtime.get("container_id") or ""),
            "engine_running": bool(runtime.get("running")),
        }
        if not inst.get("enabled", True):
            return {
                "state": "STOPPED", "label": status_mod.LABELS["STOPPED"],
                "reason_code": "stopped", "reason": "Stopped.", "detail": detail,
            }
        if not runtime.get("running"):
            return {
                "state": "STOPPED", "label": status_mod.LABELS["STOPPED"],
                "reason_code": "engine_stopped",
                "reason": "The VoWiFi engine stopped; refreshing line status.",
                "detail": detail,
            }
        return {
            "state": "REGISTERING", "label": status_mod.LABELS["REGISTERING"],
            "reason_code": "engine_changed",
            "reason": "The VoWiFi engine changed; refreshing line status.",
            "detail": detail,
        }

    async def broadcast(self, msg: dict):
        dead = []
        for ws in list(self.clients):
            try:
                await ws.send_json(msg)
            except Exception:
                dead.append(ws)
        for ws in dead:
            self.clients.discard(ws)

    async def ami_for(self, iid: str, runtime: dict | None = None) -> AmiClient | None:
        iid = str(iid)
        # Serialise per-instance so concurrent callers (the 4s status_poller + API handlers) can't
        # each build a client and orphan the other's: an orphaned AmiClient is never close()d, so
        # its panoramisk Manager reconnects forever (flooding the engine's Asterisk with AMI auth
        # failures once a container reuses its docker IP).
        lock = self._ami_locks.setdefault(iid, asyncio.Lock())
        async with lock:
            inst = cfg.get_instance(iid)
            # Docker's API can pause for seconds when the daemon is busy. AMI discovery runs
            # in the background, but synchronous Docker calls here still froze every HTTP
            # request sharing this event loop.
            if runtime is None:
                runtime = await self.runtime.get(iid)
            running = bool(inst) and bool(runtime.get("running"))
            ip = runtime.get("ip") if running else None
            generation = runtime.get("container_id")
            client = self.ami.get(iid)
            # Reuse only a healthy client still pointed at the current container.
            if (client and running and ip and client.connected and client.host == ip
                    and self.ami_generation.get(iid) == generation):
                return client
            # Any other cached client is stale/unusable — drop it (close stops its reconnect loop)
            # so it can't linger and reconnect. This is the leak the old early-returns caused: they
            # returned None when the container was gone/IP-less WITHOUT closing the cached client.
            if client:
                await self.drop_ami(iid)
            if not running or not ip:
                return None
            client = AmiClient(iid, ip, 5038, inst.get("ami_user", "vowifi"),
                               inst["ami_secret"], realm=cfg.ims_realm(inst["mcc"], inst["mnc"]),
                               msisdn=inst.get("msisdn", ""), smsc=inst.get("smsc", ""))
            try:
                await client.connect()
            except BaseException:
                # asyncio cancellation is a BaseException.  A half-created panoramisk Manager
                # otherwise retains its scheduled reconnect while never entering our cache.
                await client.close()
                raise
            if not client.connected:
                await client.close()
                return None
            self.ami[iid] = client
            self.ami_generation[iid] = generation
            return client


hub = Hub()
vpcd_registry = vpcd_slots.VpcdSlotRegistry(
    os.path.join(cfg.DATA_DIR, "vpcd-slots.json"),
    max_slots=int(os.environ.get("MDD_VPCD_SLOTS", str(vpcd_slots.MAX_SLOTS))),
)
capability_lock = asyncio.Lock()
PCSC_MAINTENANCE_WINDOW_SECONDS = 45


def _engine_start_quarantine_detail(iid: str) -> dict:
    snapshot = engine.engine_start_quarantine_status(str(iid))
    valid = bool(snapshot and snapshot.get("valid"))
    return {
        "code": "engine_start_quarantined",
        "message": ((snapshot or {}).get("reason")
                    or "This line is protected from Engine startup."),
        "quarantined": True,
        "manual_required": True,
        "quarantine_valid": valid,
    }


def _raise_engine_start_quarantined(iid: str) -> None:
    if engine.engine_start_quarantine_pending(str(iid)):
        raise HTTPException(409, _engine_start_quarantine_detail(str(iid)))


@contextmanager
def _normal_start_permit_or_http(iid: str):
    try:
        with engine.normal_start_permit(str(iid)) as permit:
            yield permit
    except engine.EngineStartQuarantined as exc:
        raise HTTPException(409, _engine_start_quarantine_detail(str(iid))) from exc
    except engine.EngineLifecycleFenced as exc:
        raise HTTPException(409, {
            "code": "maintenance_in_progress",
            "message": "This line is fenced by a durable lifecycle transaction.",
        }) from exc


@contextmanager
def _card_probe_permit_or_http():
    try:
        with engine.card_probe_permits() as permit:
            yield permit
    except engine.EngineStartQuarantined as exc:
        raise HTTPException(409, {
            "code": "sim_identity_probe_quarantined",
            "message": "SIM identity probing is paused by an Engine start quarantine.",
            "blocked_instances": list(exc.blocked_iids),
            "manual_required": exc.state_unknown,
        }) from exc
    except engine.EngineLifecycleFenced as exc:
        raise HTTPException(409, {
            "code": "lifecycle_in_progress",
            "message": "SIM identity probing is owned by another lifecycle transaction.",
        }) from exc


@contextmanager
def _normal_delete_permit_or_http(iid: str):
    try:
        with engine.normal_delete_permit(str(iid)) as permit:
            yield permit
    except engine.EngineStartQuarantined as exc:
        raise HTTPException(409, _engine_start_quarantine_detail(str(iid))) from exc
    except engine.EngineLifecycleFenced as exc:
        raise HTTPException(409, {
            "code": "lifecycle_in_progress",
            "message": "This line is owned by another durable lifecycle transaction.",
        }) from exc


def _start_engine_checked(inst: dict, settings: dict, dev_mounts: bool = False,
                          reason: str = "manual", *, replace_existing: bool = True,
                          permit=None):
    """Translate fail-closed egress errors into an actionable API response."""
    iid = str(inst.get("id") or "")
    _raise_engine_start_quarantined(iid)
    if engine.global_maintenance_pending() or engine.engine_maintenance_pending(iid):
        raise HTTPException(409, {
            "code": "maintenance_in_progress",
            "message": "This line is fenced by a durable maintenance transaction.",
        })
    try:
        # A line follows its SIM; its device identity follows the physical modem/reader
        # currently holding that SIM. Refresh the rendered snapshot on every start.
        inst = _apply_current_hardware_imei(inst)
        # A restart begins a new healthy stretch; the old one says nothing about the exit
        # this container will end up using.
        hub.ok_since.pop(str(inst.get("id") or ""), None)
        starter = engine.start if replace_existing else engine.start_if_absent
        kwargs = {"dev_mounts": dev_mounts, "reason": reason}
        if permit is not None:
            kwargs["_permit"] = permit
        return starter(inst, settings, **kwargs)
    except engine.EngineStartQuarantined as exc:
        raise HTTPException(409, _engine_start_quarantine_detail(iid)) from exc
    except engine.EngineLifecycleFenced as exc:
        raise HTTPException(409, {
            "code": "maintenance_in_progress",
            "message": "This line is fenced by a durable maintenance transaction.",
        }) from exc
    except engine.EnginePortConflict as exc:
        if not cfg.instance_uses_auto_ports(inst):
            raise HTTPException(409, {
                "code": "port_conflict",
                "message": "A configured host port is already in use. Select Automatic port mapping or choose another port.",
            }) from exc
        try:
            ports = cfg.alloc_ports_auto(cfg.load(), exclude_iid=str(inst.get("id") or ""))
            inst = cfg.upsert_instance({"id": str(inst["id"]), "ports": ports,
                                        "port_mode": "auto"})
            log.warning("instance %s: moved automatic port block after host conflict",
                        inst.get("id"))
            starter = engine.start if replace_existing else engine.start_if_absent
            kwargs = {"dev_mounts": dev_mounts,
                      "reason": "automatic-port-recovery"}
            if permit is not None:
                kwargs["_permit"] = permit
            return starter(inst, settings, **kwargs)
        except engine.EngineStartQuarantined as retry_exc:
            raise HTTPException(409, _engine_start_quarantine_detail(iid)) from retry_exc
        except engine.EngineLifecycleFenced as retry_exc:
            raise HTTPException(409, {
                "code": "maintenance_in_progress",
                "message": "This line is fenced by a durable maintenance transaction.",
            }) from retry_exc
        except engine.EnginePortConflict as retry_exc:
            raise HTTPException(409, {
                "code": "port_conflict",
                "message": "No conflict-free host port block could be started.",
            }) from retry_exc
        except ValueError as retry_exc:
            raise HTTPException(409, {
                "code": "port_conflict", "message": str(retry_exc),
            }) from retry_exc
    except egress.EgressError as exc:
        raise HTTPException(503, {"code": "egress_unavailable", "message": str(exc)})


def _match_instance_by_iccid(iccid):
    if not iccid:
        return None
    for i in cfg.list_instances():
        if i.get("iccid") == iccid:
            return i
    return None


def _active_instance_with_iccid(iccid: str, exclude_iid: str = "") -> dict | None:
    """Find another active line for one exact SIM identity.

    Historical migrations may already contain duplicates, so this helper does not mutate or
    merge them.  New management writes and restores use it to stop the ambiguity growing.
    """
    wanted = str(iccid or "").strip()
    if not wanted:
        return None
    return next((item for item in cfg.list_instances()
                 if str(item.get("id") or "") != str(exclude_iid)
                 and str(item.get("iccid") or "").strip() == wanted), None)


def _random_svn() -> str:
    """Random 2-digit Software Version Number for an auto-derived IMEISV."""
    return f"{random.randint(0, 99):02d}"


def _find_running_by_reader(name: str):
    """The running instance whose pin_keeper reports using this reader NAME
    (pin_status.json "reader") — per-reader correct with multiple SIMs."""
    if not name:
        return None
    for i in cfg.list_instances():
        if not engine.is_running(str(i["id"])):
            continue
        ps = engine.read_run_json(str(i["id"]), "pin_status.json") or {}
        if ps.get("reader") == name:
            return i
    return None


def _is_placeholder_sim_identity(card_info: dict) -> bool:
    """Recognize blank-eUICC factory values, without rejecting real test profiles."""
    iccid = "".join(ch for ch in str(card_info.get("iccid") or "") if ch.isdigit())
    imsi = "".join(ch for ch in str(card_info.get("imsi") or "") if ch.isdigit())
    return (len(iccid) >= 10 and iccid.startswith("89") and len(set(iccid[2:])) == 1
            and len(imsi) >= 6 and len(set(imsi)) == 1)


def _reader_quarantine_candidates(name: str, info: dict) -> list[str]:
    """Return quarantined *expected* lines without treating reader history as SIM identity."""
    hints: set[str] = set()
    slot = vpcd_slots.slot_from_reader_name(name)
    record = next((item for item in vpcd_registry.snapshot()
                   if item.get("slot") == slot), {}) if slot is not None else {}
    previous = hub.cards.get(name) or {}
    instances = cfg.list_instances(include_deleted=True)
    by_iccid = {str(inst.get("iccid") or ""): str(inst["id"])
                for inst in instances if inst.get("iccid")}
    for source in (record, previous):
        matched = str(source.get("matched") or "")
        if matched:
            hints.add(matched)
        historical_iccid = str(source.get("iccid") or "")
        if historical_iccid in by_iccid:
            hints.add(by_iccid[historical_iccid])
    reader_port = str(info.get("reader_port") or "")
    if reader_port:
        hints.update(str(inst["id"]) for inst in instances
                     if str(inst.get("reader_port") or "") == reader_port)
    configured = {str(inst["id"]) for inst in instances}
    return sorted(iid for iid in hints if iid in configured)


def _publish_quarantined_card_unknown(name: str, info: dict,
                                      candidates: list[str], *, blocked_iids=None,
                                      state_unknown: bool = False,
                                      resume_attempted: bool = False,
                                      race_lost: bool = False) -> None:
    """Publish carrier/endpoint history only as history; never as a current SIM match."""
    vpcd_registry.begin_observation(name)
    slot = vpcd_slots.slot_from_reader_name(name)
    record = next((item for item in vpcd_registry.snapshot()
                   if item.get("slot") == slot), {}) if slot is not None else {}
    if blocked_iids is None:
        blocked = [iid for iid in candidates
                   if engine.engine_start_quarantine_pending(iid)]
    else:
        blocked = sorted({str(iid) for iid in blocked_iids if iid})
    active = [iid for iid in candidates if iid in set(blocked)]
    probe_blocked = bool(blocked or state_unknown)
    if blocked:
        hub.probe_quarantine_blockers.update(blocked)
    if state_unknown:
        hub.probe_quarantine_state_unknown = True
    info.update({
        "present": True,
        "card_presence": "present",
        "card_identity": "unknown",
        "identity_current": False,
        "matched": None,
        "iccid": None,
        "imsi": None,
        "mcc": None,
        "mnc": None,
        "mnc_len": None,
        "smsc": None,
        "quarantined": probe_blocked,
        "probe_deferred": True,
        "probe_blocked": probe_blocked,
        "probe_blocked_by_quarantines": blocked,
        "probe_blocked_state_unknown": bool(state_unknown),
        "probe_resume_armed": bool(blocked) and not resume_attempted,
        "probe_resume_attempted": bool(resume_attempted),
        "probe_race_lost": bool(race_lost),
        "quarantine_ambiguous": len(active) > 1,
        "quarantine_expected_instance": active[0] if len(active) == 1 else None,
        "quarantine_expected_instances": active if len(active) > 1 else [],
        "last_known_iccid": str(record.get("iccid") or ""),
        "last_known_matched": str(record.get("matched") or ""),
    })
    hub.cards[name] = info


async def _on_card_insert(name, idx, *, resumed_from_quarantine: bool = False):
    if _is_remote_vpcd_reader(str(name or "")):
        _clear_remote_loss_state(str(name))
    info = {"index": idx, "name": name, "present": True, "iccid": None,
            "pin_enabled": None, "pin_tries": None, "matched": None, "imsi": None,
            "mcc": None, "mnc": None, "mnc_len": None, "smsc": None,
            "carrier_identity": {}, "reader_port": None, "hardware_kind": "reader",
            "enumerated": True, "card_presence": "present"}
    # Resolve the STABLE physical USB port for this reader index (DIRECT connect, no APDU —
    # safe even if a running engine holds the card). This is the binding a line pins to, so it
    # survives pcscd re-enumerating two identical readers into a different order.
    try:
        info["reader_port"] = await asyncio.to_thread(usbreader.port_for_index, idx)
    except Exception as e:  # noqa
        log.debug("reader_port resolve failed for idx %s: %r", idx, e)
    quarantine_candidates = _reader_quarantine_candidates(str(name or ""), info)
    try:
        with engine.card_probe_permits() as probe_permit:
            observation_generation = None
            remote_observation_committed = False
            placeholder_identity = False
            inst = await asyncio.to_thread(_find_running_by_reader, name)
            if inst is not None:
                probe_permit.bind_actual([str(inst["id"])])
                info.update(iccid=inst.get("iccid"), imsi=inst.get("imsi"),
                            matched=inst["id"], smsc=inst.get("smsc"),
                            mcc=inst.get("mcc"), mnc=inst.get("mnc"),
                            mnc_len=inst.get("mnc_len"), identity_current=True,
                            carrier_identity=inst.get("carrier_identity") or {})
            elif hub.lpa_busy.get(name):
                prev = hub.cards.get(name) or {}
                previous_iid = str(prev.get("matched") or "")
                if previous_iid:
                    probe_permit.bind_actual([previous_iid])
                    info.update(iccid=prev.get("iccid"), imsi=prev.get("imsi"),
                                matched=previous_iid, smsc=prev.get("smsc"),
                                mcc=prev.get("mcc"), mnc=prev.get("mnc"),
                                mnc_len=prev.get("mnc_len"), identity_current=True,
                                carrier_identity=prev.get("carrier_identity") or {},
                                pin_enabled=prev.get("pin_enabled"),
                                pin_tries=prev.get("pin_tries"))
                log.info("card insert during LPA busy — skipping probe reader=%s", name)
            else:
                lock = hub.reader_lock(name)
                try:
                    await asyncio.wait_for(lock.acquire(), timeout=0.05)
                except asyncio.TimeoutError:
                    _publish_quarantined_card_unknown(
                        str(name), info, quarantine_candidates,
                        resume_attempted=resumed_from_quarantine)
                    log.debug("card probe skipped — reader lock busy: %s", name)
                    return
                try:
                    observation_generation = await asyncio.to_thread(
                        vpcd_registry.begin_observation, name)
                    c = await asyncio.to_thread(sim.read_card, idx)
                except Exception as exc:  # noqa
                    log.debug("card probe failed: %r", exc)
                    hub.cards[name] = info
                    return
                finally:
                    lock.release()
                info.update(iccid=c.iccid, pin_enabled=c.pin_enabled, pin_tries=c.pin_tries,
                            imsi=c.imsi, mcc=c.mcc, mnc=c.mnc,
                            mnc_len=getattr(c, "mnc_len", None), smsc=c.smsc,
                            spn=c.spn, profile_name=c.spn,
                            carrier_identity=_carrier_identity(c))
                placeholder_identity = _is_placeholder_sim_identity(info)
                if placeholder_identity:
                    log.info("blank eUICC placeholder identity ignored reader=%s", name)
                    info.update(identity_placeholder=True, iccid=None, imsi=None, mcc=None,
                                mnc=None, mnc_len=None, smsc=None, carrier_identity={})

                matches = ([] if placeholder_identity
                           else cfg.instances_by_iccid(str(info.get("iccid") or "")))
                match_iids = sorted({str(item["id"]) for item in matches})
                proposed_iid = None
                if match_iids:
                    probe_permit.bind_actual(match_iids)
                elif info.get("iccid") and not cfg.card_auto_create_suppressed(info["iccid"]):
                    proposed_iid = _next_instance_id()
                    probe_permit.bind_actual([proposed_iid], exclusive=True)

                # A remote probe result must win its generation CAS before any config/current
                # side effect, while the global+actual permit is still held.
                if observation_generation is not None:
                    remote_observation_committed = bool(await asyncio.to_thread(
                        vpcd_registry.observe_card, name, info,
                        expected_generation=observation_generation))
                    if not remote_observation_committed:
                        previous = hub.cards.get(name)
                        if previous is not None:
                            _mark_remote_card_unknown(previous, enumerated=True)
                        return

                if len(matches) > 1:
                    info.update(matched=None, identity_current=True,
                                identity_ambiguous=True,
                                identity_ambiguous_instances=match_iids)
                elif len(matches) == 1:
                    inst = matches[0]
                    info["matched"] = inst["id"]
                    info["identity_current"] = True
                    info["imsi"] = info["imsi"] or inst.get("imsi")
                    modem_identity = _modem_identity_for_reader(name)
                    update = {"id": str(inst["id"]), **_carrier_identity_update(info)}
                    if modem_identity:
                        logical = modem_identity.get("logical_channels") or []
                        swu_slot = next((int(item.get("slot")) for item in logical
                                         if item.get("role") == "swu"), 1)
                        try:
                            current_slot = int(str(name).rsplit(" ", 1)[-1])
                        except ValueError:
                            current_slot = -1
                        if current_slot == swu_slot:
                            update.update(reader_index=idx, reader_port="")
                    else:
                        update.update(reader_index=idx,
                                      reader_port=str(info.get("reader_port") or ""))
                    if any(inst.get(key) != value for key, value in update.items()
                           if key != "id"):
                        inst = await asyncio.to_thread(cfg.upsert_instance_unique_iccid, update)
                elif info.get("iccid") and cfg.card_auto_create_suppressed(info["iccid"]):
                    info["identity_current"] = True
                    log.info("deleted SIM remains inserted; automatic line creation is paused")
                elif info.get("iccid"):
                    if proposed_iid is None:
                        _publish_quarantined_card_unknown(
                            str(name), info, quarantine_candidates,
                            resume_attempted=resumed_from_quarantine, race_lost=True)
                        return
                    try:
                        inst = await asyncio.to_thread(
                            cfg.upsert_instance_unique_iccid,
                            _card_draft_record(info, proposed_iid),
                            require_iid_absent=True, unique_name=True)
                    except (cfg.InstanceIdentityConflict, cfg.InstanceIdConflict):
                        _publish_quarantined_card_unknown(
                            str(name), info, quarantine_candidates,
                            resume_attempted=resumed_from_quarantine, race_lost=True)
                        return
                    await asyncio.to_thread(egress.publish)
                    info.update(matched=inst["id"], identity_current=True)

            if remote_observation_committed:
                if not await asyncio.to_thread(
                        vpcd_registry.observe_card, name, info,
                        expected_generation=observation_generation):
                    previous = hub.cards.get(name)
                    if previous is not None:
                        _mark_remote_card_unknown(previous, enumerated=True)
                    return
            elif observation_generation is None:
                await asyncio.to_thread(vpcd_registry.observe_card, name, info)
            hub.cards[name] = info
            log.info("card inserted reader=%s (%s) identity=%s matched=%s", idx, name,
                     "available" if info["iccid"] else "unknown", info["matched"])
            if info.get("matched") and not info.get("identity_ambiguous"):
                asyncio.create_task(_auto_start_hotplugged_line(str(info["matched"])))
    except engine.EngineStartQuarantined as exc:
        _publish_quarantined_card_unknown(
            str(name), info, quarantine_candidates,
            blocked_iids=exc.blocked_iids, state_unknown=exc.state_unknown,
            resume_attempted=resumed_from_quarantine)
    except engine.EngineLifecycleFenced:
        _publish_quarantined_card_unknown(
            str(name), info, quarantine_candidates,
            resume_attempted=resumed_from_quarantine)


async def _auto_start_hotplugged_line(iid: str) -> None:
    """Start one enabled matched line after reader enumeration settles.

    A modem exposes the same SIM through several VPCD slots, so insert events arrive more
    than once. The per-line guard collapses them into one attempt; the normal health policy
    remains responsible for bounded registration retries after the container has started.
    """
    if iid in hub.hotplug_starts:
        return
    hub.hotplug_starts.add(iid)
    try:
        await asyncio.sleep(6)
        inst = cfg.get_instance(iid)
        if not inst or await asyncio.to_thread(engine.is_running, iid):
            return
        cards = hub.cards_list()
        card_info = next((item for item in cards if item.get("present")
                          and str(item.get("iccid") or "") == str(inst.get("iccid") or "")), None)
        if not card_info:
            return
        if (card_info.get("remote") and
                (card_info.get("connection_online") is not True or
                 card_info.get("identity_current") is not True or
                 str(card_info.get("identity_session_generation") or "") !=
                 str(card_info.get("session_generation") or ""))):
            return
        device_id, device_type = _device_for_card(card_info, cards)
        desired = device_state.desired()
        wanted = ((desired.get("devices") or {}).get(device_id)
                  or desired.get("defaults") or {})
        if not wanted.get("vowifi_enabled", True):
            return

        # A newly-seen SIM is intentionally persisted as a stopped draft while the card
        # monitor is still learning its identity. Once the settled hotplug snapshot has all
        # mandatory values, promote that same line automatically. This makes inserting a
        # modem/SIM a complete operation instead of leaving the user to discover and submit
        # the manual provisioning form. Only drafts are promoted here: a ready line that the
        # user explicitly disabled remains disabled.
        if inst.get("provisioning_state") == "draft":
            inst = await asyncio.to_thread(_auto_promote_card_draft, inst, card_info, cards)
            if inst.get("provisioning_state") == "draft":
                log.info("hotplug draft %s awaiting: %s", iid,
                         ", ".join(inst.get("auto_provision_missing") or []))
                return
        if not inst.get("enabled", True):
            return
        async with hub.recovery_lock(iid):
            # Re-read after waiting: another owner may have started or disabled the line.
            current = cfg.get_instance(iid)
            if not current or not current.get("enabled", True):
                return
            if await asyncio.to_thread(engine.is_running, iid):
                return
            await asyncio.to_thread(
                _start_engine_checked, current, cfg.get_settings(),
                os.environ.get("MDD_DEV_MOUNTS", "") == "1")
            hub.reset_health(iid)
        await hub.broadcast({"type": "engine", "instance": iid, "event": "hotplug_started",
                             "args": []})
    except Exception as exc:  # noqa
        log.warning("hotplug auto-start failed for %s: %s", iid, getattr(exc, "detail", exc))
    finally:
        hub.hotplug_starts.discard(iid)


def _line_auto_start_allowed(inst: dict) -> tuple[bool, str]:
    """Whether background maintenance may create this line's engine right now.

    A saved line is not proof that its SIM is inserted: offline lines deliberately remain in
    config so their history and settings survive.  Every non-interactive start/recovery must
    therefore re-check the live card monitor and the physical device's VoWiFi desired state.
    Explicit user starts keep their existing PIN/card preflight and actionable API errors.
    """
    if not inst.get("enabled", True):
        return False, "line_disabled"
    iid = str(inst.get("id") or "")
    if iid and engine.engine_start_quarantine_pending(iid):
        return False, "engine_start_quarantined"
    iccid = str(inst.get("iccid") or "")
    cards = hub.cards_list()
    card_info = next((item for item in cards if item.get("present") and (
        (iccid and str(item.get("iccid") or "") == iccid)
        or str(item.get("matched") or "") == iid)), None)
    if card_info is None:
        return False, "no_card"
    if (card_info.get("remote") and
            (card_info.get("connection_online") is not True
             or card_info.get("identity_current") is not True
             or str(card_info.get("identity_session_generation") or "") !=
                str(card_info.get("session_generation") or ""))):
        return False, "card_identity_unknown"
    device_id, _device_type = _device_for_card(card_info, cards)
    desired = device_state.desired()
    wanted = ((desired.get("devices") or {}).get(device_id)
              or desired.get("defaults") or {})
    if not wanted.get("vowifi_enabled", True):
        return False, "vowifi_disabled"
    return True, ""


def _auto_promote_card_draft(inst: dict, card_info: dict, cards: list[dict]) -> dict:
    """Promote a complete auto-created draft, or return it with missing-field hints.

    Hardware identity follows the physical reader/modem; SIM identity follows the ICCID.
    Keeping this as a synchronous helper makes the promotion rules independently testable.
    """
    if inst.get("provisioning_state") != "draft":
        return inst

    imsi = str(card_info.get("imsi") or inst.get("imsi") or "").strip()
    mcc = str(card_info.get("mcc") or inst.get("mcc") or (imsi[:3] if len(imsi) >= 3 else ""))
    mnc = str(card_info.get("mnc") or inst.get("mnc") or "")
    smsc = str(card_info.get("smsc") or inst.get("smsc") or "").strip()
    imei, hardware_id, _device_type = _hardware_imei_for_card(card_info, cards)
    missing = []
    if not imsi:
        missing.append("IMSI")
    if not mcc or not mnc:
        missing.append("MCC/MNC")
    if len(imei) != 15:
        missing.append("IMEI")
    if not smsc:
        missing.append("SMSC")
    if card_info.get("pin_enabled") is True and not inst.get("pin"):
        missing.append("SIM PIN")
    if missing:
        return {**inst, "auto_provision_missing": missing}

    previous_imeisv = str(inst.get("imeisv") or "")
    svn = (previous_imeisv[-2:] if len(previous_imeisv) == 16
           and previous_imeisv[-2:].isdigit() else _random_svn())
    sip = cfg.merge_carrier_sip_defaults(
        mcc, mnc, card_info.get("iccid") or imsi or imei, inst.get("sip"))
    # A draft normally arrives already named; only a name generated here needs deduplicating.
    resolved_iccid = str(card_info.get("iccid") or inst.get("iccid") or "")
    generated_name = not str(inst.get("name") or "").strip()
    update = {
        "id": str(inst["id"]),
        "name": (inst.get("name")
                 or cfg.default_instance_name(mcc, mnc, resolved_iccid)),
        "provisioning_state": "ready",
        "enabled": True,
        "imsi": imsi,
        "mcc": mcc,
        "mnc": mnc,
        **_carrier_identity_update(card_info),
        "iccid": str(card_info.get("iccid") or inst.get("iccid") or ""),
        "smsc": smsc,
        "imei": imei,
        "imei_source_device_id": hardware_id,
        "imeisv": cfg.imeisv_from_imei(imei, svn=svn),
        "reader": f"imsi:{imsi}",
        "sip": sip,
        # Production logs must not expose IMS-AKA material through Asterisk debug output.
        "debug": {**(inst.get("debug") or {}), "asterisk": False},
    }
    virtual = card_info.get("virtual_slots") or []
    if virtual:
        def slot(pos: int) -> dict:
            return virtual[min(pos, len(virtual) - 1)]

        update.update({
            "pin_reader": slot(0).get("name") or str(slot(0).get("index", 0)),
            "swu_reader": slot(1).get("name") or str(slot(1).get("index", 0)),
            "ami_reader": slot(2).get("name") or str(slot(2).get("index", 0)),
            "reader_index": int(slot(1).get("index") or card_info.get("index") or 0),
            "reader_port": "",
        })
    else:
        update.update({
            "reader_index": int(card_info.get("index") or inst.get("reader_index") or 0),
            "reader_port": str(card_info.get("reader_port") or inst.get("reader_port") or ""),
        })
    promoted = cfg.upsert_instance(update, unique_name=generated_name)
    egress.publish()
    log.info("hotplug draft %s auto-provisioned for MCC %s", inst["id"], mcc)
    return promoted


async def _on_card_remove(entry: dict, reader_unplugged: bool = False,
                          remote_evidence_key: tuple | None = None) -> bool:
    """Card pulled from a reader, or (reader_unplugged) the whole reader disconnected.
    Stops the SIP engine container serving that card. The entry must be the reader's
    LAST-KNOWN state (name/matched/iccid) — the caller must not blank it first.
    Returns True when a running line was stopped."""
    # Closing either HTTP listener disconnects its VPCD WebSockets before FastAPI lifespan
    # shutdown runs.  That transport loss is not a physical card removal and must never delete
    # Engine containers during a Control restart.
    if control_lifecycle.shutdown_started():
        return False
    name, idx = entry.get("name", ""), entry.get("index")
    matched, iccid = entry.get("matched"), entry.get("iccid")
    remote_authorized = remote_evidence_key is not None
    if remote_authorized and not await _remote_loss_key_current(name, remote_evidence_key):
        return False
    # Native PC/SC removal remains the direct physical authority it was before D1. Remote
    # VPCD loss is committed only after exact Engine containment succeeds below; otherwise a
    # transient transport loss could erase the last identity or clear the recovery fence.
    if not remote_authorized:
        if iccid:
            await asyncio.to_thread(cfg.unsuppress_card, iccid)
        if not reader_unplugged:
            hub.cards[name] = {"index": idx, "name": name, "present": False, "iccid": None,
                               "matched": None, "imsi": None, "pin_enabled": None,
                               "pin_tries": None}
    log.info("%s reader=%s (%s) (identity=%s matched=%s)",
             "reader unplugged" if reader_unplugged else "card removed",
             idx, name, "available" if iccid else "unknown", matched)
    target = None
    if matched:
        target = cfg.get_instance(matched)
    if target is None and iccid:
        target = _match_instance_by_iccid(iccid)
    if target is None:
        # Unknown/unmatched identity: map by the reader NAME the running engine reports
        # using (pin_status.json). This is the only safe fallback — guessing "the single
        # running instance" could stop a healthy line on ANOTHER reader.
        target = await asyncio.to_thread(_find_running_by_reader, name)
    # Card removal is an explicit terminal condition for the current run. Cancel a frozen
    # cooldown even when its container was already removed: otherwise that in-memory recovery
    # timer can recreate an engine minutes after the SIM disappeared.
    if target:
        target_iid = str(target["id"])
        async with hub.recovery_lock(target_iid):
            # Shutdown can begin while this removal waits behind an in-flight recovery action.
            # Recheck inside the mutation boundary before touching health, AMI or Docker state.
            if control_lifecycle.shutdown_started():
                return False
            running = await asyncio.to_thread(engine.is_running, target_iid)
            # The target lookup and recovery-lock wait can both yield. Recompute the complete
            # health-session/VPCD-generation evidence immediately before the destructive
            # mutation, inside the same per-line recovery boundary.
            if remote_authorized and not await _remote_loss_key_current(
                    name, remote_evidence_key):
                return False
            hub.reset_health(target_iid)
            stopped = False
            # The durable scoped card-loss intent must be published even in the replacement
            # source-removed window where no Docker container exists. Running state only
            # determines whether exact containment has work to do.
            outcome = await asyncio.to_thread(
                engine.stop_for_card_loss, target_iid, target, {
                    "reason": "reader_unplugged" if reader_unplugged else "card_removed",
                    "reader_name": name,
                    "reader_index": idx if type(idx) is int else -1,
                    "iccid": str(iccid or ""),
                })
            stopped = bool(outcome.get("stopped"))
            if stopped:
                await hub.drop_ami(target_iid)
            elif running:
                log.error("physical card loss did not prove Engine containment for %s: %s",
                          target_iid, outcome)
    else:
        running = False
        stopped = False
        outcome = {}
    remote_missing_proven = bool(
        remote_authorized and target and not running
        and outcome.get("status") == "missing")
    contained = bool(target and (
        (stopped and (running or remote_authorized)) or remote_missing_proven))
    if contained:
        if remote_authorized:
            if iccid:
                try:
                    await asyncio.to_thread(cfg.unsuppress_card, iccid)
                except Exception as exc:  # noqa
                    # Engine containment is already durable. Keep reporting the cleanup error,
                    # but do not resurrect the stopped paid path or misreport containment.
                    log.warning("could not clear removed-card suppression for %s: %s",
                                iccid, exc)
            if not reader_unplugged:
                hub.cards[name] = {
                    "index": idx, "name": name, "present": False, "enumerated": True,
                    "card_presence": "absent", "iccid": None, "matched": None,
                    "imsi": None, "pin_enabled": None, "pin_tries": None,
                    "remote": True, "connection_online": True,
                }
        await hub.broadcast({"type": "engine", "instance": target["id"],
                             "event": "reader_lost" if reader_unplugged else "card_removed",
                             "args": [name]})
        stopped_status = {"state": "NO_CARD",
                          "label": "Reader unplugged" if reader_unplugged
                                   else "No SIM card (removed)",
                          "reason_code": "no_card", "reason": "SIM card is not available.",
                          "detail": {}}
        stopped_status = _with_status_activity(str(target["id"]), stopped_status)
        hub.status_cache[str(target["id"])] = stopped_status
        hub.status_sampled_at[str(target["id"])] = time.monotonic()
        await hub.broadcast({"type": "status", "instance": str(target["id"]),
                             **stopped_status})
        return True
    return False


def _is_remote_vpcd_reader(name: str) -> bool:
    """Whether a PC/SC row is one of the server's remote VPCD transports."""
    return vpcd_slots.slot_from_reader_name(name) is not None


def _mark_remote_card_unknown(entry: dict, *, enumerated: bool) -> None:
    """Record loss of remote evidence without inventing a physical card removal.

    VPCD presents a remote transport as a local PC/SC reader.  Either a WSS outage or a
    legal empty ATR can make pcsc-lite report the card absent, so that observation alone is
    not destructive authority.  Keep the last identity for reconnect/history while making
    the live presence explicitly unknown and ineligible for background auto-start.
    """
    slot = vpcd_slots.slot_from_reader_name(entry.get("name"))
    record = next((item for item in vpcd_registry.snapshot()
                   if item.get("slot") == slot), {})
    entry.update({
        "present": False,
        "enumerated": bool(enumerated),
        "card_presence": "unknown",
        "transport_state": "unknown" if record.get("online") else "unreachable",
        "connection_online": bool(record.get("online")),
    })


REMOTE_CARD_LOSS_STABLE_SECONDS = float(
    os.environ.get("MDD_REMOTE_CARD_LOSS_STABLE_SECONDS", "3"))


def _clear_remote_loss_state(name: str) -> None:
    hub.remote_loss_candidates.pop(str(name), None)
    hub.remote_loss_inflight = {
        key for key in hub.remote_loss_inflight if len(key) < 2 or key[1] != str(name)
    }
    hub.remote_loss_completed = {
        key for key in hub.remote_loss_completed if len(key) < 2 or key[1] != str(name)
    }


def _remote_card_absence_confirmed(name: str) -> bool:
    """Whether this VPCD generation already completed authoritative card containment."""
    slot = vpcd_slots.slot_from_reader_name(str(name or ""))
    record = next((item for item in vpcd_registry.snapshot()
                   if item.get("slot") == slot), None)
    generation = str((record or {}).get("session_generation") or "")
    return bool(generation and any(
        len(key) >= 7 and key[0] == "card" and key[1] == str(name)
        and str(key[6]) == generation for key in hub.remote_loss_completed))


async def _remote_loss_evidence_key(name: str) -> tuple | None:
    """Return the exact current destructive-authority generation, or fail closed."""
    name = str(name or "")
    entry = hub.cards.get(name)
    slot = vpcd_slots.slot_from_reader_name(name)
    record = next((item for item in vpcd_registry.snapshot()
                   if item.get("slot") == slot), None)
    if not entry or slot is None or not record:
        return None
    agent_id = str(record.get("agent_id") or "")
    run_id = str(record.get("agent_run_id") or "")
    reader_id = str(record.get("reader_id") or "")
    session_generation = str(record.get("session_generation") or "")
    if not all((agent_id, run_id, reader_id, session_generation)):
        return None
    authority = await agent_health_registry.reader_authority(agent_id, run_id)
    if authority is None:
        return None
    health_session_id = str(authority.get("session_id") or "")
    if not health_session_id:
        return None
    pcsc = authority["pcsc"]
    readers = {str(item.get("reader_id") or ""): item
               for item in (pcsc.get("readers") or [])}
    for observed_reader_id in readers:
        hub.remote_reader_seen.add(
            (agent_id, run_id, health_session_id, observed_reader_id))
    health_reader = readers.get(reader_id)
    enumerated = entry.get("enumerated") is not False
    if enumerated:
        kind = "card"
        evidence = bool(
            record.get("online") is True
            and record.get("identity_current") is True
            and str(record.get("identity_session_generation") or "") == session_generation
            and health_reader is not None
            and health_reader.get("card_present") is False
            and entry.get("present") is False)
    else:
        kind = "reader"
        # All-reader disappearance and single-reader hosts remain unknown until an OS-level
        # PnP authority is added.  One absent reader is usable only while a peer reader in the
        # same successful discovery generation remains present in inventory.
        evidence = bool(
            record.get("online") is False
            and health_reader is None
            and len(readers) >= 1
            and (agent_id, run_id, health_session_id, reader_id)
                in hub.remote_reader_seen
            and str(record.get("identity_session_generation") or "") == session_generation)
    key = (kind, name, agent_id, run_id, health_session_id, reader_id, session_generation,
           int(pcsc.get("generation") or 0))
    return key if evidence else None


async def _remote_loss_key_current(name: str, expected: tuple) -> bool:
    """Fence a queued destructive action against every live authority generation."""
    return await _remote_loss_evidence_key(name) == expected


async def _reconcile_remote_card_evidence(name: str) -> bool:
    """Join live health-v2 and VPCD evidence before invoking physical card loss.

    Every non-authoritative or conflicting combination remains ``unknown``.  A single-reader
    host cannot yet prove a physical USB-reader unplug without OS PnP evidence, so only a card
    removal within a still-live reader, or one missing reader while another remains stable,
    can become destructive in this batch.
    """
    name = str(name or "")
    entry = hub.cards.get(name)
    key = await _remote_loss_evidence_key(name)
    if entry is None or key is None:
        hub.remote_loss_candidates.pop(name, None)
        return False
    if key in hub.remote_loss_completed:
        return False
    if key in hub.remote_loss_inflight:
        return False
    now = time.monotonic()
    candidate = hub.remote_loss_candidates.get(name)
    if not candidate or candidate.get("key") != key:
        hub.remote_loss_candidates[name] = {"key": key, "since": now}
        return False
    if now - float(candidate.get("since") or now) < max(
            0.0, REMOTE_CARD_LOSS_STABLE_SECONDS):
        return False
    # In-flight and completed are deliberately separate: a failed/rejected stop is not a
    # terminal physical-removal fact and must remain unknown for a later fresh observation.
    hub.remote_loss_inflight.add(key)
    hub.remote_loss_candidates.pop(name, None)
    kind = str(key[0])
    session_generation = str(key[6])
    try:
        stopped = await _on_card_remove(
            entry, reader_unplugged=(kind == "reader"), remote_evidence_key=key)
    finally:
        hub.remote_loss_inflight.discard(key)
    if not stopped:
        return False
    hub.remote_loss_completed.add(key)
    if kind == "card":
        await asyncio.to_thread(vpcd_registry.confirm_card_absent,
                                name, session_generation)
    return stopped


async def _reconcile_all_remote_card_evidence() -> None:
    for name in list(hub.cards):
        if _is_remote_vpcd_reader(name):
            await _reconcile_remote_card_evidence(name)


async def _handle_reader_disappearance(name: str, entry: dict) -> tuple[bool, bool]:
    """Return ``(line_stopped, remote_unknown)`` for one missing reader row."""
    if _is_remote_vpcd_reader(name):
        _mark_remote_card_unknown(entry, enumerated=False)
        return await _reconcile_remote_card_evidence(name), True
    hub.cards.pop(name, None)
    return await _on_card_remove(entry, reader_unplugged=True), False


async def _handle_card_absence(entry: dict) -> bool:
    """Contain a remote ambiguity or execute the native exact card-loss path."""
    if _is_remote_vpcd_reader(str(entry.get("name") or "")):
        _mark_remote_card_unknown(entry, enumerated=True)
        return await _reconcile_remote_card_evidence(str(entry.get("name") or ""))
    return await _on_card_remove(entry)


def _sanitize_card_for_probe_quarantine(name: str, st: dict, entry: dict,
                                        global_blockers: list[str],
                                        state_unknown: bool) -> bool:
    """Turn stale current identity into an honest no-APDU blocked observation."""
    if not st.get("present"):
        return False
    expected = _reader_quarantine_candidates(name, {**entry, **st})
    exact_blockers = [iid for iid in expected
                      if engine.engine_start_quarantine_pending(iid)]
    current_known = bool(entry.get("matched") or entry.get("iccid")) \
        and entry.get("identity_current") is not False
    if not (state_unknown or (exact_blockers and current_known)
            or (global_blockers and not current_known)):
        return False
    observed = sorted(set(global_blockers + exact_blockers))
    hub.probe_quarantine_blockers.update(observed)
    _publish_quarantined_card_unknown(
        name, {**entry, **st}, expected, blocked_iids=observed,
        state_unknown=state_unknown)
    return True


def _consume_probe_resume(entry: dict, *, state_unknown: bool) -> bool:
    """Arm exactly one monitor retry after every exact blocker has been released."""
    blockers = [str(iid) for iid in
                (entry.get("probe_blocked_by_quarantines") or []) if iid]
    if (not entry.get("probe_deferred") or not entry.get("probe_resume_armed")
            or entry.get("probe_resume_attempted") or state_unknown
            or any(engine.engine_start_quarantine_pending(iid) for iid in blockers)):
        return False
    entry["probe_resume_armed"] = False
    entry["probe_resume_attempted"] = True
    return True


async def card_monitor():
    """Real-time monitor for BOTH reader hotplug (plug/unplug) and card insert/remove.
    State is keyed by reader NAME: PC/SC indices shift when a reader is unplugged, so
    names are the stable identity; each entry's `index` field is refreshed every scan for
    the API calls that take reader_index. Between scans it blocks in
    card.wait_for_change (PnP-aware SCardGetStatusChange), so hotplug is reflected
    near-instantly without hammering pcscd."""
    first = True
    while True:
        try:
            states = await asyncio.to_thread(card.reader_states)
            if states is None:
                # Transient PC/SC error (pcscd restarting?) — NOT "all readers gone".
                # Skip this cycle; keep known state and engines untouched.
                log.debug("card monitor: PC/SC unavailable, skipping scan")
                await asyncio.sleep(1.2)
                continue
            current = {st["name"]: st for st in states}
            changed = False

            # The host orchestrator briefly restarts pcscd after changing generated VPCD reader
            # stanzas.  Treat that explicit maintenance window as enumeration churn, not as a
            # physical unplug, so healthy engine containers are not stopped.
            maintenance = False
            marker = os.path.join(cfg.DATA_DIR, "orchestrator", "pcsc-maintenance")
            try:
                # Rebuilding sing-box, ModemManager ownership and all virtual readers can take
                # more than 15 seconds on a Pi. Keep this comfortably above the observed full
                # orchestrator restart time so planned churn cannot be mistaken for an unplug.
                maintenance = (time.time() - os.path.getmtime(marker)
                               < PCSC_MAINTENANCE_WINDOW_SECONDS)
            except OSError:
                pass
            if maintenance:
                await asyncio.sleep(0.5)
                continue

            # Once a trustworthy blocker snapshot exists, presence checks avoid repeatedly
            # taking global SH while Host release is waiting for bounded EX. A clean release
            # removes those exact marker paths; the next cycle clears the cache and arms one
            # normal probe. Corrupt/unenumerable state stays manual-required.
            if hub.probe_quarantine_blockers:
                hub.probe_quarantine_blockers = {
                    iid for iid in hub.probe_quarantine_blockers
                    if engine.engine_start_quarantine_pending(iid)}
            elif not hub.probe_quarantine_state_unknown:
                try:
                    hub.probe_quarantine_blockers.update(
                        await asyncio.to_thread(engine.active_engine_start_quarantines))
                except engine.EngineStartQuarantined as exc:
                    hub.probe_quarantine_blockers.update(exc.blocked_iids)
                    hub.probe_quarantine_state_unknown = exc.state_unknown
                except engine.EngineLifecycleFenced:
                    # Host owns the linearization point; the completed marker is observed next
                    # cycle. Do not infer absence while its EX transaction is in flight.
                    pass
            global_probe_blockers = sorted(hub.probe_quarantine_blockers)

            # A remote VPCD reader disappearing proves only transport loss.  Keep the row and
            # identity until Agent-health authority can distinguish an actual USB unplug.
            # Native reader disappearance remains an exact physical event.
            for name in [n for n in hub.cards if n not in current]:
                entry = hub.cards[name]
                stopped, remote_unknown = await _handle_reader_disappearance(name, entry)
                if remote_unknown:
                    changed = True
                    continue
                if not stopped:
                    # _on_card_remove already broadcast the (more informative)
                    # "reader_lost — line stopped" event; only emit the generic one
                    # when no line was affected, so the UI shows a single toast.
                    await hub.broadcast({"type": "engine", "instance": "",
                                         "event": "reader_removed", "args": [name]})
                changed = True

            for name, st in current.items():
                entry = hub.cards.get(name)
                if entry is not None and _sanitize_card_for_probe_quarantine(
                        name, st, entry, global_probe_blockers,
                        hub.probe_quarantine_state_unknown):
                    changed = True
                    continue
                # LPA holds the reader exclusively and enable/disable triggers REFRESH
                # (looks like remove+insert). Keep last-known state; skip insert/remove.
                if hub.lpa_busy.get(name):
                    if entry is None:
                        hub.cards[name] = {**st, "iccid": None, "matched": None,
                                           "imsi": None, "pin_enabled": None,
                                           "pin_tries": None}
                        changed = True
                    elif entry.get("index") != st["index"]:
                        entry["index"] = st["index"]
                        changed = True
                    continue
                if entry is None:
                    # reader newly plugged in (or first scan after manager start)
                    if not first:
                        log.info("reader plugged in: %s", name)
                        await hub.broadcast({"type": "engine", "instance": "",
                                             "event": "reader_added", "args": [name]})
                    if st["present"]:
                        await _on_card_insert(name, st["index"])
                    else:
                        hub.cards[name] = {**st, "iccid": None, "matched": None,
                                           "imsi": None, "pin_enabled": None,
                                           "pin_tries": None}
                    changed = True
                    continue
                if entry.get("index") != st["index"]:
                    entry["index"] = st["index"]     # indices shift on unplug
                    # The physical reader behind this name/index may have changed — refresh the
                    # stable USB port binding so the display + ICCID->port learning stay correct.
                    try:
                        entry["reader_port"] = await asyncio.to_thread(
                            usbreader.port_for_index, st["index"])
                    except Exception:  # noqa
                        pass
                    changed = True
                if entry.get("probe_deferred") and st["present"]:
                    if _consume_probe_resume(
                            entry, state_unknown=hub.probe_quarantine_state_unknown):
                        # Exactly one ordinary cycle after release. A busy reader or failed APDU
                        # records attempted=true and waits for a physical event/explicit refresh.
                        await _on_card_insert(
                            name, st["index"], resumed_from_quarantine=True)
                        changed = True
                        continue
                if bool(entry.get("present")) != st["present"]:
                    # eUICC REFRESH during LPA looks like remove+insert — keep last-known
                    # state and do not stop engines / probe until the LPA op finishes.
                    if hub.lpa_busy.get(name):
                        entry["present"] = st["present"]
                        changed = True
                        continue
                    if st["present"]:
                        await _on_card_insert(name, st["index"])
                    else:
                        await _handle_card_absence(entry)
                    changed = True
                elif _is_remote_vpcd_reader(name) and not st["present"]:
                    # Recompute the health/VPCD join on later successful scans so the stable
                    # window can complete regardless of which transport reported first.
                    if not _remote_card_absence_confirmed(name):
                        _mark_remote_card_unknown(entry, enumerated=True)
                    await _reconcile_remote_card_evidence(name)
            # The first completed scan is always announced, even when it found nothing:
            # it is what turns the UI's "detecting devices" state into a real answer.
            if changed or first:
                await hub.broadcast({"type": "cards", "cards": _client_cards()})
            # Only a completed scan counts: a failed first scan must retry as "first"
            # (readers seen later may belong to already-running engines).
            hub.scanned = True
            first = False
        except Exception as e:  # noqa
            log.debug("card monitor error: %r", e)
        # Instant wake on any reader/card change; the timeout bounds the worst case for
        # changes that slip between a scan and the next wait (fresh-snapshot window).
        # The short sleep bounds the rescan rate if something reports changes endlessly.
        await asyncio.to_thread(card.wait_for_change, 2.5)
        await asyncio.sleep(0.25)


async def sync_modem_msisdns():
    """Fill empty line numbers from a modem provider, gated by the current SIM ICCID.

    OwnNumbers can lag behind a physical SIM swap on some modems. Requiring both a
    non-empty current SIM ICCID and an exact configured-line match prevents a stale
    modem value from being assigned to whichever line happens to use the device.
    """
    candidates: list[tuple[str, str, str]] = []
    observed = device_state.status().get("devices") or {}
    for device in observed.values():
        cellular = (device or {}).get("cellular") or {}
        candidates.append((str(cellular.get("sim_iccid") or "").strip(),
                           str(cellular.get("msisdn") or "").strip(),
                           "modemmanager"))
    # Remote Windows/macOS/Linux Agents publish the same facts through the modem registry,
    # not the host-orchestrator device-state document.  The attachment ICCID is the stable
    # identity; agent_id, modem_id, COM port and slot are deliberately not used for matching.
    for modem in modem_registry.list():
        if not modem.get("online"):
            continue
        candidates.append((str(modem.get("iccid") or "").strip(),
                           str(modem.get("phone") or "").strip(),
                           "modem-provider"))
    for sim_iccid, msisdn, source in candidates:
        if not msisdn or not sim_iccid:
            continue
        inst = _match_instance_by_iccid(sim_iccid)
        if not inst or inst.get("msisdn"):
            continue
        iid = str(inst["id"])
        cfg.upsert_instance({"id": iid, "msisdn": msisdn,
                             "msisdn_source": source})
        client = hub.ami.get(iid)
        if client:
            client.msisdn = msisdn
        log.info("learned line number from %s for instance %s", source, iid)
        await hub.broadcast({"type": "engine", "instance": iid,
                             "event": "msisdn_updated", "args": []})


def _line_state_kind(st: dict) -> str | None:
    """Map the status machine onto the states the connectivity timeline records.

    Returns None when a sample carries no evidence about connectivity, which must not be
    written as a disconnect. compute() only reaches REGISTERING after the tunnel is
    installed, so a registration of "unknown" there means the read itself failed — the
    management timeout this codebase already refuses to treat as a carrier failure. Charting
    it as an outage makes a healthy line look like it keeps dropping.
    """
    state = str((st or {}).get("state") or "").upper()
    if state == "OK":
        return "up"
    if state == "STOPPED":
        return "off"
    detail = (st or {}).get("detail") or {}
    if state == "REGISTERING" and str(detail.get("registration") or "unknown").lower() == "unknown":
        return None
    return "down"


def _outage_detail(st: dict) -> str:
    """Compact structured evidence behind a disconnect's reason code.

    The database keeps this JSON as text for schema compatibility. The WebUI localises its
    evidence code into a short "which side failed to answer what" sentence; older free-form
    rows still render unchanged.
    """
    code = str((st or {}).get("reason_code") or "")
    detail = (st or {}).get("detail") or {}
    fqdn = str(detail.get("epdg_fqdn") or "")
    def evidence(evidence_code: str, **values) -> str:
        return json.dumps({"code": evidence_code,
                          **{key: value for key, value in values.items() if value not in (None, "", [])}},
                         ensure_ascii=False, separators=(",", ":"))

    if code == "epdg_unresolved":
        return evidence("client_dns_unresolved", peer=fqdn,
                        servers=[str(s) for s in (detail.get("nameservers") or [])])
    if code == "tunnel_child_rekey_timeout":
        return evidence("server_epdg_child_rekey_unanswered", peer=fqdn)
    if code == "tunnel_ike_rekey_timeout":
        return evidence("server_epdg_ike_rekey_unanswered", peer=fqdn)
    if code == "tunnel_rekey_send_error":
        return evidence("client_rekey_send_failed", peer=fqdn)
    if code == "tunnel_network":
        return evidence("server_epdg_ike_unanswered", peer=fqdn)
    if code == "tunnel_sim_auth":
        return evidence("client_sim_auth_failed", peer=fqdn)
    if code == "tunnel_not_authorized":
        return evidence("server_epdg_identity_rejected", peer=fqdn)
    if code == "tunnel_proposal":
        return evidence("server_epdg_proposal_rejected", peer=fqdn)
    if code == "tunnel_setup":
        # CONNECTING is a recovery state, not a root cause. If no timeout, rejection or
        # local send failure has appeared yet, say the earlier fault was not captured
        # instead of presenting the rebuild itself as the reason for the outage.
        return evidence("tunnel_cause_not_captured", peer=fqdn)
    if code == "reg_unanswered":
        return evidence("server_pcscf_register_unanswered", peer=detail.get("pcscf"))
    if code == "reg_temporary":
        return evidence("server_pcscf_sip_temporary", peer=detail.get("pcscf"),
                        status=detail.get("sip_status"),
                        retry_after=detail.get("retry_after_seconds"))
    if code in {"reg_rejected", "reg_reauth_failed"}:
        return evidence("server_pcscf_sip_rejected", peer=detail.get("pcscf"),
                        status=detail.get("sip_status"))
    if code == "registering":
        return evidence("client_registration_incomplete", peer=detail.get("pcscf"))
    if code == "maintenance_rebuild":
        return evidence("client_maintenance_rebuild")
    if code == "client_engine_failure":
        return evidence("client_engine_worker_failed")
    return ""


async def _record_line_state(iid: str, st: dict) -> None:
    """Persist one observation, skipping writes that would only repeat the known state.

    Status is sampled every few seconds; committing that to SQLite unchanged would be a
    constant write load on the SD card an appliance boots from. A transition is always
    written immediately — it is the event the timeline exists to show — and an unchanged
    state is refreshed often enough to stay well inside the segment continuity window.
    """
    iid = str(iid)
    kind = _line_state_kind(st)
    if kind is None:
        # Leave the timeline untouched. A brief blind spot is absorbed by the segment
        # continuity window; a long one falls outside it and surfaces as `unknown`, which is
        # exactly what the record can honestly claim. Forget the last write so the next real
        # observation is committed immediately rather than waiting out the refresh interval.
        _line_state_written.pop(iid, None)
        return
    previous = _line_state_written.get(iid)
    if (previous and previous[0] == kind
            and time.monotonic() - previous[1] < LINE_STATE_WRITE_INTERVAL_SECONDS):
        return
    _line_state_written[iid] = (kind, time.monotonic())
    # Only a disconnect needs explaining. The first down sample carries the cause; the store
    # keeps it for the whole segment.
    reason = str((st or {}).get("reason_code") or "") if kind == "down" else ""
    try:
        await asyncio.to_thread(store.record_line_state, iid, kind, reason=reason,
                                detail=_outage_detail(st) if reason else "")
    except Exception as exc:  # noqa
        # History is diagnostic only; never let it interrupt status sampling.
        _line_state_written.pop(iid, None)
        log.debug("line state record failed instance=%s: %r", iid, exc)


def _status_poll_delay(instances: list[dict]) -> float:
    """Fast only while a running line is actively converging.

    Enabled lines can legitimately have no container while their reader is absent. PC/SC and
    Docker events wake those paths immediately, so treating STOPPED/NO_CARD as perpetually busy
    kept the whole gateway on the four-second cadence for no useful reason.
    """
    active = []
    for inst in instances:
        if not inst.get("enabled", True):
            continue
        active.append((hub.status_cache.get(str(inst["id"])) or {}).get("state"))
    return (STATUS_POLL_FAST_SECONDS
            if any(state in {"REGISTERING", "TUNNEL_DOWN"} for state in active)
            else STATUS_POLL_HEALTHY_SECONDS)


async def status_poller():
    last_prune = 0.0
    while True:
        # Clear before sampling: an event arriving during the work remains set and causes an
        # immediate next pass instead of being lost between the sample and the wait.
        hub.status_wakeup.clear()
        instances = []
        try:
            instances = cfg.list_instances()
            await sync_modem_msisdns()
            await asyncio.gather(*(_poll_instance_status(inst)
                                   for inst in instances))
            if time.monotonic() - last_prune >= LINE_HISTORY_PRUNE_INTERVAL_SECONDS:
                last_prune = time.monotonic()
                await asyncio.to_thread(store.prune_line_states,
                                        int(time.time()) - store.LINE_STATE_RETENTION_SECONDS)
        except Exception as e:  # noqa
            log.debug("poller error: %r", e)
        try:
            await asyncio.wait_for(hub.status_wakeup.wait(), timeout=_status_poll_delay(instances))
        except asyncio.TimeoutError:
            pass


HOST_ALERT_POLL_SECONDS = 60.0
# Re-announce a condition that simply persists, rather than staying silent for days.
HOST_ALERT_REPEAT_SECONDS = float(os.environ.get("MDD_HOST_ALERT_REPEAT", "21600"))
# A condition has to stay absent this long before it counts as recovered. Without it, a
# measurement sitting near its threshold crosses back and forth all day and each re-entry
# looks like a new problem worth notifying about.
HOST_ALERT_CLEAR_SECONDS = float(os.environ.get("MDD_HOST_ALERT_CLEAR", "1800"))
ALLOWANCE_REMINDER_POLL_SECONDS = float(
    os.environ.get("MDD_ALLOWANCE_REMINDER_POLL", "3600"))

HOST_ALERT_TEXT = {
    "undervoltage_now": "供电电压不足（正在发生）。网口、蜂窝模块和读卡器共用同一路供电，"
                        "欠压会让所有线路同时掉线。请更换更强的电源或为 USB 设备加独立供电。",
    "undervoltage_seen": "检测到历史欠压事件。所有线路会在欠压瞬间同时中断。",
    "throttled_now": "CPU 正在降频/节流，处理能力下降。",
    "temperature_high": "主机温度过高，已接近或进入热节流。",
    "disk_critical": "磁盘空间即将耗尽，可能损坏历史数据并导致线路无法写入运行状态。",
    "disk_low": "磁盘空间偏低。",
    "swap_pressure": "正在频繁换页。在 SD 卡上换页会拖慢所有操作并造成状态查询超时。",
    "default_route_changed": "默认路由在上行之间发生了切换，所有出站连接的源地址随之改变。",
}


async def allowance_reminder_poller():
    """Send one reminder per line/expiry/day at 3, 2 and 1 days before expiry."""
    while True:
        try:
            settings = cfg.get_settings()
            if notify_push.has_enabled_channel(settings, notify_push.EV_ACTIVATION_REMINDER):
                try:
                    local_zone = ZoneInfo(str(settings.get("timezone") or "Asia/Shanghai"))
                except ZoneInfoNotFoundError:
                    local_zone = ZoneInfo("UTC")
                now = datetime.now(local_zone)
                for inst in cfg.list_instances():
                    iid = str(inst.get("id") or "")
                    snapshot = await asyncio.to_thread(store.get_allowance, iid)
                    days = allowance.reminder_days(snapshot, now.date())
                    expiry = allowance.parse_expiry_date(snapshot.get("valid_until"))
                    if days is None or expiry is None:
                        continue
                    claimed = await asyncio.to_thread(
                        store.claim_allowance_reminder, iid, expiry.isoformat(), days,
                        int(now.timestamp()))
                    if not claimed:
                        continue
                    text = (f"线路 {inst.get('name') or iid} 将于 {expiry.isoformat()} 到期，"
                            f"还剩 {days} 天。激活时间：{snapshot.get('activated_at')}。"
                            "请及时续期或重新激活。")
                    await asyncio.to_thread(
                        notify_push.dispatch, settings, notify_push.EV_ACTIVATION_REMINDER,
                        inst, expiry.isoformat(), text)
        except Exception as exc:  # noqa
            log.debug("allowance reminder poll failed: %r", exc)
        await asyncio.sleep(max(60.0, ALLOWANCE_REMINDER_POLL_SECONDS))


def _host_alert_summary(alerts: list[dict]) -> str:
    lines = []
    for item in alerts:
        detail = ", ".join(f"{k}={v}" for k, v in (item.get("detail") or {}).items())
        text = HOST_ALERT_TEXT.get(item["code"], item["code"])
        lines.append(f"[{item['severity']}] {text}" + (f" ({detail})" if detail else ""))
    return "\n".join(lines)


def _visible_host_alerts(alerts: list[dict], state: dict) -> list[dict]:
    """Hide acknowledged conditions until the poller observes a sustained recovery."""
    return [item for item in alerts
            if not (state.get(item["code"]) or {}).get("acknowledged")]


async def agent_health_poller():
    """Publish only Agent health freshness transitions, never ordinary heartbeats."""
    while True:
        try:
            for item in await agent_health_registry.sweep():
                await hub.broadcast({
                    "type": "agent-health", "agent_id": item.get("agent_id"),
                    "connection": item.get("connection"),
                    "online": item.get("online"),
                })
                await _reconcile_all_remote_card_evidence()
        except Exception as exc:  # noqa
            log.debug("Agent health freshness poll failed: %r", exc)
        await asyncio.sleep(2.0)


async def host_health_poller():
    """Announce host conditions that take every line down at once.

    Displaying this only in a panel is not enough: the whole point is that it explains an
    outage nobody would otherwise attribute to the box. Notifications fire on the transition
    into a condition, then only on a long repeat interval, so a persistent brown-out does not
    become a stream nobody reads.

    The suppression state is persisted because it is measured in hours: keeping it in memory
    meant every manager restart re-announced everything that was already known, and an
    appliance is restarted for upgrades far more often than these conditions change.
    """
    if hub.host_alert_state is None:
        hub.host_alert_state = _load_host_alert_state()
    state = hub.host_alert_state
    streaks: dict[str, int] = {}
    previous_alerts = None
    while True:
        try:
            snapshot = await asyncio.to_thread(sysinfo.collect, cfg.DATA_DIR)
            # Rate-based conditions need the previous sample; the first pass reports none.
            alerts = sysinfo.alerts(snapshot, hub.host_snapshot or None)
            alerts = _sustained_alerts(alerts, streaks)
            now = time.time()
            codes = {item["code"] for item in alerts}
            for code, entry in list(state.items()):
                if code in codes:
                    entry.pop("missing_since", None)
                    continue
                missing_since = entry.setdefault("missing_since", now)
                if now - missing_since >= HOST_ALERT_CLEAR_SECONDS:
                    state.pop(code, None)       # genuinely recovered; a return may notify again
            visible = _visible_host_alerts(alerts, state)
            hub.host_snapshot, hub.host_alerts = snapshot, visible
            fresh = [item for item in visible
                     if now - float((state.get(item["code"]) or {}).get("at", 0))
                     >= HOST_ALERT_REPEAT_SECONDS]
            if fresh:
                for item in fresh:
                    state[item["code"]] = {"at": now}
                    log.warning("host alert %s (%s): %s", item["code"], item["severity"],
                                item.get("detail"))
                await hub.broadcast({"type": "host_alert",
                                     "alerts": [item["code"] for item in fresh]})
                asyncio.create_task(asyncio.to_thread(
                    notify_push.dispatch, cfg.get_settings(), notify_push.EV_HOST_ALERT,
                    {"id": "host", "name": snapshot.get("model") or "gateway"},
                    snapshot.get("model") or "gateway", _host_alert_summary(fresh)))
            if fresh or codes != previous_alerts:
                previous_alerts = codes
                await asyncio.to_thread(_save_host_alert_state, state)
        except Exception as exc:  # noqa
            log.debug("host health poll failed: %r", exc)
        await asyncio.sleep(HOST_ALERT_POLL_SECONDS)


def _sustained_alerts(alerts: list[dict], streaks: dict[str, int]) -> list[dict]:
    """Drop conditions that have not held long enough to be worth acting on.

    A one-sample spike is real but not actionable: it is what a container start costs on this
    hardware. Reporting it anyway is how an indicator earns the reputation that makes people
    ignore the one explaining a genuine outage.
    """
    present = {item["code"] for item in alerts}
    for code in list(streaks):
        if code not in present:
            del streaks[code]
    kept = []
    for item in alerts:
        code = item["code"]
        if code not in SUSTAINED_ALERT_CODES:
            kept.append(item)
            continue
        streaks[code] = streaks.get(code, 0) + 1
        if streaks[code] >= SUSTAINED_ALERT_SAMPLES:
            kept.append({**item, "detail": {**(item.get("detail") or {}),
                                            "samples": streaks[code]}})
    return kept


def _save_exit_ledgers() -> None:
    path = _exit_ledger_path()
    try:
        temporary = path + ".tmp"
        with open(temporary, "w", encoding="utf-8") as handle:
            json.dump(hub.exit_ledgers, handle)
        os.replace(temporary, path)
    except OSError as exc:
        log.debug("cannot persist exit failover ledger: %r", exc)


def _clear_manual_recovery_history(iid: str) -> None:
    """Forget automatic failover history after an explicit operator intervention."""
    iid = str(iid)
    if hub.exit_ledgers.pop(iid, None) is not None:
        _save_exit_ledgers()
    hub.ok_since.pop(iid, None)
    hub.reg_unanswered_recovery_at.pop(iid, None)
    hub.reset_health(iid)


def _peer_line_registered(iid: str, country: str) -> bool:
    """Whether another line of the same country is registered right now.

    Lines of one country share one exit. A registered peer is living proof the exit can carry
    IMS — and a tunnel that moving the exit would tear down, which is the disruption this
    design exists to avoid. While one holds, eviction is off the table.
    """
    if not country:
        return False
    for other in cfg.list_instances():
        oid = str(other.get("id") or "")
        if oid == str(iid) or not other.get("enabled", True):
            continue
        if egress.line_country(other) != country:
            continue
        if (hub.status_cache.get(oid) or {}).get("state") == "OK":
            return True
    return False


def _plan_exit_failure(iid: str, inst: dict, stable_for: float) -> dict:
    """Compute one exit-failure decision without changing runtime or durable policy state."""
    iid = str(iid)
    country = egress.line_country(inst)
    exits = (egress.status().get("exits") or {}).get(country) or {}
    node = str(exits.get("node") or "")
    candidates = [str(name) for name in (exits.get("candidates") or [])]
    pinned = exits.get("selection") == "manual"
    peer_registered = _peer_line_registered(iid, country)
    try:
        swu = (engine.read_run_json(iid, "swu_status.json") or {}).get("state") or ""
        retransmits = int((engine.ike_evidence(iid) or {}).get("retransmits") or 0)
    except Exception as exc:  # noqa
        log.debug("cannot read tunnel evidence for line %s: %r", iid, exc)
        swu, retransmits = None, None
    verdict = (failover.classify(swu, retransmits, stable_for,
                                 egress.RESELECT_MIN_STABLE_SECONDS)
               if swu is not None and retransmits is not None
               else failover.UNCLEAR)
    # A fresh Engine that has never produced an OK sample has no continuity evidence. An
    # established tunnel alone cannot identify whether registration failed in the exit,
    # ePDG/IMS, reader/Agent transport or local scheduling, so keep this sample read-only.
    if stable_for <= 0 and verdict == failover.BLAMES_ELSEWHERE:
        verdict = failover.UNCLEAR
    was_backing_off = bool((hub.exit_ledgers.get(iid) or {}).get("exhausted"))
    action, ledger = failover.record(hub.exit_ledgers.get(iid), verdict, node,
                                     pinned, candidates, peer_registered=peer_registered)
    return {
        "action": action, "ledger": ledger, "country": country, "node": node,
        "candidates": candidates, "pinned": pinned, "peer_registered": peer_registered,
        "swu": swu, "retransmits": retransmits, "verdict": verdict,
        "was_backing_off": was_backing_off,
    }


def _commit_exit_failure_plan(iid: str, inst: dict, st: dict, stable_for: float,
                              plan: dict) -> str:
    """Commit a previously computed decision after its lifecycle safety gate passed."""
    iid = str(iid)
    action = str(plan["action"])
    ledger = dict(plan["ledger"])
    hub.exit_ledgers[iid] = ledger
    _save_exit_ledgers()
    transition = ("kept the current Engine for in-place recovery"
                  if action in {failover.HOLD, failover.REPORT, failover.PACE}
                  else "froze after stopping the idle Engine")
    log.info("line %s %s (%s) after %.0fs healthy; tunnel=%s ike_retransmits=%s "
             "-> blames %s, action %s (node=%s strikes=%d tried=%d/%d peer=%s)",
             iid, transition, st.get("reason_code"), stable_for,
             plan.get("swu") or "unknown",
             (plan.get("retransmits")
              if plan.get("retransmits") is not None else "unknown"),
             plan.get("verdict"), action,
             plan.get("node") or "unknown", ledger.get("strikes") or 0,
             len(ledger.get("tried") or []), len(plan.get("candidates") or []),
             bool(plan.get("peer_registered")))
    if action == failover.SWITCH:
        try:
            egress.request_reselect(inst, f"health-freeze:{st['reason_code']}",
                                    stable_for=stable_for)
        except Exception as exc:  # noqa
            log.warning("exit reselect request failed for line %s: %s", iid, exc)
    elif action in (failover.GIVE_UP, failover.REPORT) or (
            action == failover.BACK_OFF and not plan.get("was_backing_off")):
        country = str(plan.get("country") or "")
        text = failover.summarise(
            ledger, action, country, bool(plan.get("pinned")),
            {"swu": plan.get("swu"), "retransmits": plan.get("retransmits"),
             "node": plan.get("node")})
        log.warning("line %s: %s", iid, text)
        asyncio.create_task(asyncio.to_thread(
            notify_push.dispatch, cfg.get_settings(), notify_push.EV_LINE_UNRECOVERABLE,
            inst, plan.get("node") or country.upper(), text))
    return action


def _judge_exit_failure(iid: str, inst: dict, st: dict, stable_for: float) -> str:
    """Compatibility wrapper for callers that intentionally plan and commit immediately."""
    plan = _plan_exit_failure(iid, inst, stable_for)
    return _commit_exit_failure_plan(iid, inst, st, stable_for, plan)


def _host_alert_state_path() -> str:
    return os.path.join(cfg.DATA_DIR, "host-alert-state.json")


def _load_host_alert_state() -> dict:
    try:
        with open(_host_alert_state_path(), encoding="utf-8") as handle:
            loaded = json.load(handle)
        return {str(k): v for k, v in loaded.items() if isinstance(v, dict)}
    except (OSError, ValueError, AttributeError):
        return {}


def _save_host_alert_state(state: dict) -> None:
    path = _host_alert_state_path()
    try:
        temporary = path + ".tmp"
        with open(temporary, "w", encoding="utf-8") as handle:
            json.dump(state, handle)
        os.replace(temporary, path)
    except OSError as exc:
        log.debug("cannot persist host alert state: %r", exc)


async def cellular_sms_poller():
    """Import SMS received by the 4G modem even when its VoWiFi engine is stopped."""
    scanner = cellular_sms.Scanner(local_sms_tracker=store)
    remote_retry: dict[str, tuple[float, int]] = {}
    while True:
        try:
            discovered = await asyncio.to_thread(scanner.discover, cfg.list_instances())
            for item in discovered:
                if (item.get("direction") or "in") == "in" and not sms_content.is_displayable_sms_text(
                        item.get("body")):
                    log.info("dropping non-displayable cellular SMS payload")
                    continue
                rec = await asyncio.to_thread(
                    store.add_imported_message, item["fingerprint"], item["instance"],
                    item["direction"], item["peer"], item["body"], item["ts"],
                    item["transport"])
                if not rec:
                    continue
                await hub.broadcast({"type": "sms", "instance": rec["instance"],
                                     "message": rec})
                if rec["direction"] == "in":
                    _dispatch_push(notify_push.EV_INCOMING_SMS, rec["instance"],
                                   rec["peer"], rec["body"])
        except Exception as exc:  # noqa
            log.debug("cellular SMS poll failed: %r", exc)
        # Remote objects use the same durable store and broadcast path.  A successful import is
        # acknowledged only after persistence, so reconnect/replay cannot lose a message.
        for attachment in modem_registry.list():
            if not attachment.get("online") or not (attachment.get("capabilities") or {}).get("sms"):
                continue
            iccid = str(attachment.get("iccid") or "")
            retry_at, failures = remote_retry.get(iccid, (0.0, 0))
            if time.monotonic() < retry_at:
                continue
            try:
                result = await modem_registry.rpc(iccid, "sms.list", timeout=8)
                if result.get("degraded"):
                    delay = max(5, min(300, int(result.get("retry_after") or 60)))
                    remote_retry[iccid] = (time.monotonic() + delay, failures + 1)
                    continue
                for item in result.get("messages") or []:
                    iid = str((_match_instance_by_iccid(iccid) or {}).get("id") or "")
                    if not iid:
                        continue
                    direction = item.get("direction") or "in"
                    if (direction == "in" and
                            (item.get("displayable") is False or
                             not sms_content.is_displayable_sms_text(item.get("body")))):
                        # The modem has already accepted this OTA/SIM data message. Acknowledge
                        # its storage object so it does not fill the device or reappear on every
                        # poll, but never create user history or a push notification for it.
                        await modem_registry.rpc(iccid, "sms.ack",
                                                 {"id": item.get("id"),
                                                  "fingerprint": item.get("fingerprint")},
                                                 timeout=8)
                        log.info("acknowledged non-displayable remote cellular SMS payload")
                        continue
                    fingerprint = (f"remote:{iccid}:"
                                   f"{item.get('fingerprint') or item.get('id')}")
                    rec = await asyncio.to_thread(
                        store.add_imported_message, fingerprint, iid,
                        direction, item.get("peer") or "",
                        item.get("body") or "", int(item.get("ts") or time.time()), "cellular")
                    if rec:
                        await hub.broadcast({"type": "sms", "instance": iid, "message": rec})
                    await modem_registry.rpc(iccid, "sms.ack",
                                             {"id": item.get("id"),
                                              "fingerprint": item.get("fingerprint")}, timeout=8)
                remote_retry.pop(iccid, None)
            except Exception as exc:  # noqa
                failures += 1
                delay = min(300, 5 * (2 ** min(failures, 6)))
                remote_retry[iccid] = (time.monotonic() + delay, failures)
                log.debug("remote cellular SMS poll failed for %s: %r",
                          iccid[-4:], exc)
        await asyncio.sleep(5)


async def remote_call_poller():
    """Import remote cellular call state into the existing call store and broadcasts."""
    while True:
        for attachment in modem_registry.list():
            caps = attachment.get("capabilities") or {}
            if not attachment.get("online") or not caps.get("call_signalling"):
                continue
            iid = str((_match_instance_by_iccid(attachment.get("iccid") or "") or {}).get("id") or "")
            if not iid:
                continue
            try:
                result = await modem_registry.rpc(attachment["iccid"], "call.status", timeout=6)
                authoritative = bool(result.get("fresh") and result.get("authoritative"))
                terminal_evidence = bool(
                    authoritative and int(result.get("terminal_samples") or 0) >= 2)
                state, number = str(result.get("status") or "unknown"), str(result.get("number") or "")
                incoming = store.get_open_call(iid, "in", within_s=24 * 3600)
                outgoing = store.get_open_call_for_transport(iid, "cellular")
                current = incoming if incoming and incoming.get("transport") == "cellular" else outgoing
                if authoritative and state in {"ringing-in", "waiting"} and not current:
                    current = store.add_call(iid, "in", number, status="ringing",
                                             transport="cellular")
                    await hub.broadcast({"type": "call", "instance": iid, "call": current})
                    _dispatch_push(notify_push.EV_INCOMING_CALL, iid, number, "")
                elif current and authoritative:
                    if (state in _CELLULAR_TERMINAL_STATES and not terminal_evidence):
                        continue
                    status, ended = _cellular_call_result_status(state)
                    store.update_call(current["id"], status, ended=ended)
                    current["status"] = status
                    if ended:
                        current["end_ts"] = int(time.time())
                        await _close_confirmed_terminal_cellular_media(
                            call_media.manager.for_iccid(attachment["iccid"]))
                    await hub.broadcast({"type": "call", "instance": iid, "call": current})
            except Exception as exc:  # noqa
                log.debug("remote cellular call poll failed for %s: %r",
                          attachment.get("iccid", "")[-4:], exc)
        await asyncio.sleep(3)


async def cellular_call_lease_recovery():
    """Resolve durable non-terminal paid calls after a gateway restart.

    A browser media session cannot survive this process, so recovery never resumes or redials
    it. The exact ICCID attachment is queried and termination is requested until fresh CLCC
    proves idle; an offline SIM remains quarantined in the durable table.
    """
    await asyncio.sleep(2)
    while True:
        for lease in await asyncio.to_thread(store.list_open_cellular_call_leases):
            iccid = str(lease.get("iccid") or "")
            attachment = modem_registry.resolve(iccid)
            if not attachment or not attachment.online:
                continue
            try:
                status = await modem_registry.rpc(iccid, "call.status", {}, timeout=8)
                terminal = bool(
                    status.get("fresh") and status.get("authoritative") and
                    int(status.get("terminal_samples") or 0) >= 2 and
                    str(status.get("status") or "").casefold() in
                    {"idle", "ended", "terminated"})
                if not terminal:
                    await modem_registry.rpc(
                        iccid, "call.hangup", {},
                        operation_id=f"restart-release:{lease['call_id']}", timeout=20)
                    status = await modem_registry.rpc(iccid, "call.status", {}, timeout=8)
                    terminal = bool(
                        status.get("fresh") and status.get("authoritative") and
                        int(status.get("terminal_samples") or 0) >= 2 and
                        str(status.get("status") or "").casefold() in
                        {"idle", "ended", "terminated"})
                if terminal:
                    await asyncio.to_thread(
                        store.save_cellular_call_lease, lease["call_id"], lease["instance"],
                        iccid, lease["direction"], "terminal_confirmed")
            except Exception as exc:
                log.warning("paid-call restart recovery pending for %s: %s",
                            iccid[-4:], exc)
        await asyncio.sleep(10)


async def _pcscf_rebind_pending(iid: str) -> bool:
    iid = str(iid)
    pending = await asyncio.to_thread(engine.pcscf_rebind_pending, iid)
    if pending:
        hub.pcscf_rebinding.add(iid)
    else:
        hub.pcscf_rebinding.discard(iid)
        hub.pcscf_rebind_result.pop(iid, None)
    return pending


async def _line_admission_blocked(iid: str) -> bool:
    return (engine.global_maintenance_pending()
            or engine.engine_maintenance_pending(str(iid))
            or engine.engine_start_quarantine_pending(str(iid))
            or engine.usim_recovery_fence_pending(str(iid))
            or str(iid) in hub.engine_recovering
            or await _pcscf_rebind_pending(str(iid)))


def _durable_maintenance_pending(iid: str) -> bool:
    return (engine.global_maintenance_pending()
            or engine.engine_maintenance_pending(str(iid)))


def _durable_maintenance_status(iid: str) -> dict:
    return _with_status_activity(str(iid), {
        "state": "REGISTERING", "label": "Maintenance in progress",
        "reason_code": "maintenance_rebuild",
        "reason": "A verified maintenance transaction is updating this line.",
        "detail": {"maintenance_pending": True},
    })


def _engine_start_quarantine_status(iid: str) -> dict:
    snapshot = engine.engine_start_quarantine_status(str(iid)) or {"valid": False}
    valid = bool(snapshot.get("valid"))
    reason = str(snapshot.get("reason") or "")[:240]
    if valid:
        state, label = "STOPPED", "Engine start quarantined"
        reason = reason or "This line is intentionally protected from Engine startup."
    else:
        state, label = "ERROR", "Engine start quarantine needs attention"
        reason = "The durable Engine start quarantine is invalid; an administrator must inspect it."
    return _with_status_activity(str(iid), {
        "state": state,
        "label": label,
        "reason_code": "engine_start_quarantined",
        "reason": reason,
        "quarantined": True,
        "manual_required": True,
        "detail": {"engine_start_quarantined": True,
                   "quarantine_valid": valid},
        "retry": {"count": 0, "max": 0},
    })


@asynccontextmanager
async def _pcscf_admission_boundary(iid: str):
    """Totally order one immediate submission against SWu marker publication."""
    # This call is intentionally synchronous and non-blocking (LOCK_NB).  No worker can outlive a
    # cancelled coroutine and later acquire a flock whose handle the coroutine never receives.
    handle = engine.acquire_pcscf_admission(str(iid))
    try:
        # The host supervisor takes this same flock while atomically publishing the global
        # entry fence. A request that passed middleware just before publication must recheck
        # here, after acquiring the shared boundary, or it could submit after the drain's zero
        # sample.
        yield (handle is not None
               and not _durable_maintenance_pending(str(iid))
               and not engine.usim_recovery_fence_pending(str(iid)))
    finally:
        if handle is not None:
            # Unlock/close are local syscalls and must run even while the task is being cancelled.
            engine.release_pcscf_admission(handle)


@asynccontextmanager
async def _maintenance_submission_boundary(iid: str):
    """Order one cellular paid submission against the durable maintenance owner."""
    async with hub.recovery_lock(str(iid)):
        manager = engine.engine_maintenance_locked(str(iid), blocking=False)
        try:
            manager.__enter__()
        except BlockingIOError:
            yield False
            return
        try:
            # Recheck only after acquiring the shared cross-process lock. The deployment
            # owner publishes/advances the per-line marker under the same lock.
            yield not _durable_maintenance_pending(str(iid))
        finally:
            manager.__exit__(None, None, None)


async def _reconcile_pcscf_rebind(iid: str, event_run_id: str = "") -> dict | None:
    """Recover one durable P-CSCF rebind transaction after event loss/Control restart.

    Marker presence always fences new work.  Commands require exact current container id,
    Docker StartedAt and Engine run id, and are reserved durably by ``engine`` before exec.
    An old-run marker during ``unless-stopped`` bootstrap is observed but never mutated; the
    new entrypoint clears it only after freshly rendering a discovered P-CSCF.
    """
    iid = str(iid)
    if not await asyncio.to_thread(engine.pcscf_rebind_pending, iid):
        hub.pcscf_rebinding.discard(iid)
        hub.pcscf_rebind_result.pop(iid, None)
        return None
    hub.pcscf_rebinding.add(iid)
    async with hub.recovery_lock(iid):
        marker = await asyncio.to_thread(engine.read_pcscf_rebind, iid)
        if not marker:
            # Invalid/corrupt files remain a fail-closed admission fence but cannot authorize a
            # lifecycle command. A successful entrypoint render will replace/clear the file.
            if not await asyncio.to_thread(engine.pcscf_rebind_pending, iid):
                hub.pcscf_rebinding.discard(iid)
                hub.pcscf_rebind_result.pop(iid, None)
                return None
            result = {"status": "invalid_marker", "manual_required": True}
            hub.pcscf_rebind_result[iid] = result
            return result
        current = await hub.runtime.get(iid, force=True)
        inst = await asyncio.to_thread(cfg.get_instance, iid)
        if not inst or not inst.get("enabled", True):
            return {"status": "disabled"}
        current_run_id = str(current.get("engine_run_id") or "")
        if (event_run_id and event_run_id != str(marker.get("engine_run_id") or "")):
            return {"status": "stale_event"}
        if (not current.get("running") or not current.get("container_id")
                or not current.get("started_at") or not current_run_id
                or current_run_id != str(marker.get("engine_run_id") or "")):
            return {"status": "awaiting_new_generation"}
        args = (iid, str(current.get("container_id")),
                str(current.get("started_at")), current_run_id)
        if marker.get("phase") == "cancel_requested":
            result = await asyncio.to_thread(engine.cancel_pcscf_rebind, *args)
        else:
            result = await asyncio.to_thread(engine.request_pcscf_rebind, *args)
        hub.pcscf_rebind_result[iid] = dict(result or {})
        if not await asyncio.to_thread(engine.pcscf_rebind_pending, iid):
            hub.pcscf_rebinding.discard(iid)
            hub.pcscf_rebind_result.pop(iid, None)
        return result


def _with_pcscf_rebind_observation(iid: str, st: dict) -> dict:
    if str(iid) not in hub.pcscf_rebinding:
        return st
    detail = dict(st.get("detail") or {})
    detail["pcscf_rebind_pending"] = True
    result = dict(hub.pcscf_rebind_result.get(str(iid)) or {})
    if result:
        detail["pcscf_rebind_status"] = str(result.get("status") or "pending")
        if result.get("rejections") is not None:
            detail["pcscf_rebind_rejections"] = int(result["rejections"])
    manual = bool(result.get("manual_required")) or str(result.get("status") or "") in {
        "submit_retry_exhausted", "abort_retry_exhausted", "invalid_marker",
        "submit_retry_state_invalid", "abort_retry_state_invalid",
    }
    if manual:
        invalid_state = str(result.get("status") or "") in {
            "invalid_marker", "submit_retry_state_invalid", "abort_retry_state_invalid"}
        return {**st, "state": "ERROR", "label": "P-CSCF route change needs attention",
                "reason_code": "pcscf_rebind_manual", "reason":
                (("The durable route-transition state is invalid; " if invalid_state else
                  "Asterisk repeatedly rejected the safe route transition; ") +
                 "new work remains paused and an administrator must inspect the line."),
                "detail": detail}
    return {**st, "state": "REGISTERING", "label": "Applying a new P-CSCF route",
            "reason_code": "pcscf_rebind", "reason":
            "The carrier changed P-CSCF; existing calls may finish while new work is paused.",
            "detail": detail}


async def _poll_instance_status(inst: dict) -> None:
    """Sample one line in the background; slow carrier state never blocks HTTP pages."""
    iid = str(inst["id"])
    status_epoch = hub.status_epoch(iid)
    try:
        if engine.engine_start_quarantine_pending(iid):
            st = _engine_start_quarantine_status(iid)
            async with hub.status_publish_lock(iid):
                if not hub.status_epoch_current(iid, status_epoch):
                    return
                hub.status_cache[iid] = st
                hub.status_sampled_at[iid] = time.monotonic()
                await hub.broadcast({"type": "status", "instance": iid, **st})
            await _record_line_state(iid, st)
            return
        # A deployment fence is authoritative even if its JSON is corrupt. Do not stop a
        # disabled line, start recovery, open AMI, or mutate Docker while the external exact-
        # generation owner is converging this transaction. Existing SIP BYE/CANCEL and the
        # explicit hangup APIs remain outside this sampler and can still terminate calls.
        if _durable_maintenance_pending(iid):
            st = _durable_maintenance_status(iid)
            async with hub.status_publish_lock(iid):
                if not hub.status_epoch_current(iid, status_epoch):
                    return
                hub.status_cache[iid] = st
                hub.status_sampled_at[iid] = time.monotonic()
                await hub.broadcast({"type": "status", "instance": iid, **st})
            await _record_line_state(iid, st)
            return
        # One inspect supplies both running state and bridge IP to the whole sample. Previously
        # ami_for(), compute() and the grace-path each queried Docker independently.
        runtime = await hub.runtime.get(iid)
        # A disabled line is authoritative user intent. Automatic recovery must never
        # resurrect a stale container left behind by an earlier retry or process restart;
        # doing so can retain the SIM/PCSC channel and disrupt another active line.
        if not inst.get("enabled", True):
            disabled_enforced = False
            async with hub.recovery_lock(iid):
                # Both config and Docker views taken before this lock are stale by definition:
                # a manual start/stop may have waited behind the sampler. Resolve intent and the
                # exact generation again at the same lifecycle order point used by those APIs.
                current = await asyncio.to_thread(cfg.get_instance, iid)
                runtime = await hub.runtime.get(iid, force=True)
                if current and current.get("enabled", True):
                    inst = current
                else:
                    if _durable_maintenance_pending(iid):
                        st = _durable_maintenance_status(iid)
                        async with hub.status_publish_lock(iid):
                            if not hub.status_epoch_current(iid, status_epoch):
                                return
                            hub.status_cache[iid] = st
                            hub.status_sampled_at[iid] = time.monotonic()
                            await hub.broadcast({"type": "status", "instance": iid, **st})
                        return
                    if runtime["running"]:
                        await asyncio.to_thread(
                            engine.stop, iid,
                            expected_container_id=runtime.get("container_id"))
                        await hub.drop_ami(iid)
                    hub.reset_health(iid)
                    disabled_enforced = True
            if disabled_enforced:
                stopped = _with_status_activity(iid, {
                    "state": "STOPPED", "label": status_mod.LABELS["STOPPED"],
                    "reason_code": "stopped", "reason": "Stopped.", "detail": {},
                    "retry": {"count": 0, "max": 0}})
                async with hub.status_publish_lock(iid):
                    if not hub.status_epoch_current(iid, status_epoch):
                        return
                    hub.status_cache[iid] = stopped
                    hub.status_sampled_at[iid] = time.monotonic()
                    await hub.broadcast({"type": "status", "instance": iid, **stopped})
                await _record_line_state(iid, stopped)
                return
        await _reconcile_pcscf_rebind(iid)
        ami = await hub.ami_for(iid, runtime)
        st = await status_mod.compute(inst, ami, runtime)
        registration = str((st.get("detail") or {}).get("registration") or "unknown")
        previous = hub.status_cache.get(iid)
        previous_sampled_at = hub.status_sampled_at.get(iid)
        observed_at = time.monotonic()
        held_previous = False
        # A single management timeout is not evidence that a known-good registration vanished.
        # Hold the last confirmed OK briefly, but never refresh that timestamp from unknown
        # samples: a dead Asterisk must become unhealthy after the bounded grace period.
        if (st.get("state") == "REGISTERING" and registration == "unknown"
                and (previous or {}).get("state") == "OK"
                and observed_at - hub.status_sampled_at.get(iid, 0) <= STATUS_OK_GRACE_SECONDS
                and runtime["running"]):
            st = previous
            held_previous = True
        if (st.get("reason_code") == "reg_unanswered"
                or _health_recovery_due(iid, inst, st)):
            detail = dict(st.get("detail") or {})
            channels = detail.get("active_channels")
            if channels is None and ami is not None:
                try:
                    channels = await ami.active_channel_count()
                except Exception:
                    channels = None
            if channels is None:
                try:
                    channels = await asyncio.to_thread(engine.active_channel_count, iid)
                except Exception:
                    channels = None
            detail["active_channels"] = channels if type(channels) is int else None
            st = {**st, "detail": detail}
        st = _with_status_activity(
            iid, _with_pcscf_rebind_observation(
                iid, await _apply_health_with_recovery(
                    iid, inst, st, runtime.get("container_id"))))
        async with hub.status_publish_lock(iid):
            if not hub.status_epoch_current(iid, status_epoch):
                return
            hub.status_cache[iid] = st
            if held_previous and previous_sampled_at is not None:
                # apply_health(OK) clears health and its related cache bookkeeping. Restore the
                # original authoritative timestamp, never the current poll time, so unknown
                # samples cannot extend the grace window indefinitely.
                hub.status_sampled_at[iid] = previous_sampled_at
            elif not held_previous:
                hub.status_sampled_at[iid] = observed_at
            await hub.broadcast({"type": "status", "instance": iid, **st})
        await _record_line_state(iid, st)
    except Exception as exc:  # noqa
        log.debug("status sample failed instance=%s: %r", iid, exc)


def _cached_line_status(inst: dict) -> dict:
    """Return an immediate status snapshot without contacting Docker/Asterisk/AMI."""
    iid = str(inst["id"])
    if engine.engine_start_quarantine_pending(iid):
        return _engine_start_quarantine_status(iid)
    if _durable_maintenance_pending(iid):
        return _durable_maintenance_status(iid)
    if not inst.get("enabled", True):
        return _with_status_activity(iid, {
            "state": "STOPPED", "label": status_mod.LABELS["STOPPED"],
            "reason_code": "stopped", "reason": "Stopped.", "detail": {}})
    cached = hub.status_cache.get(iid)
    if cached:
        now = time.monotonic()
        sampled_at = hub.status_sampled_at.get(iid)
        age = None
        if type(sampled_at) in (int, float) and math.isfinite(float(sampled_at)):
            measured = now - float(sampled_at)
            if measured >= 0:
                age = measured
        if age is not None and age <= STATUS_CACHE_MAX_AGE_SECONDS:
            return dict(cached)
        return _with_status_activity(iid, {
            "state": "REGISTERING", "label": status_mod.LABELS["REGISTERING"],
            "reason_code": "status_stale", "reason": "Refreshing line status…",
            "detail": {
                "stale_previous_state": str(cached.get("state") or ""),
                "stale_sample_age_seconds": int(age) if age is not None else None,
            }})
    transition = hub.status_transitions.get(iid)
    if transition:
        observed_at = transition.get("observed_at")
        if type(observed_at) in (int, float) and math.isfinite(float(observed_at)):
            age = time.monotonic() - float(observed_at)
            if 0 <= age <= STATUS_CACHE_MAX_AGE_SECONDS:
                return dict(transition.get("status") or {})
    return _with_status_activity(iid, {
        "state": "REGISTERING", "label": status_mod.LABELS["REGISTERING"],
        "reason_code": "registering", "reason": "Refreshing line status…", "detail": {}})


def _with_status_activity(iid: str, st: dict) -> dict:
    """Explain the status machine in user terms: now, why, and what happens next."""
    st = dict(st)
    state = str(st.get("state") or "").upper()
    detail = st.get("detail") or {}
    retry = st.get("retry") or {}
    health = hub.health_for(str(iid))
    retrying = bool(health.get("auto_retrying"))
    remaining = st.get("automatic_retry_in")
    reason_code = str(st.get("reason_code") or "")

    if retrying:
        current = "Rebuilding the VoWiFi line automatically"
        next_action = "The SIM will be read again, then ePDG and IMS will reconnect."
    elif st.get("frozen") and remaining:
        current = "Automatic recovery is waiting"
        next_action = "The VoWiFi line will be rebuilt in {seconds} seconds."
    elif st.get("frozen"):
        current = st.get("reason") or "Waiting for manual attention"
        next_action = ("Verify the SIM PIN before automatic setup can continue."
                       if str(st.get("reason_code") or "").startswith("pin_")
                       else "Restart the line after resolving the reported problem.")
    elif state == "OK":
        current = "IMS is registered and the line is being monitored"
        next_action = "Automatic recovery will run if the connection is lost."
    elif state == "STOPPED":
        current = "The VoWiFi line is stopped"
        next_action = "Enable VoWiFi to start the line."
    elif state == "NO_CARD":
        current = "Waiting for the SIM card"
        next_action = "Insert the SIM card to continue automatically."
    elif state == "PIN_PROBLEM":
        current = "Waiting for SIM PIN attention"
        next_action = "Verify the SIM PIN before automatic setup can continue."
    elif state == "EPDG_UNRESOLVED":
        current = "Resolving the carrier ePDG gateway"
        next_action = "The backend will retry automatically."
    elif state == "TUNNEL_DOWN":
        current = "Establishing the secure ePDG tunnel"
        next_action = ("The current Engine will keep retrying; bounded recovery only replaces "
                       "an idle Engine when the selected recovery action requires it.")
    elif reason_code == "pcscf_rebind":
        current = "Applying the carrier's new P-CSCF in a fresh Engine generation"
        next_action = ("Existing calls and hangup remain available; new calls and SMS resume "
                       "after the graceful restart completes.")
    elif reason_code == "reg_temporary":
        current = "The carrier temporarily rejected IMS registration"
        next_action = "Asterisk will retry the same registration in place after its scheduled delay."
    elif reason_code == "reg_rejected":
        current = "The carrier rejected IMS registration"
        next_action = "The same Engine will retry at low frequency; check the SIM and IMS settings."
    elif reason_code == "status_stale":
        current = "Refreshing the VoWiFi line status"
        next_action = "The background sampler must confirm the Engine before calls are shown as ready."
    elif reason_code == "registering":
        current = "Contacting the carrier IMS through P-CSCF"
        next_action = "Asterisk will continue this registration in place without rebuilding the line."
    elif reason_code == "reg_unanswered":
        current = "The carrier IMS is not answering registration"
        next_action = ("The current Engine will retry in place while bounded recovery verifies "
                       "that replacement is safe and no call is active.")
    elif reason_code in {"local_bootstrap_unready", "local_registration_unreadable",
                         "local_registration_stalled"}:
        current = "The local VoWiFi Engine is not making registration progress"
        next_action = "After the safety checks, only this idle Engine generation will be recovered."
    elif state == "REGISTERING" and detail.get("pcscf"):
        current = "Contacting the carrier IMS through P-CSCF"
        next_action = "The current Engine will keep retrying IMS registration in place."
    elif state == "REGISTERING":
        current = "Waiting for carrier P-CSCF discovery"
        next_action = "IMS registration starts automatically after discovery."
    else:
        current = st.get("label") or "Checking the VoWiFi line"
        next_action = "The backend will keep monitoring the line."

    st["activity"] = {
        "current": current,
        "next": next_action,
        "automatic": (state not in {"STOPPED", "NO_CARD", "PIN_PROBLEM"}
                      and not (st.get("frozen") and not remaining)),
        "retry_count": int(retry.get("count") or 0),
        "retry_max": int(retry.get("max") or 0),
        "seconds": int(remaining or 0),
    }
    return st


def _frozen(h, st, rmax):
    remaining = max(0, int((h.get("next_retry_at") or 0) - time.monotonic()))
    return {"state": "ERROR", "label": status_mod.LABELS["ERROR"],
            "reason_code": h["frozen_code"], "reason": h["frozen_reason"],
            "detail": st.get("detail", {}), "retry": {"count": rmax, "max": rmax},
            "frozen": True, "automatic_retry_in": remaining or None}


def _health_recovery_due(iid: str, inst: dict, st: dict) -> bool:
    """Whether the next health overlay may remove the current Engine generation."""
    if st.get("state") in {"OK", "STOPPED", "NO_CARD", "PIN_PROBLEM"}:
        return False
    h = hub.health_for(str(iid))
    started = h.get("fail_start")
    if started is None:
        return False
    rcfg = inst.get("retry") or cfg.get_settings().get("retry", {})
    rmax = max(1, int(rcfg.get("max", 3)))
    rint = max(5, int(rcfg.get("interval", 40)))
    return time.monotonic() - float(started) >= rmax * rint


async def _auto_recover_instance(iid: str, inst: dict, delay: int,
                                 scheduled_epoch: int | None = None):
    h = hub.health_for(iid)
    if scheduled_epoch is None:
        scheduled_epoch = hub.lifecycle_epoch(iid)
    try:
        if hub.lifecycle_epoch(iid) != scheduled_epoch:
            h["auto_retrying"] = False
            return
        allowed, blocked_reason = _line_auto_start_allowed(inst)
        if not allowed:
            hub.reset_health(iid)
            no_card = blocked_reason == "no_card"
            stopped = _with_status_activity(iid, {
                "state": "NO_CARD" if no_card else "STOPPED",
                "label": "No SIM card" if no_card else status_mod.LABELS["STOPPED"],
                "reason_code": blocked_reason,
                "reason": ("SIM card is not available." if no_card
                           else "The line or its device VoWiFi switch is disabled."),
                "detail": {}, "retry": {"count": 0, "max": 0}})
            hub.status_cache[str(iid)] = stopped
            hub.status_sampled_at[str(iid)] = time.monotonic()
            await hub.broadcast({"type": "status", "instance": str(iid), **stopped})
            return
        async with hub.recovery_lock(iid):
            if hub.lifecycle_epoch(iid) != scheduled_epoch:
                h["auto_retrying"] = False
                return
            current = cfg.get_instance(iid)
            if not current:
                hub.reset_health(iid)
                return
            allowed, _blocked_reason = _line_auto_start_allowed(current)
            if not allowed:
                hub.reset_health(iid)
                return
            inst = current
            # A manual start or hotplug recovery may already have installed a new generation.
            # Never let an old cooldown recreate (and force-remove) that replacement.
            runtime = await hub.runtime.get(iid, force=True)
            if runtime.get("running"):
                hub.reset_health(iid)
                return
            recovering = _with_status_activity(iid, {
                "state": "REGISTERING", "label": status_mod.LABELS["REGISTERING"],
                "reason_code": h.get("frozen_code") or "registering",
                "reason": h.get("frozen_reason") or "Automatic recovery is rebuilding the line.",
                "detail": {}, "retry": {"count": 0, "max": 0}})
            hub.status_cache[str(iid)] = recovering
            hub.status_sampled_at[str(iid)] = time.monotonic()
            await hub.broadcast({"type": "status", "instance": str(iid), **recovering})
            # Broadcast yields to other work and an external host owner does not share this
            # asyncio lock. Re-read every authority immediately before Docker's atomic
            # absent-only create; never force-remove whichever generation won the name race.
            current = cfg.get_instance(iid)
            allowed, _blocked_reason = _line_auto_start_allowed(current or {})
            runtime = await hub.runtime.get(iid, force=True)
            if (hub.lifecycle_epoch(iid) != scheduled_epoch or not current or not allowed
                    or runtime.get("running") or _durable_maintenance_pending(iid)):
                h["auto_retrying"] = False
                return
            inst = current
            try:
                await asyncio.to_thread(
                    _start_engine_checked, inst, cfg.get_settings(),
                    os.environ.get("MDD_DEV_MOUNTS", "") == "1",
                    # Records why the health policy gave up on the previous container, so the
                    # captured snapshot explains itself without cross-referencing the journal.
                    f"auto-recover:{h.get('frozen_code') or 'unhealthy'}",
                    replace_existing=False)
            except engine.EngineAlreadyExists:
                hub.reset_health(iid)
                return
        hub.reset_health(iid)
        starting = _with_status_activity(iid, {
            "state": "REGISTERING", "label": status_mod.LABELS["REGISTERING"],
            "reason_code": "registering", "reason": "The line was rebuilt successfully.",
            "detail": {}, "retry": {"count": 0, "max": 0}})
        hub.status_cache[str(iid)] = starting
        hub.status_sampled_at[str(iid)] = time.monotonic()
        await hub.broadcast({"type": "status", "instance": str(iid), **starting})
    except Exception as exc:
        h["auto_retrying"] = False
        # Preserve the policy cadence. A failed hourly probe must not fall back to the normal
        # 20-160 second retry loop and resume container churn.
        h["next_retry_at"] = time.monotonic() + delay
        h["frozen_reason"] = str(getattr(exc, "detail", exc))


def _preserve_engine_after_exit_action(iid: str, st: dict, overlaid: dict,
                                       action: str, rmax: int) -> dict:
    """Restart the observation budget while the current Engine keeps retrying in place."""
    h = hub.health_for(iid)
    h["fail_start"] = None
    h["retry_count"] = 0
    h["frozen_code"] = None
    h["frozen_reason"] = None
    h["next_retry_at"] = None
    h["auto_retrying"] = False
    h["retry_delay"] = None
    h["recovery_blocked_generation"] = None
    h["recovery_blocked_until"] = None
    h["recovery_blocked_reason"] = None
    return {**overlaid, "detail": {**(overlaid.get("detail") or {}),
                                     "recovery_action": action,
                                     "recovery_mode": "in_place"},
            "retry": {"count": 0, "max": rmax}}


def _finalize_health_freeze(iid: str, inst: dict, st: dict, *, fast_unanswered: bool,
                            stable_for: float, exit_plan: dict) -> dict:
    """Commit failover/cooldown state only after the exact Engine was safely stopped."""
    rcfg = inst.get("retry") or cfg.get_settings().get("retry", {})
    rmax = max(1, int(rcfg.get("max", 3)))
    rint = max(5, int(rcfg.get("interval", 40)))
    now = time.monotonic()
    h = hub.health_for(iid)
    h["frozen_code"] = st["reason_code"]
    h["frozen_reason"] = st["reason"]
    if fast_unanswered:
        cooldown = max(1.0, REG_UNANSWERED_RECOVERY_DELAY_SECONDS)
    else:
        cooldown = (max(20, rint) if st["reason_code"] == "registering"
                    else max(60, rint * 4))
    h["retry_delay"] = cooldown
    h["next_retry_at"] = now + cooldown
    hub.ok_since.pop(str(iid), None)
    action = _commit_exit_failure_plan(str(iid), inst, st, stable_for, exit_plan)
    if (action == failover.GIVE_UP
            or bool((hub.exit_ledgers.get(str(iid)) or {}).get("given_up"))):
        h["next_retry_at"] = None
        h["retry_delay"] = None
    elif action in {failover.BACK_OFF, failover.REPORT, failover.PACE}:
        h["retry_delay"] = failover.EXHAUSTED_RETRY_SECONDS
        h["next_retry_at"] = now + h["retry_delay"]
    asyncio.create_task(hub.drop_ami(str(iid)))
    return _frozen(h, st, rmax)


async def _apply_health_with_recovery(iid: str, inst: dict, st: dict,
                                      container_id: str | None) -> dict:
    """Apply health policy and execute an Engine recovery as one per-line transaction."""
    overlaid = apply_health(iid, inst, st, container_id)
    request = overlaid.pop("_engine_recovery", None)
    if not request:
        return overlaid
    if not container_id:
        return {**overlaid, "detail": {**(overlaid.get("detail") or {}),
                                        "recovery_blocked": "generation_unknown"}}
    lock = hub.recovery_lock(iid)
    async with lock:
        # The sampler may have decided to recover before the deployment owner published its
        # marker and then waited here. Recheck inside the same per-line order point; otherwise
        # stale health work can capture/remove the exact generation now owned by maintenance.
        if _durable_maintenance_pending(str(iid)):
            return _durable_maintenance_status(str(iid))
        # Another status source may have completed the same recovery while this request waited.
        # Reuse its committed result instead of overwriting the frozen status with a stale
        # pre-removal REGISTERING snapshot.
        current_health = hub.health_for(iid)
        if current_health.get("frozen_code"):
            rcfg = inst.get("retry") or cfg.get_settings().get("retry", {})
            return _frozen(current_health, st, max(1, int(rcfg.get("max", 3))))
        current = cfg.get_instance(iid)
        runtime = await hub.runtime.get(iid, force=True)
        if (not current or not current.get("enabled", True)
                or not runtime.get("running")
                or str(runtime.get("container_id") or "") != str(container_id)):
            return {**overlaid, "detail": {**(overlaid.get("detail") or {}),
                                            "recovery_blocked": "generation_changed"}}
        rcfg = inst.get("retry") or cfg.get_settings().get("retry", {})
        rmax = max(1, int(rcfg.get("max", 3)))
        fast_unanswered = bool(request.get("fast_unanswered"))
        if st.get("reason_code") == "reg_unanswered" and not fast_unanswered:
            return _preserve_engine_after_exit_action(
                iid, st, overlaid, "reg_unanswered_rate_limited", rmax)
        stable_for = max(
            0.0, time.monotonic() - hub.ok_since.get(str(iid), time.monotonic()))
        try:
            exit_plan = _plan_exit_failure(str(iid), inst, stable_for)
        except Exception as exc:  # noqa
            log.warning("exit failover judgement failed for line %s: %s", iid, exc)
            return _preserve_engine_after_exit_action(
                iid, st, overlaid, "exit_judgement_failed", rmax)
        action = str(exit_plan["action"])
        if (not fast_unanswered
                and action in {failover.HOLD, failover.REPORT, failover.PACE}):
            _commit_exit_failure_plan(str(iid), inst, st, stable_for, exit_plan)
            return _preserve_engine_after_exit_action(iid, st, overlaid, action, rmax)
        hub.engine_recovering.add(str(iid))
        try:
            result = await asyncio.to_thread(
                engine.capture_and_stop_if_idle, iid, inst,
                f"health-freeze:{st['reason_code']}", container_id)
            if result.get("stopped"):
                return _finalize_health_freeze(
                    iid, inst, st, fast_unanswered=fast_unanswered,
                    stable_for=stable_for, exit_plan=exit_plan)
            reason = str(result.get("status") or "call_state_unknown")
            if reason not in {"active_call", "call_state_unknown", "generation_changed",
                              "missing", "foreign", "error", "quiesce_failed",
                              "quiesce_pending", "quiesce_state_unknown",
                              "quiesce_finalize_failed", "quiesce_restart_race",
                              "restart_policy_disable_failed",
                              "restart_policy_restore_failed"}:
                reason = "call_state_unknown"
            rcfg = inst.get("retry") or cfg.get_settings().get("retry", {})
            ordinary_cooldown = max(60, max(5, int(rcfg.get("interval", 40))) * 4)
            cooldown = (failover.EXHAUSTED_RETRY_SECONDS
                        if reason in {"quiesce_restart_race",
                                      "restart_policy_disable_failed",
                                      "restart_policy_restore_failed"}
                        else ordinary_cooldown)
            current_health["recovery_blocked_generation"] = str(container_id)
            current_health["recovery_blocked_until"] = time.monotonic() + cooldown
            current_health["recovery_blocked_reason"] = reason
            return {**overlaid, "detail": {**(overlaid.get("detail") or {}),
                                            "recovery_blocked": reason,
                                            "recovery_retry_in": int(cooldown)}}
        finally:
            hub.engine_recovering.discard(str(iid))


def apply_health(iid, inst, st, container_id: str | None = None):
    """Overlay bounded auto-retry state. After max attempts of continuous failure (with the
    SIM still present) the engine is stopped and the status frozen to ERROR + reason, until
    the user retries/re-provisions or the card is re-inserted."""
    rcfg = inst.get("retry") or cfg.get_settings().get("retry", {})
    rmax = max(1, int(rcfg.get("max", 3)))
    rint = max(5, int(rcfg.get("interval", 40)))
    h = hub.health_for(iid)
    state = st["state"]
    now = time.monotonic()

    if not inst.get("enabled", True):
        hub.reset_health(iid)
        return {"state": "STOPPED", "label": status_mod.LABELS["STOPPED"],
                "reason_code": "stopped", "reason": "Stopped.", "detail": {},
                "retry": {"count": 0, "max": rmax}}

    if state == "OK":
        # Set once per healthy stretch: this is when the current exit last proved it can
        # carry IMS, and reset_health() below would otherwise lose it.
        hub.ok_since.setdefault(str(iid), time.monotonic())
        # A registration proves the current exit works, so nothing the ledger holds against it
        # still stands. Clearing here is also what lets a reported line report again later.
        if hub.exit_ledgers.pop(str(iid), None) is not None:
            _save_exit_ledgers()
        hub.reset_health(iid)
        st["retry"] = {"count": 0, "max": rmax}
        return st
    if h.get("frozen_code"):
        # PIN/PUK failures require a person. Network, tunnel and IMS failures recover after a
        # cooldown so a brief carrier rejection never leaves WiFi Calling stopped forever.
        if (h.get("frozen_code") not in {"pin_wrong", "pin_blocked", "pin_required"}
                and time.monotonic() >= (h.get("next_retry_at") or float("inf"))
                and not h.get("auto_retrying")):
            h["auto_retrying"] = True
            asyncio.create_task(_auto_recover_instance(
                iid, inst, int(h.get("retry_delay") or max(60, rint * 4)),
                hub.lifecycle_epoch(iid)))
        return _frozen(h, st, rmax)

    # Registration progress and carrier replies belong to Asterisk's registration state
    # machine.  Rebuilding the whole Engine for an ordinary "still registering" sample or a
    # SIP rejection changes the IKE identity/P-CSCF and immediately sends another REGISTER;
    # that used to turn a transient condition into a self-sustaining registration storm.
    #
    # ``reg_unanswered`` is deliberately excluded: it is a complete transaction with no peer
    # response and has its own exact-generation/zero-call bounded recovery below.  Explicit
    # local tunnel/bootstrap failures are excluded too and retain their existing recovery.
    # A pre-existing frozen/PIN state above remains authoritative and is never unlocked by a
    # later observation.  Only a failed destructive recovery for this exact generation is
    # cleared, so the same healthy process is allowed to keep making progress.
    if st.get("reason_code") in {
            "registering", "reg_temporary", "reg_rejected", "local_usim_unavailable"}:
        h["fail_start"] = None
        h["retry_count"] = 0
        if (container_id and h.get("recovery_blocked_generation")
                and str(h["recovery_blocked_generation"]) == str(container_id)):
            h["recovery_blocked_generation"] = None
            h["recovery_blocked_until"] = None
            h["recovery_blocked_reason"] = None
        st["retry"] = {"count": 0, "max": rmax}
        return st
    if state == "STOPPED":
        st["retry"] = {"count": 0, "max": rmax}
        return st
    if state == "NO_CARD":
        # SIM removed/absent -> handled by the card monitor; don't count as a retry.
        h["fail_start"] = None
        h["retry_count"] = 0
        st["retry"] = {"count": 0, "max": rmax}
        return st
    if state == "PIN_PROBLEM":
        # wrong/blocked PIN won't recover by retrying — surface immediately.
        h["frozen_code"] = st["reason_code"]
        h["frozen_reason"] = st["reason"]
        h["next_retry_at"] = None
        return _frozen(h, st, rmax)

    # A failed destructive recovery is fenced by the exact Docker generation. Without this
    # cross-poll gate, every 4/15-second status sample could start another pair of graceful/
    # manual-stop attempts even though each individual helper call is bounded. A genuinely
    # new generation gets a fresh assessment; authoritative OK above clears the gate too.
    blocked_generation = h.get("recovery_blocked_generation")
    if blocked_generation and container_id and str(container_id) != str(blocked_generation):
        h["recovery_blocked_generation"] = None
        h["recovery_blocked_until"] = None
        h["recovery_blocked_reason"] = None
    elif (blocked_generation and container_id
          and str(container_id) == str(blocked_generation)
          and now < float(h.get("recovery_blocked_until") or 0)):
        remaining = max(1, int(float(h["recovery_blocked_until"]) - now))
        st = {**st, "detail": {**(st.get("detail") or {}),
                                "recovery_blocked": (
                                    h.get("recovery_blocked_reason") or "recovery_backoff"),
                                "recovery_retry_in": remaining}}
        st["retry"] = {"count": rmax, "max": rmax}
        return st

    # Asterisk has already spent a complete SIP transaction proving that this established
    # P-CSCF session no longer answers.  If AMI also proves no call is active, skip the generic
    # retry budget and rebuild the exact container generation.  Missing logs, missing AMI, an
    # active call, a missing generation, or the rate limiter all fall through unchanged.
    fast_unanswered = False
    if (st.get("reason_code") == "reg_unanswered"
            and (st.get("detail") or {}).get("active_channels") == 0
            and container_id):
        last_fast = hub.reg_unanswered_recovery_at.get(str(iid), float("-inf"))
        if now - last_fast >= REG_UNANSWERED_MIN_INTERVAL_SECONDS:
            fast_unanswered = True
            hub.reg_unanswered_recovery_at[str(iid)] = now
            # Reuse the established freeze/capture/failover path below.  Backdating fail_start
            # makes this observation exhaust only this reason's retry budget immediately.
            h["fail_start"] = now - (rmax * rint)
            log.warning("line %s IMS registration is unanswered with no active channels; "
                        "fast recovery will rebuild container generation %s", iid,
                        str(container_id)[:12])

    # EPDG_UNRESOLVED / TUNNEL_DOWN / REGISTERING -> the engine keeps retrying internally;
    # we bound the total time and then give up.
    if h["fail_start"] is None:
        h["fail_start"] = now
    elapsed = now - h["fail_start"]
    count = min(rmax, int(elapsed // rint) + 1)
    h["retry_count"] = count
    if elapsed >= rmax * rint:
        # Re-registration can fail while an established call and its RTP still work.  Never
        # remove the Engine unless AMI or the bounded CLI fallback has authoritatively proved
        # that Asterisk has zero live channels.  Unknown is not zero and must fail closed.
        active_channels = (st.get("detail") or {}).get("active_channels")
        if type(active_channels) is not int or active_channels != 0:
            st = {**st, "detail": {**(st.get("detail") or {}),
                                    "recovery_blocked": (
                                        "active_call" if type(active_channels) is int
                                        and active_channels > 0 else "call_state_unknown")}}
            st["retry"] = {"count": rmax, "max": rmax}
            return st
        # The async wrapper owns the destructive transaction.  Do not freeze, judge the exit,
        # or schedule a rebuild until it has rechecked and removed this exact idle generation.
        return {**st, "retry": {"count": rmax, "max": rmax},
                "_engine_recovery": {"fast_unanswered": fast_unanswered}}
    st["retry"] = {"count": count, "max": rmax}
    return st


def _remote_usim_recovery_topology(inst: dict) -> tuple[str, dict] | None:
    """Return the exact live VPCD identity for a local-auth recovery, without probing a SIM.

    Reader names and enumeration indices are transport details, not identity.  The registry is
    the authority that binds an online slot to Agent, reader and card identities learned earlier.
    Native server readers deliberately fail closed here; their physical-port recovery can be
    added only with an equally authoritative snapshot.
    """
    if str(inst.get("reader_port") or "").strip():
        return None
    try:
        slot = int(inst.get("reader_index"))
    except (TypeError, ValueError):
        return None
    record = next((item for item in vpcd_registry.snapshot()
                   if item.get("slot") == slot), None)
    if (not record or record.get("online") is not True
            or record.get("identity_current") is not True
            or not str(record.get("session_generation") or "")
            or str(record.get("identity_session_generation") or "") !=
               str(record.get("session_generation") or "")
            or str(record.get("matched") or "") != str(inst.get("id") or "")
            or not str(record.get("agent_id") or "")
            or not str(record.get("reader_id") or "")
            or not str(record.get("agent_run_id") or "")):
        return None
    for field in ("iccid", "imsi"):
        expected = str(inst.get(field) or "")
        if expected and str(record.get(field) or "") != expected:
            return None
    if not str(record.get("iccid") or record.get("imsi") or ""):
        return None
    identity = {
        "slot": slot,
        "agent_id": str(record["agent_id"]),
        "reader_id": str(record["reader_id"]),
        "agent_run_id": str(record["agent_run_id"]),
        "session_generation": str(record["session_generation"]),
        "matched": str(record["matched"]),
        "iccid": str(record.get("iccid") or ""),
        "imsi": str(record.get("imsi") or ""),
    }
    digest = hashlib.sha256(json.dumps(
        identity, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    return digest, identity


def _same_remote_usim_recovery_topology(inst: dict, expected_digest: str) -> bool:
    current = _remote_usim_recovery_topology(inst)
    return current is not None and hmac.compare_digest(current[0], expected_digest)


_usim_recovery_workers: set[asyncio.Task] = set()


async def _await_usim_recovery_worker(function, /, *args, **kwargs):
    """Keep a cancelled reconciler's recovery lock until its bounded thread really exits."""
    worker = asyncio.create_task(asyncio.to_thread(function, *args, **kwargs))
    _usim_recovery_workers.add(worker)
    cancelled = False
    try:
        while not worker.done():
            try:
                await asyncio.shield(worker)
            except asyncio.CancelledError:
                # Executor threads cannot be cancelled. Repeated cancellation must not let the
                # surrounding ``recovery_lock`` go while this worker can still send REGISTER.
                cancelled = True
        result = worker.result()
    finally:
        if worker.done():
            _usim_recovery_workers.discard(worker)
    if cancelled:
        raise asyncio.CancelledError
    return result


async def _reconcile_usim_auth_recovery(inst: dict) -> None:
    """Reconcile one exact PC/SC-service failure without rebuilding or guessing a reader."""
    iid = str(inst.get("id") or "")
    if (not iid or _durable_maintenance_pending(iid)
            or not engine.usim_recovery_fence_pending(iid)):
        return
    runtime = await hub.runtime.get(iid, force=True)
    if not runtime.get("running"):
        return
    failure = status_mod.current_local_usim_unavailable(
        await asyncio.to_thread(engine.usim_status, iid), runtime)
    if failure is None:
        return
    topology = _remote_usim_recovery_topology(inst)
    allowed, _reason = _line_auto_start_allowed(inst)
    if topology is None or not allowed:
        return
    topology_digest, _identity = topology
    try:
        reservation = await asyncio.to_thread(
            engine.reserve_usim_recovery_attempt, iid,
            container_id=str(runtime.get("container_id") or ""),
            started_at=str(runtime.get("started_at") or ""),
            engine_run_id=str(runtime.get("engine_run_id") or ""),
            auth_seq=failure["auth_seq"], topology_digest=topology_digest)
    except engine.UsimRecoveryStateError as exc:
        key = (iid, str(exc))
        if key not in hub.usim_recovery_diagnostics:
            hub.usim_recovery_diagnostics.add(key)
            log.error("line %s local-auth recovery is fenced: %s", iid, exc)
        return
    if reservation.get("status") != "reserved":
        return
    attempt = reservation["attempt"]

    async with hub.recovery_lock(iid):
        current = await hub.runtime.get(iid, force=True)
        exact = all(current.get(field) == runtime.get(field)
                    for field in ("container_id", "started_at", "engine_run_id"))
        if (not exact or _durable_maintenance_pending(iid)
                or await _pcscf_rebind_pending(iid)
                or not _same_remote_usim_recovery_topology(inst, topology_digest)):
            return
        current_failure = status_mod.current_local_usim_unavailable(
            await asyncio.to_thread(engine.usim_status, iid), current)
        if current_failure != failure:
            return
        ami = await hub.ami_for(iid, current)
        if ami is None or not ami.connected:
            return
        protocol = getattr(getattr(ami, "_mgr", None), "protocol", None)
        if protocol is None:
            return
        # Refresh before handing off. The worker obtains the P-CSCF flock first, then asks this
        # event loop for the complete AMI zero-channel snapshot while ordinary submissions fail
        # closed on both the flock and the Engine-published local-auth fence.
        refreshed = await hub.runtime.get(iid, force=True)
        if any(refreshed.get(field) != runtime.get(field)
               for field in ("container_id", "started_at", "engine_run_id")):
            return
        loop = asyncio.get_running_loop()

        async def zero_channel_snapshot():
            if (not ami.connected
                    or getattr(getattr(ami, "_mgr", None), "protocol", None) is not protocol):
                return False
            result = await ami.zero_usim_recovery_call_channels_complete(timeout=2.0)
            registration = await ami.registration_state()
            return (result is True and registration in {"Rejected", "Unregistered"}
                    and ami.connected
                    and getattr(getattr(ami, "_mgr", None), "protocol", None) is protocol)

        def zero_channels():
            future = asyncio.run_coroutine_threadsafe(zero_channel_snapshot(), loop)
            try:
                # The exact snapshot contains two bounded AMI commands: zero channels (2s)
                # followed by current registration state (3s). Keep one scheduling margin.
                return future.result(timeout=5.5) is True
            except Exception:
                future.cancel()
                return False

        def before_exec():
            return (ami.connected
                    and getattr(getattr(ami, "_mgr", None), "protocol", None) is protocol
                    and not _durable_maintenance_pending(iid)
                    and _same_remote_usim_recovery_topology(inst, topology_digest)
                    and engine.usim_recovery_transport_ready(
                        iid, str(runtime["engine_run_id"])))

        try:
            result = await _await_usim_recovery_worker(
                engine.submit_usim_recovery_register, iid,
                container_id=str(runtime["container_id"]),
                started_at=str(runtime["started_at"]),
                engine_run_id=str(runtime["engine_run_id"]),
                auth_seq=failure["auth_seq"], attempt=attempt,
                topology_digest=topology_digest, zero_channels=zero_channels,
                before_exec=before_exec)
        except engine.UsimRecoveryStateError as exc:
            log.error("line %s local-auth recovery submission fenced: %s", iid, exc)
            return
        if result.get("submitted"):
            log.warning("line %s submitted one bounded IMS re-registration after a local "
                        "PC/SC interruption", iid)
            hub.status_wakeup.set()


async def usim_auth_recovery_reconciler() -> None:
    """Dedicated marker reconciler; HTTP/status reads never submit IMS registration."""
    while True:
        for inst in cfg.list_instances():
            try:
                await _reconcile_usim_auth_recovery(inst)
            except asyncio.CancelledError:
                raise
            except Exception as exc:  # noqa
                log.debug("local-auth recovery sample failed line=%s: %r",
                          inst.get("id"), exc)
        await asyncio.sleep(USIM_RECOVERY_SCAN_SECONDS)


_lifespan_users = 0
_lifespan_tasks: list[asyncio.Task] = []


@asynccontextmanager
async def lifespan(app: FastAPI):
    global _lifespan_users, _lifespan_tasks
    _lifespan_users += 1
    # run.py serves HTTP and HTTPS with the same FastAPI object. Uvicorn enters its lifespan
    # twice; background hardware/SMS/call pollers must still have exactly one owner.
    if _lifespan_users > 1:
        try:
            yield
        finally:
            control_lifecycle.begin_shutdown()
            _lifespan_users -= 1
            if _lifespan_users == 0:
                await _shutdown_background_tasks()
        return
    store.init()
    # Legacy history used a free-form line name. Map only unique, non-numeric current names;
    # numeric ids are reusable and therefore unsafe to guess across deleted/recreated lines.
    aliases: dict[str, list[str]] = {}
    for item in cfg.list_instances():
        alias = re.sub(r"[^a-z0-9]+", "", str(item.get("name") or "").lower())
        if alias and not alias.isdigit():
            aliases.setdefault(alias, []).append(str(item["id"]))
    unique_aliases = {alias: ids[0] for alias, ids in aliases.items() if len(ids) == 1}
    migrated = store.migrate_legacy_history(unique_aliases)
    if migrated["calls"] or migrated["messages"]:
        log.info("merged legacy history: %d call(s), %d message(s)",
                 migrated["calls"], migrated["messages"])
    # Re-publish after every manager restart so the host orchestrator can reconstruct routes and
    # modem services from persistent config without waiting for a settings edit/line restart.
    egress.publish()
    await hub.runtime.start(hub.runtime_changed)
    _lifespan_tasks = [
        asyncio.create_task(status_poller()), asyncio.create_task(card_monitor()),
        asyncio.create_task(usim_auth_recovery_reconciler()),
        asyncio.create_task(cellular_sms_poller()), asyncio.create_task(remote_call_poller()),
        asyncio.create_task(cellular_call_lease_recovery()),
        asyncio.create_task(remote_modem_reconciler()),
        asyncio.create_task(host_health_poller()),
        asyncio.create_task(agent_health_poller()),
        asyncio.create_task(allowance_reminder_poller()),
    ]
    try:
        yield
    finally:
        control_lifecycle.begin_shutdown()
        _lifespan_users -= 1
        if _lifespan_users == 0:
            await _shutdown_background_tasks()


async def _shutdown_background_tasks():
    global _lifespan_tasks
    log.info("control shutdown phase=pollers begin count=%d", len(_lifespan_tasks))
    for task in _lifespan_tasks:
        task.cancel()
    # Reap the cancelled tasks (the monitor may be parked in a to_thread wait for up to
    # its timeout; awaiting keeps shutdown deterministic instead of leaking the error).
    await asyncio.gather(*_lifespan_tasks, return_exceptions=True)
    _lifespan_tasks = []
    log.info("control shutdown phase=pollers end")
    # A cancelled reconciler keeps its per-line recovery lock until its bounded executor worker
    # exits. Normally the gather above already proves this set empty; retain an explicit shutdown
    # join so AMI/Docker clients are never closed under a still-capable REGISTER worker.
    if _usim_recovery_workers:
        log.info("control shutdown phase=usim_workers begin count=%d",
                 len(_usim_recovery_workers))
        await asyncio.gather(*tuple(_usim_recovery_workers), return_exceptions=True)
    log.info("control shutdown phase=usim_workers end")
    sms_tasks = [entry.get("task") for entry in hub.sms_submission_tasks.values()
                 if entry.get("task") and not entry["task"].done()]
    log.info("control shutdown phase=sms begin count=%d", len(sms_tasks))
    if sms_tasks:
        _done, pending_sms = await asyncio.wait(sms_tasks, timeout=195.0)
        if pending_sms:
            # Never cancel an in-flight paid action. Its durable pending row makes startup and
            # maintenance fail closed; store.init resolves that row to unknown after restart.
            log.critical("shutdown left %d bounded SMS submission(s) unresolved",
                         len(pending_sms))
    log.info("control shutdown phase=sms end")
    # Browser VoWiFi calls have an Asterisk-local 10s expiry even if Control crashes. During a
    # controlled stop, also attempt the exact uniqueid hangup before any longer cellular
    # quarantine wait and before AMI clients are closed.
    log.info("control shutdown phase=softphone begin")
    await _shutdown_softphone_call_leases()
    log.info("control shutdown phase=softphone end")
    sessions = call_media.manager.sessions()
    log.info("control shutdown phase=cellular begin count=%d", len(sessions))
    if sessions:
        await asyncio.gather(
            *(_finalize_abandoned_cellular_media(session) for session in sessions),
            return_exceptions=True)
        termination = [session.termination_task for session in sessions
                       if session.termination_task and not session.termination_task.done()]
        if termination:
            try:
                await asyncio.wait_for(
                    asyncio.gather(*termination, return_exceptions=True), timeout=61)
            except asyncio.TimeoutError:
                log.error("shutdown timed out waiting for cellular call termination")
        # Any remaining entry is a committed/unknown call whose termination could not be
        # confirmed. Do not pretend that raw media deletion is a successful hangup.
        for session in call_media.manager.sessions():
            log.critical(
                "cellular call termination unconfirmed at shutdown: instance=%s call=%s state=%s",
                session.instance_iid, session.call_id[:8], session.release_state or "unknown")
    log.info("control shutdown phase=cellular end")
    log.info("control shutdown phase=runtime begin")
    await hub.runtime.close()
    log.info("control shutdown phase=runtime end")
    log.info("control shutdown phase=ami begin count=%d", len(hub.ami))
    for c in hub.ami.values():
        await c.close()
    log.info("control shutdown phase=ami end")
    log.info("control shutdown phase=docker_client begin")
    await asyncio.to_thread(engine.close_client)
    log.info("control shutdown phase=docker_client end")
    log.info("control shutdown cleanup complete")


app = FastAPI(title="MDD Sim Gateway", lifespan=lifespan)


class ContextPathMiddleware:
    """Seamlessly strip /mdd prefix for all HTTP and WebSocket requests."""
    def __init__(self, app):
        self.app = app

    async def __call__(self, scope, receive, send):
        if scope["type"] in ("http", "websocket"):
            raw_path = scope.get("path", "")
            if raw_path == "/mdd":
                scope["path"] = "/"
                scope["root_path"] = "/mdd"
            elif raw_path.startswith("/mdd/"):
                scope["path"] = raw_path[4:]
                scope["root_path"] = "/mdd"
        await self.app(scope, receive, send)


app.add_middleware(ContextPathMiddleware)

_AUTH_PUBLIC = {"/api/auth/status", "/api/auth/setup", "/api/auth/login"}


def _auth_path(path: str) -> str:
    """Normalize the public context prefix before applying management authentication."""
    if path == "/mdd":
        return "/"
    return path[4:] if path.startswith("/mdd/") else path


@app.middleware("http")
async def require_admin_session(request: Request, call_next):
    """Protect every management API and require CSRF on state changes.

    The engine callback is authenticated separately with the per-install internal token.
    Static assets remain public so the browser can render the login screen.
    """
    path = _auth_path(request.url.path)
    if not path.startswith("/api/") or path in _AUTH_PUBLIC:
        return await call_next(request)
    if path == "/api/engine/event":
        expected = cfg.internal_event_token()
        supplied = request.headers.get("x-mdd-engine-token", "")
        if not expected or not hmac.compare_digest(supplied, expected):
            return JSONResponse({"detail": "invalid engine token"}, status_code=401)
        return await call_next(request)

    auth_hdr = request.headers.get("authorization", "")
    bearer_token = auth_hdr[7:].strip() if auth_hdr.startswith("Bearer ") else ""
    token = request.headers.get("x-mdd-session") or bearer_token or request.cookies.get(auth.SESSION_COOKIE)
    current = auth.session(token)
    if not current:
        return JSONResponse({"detail": "authentication required"}, status_code=401)
    if request.method in {"POST", "PUT", "PATCH", "DELETE"}:
        supplied = request.headers.get("x-mdd-csrf-token", "")
        if not hmac.compare_digest(supplied, current["csrf"]):
            return JSONResponse({"detail": "invalid CSRF token"}, status_code=403)
    request.state.admin_session = current
    return await call_next(request)


_MAINTENANCE_HANGUP_PATH = re.compile(
    r"^/api/instances/([^/]+)/(?:hangup|cellular-call/hangup|"
    r"cellular-call/[^/]+/release|calls/[^/]+/hangup)$")
_MAINTENANCE_LINE_MUTATION = re.compile(r"^/api/instances/([^/]+)(?:/|$)")


@app.middleware("http")
async def fence_maintenance_mutations(request: Request, call_next):
    """Deny new/mutating work while a durable deployment owner controls the line.

    Hangup is deliberately exempt: maintenance must never prevent an existing charged call
    from terminating. Engine callbacks are authenticated by the inner middleware and remain
    available so already-accepted carrier events can drain before the owner advances.
    """
    if request.method not in {"POST", "PUT", "PATCH", "DELETE"}:
        return await call_next(request)
    path = _auth_path(request.url.path)
    if path == "/api/engine/event" or _MAINTENANCE_HANGUP_PATH.fullmatch(path):
        return await call_next(request)
    line_match = _MAINTENANCE_LINE_MUTATION.match(path)
    blocked = engine.global_maintenance_pending()
    if line_match and not blocked:
        blocked = engine.engine_maintenance_pending(line_match.group(1))
    if blocked:
        return JSONResponse({"detail": {
            "code": "maintenance_in_progress",
            "message": "A durable maintenance transaction is in progress; no new work was accepted.",
        }}, status_code=503)
    return await call_next(request)


@app.get("/api/auth/status")
def api_auth_status(request: Request):
    auth_hdr = request.headers.get("authorization", "")
    bearer_token = auth_hdr[7:].strip() if auth_hdr.startswith("Bearer ") else ""
    token = request.headers.get("x-mdd-session") or bearer_token or request.cookies.get(auth.SESSION_COOKIE)
    current = auth.session(token)
    return {"configured": auth.configured(), "authenticated": bool(current),
            "username": auth.username(),
            "token": token if current else "",
            "csrf": current.get("csrf") if current else ""}


def _is_secure_request(request: Request) -> bool:
    """Check whether request was made over HTTPS (directly or via reverse proxy)."""
    forwarded = request.headers.get("x-forwarded-proto", "").lower()
    if forwarded == "https":
        return True
    if forwarded == "http":
        return False
    return request.url.scheme == "https"


@app.post("/api/auth/setup")
def api_auth_setup(body: dict, request: Request):
    if auth.configured():
        raise HTTPException(409, "administrator account is already configured")
    try:
        auth.setup(str(body.get("password") or ""), str(body.get("username") or "admin"))
    except ValueError as exc:
        raise HTTPException(400, str(exc)) from exc
    result = auth.login(str(body.get("username") or "admin"), str(body.get("password") or ""),
                        request.client.host if request.client else "")
    token, csrf = result
    response = JSONResponse({"ok": True, "authenticated": True, "token": token, "csrf": csrf})
    is_secure = _is_secure_request(request)
    response.set_cookie(auth.SESSION_COOKIE, token, max_age=auth.SESSION_TTL, httponly=True,
                        secure=is_secure, samesite="lax", path="/")
    return response


@app.post("/api/auth/login")
def api_auth_login(body: dict, request: Request):
    if not auth.configured():
        raise HTTPException(409, "administrator setup is required")
    peer = request.client.host if request.client else ""
    retry = auth.throttled(peer)
    if retry:
        return JSONResponse({"detail": "too many attempts", "retry_after": retry},
                            status_code=429, headers={"Retry-After": str(retry)})
    result = auth.login(str(body.get("username") or "admin"), str(body.get("password") or ""), peer)
    if not result:
        raise HTTPException(401, "invalid username or password")
    token, csrf = result
    response = JSONResponse({"ok": True, "authenticated": True, "token": token, "csrf": csrf})
    is_secure = _is_secure_request(request)
    response.set_cookie(auth.SESSION_COOKIE, token, max_age=auth.SESSION_TTL, httponly=True,
                        secure=is_secure, samesite="lax", path="/")
    return response


@app.post("/api/auth/logout")
def api_auth_logout(request: Request):
    auth.logout(request.cookies.get(auth.SESSION_COOKIE))
    response = JSONResponse({"ok": True})
    response.delete_cookie(auth.SESSION_COOKIE, path="/")
    return response


@app.post("/api/auth/password")
def api_auth_password(body: dict, request: Request):
    try:
        auth.change_password(str(body.get("current_password") or ""),
                             str(body.get("new_password") or ""))
    except ValueError as exc:
        raise HTTPException(400, str(exc)) from exc
    response = JSONResponse({"ok": True, "reauthenticate": True})
    response.delete_cookie(auth.SESSION_COOKIE, path="/")
    return response


def _audit_client(request: Request, settings: dict) -> str:
    peer = request.client.host if request.client else ""
    trusted = (settings.get("security") or {}).get("trusted_proxies") or []
    try:
        address = ipaddress.ip_address(peer)
        allowed = any(address in ipaddress.ip_network(str(item), strict=False) for item in trusted)
    except ValueError:
        allowed = False
    if allowed:
        forwarded = request.headers.get("x-forwarded-for", "").split(",", 1)[0].strip()
        try:
            return str(ipaddress.ip_address(forwarded))
        except ValueError:
            pass
    return peer


@app.middleware("http")
async def audit_mutations(request: Request, call_next):
    response = await call_next(request)
    if request.method in {"POST", "PUT", "PATCH", "DELETE"} and request.url.path.startswith("/api/"):
        settings = cfg.get_settings()
        _write_audit_record({"at": int(time.time()), "method": request.method,
                             "path": request.url.path, "status": response.status_code,
                             "client": _audit_client(request, settings)}, settings)
    return response


def _write_audit_record(record: dict, settings: dict | None = None) -> None:
    """Append one administrative action from the authenticated control surface."""
    settings = settings if settings is not None else cfg.get_settings()
    if not (settings.get("security") or {}).get("audit_enabled", True):
        return
    path = os.path.join(cfg.DATA_DIR, "audit", "operations.jsonl")
    try:
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "a", encoding="utf-8") as handle:
            handle.write(json.dumps(record, ensure_ascii=False) + "\n")
    except OSError:
        log.warning("could not write administrative audit record")


# ----------------------------- SIM / readers -----------------------------
@app.get("/api/readers")
def api_readers():
    try:
        return {"readers": sim.list_readers(), "stale": False}
    except Exception as exc:  # noqa
        # The card monitor already owns a last-known, index-sorted view. Returning it is
        # safer than telling the UI there are zero readers after one transient pcsc-lite
        # context failure. The stale flag asks the browser to retry in the background.
        cached = [str(item.get("name") or "") for item in hub.cards_list()
                  if item.get("name")]
        if cached:
            log.warning("PC/SC reader enumeration temporarily unavailable; using %d cached readers: %r",
                        len(cached), exc)
            return {"readers": cached, "stale": True}
        log.warning("PC/SC reader enumeration unavailable and no cache exists: %r", exc)
        raise HTTPException(503, "card readers are temporarily unavailable") from exc


@app.get("/api/sim/detect")
async def api_sim_detect(reader_index: int = 0):
    rlist = await asyncio.to_thread(sim.list_readers)
    if reader_index < 0 or reader_index >= len(rlist):
        raise HTTPException(400, "reader index out of range")
    name = rlist[reader_index]
    async with hub.reader_lock(name):
        return await asyncio.to_thread(
            lambda: _client_card_info(sim.read_card(reader_index).dict()))


def _resolve_reader_index(body: dict) -> int:
    """Resolve the target reader for index-taking SIM APIs. When the caller supplies the
    physical reader NAME we re-resolve the index at request time. A configured line's
    ``reader`` value may instead be a logical selector (``imsi:...``), which must never be
    mistaken for a PC/SC name. Prefer stable port/card identity over a cached enumeration
    index so hotplug cannot redirect a PIN or provisioning request to another SIM."""
    rlist = sim.list_readers()
    if not rlist:
        raise HTTPException(409, "no PC/SC readers connected")
    try:
        cached_idx = int(body.get("reader_index", 0))
    except (TypeError, ValueError):
        cached_idx = 0
    rname = str(body.get("reader") or "").strip()
    logical_selector = rname.startswith(("imsi:", "iccid:"))

    if rname and not logical_selector:
        if rname not in rlist:
            raise HTTPException(409, "the selected card reader is no longer connected")
        return rlist.index(rname)

    port = str(body.get("reader_port") or "").strip()
    if port:
        try:
            port_idx = usbreader.index_for_port(port)
        except Exception as exc:  # noqa
            log.debug("request port->index resolve failed for %s: %r", port, exc)
            port_idx = None
        if port_idx is not None and 0 <= int(port_idx) < len(rlist):
            return int(port_idx)

    iccid = str(body.get("iccid") or "").strip()
    imsi = str(body.get("imsi") or "").strip()
    if rname.startswith("iccid:"):
        iccid = rname.split(":", 1)[1].strip()
    elif rname.startswith("imsi:"):
        imsi = rname.split(":", 1)[1].strip()
    if iccid or imsi:
        for card_info in hub.cards.values():
            if not card_info.get("present"):
                continue
            if ((iccid and str(card_info.get("iccid") or "") == iccid)
                    or (imsi and str(card_info.get("imsi") or "") == imsi)):
                idx = card_info.get("index")
                if idx is not None and 0 <= int(idx) < len(rlist):
                    return int(idx)
        raise HTTPException(409, "the SIM for this line is no longer connected")

    if cached_idx < 0 or cached_idx >= len(rlist):
        raise HTTPException(409, "the selected card reader is no longer connected")
    return cached_idx


@app.post("/api/sim/verify-pin")
async def api_verify_pin(body: dict):
    idx = await asyncio.to_thread(_resolve_reader_index, body)
    rlist = await asyncio.to_thread(sim.list_readers)
    name = rlist[idx] if 0 <= idx < len(rlist) else ""
    async with hub.reader_lock(name or f"idx:{idx}"):
        res = await asyncio.to_thread(sim.verify_pin, body["pin"], idx)
        if res.get("ok"):
            # PIN now satisfied — re-read the (previously locked) IMSI + SMSC and refresh the
            # detected-card entry so the dashboard can move from "locked" to "ready to provision".
            try:
                c = await asyncio.to_thread(sim.read_card, idx, body["pin"])
                # Key strictly by the reader NAME the read actually used — an index-keyed
                # lookup could merge this card's identity into a stale entry of a reader
                # that was just unplugged.
                card_entry = hub.cards.get(c.reader) or {"index": idx, "name": c.reader,
                                                         "present": True}
                card_entry.update(present=True, iccid=c.iccid, imsi=c.imsi, mcc=c.mcc,
                                  mnc=c.mnc, mnc_len=getattr(c, "mnc_len", None),
                                  pin_enabled=c.pin_enabled, pin_tries=c.pin_tries,
                                  smsc=c.smsc, carrier_identity=_carrier_identity(c))
                inst = _match_instance_by_iccid(c.iccid)
                if inst and _carrier_identity_update(c):
                    await asyncio.to_thread(cfg.upsert_instance, {
                        "id": str(inst["id"]), **_carrier_identity_update(c)})
                card_entry["matched"] = inst["id"] if inst else None
                hub.cards[c.reader] = card_entry
                res["card"] = _client_card_info(card_entry)
                await hub.broadcast({"type": "cards", "cards": _client_cards()})
            except Exception as e:  # noqa
                log.debug("post-verify re-read failed: %r", e)
    return res


@app.post("/api/sim/change-pin")
async def api_change_pin(body: dict):
    idx = await asyncio.to_thread(_resolve_reader_index, body)
    rlist = await asyncio.to_thread(sim.list_readers)
    name = rlist[idx] if 0 <= idx < len(rlist) else f"idx:{idx}"
    async with hub.reader_lock(name):
        return await asyncio.to_thread(sim.change_pin, body["old"], body["new"], idx)


@app.post("/api/sim/pin-enabled")
async def api_pin_enabled(body: dict):
    idx = await asyncio.to_thread(_resolve_reader_index, body)
    rlist = await asyncio.to_thread(sim.list_readers)
    name = rlist[idx] if 0 <= idx < len(rlist) else f"idx:{idx}"
    async with hub.reader_lock(name):
        return await asyncio.to_thread(
            sim.set_pin_enabled, body["pin"], bool(body["enabled"]), idx)


def _refresh_card_matches():
    """Recompute each detected card's matched instance against current config. Only for
    entries whose ICCID is known — entries mapped via a running engine's pin_status
    (identity not probed) must keep that match instead of being wiped to None."""
    for c in hub.cards.values():
        if c.get("present") and c.get("iccid"):
            inst = _match_instance_by_iccid(c.get("iccid"))
            c["matched"] = inst["id"] if inst else None



def _esim_resolve_reader(reader_index: int | None = None, reader: str | None = None) -> tuple[str, int]:
    """Resolve (reader_name, index) for eSIM APIs. Prefer NAME when provided."""
    rlist = sim.list_readers()
    if not rlist:
        raise HTTPException(409, "no PC/SC readers connected")
    if reader:
        if reader not in rlist:
            raise HTTPException(409, f"reader '{reader}' is no longer connected")
        return reader, rlist.index(reader)
    idx = 0 if reader_index is None else int(reader_index)
    if idx < 0 or idx >= len(rlist):
        raise HTTPException(400, "reader index out of range")
    return rlist[idx], idx


def _esim_imei_for_reader(name: str, override: str | None = None) -> str:
    if override and str(override).strip():
        return str(override).strip()
    entry = hub.cards.get(name) or {}
    matched = entry.get("matched")
    if matched:
        inst = cfg.get_instance(matched)
        if inst and inst.get("imei"):
            return str(inst["imei"])
    # A profile switch changes the ICCID before a matching line exists. Native readers keep
    # their configured device identity in devices-hardware.json, so downloads and automatic
    # provisioning must still be able to resolve that physical reader's IMEI in this gap.
    hardware_imei, _device_id, _device_type = _hardware_imei_for_card(entry)
    if hardware_imei:
        return hardware_imei
    return ""


def _esim_resolve_se(
    name: str,
    idx: int,
    se_id: str | None = None,
    aid: str | None = None,
    *,
    require: bool = False,
) -> dict:
    """Resolve which ISD-R / SE to target. Dual-SE cards need se_id or aid when require=True."""
    ses = estkme.discover_ses(name, idx)
    try:
        if require and len(ses) > 1 and not (se_id or aid):
            raise KeyError("eUICC SE is required for dual-SE cards")
        return estkme.resolve_se(ses, se_id=se_id, aid=aid)
    except KeyError as e:
        raise HTTPException(400, str(e)) from e


def _esim_guard_engine(name: str):
    """Refuse LPA while a VoWiFi engine holds the card (lpac needs exclusive PC/SC)."""
    inst = _find_running_by_reader(name)
    if inst is not None:
        raise HTTPException(
            409,
            f"Line {inst.get('id')} is running on this reader — stop it before eSIM operations",
        )


async def _esim_refresh_card(name: str, idx: int):
    """Re-probe USIM identity after profile enable/disable/download and broadcast."""
    info = hub.cards.get(name) or {"index": idx, "name": name, "present": True}
    try:
        c = await asyncio.to_thread(sim.read_card, idx)
        info.update(
            present=True, index=idx, name=name,
            iccid=c.iccid, imsi=c.imsi, mcc=c.mcc, mnc=c.mnc,
            mnc_len=getattr(c, "mnc_len", None),
            pin_enabled=c.pin_enabled, pin_tries=c.pin_tries, smsc=c.smsc,
            carrier_identity=_carrier_identity(c),
        )
        inst = _match_instance_by_iccid(c.iccid)
        if inst and _carrier_identity_update(c):
            inst = await asyncio.to_thread(cfg.upsert_instance, {
                "id": str(inst["id"]), **_carrier_identity_update(c)})
        if not inst and c.iccid and not cfg.card_auto_create_suppressed(c.iccid):
            # REFRESH arrives while lpa_busy is set, so the normal card-insert callback
            # deliberately keeps the previous ICCID and cannot create/start the newly active
            # profile. Do it from the authoritative post-LPA probe instead.
            inst = await asyncio.to_thread(_ensure_card_draft, info)
        info["matched"] = inst["id"] if inst else None
    except Exception as e:  # noqa
        log.debug("post-LPA card refresh failed: %r", e)
        info.update(index=idx, name=name, present=True)
    hub.cards[name] = info
    await hub.broadcast({"type": "cards", "cards": _client_cards()})
    if info.get("matched"):
        asyncio.create_task(_auto_start_hotplugged_line(str(info["matched"])))
    return info


async def _esim_run(name: str, idx: int, coro, *, refresh: bool = False):
    """Serialize an LPA call: engine gate + per-reader lock + lpa_busy + optional refresh."""
    await asyncio.to_thread(_esim_guard_engine, name)
    async with hub.reader_lock(name):
        hub.lpa_busy[name] = True
        try:
            result = await coro
            if refresh:
                await _esim_refresh_card(name, idx)
            return result
        except lpa.LpaError as e:
            raise HTTPException(400, e.user_message()) from e
        except FileNotFoundError as e:
            raise HTTPException(503, str(e)) from e
        finally:
            hub.lpa_busy.pop(name, None)


@app.get("/api/cards")
async def api_cards():
    """Physically detected readers/cards (from the real-time monitor)."""
    if not hub.scanned:
        # The monitor hasn't finished its first scan yet (manager just started) — answer
        # from a live reader scan so the UI never sees a false "no readers" flash. Map
        # present cards to running engines by pin_status reader name (no card access).
        def scan():
            out = []
            for st in card.reader_states() or []:
                inst = _find_running_by_reader(st["name"]) if st["present"] else None
                out.append({**st,
                            "iccid": inst.get("iccid") if inst else None,
                            "imsi": inst.get("imsi") if inst else None,
                            "matched": inst["id"] if inst else None,
                            "pin_enabled": None, "pin_tries": None})
            return out
        cards = await asyncio.to_thread(scan)
        return {"cards": _with_detected_imei(vpcd_registry.enrich_cards(cards))}
    _refresh_card_matches()
    return {"cards": _client_cards()}


@app.get("/api/ports/suggest")
def api_ports_suggest():
    """Preview the SIP port the automatic allocator would pick for a NEW line right now
    (conflict-checked against other lines + live host listeners). Lets the manual-port UI
    show a sensible default and the auto option show what it will use."""
    try:
        block = cfg.alloc_ports_auto(cfg.load())
        return {"auto_sip_udp": block["sip_udp"], "auto_sip_tls": block["sip_tls"],
                "min": cfg.MIN_USER_PORT, "max": cfg.MAX_USER_PORT}
    except Exception as e:  # noqa
        raise HTTPException(409, f"no free port block: {e}")


def _reader_index_for_instance(inst: dict) -> int | None:
    """Resolve the PC/SC reader index this instance should address, preferring the STABLE
    physical USB port binding over the (unstable) enumeration index/ICCID.

    Priority:
      1. inst.reader_port -> live index via the USB port map. This is authoritative: it sticks
         to the physical reader socket even when pcscd flips the indices of two identical
         readers. It does not require the card to be readable/matched by the monitor.
      2. ICCID match against the live card monitor (works once the card's identity is known).
    Returns None if neither resolves (card/reader not present)."""
    # A VPCD multi-slot bridge intentionally exposes the same physical SIM on
    # several logical readers. ICCID matching is therefore ambiguous; preserve
    # the dedicated SWu slot selected by the instance instead of collapsing to
    # the first matching slot (which is reserved for pin_keeper).
    swu_name = inst.get("swu_reader")
    if swu_name:
        for c in hub.cards.values():
            if c.get("name") == swu_name:
                return c.get("index")
    if "pin_reader" in inst and "ami_reader" in inst:
        try:
            return int(inst.get("reader_index", 1))
        except (TypeError, ValueError):
            return 1
    port = inst.get("reader_port")
    if port:
        try:
            idx = usbreader.index_for_port(port)
        except Exception as e:  # noqa
            log.debug("port->index resolve failed for %s: %r", port, e)
            idx = None
        if idx is not None:
            return idx
    iccid = inst.get("iccid")
    for c in hub.cards.values():
        if c.get("present") and iccid and c.get("iccid") == iccid:
            return c.get("index")
    return None


def _reader_port_for_instance(inst: dict) -> str | None:
    """The stable USB port path this instance's SIM currently sits at. Resolved from the live
    card monitor by ICCID (the port is captured per-reader on each scan). Used to (re)learn /
    refresh a line's reader_port binding at start time so it self-heals if the SIM was moved."""
    iccid = inst.get("iccid")
    if iccid:
        for c in hub.cards.values():
            if c.get("present") and c.get("iccid") == iccid and c.get("reader_port"):
                return c.get("reader_port")
    return None


def _card_identity_mismatch(inst: dict) -> dict | None:
    """Detect that the reader this line uses now holds a DIFFERENT SIM identity — the
    signature of an eSIM profile switch (enable/disable/download changes the eUICC's
    active profile, so the same physical reader re-enumerates with a new ICCID/IMSI).

    Starting the line anyway is what used to break things: the engine grabs whatever
    card is in the reader, runs EAP-AKA with the OLD line's IMSI against the NEW
    profile's keys, the carrier rejects it (tunnel_sim_auth), and the bounded retry
    loop stops the container. Refuse the start up-front with a structured error
    instead. Only a positive, known conflict blocks — absent readers/unknown ICCIDs
    keep the existing fail-open behavior (engine start surfaces NO_CARD as before)."""
    want = (inst.get("iccid") or "").strip()
    if not want:
        return None
    if _reader_index_for_instance(inst) is not None:
        return None      # this line's SIM/profile is present somewhere — all good
    # Prefer the stable USB port binding when present; fall back to stored index.
    port = (inst.get("reader_port") or "").strip()
    idx = inst.get("reader_index")
    for c in hub.cards.values():
        if not c.get("present"):
            continue
        if port and c.get("reader_port") == port:
            pass
        elif not port and c.get("index") == idx:
            pass
        else:
            continue
        got = (c.get("iccid") or "").strip()
        if got and got != want:
            return {"reader": c.get("name") or (f"USB {port}" if port else f"reader {idx}"),
                    "iccid": got}
    return None


def _raise_card_mismatch(inst: dict, mism: dict):
    raise HTTPException(409, {
        "code": "card_mismatch",
        "reader": mism["reader"],
        "card_iccid": mism["iccid"],
        "line_iccid": inst.get("iccid") or "",
        "message": (f"The card in {mism['reader']} now has a different identity "
                    f"(ICCID {mism['iccid']}; this line expects {inst.get('iccid')}). "
                    "This usually means the eSIM profile was switched. Provision the "
                    "active profile as its own line, or switch the eSIM back to this "
                    "profile, then start again."),
    })


def _preflight_pin_locked(inst: dict, idx: int) -> dict:
    """PIN preflight body — caller must already hold the reader asyncio.Lock.
    Sync so it can run under asyncio.to_thread (PC/SC is blocking)."""
    try:
        probe = sim.read_card(idx)          # no VERIFY: learns pin_enabled + presence
    except Exception as e:  # noqa
        log.debug("preflight probe failed: %r", e)
        return {"ok": True, "need_pin": bool(inst.get("pin"))}
    if not probe.present:
        if probe.error:
            return {"ok": True, "need_pin": bool(inst.get("pin"))}
        return {"ok": False, "code": "no_card"}
    if probe.pin_enabled is False:
        return {"ok": True, "need_pin": False}
    saved = inst.get("pin")
    if not saved:
        return {"ok": False, "code": "pin_required", "tries": probe.pin_tries}
    try:
        chk = sim.read_card(idx, saved)
    except Exception as e:  # noqa
        log.debug("preflight verify failed: %r", e)
        return {"ok": True, "need_pin": True}     # couldn't verify now; let the engine try
    if chk.error and "PIN" in (chk.error or "").upper():
        return {"ok": False, "code": "pin_invalid", "clear": True, "tries": chk.pin_tries}
    return {"ok": True, "need_pin": True}


async def _preflight_pin(inst: dict) -> dict:
    """Actively check the SIM's PIN state BEFORE starting the engine (so we never spin up
    the SWu tunnel/IMS against a locked card). Reads the physical card:
      - card absent                         -> {ok:False, code:'no_card'}
      - PIN not required (disabled)          -> {ok:True,  need_pin:False}
      - PIN required, no saved PIN           -> {ok:False, code:'pin_required'}
      - PIN required, saved PIN verifies     -> {ok:True,  need_pin:True}
      - PIN required, saved PIN wrong/blocked -> {ok:False, code:'pin_invalid', clear:True}
    On 'pin_invalid' the saved PIN is stale and should be cleared so the user re-enters it.
    If the card can't be located/read we fail OPEN (ok:True) rather than block a start that
    might otherwise work (e.g. an engine already holds the card)."""
    idx = _reader_index_for_instance(inst)
    if idx is None:
        # Card not seen by the monitor — could be held by a running engine, or truly gone.
        # Don't block here; engine start + status FSM will surface NO_CARD if it's absent.
        return {"ok": True, "need_pin": bool(inst.get("pin"))}
    # Skip while LPA owns the reader (exclusive PC/SC) — let the engine try later.
    rlist = await asyncio.to_thread(sim.list_readers)
    rname = rlist[idx] if 0 <= idx < len(rlist) else None
    if rname and hub.lpa_busy.get(rname):
        return {"ok": True, "need_pin": bool(inst.get("pin"))}
    lock = hub.reader_lock(rname or f"idx:{idx}")
    # asyncio.Lock has no blocking=False; try a short acquire, fail-open if busy.
    try:
        await asyncio.wait_for(lock.acquire(), timeout=0.05)
    except asyncio.TimeoutError:
        return {"ok": True, "need_pin": bool(inst.get("pin"))}
    try:
        return await asyncio.to_thread(_preflight_pin_locked, inst, idx)
    finally:
        lock.release()


def _pin_preflight_detail(result: dict) -> dict:
    code = str(result.get("code") or "pin_error")
    tries = result.get("tries")
    messages = {
        "no_card": "The SIM card is not available.",
        "pin_required": "Enter the SIM PIN before enabling VoWiFi.",
        "pin_invalid": "The saved SIM PIN is invalid; enter the correct PIN again.",
    }
    message = messages.get(code, "SIM PIN verification failed.")
    if tries is not None:
        message += f" {tries} attempts remain."
    return {"code": code, "tries": tries, "message": message}


@app.post("/api/provision")
async def api_provision(body: dict):
    requested_iid = str(body.get("id") or (len(cfg.list_instances()) + 1))
    async with hub.recovery_lock(requested_iid):
        # Stage 1 is identity-only: global quarantine presence means zero APDU. No config,
        # port allocation, current publication or Engine create occurs in this stage.
        with _card_probe_permit_or_http() as probe_permit:
            idx = await asyncio.to_thread(_resolve_reader_index, body)
            pin = body.get("pin", "")
            rlist = await asyncio.to_thread(sim.list_readers)
            rname = (rlist[idx] if 0 <= idx < len(rlist)
                     else body.get("reader") or f"idx:{idx}")
            async with hub.reader_lock(rname):
                c = await asyncio.to_thread(sim.read_card, idx, pin or None)
            if c.error and "PIN" in (c.error or "").upper():
                raise HTTPException(
                    400, f"PIN error: {c.error} ({c.pin_tries} tries left)")
            if not c.imsi:
                raise HTTPException(400, "could not read IMSI (is the PIN correct?)")
            matches = cfg.instances_by_iccid(str(c.iccid or ""))
            matched_iids = sorted({str(item["id"]) for item in matches})
            if len(matched_iids) > 1 or (matched_iids and matched_iids != [requested_iid]):
                raise HTTPException(409, {
                    "code": "sim_identity_conflict",
                    "message": "This SIM identity already belongs to another line.",
                    "existing_instances": matched_iids,
                    "requested_instance": requested_iid,
                })
            probe_permit.bind_actual([requested_iid])

        # Stage 2 may lose to Host acquire in the deliberate gap above. In that case this
        # exact permit fails before every config/Hub/create side effect.
        with _normal_start_permit_or_http(requested_iid) as permit:
            matched_iids = sorted({
                str(item["id"]) for item in cfg.instances_by_iccid(str(c.iccid or ""))})
            if len(matched_iids) > 1 or (matched_iids and matched_iids != [requested_iid]):
                raise HTTPException(409, {
                    "code": "sim_identity_conflict",
                    "message": "This SIM identity changed ownership during provisioning.",
                    "existing_instances": matched_iids,
                    "requested_instance": requested_iid,
                })
            return await _api_provision_with_permit(
                body, requested_iid=requested_iid, permit=permit,
                c=c, idx=idx, pin=pin)


async def _api_provision_with_permit(body: dict, *, requested_iid: str, permit,
                                     c, idx: int, pin: str):
    """Provision a detected card: verify PIN, read identity, create the line and start it.
    PIN is required only when CHV1 is enabled. IMEI is auto-read from bridge metadata when
    available, otherwise the caller must supply it. Optional: imeisv (auto-derived from imei if blank), name, smsc,
    reader_index, reader (name), sip, webrtc, id, port_mode ('auto'|'manual'), sip_port
    (int, when manual), apn (default 'ims'), idr_mode ('apn'|'fqdn', default 'apn')."""
    sip = cfg.merge_carrier_sip_defaults(
        c.mcc, c.mnc, c.iccid or c.imsi,
        body.get("sip") or {"listen_addr": "0.0.0.0", "transport": "udp",
                            "external": []})
    sip.setdefault("webrtc", {"enable": bool(body.get("webrtc", True))})
    # SMSC: manual override wins; otherwise read from the SIM (EF_SMSP, authoritative).
    # If the SIM can't provide it we ask the user to type it (no carrier presets).
    smsc = (body.get("smsc") or "").strip() or c.smsc
    if not smsc:
        raise HTTPException(422, "smsc_unreadable: could not read the SMS centre from the SIM — "
                                 "please provide it manually.")
    live_cards = hub.cards_list()
    live_card = next((item for item in live_cards
                      if (item.get("name") == c.reader or item.get("index") == idx
                          or (c.iccid and item.get("iccid") == c.iccid))), {})
    imei, _hardware_id, _hardware_type = _hardware_imei_for_card(live_card, live_cards)
    if len(imei) != 15:
        raise HTTPException(422, "imei_unavailable: configure a 15-digit IMEI in "
                                 "Device > Hardware before provisioning this SIM.")
    inst = {
        "id": requested_iid,
        "name": body.get("name") or f"{c.mcc}-{c.mnc}",
        "provisioning_state": "ready",
        "imsi": c.imsi, "mcc": c.mcc, "mnc": c.mnc, "iccid": c.iccid,
        **_carrier_identity_update(c),
        # Blank means automatic MCC->ISO country mapping; a two-letter value is a per-line
        # override for MVNO/roaming/operator edge cases.
        "proxy_country": egress.normalize_country(body.get("proxy_country")),
        "imei": imei,
        "imei_source_device_id": _hardware_id,
        # IMEISV for DEVICE_IDENTITY: user value if provided, else auto-derive (14-digit IMEI
        # base + random 2-digit SVN) so each line looks like a distinct handset build.
        "imeisv": (body.get("imeisv") or "").strip()
                  or cfg.imeisv_from_imei(imei, svn=_random_svn()),
        "pin": pin,
        "reader": f"imsi:{c.imsi}",
        "reader_index": idx,  # store the physical reader index for USB device passthrough
        # Stable USB port path of the reader this SIM was provisioned in. This is the primary
        # binding used at start time (resolved back to a live index), so the line sticks to its
        # physical reader socket even if pcscd re-enumerates two identical readers in a different
        # order. Falls back to reader_index/ICCID when absent.
        "reader_port": c.reader_port or usbreader.port_for_index(idx) or "",
        "smsc": smsc,
        "msisdn": body.get("msisdn", ""),
        "msisdn_source": "manual" if str(body.get("msisdn") or "").strip() else "",
        "enabled": True, "sip": sip,
        # APN + ePDG identity (IDr) encoding for the SWu tunnel. apn defaults to 'ims'; idr_mode
        # defaults to 'fqdn' (real-UE APN-FQDN form). Normalised in config.render_instance_json.
        "apn": cfg.normalize_apn(body.get("apn", "")),
        "idr_mode": cfg.normalize_idr_mode(body.get("idr_mode", "")),
        # CFG request address family. Defaults to 'auto' (discovery ladder + carrier DB, seamless);
        # 'v6' Telus/EE, 'v4' Vodafone UK, 'dual'. Normalised in config.render_instance_json.
        "cp_mode": cfg.normalize_cp_mode(body.get("cp_mode", "")),
        # Full Asterisk debug contains SIP identities and is never enabled by background status
        # or metadata collection. Explicit diagnostics must remain bounded and sanitized.
        "debug": {**(body.get("debug") or {}), "asterisk": False},
    }
    # A modem is represented as one UI device but has three internal logical channels so PIN
    # keeping, SWu authentication and Asterisk/SMS can operate independently. Native readers
    # omit virtual_slots and keep the legacy single-reader behaviour.
    virtual = body.get("virtual_slots") or []
    if virtual:
        def slot(pos):
            return virtual[min(pos, len(virtual) - 1)]
        inst["pin_reader"] = slot(0).get("name") or str(slot(0).get("index", 0))
        inst["swu_reader"] = slot(1).get("name") or str(slot(1).get("index", idx))
        inst["ami_reader"] = slot(2).get("name") or str(slot(2).get("index", idx))
        inst["reader_index"] = int(slot(1).get("index", idx))
    # Port mapping: 'manual' pins the SIP UDP port the user chose (the rest of the block
    # derives from it, validated for range + host/instance conflicts). 'auto' (default)
    # allocates a conflict-free block now — and when re-provisioning an existing line it
    # RE-allocates (so switching an already-provisioned line back to Auto actually moves it
    # off a manual port), stepping past anything in use.
    iid = str(inst["id"])
    if body.get("port_mode") == "manual":
        try:
            inst["ports"] = cfg.ports_from_sip_base(cfg.load(), int(body.get("sip_port", 0)),
                                                    exclude_iid=iid)
        except (ValueError, TypeError) as e:
            raise HTTPException(422, f"port_error: {e}")
        inst["port_mode"] = "manual"
    else:
        try:
            inst["ports"] = cfg.alloc_ports_auto(cfg.load(), exclude_iid=iid)
        except ValueError as e:
            raise HTTPException(422, f"port_error: {e}")
        inst["port_mode"] = "auto"
    try:
        inst = cfg.upsert_instance_unique_iccid(inst)
    except cfg.InstanceIdentityConflict as exc:
        raise HTTPException(409, {
            "code": "sim_identity_conflict",
            "message": "This SIM identity already belongs to another line.",
            "existing_instances": list(exc.iids),
            "requested_instance": requested_iid,
        }) from exc
    hub.reset_health(inst["id"])
    # engine.start force-removes any existing container; retire AMI first so a cached
    # client can't keep Login'ing the old (or IP-reused) engine with a stale secret.
    await hub.drop_ami(str(inst["id"]))
    await asyncio.to_thread(
        _start_engine_checked, inst, cfg.get_settings(),
        dev_mounts=os.environ.get("MDD_DEV_MOUNTS", "") == "1", permit=permit)
    _refresh_card_matches()
    await hub.broadcast({"type": "cards", "cards": _client_cards()})
    safe = {k: v for k, v in inst.items() if k not in ("pin", "carrier_identity")}
    return {"ok": True, "instance": safe}


# ----------------------------- unified physical devices -----------------------------
def _read_json_file(path: str) -> dict:
    try:
        with open(path, encoding="utf-8") as handle:
            value = json.load(handle)
        return value if isinstance(value, dict) else {}
    except (OSError, ValueError, TypeError):
        return {}


def _device_sources() -> tuple[dict, dict, dict]:
    """Return desired, observed and hardware assignment state from the host orchestrator."""
    desired = device_state.desired()
    observed = device_state.status()
    hardware = _read_json_file(os.path.join(cfg.DATA_DIR, "orchestrator", "hardware-state.json"))
    return desired, observed, hardware.get("assignments") or {}


def _device_identities() -> dict[str, dict]:
    identities = {}
    for path in glob.glob(os.path.join(cfg.DATA_DIR, "modems", "*.json")):
        value = _read_json_file(path)
        device_id = str(value.get("hardware_id") or "")
        if device_id:
            identities[device_id] = value
    return identities


def _remote_modem_device_id(iccid: str) -> str:
    """Stable UI/control identity follows the SIM, never its current Agent or VPCD slot."""
    return "remote-modem-" + hashlib.sha256(str(iccid).encode("ascii")).hexdigest()[:16]


def _remote_modem_for_device(device_id: str) -> dict | None:
    return next((item for item in modem_registry.list()
                 if _remote_modem_device_id(str(item.get("iccid") or "")) == device_id), None)


def _merge_remote_modem_devices(devices: list[dict],
                                available_countries: list[str] | None = None,
                                egress_state: dict | None = None) -> list[dict]:
    """Replace a modem's transport-reader row with one ICCID-scoped modem row."""
    desired_doc = device_state.desired()
    for remote in modem_registry.list():
        iccid = str(remote.get("iccid") or "")
        capabilities = remote.get("capabilities") or {}
        if not iccid or not capabilities.get("cellular_data"):
            continue
        remote_id = _remote_modem_device_id(iccid)
        base_index = next((index for index, item in enumerate(devices)
                           if str((item.get("sim") or {}).get("iccid") or "") == iccid), None)
        base = dict(devices[base_index]) if base_index is not None else {}
        saved = (desired_doc.get("devices") or {}).get(remote_id)
        previous_caps = base.get("capabilities") or {}
        wanted = saved or {
            **desired_doc.get("defaults", {}),
            "vowifi_enabled": bool((previous_caps.get("vowifi") or {}).get("desired", True)),
        }
        online = bool(remote.get("online"))
        status = remote.get("status") or {}
        uicc_health = dict(status.get("uicc_health") or {})
        sms_runtime_ready = bool(capabilities.get("sms") and status.get("sms_ready", True))
        sms_runtime_reason = ("" if sms_runtime_ready else
                              "Cellular SMS is not ready" +
                              (f" ({status.get('sms_error')})" if status.get("sms_error") else ""))
        firmware = str(remote.get("firmware") or base.get("firmware") or "")
        firmware_advice = firmware_matrix.advise(firmware, model=str(remote.get("model") or ""))
        sms_service_center = str(status.get("sms_service_center") or "")
        # A rejected SMS submit is reported by the network as an unspecified error, so the
        # page must name the preconditions it cannot infer.  An advisory has to stay visible
        # even when the capability reports ready, because a driver can report `sms_ready`
        # while every submit is still rejected.
        #
        # An absent service centre is deliberately *not* an advisory: on 2026-08-19 real
        # hardware, Windows MBN reported an empty centre while SMS submission succeeded,
        # because the modem holds the address below the MBN interface.  Warning on emptiness
        # would therefore mark a working device as suspect.  The value stays published as a
        # fact, and it is recorded in the failure detail if a submit actually fails.
        sms_advisory = []
        if "sms" in (firmware_advice.get("impact") or []):
            sms_advisory.append(str(firmware_advice.get("reason") or ""))
        if status.get("sms_service_center_changed"):
            sms_advisory.append(str(status.get("sms_service_center_advisory") or
                                    "The SMS centre differs from the last successful send; "
                                    "check with your carrier if sends fail."))
        call_contract_error = call_contract_reason(capabilities)
        call_runtime_ready = bool(
            capabilities.get("call_signalling") and capabilities.get("call_audio") and
            not call_contract_error and status.get("call_ready", False) and
            status.get("call_audio_ready", False))
        call_runtime_reason = ("" if call_runtime_ready else
                               str((uicc_health.get("reason")
                                    if uicc_health.get("ready") is False else "") or
                                   call_contract_error or
                                   capabilities.get("call_contract_error") or
                                   status.get("call_audio_error") or status.get("call_error") or
                                   "Cellular call signalling is not ready"))
        last_cellular = status.get("cellular") or {}
        data_active = bool(status.get("data_active") or status.get("data") == "connected")
        proxy_ready = bool((status.get("proxy") or {}).get("ready"))
        registration = str(status.get("registration") or
                           last_cellular.get("registration") or "unknown")
        desired_cellular = bool(wanted.get("cellular_enabled"))
        flight_desired = bool(wanted.get("flight_mode"))
        roaming_desired = bool(wanted.get("roaming_enabled"))
        error = str(last_cellular.get("error") or "")
        if not online:
            cell_actual, cell_reason = "off", "Device not connected"
        elif not desired_cellular:
            cell_actual, cell_reason = "off", ""
        elif flight_desired:
            cell_actual, cell_reason = "off", "Flight mode is enabled"
        elif registration == "roaming" and not roaming_desired:
            cell_actual, cell_reason = "error", "Data roaming is disabled for this SIM."
        elif data_active and proxy_ready:
            cell_actual, cell_reason = "on", ""
        elif error:
            cell_actual, cell_reason = "error", error
        else:
            cell_actual, cell_reason = "starting", ""
        radio_enabled = status.get("radio_enabled")
        flight_actual = ("off" if not online else
                         "on" if radio_enabled is False else
                         "off" if radio_enabled is True else "starting")
        roaming_actual = ("off" if not online else
                          "on" if bool(status.get("roaming_allowed", roaming_desired)) else "off")
        inst = _match_instance_by_iccid(iccid)
        line_status = base.get("status") or {}
        vowifi_view = device_state.native_vowifi_capability(
            bool(wanted.get("vowifi_enabled", True)),
            bool(inst) and str(line_status.get("state") or "").upper() != "STOPPED",
            line_status)
        sim_apdu = bool(capabilities.get("sim_apdu", True) and
                        status.get("sim_apdu_ready", True))
        sim_apdu_reason = str(status.get("sim_apdu_error") or
                              "SIM APDU access is currently unavailable")
        vowifi_view["available"] = bool(online and inst and sim_apdu)
        if not online:
            vowifi_view.update(actual="off", reason="Device not connected")
        elif not inst:
            vowifi_view.update(actual="off", reason="Configure the SIM before enabling VoWiFi")
        elif not sim_apdu:
            vowifi_view.update(
                actual="unsupported",
                reason=sim_apdu_reason)
        sim = dict(base.get("sim") or {})
        remote_imsi = "".join(ch for ch in str(remote.get("imsi") or "") if ch.isdigit())
        inferred_mnc, inferred_mnc_source = carrier_id.infer_mnc_from_imsi(remote_imsi)
        exact_mnc = str(sim.get("mnc") or "")
        sim.update({"iccid": iccid, "present": online,
                    "imsi": remote_imsi or sim.get("imsi") or "",
                    "mcc": (remote_imsi[:3] if len(remote_imsi) >= 3 else
                            sim.get("mcc") or ""),
                    # EF_AD/APDU remains authoritative.  Without it, split the IMSI only
                    # when every published PLMN under this MCC has the same MNC length.
                    "mnc": exact_mnc or inferred_mnc,
                    "mnc_source": ("sim" if exact_mnc else inferred_mnc_source),
                    "number": remote.get("phone") or sim.get("number") or "",
                    "name": sim.get("name") or (inst or {}).get("name") or "SIM",
                    "identity_source": "modem-provider",
                    "apdu_available": sim_apdu})
        cellular = {
            "registration": registration, "operator": status.get("operator") or "",
            "operator_id": status.get("operator_id") or "",
            "signal": status.get("signal"), "apn": status.get("apn") or "",
            "ip": status.get("ip") or "", "data_active": data_active,
            "roaming": registration == "roaming", "roaming_allowed": roaming_desired,
            "rx_bytes": int(status.get("rx_bytes") or 0),
            "tx_bytes": int(status.get("tx_bytes") or 0),
            "profile": status.get("profile") or "",
            "interface": status.get("interface") or "",
        }
        # Reconstruct display metadata from the ICCID-matched saved line every time.  A stale
        # VPCD row may enrich the live facts, but its presence must not decide whether country,
        # exit selection or carrier information exists on a remote modem.
        sim["carrier"] = _carrier_description(inst, sim, cellular)
        base.update({
            "id": remote_id, "device_type": "modem", "present": online,
            "agent_id": str(remote.get("agent_id") or ""),
            "agent_health_ref": str(remote.get("agent_id") or ""),
            "name": remote.get("model") or base.get("name") or "Remote cellular modem",
            "model": remote.get("model") or base.get("model") or "",
            "firmware": firmware,
            "firmware_advice": firmware_advice,
            "sms_diagnostics": {"service_center": sms_service_center,
                                "advisory": [item for item in sms_advisory if item],
                                "recovery": dict(status.get("recovery") or {}),
                                "uicc_health": uicc_health},
            "imei": remote.get("imei") or base.get("imei") or "",
            "imei_masked": _masked_identifier(remote.get("imei") or base.get("imei") or ""),
            "stable_path": "", "instance_id": str(inst["id"]) if inst else None,
            # ICCID is the stable identity of a remote cellular attachment.  Keep it at the
            # same top-level location used by /api/cellular-sims as well as in ``sim`` so
            # consumers never have to fall back to a transient Agent, slot, or modem id.
            "iccid": iccid,
            "sim": sim, "cellular": cellular,
            "egress": _device_egress_view(
                inst, sim, available_countries=available_countries,
                egress_state=egress_state),
            # A Windows MBN attachment can provide SMS while its VoWiFi engine is deliberately
            # stopped (the OS owns the SIM).  Do not derive these badges from the VoWiFi line
            # state or the UI will claim that every communication path is stopped.
            "ims_capabilities": {
                "voice": {
                    "actual": "on" if online and call_runtime_ready else "off",
                    "available": bool(online and call_runtime_ready),
                    "reason": "" if online and call_runtime_ready else call_runtime_reason,
                },
                "sms": {
                    "actual": "on" if online and sms_runtime_ready else "off",
                    "available": bool(online and sms_runtime_ready),
                    "reason": "" if online and sms_runtime_ready else
                              sms_runtime_reason or "Cellular SMS is unavailable",
                },
                "rcs": {"actual": "unsupported",
                        "reason": "RCS is not implemented by this gateway"},
            },
            "capabilities": {
                "cellular": {"desired": desired_cellular, "actual": cell_actual,
                             "available": online, "reason": cell_reason},
                "flight": {"desired": flight_desired, "actual": flight_actual,
                           "available": online, "reason": "" if online else "Device not connected"},
                "roaming": {"desired": roaming_desired, "actual": roaming_actual,
                            "available": online, "reason": "" if online else "Device not connected"},
                "vowifi": vowifi_view,
                "sms": {"desired": bool(capabilities.get("sms")),
                        "actual": "on" if online and sms_runtime_ready else "off",
                        "available": bool(online and sms_runtime_ready),
                        "reason": "" if online and sms_runtime_ready else
                                  sms_runtime_reason or "Cellular SMS is unavailable"},
                "call": {"desired": bool(capabilities.get("call_signalling") and
                                           capabilities.get("call_audio")),
                         "actual": "on" if online and call_runtime_ready else "off",
                         "available": bool(online and call_runtime_ready),
                         "reason": "" if online and call_runtime_ready else call_runtime_reason},
            },
            "remote_modem": True,
        })
        if base_index is None:
            devices.append(base)
        else:
            devices[base_index] = base
    # A historical hardware record can already use the ICCID-derived remote id while lacking
    # a live SIM.  Never return both it and the merged attachment: React selection and every
    # device API use this id as a unique key. Prefer the live/ICCID-bearing remote view.
    unique: list[dict] = []
    positions: dict[str, int] = {}
    for device in devices:
        device_id = str(device.get("id") or "")
        if device_id not in positions:
            positions[device_id] = len(unique)
            unique.append(device)
            continue
        current = unique[positions[device_id]]
        current_score = (bool(current.get("remote_modem")), bool(current.get("present")),
                         bool((current.get("sim") or {}).get("iccid")))
        candidate_score = (bool(device.get("remote_modem")), bool(device.get("present")),
                           bool((device.get("sim") or {}).get("iccid")))
        if candidate_score > current_score:
            unique[positions[device_id]] = device
    return unique


def _instance_for_device(device_id: str, identity: dict, cards: list[dict],
                         observed: dict | None = None) -> dict | None:
    """Match a line only from the SIM that is live in this device right now.

    Bridge identity files survive unplugging and intentionally retain hardware facts.
    Their last ICCID must never make an offline modem appear permanently attached to
    the last SIM it happened to contain.
    """
    # ModemManager owns the physical modem while 4G is active and remains the
    # authoritative source for the inserted SIM.  In that state the optional
    # PC/SC VoWiFi bridge can legitimately expose no card at all.
    live_iccid = str((((observed or {}).get("cellular") or {}).get("sim_iccid")) or "")
    if live_iccid:
        return _match_instance_by_iccid(live_iccid)
    card_info = next((item for item in cards
                      if item.get("hardware_id") == device_id and item.get("present")), None)
    if card_info and card_info.get("iccid"):
        return _match_instance_by_iccid(card_info["iccid"])
    return None


def _device_for_card(card_info: dict, cards: list[dict] | None = None) -> tuple[str, str]:
    """Return (device_id, device_type) for a live card-monitor entry."""
    cards = cards or hub.cards_list()
    hardware_id = str(card_info.get("hardware_id") or "")
    if card_info.get("hardware_kind") == "modem" and hardware_id:
        return hardware_id, "modem"
    name = str(card_info.get("name") or "")
    port = str(card_info.get("reader_port") or "")
    for device_id, candidate in device_state.native_reader_devices(cards).items():
        if ((name and candidate.get("name") == name)
                or (port and candidate.get("reader_port") == port)):
            return device_id, "reader"
    return "", ""


def _hardware_imei_for_card(card_info: dict, cards: list[dict] | None = None,
                            *, migrate_legacy: bool = True) -> tuple[str, str, str]:
    """Resolve device IMEI for the SIM / hardware currently holding a SIM."""
    cards = cards or hub.cards_list()
    device_id, device_type = _device_for_card(card_info, cards)

    # Priority 1: Persistent ICCID <-> IMEI binding
    iccid = str(card_info.get("iccid") or "").strip()
    if iccid:
        binding = cfg.get_iccid_imei_binding(iccid)
        if binding and len(binding.get("imei", "")) == 15:
            return binding["imei"], device_id, device_type

    # A remote AT+CSIM agent reports the modem's immutable hardware IMEI as authenticated
    # allocation metadata. It offers VoWiFi card access only, so it remains a remote reader
    # in the capability model even though its identity comes from the cellular module.
    reported_imei = cfg.normalize_imei(card_info.get("imei", ""))
    if card_info.get("remote") and len(reported_imei) == 15:
        return reported_imei, device_id, device_type

    # Priority 2: Physical modem identity (built-in IMEI from modem AT/QMI)
    if device_type == "modem":
        identity = _device_identities().get(device_id) or {}
        imei = cfg.normalize_imei(identity.get("imei", ""))
        return imei if len(imei) == 15 else "", device_id, device_type

    # Priority 3: Explicit hardware record (e.g. from Devices > Hardware tab)
    if device_type == "reader":
        record = device_state.hardware().get(device_id) or {}
        imei = cfg.normalize_imei(record.get("imei", ""))
        if len(imei) == 15:
            return imei, device_id, device_type

    return "", device_id, device_type


def _apply_current_hardware_imei(inst: dict) -> dict:
    """Snapshot the current physical device IMEI into the engine line before start."""
    iccid = str(inst.get("iccid") or "")
    cards = hub.cards_list()
    card_info = next((item for item in cards
                      if item.get("present") and str(item.get("iccid") or "") == iccid), None)
    if not card_info:
        card_info = {"iccid": iccid}
    imei, _device_id, _device_type = _hardware_imei_for_card(card_info, cards)
    if len(imei) != 15:
        # Check if inst has an IMEI
        inst_imei = cfg.normalize_imei(inst.get("imei", ""))
        if len(inst_imei) == 15:
            imei = inst_imei
            if iccid:
                cfg.set_iccid_imei_binding(iccid, imei)
    if len(imei) != 15:
        raise HTTPException(409, {
            "code": "imei_binding_required",
            "message": "This SIM profile has no bound IMEI. Please select or add an IMEI from the pool.",
            "iccid": iccid,
            "pool": cfg.list_imei_pool(),
        })
    if imei == cfg.normalize_imei(inst.get("imei", "")):
        return inst
    previous_imeisv = str(inst.get("imeisv") or "")
    svn = previous_imeisv[-2:] if len(previous_imeisv) == 16 and previous_imeisv[-2:].isdigit() else _random_svn()
    return cfg.upsert_instance({"id": str(inst["id"]), "imei": imei,
                                "imei_source_device_id": _device_id,
                                "imeisv": cfg.imeisv_from_imei(imei, svn=svn)})



def _masked_identifier(value) -> str:
    text = str(value or "")
    return ("*" * max(0, len(text) - 4) + text[-4:]) if text else ""


def _vowifi_capability(desired: bool, observed: dict, running: bool,
                       line_status: dict | None) -> dict:
    transitioning = bool(observed.get("transitioning"))
    bridge = bool((observed.get("actual") or {}).get("vowifi_bridge_active"))
    error = str(observed.get("error") or "")
    if error:
        actual = "error"
    elif transitioning:
        actual = "starting" if desired else "stopping"
    elif not desired:
        # The bridge stays up for every present modem so the card remains readable, so it
        # says nothing about VoWiFi here — only a still-running line means "stopping".
        actual = "stopping" if running else "off"
    elif not bridge:
        actual = "starting"
    elif not running:
        actual, error = "degraded", "VoWiFi is enabled but no configured line is running"
    else:
        raw = str((line_status or {}).get("state") or (line_status or {}).get("label") or "").lower()
        if raw in {"ok", "working", "registered"}:
            actual = "on"
        elif raw in {"error", "failed", "stopped"}:
            actual = "error"
            error = str((line_status or {}).get("reason") or (line_status or {}).get("detail") or "")
        else:
            actual = "starting"
    return {"desired": desired, "actual": actual, "reason": error}


def _ims_capabilities(inst: dict | None, line_status: dict | None,
                      device_present: bool) -> dict:
    """Actual gateway IMS service paths, derived from the sampled registration state.

    These are observations, not claims about a subscription: Voice and SMS become available
    only after this line is registered.  RCS is explicit ``unsupported`` because this gateway
    does not implement an RCS client; presenting it as merely offline would be misleading.
    """
    inst, line_status = inst or {}, line_status or {}
    state = str(line_status.get("state") or "STOPPED").upper()
    reason = str(line_status.get("reason") or "")
    if not device_present or not inst or state in {"STOPPED", "NO_CARD"}:
        actual = "off"
    elif state == "OK":
        actual = "on"
    elif state in {"REGISTERING", "TUNNEL_DOWN", "EPDG_UNRESOLVED"}:
        actual = "starting"
    else:
        actual = "error"
    sms_actual = actual
    if actual == "on" and not str(inst.get("smsc") or "").strip():
        sms_actual, reason = "degraded", "No SMSC is configured for this SIM"
    return {
        "voice": {"actual": actual, "reason": reason},
        "sms": {"actual": sms_actual, "reason": reason},
        "rcs": {"actual": "unsupported", "reason": "RCS is not implemented by this gateway"},
    }


def _follow_imei_source(old_id: str, new_id: str) -> list[str]:
    """Repoint lines that name a device id the reader migration just retired.

    The field records which physical device supplied a line's IMEI, and doubles as the marker
    that the one-time legacy migration already ran for that line. It is not load-bearing — the
    IMEI is resolved from whichever device currently holds the card — so a stale value is
    harmless to the engine but reads as a device that no longer exists, and clearing it would
    be worse than leaving it: an empty marker lets the legacy migration run a second time.
    """
    followed = []
    for inst in cfg.list_instances():
        if str(inst.get("imei_source_device_id") or "") != old_id:
            continue
        cfg.upsert_instance({"id": str(inst["id"]), "imei_source_device_id": new_id})
        followed.append(str(inst["id"]))
    return followed


async def _unified_devices() -> list[dict]:
    desired_doc, observed_doc, assignments = _device_sources()
    desired_devices = desired_doc.get("devices") or {}
    observed_devices = observed_doc.get("devices") or {}
    identities = _device_identities()
    cards = hub.cards_list()
    native_readers = device_state.native_reader_devices(cards)
    # A reader that moved to a different USB port derives a new id and strands its saved
    # record, which this list would then render as a second, permanently offline copy of a
    # connected reader. Move the record first: it holds the IMEI the line presents.
    if cards:
        try:
            moved = await asyncio.to_thread(device_state.migrate_reader_records, native_readers)
            for old_id, new_id in moved:
                log.info("reader record migrated: %s -> %s (same reader on a new USB port)",
                         old_id, new_id)
                followed = await asyncio.to_thread(_follow_imei_source, old_id, new_id)
                if followed:
                    log.info("line(s) %s now name the migrated reader as their IMEI source",
                             ", ".join(followed))
        except Exception as exc:  # noqa
            log.debug("reader record migration failed: %r", exc)
    hardware_records = device_state.hardware()
    saved_reader_ids = {device_id for device_id, record in hardware_records.items()
                        if record.get("device_type") == "reader"}
    saved_modem_ids = set(hardware_records) - saved_reader_ids
    modem_ids = (set(assignments) | set(desired_devices) | set(observed_devices)
                 | set(identities) | saved_modem_ids)
    device_ids = sorted(modem_ids | set(native_readers) | saved_reader_ids)
    # A saved assignment says where a modem belongs, not that it is physically connected.
    # Presence must come from the live orchestrator observation; otherwise an unplugged modem
    # retains its desired toggles and is misleadingly rendered as "starting/stopping" forever.
    present_ids = sorted(device_id for device_id in modem_ids
                         if bool((observed_devices.get(device_id) or {}).get("present", False)))
    shared = observed_doc.get("shared") or {}

    settings = cfg.get_settings()
    configured_exits = settings.get("proxy", {}).get("exits", {}) or {}
    available_countries = sorted(country for country, value in configured_exits.items()
                                 if isinstance(value, dict) and value.get("enabled", False))
    egress_state = egress.status()
    result = []
    for device_id in device_ids:
        native_card = native_readers.get(device_id)
        hardware_record = hardware_records.get(device_id) or {}
        is_native_reader = native_card is not None or hardware_record.get("device_type") == "reader"
        assignment = assignments.get(device_id) or {}
        observed = observed_devices.get(device_id) or {}
        identity = identities.get(device_id) or {}
        # Device presence is physical endpoint presence, not SIM insertion. A remote VPCD
        # heartbeat therefore brings its reader online even when no card is inserted; native
        # PC/SC readers are present while they remain enumerated. SIM presence stays separate
        # in `sim.present` below.
        device_present = ((bool((native_card or {}).get("connection_online"))
                           if (native_card or {}).get("remote") else bool(native_card))
                          if is_native_reader else bool(observed.get("present", False)))
        host_cell = observed.get("cellular") or {}
        inst = (_match_instance_by_iccid(native_card.get("iccid"))
                if native_card and native_card.get("iccid")
                else _instance_for_device(device_id, identity, cards, observed)
                if device_present else None)
        wanted = ({"cellular_enabled": False,
                   "vowifi_enabled": bool((inst or {}).get("enabled", bool(inst))),
                   "flight_mode": False}
                  if is_native_reader else desired_devices.get(device_id)
                  or desired_doc.get("defaults") or {
                      "cellular_enabled": False, "vowifi_enabled": True,
                      "flight_mode": False})
        cell_desired = bool(wanted.get("cellular_enabled"))
        vowifi_desired = bool(wanted.get("vowifi_enabled"))
        flight_desired = bool(wanted.get("flight_mode"))
        line_status = _cached_line_status(inst) if inst else None
        running = bool(inst) and (line_status or {}).get("state") != "STOPPED"
        vowifi = (device_state.native_vowifi_capability(vowifi_desired, running, line_status)
                  if is_native_reader else
                  _vowifi_capability(vowifi_desired, observed, running, line_status))
        is_draft = bool(inst) and inst.get("provisioning_state") == "draft"
        if is_draft and inst:
            promoted = _auto_promote_card_draft(inst, native_card or card_info, cards)
            if promoted.get("provisioning_state") != "draft":
                inst = promoted
                is_draft = False
        if not device_present:
            vowifi.update(actual="off", available=False, reason="Device not connected")
        elif not inst:
            vowifi.update(available=False, reason="Insert a readable SIM before enabling VoWiFi")
        elif is_draft:
            vowifi.update(available=False,
                          reason="Automatic setup is waiting for SIM or hardware information")
        else:
            vowifi.update(available=True)


        actual_state = observed.get("actual") or {}
        # Published by the orchestrator when this gateway is configured VoWiFi-only
        # (hardware.modem_backend = serial): ModemManager never runs, so cellular is a
        # capability this host does not have — not a fault to keep retrying.
        serial_only = actual_state.get("cellular_supported") is False
        is_cellular_target = (not is_native_reader and not serial_only
                              and device_id in present_ids)
        cell_reason = ""
        cell_actual = "unsupported" if (is_native_reader or serial_only) else "off"
        radio_on = bool(actual_state.get("cellular_radio_enabled"))
        if is_native_reader:
            cell_reason = "A smart-card reader supports VoWiFi only"
        elif serial_only:
            cell_reason = "This gateway is configured for VoWiFi only (ModemManager disabled)"
        cellular_view = None
        if is_cellular_target:
            if host_cell.get("available"):
                registration = str(host_cell.get("registration") or "unknown").lower()
                radio_on = bool(actual_state.get("cellular_radio_enabled",
                                                 host_cell.get("radio_enabled",
                                                               host_cell.get("powered"))))
                registered = registration in {"home", "roaming", "registered"}
                if not cell_desired and host_cell.get("data_active"):
                    cell_actual = "stopping"
                elif not cell_desired:
                    cell_actual = "off"
                elif flight_desired:
                    cell_actual, cell_reason = "off", "Flight mode is enabled"
                elif radio_on and registered and host_cell.get("data_active"):
                    cell_actual = "on"
                elif radio_on:
                    cell_actual = "starting"
                else:
                    cell_actual, cell_reason = "error", "Cellular radio is not enabled"
                cellular_view = {
                    "registration": registration, "operator": host_cell.get("operator") or "",
                    "signal": host_cell.get("signal"), "apn": host_cell.get("apn") or "",
                    "ip": host_cell.get("ip") or "",
                    "data_active": bool(host_cell.get("data_active")),
                    "roaming": registration == "roaming",
                    "rx_bytes": int(host_cell.get("rx_bytes") or 0),
                    "tx_bytes": int(host_cell.get("tx_bytes") or 0),
                    "profile": host_cell.get("profile") or "",
                    "interface": host_cell.get("network_interface") or "",
                }
            elif cell_desired:
                if shared.get("error"):
                    cell_actual = "error"
                    cell_reason = str(shared.get("error"))
                else:
                    cell_actual = "starting"
        card_info = native_card or next((item for item in cards
                                         if item.get("hardware_id") == device_id
                                         and item.get("present")), {})
        # Keep physical SIM state independent from the optional VoWiFi PC/SC
        # bridge.  A connected cellular modem can have a readable SIM even when
        # every virtual reader slot is empty or VoWiFi is disabled.
        live_modem_iccid = (str(host_cell.get("sim_iccid") or "")
                            if device_present and not is_native_reader else "")
        if live_modem_iccid and not card_info:
            card_info = {
                "present": True, "iccid": live_modem_iccid,
                "hardware_id": device_id, "hardware_kind": "modem",
                "mcc": (inst or {}).get("mcc", ""),
                "mnc": (inst or {}).get("mnc", ""),
                "imsi": (inst or {}).get("imsi", ""),
                "smsc": (inst or {}).get("smsc", ""),
                "mnc_len": (inst or {}).get("mnc_len"),
                "carrier_identity": (inst or {}).get("carrier_identity") or {},
            }
        carrier = _carrier_description(inst, card_info, cellular_view)
        card_iccid = str((inst or {}).get("iccid") or (native_card or {}).get("iccid") or (card_info or {}).get("iccid") or "").strip()
        bound_info = cfg.get_iccid_imei_binding(card_iccid) if card_iccid else None
        bound_imei = {
            "is_bound": bool(bound_info),
            "imei": bound_info.get("imei", "") if bound_info else "",
            "imei_masked": bound_info.get("imei_masked", "") if bound_info else "",
            "imei_id": bound_info.get("imei_id", "") if bound_info else "",
            "name": bound_info.get("name", "") if bound_info else "",
            "bound_at": bound_info.get("bound_at") if bound_info else None,
        }
        if native_card:
            hardware_imei, _hardware_id, _hardware_type = _hardware_imei_for_card(
                native_card, cards)
            hardware_record = device_state.hardware().get(device_id) or hardware_record
        else:
            hardware_imei = cfg.normalize_imei(identity.get("imei", ""))
        masked_imei = _masked_identifier(hardware_imei)
        bridge_active = bool(actual_state.get("vowifi_bridge_active"))
        logical_channels = (None if is_native_reader else
                            device_state.logical_channel_view(identity, bridge_active))
        if not device_present:
            cell_actual, cell_reason = "off", "Device not connected"
            flight_actual, flight_available = "off", False
        else:
            flight_actual = ("unsupported" if is_native_reader or serial_only else
                             "on" if flight_desired and not radio_on else
                             "off" if not flight_desired and radio_on else
                             "starting" if flight_desired else "stopping")
            flight_available = not is_native_reader and not serial_only
        result.append({
            "id": device_id, "device_type": "reader" if is_native_reader else "modem",
            "agent_id": str(card_info.get("agent_id") or "") if is_native_reader else "",
            "agent_health_ref": str(card_info.get("agent_id") or "") if is_native_reader else "",
            "name": (card_info.get("display_name") or card_info.get("name")
                     or hardware_record.get("name") or "Smart-card reader"
                     if is_native_reader else
                     assignment.get("name") or observed.get("name")
                     or hardware_record.get("name") or "Cellular modem"),
            "present": device_present,
            "model": identity.get("model") or observed.get("model") or "",
            "firmware": identity.get("firmware") or observed.get("firmware") or "",
            "imei": hardware_imei,
            "imei_masked": masked_imei,
            "bound_imei": bound_imei,
            "stable_path": ((card_info.get("reader_port") or hardware_record.get("stable_path") or "")
                            if is_native_reader else
                            assignment.get("usb_path") or identity.get("usb_path")
                            or hardware_record.get("stable_path") or ""),
            "reader": card_info.get("name") or "", "instance_id": str(inst["id"]) if inst else None,
            "status": line_status,
            "logical_channels": logical_channels,
            "sim": {"name": (((inst or {}).get("name")
                             or (cellular_view or {}).get("operator") or (card_info or {}).get("display_name") or (card_info or {}).get("name") or "SIM") if (inst or card_info.get("present")) else ""),
                    "number": (inst or {}).get("msisdn") or "",
                    "iccid": card_iccid,
                    "present": bool(card_info.get("present")),
                    "presence": str(card_info.get("card_presence") or
                                    ("present" if card_info.get("present") else "absent")),
                    "carrier": carrier},

            "cellular": cellular_view,
            "vowifi": {"epdg": (line_status or {}).get("detail") or "",
                       "ims": (line_status or {}).get("label") or "",
                       "rekey_minutes": (inst or {}).get("rekey_minutes",
                           (cfg.get_settings().get("rekey") or {}).get("minutes", 30))},
            "ims_capabilities": _ims_capabilities(inst, line_status, device_present),
            "egress": _device_egress_view(
                inst, card_info, available_countries=available_countries,
                egress_state=egress_state),
            "provisioning": {"state": "draft" if is_draft else "ready" if inst else "detecting",
                "missing": ([key for key, value in (
                    ("imsi", (inst or card_info).get("imsi")),
                    ("imei", hardware_imei),
                    ("smsc", (inst or card_info).get("smsc"))) if not value])},
            "capabilities": {"cellular": {"desired": cell_desired, "actual": cell_actual,
                                             "reason": cell_reason},
                             "flight": {"desired": flight_desired,
                                        "actual": flight_actual,
                                        "available": flight_available,
                                        "reason": ("Device not connected" if not device_present
                                                   else "This gateway is configured for VoWiFi "
                                                        "only (ModemManager disabled)"
                                                   if serial_only else "")},
                             "vowifi": vowifi},
            "shared": shared,
        })
    devices = _merge_remote_modem_devices(
        result, available_countries=available_countries, egress_state=egress_state)
    hidden_device_ids = device_state.hidden_devices()
    visible = []
    for device in devices:
        device_id = str(device.get("id") or "")
        if device_id not in hidden_device_ids:
            visible.append(device)
            continue
        # A live observation is the automatic restore signal requested by the operator.
        # Drop only the presentation tombstone; all matching state was preserved throughout.
        if device.get("present"):
            await asyncio.to_thread(device_state.unhide_device, device_id)
            hidden_device_ids.discard(device_id)
            visible.append(device)
    return visible


@app.get("/api/devices")
async def api_devices():
    # Sessions are memory-only, so a sign-in usually follows a control-plane restart — right
    # when the card monitor is still completing its first scan and smart-card readers are not
    # in the list yet. `discovering` lets the UI say so instead of reporting a confident zero.
    return {"devices": await _unified_devices(), "discovering": not hub.scanned,
            "shared": device_state.status().get("shared") or {}}


@app.put("/api/devices/{device_id}/hardware")
async def api_device_hardware(device_id: str, body: dict):
    """Save user-managed physical hardware identity (currently native-reader IMEI)."""
    if set(body or {}) - {"imei", "name"}:
        raise HTTPException(400, "only imei and name can be changed")
    device = next((item for item in await _unified_devices() if item["id"] == device_id), None)
    if not device:
        raise HTTPException(404, "no such physical device")
    if device.get("device_type") != "reader":
        raise HTTPException(400, "a modem reports its hardware IMEI automatically")
    raw = str((body or {}).get("imei") or "").strip()
    name = str((body or {}).get("name") or "").strip()
    imei = cfg.normalize_imei(raw)
    if len(imei) != 15:
        raise HTTPException(422, "IMEI must contain exactly 15 digits")
    record = device_state.set_hardware(device_id, {
        "device_type": "reader", "name": device.get("name") or "Smart-card reader",
        "stable_path": device.get("stable_path") or "", "imei": imei})

    # Save to global IMEI pool and bind to current SIM card ICCID if present
    cfg.upsert_imei_pool_entry(name=name or device.get("name") or "Reader Device", imei=imei)
    card_iccid = str(device.get("sim", {}).get("iccid") or "").strip()
    if card_iccid:
        cfg.set_iccid_imei_binding(card_iccid, imei, name=name or device.get("name") or "Reader Device")

    # A running line renders the device identity inside its container. Apply a hardware
    # change immediately to the SIM currently inserted in this reader.
    iid = str(device.get("instance_id") or "")
    applied = False
    if iid and imei:
        inst = cfg.get_instance(iid) or {}
        previous_imeisv = str(inst.get("imeisv") or "")
        svn = (previous_imeisv[-2:] if len(previous_imeisv) == 16
               and previous_imeisv[-2:].isdigit() else _random_svn())
        inst = cfg.upsert_instance({"id": iid, "imei": imei,
                                    "imei_source_device_id": device_id,
                                    "imeisv": cfg.imeisv_from_imei(imei, svn=svn)})
        async with hub.recovery_lock(iid):
            if await asyncio.to_thread(engine.is_running, iid):
                await hub.drop_ami(iid)
                await asyncio.to_thread(
                    _start_engine_checked, inst, cfg.get_settings(),
                    dev_mounts=os.environ.get("MDD_DEV_MOUNTS", "") == "1")
                hub.reset_health(iid)
                applied = True
    await hub.broadcast({"type": "hardware", "device": device_id})
    return {"ok": True, "imei_masked": _masked_identifier(record.get("imei")),
            "applied": applied}


@app.get("/api/imei-pool")
async def api_get_imei_pool():
    return {
        "ok": True,
        "pool": cfg.list_imei_pool(),
        "bindings": cfg.list_iccid_imei_bindings(),
    }


@app.post("/api/imei-pool")
async def api_save_imei_pool_entry(body: dict):
    name = str(body.get("name") or "").strip()
    imei = str(body.get("imei") or "").strip()
    notes = str(body.get("notes") or "").strip()
    entry_id = str(body.get("id") or "").strip() or None
    if not name:
        raise HTTPException(400, "Device name is required")
    norm_imei = cfg.normalize_imei(imei)
    if len(norm_imei) != 15:
        raise HTTPException(400, "IMEI must be exactly 15 digits")
    if entry_id:
        existing = cfg.get_imei_pool_entry(entry_id)
        used_by = [iccid for iccid, binding in cfg.list_iccid_imei_bindings().items()
                   if binding.get("imei_id") == entry_id]
        if existing and existing.get("imei") != norm_imei and used_by:
            raise HTTPException(409, {
                "code": "imei_pool_entry_in_use",
                "message": "Unbind this IMEI from its SIM cards before changing its digits.",
                "iccids": used_by,
            })
    entry = cfg.upsert_imei_pool_entry(name=name, imei=norm_imei, notes=notes, entry_id=entry_id)
    await hub.broadcast({"type": "imei_pool", "pool": cfg.list_imei_pool()})
    return {"ok": True, "entry": entry, "pool": cfg.list_imei_pool()}


@app.delete("/api/imei-pool/{entry_id}")
async def api_delete_imei_pool_entry(entry_id: str):
    used_by = [iccid for iccid, binding in cfg.list_iccid_imei_bindings().items()
               if binding.get("imei_id") == entry_id]
    if used_by:
        raise HTTPException(409, {
            "code": "imei_pool_entry_in_use",
            "message": "Unbind this IMEI from its SIM cards before deleting it.",
            "iccids": used_by,
        })
    ok = cfg.delete_imei_pool_entry(entry_id)
    await hub.broadcast({"type": "imei_pool", "pool": cfg.list_imei_pool()})
    return {"ok": ok, "pool": cfg.list_imei_pool()}


@app.post("/api/imei-pool/bind")
async def api_bind_imei_to_iccid(body: dict):
    iccid = str(body.get("iccid") or "").strip()
    imei = str(body.get("imei") or "").strip()
    name = str(body.get("name") or "").strip()
    imei_id = str(body.get("imei_id") or "").strip()
    if not iccid:
        raise HTTPException(400, "ICCID is required")
    if imei_id and not imei:
        pool_entry = cfg.get_imei_pool_entry(imei_id)
        if not pool_entry:
            raise HTTPException(404, "IMEI not found in pool")
        imei = pool_entry["imei"]
        name = name or pool_entry["name"]
    norm_imei = cfg.normalize_imei(imei)
    if len(norm_imei) != 15:
        raise HTTPException(400, "IMEI must be exactly 15 digits")

    binding = cfg.set_iccid_imei_binding(iccid, norm_imei, name=name, imei_id=imei_id)

    # Update any existing instance matching this ICCID
    inst = _match_instance_by_iccid(iccid)
    if inst:
        cards = hub.cards_list()
        card_info = next((item for item in cards if item.get("present") and str(item.get("iccid") or "") == iccid), {})
        _auto_promote_card_draft(inst, card_info, cards)

    await hub.broadcast({"type": "imei_bindings", "bindings": cfg.list_iccid_imei_bindings()})
    return {"ok": True, "binding": binding, "bindings": cfg.list_iccid_imei_bindings()}


@app.delete("/api/imei-pool/binding/{iccid}")
async def api_unbind_imei_from_iccid(iccid: str):
    ok = cfg.remove_iccid_imei_binding(iccid)
    await hub.broadcast({"type": "imei_bindings", "bindings": cfg.list_iccid_imei_bindings()})
    return {"ok": ok, "bindings": cfg.list_iccid_imei_bindings()}



@app.delete("/api/devices/{device_id}")
async def api_device_delete(device_id: str):
    """Hide an offline physical device until its next normal heartbeat.

    No hardware identity, desired state, SIM/line association, Agent/VPCD history or cached
    eSIM data is deleted. This endpoint keeps the established DELETE route for compatibility,
    but its operation is deliberately reversible and presentation-only.
    """
    device = next((item for item in await _unified_devices() if item["id"] == device_id), None)
    if not device:
        raise HTTPException(404, "no such physical device")
    if device.get("present"):
        raise HTTPException(409, "disconnect the physical device before hiding it")
    await asyncio.to_thread(device_state.hide_device, device_id)
    await hub.broadcast({"type": "hardware", "device": device_id, "event": "hidden"})
    return {"ok": True, "device_id": device_id, "hidden": True,
            "data_preserved": True, "reappears_on_heartbeat": True}


@app.get("/api/devices/{device_id}/cellular")
async def api_device_cellular(device_id: str):
    device = next((item for item in await _unified_devices() if item["id"] == device_id), None)
    if not device:
        raise HTTPException(404, "no such physical device")
    return {"device_id": device_id, "capability": device["capabilities"]["cellular"],
            "cellular": device.get("cellular")}


@app.get("/api/devices/{device_id}/cellular/profiles")
async def api_device_cellular_profiles(device_id: str):
    remote = _remote_modem_for_device(device_id)
    if not remote or not remote.get("online"):
        raise HTTPException(404, "no online remote cellular modem for this device")
    try:
        result = await modem_registry.rpc(
            str(remote.get("iccid") or ""), "cellular.profile.list", timeout=20)
    except ModemTimeout as exc:
        raise HTTPException(504, str(exc)) from exc
    except (ModemUnavailable, RuntimeError) as exc:
        raise HTTPException(503, str(exc)) from exc
    return {"device_id": device_id, **result}


@app.put("/api/devices/{device_id}/cellular/profile")
async def api_device_cellular_profile_save(device_id: str, body: dict):
    remote = _remote_modem_for_device(device_id)
    if not remote or not remote.get("online"):
        raise HTTPException(404, "no online remote cellular modem for this device")
    allowed = {"name", "apn", "auth", "username", "password"}
    if set(body) - allowed:
        raise HTTPException(400, "unknown cellular profile field")
    name, apn = str(body.get("name") or "").strip(), str(body.get("apn") or "").strip()
    auth = str(body.get("auth") or "NONE").upper()
    if not name or not apn:
        raise HTTPException(400, "profile name and APN are required")
    if len(name) > 100 or len(apn) > 100:
        raise HTTPException(400, "profile name and APN must not exceed 100 characters")
    if auth not in {"NONE", "PAP", "CHAP", "MSCHAPV2"}:
        raise HTTPException(400, "unsupported mobile-broadband authentication method")
    params = {"name": name, "apn": apn, "auth": auth,
              "username": str(body.get("username") or "")[:200],
              "password": str(body.get("password") or "")[:500]}
    try:
        result = await modem_registry.rpc(
            str(remote.get("iccid") or ""), "cellular.profile.save", params, timeout=30)
    except ModemTimeout as exc:
        raise HTTPException(504, str(exc)) from exc
    except (ModemUnavailable, RuntimeError) as exc:
        raise HTTPException(503, str(exc)) from exc
    # Never return the submitted credentials. The platform profile store is authoritative.
    return {"device_id": device_id, "ok": bool(result.get("ok")),
            "name": str(result.get("name") or name),
            "apn": str(result.get("apn") or apn),
            "platform": result.get("platform")}


@app.post("/api/devices/{device_id}/diagnostics")
async def api_device_diagnostics(device_id: str):
    device = next((item for item in await _unified_devices() if item["id"] == device_id), None)
    if not device:
        raise HTTPException(404, "no such physical device")
    checks = [
        {"name": "hardware", "ok": bool(device.get("present")),
         "detail": "detected" if device.get("present") else "not detected"},
        {"name": "cellular", "ok": device["capabilities"]["cellular"]["actual"] in {"on", "off", "unsupported"},
         "detail": device["capabilities"]["cellular"]["actual"]},
        {"name": "vowifi", "ok": device["capabilities"]["vowifi"]["actual"] in {"on", "off"},
         "detail": device["capabilities"]["vowifi"]["actual"]},
        {"name": "country_egress", "ok": (not device["capabilities"]["vowifi"]["desired"]
                                             or bool(device.get("egress", {}).get("node"))),
         "detail": device.get("egress", {}).get("node") or "not selected"},
    ]
    return {"ok": all(item["ok"] for item in checks), "device_id": device_id,
            "checked_at": int(time.time()), "checks": checks}


@app.post("/api/devices/{device_id}/sms/refresh")
async def api_device_sms_refresh(device_id: str):
    remote = _remote_modem_for_device(device_id)
    if not remote:
        raise HTTPException(404, "no such remote cellular modem")
    try:
        result = await modem_registry.rpc(
            str(remote.get("iccid") or ""), "sms.config.refresh", timeout=30)
    except ModemTimeout as exc:
        raise HTTPException(504, str(exc)) from exc
    except (ModemUnavailable, RuntimeError) as exc:
        raise HTTPException(503, str(exc)) from exc
    if not result.get("ok"):
        raise HTTPException(409, str(result.get("error") or
                                     "SMS configuration is still unavailable"))
    return {"device_id": device_id, **result}


@app.post("/api/devices/{device_id}/soft-restart")
async def api_device_soft_restart(device_id: str):
    remote = _remote_modem_for_device(device_id)
    if not remote:
        raise HTTPException(404, "no such remote cellular modem")
    recovery = ((remote.get("status") or {}).get("recovery") or {}).get("soft_restart") or {}
    if not recovery.get("available"):
        raise HTTPException(409, str(recovery.get("reason") or
                                     "Soft restart is unavailable for this device"))
    try:
        result = await modem_registry.rpc(
            str(remote.get("iccid") or ""), "modem.soft_restart", timeout=10)
    except ModemTimeout as exc:
        raise HTTPException(504, str(exc)) from exc
    except (ModemUnavailable, RuntimeError) as exc:
        raise HTTPException(503, str(exc)) from exc
    if not result.get("ok"):
        raise HTTPException(409, str(result.get("error") or "Soft restart was rejected"))
    return {"device_id": device_id, **result}


async def _wait_for_device_request(device_id: str, wanted: dict, timeout: float = 120) -> dict:
    deadline = time.monotonic() + timeout
    latest = {}
    while time.monotonic() < deadline:
        latest = device_state.status()
        current = (latest.get("devices") or {}).get(device_id) or {}
        observed_wanted = current.get("desired") or {}
        if (all(observed_wanted.get(key) == value for key, value in wanted.items())
                and not current.get("transitioning")
                and not (latest.get("shared") or {}).get("transitioning")):
            # A shared MM shutdown resets the USB modem. The orchestrator can publish one
            # intermediate "not connected" sample after the desired state is applied; wait for
            # re-enumeration instead of reporting a failed toggle that actually succeeded.
            if not current.get("present", True) or current.get("error") == "device is not connected":
                await asyncio.sleep(.5)
                continue
            if current.get("error") or (latest.get("shared") or {}).get("error"):
                raise RuntimeError(current.get("error") or latest["shared"]["error"])
            return latest
        await asyncio.sleep(.5)
    raise TimeoutError("device capability transition timed out")


async def _resume_instances(instance_ids: set[str], skip: set[str] | None = None) -> dict:
    failed = {}
    for iid in sorted(instance_ids - (skip or set())):
        inst = cfg.get_instance(iid)
        if not inst:
            continue
        try:
            # Use the full manual-start path so a retry clears frozen health, refreshes the
            # current reader binding, checks PIN/card identity and drops a stale AMI client.
            await api_instance_start(iid)
        except Exception as exc:
            failed[iid] = str(getattr(exc, "detail", exc))
    return failed


@app.patch("/api/devices/{device_id}/capabilities")
async def api_device_capabilities(device_id: str, body: dict):
    allowed = {"cellular_enabled", "vowifi_enabled", "flight_mode", "roaming_enabled"}
    if not body or not set(body).issubset(allowed):
        raise HTTPException(400, "provide cellular_enabled, vowifi_enabled and/or flight_mode only")
    if any(not isinstance(value, bool) for value in body.values()):
        raise HTTPException(400, "capability values must be boolean")

    async with capability_lock:
        unified = await _unified_devices()
        device = next((item for item in unified if item["id"] == device_id), None)
        if not device:
            raise HTTPException(404, "no such physical device")
        remote = _remote_modem_for_device(device_id)
        if remote:
            if (body.get("vowifi_enabled") is True and
                    not bool((remote.get("capabilities") or {}).get("sim_apdu", True))):
                raise HTTPException(
                    409, "VoWiFi is unavailable because this Agent cannot expose SIM APDU access")
            current_caps = device.get("capabilities") or {}
            previous = (device_state.desired().get("devices") or {}).get(device_id) or {
                "cellular_enabled": bool((current_caps.get("cellular") or {}).get("desired")),
                "vowifi_enabled": bool((current_caps.get("vowifi") or {}).get("desired", True)),
                "flight_mode": bool((current_caps.get("flight") or {}).get("desired")),
                "roaming_enabled": bool((current_caps.get("roaming") or {}).get("desired")),
            }
            wanted = {**previous, **body}
            device_state.set_desired(
                device_id, cellular_enabled=wanted["cellular_enabled"],
                vowifi_enabled=wanted["vowifi_enabled"],
                flight_mode=wanted["flight_mode"],
                roaming_enabled=wanted["roaming_enabled"])
            iccid = str(remote.get("iccid") or "")
            try:
                if body.get("flight_mode") is True:
                    await modem_registry.rpc(iccid, "cellular.disable")
                    await modem_registry.rpc(iccid, "radio.set", {"enabled": False})
                elif body.get("flight_mode") is False:
                    await modem_registry.rpc(iccid, "radio.set", {"enabled": True})
                if "roaming_enabled" in body:
                    await modem_registry.rpc(
                        iccid, "cellular.roaming.set",
                        {"enabled": bool(wanted["roaming_enabled"])})
                retry_data = bool(
                    body.get("flight_mode") is False and wanted["cellular_enabled"] or
                    body.get("roaming_enabled") is True and wanted["cellular_enabled"])
                if "cellular_enabled" in body or retry_data:
                    if wanted["cellular_enabled"] and not wanted["flight_mode"]:
                        await modem_registry.rpc(
                            iccid, "cellular.ensure",
                            {"allow_roaming": bool(wanted["roaming_enabled"])}, timeout=75)
                    else:
                        await modem_registry.rpc(iccid, "cellular.disable")
            except ModemTimeout as exc:
                raise HTTPException(504, str(exc)) from exc
            except ModemUnavailable as exc:
                raise HTTPException(503, str(exc)) from exc
            except RuntimeError as exc:
                raise HTTPException(503, str(exc)) from exc

            iid = str(device.get("instance_id") or "")
            if "vowifi_enabled" in body and iid:
                if wanted["vowifi_enabled"]:
                    await api_instance_start(iid)
                else:
                    await api_instance_stop(iid)
            egress.publish()
            await hub.broadcast({"type": "capability", "device": device_id,
                                 "desired": wanted})
            refreshed = await _unified_devices()
            return next(item for item in refreshed if item["id"] == device_id)
        if device.get("device_type") == "reader":
            if "cellular_enabled" in body or "flight_mode" in body or "roaming_enabled" in body:
                raise HTTPException(400, "a smart-card reader has no cellular radio")
            iid = str(device.get("instance_id") or "")
            if not iid:
                if body.get("vowifi_enabled"):
                    raise HTTPException(409, "configure the SIM before enabling VoWiFi")
                return device
            inst = cfg.get_instance(iid)
            previous = bool((inst or {}).get("enabled", True))
            wanted = bool(body.get("vowifi_enabled", previous))
            retry = bool(wanted and not await asyncio.to_thread(engine.is_running, iid))
            if wanted == previous and not retry:
                return device
            if wanted:
                await api_instance_start(iid)
            else:
                await api_instance_stop(iid)
            refreshed = await _unified_devices()
            return next(item for item in refreshed if item["id"] == device_id)

        desired_doc, observed_doc, assignments = _device_sources()
        known = set(assignments) | set(desired_doc.get("devices") or {}) | set(observed_doc.get("devices") or {})
        if device_id not in known:
            raise HTTPException(404, "no such physical device")
        if "roaming_enabled" in body:
            raise HTTPException(400, "roaming control is not available on this local backend")
        present = sorted(key for key in known if (observed_doc.get("devices") or {}).get(
            key, {}).get("present", key in assignments))
        previous = (desired_doc.get("devices") or {}).get(device_id) or desired_doc.get("defaults") or {
            "cellular_enabled": False, "vowifi_enabled": True, "flight_mode": False}
        wanted = {**previous, **body}
        cellular_changed = wanted["cellular_enabled"] != bool(previous.get("cellular_enabled"))
        vowifi_changed = wanted["vowifi_enabled"] != bool(previous.get("vowifi_enabled"))
        flight_changed = bool(wanted.get("flight_mode")) != bool(previous.get("flight_mode"))

        identities = _device_identities()
        cards = hub.cards_list()
        target_observed = (observed_doc.get("devices") or {}).get(device_id) or {}
        target_instance = _instance_for_device(
            device_id, identities.get(device_id) or {}, cards, target_observed)
        target_iid = str(target_instance["id"]) if target_instance else ""
        # Repeating an ON request is an explicit retry when the device-level intent says ON
        # but the line is disabled or its engine has stopped. Do not discard it as a no-op.
        vowifi_retry = bool(
            body.get("vowifi_enabled") is True and target_instance
            and (not target_instance.get("enabled", True)
                 or not await asyncio.to_thread(engine.is_running, target_iid)))
        if not cellular_changed and not vowifi_changed and not flight_changed and not vowifi_retry:
            devices = await _unified_devices()
            return next(item for item in devices if item["id"] == device_id)
        vowifi_action = vowifi_changed or vowifi_retry
        # Data bearer and flight-mode changes are reconciled underneath the existing line.
        # Only a VoWiFi toggle intentionally stops/starts that line.
        running_ids = []
        if target_iid and vowifi_action:
            async with hub.recovery_lock(target_iid):
                # Persist intent before stopping while holding the same gate as auto-recovery.
                # An OFF request can therefore never leave a window in which a queued recovery
                # still sees the old enabled=true state and resurrects the Engine.
                device_state.set_desired(
                    device_id, cellular_enabled=wanted["cellular_enabled"],
                    vowifi_enabled=wanted["vowifi_enabled"],
                    flight_mode=bool(wanted.get("flight_mode")))
                target_instance = cfg.upsert_instance({
                    "id": target_iid, "enabled": bool(wanted["vowifi_enabled"])})
                hub.reset_health(target_iid)
                if await asyncio.to_thread(engine.is_running, target_iid):
                    running_ids.append(target_iid)
                    await asyncio.to_thread(engine.stop, target_iid)
                    await hub.drop_ami(target_iid)
        else:
            device_state.set_desired(
                device_id, cellular_enabled=wanted["cellular_enabled"],
                vowifi_enabled=wanted["vowifi_enabled"],
                flight_mode=bool(wanted.get("flight_mode")))
        egress.publish()
        skip_resume = {target_iid} if target_iid and not wanted["vowifi_enabled"] else set()
        try:
            await _wait_for_device_request(device_id, wanted)
        except TimeoutError as exc:
            await _resume_instances(set(running_ids), skip_resume)
            raise HTTPException(504, str(exc)) from exc
        except RuntimeError as exc:
            await _resume_instances(set(running_ids), skip_resume)
            raise HTTPException(503, str(exc)) from exc

        resume_ids = set(running_ids)
        if vowifi_action and wanted["vowifi_enabled"] and target_instance:
            resume_ids.add(str(target_instance["id"]))
        failed = await _resume_instances(resume_ids, skip_resume)
        await hub.broadcast({"type": "capability", "device": device_id, "desired": wanted,
                             "resume_failed": failed})
        devices = await _unified_devices()
        response = next(item for item in devices if item["id"] == device_id)
        if failed:
            response["resume_failed"] = failed
        return response


# ----------------------------- settings -----------------------------


@app.get("/api/settings")
def api_get_settings():
    return {key: value for key, value in cfg.get_settings().items() if key != "system_name"}


@app.put("/api/settings")
def api_put_settings(body: dict):
    # Ignore the legacy field from older cached clients. Product identity is fixed.
    body.pop("system_name", None)
    proxy = body.get("proxy")
    if proxy is not None:
        if not isinstance(proxy, dict) or not isinstance(proxy.get("profiles", {}), dict) \
                or not isinstance(proxy.get("exits", {}), dict):
            raise HTTPException(400, "invalid proxy library")
        profiles = proxy.get("profiles") or {}
        for profile_id, profile in profiles.items():
            if not re.fullmatch(r"[A-Za-z0-9_.-]{1,80}", str(profile_id)) \
                    or not isinstance(profile, dict):
                raise HTTPException(400, "invalid proxy profile id")
            profile_type = str(profile.get("type") or "")
            if profile_type not in {"subscription", "node", "socks5", "existing", "cellular_sim"}:
                raise HTTPException(400, "invalid proxy profile type")
            if not str(profile.get("name") or "").strip():
                raise HTTPException(400, "proxy profile name is required")
            if profile_type == "node":
                try:
                    egress.validate_node_chain(profile.get("value"))
                except egress.EgressError as exc:
                    raise HTTPException(400, str(exc)) from exc
            if profile_type == "cellular_sim" and not re.fullmatch(
                    r"\d{18,22}", str(profile.get("sim_iccid") or "")):
                raise HTTPException(400, "cellular data proxy requires a valid ICCID")
        for country, exit_cfg in (proxy.get("exits") or {}).items():
            if not egress.normalize_country(country) or not isinstance(exit_cfg, dict):
                raise HTTPException(400, "invalid country exit")
            profile_id = str(exit_cfg.get("profile_id") or "")
            if profile_id and profile_id not in profiles:
                raise HTTPException(400, f"country exit references unknown proxy {profile_id!r}")
    if "timezone" in body:
        try:
            ZoneInfo(str(body.get("timezone") or ""))
        except (ZoneInfoNotFoundError, ValueError) as exc:
            raise HTTPException(400, "unknown timezone") from exc
    defaults = body.get("device_defaults")
    if defaults is not None:
        if not isinstance(defaults, dict) or any(
                key not in {"cellular_enabled", "vowifi_enabled", "flight_mode",
                            "roaming_enabled"}
                for key in defaults):
            raise HTTPException(400, "invalid new-device defaults")
        if any(not isinstance(value, bool) for value in defaults.values()):
            raise HTTPException(400, "new-device defaults must be boolean")
    webhook = body.get("webhook") or {}
    if webhook.get("enabled"):
        try:
            sample = notify_push.build_payload(
                notify_push.EV_INCOMING_SMS,
                {"id": "preview", "name": "SIM", "iccid": "", "msisdn": ""},
                "+10000000000", "123456")
            notify_push.build_webhook_request(webhook, sample)
        except (ValueError, TypeError, json.JSONDecodeError) as exc:
            raise HTTPException(400, f"invalid webhook configuration: {exc}")
    # Telegram is notification-only. Ignore stale clients that
    # still submit a remote command configuration.
    (body.get("telegram") or {}).pop("commands", None)
    pushplus = body.get("pushplus") or {}
    if pushplus.get("enabled"):
        if not str(pushplus.get("token") or "").strip():
            raise HTTPException(400, "PushPlus token is required")
        if str(pushplus.get("template") or "html") not in {"html", "txt", "markdown", "json"}:
            raise HTTPException(400, "unsupported PushPlus template")
    if "updates" in body:
        try:
            body["updates"] = update_check.validate_network_settings(body.get("updates"))
        except update_check.UpdateNetworkError as exc:
            raise HTTPException(400, str(exc)) from exc
        if body["updates"]["proxy_mode"] == "library":
            effective_proxy = (body.get("proxy") if isinstance(body.get("proxy"), dict)
                               else cfg.get_settings().get("proxy")) or {}
            if body["updates"]["proxy_profile_id"] not in (effective_proxy.get("profiles") or {}):
                raise HTTPException(400, "update proxy references an unknown proxy library entry")
    hardware = body.get("hardware")
    if hardware is not None:
        if not isinstance(hardware, dict):
            raise HTTPException(400, "invalid hardware settings")
        backend = str(hardware.get("modem_backend", "auto") or "auto")
        if backend not in {"auto", "serial"}:
            raise HTTPException(400, "modem_backend must be auto or serial")
        # Settings updates replace top-level keys wholesale; a client sending only the
        # backend switch must not wipe the modem profiles beside it.
        body["hardware"] = {**cfg.get_settings().get("hardware", {}), **hardware,
                            "modem_backend": backend}
    saved = cfg.update_settings(body)
    if defaults is not None:
        device_state.set_defaults(**defaults)
    egress.publish(settings=saved)
    return {key: value for key, value in saved.items() if key != "system_name"}


@app.get("/api/egress/status")
def api_egress_status():
    return egress.status()


@app.post("/api/egress/refresh")
def api_egress_refresh():
    cache = os.path.join(cfg.DATA_DIR, "orchestrator", "subscription.yaml")
    try:
        os.remove(cache)
    except FileNotFoundError:
        pass
    cache_dir = os.path.join(cfg.DATA_DIR, "orchestrator", "subscriptions")
    try:
        for name in os.listdir(cache_dir):
            if name.endswith(".yaml"):
                os.remove(os.path.join(cache_dir, name))
    except FileNotFoundError:
        pass
    egress.publish()
    return {"ok": True, "requested_at": int(time.time())}


async def _test_egress_country(country: str):
    country = egress.normalize_country(country)
    exits = (cfg.get_settings().get("proxy") or {}).get("exits") or {}
    if not country or country not in exits:
        raise HTTPException(404, "country exit is not configured")
    egress.publish()
    deadline = time.monotonic() + 25
    latest = {}
    while time.monotonic() < deadline:
        latest = (egress.status().get("exits") or {}).get(country) or {}
        if latest.get("ready"):
            host, port = str(latest.get("proxy_host") or ""), int(latest.get("proxy_port") or 0)
            if not host or not port:
                raise HTTPException(503, "country exit has no UDP test endpoint")
            try:
                latency = await asyncio.to_thread(egress.test_udp_proxy, host, port)
            except egress.EgressError as exc:
                raise HTTPException(503, str(exc)) from exc
            return {"ok": True, "country": country, "node": latest.get("node") or "",
                    "interface": latest.get("interface") or "", "latency_ms": latency}
        if latest.get("error"):
            break
        await asyncio.sleep(.5)
    raise HTTPException(503, latest.get("error") or "no healthy UDP-capable node is ready")


@app.post("/api/egress/profile/{profile_id}/test")
async def api_egress_profile_test(profile_id: str, body: dict | None = None):
    saved = ((cfg.get_settings().get("proxy") or {}).get("profiles") or {}).get(profile_id)
    profile = body if isinstance(body, dict) and body else saved
    if not isinstance(profile, dict):
        raise HTTPException(404, "save this proxy before testing it")
    if profile.get("type") == "cellular_sim":
        iccid = str(profile.get("sim_iccid") or "")
        try:
            result = await modem_registry.rpc(iccid, "cellular.ensure", timeout=75)
        except ModemTimeout as exc:
            return {"ok": False, "unavailable": False, "uncertain": True,
                    "status": "unknown", "error": str(exc), "transport": "cellular"}
        except ModemUnavailable as exc:
            raise HTTPException(503, str(exc)) from exc
        endpoint = result.get("proxy") or {}
        if not result.get("ok") or not endpoint.get("ready"):
            raise HTTPException(503, result.get("error") or "cellular data is unavailable")
        try:
            latency = await asyncio.to_thread(
                egress.test_udp_proxy, endpoint.get("host"), int(endpoint.get("port") or 0))
        except egress.EgressError as exc:
            raise HTTPException(503, str(exc)) from exc
        return {"ok": True, "profile_id": profile_id, "latency_ms": latency,
                "iccid": iccid}
    if profile.get("type") not in {"node", "socks5"}:
        raise HTTPException(400, "this proxy type cannot be tested here")
    try:
        latency = await asyncio.to_thread(egress.test_proxy_profile, profile)
    except egress.EgressError as exc:
        raise HTTPException(503, str(exc)) from exc
    return {"ok": True, "profile_id": profile_id, "latency_ms": latency}


@app.post("/api/egress/{country}/test")
async def api_egress_test(country: str):
    return await _test_egress_country(country)


def _test_push_payload() -> dict:
    return notify_push.build_payload(
        notify_push.EV_INCOMING_SMS,
        {"id": "test", "name": "Gateway test", "iccid": "", "msisdn": ""},
        "+10000000000", "MDD Sim Gateway notification test")


@app.post("/api/notifications/webhook/test")
async def api_webhook_test(body: dict):
    try:
        return await asyncio.to_thread(notify_push.send_webhook, body, _test_push_payload())
    except (ValueError, RuntimeError, json.JSONDecodeError) as exc:
        raise HTTPException(400 if isinstance(exc, (ValueError, json.JSONDecodeError)) else 502,
                            str(exc)) from exc


@app.post("/api/notifications/telegram/test")
async def api_telegram_test(body: dict):
    try:
        return await asyncio.to_thread(notify_push.send_telegram, body, _test_push_payload())
    except ValueError as exc:
        raise HTTPException(400, str(exc)) from exc
    except RuntimeError as exc:
        raise HTTPException(502, str(exc)) from exc


@app.post("/api/notifications/pushplus/test")
async def api_pushplus_test(body: dict):
    try:
        return await asyncio.to_thread(notify_push.send_pushplus, body, _test_push_payload())
    except ValueError as exc:
        raise HTTPException(400, str(exc)) from exc
    except RuntimeError as exc:
        raise HTTPException(502, str(exc)) from exc


@app.get("/api/notifications/deliveries")
def api_notification_deliveries(limit: int = 100):
    return notify_push.delivery_status(limit)


@app.delete("/api/notifications/deliveries")
def api_notification_deliveries_clear():
    notify_push.clear_delivery_history()
    return {"ok": True}


@app.get("/api/system/status")
def api_system_status():
    settings = cfg.get_settings()
    # Served from the poller's sample: collecting here would shell out to vcgencmd/dmesg on
    # every page load of an already power-constrained box.
    host = hub.host_snapshot or sysinfo.collect(cfg.DATA_DIR)
    return {
        "system_name": "MDD Sim Gateway",
        "host": host,
        "host_alerts": hub.host_alerts if hub.host_snapshot else sysinfo.alerts(host),
        "timezone": settings.get("timezone") or "UTC",
        "version": VERSION,
        "repository_url": f"https://github.com/{update_check.repository()}",
        "backups": operations.list_local_backups(),
        "security": {
            "https": True,
            "certificate_mode": "self-signed" if (settings.get("tls") or {}).get("self_signed") else "custom",
            "cert_fingerprint": auth.get_cert_fingerprint(),
            "agent_token": auth.get_or_create_agent_token(),
            "audit_enabled": bool((settings.get("security") or {}).get("audit_enabled", True)),
        },
    }


@app.delete("/api/system/host-alerts")
def api_host_alerts_clear():
    """Acknowledge active host alerts until each condition genuinely clears."""
    if hub.host_alert_state is None:
        hub.host_alert_state = _load_host_alert_state()
    now = time.time()
    cleared = []
    for item in hub.host_alerts:
        code = str(item.get("code") or "")
        if not code:
            continue
        entry = hub.host_alert_state.setdefault(code, {})
        entry["acknowledged"] = True
        entry["acknowledged_at"] = now
        entry.setdefault("at", now)
        cleared.append(code)
    hub.host_alerts = []
    _save_host_alert_state(hub.host_alert_state)
    return {"ok": True, "cleared": cleared}


@app.get("/api/system/media-ingress")
def api_media_ingress_status(request: Request):
    """Describe the current browser's host-owned direct WebRTC route.

    Raw IPs are never accepted from the client.  The response carries opaque IDs from the
    host-orchestrator inventory so a stale page cannot approve a removed interface.
    """
    return media_ingress.status(request.headers.get("host", ""))


@app.post("/api/system/media-ingress/confirm")
def api_media_ingress_confirm(body: dict, request: Request):
    try:
        return media_ingress.confirm(
            str((body or {}).get("candidate_id") or ""),
            str((body or {}).get("inventory_generation") or ""),
            request.headers.get("host", ""),
        )
    except ValueError as exc:
        raise HTTPException(409, str(exc)) from exc


def _agent_health_views() -> list[dict]:
    """Merge host health with live attachment counts without changing device identity."""
    values = {str(item.get("agent_id") or ""): dict(item)
              for item in agent_health_registry.list() if item.get("agent_id")}
    reader_counts: dict[str, int] = {}
    for row in vpcd_registry.snapshot():
        agent_id = str(row.get("agent_id") or "")
        if agent_id and row.get("online"):
            reader_counts[agent_id] = reader_counts.get(agent_id, 0) + 1
    modem_counts: dict[str, int] = {}
    for row in modem_registry.list():
        agent_id = str(row.get("agent_id") or "")
        if agent_id and row.get("online"):
            modem_counts[agent_id] = modem_counts.get(agent_id, 0) + 1
    for agent_id in sorted(set(reader_counts) | set(modem_counts)):
        values.setdefault(agent_id, {
            "agent_id": agent_id,
            "meta": {},
            "snapshot": {},
            "connection": "unreported",
            "online": False,
            "reporting": False,
            "seen_at": None,
        })
    output = []
    for agent_id, item in values.items():
        current = dict(item)
        current["id"] = agent_id
        current["display_id"] = agent_id[-6:] if len(agent_id) > 6 else agent_id
        current["attachments"] = {
            "readers_online": reader_counts.get(agent_id, 0),
            "modems_online": modem_counts.get(agent_id, 0),
        }
        output.append(current)
    return sorted(output, key=lambda item: (
        str((item.get("meta") or {}).get("platform") or "z"),
        str(item.get("agent_id") or ""),
    ))


@app.get("/api/agents/health")
def api_agents_health():
    return {"agents": _agent_health_views(),
            "heartbeat_interval_seconds": 10,
            "fresh_seconds": 25,
            "offline_seconds": 40}


@app.get("/api/system/update/check")
async def api_system_update_check(force: bool = False):
    """Read-only release lookup. Requires an admin session (see _AUTH_PUBLIC).

    The periodic UI poll uses the short in-process cache; only an explicit "Check for updates"
    click passes force=true, so repeated logins/reloads cannot burn GitHub's unauthenticated
    rate limit.
    """
    return await asyncio.to_thread(update_check.check, force)


@app.post("/api/system/update/apply")
async def api_system_update_apply():
    """One-click update: publish a request for the host orchestrator, which runs the detached
    updater (host/mdd_update.py). Responds immediately; progress is polled separately."""
    return await asyncio.to_thread(update_check.request_apply)


@app.get("/api/system/update/progress")
def api_system_update_progress():
    return update_check.apply_status()


@app.post("/api/system/backups")
async def api_system_backup():
    settings = cfg.get_settings()
    return await asyncio.to_thread(
        operations.create_local_backup, "mdd-sim-gateway")


@app.post("/api/system/maintenance")
async def api_system_maintenance(body: dict):
    action = str(body.get("action") or "")
    if action == "clear_notification_history":
        notify_push.clear_delivery_history()
        return {"ok": True, "action": action}
    if action == "refresh_egress":
        return api_egress_refresh()
    if action == "restart_lines":
        restarted, failed = [], {}
        # A saved line may be offline because its SIM is absent or its physical device has
        # VoWiFi disabled. Snapshot only containers that were actually running when the
        # operator requested a restart; never turn a restart operation into "start all saved".
        running = []
        for inst in cfg.list_instances():
            iid = str(inst["id"])
            if _durable_maintenance_pending(iid):
                continue
            if await asyncio.to_thread(engine.is_running, iid):
                running.append(inst)
        for inst in running:
            iid = str(inst["id"])
            try:
                async with hub.recovery_lock(iid):
                    if _durable_maintenance_pending(iid):
                        failed[iid] = "maintenance_in_progress"
                        continue
                    _clear_manual_recovery_history(iid)
                    await asyncio.to_thread(engine.stop, iid)
                    await hub.drop_ami(iid)
                    await asyncio.to_thread(
                        _start_engine_checked, inst, cfg.get_settings(),
                        dev_mounts=os.environ.get("MDD_DEV_MOUNTS", "") == "1")
                restarted.append(iid)
            except Exception as exc:
                failed[iid] = str(getattr(exc, "detail", exc))
        return {"ok": not failed, "action": action, "restarted": restarted, "failed": failed}
    raise HTTPException(400, "unknown maintenance action")


@app.post("/api/system/agent-token")
def api_set_agent_token(body: dict):
    """Set and persist a custom agent token for multi-device agent authentication."""
    token = str(body.get("agent_token") or body.get("token") or "")
    try:
        updated = auth.set_agent_token(token)
        return {"ok": True, "agent_token": updated}
    except ValueError as exc:
        raise HTTPException(400, str(exc))


@app.post("/api/system/agent-token/generate")
def api_generate_agent_token():
    """Generate a high-entropy random agent token string without saving immediately."""
    return {"ok": True, "agent_token": auth.generate_agent_token()}


@app.get("/api/diagnostics/support-bundle")
async def api_support_bundle():
    settings = cfg.get_settings()
    documents = {"devices": device_state.status(), "egress": egress.status(),
                 # What pcscd is actually exposing right now — the one fact every card-path
                 # report needed and the bundle never carried. Passive: monitor state only.
                 "readers": [{"name": item.get("name"), "present": item.get("present"),
                              "hardware_kind": item.get("hardware_kind")}
                             for item in hub.cards_list()],
                 "instances": [{"id": item.get("id"), "name": item.get("name")}
                               for item in cfg.list_instances()]}
    content = await asyncio.to_thread(
        operations.support_bundle, documents,
        (settings.get("maintenance") or {}).get("support_bundle_log_lines", 500))
    headers = {"Content-Disposition": 'attachment; filename="vowifi-support-redacted.zip"'}
    return Response(content, media_type="application/zip", headers=headers)


# ----------------------------- instances -----------------------------
@app.get("/api/instances")
async def api_instances(include_deleted: bool = False):
    return {"instances": _client_instances(include_deleted)}


def _client_instances(include_deleted: bool = False) -> list[dict]:
    out = []
    for inst in cfg.list_instances(include_deleted=include_deleted):
        st = _cached_line_status(inst)
        safe = {k: v for k, v in inst.items() if k not in ("pin", "carrier_identity")}
        safe["has_pin"] = bool(inst.get("pin"))
        safe["proxy_country_effective"] = egress.line_country(inst)
        live_idx = _reader_index_for_instance(inst)
        if live_idx is not None:
            safe["reader_index"] = live_idx
        live_port = _reader_port_for_instance(inst)
        if live_port:
            safe["reader_port"] = live_port
        out.append({**safe, "status": st})
    return out


@app.get("/api/snapshot")
async def api_snapshot():
    """One coherent dashboard snapshot, avoiding three polling requests per refresh."""
    _refresh_card_matches()
    return {"instances": _client_instances(), "cards": _client_cards(),
            "devices": await _unified_devices(), "discovering": not hub.scanned,
            "shared": device_state.status().get("shared") or {}}


@app.get("/api/instances/soft-deleted")
async def api_instances_soft_deleted():
    """List soft-deleted SIM instances in trash."""
    out = []
    for inst in cfg.list_soft_deleted_instances():
        safe = {k: v for k, v in inst.items() if k not in ("pin", "carrier_identity")}
        safe["has_pin"] = bool(inst.get("pin"))
        out.append(safe)
    return {"instances": out, "total": len(out)}


@app.post("/api/instances/{iid}/soft-delete")
async def api_instance_soft_delete(iid: str):
    """Soft-delete a SIM line: stops engine and moves line to recycle bin, preserving history."""
    inst = cfg.get_instance(iid)
    if not inst:
        raise HTTPException(404, "no such instance")
    async with hub.recovery_lock(iid):
        await asyncio.to_thread(engine.stop, iid)
        await hub.drop_ami(iid)
        hub.status_cache.pop(str(iid), None)
        hub.status_sampled_at.pop(str(iid), None)
        hub.status_transitions.pop(str(iid), None)
        hub.health.pop(str(iid), None)
        # Mark soft-deleted while recovery remains fenced.
        cfg.soft_delete_instance(iid)
    store.soft_delete_instance(
        instance_id=iid,
        iccid=str(inst.get("iccid") or ""),
        imsi=str(inst.get("imsi") or ""),
        name=str(inst.get("name") or ""),
    )
    
    _refresh_card_matches()
    await hub.broadcast({"type": "cards", "cards": _client_cards()})
    await hub.broadcast({"type": "line", "instance": str(iid), "event": "soft_deleted"})
    return {"ok": True, "instance": iid, "soft_deleted": True}


@app.post("/api/instances/{iid}/restore")
async def api_instance_restore(iid: str):
    """Restore a soft-deleted SIM line from recycle bin back to active."""
    inst = cfg.get_instance(iid)
    if not inst:
        raise HTTPException(404, "no such instance")
    conflict = _active_instance_with_iccid(inst.get("iccid"), exclude_iid=iid)
    if conflict:
        raise HTTPException(
            409, f"this SIM is already configured as line {conflict.get('id')}")
    
    cfg.restore_instance(iid)
    store.restore_instance(iid)
    
    _refresh_card_matches()
    await hub.broadcast({"type": "cards", "cards": _client_cards()})
    await hub.broadcast({"type": "line", "instance": str(iid), "event": "restored"})
    return {"ok": True, "instance": iid, "restored": True}



@app.post("/api/instances")
async def api_instance_upsert(body: dict):
    if "id" not in body:
        raise HTTPException(400, "id required")
    body = dict(body)
    iid = str(body["id"])
    original_iid = str(body.pop("original_id", "") or "")
    if original_iid and original_iid != iid:
        raise HTTPException(409, "instance ID is immutable")
    body = {key: value for key, value in body.items() if key != "carrier_identity"}
    current = cfg.get_instance(iid) or {}
    candidate_iccid = str(body.get("iccid", current.get("iccid")) or "").strip()
    conflict = _active_instance_with_iccid(candidate_iccid, exclude_iid=iid)
    if conflict:
        raise HTTPException(
            409, f"this SIM is already configured as line {conflict.get('id')}")
    # Reject an explicit rename onto another line's name rather than silently suffixing it:
    # the operator asked for that exact label, and a duplicate makes the name useless as a
    # handle in the UI and audit history.
    if "name" in body and cfg.instance_name_taken(body.get("name"), exclude_iid=iid):
        raise HTTPException(409, "another line already uses that name")
    async with hub.recovery_lock(iid):
        was_running = await asyncio.to_thread(engine.is_running, iid)
        _clear_manual_recovery_history(iid)
        inst = cfg.upsert_instance(body)
        applied = False
        # A running line holds its config in the engine container (rendered instance.json:
        # WebRTC credentials, IMEI, SMSC, User-Agent, …). Editing the config alone doesn't reach
        # the running Asterisk — so restart the container to re-render + reload the new config.
        if was_running:
            try:
                await hub.drop_ami(iid)
                await asyncio.to_thread(_start_engine_checked, inst, cfg.get_settings(),
                                        dev_mounts=os.environ.get("MDD_DEV_MOUNTS", "") == "1")
                applied = True
                asyncio.create_task(push_status(iid))
            except Exception as e:  # noqa
                log.warning("apply-on-save restart failed for %s: %r", iid, e)
    safe = {k: v for k, v in inst.items() if k not in ("pin", "carrier_identity")}
    safe["applied"] = applied      # true => config was re-applied to the running engine
    return safe


@app.put("/api/instances/{iid}/country")
async def api_instance_country(iid: str, body: dict):
    """Select a per-line country exit, or clear it to return to MCC auto-detection."""
    if not cfg.get_instance(iid):
        raise HTTPException(404, "no such instance")
    raw = str(body.get("country") or "").strip()
    country = egress.normalize_country(raw)
    if raw and not country:
        raise HTTPException(400, "country must be a two-letter ISO code")
    safe = await api_instance_upsert({"id": str(iid), "proxy_country": country})
    egress.publish()
    return {"ok": True, "country": country,
            "effective_country": egress.line_country(cfg.get_instance(iid) or {}),
            "applied": bool(safe.get("applied"))}


@app.delete("/api/instances/{iid}")
async def api_instance_delete(iid: str, delete_history: bool = True, confirm_id: str = ""):
    """Delete one SIM line and its engine data; optionally retain SMS/call history.

    If the card is still inserted, suppress automatic draft creation until it is physically
    removed. Otherwise the card monitor would recreate the line immediately and make a
    successful delete look broken.
    """
    if str(confirm_id) != str(iid):
        raise HTTPException(400, "confirm_id must exactly match the SIM line id")
    inst = cfg.get_instance(iid)
    if not inst:
        raise HTTPException(404, "no such instance")
    inserted = any(card_info.get("present") and (
        str(card_info.get("matched") or "") == str(iid)
        or (inst.get("iccid") and str(card_info.get("iccid") or "") == str(inst["iccid"])))
        for card_info in hub.cards_list())
    # Old migrations could leave two records for the same ICCID. Deleting one must not pause
    # or strand the surviving line that should take ownership of the still-inserted SIM.
    replacements = [item for item in cfg.list_instances()
                    if str(item.get("id")) != str(iid)
                    and inst.get("iccid")
                    and str(item.get("iccid") or "") == str(inst.get("iccid"))]
    async with hub.recovery_lock(iid):
        # Stable orchestrator lock survives rmtree(instance). Holding it across every config,
        # history and instance-data mutation makes hard-delete/acquire a one-winner race.
        with _normal_delete_permit_or_http(iid) as delete_permit:
            if inserted and inst.get("iccid") and not replacements:
                await asyncio.to_thread(cfg.suppress_card_until_removal, inst["iccid"])
            await asyncio.to_thread(engine.stop, iid)
            await hub.drop_ami(iid)
            hub.status_cache.pop(str(iid), None)
            hub.status_sampled_at.pop(str(iid), None)
            hub.status_transitions.pop(str(iid), None)
            hub.health.pop(str(iid), None)
            cfg.delete_instance(iid)
            await asyncio.to_thread(
                engine.delete_instance_data, iid, _permit=delete_permit)
            deleted_messages = deleted_calls = 0
            if delete_history:
                deleted_messages, deleted_calls = await asyncio.gather(
                    asyncio.to_thread(store.clear_messages, iid),
                    asyncio.to_thread(store.clear_calls, iid))
            # Line ids are reused by the next created line, so its connectivity timeline always
            # goes with the line it describes.
            _line_state_written.pop(str(iid), None)
            await asyncio.to_thread(store.clear_line_states, iid)
            await asyncio.to_thread(store.clear_allowance_data, iid)
    _refresh_card_matches()
    if inserted and replacements:
        replacement = next((item for item in replacements if item.get("enabled", True)), None)
        if replacement:
            asyncio.create_task(_auto_start_hotplugged_line(str(replacement["id"])))
    await hub.broadcast({"type": "cards", "cards": _client_cards()})
    await hub.broadcast({"type": "line", "instance": str(iid), "event": "deleted"})
    if delete_history:
        await hub.broadcast({"type": "sms", "instance": str(iid),
                             "deleted": deleted_messages})
        await hub.broadcast({"type": "call", "instance": str(iid),
                             "deleted": deleted_calls})
    return {"ok": True, "history_deleted": bool(delete_history),
            "deleted_messages": deleted_messages, "deleted_calls": deleted_calls}


@app.post("/api/instances/{iid}/start")
async def api_instance_start(iid: str, body: dict | None = None):
    """Start (or restart) a line. Actively checks the SIM PIN state first: if the card
    requires a PIN and we have no valid saved one, the start is refused with a structured
    error so the UI can prompt for the PIN — we never bring up the IPsec/IMS engine against
    a locked card. A PIN supplied in the body (re-entry) is verified, saved, and used."""
    inst = cfg.get_instance(iid)
    if not inst:
        raise HTTPException(404, "no such instance")
    async with hub.recovery_lock(iid):
        # The same cross-process permit begins before every startup-related APDU and remains
        # live through Docker create. A Host acquire can win before this block or after it,
        # never between PIN probing and Engine startup.
        with _normal_start_permit_or_http(iid) as permit:
            inst = cfg.get_instance(iid)
            if not inst:
                raise HTTPException(404, "no such instance")
            mism = _card_identity_mismatch(inst)
            if mism:
                _raise_card_mismatch(inst, mism)

            pending_updates: dict = {}
            supplied = (body or {}).get("pin")
            if supplied:
                idx = await asyncio.to_thread(_reader_index_for_instance, inst)
                if idx is not None:
                    chk = await asyncio.to_thread(sim.read_card, idx, supplied)
                    if chk.error and "PIN" in (chk.error or "").upper():
                        suffix = (f" ({chk.pin_tries} tries left)"
                                  if chk.pin_tries is not None else "")
                        raise HTTPException(400, f"PIN error: {chk.error}{suffix}")
                pending_updates["pin"] = supplied
                inst = {**inst, "pin": supplied}

            pf = await _preflight_pin(inst)
            if not pf["ok"]:
                if pf.get("clear"):
                    cfg.clear_pin(str(iid))
                raise HTTPException(409, _pin_preflight_detail(pf))

            settings = cfg.get_settings()
            dev = os.environ.get("MDD_DEV_MOUNTS", "") == "1"
            updates: dict = {}
            live_port = await asyncio.to_thread(_reader_port_for_instance, inst)
            if live_port and live_port != inst.get("reader_port"):
                log.info("instance %s: reader port %s -> %s (live ICCID match)",
                         iid, inst.get("reader_port"), live_port)
                updates["reader_port"] = live_port
                inst = {**inst, "reader_port": live_port}
            live_idx = await asyncio.to_thread(_reader_index_for_instance, inst)
            if live_idx is not None and live_idx != inst.get("reader_index"):
                log.info("instance %s: reader index %s -> %s (port/ICCID resolve)",
                         iid, inst.get("reader_index"), live_idx)
                updates["reader_index"] = live_idx
            if updates:
                pending_updates.update(updates)
            hub.bump_lifecycle_epoch(iid)
            inst = cfg.upsert_instance(
                {"id": str(iid), **pending_updates, "enabled": True})
            _clear_manual_recovery_history(str(iid))
            await hub.drop_ami(iid)
            cid = await asyncio.to_thread(
                _start_engine_checked, inst, settings, dev_mounts=dev, permit=permit)
    asyncio.create_task(push_status(str(iid)))
    return {"ok": True, "container": cid}


@app.post("/api/instances/{iid}/reprovision")
async def api_reprovision(iid: str, body: dict | None = None):
    """Manual re-provision: reset retry state and re-establish the line using the stored
    config (re-reads the SIM, no PIN re-entry). Optional body overrides fields (e.g. sip
    user_agent) before restart. Runs the same PIN preflight as start."""
    inst = cfg.get_instance(iid)
    if not inst:
        raise HTTPException(404, "no such instance")
    async with hub.recovery_lock(iid):
        with _normal_start_permit_or_http(iid) as permit:
            inst = cfg.get_instance(iid)
            if not inst:
                raise HTTPException(404, "no such instance")
            # Body changes are part of the same linearized request; quarantine acquisition
            # cannot succeed after this write while a later APDU/create remains outstanding.
            if body:
                inst = cfg.upsert_instance({"id": str(iid), **body})
            mism = _card_identity_mismatch(inst)
            if mism:
                _raise_card_mismatch(inst, mism)
            pf = await _preflight_pin(inst)
            if not pf["ok"]:
                if pf.get("clear"):
                    cfg.clear_pin(str(iid))
                raise HTTPException(409, _pin_preflight_detail(pf))
            hub.bump_lifecycle_epoch(iid)
            inst = cfg.upsert_instance({"id": str(iid), "enabled": True})
            _clear_manual_recovery_history(str(iid))
            await hub.drop_ami(iid)
            dev = os.environ.get("MDD_DEV_MOUNTS", "") == "1"
            cid = await asyncio.to_thread(
                _start_engine_checked, inst, cfg.get_settings(), dev_mounts=dev,
                permit=permit)
    asyncio.create_task(push_status(str(iid)))
    return {"ok": True, "container": cid}


@app.post("/api/instances/{iid}/pin/clear")
async def api_clear_pin(iid: str):
    """Delete the saved SIM PIN for a line. If it's running, stop it — the next start must
    re-run the PIN flow (the whole point of forgetting the PIN)."""
    inst = cfg.get_instance(iid)
    if not inst:
        raise HTTPException(404, "no such instance")
    async with hub.recovery_lock(iid):
        had = cfg.clear_pin(str(iid))
        if await asyncio.to_thread(engine.is_running, str(iid)):
            await asyncio.to_thread(engine.stop, str(iid))
            await hub.drop_ami(str(iid))
            asyncio.create_task(push_status(str(iid)))
    return {"ok": True, "had_pin": had}


@app.post("/api/instances/{iid}/stop")
async def api_instance_stop(iid: str):
    # Cancel frozen cooldown intent before stopping. Otherwise a pending health recovery can
    # recreate the line after the user explicitly stopped it.
    if not cfg.get_instance(iid):
        raise HTTPException(404, "no such instance")
    async with hub.recovery_lock(iid):
        if not cfg.get_instance(iid):
            raise HTTPException(404, "no such instance")
        hub.bump_lifecycle_epoch(iid)
        cfg.upsert_instance({"id": str(iid), "enabled": False})
        _clear_manual_recovery_history(str(iid))
        runtime = await hub.runtime.get(str(iid), force=True)
        if runtime.get("running"):
            await asyncio.to_thread(
                engine.stop, iid, expected_container_id=runtime.get("container_id"))
        # Tear down the AMI client too — otherwise its Manager keeps auto-reconnecting to the
        # now-removed container (and floods a container that later reuses the docker IP).
        await hub.drop_ami(iid)
    hub.status_cache[str(iid)] = _with_status_activity(str(iid), {
        "state": "STOPPED", "label": status_mod.LABELS["STOPPED"],
        "reason_code": "stopped", "reason": "Stopped.", "detail": {}})
    hub.status_sampled_at[str(iid)] = time.monotonic()
    return {"ok": True}


@app.get("/api/instances/{iid}/status")
async def api_instance_status(iid: str):
    inst = cfg.get_instance(iid)
    if not inst:
        raise HTTPException(404, "no such instance")
    return _cached_line_status(inst)


def _availability_window(now: int, recorded_since: int | None) -> int:
    """How far back the chart reaches: as far as history goes, bounded on both sides."""
    span = (LINE_HISTORY_MIN_SECONDS if recorded_since is None
            else max(LINE_HISTORY_MIN_SECONDS, now - int(recorded_since)))
    return min(span, LINE_HISTORY_MAX_SECONDS)


@app.get("/api/instances/{iid}/availability")
async def api_instance_availability(iid: str):
    """VoWiFi connectivity history for one line, as a gap-aware up/down timeline."""
    inst = cfg.get_instance(str(iid))
    if not inst:
        raise HTTPException(404, "no such instance")
    now = int(time.time())
    recorded_since = await asyncio.to_thread(store.line_state_recorded_since, str(iid))
    span = _availability_window(now, recorded_since)
    start = now - span
    segments = await asyncio.to_thread(store.line_state_timeline, str(iid), start, now)
    return {"instance": str(iid), "start": start, "end": now, "span_seconds": span,
            "max_span_seconds": LINE_HISTORY_MAX_SECONDS,
            "recorded_since": int(recorded_since) if recorded_since is not None else None,
            "segments": segments, "summary": store.line_state_summary(segments)}


@app.get("/api/instances/{iid}/logs")
def api_instance_logs(iid: str, tail: int = 200):
    return {"engine": engine.logs(iid, tail),
            "charon": _read_run_text(iid, "charon.log", 200),
            # Survives container rebuilds, unlike the two above.
            "diagnostics": _read_log_text(iid, "diagnostics.jsonl", 50)}


def _read_run_text(iid, name, tail):
    return _read_instance_text(iid, "run", name, tail)


def _read_log_text(iid, name, tail):
    return _read_instance_text(iid, "logs", name, tail)


def _read_instance_text(iid, folder, name, tail):
    path = os.path.join(cfg.DATA_DIR, "instances", str(iid), folder, name)
    try:
        with open(path, errors="replace") as f:
            return "".join(f.readlines()[-tail:])
    except Exception:
        return ""


@app.post("/api/instances/{iid}/register")
async def api_instance_register(iid: str):
    if await _line_admission_blocked(str(iid)):
        raise HTTPException(409, {"code": "pcscf_rebind",
                                  "message": "The carrier route is changing; REGISTER was not sent."})
    result = await asyncio.to_thread(
        engine.exec_cli_with_pcscf_admission,
        str(iid), engine.IMS_REGISTER_COMMAND)
    if not result.get("admitted"):
        raise HTTPException(409, {"code": "pcscf_rebind",
                                  "message": "The carrier route changed before REGISTER; "
                                             "REGISTER was not sent."})
    return {"output": str(result.get("output") or "")}


# ----------------------------- SMS -----------------------------
@app.get("/api/instances/{iid}/messages/threads")
def api_threads(iid: str):
    return {"threads": store.list_threads(iid)}


@app.get("/api/instances/{iid}/messages/{peer}")
def api_messages(iid: str, peer: str):
    return {"messages": store.list_messages(iid, peer)}


@app.post("/api/instances/{iid}/messages/delete")
async def api_messages_delete(iid: str, body: dict):
    """Delete messages. Body: {ids:[...]} for specific messages, {peer:"..."} for a whole
    conversation, or {all:true} to wipe every message on the line. Broadcasts a refresh."""
    if body.get("all"):
        n = await asyncio.to_thread(store.clear_messages, iid)
    elif body.get("peer") is not None:
        n = await asyncio.to_thread(store.delete_thread, iid, body["peer"])
    elif body.get("ids"):
        n = await asyncio.to_thread(store.delete_messages, iid, body["ids"])
    else:
        raise HTTPException(400, "provide ids, peer, or all")
    await hub.broadcast({"type": "sms", "instance": str(iid), "deleted": n})
    return {"ok": True, "deleted": n}


SMS_RESP_RE = re.compile(r"Received SIP response")
# The patched (sysmocom) Asterisk logs the raw 3GPP RP PDU of every SMS it parses via
# res_pjsip_messaging.c parse_rpdata. For an MO SMS the SMSC returns an async RP-ACK / RP-ERROR
# "submit report" (an incoming application/vnd.3gpp.sms MESSAGE whose Call-ID is
# <our-outbound-Call-ID>:sm-submit-report) — THIS, not the SIP 202 Accepted, is the authoritative
# delivery verdict. Byte 0 low 3 bits = RP-MTI: 3 = RP-ACK (delivered), 5 = RP-ERROR (failed,
# followed by an RP-Cause). 1 = RP-DATA (a real inbound SMS) which we ignore here.
RPDATA_RE = re.compile(r"parse_rpdata:\s*SMS RP-DATA\s*'([0-9a-fA-F]+)'")
_RP_ACK_MTI = 3
_RP_ERROR_MTI = 5
# RP-Cause value (3GPP TS 24.011 §8.2.5.4, values per TS 24.008) -> human reason.
RP_CAUSE = {
    1: "unassigned/unallocated number", 8: "operator determined barring", 10: "call barred",
    11: "reserved", 21: "short message transfer rejected", 22: "memory capacity exceeded",
    27: "destination out of order", 28: "unidentified subscriber", 29: "facility rejected",
    30: "unknown subscriber", 38: "network out of order", 41: "temporary failure",
    42: "congestion", 47: "resources unavailable", 50: "requested facility not subscribed",
    69: "requested facility not implemented", 81: "invalid short message reference value",
    95: "invalid message", 96: "invalid mandatory information", 97: "message type non-existent",
    98: "message not compatible with SM protocol state", 99: "information element non-existent",
    111: "protocol error", 127: "interworking, unspecified",
}


def _decode_rp_report(pdu_hex: str) -> dict | None:
    """Decode an RP submit-report PDU (hex). Returns {ok, cause, reason} for an RP-ACK/RP-ERROR,
    or None when the PDU is not a submit report (e.g. RP-DATA, a real inbound SMS)."""
    try:
        b = bytes.fromhex(pdu_hex)
    except ValueError:
        return None
    if not b:
        return None
    mti = b[0] & 0x07
    if mti == _RP_ACK_MTI:
        return {"ok": True}
    if mti == _RP_ERROR_MTI:
        # octet0 MTI, octet1 msg-ref, octet2 RP-Cause IE length, octet3 cause value (bit8=ext).
        cause = (b[3] & 0x7f) if len(b) >= 4 else None
        reason = RP_CAUSE.get(cause, f"cause {cause}" if cause is not None else "delivery failed")
        return {"ok": False, "cause": cause, "reason": reason}
    return None


def detect_sms_result(iid: str, since=None) -> dict:
    """Determine the real MO SMS outcome. Two authoritative signals, checked in order:
      1. The SMSC's RP-ACK/RP-ERROR submit report (parse_rpdata) — the true delivery verdict.
      2. A SIP 4xx/5xx to our MESSAGE (IMS rejected it before the SMSC).
    A SIP 202/2xx is NOT success — the carrier accepts almost everything and reports the real
    result via the async RP submit report. Returns {ok: True|False|None, code?, reason?}."""
    raw = engine.logs(iid, 4000, since=since)
    raw = re.sub(r"\x1b\[[0-9;]*m", "", raw)
    # 1. RP submit report (authoritative). Take the LAST ACK/ERROR seen in the window (our send's).
    for h in reversed(RPDATA_RE.findall(raw)):
        d = _decode_rp_report(h)
        if d is not None:
            if d["ok"]:
                return {"ok": True}
            return {"ok": False, "reason": d.get("reason", "delivery failed"),
                    "cause": d.get("cause")}
    # 2. Fall back to a negative SIP response to our MESSAGE.
    result = {"ok": None}
    for b in SMS_RESP_RE.split(raw)[1:]:
        m = re.search(r"SIP/2\.0 (\d{3})([^\n]*)", b)
        if not m:
            continue
        if re.search(r"CSeq:\s*\d+\s+MESSAGE", b):   # a response to our MESSAGE
            code = int(m.group(1))
            result = {"ok": 200 <= code < 300, "code": code, "reason": m.group(2).strip()}
    return result


async def _watch_sms_delivery(iid: str, mid: int, since: int, timeout: float = 40.0):
    """Asynchronously resolve an MO SMS's REAL delivery outcome after the IMS accepted it.
    The message is already stored as 'sent'; here we poll for the SMSC's RP submit report (or a
    SIP 4xx) and update the record to 'delivered' or 'failed' (+ reason), broadcasting each change
    so the open Messages view refreshes. On timeout the message stays 'sent' (accepted, delivery
    unconfirmed — e.g. Asterisk SMS debug off, or the network sent no report)."""
    iid = str(iid)
    loops = max(1, int(timeout // 2))
    for _ in range(loops):
        await asyncio.sleep(2)
        if not await asyncio.to_thread(engine.is_running, iid):
            return
        d = await asyncio.to_thread(detect_sms_result, iid, since)
        if d.get("ok") is True:
            store.set_message_status(mid, "delivered", None)
            await hub.broadcast({"type": "sms", "instance": iid,
                                 "message": {"id": mid, "status": "delivered",
                                             "direction": "out", "error": None}})
            return
        if d.get("ok") is False:
            reason = d.get("reason") or "unknown"
            code = d.get("code")
            err = (f"Carrier rejected the SMS: {reason}"
                   + (f" (SIP {code})" if code else "")).strip()
            store.set_message_status(mid, "failed", err)
            await hub.broadcast({"type": "sms", "instance": iid,
                                 "message": {"id": mid, "status": "failed",
                                             "direction": "out", "error": err}})
            return
    # no verdict within the window — leave as 'sent' (accepted, unconfirmed).


async def _send_sms_vowifi(iid: str, to: str, text: str,
                           ami: AmiClient | None = None) -> dict:
    """Submit one MO SMS through Asterisk/IMS and start its delivery watcher."""
    if await _line_admission_blocked(iid):
        return {"ok": False, "unavailable": True, "message": None,
                "error": "VoWiFi is applying a new carrier route; no SMS was submitted.",
                "transport": "vowifi"}
    ami = ami or await hub.ami_for(iid)
    if not ami:
        return {"ok": False, "unavailable": True, "message": None,
                "error": "VoWiFi is not running / its control channel is unavailable.",
                "transport": "vowifi"}
    since = int(time.time())
    async with _pcscf_admission_boundary(iid) as admitted:
        if not admitted:
            return {"ok": False, "unavailable": True, "message": None,
                    "error": "VoWiFi carrier route changed before SMS submission; no SMS "
                             "was submitted.", "transport": "vowifi"}
        rec = store.add_message(iid, "out", to, text, status="pending", transport="vowifi")
        res = await ami.send_sms(to, text)

    if not res.get("ok"):
        # Asterisk itself refused to dispatch (endpoint down, bad address, etc.) — final failure.
        err = res.get("detail") or res.get("error") or "Send rejected by the line."
        store.set_message_status(rec["id"], "failed", err)
        rec["status"], rec["error"] = "failed", err
        await hub.broadcast({"type": "sms", "instance": str(iid), "message": rec})
        return {"ok": False, "message": rec, "error": err, "transport": "vowifi"}

    # IMS accepted the MESSAGE (SIP 202). That is NOT delivery confirmation — mark the message
    # 'sent' now and resolve the REAL outcome asynchronously from the SMSC's RP submit report,
    # flipping it to 'delivered' or 'failed' (+ reason) when it arrives. This keeps the send
    # snappy and stops the old false "success" on carrier/SMSC rejections.
    store.set_message_status(rec["id"], "sent", None)
    rec["status"], rec["error"] = "sent", None
    await hub.broadcast({"type": "sms", "instance": str(iid), "message": rec})
    asyncio.create_task(_watch_sms_delivery(iid, rec["id"], since))
    return {"ok": True, "message": rec, "error": None, "transport": "vowifi",
            "pending_delivery": True}


async def _registered_vowifi_ami(iid: str) -> AmiClient | None:
    """Return a sender only when IMS registration is confirmed before submission.

    This preflight is used solely by ``auto`` routing. If it cannot prove that VoWiFi is ready,
    no SMS has been attempted yet and selecting cellular is safe. Once either transport's send
    operation begins, ``auto`` never retries on the other transport: an action timeout may still
    mean that the first copy reached the SMSC.
    """
    if await _line_admission_blocked(iid):
        return None
    ami = await hub.ami_for(iid)
    if not ami or not ami.connected:
        return None
    state = await ami.registration_state()
    return ami if state == "Registered" else None


async def _send_sms_cellular_unfenced(iid: str, to: str, text: str,
                                      instances: list[dict],
                                      pending_record: dict | None = None) -> dict:
    """Submit a remote SMS after the caller owns the maintenance admission lock."""
    if not remote_modem.attached_iccid(instances, iid):
        raise RuntimeError("remote cellular SMS submission has no attached Agent modem")
    if remote_modem.attached_iccid(instances, iid):
        try:
            result = await remote_modem.invoke(instances, iid, "sms.send",
                                               {"to": to, "body": text}, timeout=190)
        except ModemTimeout as exc:
            result = {"ok": False, "unavailable": False, "uncertain": True,
                      "status": "unknown", "error": str(exc), "transport": "cellular"}
        except ModemUnavailable as exc:
            # Registry rejection occurs before a request frame can be submitted. Close the
            # durable pending row as failed before releasing the maintenance boundary.
            result = {"ok": False, "unavailable": True, "uncertain": False,
                      "status": "unavailable", "error": str(exc),
                      "transport": "cellular"}
        except RuntimeError as exc:
            result = {"ok": False, "unavailable": False, "uncertain": False,
                      "status": "failed", "error": str(exc), "transport": "cellular"}
        result = {"transport": "cellular", "unavailable": False,
                  "uncertain": False, **result}
        if not result.get("ok") and not result.get("error"):
            result["error"] = ("Cellular SMS submission failed"
                               f" ({result.get('hresult') or result.get('status') or 'unknown'}).")
        message_status = ("sent" if result.get("ok") else
                          "unknown" if result.get("uncertain") else "failed")
        rec = pending_record or store.add_message(
            iid, "out", to, text, status="pending", transport="cellular")
        store.set_message_status(rec["id"], message_status, result.get("error"))
        rec["status"], rec["error"] = message_status, result.get("error")
        await hub.broadcast({"type": "sms", "instance": str(iid), "message": rec})
        return {**result, "message": rec}
    raise AssertionError("remote cellular SMS path did not return a result")


def _send_local_sms_guarded_sync(iid: str, to: str, text: str,
                                 instances: list[dict]) -> dict:
    """Own the cross-process admission lock for the complete local paid operation."""
    manager = engine.engine_maintenance_locked(str(iid), blocking=False)
    try:
        manager.__enter__()
    except BlockingIOError:
        return {"ok": False, "unavailable": True, "uncertain": False,
                "status": "maintenance", "error":
                "A verified maintenance transaction is in progress; no SMS was submitted.",
                "transport": "cellular", "message": None}
    try:
        if _durable_maintenance_pending(str(iid)):
            return {"ok": False, "unavailable": True, "uncertain": False,
                    "status": "maintenance", "error":
                    "A verified maintenance transaction is in progress; no SMS was submitted.",
                    "transport": "cellular", "message": None}
        pending = store.add_message(
            iid, "out", to, text, status="pending", transport="cellular")
        try:
            result = cellular_sms.send(
                instances, iid, to, text, timeout=180.0, local_sms_tracker=store,
                existing_message_id=int(pending["id"]))
            reservation_id = result.pop("_reservation_id", None)
            message_status = ("sent" if result.get("ok") else
                              "unknown" if result.get("uncertain") else "failed")
            rec = (store.local_modem_sms_message(reservation_id)
                   if reservation_id is not None else None) or pending
            error = result.get("error")
            store.set_message_status(rec["id"], message_status, error)
            rec["status"], rec["error"] = message_status, error
            return {**result, "message": rec, "transport": "cellular"}
        except Exception:
            # If this write fails too, pending remains and maintenance stays fail-closed. Store
            # startup resolves surviving cellular pending rows to unknown and never replays them.
            store.set_message_status(
                pending["id"], "unknown",
                "Cellular SMS submission was interrupted; delivery is unknown.")
            raise
    finally:
        manager.__exit__(None, None, None)


async def _send_local_sms_guarded(iid: str, to: str, text: str,
                                  instances: list[dict]) -> dict:
    """Keep the recovery lock and worker alive independently of the HTTP request task."""
    async with hub.recovery_lock(str(iid)):
        result = await asyncio.to_thread(
            _send_local_sms_guarded_sync, iid, to, text, instances)
        if result.get("message"):
            await hub.broadcast({"type": "sms", "instance": str(iid),
                                 "message": result["message"]})
        return result


async def _send_sms_cellular(iid: str, to: str, text: str) -> dict:
    """Submit one cellular SMS exactly once, ordered against maintenance publication."""
    instances = await asyncio.to_thread(cfg.list_instances)
    if not remote_modem.attached_iccid(instances, iid):
        # This server-owned task retains recovery_lock while its synchronous worker owns the
        # cross-process flock. Repeated cancellation of the HTTP task cannot release either.
        worker = asyncio.create_task(
            _send_local_sms_guarded(iid, to, text, instances),
            name=f"local-cellular-sms-{iid}")
        return await asyncio.shield(worker)

    async with _maintenance_submission_boundary(str(iid)) as admitted:
        if not admitted:
            return {"ok": False, "unavailable": True, "uncertain": False,
                    "status": "maintenance", "error":
                    "A verified maintenance transaction is in progress; no SMS was submitted.",
                    "transport": "cellular", "message": None}
        # Commit in-flight evidence before the RPC can reach the modem. Timeout/cancellation is
        # resolved before this boundary releases, so maintenance cannot observe false zero work.
        pending = await asyncio.to_thread(
            store.add_message, iid, "out", to, text,
            status="pending", transport="cellular")
        try:
            return await _send_sms_cellular_unfenced(
                iid, to, text, instances, pending_record=pending)
        except BaseException:
            await asyncio.to_thread(
                store.set_message_status, pending["id"], "unknown",
                "Cellular SMS submission was interrupted; delivery is unknown.")
            raise


async def _send_sms_on_line_owned(iid: str, to: str, text: str,
                                  transport: str = "auto") -> dict:
    """Send one MO SMS using ``auto``, ``vowifi`` or ``cellular``.

    ``auto`` prefers a *confirmed registered* VoWiFi route. It selects cellular only before any
    VoWiFi submission has been attempted, and never retries across transports after an error or
    timeout because SMS has no cross-transport idempotency key.
    """
    iid, transport = str(iid), str(transport or "auto").lower()
    if transport not in {"auto", "vowifi", "cellular"}:
        return {"ok": False, "unavailable": True, "message": None,
                "error": "Unknown SMS transport; use auto, vowifi, or cellular."}

    lock = hub.sms_send_locks.setdefault(iid, asyncio.Lock())
    async with lock:
        if transport == "vowifi":
            return await _send_sms_vowifi(iid, to, text)
        if transport == "cellular":
            return await _send_sms_cellular(iid, to, text)

        ami = await _registered_vowifi_ami(iid)
        if ami:
            result = await _send_sms_vowifi(iid, to, text, ami=ami)
        else:
            result = await _send_sms_cellular(iid, to, text)
            if result.get("unavailable"):
                cellular_error = result.get("error") or "Cellular SMS is unavailable."
                result["error"] = f"VoWiFi is not registered. {cellular_error}"
        result["requested_transport"] = "auto"
        return result


def _sms_operation_id(value: object) -> str:
    raw = str(value or "")
    try:
        parsed = uuid.UUID(raw)
    except (ValueError, AttributeError) as exc:
        raise ValueError("SMS operation_id must be a UUID") from exc
    if str(parsed) != raw.casefold():
        raise ValueError("SMS operation_id must use canonical UUID form")
    return str(parsed)


async def _send_sms_submission_owned(iid: str, to: str, text: str,
                                     transport: str, operation_id: str) -> dict:
    try:
        result = await _send_sms_on_line_owned(iid, to, text, transport)
        message_id = ((result.get("message") or {}).get("id")
                      if isinstance(result, dict) else None)
        completed = {**result, "submission_id": operation_id}
        await asyncio.to_thread(
            store.finish_sms_submission, iid, operation_id, "completed",
            completed, message_id)
        return completed
    except BaseException:
        # This task is independent of the HTTP waiter. Process shutdown/crash leaves the active
        # row for store.init to turn orphaned; ordinary failures are made durable immediately.
        with suppress(BaseException):
            await asyncio.to_thread(
                store.finish_sms_submission, iid, operation_id, "orphaned")
        raise


async def send_sms_on_line(iid: str, to: str, text: str,
                           transport: str = "auto",
                           operation_id: str | None = None) -> dict:
    """Give one server-owned task the complete route decision and chargeable submission."""
    iid, transport = str(iid), str(transport or "auto").lower()
    if transport not in {"auto", "vowifi", "cellular"}:
        return {"ok": False, "unavailable": True, "message": None,
                "error": "Unknown SMS transport; use auto, vowifi, or cellular."}
    operation_id = _sms_operation_id(operation_id or str(uuid.uuid4()))
    payload_hash = hashlib.sha256(
        json.dumps([iid, to, text, transport], ensure_ascii=False,
                   separators=(",", ":")).encode("utf-8")).hexdigest()
    guard = store.begin_sms_submission(iid, operation_id, payload_hash, transport)
    if not guard.get("created"):
        if guard.get("conflict"):
            return {"ok": False, "unavailable": True, "uncertain": True,
                    "status": "submission_conflict", "message": None,
                    "submission_id": guard.get("operation_id"),
                    "error": ("A previous SMS submission is unresolved or unacknowledged. "
                              "Review its result and acknowledge it before sending another.")}
        if guard.get("acknowledged"):
            replay = dict(guard.get("result") or {
                "ok": False, "uncertain": True, "status": "acknowledged_unknown",
                "message": None,
                "error": ("This SMS operation had an unknown outcome and was already "
                          "acknowledged; it was not submitted again."),
            })
            # The acknowledgement response may itself have been lost. Return a normal response
            # so the same client operation can converge without another confirm dialog or send.
            replay.pop("unavailable", None)
            return {**replay, "submission_id": operation_id,
                    "replayed_result": True, "submission_acknowledged": True}
        if isinstance(guard.get("result"), dict):
            return {**guard["result"], "submission_id": operation_id,
                    "replayed_result": True}
        return {"ok": False, "unavailable": True, "uncertain": True,
                "status": "unknown" if guard.get("state") == "orphaned" else "busy",
                "message": None, "submission_id": operation_id,
                "error": "This SMS operation is still active or has an unknown outcome."}

    current = hub.sms_submission_tasks.get(iid)
    if current:
        task = current["task"]
        if not current.get("orphaned"):
            return {"ok": False, "unavailable": True, "uncertain": True,
                    "status": "busy", "message": None,
                    "error": ("A cellular or VoWiFi SMS submission is still in progress or "
                              "its response has not yet been acknowledged.")}
        return {"ok": False, "unavailable": True, "uncertain": True,
                "status": "unknown", "message": None,
                "submission_id": current.get("operation_id"),
                "error": "The previous SMS response was interrupted; its outcome is unknown."}

    owner = asyncio.create_task(
        _send_sms_submission_owned(iid, to, text, transport, operation_id),
        name=f"sms-submit-{iid}")
    entry = {"task": owner, "to": to, "text": text, "transport": transport,
             "operation_id": operation_id, "orphaned": False}
    hub.sms_submission_tasks[iid] = entry

    def completed(task: asyncio.Task) -> None:
        # Retrieve failures even when the HTTP waiter disappeared. Identity comparison prevents
        # a late callback from deleting a newer owner after tombstone expiry.
        if not task.cancelled():
            with suppress(BaseException):
                task.exception()
        if hub.sms_submission_tasks.get(iid) is not entry:
            return
        entry["done_at"] = asyncio.get_running_loop().time()

    owner.add_done_callback(completed)
    try:
        result = await asyncio.shield(owner)
    except asyncio.CancelledError:
        entry["orphaned"] = True
        raise
    except BaseException:
        if hub.sms_submission_tasks.get(iid) is entry:
            hub.sms_submission_tasks.pop(iid, None)
        raise
    if hub.sms_submission_tasks.get(iid) is entry:
        hub.sms_submission_tasks.pop(iid, None)
    return result


@app.post("/api/instances/{iid}/sms/send")
async def api_sms_send(iid: str, body: dict):
    to = str((body or {}).get("to") or "").strip()
    text = (body or {}).get("body")
    transport = str((body or {}).get("transport") or "auto").lower()
    if not to or not isinstance(text, str) or not text:
        raise HTTPException(422, "recipient and non-empty message body are required")
    if transport not in {"auto", "vowifi", "cellular"}:
        raise HTTPException(422, "transport must be auto, vowifi, or cellular")
    try:
        operation_id = _sms_operation_id((body or {}).get("operation_id") or str(uuid.uuid4()))
    except ValueError as exc:
        raise HTTPException(422, str(exc)) from exc
    result = await send_sms_on_line(iid, to, text, transport, operation_id)
    if result.pop("unavailable", False):
        raise HTTPException(409, {
            "code": str(result.get("status") or "sms_unavailable"),
            "message": result.get("error") or "SMS submission is unavailable",
            "submission_id": result.get("submission_id"),
            "uncertain": bool(result.get("uncertain")),
        })
    return result


@app.post("/api/instances/{iid}/sms/submissions/{operation_id}/ack")
async def api_sms_submission_ack(iid: str, operation_id: str):
    try:
        operation_id = _sms_operation_id(operation_id)
    except ValueError as exc:
        raise HTTPException(422, str(exc)) from exc
    acknowledged = await asyncio.to_thread(
        store.acknowledge_sms_submission, str(iid), operation_id)
    if not acknowledged:
        raise HTTPException(409, "SMS submission is still active or no longer exists")
    return {"ok": True, "acknowledged": True, "submission_id": operation_id}


# ----------------------------- Allowance / balance -----------------------------
def _allowance_instance(iid: str) -> dict:
    inst = cfg.get_instance(str(iid))
    if not inst:
        raise HTTPException(404, "instance not found")
    return {**inst, "id": str(iid)}


def _allowance_rule(inst: dict) -> dict:
    return allowance.query_rule(inst, carrier_id.lookup(inst))


@app.get("/api/instances/{iid}/allowance")
def api_allowance(iid: str):
    _allowance_instance(iid)
    return {"allowance": allowance.reconcile(str(iid))}


@app.put("/api/instances/{iid}/allowance")
def api_allowance_save(iid: str, body: dict):
    _allowance_instance(iid)
    try:
        values = allowance.clean_allowance(body or {})
    except ValueError as exc:
        raise HTTPException(422, str(exc)) from exc
    return {"allowance": store.save_allowance(str(iid), values, source="manual")}


@app.get("/api/instances/{iid}/allowance/query-rule")
def api_allowance_query_rule(iid: str):
    return {"rule": _allowance_rule(_allowance_instance(iid))}


@app.put("/api/instances/{iid}/allowance/query-rule")
def api_allowance_query_rule_save(iid: str, body: dict):
    inst = _allowance_instance(iid)
    try:
        recipient, text = allowance.validate_rule((body or {}).get("recipient"),
                                                   (body or {}).get("body"))
    except ValueError as exc:
        raise HTTPException(422, str(exc)) from exc
    store.save_allowance_query_rule(str(iid), recipient, text)
    return {"rule": _allowance_rule(inst)}


@app.delete("/api/instances/{iid}/allowance/query-rule")
def api_allowance_query_rule_reset(iid: str):
    inst = _allowance_instance(iid)
    store.delete_allowance_query_rule(str(iid))
    return {"rule": _allowance_rule(inst)}


@app.post("/api/instances/{iid}/allowance/query")
async def api_allowance_query(iid: str, body: dict):
    inst = _allowance_instance(iid)
    rule = _allowance_rule(inst)
    effective = rule.get("effective")
    if not effective:
        raise HTTPException(409, "allowance query method is unknown; configure it in Messages")
    transport = str((body or {}).get("transport") or "auto").lower()
    if transport not in {"auto", "vowifi", "cellular"}:
        raise HTTPException(422, "transport must be auto, vowifi, or cellular")
    query = store.start_allowance_query(
        str(iid), effective["recipient"], effective["body"],
        rule.get("carrier_key") or "", transport)
    operation_id = str(uuid.uuid4())
    result = await send_sms_on_line(str(iid), effective["recipient"],
                                    effective["body"], transport, operation_id)
    if result.get("unavailable"):
        store.set_allowance_query_status(
            query["id"], "unknown" if result.get("uncertain") else "failed")
        if (not result.get("uncertain")
                and result.get("submission_id") == operation_id):
            store.acknowledge_sms_submission(str(iid), operation_id)
        raise HTTPException(409, result.get("error") or "SMS transport unavailable")
    store.set_allowance_query_status(
        query["id"], "sent" if result.get("ok") else
        "unknown" if result.get("uncertain") else "failed")
    if (not result.get("uncertain")
            and result.get("submission_id") == operation_id):
        store.acknowledge_sms_submission(str(iid), operation_id)
    return {"ok": bool(result.get("ok")), "query": query, "rule": rule,
            "send": result}


# ----------------------------- Calls -----------------------------
@app.get("/api/instances/{iid}/calls")
def api_calls(iid: str):
    return {"calls": store.list_calls(iid)}


@app.get("/api/instances/{iid}/calls/open-incoming")
async def api_open_incoming_calls(iid: str):
    """Reconcile open call rows against the current Engine's complete live snapshot."""
    runtime = await hub.runtime.get(str(iid), force=True) or {}
    engine_run_id = str(runtime.get("engine_run_id") or "")
    generation = str(runtime.get("container_id") or "")
    if not runtime.get("running"):
        return {"calls": []}
    if not generation or not engine_run_id:
        raise HTTPException(503, "live Engine identity is incomplete")
    ami = await hub.ami_for(str(iid), runtime)
    if not ami:
        raise HTTPException(503, "live incoming-call snapshot is unavailable")
    snapshot = await ami.complete_channel_snapshot()
    if not snapshot.get("ok"):
        raise HTTPException(503, "live incoming-call snapshot is incomplete")
    confirmed = await hub.runtime.get(str(iid), force=True) or {}
    if (not confirmed.get("running")
            or str(confirmed.get("container_id") or "") != generation
            or str(confirmed.get("engine_run_id") or "") != engine_run_id):
        raise HTTPException(503, "Engine changed during incoming-call snapshot")
    live_linkedids = {str(item.get("Linkedid") or "")
                      for item in snapshot.get("channels", []) if item.get("Linkedid")}
    now = int(time.time())
    calls = []
    for rec in store.list_open_incoming_calls(str(iid), "vowifi"):
        source = str(rec.get("source_call_id") or "")
        record_run = str(rec.get("engine_run_id") or "")
        prefix = record_run + ":"
        linkedid = source[len(prefix):] if record_run and source.startswith(prefix) else ""
        exact_format = bool(record_run and re.fullmatch(
            r"[A-Za-z0-9_.:-]{1,128}", record_run) and
            re.fullmatch(r"[A-Za-z0-9_.-]{1,160}", linkedid))
        if (exact_format and record_run == engine_run_id and linkedid in live_linkedids):
            calls.append(rec)
        elif (not exact_format and record_run in ("", engine_run_id)
              and now - int(rec.get("start_ts") or 0) <= 120):
            # Legacy/mixed Engine records cannot authorize an action. Keep only a very recent
            # diagnostic row from the current/unknown generation; old-generation or old
            # unterminated history must never resurrect a full-screen call.
            calls.append(rec)
    return {"calls": calls}


@app.post("/api/instances/{iid}/calls/delete")
async def api_calls_delete(iid: str, body: dict):
    """Delete call-log entries. Body: {ids:[...]} for specific calls or {all:true} to clear
    the whole log. Broadcasts a refresh so open Softphone views reload the list."""
    if body.get("all"):
        n = await asyncio.to_thread(store.clear_calls, iid)
    elif body.get("ids"):
        n = await asyncio.to_thread(store.delete_calls, iid, body["ids"])
    else:
        raise HTTPException(400, "provide ids or all")
    await hub.broadcast({"type": "call", "instance": str(iid), "deleted": n})
    return {"ok": True, "deleted": n}


async def place_call_on_line(iid: str, to: str, from_endpoint: str = "webrtc") -> dict:
    """Ring the browser endpoint and bridge it to `to` over IMS, logging the call."""
    async with hub.recovery_lock(iid):
        if await _line_admission_blocked(iid):
            return {"ok": False, "unavailable": True,
                    "error": "VoWiFi is applying a new carrier route"}
        ami = await hub.ami_for(iid)
        if not ami:
            return {"ok": False, "unavailable": True, "error": "instance not running"}
        async with _pcscf_admission_boundary(iid) as admitted:
            if not admitted:
                return {"ok": False, "unavailable": True,
                        "error": "VoWiFi carrier route changed before call submission"}
            res = await ami.originate(to, from_endpoint)
        store.add_call(iid, "out", to, status="ringing")
        return res


async def hangup_on_line(iid: str) -> dict:
    ami = await hub.ami_for(iid)
    if not ami:
        return {"ok": False, "unavailable": True, "error": "instance not running"}
    return await ami.hangup_all()


def _runtime_started_ts(runtime: dict) -> int:
    value = str((runtime or {}).get("started_at") or "")
    if not value:
        return 0
    try:
        return int(datetime.fromisoformat(value.replace("Z", "+00:00")).timestamp())
    except (TypeError, ValueError):
        return 0


async def hangup_incoming_vowifi_call(iid: str, call_id: str,
                                      source_call_id: str = "",
                                      supplied_engine_run_id: str = "") -> dict:
    """Terminate one exact inbound VoWiFi call by its Asterisk linkedid.

    This is intentionally narrower than ``hangup_on_line``.  It exists for the browser
    fallback incoming-call panel where the page has a backend call record but no JsSIP
    session/contact.  A stale or mismatched record fails closed and never falls back to
    hanging up the whole line.
    """
    rec = store.get_call_by_id(str(iid), call_id)
    if not rec:
        return {"ok": False, "unavailable": True, "code": "stale_call_identity",
                "error": "incoming call is not open"}
    full_source_call_id = str(rec.get("source_call_id") or "")
    supplied_source = str(source_call_id or "")
    engine_run_id = str(rec.get("engine_run_id") or "")
    supplied_run = str(supplied_engine_run_id or "")
    if (rec.get("end_ts") is not None or rec.get("direction") != "in"
            or str(rec.get("transport") or "vowifi") != "vowifi"
            or not supplied_source or supplied_source != full_source_call_id
            or not supplied_run or supplied_run != engine_run_id
            or not re.fullmatch(r"[A-Za-z0-9_.:-]{1,240}", full_source_call_id)
            or not re.fullmatch(r"[A-Za-z0-9_.:-]{1,128}", engine_run_id)
            or not full_source_call_id.startswith(engine_run_id + ":")):
        return {"ok": False, "unavailable": True, "code": "stale_call_identity",
                "error": "incoming call is not open"}
    linkedid = full_source_call_id[len(engine_run_id) + 1:]
    if not re.fullmatch(r"[A-Za-z0-9_.-]{1,160}", linkedid):
        return {"ok": False, "unavailable": True, "code": "stale_call_identity",
                "error": "incoming call is not open"}
    # Share the same stable per-line lock used by start/stop/reprovision and call admission. The
    # one-shot AMI socket additionally fences Engine changes initiated outside this process.
    terminal_rec = None
    async with hub.recovery_lock(str(iid)):
        runtime = await hub.runtime.get(str(iid), force=True) or {}
        generation = str(runtime.get("container_id") or "")
        if (not runtime.get("running") or not generation
                or str(runtime.get("engine_run_id") or "") != engine_run_id):
            return {"ok": False, "unavailable": True, "code": "stale_call_identity",
                    "error": "incoming call belongs to a stale engine generation"}
        inst = cfg.get_instance(str(iid)) or {}
        engine_host = str(runtime.get("ip") or "")
        if not engine_host or not inst.get("ami_secret"):
            return {"ok": False, "unavailable": True, "code": "hangup_unknown",
                    "error": "exact Engine AMI identity is unavailable"}

        async def generation_current() -> bool:
            current = await hub.runtime.get(str(iid), force=True) or {}
            return bool(current.get("running")
                        and str(current.get("container_id") or "") == generation
                        and str(current.get("engine_run_id") or "") == engine_run_id)

        try:
            async with OneShotAmiSession(
                    str(iid), engine_host, 5038,
                    str(inst.get("ami_user") or "vowifi"), str(inst["ami_secret"]),
                    generation_current) as ami:
                result = await ami.hangup_channels_by_linkedid(linkedid)
        except StaleAmiGeneration as exc:
            result = {"ok": False, "outcome": "stale", "attempted": 0,
                      "remaining": None, "error": str(exc)}
        except Exception as exc:  # fail closed; the UI remains retryable
            result = {"ok": False, "outcome": "unknown", "attempted": 0,
                      "remaining": None, "error": repr(exc)}
        if result.get("ok") and result.get("terminal_confirmed"):
            # Keep terminal persistence inside the same lifecycle lock and require one final
            # exact-generation read. A successful old-generation AMI result must never close a
            # record after an externally initiated Engine replacement became observable.
            if not await generation_current():
                result = {"ok": False, "outcome": "stale",
                          "attempted": int(result.get("attempted") or 0),
                          "remaining": None,
                          "error": "engine generation changed before terminal persistence"}
            else:
                terminal_rec = store.finalize_exact_call(
                    str(iid), rec["id"], full_source_call_id, "ended") or {
                    **rec, "status": "ended", "end_ts": int(time.time())}
    if terminal_rec is not None:
        await hub.broadcast({"type": "call", "instance": str(iid), "call": terminal_rec})
        return {"ok": True, "terminal_confirmed": True,
                "outcome": str(result.get("outcome") or "terminated"),
                "call_id": rec["id"], "source_call_id": full_source_call_id,
                "attempted": int(result.get("attempted") or 0), "remaining": 0}
    outcome = str(result.get("outcome") or "unknown")
    code = ("stale_call_identity" if outcome == "stale" else
            "hangup_partial" if outcome == "partial" else "hangup_unknown")
    return {"ok": False, "unavailable": True, "code": code,
            "terminal_confirmed": False, "outcome": outcome,
            "attempted": int(result.get("attempted") or 0),
            "remaining": result.get("remaining"),
            "error": str(result.get("error") or "exact call termination is unconfirmed")}


async def _webrtc_port_open(port: int, timeout: float = 1.5) -> bool:
    """Bounded liveness check for the host-side Asterisk WebRTC binding."""
    writer = None
    try:
        _, writer = await asyncio.wait_for(
            asyncio.open_connection("127.0.0.1", int(port)), timeout=timeout)
        return True
    except (OSError, asyncio.TimeoutError, ValueError):
        return False
    finally:
        if writer:
            writer.close()
            try:
                await writer.wait_closed()
            except Exception:
                pass


async def _cellular_media_anchor(preferred_iid: str) -> tuple[
        str, AmiClient, dict, int] | tuple[None, None, None, None]:
    """Select one live media anchor from authoritative runtime state.

    Each candidate is inspected and port-probed once.  There is no background fallback and no
    anchor switching after a call has been prepared or committed.
    """
    candidates = [str(preferred_iid)] + [str(item.get("id")) for item in cfg.list_instances()
                                        if str(item.get("id")) != str(preferred_iid)]
    deadline = asyncio.get_running_loop().time() + 8.0
    for candidate in candidates:
        remaining = deadline - asyncio.get_running_loop().time()
        if remaining <= 0:
            break
        inst = cfg.get_instance(candidate)
        if not inst or not ((inst.get("sip") or {}).get("webrtc") or {}).get("enable", True):
            continue
        if await _line_admission_blocked(candidate):
            continue
        try:
            async with asyncio.timeout(min(3.0, remaining)):
                runtime = await hub.runtime.get(candidate, force=True)
                if not runtime.get("running") or not runtime.get("container_id"):
                    continue
                ami = await hub.ami_for(candidate, runtime)
                port = int((inst.get("ports") or {}).get("webrtc") or 8089)
                if (ami and runtime.get("webrtc_host_port") == port and
                        await _webrtc_port_open(port)):
                    return candidate, ami, runtime, port
        except (OSError, asyncio.TimeoutError, ValueError):
            continue
    return None, None, None, None


async def _media_anchor_still_live(session: call_media.MediaSession) -> bool:
    """Fail closed when the prepared anchor was stopped, recreated or rebound."""
    if not session.anchor_iid or not session.anchor_generation or not session.anchor_webrtc_port:
        return False
    if await _line_admission_blocked(session.anchor_iid):
        return False
    runtime = await hub.runtime.get(session.anchor_iid, force=True)
    if (not runtime.get("running") or
            str(runtime.get("container_id") or "") != session.anchor_generation):
        return False
    inst = cfg.get_instance(session.anchor_iid)
    current_port = int(((inst or {}).get("ports") or {}).get("webrtc") or 8089)
    return (current_port == session.anchor_webrtc_port and
            runtime.get("webrtc_host_port") == current_port and
            await _webrtc_port_open(current_port))


async def _prepared_media_still_live(session: call_media.MediaSession) -> bool:
    """Validate the exact prepared bridge and anchor immediately before signalling."""
    writer = session.audio_writer
    task = session.bridge_task
    if (session.closed.is_set() or session.agent_ws is None or writer is None or
            writer.is_closing() or task is None or task.done() or
            not session.media_status().get("ready")):
        return False
    return await _media_anchor_still_live(session)


async def _prepared_session_reusable(session: call_media.MediaSession) -> bool:
    """A pre-answer session may not have AudioSocket yet, but its Agent and anchor must live."""
    if session.closed.is_set() or session.agent_ws is None:
        return False
    return await _media_anchor_still_live(session)


async def _install_cellular_media_extension(ami: AmiClient,
                                            session: call_media.MediaSession) -> None:
    extension = "88" + str(int(session.call_id[:16], 16)).zfill(20)[:12]
    commands = [
        f"dialplan add extension {extension},1,Answer() into from-local",
        (f"dialplan add extension {extension},2,AudioSocket({session.audio_uuid},"
         f"host.docker.internal:{session.port}) into from-local"),
        f"dialplan add extension {extension},3,Hangup() into from-local",
    ]
    for command in commands:
        result = await ami.command(command)
        output = str(result.get("output") or result.get("error") or "")
        if not result.get("ok") or re.search(r"failed|unable|no such", output, re.I):
            await ami.command(f"dialplan remove extension {extension}@from-local")
            raise ModemUnavailable(f"Asterisk could not allocate cellular media: {output[:200]}")
    session.extension = extension


async def _close_cellular_media(session: call_media.MediaSession | None) -> None:
    if not session:
        return
    current = asyncio.current_task()
    release_owner = (getattr(session, "release_state", "") == "terminated"
                     or current in {
                         getattr(session, "release_coordinator_task", None),
                         getattr(session, "termination_task", None),
                     })
    if getattr(session, "release_requested", False) and not release_owner:
        return
    try:
        if modem_registry.resolve(session.iccid):
            await modem_registry.rpc(session.iccid, "audio.close",
                                     {"call_id": session.call_id}, timeout=15)
    except Exception:
        pass
    if session.anchor_iid and session.extension:
        try:
            ami = await hub.ami_for(session.anchor_iid)
            if ami:
                await ami.command(
                    f"dialplan remove extension {session.extension}@from-local")
        except Exception:
            pass
    # release_requested is published without waiting for commit_lock. It can therefore become
    # true while an earlier close is awaiting Agent/AMI cleanup. Recheck immediately before the
    # destructive manager removal so the release coordinator can still persist the terminal
    # lease and remain the sole close owner.
    current = asyncio.current_task()
    release_owner = (getattr(session, "release_state", "") == "terminated"
                     or current in {
                         getattr(session, "release_coordinator_task", None),
                         getattr(session, "termination_task", None),
                     })
    if getattr(session, "release_requested", False) and not release_owner:
        return
    await call_media.manager.close(session.call_id)


async def _expire_prepared_cellular_media(
        session: call_media.MediaSession, ttl: float = CELLULAR_MEDIA_PREPARE_TTL_SECONDS) -> None:
    """Bound orphaned non-billable prepare sessions without touching a committed call."""
    try:
        await asyncio.sleep(max(0.0, ttl))
        if call_media.manager.get(session.call_id) is session:
            await _finalize_abandoned_cellular_media(session)
    except asyncio.CancelledError:
        raise
    except Exception as exc:
        log.warning("prepared cellular media expiry failed: %s", exc)


async def _cancel_media_expiry(session: call_media.MediaSession) -> None:
    task = getattr(session, "expiry_task", None)
    session.expiry_task = None
    if task and task is not asyncio.current_task():
        task.cancel()
        await asyncio.gather(task, return_exceptions=True)


async def _cancel_uncommitted_cellular_media(session: call_media.MediaSession) -> bool:
    """Close a prepare only while its commit boundary still proves no call was signalled."""
    async with session.commit_lock:
        if call_media.manager.get(session.call_id) is not session:
            return True
        if session.commit_result is not None:
            return False
        await asyncio.to_thread(
            store.save_cellular_call_lease, session.call_id, session.instance_iid,
            session.iccid, session.direction, "cancelled")
        await _close_cellular_media(session)
        return True


_CELLULAR_TERMINAL_STATES = {"idle", "ended", "terminated"}
PAID_CALL_MEDIA_GRACE_SECONDS = 10.0
PAID_CALL_RENEW_INTERVAL_SECONDS = 2.0
_cellular_call_alert_lock = asyncio.Lock()


def _cellular_call_alert_path() -> str:
    return os.path.join(cfg.DATA_DIR, "cellular-call-alerts.json")


def _load_cellular_call_alerts() -> dict[str, dict]:
    try:
        with open(_cellular_call_alert_path(), encoding="utf-8") as handle:
            loaded = json.load(handle)
        if (not isinstance(loaded, dict) or loaded.get("version") != 1 or
                not isinstance(loaded.get("alerts"), dict)):
            raise ValueError("cellular call alert store has an invalid shape")
        alerts = loaded["alerts"]
        for call_id, value in alerts.items():
            if (not isinstance(call_id, str) or not call_id or not isinstance(value, dict) or
                    str(value.get("call_id") or "") != call_id):
                raise ValueError("cellular call alert store contains an invalid entry")
        return dict(alerts)
    except FileNotFoundError:
        return {}


def _save_cellular_call_alerts(alerts: dict[str, dict]) -> None:
    path = _cellular_call_alert_path()
    os.makedirs(os.path.dirname(path), mode=0o700, exist_ok=True)
    temporary = f"{path}.{os.getpid()}.{uuid.uuid4().hex}.tmp"
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump({"version": 1, "alerts": alerts}, handle, ensure_ascii=False)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
        directory_fd = os.open(os.path.dirname(path), os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    except BaseException:
        try:
            os.unlink(temporary)
        except OSError:
            pass
        raise


async def _record_cellular_call_alert(session: call_media.MediaSession, error: str) -> dict:
    alert = {
        "type": "cellular_call_alert",
        "instance": str(session.instance_iid),
        "call_id": str(session.call_id),
        "state": "hangup_failed",
        "error": str(error or "The modem did not confirm call termination"),
        "updated_at": int(time.time()),
    }
    try:
        async with _cellular_call_alert_lock:
            alerts = await asyncio.to_thread(_load_cellular_call_alerts)
            alerts[session.call_id] = {key: value for key, value in alert.items()
                                       if key != "type"}
            await asyncio.to_thread(_save_cellular_call_alerts, alerts)
    except Exception:
        # Persistence failure must preserve the original evidence, but an already-open UI can
        # still warn the operator immediately.
        await hub.broadcast(alert)
        raise
    await hub.broadcast(alert)
    return alert


async def _resolve_cellular_call_alert(call_id: str) -> bool:
    call_id = str(call_id or "")
    if not call_id:
        return False
    removed = False
    async with _cellular_call_alert_lock:
        alerts = await asyncio.to_thread(_load_cellular_call_alerts)
        removed = alerts.pop(call_id, None) is not None
        if removed:
            await asyncio.to_thread(_save_cellular_call_alerts, alerts)
    if removed:
        await hub.broadcast({"type": "cellular_call_alert_resolved", "call_id": call_id})
    return removed


async def _attempt_cellular_termination(
        session: call_media.MediaSession, deadline: float | None = None) -> tuple[bool, dict]:
    """One bounded idempotent hangup attempt plus an authoritative status confirmation."""
    if not session.release_operation_id:
        if session.release_attempts >= 3:
            hangup = session.release_result or {"ok": False, "error": "retry budget exhausted"}
        else:
            session.release_attempts += 1
            session.release_operation_id = (
                f"call-release:{session.call_id}:{session.release_attempts}")
            hangup = None
    else:
        hangup = None
    if hangup is None:
        try:
            remaining = ((deadline - asyncio.get_running_loop().time())
                         if deadline is not None else 15.0)
            if remaining <= 0:
                raise asyncio.TimeoutError
            hangup = await modem_registry.rpc(
                session.iccid, "call.hangup", {},
                operation_id=session.release_operation_id,
                timeout=max(0.1, min(15.0, remaining)))
            session.release_unknown = False
            if hangup.get("terminal_confirmed"):
                session.release_result = hangup
            else:
                # Command acceptance is not terminal evidence. A later bounded attempt needs a
                # new operation ID instead of replaying a cached ATH/CHUP false success.
                session.release_operation_id = ""
        except Exception as exc:
            # Transport outcome is unknown. Keep this ID so the next bounded check retrieves the
            # original operation instead of issuing a second hangup command.
            session.release_unknown = True
            hangup = {"ok": False, "error": str(exc), "outcome": "unknown"}
    session.release_result = hangup
    try:
        remaining = ((deadline - asyncio.get_running_loop().time())
                     if deadline is not None else 8.0)
        if remaining <= 0:
            raise asyncio.TimeoutError
        status = await modem_registry.rpc(
            session.iccid, "call.status", {}, timeout=max(0.1, min(8.0, remaining)))
        if (status.get("fresh") and status.get("authoritative") and
                int(status.get("terminal_samples") or 0) >= 2 and
                str(status.get("status") or "").casefold() in _CELLULAR_TERMINAL_STATES):
            confirmed = {"ok": True, "confirmed_by": "call.status",
                         "terminal_confirmed": True, "status": status.get("status"),
                         "observed_at": status.get("observed_at")}
            session.release_result = confirmed
            await asyncio.to_thread(
                store.save_cellular_call_lease, session.call_id, session.instance_iid,
                session.iccid, session.direction, "terminal_confirmed")
            return True, confirmed
    except Exception:
        pass
    return False, hangup


async def _supervise_cellular_termination(session: call_media.MediaSession) -> dict:
    """Finite post-disconnect termination supervisor; never redials and never runs forever."""
    now = asyncio.get_running_loop().time()
    deadline = session.release_deadline or (now + 60.0)
    session.release_deadline = deadline
    checks = 0
    persist_task = asyncio.create_task(asyncio.to_thread(
        store.mark_cellular_call_terminating, session.call_id))
    try:
        await asyncio.wait_for(asyncio.shield(persist_task), timeout=2.0)
    except (Exception, asyncio.TimeoutError) as exc:
        # The existing open lease is still durable recovery evidence. A storage failure must not
        # cause the sole server-owned hangup coordinator to abandon the physical call.
        log.critical("could not persist terminating call state for %s: %s",
                     session.call_id[:12], exc)
        if not persist_task.done():
            persist_task.add_done_callback(
                lambda task: task.exception() if not task.cancelled() else None)
    total_remaining = max(0.1, deadline - asyncio.get_running_loop().time())
    try:
        async with asyncio.timeout(total_remaining):
            while checks < 8 and asyncio.get_running_loop().time() < deadline:
                if checks:
                    remaining = deadline - asyncio.get_running_loop().time()
                    await asyncio.sleep(max(0.0, min(8.0, 1.5 * checks, remaining)))
                async with session.commit_lock:
                    if call_media.manager.get(session.call_id) is not session:
                        return
                    terminal, hangup = await _attempt_cellular_termination(
                        session, deadline=deadline)
                    if terminal:
                        session.release_state = "terminated"
                        await _resolve_cellular_call_alert(session.call_id)
                        await _close_cellular_media(session)
                        return {"ok": True, "released": True,
                                "committed": session.commit_result is not None,
                                "physical_hangup": True, "terminal_confirmed": True,
                                "hangup": hangup}
                    session.release_state = "termination_pending"
                checks += 1
    except asyncio.TimeoutError:
        pass
    try:
        session.release_state = "hangup_failed"
        await _record_cellular_call_alert(
            session, str((session.release_result or {}).get("error") or
                         "The modem did not confirm call termination"))
    except asyncio.CancelledError:
        raise
    except Exception as exc:
        session.release_state = "hangup_failed"
        log.error("cellular termination supervisor failed: %s", exc)
    return {"ok": False, "released": False,
            "committed": session.commit_result is not None,
            "physical_hangup": True,
            "hangup": session.release_result, "termination_pending": False,
            "hangup_failed": True}


async def _finalize_abandoned_cellular_media_owned(
        session: call_media.MediaSession) -> dict:
    """Coordinator body; a committed call is hung up before its audio is removed."""
    termination_task = None
    async with session.commit_lock:
        if call_media.manager.get(session.call_id) is not session:
            return {"ok": True, "released": False, "missing": True}
        committed = session.commit_result is not None
        physical_hangup_required = committed or str(session.direction or "").lower() == "in"
        if physical_hangup_required:
            if session.release_state == "hangup_failed":
                return {"ok": False, "released": False, "committed": committed,
                        "physical_hangup": True,
                        "hangup": session.release_result, "termination_pending": False,
                        "hangup_failed": True}
            session.release_state = "terminating"
            if not session.release_deadline:
                session.release_deadline = asyncio.get_running_loop().time() + 60.0
            if not session.termination_task:
                # No await is allowed between publishing terminating and creating this owner.
                # The request may be cancelled immediately after leaving this critical section;
                # the server-owned coordinator nevertheless remains alive and bounded.
                session.termination_task = asyncio.create_task(
                    _supervise_cellular_termination(session),
                    name=f"cellular-call-terminate-{session.call_id[:8]}")
            termination_task = session.termination_task
        else:
            await asyncio.to_thread(
                store.save_cellular_call_lease, session.call_id, session.instance_iid,
                session.iccid, session.direction, "cancelled")
            await _close_cellular_media(session)
            return {"ok": True, "released": True, "committed": False,
                    "physical_hangup": False, "hangup": None}
    return await asyncio.shield(termination_task)


async def _finalize_abandoned_cellular_media(session: call_media.MediaSession) -> dict:
    """Publish a server-owned release coordinator before this caller can be cancelled."""
    # This synchronous flag is visible even while commit_lock is held by dial/answer. Their final
    # pre-RPC boundary must observe it and refuse to create a new paid carrier action.
    session.release_requested = True
    if getattr(session, "release_state", "") == "hangup_failed":
        return {"ok": False, "released": False, "committed": True,
                "hangup": session.release_result, "termination_pending": False,
                "hangup_failed": True}
    coordinator = getattr(session, "release_coordinator_task", None)
    if coordinator is None:
        # There is deliberately no await before the reference is stored. Coroutines execute this
        # short section atomically on the event loop, so concurrent HTTP/orphan/shutdown callers
        # all shield the same owner even if commit_lock is currently held by call signalling.
        coordinator = asyncio.create_task(
            _finalize_abandoned_cellular_media_owned(session),
            name=f"cellular-call-release-{session.call_id[:8]}")
        session.release_coordinator_task = coordinator
    return await asyncio.shield(coordinator)


async def _supervise_paid_call_lease(session: call_media.MediaSession) -> None:
    """Renew the Agent lease only while all browser/media evidence remains fresh."""
    session.lease_last_healthy_at = asyncio.get_running_loop().time()
    try:
        while call_media.manager.get(session.call_id) is session:
            if (getattr(session, "release_requested", False)
                    or getattr(session, "release_state", "") in {
                    "terminating", "termination_pending", "hangup_failed", "terminated"}):
                return
            media = session.media_status()
            now = asyncio.get_running_loop().time()
            if media.get("ready"):
                renewed = await modem_registry.rpc(
                    session.iccid, "call.lease.renew", {"lease_id": session.call_id},
                    timeout=6)
                if not renewed.get("ok"):
                    raise ModemUnavailable(str(renewed.get("error") or
                                               "Agent rejected the paid-call lease"))
                session.lease_last_healthy_at = now
            elif now - session.lease_last_healthy_at >= PAID_CALL_MEDIA_GRACE_SECONDS:
                log.error("Paid call %s lost media/browser evidence; terminating",
                          session.call_id[:12])
                await _finalize_abandoned_cellular_media(session)
                return
            await asyncio.sleep(PAID_CALL_RENEW_INTERVAL_SECONDS)
    except asyncio.CancelledError:
        raise
    except Exception as exc:
        log.error("Paid-call lease supervisor failed for %s: %s",
                  session.call_id[:12], exc)
        if call_media.manager.get(session.call_id) is session:
            await _finalize_abandoned_cellular_media(session)


def _remote_voice_attachment(iid: str):
    iccid = remote_modem.attached_iccid(cfg.list_instances(), iid)
    attachment = modem_registry.resolve(iccid)
    capabilities = attachment.capabilities if attachment else {}
    status = attachment.status if attachment else {}
    contract_error = call_contract_reason(capabilities) if attachment else ""
    if contract_error or capabilities.get("call_contract_error"):
        raise HTTPException(409, contract_error or capabilities.get("call_contract_error"))
    if not (attachment and capabilities.get("call_signalling") and
            capabilities.get("call_audio") and status.get("call_audio_ready") and
            status.get("call_ready")):
        reason = str(status.get("call_audio_error") or status.get("call_error") or
                     "The remote modem did not pass its call-audio self-test")
        raise HTTPException(409, reason)
    if int(capabilities.get("paid_call_lease_version") or 0) < 1:
        raise HTTPException(
            409, "The Agent does not support the paid-call safety lease; update it before "
                 "placing cellular calls")
    return iccid, attachment


async def _prepare_remote_cellular_media(iid: str, number: str, request: Request,
                                         direction: str) -> tuple[call_media.MediaSession, dict]:
    """Prepare the same fail-closed media bridge for outgoing and incoming calls."""
    iccid, _ = _remote_voice_attachment(iid)
    existing = call_media.manager.for_iccid(iccid)
    unresolved = await asyncio.to_thread(store.open_cellular_call_lease, iccid)
    if unresolved and (not existing or unresolved.get("call_id") != existing.call_id):
        raise HTTPException(
            409, "A previous paid call has not yet been physically confirmed ended")
    reuse = False
    if (direction == "in" and existing and existing.direction == "in" and
            existing.instance_iid == str(iid)):
        try:
            reuse = await _prepared_session_reusable(existing)
        except Exception:
            reuse = False
    if reuse:
        session = existing
    else:
        if existing:
            if not await _cancel_uncommitted_cellular_media(existing):
                raise HTTPException(
                    409, "A cellular call is already active for this SIM; hang it up first")
        anchor_iid, ami, anchor_runtime, anchor_port = await _cellular_media_anchor(str(iid))
        if not ami or not anchor_iid:
            raise HTTPException(409, "No running Asterisk/WebRTC media anchor is available")
        session = None
        try:
            async with hub.recovery_lock(str(anchor_iid)):
                if await _line_admission_blocked(str(anchor_iid)):
                    raise ModemUnavailable("Asterisk media anchor is under recovery")
                # Selection happened before acquiring the gate. Revalidate the exact
                # generation and published port inside it before allocating any durable
                # session, dialplan entry or Agent audio resource.
                current_runtime = await hub.runtime.get(str(anchor_iid), force=True)
                current_generation = str(current_runtime.get("container_id") or "")
                expected_generation = str(anchor_runtime.get("container_id") or "")
                current_inst = cfg.get_instance(str(anchor_iid))
                current_port = int(((current_inst or {}).get("ports") or {}).get(
                    "webrtc") or 8089)
                if (not current_runtime.get("running") or not expected_generation or
                        current_generation != expected_generation or
                        current_port != int(anchor_port) or
                        current_runtime.get("webrtc_host_port") != current_port or
                        not await _webrtc_port_open(current_port)):
                    raise ModemUnavailable(
                        "Asterisk media anchor changed while the call was being prepared")
                ami = await hub.ami_for(str(anchor_iid), current_runtime)
                if not ami:
                    raise ModemUnavailable("Asterisk media anchor is not ready")
                session = await call_media.manager.allocate(iccid)
                session.orphan_handler = _finalize_abandoned_cellular_media
                session.anchor_iid = str(anchor_iid)
                session.anchor_generation = current_generation
                session.anchor_webrtc_port = current_port
                session.instance_iid = str(iid)
                session.direction = direction
                session.number = number
                await asyncio.to_thread(
                    store.save_cellular_call_lease, session.call_id,
                    session.instance_iid, session.iccid, session.direction, "prepared")
                await _install_cellular_media_extension(ami, session)
                opened = await modem_registry.rpc(
                    iccid, "audio.open",
                    {"call_id": session.call_id, "token": session.token},
                    operation_id=f"audio-open:{session.call_id}", timeout=25)
                if not opened.get("ok") or not opened.get("ready"):
                    raise ModemUnavailable(str(opened.get("error") or
                                               "Agent call audio did not become ready"))
                await asyncio.wait_for(session.agent_ready.wait(), 3)
                if not await _media_anchor_still_live(session):
                    raise ModemUnavailable(
                        "Asterisk media anchor changed or its WebRTC port became unavailable")
                session.expiry_task = asyncio.create_task(
                    _expire_prepared_cellular_media(session),
                    name=f"cellular-media-expiry-{session.call_id[:8]}")
        except Exception:
            if session:
                await _cancel_uncommitted_cellular_media(session)
            raise
    provisioning = await _softphone_provisioning(
        session.anchor_iid, request,
        runtime={"running": True, "container_id": session.anchor_generation,
                 "webrtc_host_port": session.anchor_webrtc_port})
    return session, {"ok": True, "call_id": session.call_id,
                     "browser_nonce": session.browser_nonce,
                     "media_target": session.extension,
                     "media_anchor": session.anchor_iid,
                     "direction": session.direction,
                     "softphone": provisioning,
                     "audio": {"backend": "uac", "sample_rate": 8000,
                               "channels": 1, "format": "s16le",
                               "phase": session.phase}}


@app.post("/api/instances/{iid}/call")
async def api_call(iid: str, body: dict):
    # AMI originate cannot prove that the browser has a usable ICE/RTP path before it creates
    # the carrier leg. Keep the legacy route fail-closed; the built-in softphone uses the
    # one-shot local Echo admission on the authenticated WS bridge instead.
    raise HTTPException(409, "Use the browser softphone media-admission flow")


@app.post("/api/instances/{iid}/hangup")
async def api_hangup(iid: str):
    result = await hangup_on_line(iid)
    if result.pop("unavailable", False):
        raise HTTPException(409, result["error"])
    return result


@app.post("/api/instances/{iid}/calls/{call_id}/hangup")
async def api_hangup_incoming_vowifi_call(iid: str, call_id: str, body: dict | None = None):
    result = await hangup_incoming_vowifi_call(
        iid, call_id, str((body or {}).get("source_call_id") or ""),
        str((body or {}).get("engine_run_id") or ""))
    unavailable = result.pop("unavailable", False)
    if unavailable or not (result.get("ok") and result.get("terminal_confirmed") is True):
        code = str(result.get("code") or "hangup_unknown")
        status_code = 503 if code == "hangup_unknown" else 409
        raise HTTPException(status_code, {
            "code": code, "message": result.get("error", "Call termination is unconfirmed"),
            "terminal_confirmed": False, "outcome": result.get("outcome", "unknown"),
            "attempted": result.get("attempted", 0), "remaining": result.get("remaining"),
        })
    return result


def _cellular_call_result_status(value: str) -> tuple[str, bool]:
    state = str(value or "").casefold()
    if state == "active":
        return "answered", False
    if state in {"dialing", "ringing-out"}:
        return "ringing", False
    if state in {"terminated", "ended", "idle"}:
        return "ended", True
    if state == "unknown":
        return "unknown", False
    return state or "unknown", False


async def _recover_cancelled_call_signal(session: call_media.MediaSession) -> None:
    """Lookup-only recovery after HTTP task cancellation; never reissues dial or answer."""
    try:
        async with session.commit_lock:
            if call_media.manager.get(session.call_id) is not session:
                return
            result = None
            try:
                lookup = await modem_registry.rpc(
                    session.iccid, "operation.result",
                    {"operation_id": session.signalling_operation_id}, timeout=10)
                if lookup.get("found") and isinstance(lookup.get("result"), dict):
                    result = lookup["result"]
            except Exception:
                pass
            if result is None:
                state = "unknown"
                try:
                    observed = await modem_registry.rpc(
                        session.iccid, "call.status", {}, timeout=8)
                    state = str(observed.get("status") or "unknown")
                except Exception:
                    pass
                result = {"ok": False, "uncertain": True, "status": state,
                          "error": "request was cancelled after call signalling started"}
            session.signalling_in_flight = False
            session.commit_result = result
            if result.get("ok") or result.get("uncertain"):
                await _cancel_media_expiry(session)
            else:
                await _close_cellular_media(session)
    except asyncio.CancelledError:
        raise
    except Exception as exc:
        log.error("cancelled call signalling recovery failed: %s", exc)


async def _remote_call_signal_with_recovery(
        session: call_media.MediaSession, method: str, params: dict,
        operation_id: str, timeout: float) -> dict:
    """Issue one call signal; transport loss is unknown, never proof it was not executed."""
    if getattr(session, "release_requested", False):
        return {"ok": False, "uncertain": False, "status": "cancelled",
                "error": "call release was requested before carrier signalling"}
    session.signalling_in_flight = True
    session.signalling_method = method
    session.signalling_operation_id = operation_id
    session.signalling_params = dict(params)
    try:
        result = await modem_registry.rpc(
            session.iccid, method, params, operation_id=operation_id, timeout=timeout)
        session.signalling_in_flight = False
        return result
    except asyncio.CancelledError:
        session.commit_result = {
            "ok": False, "uncertain": True, "status": "recovering",
            "error": "request was cancelled while call signalling was in flight"}
        expiry = getattr(session, "expiry_task", None)
        session.expiry_task = None
        if expiry:
            expiry.cancel()
        if not session.signalling_recovery_task or session.signalling_recovery_task.done():
            session.signalling_recovery_task = asyncio.create_task(
                _recover_cancelled_call_signal(session),
                name=f"cellular-signal-recover-{session.call_id[:8]}")
        raise
    except ModemTimeout as exc:
        first_error = str(exc)
    except Exception as exc:
        session.signalling_in_flight = False
        return {"ok": False, "error": str(exc)}
    # Never call the paid method again. Even with the same ID, an Agent restart would have an
    # empty in-memory cache and could execute it twice. operation.result is lookup-only.
    try:
        lookup = await modem_registry.rpc(
            session.iccid, "operation.result", {"operation_id": operation_id}, timeout=10)
        if lookup.get("found") and isinstance(lookup.get("result"), dict):
            session.signalling_in_flight = False
            return lookup["result"]
    except Exception as retry_exc:
        first_error = first_error or str(retry_exc)
    state = "unknown"
    try:
        observed = await modem_registry.rpc(session.iccid, "call.status", {}, timeout=8)
        state = str(observed.get("status") or "unknown")
    except Exception:
        pass
    session.signalling_in_flight = False
    return {"ok": False, "uncertain": True, "status": state,
            "error": first_error or "remote call signalling outcome is unknown"}


async def _close_confirmed_terminal_cellular_media(
        session: call_media.MediaSession | None) -> bool:
    """A stale idle sample may not tear down a prepare or race a call commit."""
    if not session:
        return False
    async with session.commit_lock:
        if (call_media.manager.get(session.call_id) is not session or
                session.commit_result is None):
            return False
        try:
            observed = await modem_registry.rpc(session.iccid, "call.status", {}, timeout=8)
        except Exception:
            return False
        if (not observed.get("fresh") or not observed.get("authoritative") or
                int(observed.get("terminal_samples") or 0) < 2 or
                str(observed.get("status") or "").casefold() not in
                _CELLULAR_TERMINAL_STATES):
            return False
        await asyncio.to_thread(
            store.save_cellular_call_lease, session.call_id, session.instance_iid,
            session.iccid, session.direction, "terminal_confirmed")
        session.release_state = "terminated"
        await _resolve_cellular_call_alert(session.call_id)
        await _close_cellular_media(session)
        return True


def _sync_cellular_call_record(iid: str, state: str) -> dict | None:
    rec = store.get_open_call_for_transport(str(iid), "cellular")
    if not rec:
        return None
    status, ended = _cellular_call_result_status(state)
    store.update_call(rec["id"], status, ended=ended)
    rec["status"] = status
    if ended:
        rec["end_ts"] = int(time.time())
    return rec


@app.post("/api/instances/{iid}/cellular-call/prepare")
async def api_cellular_call_prepare(iid: str, body: dict, request: Request):
    """Allocate browser/Agent media without performing a billable dial operation."""
    inst = cfg.get_instance(str(iid))
    if not inst:
        raise HTTPException(404, "instance not found")
    number = str((body or {}).get("to") or "").strip()
    if not re.fullmatch(r"\+?\d{1,32}", number):
        raise HTTPException(422, "invalid telephone number")
    try:
        _, response = await _prepare_remote_cellular_media(
            str(iid), number, request, "out")
        return response
    except Exception as exc:
        if isinstance(exc, HTTPException):
            raise
        raise HTTPException(409, str(exc)) from exc


@app.post("/api/instances/{iid}/cellular-call/incoming/prepare")
async def api_cellular_incoming_prepare(iid: str, request: Request):
    """Prepare browser media for a currently ringing modem without answering it."""
    if not cfg.get_instance(str(iid)):
        raise HTTPException(404, "instance not found")
    iccid, _ = _remote_voice_attachment(str(iid))
    state = await modem_registry.rpc(iccid, "call.status", timeout=8)
    if str(state.get("status") or "") not in {"ringing-in", "waiting"}:
        raise HTTPException(409, "The cellular call is no longer ringing")
    try:
        _, response = await _prepare_remote_cellular_media(
            str(iid), str(state.get("number") or ""), request, "in")
        return response
    except Exception as exc:
        if isinstance(exc, HTTPException):
            raise
        raise HTTPException(409, str(exc)) from exc


@app.post("/api/instances/{iid}/cellular-call/{call_id}/ring")
async def api_cellular_incoming_ring(iid: str, call_id: str):
    """Ring the registered browser; this still does not answer the physical call."""
    session = call_media.manager.get(call_id)
    if (not session or session.direction != "in" or
            session.instance_iid != str(iid)):
        raise HTTPException(409, "incoming call media session is missing or expired")
    async with session.ring_lock:
        if session.ring_result is not None:
            return session.ring_result
        failure = None
        async with hub.recovery_lock(session.anchor_iid):
            try:
                anchor_live = await _media_anchor_still_live(session)
            except Exception:
                anchor_live = False
            if not anchor_live:
                failure = "Asterisk media anchor is no longer available"
            else:
                try:
                    ami = await hub.ami_for(session.anchor_iid)
                    if not ami:
                        raise ModemUnavailable("Asterisk media anchor is unavailable")
                    async with _pcscf_admission_boundary(
                            session.anchor_iid) as admitted:
                        if not admitted:
                            raise ModemUnavailable(
                                "Asterisk media anchor began a carrier-route transition")
                        result = await ami.originate(
                            session.extension, "webrtc",
                            caller_id=session.number or "cellular")
                    if not result.get("ok"):
                        raise ModemUnavailable(
                            result.get("error") or result.get("detail") or
                            "The browser could not be rung")
                except Exception as exc:
                    failure = str(exc)
        # Cancellation takes commit_lock. It must run after releasing recovery_lock because
        # answer/commit deliberately use the opposite (commit -> recovery) order.
        if failure is not None:
            await _cancel_uncommitted_cellular_media(session)
            raise HTTPException(409, failure)
        session.ring_result = {**result, "ok": True, "call_id": session.call_id}
        return session.ring_result


@app.post("/api/instances/{iid}/cellular-call/{call_id}/browser-media")
async def api_cellular_browser_media(iid: str, call_id: str, body: dict):
    """Accept short-lived browser RTP evidence; this endpoint never signals the modem."""
    session = call_media.manager.get(call_id)
    if not session or session.instance_iid != str(iid):
        raise HTTPException(409, "call media session is missing or expired")
    try:
        status = session.record_browser_evidence(
            str((body or {}).get("nonce") or ""), body or {})
    except (TypeError, ValueError, call_media.MediaUnavailable) as exc:
        raise HTTPException(409, str(exc)) from exc
    return {"ok": True, "call_id": session.call_id, "media": status}


@app.get("/api/instances/{iid}/cellular-call/{call_id}/media")
async def api_cellular_media_status(iid: str, call_id: str):
    """Expose call-scoped evidence without inferring readiness from global device state."""
    session = call_media.manager.get(call_id)
    if not session or session.instance_iid != str(iid):
        raise HTTPException(404, "call media session is missing or expired")
    return {"ok": True, "call_id": session.call_id, "media": session.media_status()}


@app.post("/api/instances/{iid}/cellular-call/{call_id}/answer")
async def api_cellular_incoming_answer(iid: str, call_id: str):
    """Answer the modem once, only after the browser/Asterisk media leg is active."""
    session = call_media.manager.get(call_id)
    iccid = remote_modem.attached_iccid(cfg.list_instances(), iid)
    if (not session or session.direction != "in" or session.iccid != iccid or
            session.instance_iid != str(iid)):
        raise HTTPException(409, "incoming call media session is missing or expired")
    try:
        async with session.commit_lock:
            if session.commit_result is not None:
                return session.commit_result
            await asyncio.wait_for(session.media_prepared.wait(), 12)
            async with hub.recovery_lock(session.anchor_iid):
                async with _pcscf_admission_boundary(
                        session.anchor_iid) as admitted:
                    if not admitted:
                        raise HTTPException(
                            409, "Asterisk media anchor began a carrier-route transition; "
                                 "the cellular call was not answered")
                    try:
                        media_live = await _prepared_media_still_live(session)
                    except Exception:
                        media_live = False
                    if not media_live:
                        if not getattr(session, "release_requested", False):
                            await _close_cellular_media(session)
                        raise HTTPException(
                            409, "prepared browser media closed or its Asterisk anchor changed; "
                                 "the cellular call was not answered")
                    # This durable signalling lease is the atomic admission point. Once it is
                    # committed before SWu's marker, the already-prepared Asterisk channel is
                    # an existing call that graceful shutdown must preserve.
                    if getattr(session, "release_requested", False):
                        raise HTTPException(
                            409, "call release was requested; cellular answer was not sent")
                    await asyncio.to_thread(
                        store.save_cellular_call_lease, session.call_id,
                        session.instance_iid, session.iccid, session.direction, "signalling")
                result = await _remote_call_signal_with_recovery(
                    session, "call.answer", {"lease_id": session.call_id},
                    f"call-answer:{session.call_id}", 30)
            if result.get("status") == "cancelled":
                # release_coordinator owns the durable cancelled transition and media close once
                # this commit_lock is released. Closing here would remove the session before that
                # owner can finish and can strand a prior signalling lease.
                return result
            if not result.get("ok") and not result.get("uncertain"):
                if not getattr(session, "release_requested", False):
                    await _close_cellular_media(session)
                return result
            session.commit_result = {**result, "audio": True,
                                     "call_id": session.call_id}
            session.commit_result["media"] = session.media_status()
            await _cancel_media_expiry(session)
            await asyncio.to_thread(
                store.save_cellular_call_lease, session.call_id, session.instance_iid,
                session.iccid, session.direction, "active")
            session.lease_task = asyncio.create_task(
                _supervise_paid_call_lease(session),
                name=f"paid-call-lease-{session.call_id[:8]}")
            try:
                incoming = store.get_open_call(str(iid), "in", within_s=24 * 3600)
                if incoming and incoming.get("transport") == "cellular":
                    store.update_call(incoming["id"], "answered")
                    incoming["status"] = "answered"
                    await hub.broadcast({"type": "call", "instance": str(iid),
                                         "call": incoming})
            except Exception as exc:
                log.warning("cellular call answered but history update failed: %s", exc)
            return session.commit_result
    except asyncio.TimeoutError as exc:
        if not getattr(session, "release_requested", False):
            await _close_cellular_media(session)
        raise HTTPException(
            409, "browser media did not become ready; the cellular call was not answered") from exc
    except Exception as exc:
        if (session.commit_result is None
                and not getattr(session, "release_requested", False)):
            await _close_cellular_media(session)
        if isinstance(exc, HTTPException):
            raise
        raise HTTPException(409, str(exc)) from exc


@app.post("/api/instances/{iid}/cellular-call/{call_id}/commit")
async def api_cellular_call_commit(iid: str, call_id: str):
    """Dial once, only after the browser, Asterisk and Agent media legs are ready."""
    session = call_media.manager.get(call_id)
    iccid = remote_modem.attached_iccid(cfg.list_instances(), iid)
    if not session or not iccid or session.iccid != iccid:
        raise HTTPException(409, "call media session is missing or expired")
    try:
        # The browser can retry an HTTP request after a transient disconnect. Serialize and
        # cache commit at the server boundary so that such a retry can never produce a second
        # billable ATD operation or a duplicate call record.
        async with session.commit_lock:
            if session.commit_result is not None:
                return session.commit_result
            await asyncio.wait_for(session.media_prepared.wait(), 12)
            async with hub.recovery_lock(session.anchor_iid):
                async with _pcscf_admission_boundary(
                        session.anchor_iid) as admitted:
                    if not admitted:
                        raise HTTPException(
                            409, "Asterisk media anchor began a carrier-route transition; "
                                 "cellular dial was not sent")
                    if not await _prepared_media_still_live(session):
                        raise HTTPException(
                            409, "prepared browser media closed or its Asterisk anchor changed; "
                                 "cellular dial was not sent")
                    if getattr(session, "release_requested", False):
                        raise HTTPException(
                            409, "call release was requested; cellular dial was not sent")
                    await asyncio.to_thread(
                        store.save_cellular_call_lease, session.call_id,
                        session.instance_iid, session.iccid, session.direction, "signalling")
                result = await _remote_call_signal_with_recovery(
                    session, "call.dial", {"to": session.number, "lease_id": session.call_id},
                    f"call-dial:{session.call_id}", 90)
            if result.get("status") == "cancelled":
                return result
            if not result.get("ok") and not result.get("uncertain"):
                session.commit_result = result
                if not getattr(session, "release_requested", False):
                    await _close_cellular_media(session)
                return result
            session.commit_result = {
                **result, "audio": True, "call_id": session.call_id}
            session.commit_result["media"] = session.media_status()
            await _cancel_media_expiry(session)
            await asyncio.to_thread(
                store.save_cellular_call_lease, session.call_id, session.instance_iid,
                session.iccid, session.direction, "active")
            session.lease_task = asyncio.create_task(
                _supervise_paid_call_lease(session),
                name=f"paid-call-lease-{session.call_id[:8]}")
            try:
                rec = store.add_call(
                    str(iid), "out", session.number,
                    status="unknown" if result.get("uncertain") else "ringing",
                    transport="cellular")
                session.commit_result["record"] = rec
                await hub.broadcast({"type": "call", "instance": str(iid), "call": rec})
            except Exception as exc:
                log.warning("cellular call started but history update failed: %s", exc)
            return session.commit_result
    except asyncio.TimeoutError as exc:
        if not getattr(session, "release_requested", False):
            await _close_cellular_media(session)
        raise HTTPException(409, "browser media did not become ready; cellular dial was not sent") from exc
    except Exception as exc:
        if not getattr(session, "release_requested", False):
            await _close_cellular_media(session)
        if isinstance(exc, HTTPException):
            raise
        raise HTTPException(409, str(exc)) from exc


@app.post("/api/instances/{iid}/cellular-call/{call_id}/cancel")
async def api_cellular_call_cancel(iid: str, call_id: str):
    """Cancel only an uncommitted media prepare; never signal or hang up the modem call."""
    session = call_media.manager.get(call_id)
    if not session or session.instance_iid != str(iid):
        return {"ok": True, "cancelled": False, "missing": True}
    if not await _cancel_uncommitted_cellular_media(session):
        return {"ok": True, "cancelled": False, "committed": True}
    return {"ok": True, "cancelled": True}


@app.post("/api/instances/{iid}/cellular-call/{call_id}/release")
async def api_cellular_call_release(iid: str, call_id: str):
    """Dispose a page-owned session atomically; committed calls are explicitly hung up."""
    session = call_media.manager.get(call_id)
    if not session or session.instance_iid != str(iid):
        return {"ok": True, "released": False, "missing": True}
    return await _finalize_abandoned_cellular_media(session)


@app.get("/api/cellular-call-alerts")
async def api_cellular_call_alerts():
    alerts = await asyncio.to_thread(_load_cellular_call_alerts)
    return {"alerts": sorted(alerts.values(), key=lambda item: (
        int(item.get("updated_at") or 0), str(item.get("call_id") or "")), reverse=True)}


@app.delete("/api/cellular-call-alerts/{call_id}")
async def api_dismiss_cellular_call_alert(call_id: str):
    """Acknowledge one persisted warning; never imply or signal physical call termination."""
    return {"ok": True, "dismissed": await _resolve_cellular_call_alert(call_id)}


@app.post("/api/instances/{iid}/cellular-call")
async def api_cellular_call(iid: str, body: dict):
    inst = cfg.get_instance(str(iid))
    if not inst:
        raise HTTPException(404, "instance not found")
    number = str((body or {}).get("to") or "").strip()
    instances = cfg.list_instances()
    if remote_modem.attached_iccid(instances, iid):
        # Remote voice must use prepare/commit so media is proven before the one billable dial.
        # Keep this legacy endpoint only for locally attached legacy modems.
        raise HTTPException(
            409, "The web interface is outdated. Reload the page before placing a remote "
                 "cellular call; direct dial without prepared audio is blocked")
    async with _maintenance_submission_boundary(str(iid)) as admitted:
        if not admitted:
            raise HTTPException(503, {
                "code": "maintenance_in_progress",
                "message": "No cellular call was submitted during maintenance.",
            })
        call_id = f"legacy-{uuid.uuid4().hex}"
        iccid = str(inst.get("iccid") or remote_modem.instance_iccid(instances, iid) or "")
        if not iccid:
            raise HTTPException(409, "The SIM identity is unavailable; no call was submitted")
        await asyncio.to_thread(
            store.save_cellular_call_lease, call_id, str(iid), iccid, "out", "dialing")
        try:
            result = await asyncio.to_thread(
                cellular_call.dial, instances, str(iid), number)
        except BaseException:
            await asyncio.to_thread(
                store.save_cellular_call_lease, call_id, str(iid), iccid, "out", "unknown")
            raise
        unavailable = result.pop("unavailable", False)
        lease_state = ("active" if result.get("ok") else
                       "unknown" if result.get("uncertain") else "cancelled")
        await asyncio.to_thread(
            store.save_cellular_call_lease, call_id, str(iid), iccid, "out", lease_state)
        if unavailable:
            raise HTTPException(409, result.get("error") or "Cellular calling is unavailable")
        if result.get("ok") or result.get("uncertain"):
            rec = store.add_call(
                str(iid), "out", number,
                status="unknown" if result.get("uncertain") else "ringing",
                transport="cellular")
            result["record"] = rec
            result["call_id"] = call_id
            await hub.broadcast({"type": "call", "instance": str(iid), "call": rec})
        return result


@app.get("/api/instances/{iid}/cellular-call/status")
async def api_cellular_call_status(iid: str):
    if not cfg.get_instance(str(iid)):
        raise HTTPException(404, "instance not found")
    instances = cfg.list_instances()
    remote = bool(remote_modem.attached_iccid(instances, iid))
    if remote:
        try:
            result = await remote_modem.invoke(instances, iid, "call.status")
        except ModemUnavailable as exc:
            result = {"unavailable": True, "status": "unknown", "error": str(exc)}
        except RuntimeError as exc:
            result = {"unavailable": False, "status": "unknown", "error": str(exc)}
    else:
        result = await asyncio.to_thread(cellular_call.status, instances, str(iid))
    if not result.get("unavailable"):
        active_iccid = remote_modem.instance_iccid(instances, iid)
        active_media = call_media.manager.for_iccid(active_iccid)
        if active_media:
            active_media.cellular_state = str(result.get("status") or "").casefold()
            result["media"] = active_media.media_status()
        authoritative = bool(
            not remote or (result.get("fresh") and result.get("authoritative")))
        terminal_evidence = bool(
            authoritative and (not remote or
                               int(result.get("terminal_samples") or 0) >= 2))
        state = str(result.get("status") or "").casefold()
        sync_allowed = bool(
            authoritative and
            (state not in _CELLULAR_TERMINAL_STATES or terminal_evidence))
        rec = (_sync_cellular_call_record(str(iid), result.get("status") or "")
               if sync_allowed else None)
        if rec:
            result["record"] = rec
        if terminal_evidence and state in _CELLULAR_TERMINAL_STATES:
            await _close_confirmed_terminal_cellular_media(
                active_media)
    return result


@app.post("/api/instances/{iid}/cellular-call/hangup")
async def api_cellular_call_hangup(iid: str):
    if not cfg.get_instance(str(iid)):
        raise HTTPException(404, "instance not found")
    instances = cfg.list_instances()
    iccid = remote_modem.instance_iccid(instances, iid)
    remote = bool(remote_modem.attached_iccid(instances, iid))
    session = call_media.manager.for_iccid(iccid) if remote else None
    if session:
        if call_media.manager.get(session.call_id) is not session:
            session = None
        else:
            result = await _finalize_abandoned_cellular_media(session)
            if result.get("termination_pending"):
                return result
            if (result.get("released") and not result.get("committed")
                    and not result.get("physical_hangup")):
                return {"ok": True, "cancelled_prepare": True}
    if not session:
        if remote:
            try:
                result = await remote_modem.invoke(
                    instances, iid, "call.hangup", timeout=90)
            except ModemUnavailable as exc:
                result = {"unavailable": True, "error": str(exc)}
        else:
            result = await asyncio.to_thread(cellular_call.hangup, instances, str(iid))
            if result.get("ok") and str(result.get("status") or "").casefold() == "ended":
                result["terminal_confirmed"] = True
    if result.pop("unavailable", False):
        raise HTTPException(409, result.get("error") or "Cellular calling is unavailable")
    if remote and not result.get("terminal_confirmed"):
        unresolved = await asyncio.to_thread(store.open_cellular_call_lease, iccid)
        if unresolved:
            result["termination_pending"] = True
    if result.get("terminal_confirmed"):
        for lease in await asyncio.to_thread(store.list_open_cellular_call_leases):
            if (str(lease.get("instance")) == str(iid)
                    and (not iccid or str(lease.get("iccid")) == str(iccid))):
                await asyncio.to_thread(
                    store.save_cellular_call_lease, lease["call_id"], lease["instance"],
                    lease["iccid"], lease["direction"], "terminal_confirmed")
        rec = _sync_cellular_call_record(str(iid), "ended")
        if rec:
            result["record"] = rec
            await hub.broadcast({"type": "call", "instance": str(iid), "call": rec})
    return result


@app.post("/api/instances/{iid}/cellular-call/answer")
async def api_cellular_call_answer(iid: str):
    instances = cfg.list_instances()
    if remote_modem.attached_iccid(instances, iid):
        raise HTTPException(
            409, "Reload the web interface; remote incoming calls require prepared audio")
    raise HTTPException(409, "Answer is unavailable for this cellular modem")


@app.post("/api/instances/{iid}/cellular-call/dtmf")
async def api_cellular_call_dtmf(iid: str, body: dict):
    digits = str((body or {}).get("digits") or "")
    if not digits or not re.fullmatch(r"[0-9A-D*#]+", digits, re.I):
        raise HTTPException(422, "digits must contain only 0-9, A-D, * or #")
    instances = cfg.list_instances()
    if not remote_modem.attached_iccid(instances, iid):
        raise HTTPException(409, "DTMF is unavailable for this cellular modem")
    try:
        return await remote_modem.invoke(instances, iid, "call.dtmf", {"digits": digits})
    except ModemUnavailable as exc:
        raise HTTPException(409, str(exc)) from exc


async def _softphone_provisioning(iid: str, request: Request,
                                  runtime: dict | None = None) -> dict:
    inst = cfg.get_instance(iid)
    if not inst:
        raise HTTPException(404, "no such instance")
    sip = inst.get("sip", {}) or {}
    wr = sip.get("webrtc", {}) or {}
    ports = inst.get("ports", {})
    if runtime is None:
        runtime = await hub.runtime.get(iid)
    runtime = runtime or {}
    rebind_pending = await _line_admission_blocked(iid)
    configured_port = int(ports.get("webrtc") or 8089)
    running = bool(runtime.get("running") and runtime.get("container_id"))
    port_matches = running and runtime.get("webrtc_host_port") == configured_port
    ingress = media_ingress.status(request.headers.get("host", ""))
    media_ready = bool(ingress.get("confirmed") and ingress.get("candidate")
                       and runtime.get("rtp_mapping_exact") is True)
    host = (request.headers.get("host") or "").split(":")[0] or request.url.hostname

    # Determine scheme and WebSocket URL (supports plain HTTP, HTTPS, Nginx reverse proxy)
    forwarded_proto = request.headers.get("x-forwarded-proto", "").lower()
    scheme = forwarded_proto if forwarded_proto in ("http", "https") else request.url.scheme
    ws_proto = "wss" if scheme == "https" else "ws"
    host_header = request.headers.get("host") or f"{host}:{request.url.port or (443 if scheme == 'https' else 80)}"
    prefix = request.scope.get("root_path", "")
    if not prefix:
        f_prefix = request.headers.get("x-forwarded-prefix", "")
        if "/mdd" in f_prefix or request.url.path.startswith("/mdd"):
            prefix = "/mdd"
    prefix = prefix.rstrip("/")
    generation = str(runtime.get("container_id") or "")
    ws_url = (f"{ws_proto}://{host_header}{prefix}/api/instances/{iid}/ws"
              f"?generation={quote(generation, safe='')}")

    enabled = bool(wr.get("enable", True) and port_matches and media_ready
                   and not rebind_pending)
    return {
        "instance_id": str(iid),
        "enabled": enabled,
        "state": ("rebind_pending" if rebind_pending else
                  "running" if port_matches and media_ready else
                  "stopped" if not running else
                  "port_mismatch" if not port_matches else "media_unconfigured"),
        "media_ready": media_ready,
        "media_error": ("The carrier changed P-CSCF; new media is paused until the graceful "
                        "Engine restart completes." if rebind_pending else
                        "" if media_ready else (
            "The Engine RTP range is not published one-to-one on this host."
            if ingress.get("confirmed") and runtime.get("rtp_mapping_exact") is not True else
            "Confirm and verify this browser's gateway media route before using voice.")),
        "media_ingress": {
            "candidate_id": (ingress.get("candidate") or {}).get("id", ""),
            "address": (ingress.get("candidate") or {}).get("address", ""),
            "interface": (ingress.get("candidate") or {}).get("interface", ""),
            "inventory_generation": ingress.get("inventory_generation", ""),
            "confirmed": bool(ingress.get("confirmed")),
            "reason": ingress.get("reason", ""),
        },
        "media_test_target": "mdd-media-check",
        "ice_servers": [],
        "generation": generation,
        "username": wr.get("username", "webrtc"),
        # Do not disclose a usable SIP credential when the host cannot prove a browser media
        # route.  The WS endpoint independently enforces the same fail-closed condition.
        "password": wr.get("password", "") if enabled else "",
        "ws_port": ports.get("webrtc", 8089),
        "ws_url": ws_url,
        "host": host,
        "realm": cfg.ims_realm(inst["mcc"], inst["mnc"]),
    }


@app.get("/api/instances/{iid}/softphone")
async def api_softphone(iid: str, request: Request):
    """Provisioning for the browser softphone (JsSIP over WSS/WS)."""
    return await _softphone_provisioning(iid, request)


async def _current_softphone_generation(iid: str) -> str:
    runtime = await hub.runtime.get(str(iid), force=True)
    if not runtime.get("running"):
        return ""
    return str(runtime.get("container_id") or "")


@app.post("/api/instances/{iid}/softphone/media-admission/new")
async def api_softphone_media_admission_new(iid: str, request: Request):
    inst = cfg.get_instance(str(iid))
    if not inst:
        raise HTTPException(404, "no such instance")
    runtime = await hub.runtime.get(str(iid), force=True)
    generation = str(runtime.get("container_id") or "")
    configured_port = int((inst.get("ports") or {}).get("webrtc") or 8089)
    webrtc = ((inst.get("sip") or {}).get("webrtc") or {})
    ingress = media_ingress.status(request.headers.get("host", ""))
    route = ingress.get("candidate") or {}
    route_binding = media_ingress.binding_id(ingress)
    ready = bool(webrtc.get("enable", True) and ingress.get("confirmed") and route
                 and route_binding
                 and runtime.get("running")
                 and runtime.get("webrtc_host_port") == configured_port
                 and not await _line_admission_blocked(str(iid)))
    if not ready:
        raise HTTPException(409, "browser media ingress is not ready")
    token = media_admission.issue(str(iid), generation, route_binding)
    if not token:
        raise HTTPException(503, "browser media admission capacity is exhausted")
    return {"token": token, "generation": generation,
            "media_route_id": route_binding, "expires_in": 30}


@app.post("/api/instances/{iid}/softphone/media-evidence")
async def api_softphone_media_evidence(iid: str, body: dict, request: Request):
    token = str((body or {}).get("token") or "")
    evidence = (body or {}).get("evidence")
    generation = await _current_softphone_generation(iid)
    route_binding = media_ingress.binding_id(
        media_ingress.status(request.headers.get("host", "")))
    if (not generation or not route_binding
            or not media_admission.matches_route(
                token, str(iid), generation, route_binding)
            or not media_admission.mark_browser(
                token, str(iid), generation, evidence)):
        raise HTTPException(409, "browser media admission is stale or invalid")
    return media_admission.status(token, str(iid), generation)


@app.post("/api/instances/{iid}/softphone/media-admission")
async def api_softphone_media_admission(iid: str, body: dict, request: Request):
    token = str((body or {}).get("token") or "")
    generation = await _current_softphone_generation(iid)
    route_binding = media_ingress.binding_id(
        media_ingress.status(request.headers.get("host", "")))
    if (not generation or not route_binding
            or not media_admission.matches_route(
                token, str(iid), generation, route_binding)):
        return {"ready": False, "engine_proven": False, "browser_proven": False}
    return media_admission.status(token, str(iid), generation)


def _sip_initial_invite(message: str) -> bool:
    """True only for a dialog-creating INVITE, never an in-dialog re-INVITE."""
    normalized = str(message or "").replace("\r\n", "\n")
    # RFC 3261 inherits header folding. Unfold before looking for the dialog tag so a legal
    # continuation line cannot be mistaken for an initial INVITE.
    normalized = re.sub(r"\n[ \t]+", " ", normalized)
    head = normalized.split("\n\n", 1)[0]
    lines = head.splitlines()
    if not lines or not lines[0].strip().upper().startswith("INVITE "):
        return False
    for line in lines[1:]:
        name, separator, value = line.partition(":")
        if separator and name.strip().casefold() in {"to", "t"}:
            # URI parameters live inside <...>; only parameters after the name-addr can be the
            # To header's dialog tag. Treating `sip:user@example;tag=uri-value` inside brackets
            # as a dialog tag would let an initial call bypass the recovery fence.
            closing = value.rfind(">") if "<" in value else -1
            header_parameters = value[closing + 1:] if closing >= 0 else value
            return not bool(re.search(
                r"(?:^|;)\s*tag\s*=", header_parameters, re.I))
    # A malformed INVITE without To cannot be an established in-dialog request.
    return True


def _sip_message_request(message: str) -> bool:
    """True for a browser-originated SIP MESSAGE request (a new carrier SMS submission)."""
    first = str(message or "").replace("\r\n", "\n").split("\n", 1)[0].strip()
    return bool(re.match(r"^MESSAGE\s+\S+\s+SIP/2\.0\s*$", first, re.I))


def _sip_initial_invite_admission(message: str) -> tuple[str, str, str, int, str, bool] | None:
    """Return the exact initial-INVITE identity used by one-shot admission."""
    if not _sip_initial_invite(message):
        return None
    normalized = re.sub(r"\r?\n[ \t]+", " ", str(message or ""))
    lines = normalized.replace("\r\n", "\n").splitlines()
    if not lines:
        return ("", "", "", 0, "", False)
    match = re.match(r"^INVITE\s+(?:sips?:|tel:)?([^@; >]+)", lines[0].strip(), re.I)
    target = unquote(match.group(1)) if match else ""
    token = ""
    call_id = ""
    from_tag = ""
    cseq = 0
    branch = ""
    has_authorization = False
    for line in lines[1:]:
        name, separator, value = line.partition(":")
        if separator and name.strip().casefold() == "x-mdd-media-token":
            candidate = value.strip()
            if re.fullmatch(r"[A-Za-z0-9_-]{32,128}", candidate):
                token = candidate
            break
    for line in lines[1:]:
        name, separator, value = line.partition(":")
        folded = name.strip().casefold()
        if separator and folded == "cseq":
            match = re.fullmatch(r"\s*(\d+)\s+INVITE\s*", value, re.I)
            if match:
                cseq = int(match.group(1))
        elif separator and folded in {"via", "v"} and not branch:
            match = re.search(r"(?:^|;)\s*branch=([^;\s,]+)", value, re.I)
            if match and len(match.group(1)) <= 160:
                branch = match.group(1)
        elif separator and folded in {"authorization", "proxy-authorization"}:
            has_authorization = bool(value.strip())
    for line in lines[1:]:
        name, separator, value = line.partition(":")
        if separator and name.strip().casefold() in {"call-id", "i"}:
            candidate = value.strip()
            if 0 < len(candidate) <= 255:
                call_id = candidate
            break
    for line in lines[1:]:
        name, separator, value = line.partition(":")
        if separator and name.strip().casefold() in {"from", "f"}:
            closing = value.rfind(">") if "<" in value else -1
            header_parameters = value[closing + 1:] if closing >= 0 else value
            match = re.search(r"(?:^|;)\s*tag\s*=\s*([^;\s]+)",
                              header_parameters, re.I)
            if match and len(match.group(1)) <= 160:
                from_tag = match.group(1)
            break
    transaction_id = (hashlib.sha256(
        f"{call_id}\0{from_tag}".encode("utf-8")).hexdigest()
        if call_id and from_tag else "")
    return target, token, transaction_id, cseq, branch, has_authorization


def _sip_invite_response(message: str) -> tuple[str, int, int] | None:
    """Return (Call-ID+From-tag transaction, CSeq, status) for an INVITE response."""
    normalized = re.sub(r"\r?\n[ \t]+", " ", str(message or ""))
    lines = normalized.replace("\r\n", "\n").splitlines()
    if not lines:
        return None
    status_match = re.match(r"^SIP/2\.0\s+(\d{3})(?:\s|$)", lines[0].strip(), re.I)
    if not status_match:
        return None
    call_id = ""
    from_tag = ""
    cseq = 0
    for line in lines[1:]:
        name, separator, value = line.partition(":")
        if not separator:
            continue
        folded = name.strip().casefold()
        if folded in {"call-id", "i"} and 0 < len(value.strip()) <= 255:
            call_id = value.strip()
        elif folded in {"from", "f"}:
            closing = value.rfind(">") if "<" in value else -1
            match = re.search(r"(?:^|;)\s*tag\s*=\s*([^;\s]+)",
                              value[closing + 1:] if closing >= 0 else value, re.I)
            if match and len(match.group(1)) <= 160:
                from_tag = match.group(1)
        elif folded == "cseq":
            match = re.fullmatch(r"\s*(\d+)\s+INVITE\s*", value, re.I)
            if match:
                cseq = int(match.group(1))
    if not call_id or not from_tag or not cseq:
        return None
    identity = hashlib.sha256(f"{call_id}\0{from_tag}".encode("utf-8")).hexdigest()
    return identity, cseq, int(status_match.group(1))


def _discard_async_task_result(task: asyncio.Task) -> None:
    try:
        task.exception()
    except (asyncio.CancelledError, Exception):
        pass


def _abort_websocket_transport(websocket) -> None:
    """Synchronously make a timed-out upstream incapable of submitting bytes later."""
    for owner in (websocket, getattr(websocket, "protocol", None)):
        transport = getattr(owner, "transport", None)
        abort = getattr(transport, "abort", None)
        if callable(abort):
            try:
                abort()
            except Exception:
                pass
            return


async def _bounded_upstream_submission(upstream_ws, message) -> bool:
    """Queue one new carrier operation with a hard lock-hold bound.

    ``asyncio.wait_for`` waits for cancellation acknowledgement and therefore is not a hard
    deadline when a third-party send coroutine suppresses cancellation.  At the deadline (or if
    the caller is cancelled), abort the real transport synchronously and detach the cancelled
    task.  It can no longer submit bytes after the P-CSCF flock is released.
    """
    task = asyncio.create_task(upstream_ws.send(message))
    try:
        done, _pending = await asyncio.wait(
            {task}, timeout=SOFTPHONE_UPSTREAM_SUBMIT_TIMEOUT_SECONDS)
    except asyncio.CancelledError:
        _abort_websocket_transport(upstream_ws)
        task.cancel()
        task.add_done_callback(_discard_async_task_result)
        raise
    if task in done:
        await task
        return True
    _abort_websocket_transport(upstream_ws)
    task.cancel()
    task.add_done_callback(_discard_async_task_result)
    return False


async def _bounded_upstream_close(upstream_ws) -> None:
    """Best-effort close without turning its one-second deadline into another wait-for trap."""
    task = asyncio.create_task(upstream_ws.close())
    try:
        done, _pending = await asyncio.wait({task}, timeout=1.0)
    except asyncio.CancelledError:
        _abort_websocket_transport(upstream_ws)
        task.cancel()
        task.add_done_callback(_discard_async_task_result)
        raise
    if task in done:
        try:
            await task
        except Exception:
            pass
        return
    _abort_websocket_transport(upstream_ws)
    task.cancel()
    task.add_done_callback(_discard_async_task_result)


async def _forward_softphone_client(websocket: WebSocket, upstream_ws, iid: str,
                                    generation: str = "", websocket_id: str = "",
                                    media_route_id: str = "", host_header: str = "") -> None:
    """Forward browser SIP while fencing only new dialogs during Engine recovery."""
    try:
        while True:
            event = await websocket.receive()
            if event.get("type") == "websocket.disconnect":
                return
            msg = event.get("text")
            if msg is None:
                msg = event.get("bytes")
            if msg is None:
                continue
            try:
                sip_text = msg.decode("utf-8", errors="strict") \
                    if isinstance(msg, bytes) else str(msg)
            except UnicodeError:
                await websocket.close(code=4414, reason="invalid SIP frame")
                return
            # BYE/CANCEL and in-dialog traffic must continue so existing calls can terminate
            # cleanly. New dialogs and carrier SMS submissions are fenced while Asterisk enters
            # graceful maintenance and the exact generation is proved idle.
            initial_invite = _sip_initial_invite(sip_text)
            sip_message = _sip_message_request(sip_text)
            carrier_submission = initial_invite or sip_message
            if (carrier_submission and await _line_admission_blocked(str(iid))):
                await websocket.close(code=4412, reason="line recovery in progress")
                return
            admission = _sip_initial_invite_admission(sip_text)
            if admission is not None:
                current_ingress = media_ingress.status(host_header)
                if media_ingress.binding_id(current_ingress) != media_route_id:
                    await websocket.close(code=4410, reason="browser media route changed")
                    return
                target, token, transaction_id, cseq, branch, has_authorization = admission
                if target == "mdd-media-check":
                    admitted = media_admission.claim_canary(
                        token, str(iid), generation, websocket_id, media_route_id)
                elif re.fullmatch(r"[+0-9]{2,32}", target):
                    admitted = media_admission.authorize_invite(
                        token, str(iid), generation, websocket_id,
                        transaction_id, target, cseq, branch, has_authorization)
                else:
                    admitted = False
                if not admitted:
                    await websocket.close(code=4413, reason="browser media proof required")
                    return
            if carrier_submission:
                send_timed_out = False
                async with _pcscf_admission_boundary(str(iid)) as admitted:
                    if not admitted:
                        await websocket.close(code=4412, reason="line recovery in progress")
                        return
                    send_timed_out = not await _bounded_upstream_submission(upstream_ws, msg)
                if send_timed_out:
                    await websocket.close(code=4415, reason="Engine SIP bridge timed out")
                    await _bounded_upstream_close(upstream_ws)
                    return
            else:
                # BYE/CANCEL and in-dialog re-INVITE never take the new-work fence.
                await upstream_ws.send(msg)
    except Exception:
        pass


def _softphone_upstream_url(runtime: dict) -> str:
    """Exact Engine-internal WSS endpoint; never derived from a browser-controlled host."""
    upstream_host = str((runtime or {}).get("ip") or "")
    try:
        address = ipaddress.ip_address(upstream_host)
    except ValueError as exc:
        raise ValueError("exact Engine bridge address is unavailable") from exc
    if address.is_unspecified or address.is_multicast:
        raise ValueError("exact Engine bridge address is invalid")
    upstream_host = str(address)
    if ":" in upstream_host:
        upstream_host = f"[{upstream_host}]"
    return f"wss://{upstream_host}:8089/ws"


@app.websocket("/api/instances/{iid}/ws")
async def api_softphone_ws(websocket: WebSocket, iid: str):
    """Proxy SIP-over-WebSocket between browser and Asterisk container.

    Enables softphone to work seamlessly over the existing HTTP/HTTPS connection on the same
    origin/port, eliminating browser cross-port self-signed certificate blocks and simplifying
    Nginx reverse-proxy deployment to a single location.
    """
    raw_subproto = websocket.headers.get("sec-websocket-protocol", "")
    subprotocols = [s.strip() for s in raw_subproto.split(",") if s.strip()]
    if "sip" not in subprotocols:
        await websocket.close(code=4406, reason="SIP WebSocket subprotocol required")
        return
    selected_subproto = "sip"

    session_token = websocket.cookies.get(auth.SESSION_COOKIE)
    if not auth.session(session_token):
        await websocket.close(code=4401, reason="authentication required")
        return
    host_header = websocket.headers.get("host", "")
    if not media_ingress.same_origin(websocket.headers.get("origin", ""), host_header):
        await websocket.close(code=4403, reason="same-origin softphone required")
        return
    ingress = media_ingress.status(host_header)
    route = ingress.get("candidate") or {}
    candidate_id = str(route.get("id") or "")
    media_route_id = media_ingress.binding_id(ingress)
    advertised_media_ip = str(route.get("address") or "")
    if (not ingress.get("confirmed") or not candidate_id or not media_route_id
            or not advertised_media_ip):
        await websocket.close(code=4410, reason="browser media route is not confirmed")
        return

    inst = cfg.get_instance(iid)
    if not inst:
        await websocket.close(code=4404)
        return

    runtime = await hub.runtime.get(iid, force=True)
    generation = str(runtime.get("container_id") or "")
    expected_generation = str(websocket.query_params.get("generation") or "")
    configured_port = int((inst.get("ports") or {}).get("webrtc") or 8089)
    if (not runtime.get("running") or not generation or
            expected_generation != generation or
            runtime.get("webrtc_host_port") != configured_port or
            runtime.get("rtp_mapping_exact") is not True or
            await _line_admission_blocked(str(iid))):
        await websocket.close(code=4410, reason="softphone engine is stopped")
        return

    ports = inst.get("ports", {})
    webrtc_port = configured_port
    rtp_start = int(ports.get("rtp_start") or 0)
    rtp_end = rtp_start + cfg.rtp_span(ports) - 1

    import ssl
    import websockets

    ssl_ctx = ssl.create_default_context()
    ssl_ctx.check_hostname = False
    ssl_ctx.verify_mode = ssl.CERT_NONE

    # Connect the exact Engine generation on its Docker bridge address. This works whether
    # Control runs on the Linux host or in the default Docker bridge and avoids treating
    # container-local 127.0.0.1 as the host. Missing/ambiguous runtime identity fails closed.
    try:
        upstream_url = _softphone_upstream_url(runtime)
        upstream_ip = str(ipaddress.ip_address(str(runtime.get("ip") or "")))
    except ValueError:
        await websocket.close(code=4410, reason="softphone engine address is unavailable")
        return
    websocket_id = ""
    try:
        async with websockets.connect(upstream_url, ssl=ssl_ctx, subprotocols=["sip"], max_size=2**22) as upstream_ws:
            confirmed = await hub.runtime.get(iid, force=True)
            confirmed_ingress = media_ingress.status(host_header)
            confirmed_inst = cfg.get_instance(iid)
            confirmed_webrtc = (((confirmed_inst or {}).get("sip") or {}).get("webrtc") or {})
            confirmed_port = int(((confirmed_inst or {}).get("ports") or {}).get(
                "webrtc") or 8089)
            try:
                confirmed_ip = str(ipaddress.ip_address(str(confirmed.get("ip") or "")))
            except ValueError:
                confirmed_ip = ""
            if (not confirmed.get("running") or
                    str(confirmed.get("container_id") or "") != generation or
                    confirmed_ip != upstream_ip or
                    confirmed.get("webrtc_host_port") != configured_port or
                    confirmed.get("rtp_mapping_exact") is not True or
                    confirmed_port != configured_port or
                    not confirmed_webrtc.get("enable", True) or
                    await _line_admission_blocked(str(iid)) or
                    not confirmed_ingress.get("confirmed") or
                    (confirmed_ingress.get("candidate") or {}).get("id") != candidate_id or
                    media_ingress.binding_id(confirmed_ingress) != media_route_id):
                await websocket.close(code=4410, reason="softphone engine changed")
                return
            await websocket.accept(subprotocol=selected_subproto)
            websocket_id = uuid.uuid4().hex
            async def client_to_upstream():
                await _forward_softphone_client(
                    websocket, upstream_ws, iid, generation, websocket_id,
                    media_route_id, host_header)

            async def upstream_to_client():
                try:
                    while True:
                        msg = await upstream_ws.recv()
                        try:
                            response_text = (msg.decode("utf-8", errors="strict")
                                             if isinstance(msg, bytes) else str(msg))
                            response = _sip_invite_response(response_text)
                            if response:
                                transaction_id, cseq, status_code = response
                                media_admission.observe_invite_response(
                                    websocket_id, transaction_id, cseq, status_code)
                        except UnicodeError:
                            raise SipMediaRewriteError("invalid Engine SIP response")
                        rewritten = rewrite_engine_sdp(
                            msg, engine_ip=upstream_ip,
                            advertised_ip=advertised_media_ip,
                            route_id=media_route_id,
                            rtp_start=rtp_start, rtp_end=rtp_end)
                        if isinstance(rewritten, bytes):
                            await websocket.send_bytes(rewritten)
                        else:
                            await websocket.send_text(rewritten)
                except SipMediaRewriteError:
                    try:
                        await websocket.close(code=4414, reason="unsafe Engine SDP")
                    except Exception:
                        pass
                except Exception:
                    pass

            done, pending = await asyncio.wait(
                [asyncio.create_task(client_to_upstream()), asyncio.create_task(upstream_to_client())],
                return_when=asyncio.FIRST_COMPLETED,
            )
            for t in pending:
                t.cancel()
    except Exception as exc:
        log.warning("softphone ws bridge closed for line %s: %s", iid, exc)
    finally:
        if websocket_id:
            authorizations = media_admission.release_websocket(websocket_id)
            _schedule_disconnected_softphone_cleanup(iid, generation, authorizations)
        try:
            await websocket.close()
        except Exception:
            pass


# ----------------------------- engine event hook -----------------------------
def _call_disposition(dialstatus: str, cause: int, direction: str = "out") -> str:
    """Map Asterisk DIALSTATUS + Q.850 hangupcause to a friendly outcome. No retry — a
    rejected/busy/no-answer call is simply recorded as such. Incoming and outgoing read the
    same DIALSTATUS differently: for an inbound call the Dial targets our local softphone, so
    BUSY/decline means WE declined and CANCEL/NOANSWER means we missed it."""
    if dialstatus == "ANSWER":
        return "answered"
    if direction == "in":
        if dialstatus == "BUSY" or cause == 21:
            return "rejected"        # local softphone actively declined
        return "missed"              # remote hung up first, no answer, or rang out
    # outgoing
    if cause == 21:                     # 603 Decline — far end actively rejected
        return "rejected"
    if cause == 17 or dialstatus == "BUSY":
        return "busy"
    if dialstatus == "NOANSWER" or cause == 19:
        return "no answer"
    if dialstatus == "CANCEL":
        return "cancelled"
    if dialstatus in ("CONGESTION", "CHANUNAVAIL"):
        return "failed"
    # empty DIALSTATUS in the hangup handler => caller hung up before/while dialing.
    return (dialstatus.lower() if dialstatus else "cancelled")


_sms_concat_buffers: dict[tuple[str, str], dict] = {}
_media_canary_tasks: set[asyncio.Task] = set()
_softphone_disconnect_tasks: set[asyncio.Task] = set()
_softphone_call_leases: dict[str, dict] = {}


async def _prove_engine_media_canary(iid: str, token: str, generation: str,
                                     source_call_id: str) -> None:
    """Require Asterisk-side RTP counters on the exact Echo channel before admission."""
    uniqueid = str(source_call_id or "").rsplit(":", 1)[-1]
    if not uniqueid or not re.fullmatch(r"[A-Za-z0-9_.-]{1,160}", uniqueid):
        return
    deadline = time.monotonic() + 7.0
    try:
        ami = await hub.ami_for(str(iid))
        while time.monotonic() < deadline:
            runtime = await hub.runtime.get(str(iid), force=True)
            if (not runtime.get("running") or
                    str(runtime.get("container_id") or "") != str(generation)):
                return
            counts = await ami.channel_rtp_counts(uniqueid)
            if (counts and type(counts.get("tx_packets")) is int
                    and type(counts.get("rx_packets")) is int
                    and counts["tx_packets"] > 0 and counts["rx_packets"] > 0):
                media_admission.mark_engine(token, str(iid), str(generation))
                return
            await asyncio.sleep(0.25)
    except Exception:
        return


def _schedule_engine_media_canary(iid: str, token: str, generation: str,
                                  source_call_id: str) -> None:
    task = asyncio.create_task(_prove_engine_media_canary(
        iid, token, generation, source_call_id))
    _media_canary_tasks.add(task)
    task.add_done_callback(_media_canary_tasks.discard)


async def _terminate_disconnected_softphone_calls(iid: str, generation: str,
                                                  authorizations: list[dict]) -> None:
    """After a 10s WSS grace, terminate only channels admitted by that dead session."""
    uniqueids = {str(item.get("source_call_id") or "") for item in authorizations
                 if item.get("generation") == str(generation)}
    uniqueids.discard("")
    if not uniqueids:
        return
    await asyncio.sleep(10.0)
    try:
        ami = await hub.ami_for(str(iid))
        for uniqueid in sorted(uniqueids):
            runtime = await hub.runtime.get(str(iid), force=True)
            if (not runtime.get("running") or
                    str(runtime.get("container_id") or "") != str(generation)):
                return
            await ami.hangup_channel(uniqueid)
    except Exception:
        return


def _schedule_disconnected_softphone_cleanup(iid: str, generation: str,
                                             authorizations: list[dict]) -> None:
    if not authorizations:
        return
    task = asyncio.create_task(_terminate_disconnected_softphone_calls(
        str(iid), str(generation), authorizations))
    _softphone_disconnect_tasks.add(task)
    task.add_done_callback(_softphone_disconnect_tasks.discard)


async def _renew_softphone_call_lease(token: str, iid: str, generation: str,
                                      source_call_id: str) -> None:
    """Renew Asterisk's local 10s absolute timeout while the owning WSS admission exists."""
    missed_renewals = 0
    try:
        ami = await hub.ami_for(str(iid))
        while media_admission.authorization_active(
                token, str(iid), str(generation), str(source_call_id)):
            runtime = await hub.runtime.get(str(iid), force=True)
            if (not runtime.get("running") or
                    str(runtime.get("container_id") or "") != str(generation)):
                media_admission.close_call(token, str(iid), str(source_call_id))
                return
            renewed = await ami.renew_channel_absolute_timeout(str(source_call_id), 10)
            if renewed:
                missed_renewals = 0
            else:
                missed_renewals += 1
                if missed_renewals >= 3:
                    # The exact Asterisk channel disappeared (or can no longer be proven).  Stop
                    # retaining the registry entry/task; the already-installed absolute timeout
                    # remains the independent fail-safe and is never extended on a failed write.
                    media_admission.close_call(token, str(iid), str(source_call_id))
                    return
            await asyncio.sleep(2.0)
    except asyncio.CancelledError:
        raise
    except Exception:
        media_admission.close_call(token, str(iid), str(source_call_id))
        return


def _schedule_softphone_call_lease(token: str, iid: str, generation: str,
                                   source_call_id: str) -> None:
    if token in _softphone_call_leases:
        return
    task = asyncio.create_task(_renew_softphone_call_lease(
        token, str(iid), str(generation), str(source_call_id)))
    record = {"iid": str(iid), "generation": str(generation),
              "source_call_id": str(source_call_id), "task": task}
    _softphone_call_leases[token] = record
    def finished(completed):
        current = _softphone_call_leases.get(token)
        if current and current.get("task") is completed:
            _softphone_call_leases.pop(token, None)
    task.add_done_callback(finished)


async def _shutdown_softphone_call_leases() -> None:
    """Best-effort exact cleanup; Asterisk's absolute timeout remains the crash fallback."""
    records = list(_softphone_call_leases.values())
    if records:
        async def stop_one(record):
            runtime = await hub.runtime.get(record["iid"], force=True)
            if (runtime.get("running") and
                    str(runtime.get("container_id") or "") == record["generation"]):
                ami = await hub.ami_for(record["iid"])
                await ami.hangup_channel(record["source_call_id"])
        try:
            await asyncio.wait_for(asyncio.gather(
                *(stop_one(record) for record in records), return_exceptions=True), timeout=5.0)
        except asyncio.TimeoutError:
            pass
    for record in records:
        record["task"].cancel()
    if records:
        await asyncio.gather(*(record["task"] for record in records),
                             return_exceptions=True)
    _softphone_call_leases.clear()


async def _flush_concatenated_sms(iid: str, sender: str):
    try:
        await asyncio.sleep(1.0)
    except asyncio.CancelledError:
        return
    key = (iid, sender)
    buf = _sms_concat_buffers.pop(key, None)
    if not buf:
        return
    parts = buf.get("parts", [])
    if not parts:
        return
    full_text = "".join(parts)
    rec = store.add_message(iid, "in", sender, full_text)
    await hub.broadcast({"type": "sms", "instance": iid, "message": rec})
    _dispatch_push(notify_push.EV_INCOMING_SMS, iid, sender, full_text)
    try:
        allowance.reconcile(iid)
    except Exception:
        pass


@app.post("/api/engine/event")
async def api_engine_event(payload: dict):
    """Receives notify.py callbacks from engine containers."""
    iid = str(payload.get("instance", ""))
    event = payload.get("event", "")
    args = payload.get("args", [])
    engine_run_id = str(payload.get("engine_run_id") or "")
    if engine_run_id and not re.fullmatch(r"[A-Za-z0-9_.:-]{1,128}", engine_run_id):
        raise HTTPException(422, "invalid Engine run id")
    source_call_id = str(payload.get("source_call_id") or "")
    if source_call_id and not re.fullmatch(r"[A-Za-z0-9_.:-]{1,240}", source_call_id):
        raise HTTPException(422, "invalid engine call correlation id")
    if event == "pcscf_rebind" and len(args) == 1:
        if _durable_maintenance_pending(iid):
            await hub.broadcast({"type": "engine", "instance": iid,
                                 "event": event, "args": args,
                                 "maintenance_deferred": True})
            return {"ok": True, "accepted": False, "reason": "maintenance_deferred"}
        if not engine_run_id:
            return {"ok": True, "accepted": False, "reason": "missing_run_id"}
        result = await _reconcile_pcscf_rebind(
            iid, event_run_id=engine_run_id) or {"status": "already_applied"}
        await hub.broadcast({"type": "engine", "instance": iid,
                             "event": event, "args": args})
        asyncio.create_task(push_status(iid))
        return {"ok": True, "accepted": result.get("status") not in {
            "stale_event", "invalid_marker", "disabled"}, "result": result}
    if event == "media_check" and len(args) == 1:
        runtime = await hub.runtime.get(iid, force=True)
        generation = str(runtime.get("container_id") or "") if runtime.get("running") else ""
        if generation and source_call_id:
            _schedule_engine_media_canary(
                iid, str(args[0]), generation, source_call_id)
            return {"ok": True, "accepted": True, "proof": "pending"}
        return {"ok": True, "accepted": False}
    if event == "sms_in" and len(args) >= 2:
        try:
            text = base64.b64decode(args[1]).decode(errors="replace")
        except Exception:
            text = args[1]
        sender = args[0] or ""
        # Drop inbound MESSAGEs that carry NO human-readable text (empty/whitespace body). Two
        # real sources produce these, and neither is a text the user should see:
        #   1. IMS-internal signalling: the carrier's IP-SM-GW / SMSC sends non-user MESSAGEs
        #      whose From is a bare private-IP SIP URI (e.g. <sip:10.183.150.10>).
        #   2. Binary / SIM-targeted SMS: OTA "SIM data-download" messages (3GPP TS 23.040
        #      TP-DCS 0xF6 = 8-bit, message-class 2) and other non-text PDUs — Asterisk decodes
        #      their user-data to an empty string because there is no displayable text (seen from
        #      short-codes like 20023). These are operator/service payloads for the SIM, not texts.
        # A genuine text always has a non-empty decoded body, so dropping on empty-body never
        # loses a real message. (An empty body with a normal sender is likewise nothing to show.)
        if not sms_content.is_displayable_sms_text(text):
            log.info("dropping non-displayable inbound SMS (internal signalling / binary/OTA "
                     "SIM message)")
            return {"ok": True, "dropped": "non_displayable"}
        key = (iid, sender)
        existing = _sms_concat_buffers.get(key)
        if existing:
            existing["parts"].append(text)
            old_task = existing.get("task")
            if old_task and not old_task.done():
                old_task.cancel()
            existing["task"] = asyncio.create_task(_flush_concatenated_sms(iid, sender))
        else:
            task = asyncio.create_task(_flush_concatenated_sms(iid, sender))
            _sms_concat_buffers[key] = {"parts": [text], "task": task}
        return {"ok": True, "buffered": True}
    elif event == "sms_out" and len(args) >= 2:
        pass  # already stored by the send path
    elif event == "call_in":
        # Log inbound calls even when the caller withholds/omits their number (peer "") so an
        # anonymous call still gets a record that the 'h' disposition can finalize. The IMS
        # delivers one INVITE several times (VoLTE preconditions / GRUU fork / retransmit),
        # firing call_in more than once per call — both while the record is still open AND as a
        # trailing retransmit a few seconds AFTER it was finalized. add_call_deduped coalesces
        # both into the single record so no ghost 'ringing' row is left behind.
        peer = args[0] if args else ""
        rec, _created = store.record_call_start(
            iid, "in", peer, "ringing", source_call_id, engine_run_id=engine_run_id)
        terminal = rec.get("end_ts") is not None
        if not terminal:
            await hub.broadcast({"type": "call", "instance": iid, "call": rec})
        # Push-notify ONCE per real inbound call. IMS re-delivers call_in several times for
        # one call (VoLTE preconditions / GRUU fork / retransmit); add_call_deduped folds
        # them into a single record, so key the notification on that record id. An anonymous
        # first event ('') whose number arrives on a later duplicate would push before the
        # number is known — so only notify once we have the peer, or after ~4s if it stays
        # anonymous (caller genuinely withheld it).
        cid = rec.get("id")
        if not terminal and cid is not None and cid not in hub._pushed_calls:
            if peer or int(time.time()) - int(rec.get("start_ts", 0)) >= 4:
                hub._pushed_calls.add(cid)
                if len(hub._pushed_calls) > 512:      # bound the dedupe set
                    hub._pushed_calls = set(list(hub._pushed_calls)[-256:])
                _dispatch_push(notify_push.EV_INCOMING_CALL, iid, rec.get("peer") or peer)
    elif event == "call_out" and args:
        rec, _created = store.record_call_start(
            iid, "out", args[0], "dialing", source_call_id,
            engine_run_id=engine_run_id)
        token = str(args[1] if len(args) > 1 else "")
        uniqueid = source_call_id.rsplit(":", 1)[-1] if source_call_id else ""
        if (re.fullmatch(r"[A-Za-z0-9_-]{32,128}", token)
                and re.fullmatch(r"[A-Za-z0-9_.-]{1,160}", uniqueid)):
            runtime = await hub.runtime.get(iid, force=True)
            generation = str(runtime.get("container_id") or "") \
                if runtime.get("running") else ""
            if (generation and media_admission.bind_channel(
                    token, iid, generation, uniqueid)):
                _schedule_softphone_call_lease(token, iid, generation, uniqueid)
        if rec.get("end_ts") is None:
            await hub.broadcast({"type": "call", "instance": iid, "call": rec})
    elif event == "call_result" and args:
        # New form: call_result <direction> <peer> <dialstatus> <cause> (fired from the 'h'
        # hangup handler for BOTH directions). Legacy form: call_result <peer> <dialstatus>
        # <cause> (outgoing only) — kept for engines running an older dialplan.
        if args[0] in ("in", "out"):
            direction = args[0]
            to = args[1] if len(args) > 1 else ""
            dialstatus = (args[2] if len(args) > 2 else "").upper()
            cause = int(args[3]) if len(args) > 3 and str(args[3]).isdigit() else 0
            call_token = str(args[4] if direction == "out" and len(args) > 4 else "")
        else:
            direction = "out"
            to = args[0]
            dialstatus = (args[1] if len(args) > 1 else "").upper()
            cause = int(args[2]) if len(args) > 2 and str(args[2]).isdigit() else 0
            call_token = ""
        if (call_token and source_call_id
                and re.fullmatch(r"[A-Za-z0-9_-]{32,128}", call_token)):
            media_admission.close_call(
                call_token, iid, source_call_id.rsplit(":", 1)[-1])
        disp = _call_disposition(dialstatus, cause, direction)
        rec, _created = store.record_call_result(
            iid, direction, to, disp, source_call_id, engine_run_id=engine_run_id)
        if rec:
            await hub.broadcast({"type": "call", "instance": iid, "call": rec})
    elif event == "cp_mode_resolved" and args:
        # CP auto-discovery success: the engine found the address family (v6/v4/dual) that yields a
        # usable PDN on this carrier. Repin the line from 'auto' to the resolved family so it stops
        # re-walking the ladder on future starts (fast, deterministic), and record that it was
        # auto-detected. Only acts on an auto line; a pinned line ignores a stray report.
        resolved = (args[0] or "").strip().lower()
        if _durable_maintenance_pending(iid):
            await hub.broadcast({"type": "engine", "instance": iid,
                                 "event": event, "args": args,
                                 "maintenance_deferred": True})
            return {"ok": True, "accepted": False, "reason": "maintenance_deferred"}
        if resolved in ("v6", "v4", "dual"):
            inst = cfg.get_instance(iid)
            if inst and cfg.normalize_cp_mode(inst.get("cp_mode", "")) == "auto":
                try:
                    cfg.upsert_instance({"id": iid, "cp_mode": resolved, "cp_mode_source": "auto"})
                    log.info("instance %s: CP auto-discovery resolved to %s (repinned)", iid, resolved)
                except Exception as e:  # noqa
                    log.warning("cp_mode_resolved persist failed for %s: %r", iid, e)
            await hub.broadcast({"type": "engine", "instance": iid, "event": event, "args": args})
    else:
        await hub.broadcast({"type": "engine", "instance": iid, "event": event, "args": args})
    # real-time: any tunnel/registration transition triggers an immediate status push
    if event in ("tunnel_up", "tunnel_down", "pcscf", "pcscf_rebind",
                 "registered", "unregistered"):
        asyncio.create_task(push_status(iid))
    return {"ok": True}


async def push_status(iid: str):
    """Compute + broadcast status for a single instance immediately (event-driven)."""
    iid = str(iid)
    status_epoch = hub.status_epoch(iid)
    inst = cfg.get_instance(iid)
    if not inst:
        return
    try:
        if engine.engine_start_quarantine_pending(iid):
            st = _engine_start_quarantine_status(iid)
            async with hub.status_publish_lock(iid):
                if not hub.status_epoch_current(iid, status_epoch):
                    return
                hub.status_cache[iid] = st
                hub.status_sampled_at[iid] = time.monotonic()
                await hub.broadcast({"type": "status", "instance": iid, **st})
            return
        if _durable_maintenance_pending(iid):
            st = _durable_maintenance_status(iid)
            async with hub.status_publish_lock(iid):
                if not hub.status_epoch_current(iid, status_epoch):
                    return
                hub.status_cache[iid] = st
                hub.status_sampled_at[iid] = time.monotonic()
                await hub.broadcast({"type": "status", "instance": iid, **st})
            return
        runtime = await hub.runtime.get(iid)
        await _reconcile_pcscf_rebind(iid)
        ami = await hub.ami_for(iid, runtime)
        st = await status_mod.compute(inst, ami, runtime)
        st = _with_status_activity(
            iid, _with_pcscf_rebind_observation(
                iid, await _apply_health_with_recovery(
                    iid, inst, st, runtime.get("container_id"))))
        async with hub.status_publish_lock(iid):
            if not hub.status_epoch_current(iid, status_epoch):
                return
            hub.status_cache[iid] = st
            hub.status_sampled_at[iid] = time.monotonic()
            await hub.broadcast({"type": "status", "instance": iid, **st})
    except Exception as e:  # noqa
        log.debug("push_status error: %r", e)


def _dispatch_push(event: str, iid: str, source: str, text: str | None = None):
    """Fire outbound push notifications for an incoming event, off the
    event path so a slow endpoint can't stall engine-event handling. No-op unless a channel
    is enabled for this event."""
    inst = cfg.get_instance(iid)
    if not inst:
        return
    settings = cfg.get_settings()
    wh = settings.get("webhook") or {}
    tg = settings.get("telegram") or {}
    pp = settings.get("pushplus") or {}
    if not (wh.get("enabled") or tg.get("enabled") or pp.get("enabled")):
        return
    asyncio.create_task(
        asyncio.to_thread(notify_push.dispatch, settings, event, inst, source, text))




# ----------------------------- eSIM / LPA (lpac) -----------------------------
@app.get("/api/esim/status")
async def api_esim_status():
    """Whether lpac is installed and basic settings."""
    settings = cfg.get_settings().get("esim") or {}
    bin_path = lpa.lpac_bin()
    return {
        "available": lpa.lpac_available(),
        "lpac_bin": bin_path,
        "download_timeout": int(settings.get("download_timeout") or 300),
        "auto_process_notifications": bool(settings.get("auto_process_notifications", True)),
        "busy_readers": list(hub.lpa_busy.keys()),
    }


# ---------------------------------------------------------------- eSIM chip cache
# Last successful chip read per eUICC (keyed by EID), persisted in the data dir so every
# browser/session can show the profile list — and switch profiles — without stopping a
# running line for a fresh exclusive read. Entries are matched to the inserted card via the
# ICCIDs of their profiles (the card monitor reads the active ICCID without exclusivity).
_ESIM_CACHE_PATH = os.path.join(cfg.DATA_DIR, "esim-chip-cache.json")
_ESIM_CACHE_TTL = 300  # seconds (5 minutes); avoids lingering phantom cards after reader removal


def _esim_cache_load() -> dict:
    doc = _read_json_file(_ESIM_CACHE_PATH)
    return doc if isinstance(doc, dict) else {}


def _esim_cache_write(data: dict):
    tmp = _ESIM_CACHE_PATH + ".tmp"
    with open(tmp, "w", encoding="utf-8") as handle:
        json.dump(data, handle, ensure_ascii=False)
    os.chmod(tmp, 0o600)
    os.replace(tmp, _ESIM_CACHE_PATH)


def _esim_cache_store(ses: list, imei: str, card_info: dict | None = None):
    eid = next((str(se.get("eid")) for se in ses if se.get("eid")), "")
    # Only a fully successful read may overwrite the cache — a partial/failed load would
    # replace a good profile list with an empty one.
    if not eid or any(se.get("error") for se in ses):
        return
    data = _esim_cache_load()
    card_info = card_info or {}
    data[eid] = {"ses": ses, "imei": imei or "", "ts": int(time.time()),
                 "endpoint_key": str(card_info.get("endpoint_key") or "")}
    _esim_cache_write(data)


def _esim_cache_for_iccid(iccid: str) -> dict | None:
    if not iccid:
        return None
    for entry in _esim_cache_load().values():
        for se in entry.get("ses") or []:
            if any(p.get("iccid") == iccid for p in (se.get("profiles") or [])):
                return entry
    return None


def _esim_cache_for_card(card_info: dict) -> dict | None:
    """Match cached eUICC data even when the chip has no profiles/active ICCID."""
    data = _esim_cache_load()
    eid = str(card_info.get("eid") or "").strip()
    if eid and isinstance(data.get(eid), dict):
        return data[eid]
    endpoint_key = str(card_info.get("endpoint_key") or "").strip()
    if endpoint_key:
        entry = next((item for item in data.values()
                      if isinstance(item, dict) and item.get("endpoint_key") == endpoint_key), None)
        if entry:
            return entry
    return _esim_cache_for_iccid(str(card_info.get("iccid") or ""))


def _esim_cache_update_profile(iccid: str, *, state: str | None = None,
                               nickname: str | None = None, remove: bool = False):
    """Mirror a successful enable/disable/delete/nickname onto the cached view."""
    data = _esim_cache_load()
    changed = False
    for entry in data.values():
        for se in entry.get("ses") or []:
            profiles = se.get("profiles") or []
            hit = next((p for p in profiles if p.get("iccid") == iccid), None)
            if hit is None:
                continue
            if remove:
                se["profiles"] = [p for p in profiles if p.get("iccid") != iccid]
            else:
                if state == "enabled":
                    for p in profiles:
                        if str(p.get("profileState") or "").lower() == "enabled":
                            p["profileState"] = "disabled"
                if state is not None:
                    hit["profileState"] = state
                if nickname is not None:
                    hit["profileNickname"] = nickname
            changed = True
    if changed:
        _esim_cache_write(data)


@app.get("/api/esim/chip/cached")
async def api_esim_chip_cached(reader_index: int = 0, reader: str | None = None):
    """Cached chip view for the card in this reader — never touches the card, so it is safe
    while a VoWiFi line holds the reader."""
    name, idx = await asyncio.to_thread(_esim_resolve_reader, reader_index, reader)
    card_info = next((item for item in hub.cards_list() if item.get("name") == name), {})
    if not card_info.get("present"):
        return {"ok": True, "cached": False, "reader": name, "reader_index": idx}
    entry = await asyncio.to_thread(_esim_cache_for_card, card_info)
    if not entry:
        return {"ok": True, "cached": False, "reader": name, "reader_index": idx}
    ts = entry.get("ts") or 0
    if ts and (time.time() - ts) > _ESIM_CACHE_TTL:
        return {"ok": True, "cached": False, "reader": name, "reader_index": idx}
    return {"ok": True, "cached": True, "reader": name, "reader_index": idx,
            "ses": entry.get("ses") or [], "imei": entry.get("imei") or "",
            "ts": ts}


@app.get("/api/esim/chip")
async def api_esim_chip(reader_index: int = 0, reader: str | None = None):
    """Load chip info for every SE on the card (dual SE → two entries)."""
    name, idx = await asyncio.to_thread(_esim_resolve_reader, reader_index, reader)
    running = await asyncio.to_thread(_find_running_by_reader, name)
    payload = await _esim_run(name, idx, lpa.load_all_ses(name, idx))
    ses = payload.get("ses") or []
    card_info = next((item for item in hub.cards_list() if item.get("name") == name), {})
    await asyncio.to_thread(_esim_cache_store, ses, _esim_imei_for_reader(name), card_info)
    eid = next((str(se.get("eid")) for se in ses if se.get("eid")), "")
    if eid:
        await asyncio.to_thread(vpcd_registry.observe_card, name, card_info, eid=eid)
    # Backward-compatible single-chip view = first SE that loaded successfully.
    primary = next((s for s in ses if s.get("chip")), ses[0] if ses else None)
    return {
        "ok": True,
        "reader": name,
        "reader_index": idx,
        "dual": bool(payload.get("dual")),
        "ses": ses,
        "chip": (primary or {}).get("chip"),
        "imei": _esim_imei_for_reader(name),
        "line_running": bool(running),
        "matched_instance": running["id"] if running else (hub.cards.get(name) or {}).get("matched"),
    }


@app.get("/api/esim/profiles")
async def api_esim_profiles(reader_index: int = 0, reader: str | None = None):
    """List profiles grouped per SE (same load as chip — prefer /api/esim/chip for full view)."""
    name, idx = await asyncio.to_thread(_esim_resolve_reader, reader_index, reader)
    running = await asyncio.to_thread(_find_running_by_reader, name)
    payload = await _esim_run(name, idx, lpa.load_all_ses(name, idx))
    ses = payload.get("ses") or []
    flat = []
    for se in ses:
        flat.extend(se.get("profiles") or [])
    return {
        "ok": True,
        "reader": name,
        "reader_index": idx,
        "dual": bool(payload.get("dual")),
        "ses": ses,
        "profiles": flat,
        "imei": _esim_imei_for_reader(name),
        "line_running": bool(running),
        "matched_instance": running["id"] if running else (hub.cards.get(name) or {}).get("matched"),
        "lpa_busy": bool(hub.lpa_busy.get(name)),
    }


@app.post("/api/esim/profiles/{iccid}/enable")
async def api_esim_enable(iccid: str, body: dict | None = None):
    body = body or {}
    name, idx = await asyncio.to_thread(
        _esim_resolve_reader, body.get("reader_index", 0), body.get("reader"))
    se = await asyncio.to_thread(
        _esim_resolve_se, name, idx, body.get("se_id") or body.get("seId"), body.get("aid"),
        require=True)
    await _esim_run(
        name, idx, lpa.profile_enable(name, iccid, aid=se.get("aid")), refresh=True)
    await asyncio.to_thread(_esim_cache_update_profile, iccid, state="enabled")
    return {"ok": True, "iccid": iccid, "se_id": se["id"], "card": hub.cards.get(name)}


@app.post("/api/esim/profiles/{iccid}/disable")
async def api_esim_disable(iccid: str, body: dict | None = None):
    body = body or {}
    name, idx = await asyncio.to_thread(
        _esim_resolve_reader, body.get("reader_index", 0), body.get("reader"))
    se = await asyncio.to_thread(
        _esim_resolve_se, name, idx, body.get("se_id") or body.get("seId"), body.get("aid"),
        require=True)
    await _esim_run(
        name, idx, lpa.profile_disable(name, iccid, aid=se.get("aid")), refresh=True)
    await asyncio.to_thread(_esim_cache_update_profile, iccid, state="disabled")
    return {"ok": True, "iccid": iccid, "se_id": se["id"], "card": hub.cards.get(name)}


@app.delete("/api/esim/profiles/{iccid}")
async def api_esim_delete(
    iccid: str, reader_index: int = 0, reader: str | None = None,
    se_id: str | None = None, aid: str | None = None,
):
    del reader_index, reader, se_id, aid
    raise HTTPException(
        status_code=403,
        detail={
            "code": "physical_delete_prohibited",
            "message": f"Physical deletion of eSIM profile {iccid} on the card is permanently disabled by policy. Please use soft-delete or disable instead.",
        },
    )



@app.post("/api/esim/profiles/{iccid}/nickname")
async def api_esim_nickname(iccid: str, body: dict):
    name, idx = await asyncio.to_thread(
        _esim_resolve_reader, body.get("reader_index", 0), body.get("reader"))
    se = await asyncio.to_thread(
        _esim_resolve_se, name, idx, body.get("se_id") or body.get("seId"), body.get("aid"),
        require=True)
    nick = body.get("nickname", "")
    await _esim_run(
        name, idx, lpa.profile_nickname(name, iccid, nick, aid=se.get("aid")))
    await asyncio.to_thread(_esim_cache_update_profile, iccid, nickname=nick)
    return {"ok": True, "iccid": iccid, "nickname": nick, "se_id": se["id"]}


@app.post("/api/esim/download")
async def api_esim_download(body: dict):
    """Start a profile download as a background task; progress via WS type=esim_download."""
    name, idx = await asyncio.to_thread(
        _esim_resolve_reader, body.get("reader_index", 0), body.get("reader"))
    se = await asyncio.to_thread(
        _esim_resolve_se, name, idx, body.get("se_id") or body.get("seId"), body.get("aid"),
        require=True)
    if hub.lpa_busy.get(name):
        raise HTTPException(409, "an eSIM operation is already running on this reader")
    await asyncio.to_thread(_esim_guard_engine, name)
    imei = _esim_imei_for_reader(name, body.get("imei"))
    # Claim busy before returning so a second concurrent POST cannot start another job.
    hub.lpa_busy[name] = True
    se_id = se["id"]
    aid = se.get("aid")

    async def _job():
        try:
            async with hub.reader_lock(name):
                try:
                    await hub.broadcast({
                        "type": "esim_download", "reader": name, "reader_index": idx,
                        "se_id": se_id, "event": "started", "step": "started", "imei": imei,
                    })

                    async def on_progress(event):
                        # lpa.run_lpac passes {"step", "data", "code"}
                        step = (event or {}).get("step") or ""
                        data = (event or {}).get("data")
                        msg = {
                            "type": "esim_download", "reader": name, "reader_index": idx,
                            "se_id": se_id, "event": "progress", "step": step,
                        }
                        if isinstance(data, dict):
                            msg["metadata"] = data
                            msg["data"] = data
                        elif data is not None:
                            msg["data"] = data
                        if step == "es8p_metadata_parse" and isinstance(data, dict):
                            msg["event"] = "preview"
                        await hub.broadcast(msg)

                    result = await lpa.download(
                        name,
                        activation_code=body.get("activation_code"),
                        smdp=body.get("smdp"),
                        matching_id=body.get("matching_id"),
                        confirmation_code=body.get("confirmation_code"),
                        imei=imei or None,
                        aid=aid,
                        on_progress=on_progress,
                    )
                    await _esim_refresh_card(name, idx)
                    await hub.broadcast({
                        "type": "esim_download", "reader": name, "reader_index": idx,
                        "se_id": se_id, "event": "completed", "step": "completed",
                        "result": result, "card": hub.cards.get(name),
                    })
                except lpa.LpaError as e:
                    # lpac puts the failing function name in message (e.g. es9p_authenticate_client).
                    err = {
                        "type": "esim_download", "reader": name, "reader_index": idx,
                        "se_id": se_id, "event": "error",
                        "step": (e.message or "").strip() or None,
                        "error": e.user_message(),
                    }
                    await hub.broadcast(err)
                except Exception as e:  # noqa
                    log.exception("esim download failed")
                    await hub.broadcast({
                        "type": "esim_download", "reader": name, "reader_index": idx,
                        "se_id": se_id, "event": "error", "error": str(e),
                    })
        finally:
            hub.lpa_busy.pop(name, None)

    asyncio.create_task(_job())
    return {
        "ok": True, "started": True, "reader": name, "reader_index": idx,
        "se_id": se_id, "imei": imei,
    }


@app.post("/api/esim/download/cancel")
async def api_esim_download_cancel(body: dict | None = None):
    body = body or {}
    name, _idx = await asyncio.to_thread(
        _esim_resolve_reader, body.get("reader_index", 0), body.get("reader"))
    cancelled = lpa.cancel_download(name)
    if cancelled:
        await hub.broadcast({
            "type": "esim_download", "reader": name,
            "event": "cancelling", "step": "cancelling",
        })
    return {"ok": True, "cancelled": cancelled}


@app.post("/api/esim/discovery")
async def api_esim_discovery(body: dict | None = None):
    body = body or {}
    name, idx = await asyncio.to_thread(
        _esim_resolve_reader, body.get("reader_index", 0), body.get("reader"))
    se = await asyncio.to_thread(
        _esim_resolve_se, name, idx, body.get("se_id") or body.get("seId"), body.get("aid"),
        require=True)
    imei = _esim_imei_for_reader(name, body.get("imei"))
    entries = await _esim_run(
        name, idx,
        lpa.discovery(name, imei=imei or None, smds=body.get("smds"), aid=se.get("aid")))
    return {
        "ok": True, "reader": name, "se_id": se["id"],
        "entries": entries or [], "imei": imei,
    }


@app.get("/api/esim/notifications")
async def api_esim_notifications(reader_index: int = 0, reader: str | None = None):
    name, idx = await asyncio.to_thread(_esim_resolve_reader, reader_index, reader)
    payload = await _esim_run(name, idx, lpa.load_all_ses(name, idx))
    ses = payload.get("ses") or []
    flat = []
    for se in ses:
        notes = se.get("notifications") or []
        flat.extend(notes)
        if notes:
            await asyncio.to_thread(store.save_esim_notifications, notes, reader_name=name, aid=str(se.get("aid") or ""))
    return {
        "ok": True, "reader": name, "dual": bool(payload.get("dual")),
        "ses": ses, "notifications": flat,
    }


@app.post("/api/esim/notifications/replay")
async def api_esim_notifications_replay(body: dict):
    """Explicitly replay a deletion or management notification to SM-DP+/SM-DS.

    Requires explicit 'confirmed: True' because replaying a deletion notification
    tells the carrier the eSIM profile was deleted, which may permanently deactivate it.
    """
    if not body.get("confirmed"):
        raise HTTPException(
            status_code=400,
            detail={
                "code": "confirmation_required",
                "message": "Explicit user confirmation ('confirmed': true) is required to replay this deletion notification.",
            },
        )
    seq = body.get("seq")
    if seq is None:
        raise HTTPException(400, "seq required")
    name, idx = await asyncio.to_thread(
        _esim_resolve_reader, body.get("reader_index", 0), body.get("reader"))
    se = await asyncio.to_thread(
        _esim_resolve_se, name, idx, body.get("se_id") or body.get("seId"), body.get("aid"),
        require=False)
    aid = se.get("aid") if se else body.get("aid")
    autoremove = bool(body.get("autoremove", False))
    
    try:
        res = await _esim_run(
            name, idx,
            lpa.notification_replay(name, int(seq), autoremove=autoremove, aid=aid)
        )
        await asyncio.to_thread(
            store.record_notification_replay, int(seq), iccid=str(body.get("iccid") or ""), success=True
        )
        return {"ok": True, "replayed": True, "seq": seq, "data": res}
    except Exception as exc:
        await asyncio.to_thread(
            store.record_notification_replay, int(seq), iccid=str(body.get("iccid") or ""), success=False, error=str(exc)
        )
        raise


@app.post("/api/esim/notifications/process")
async def api_esim_notifications_process(body: dict | None = None):
    body = body or {}
    name, idx = await asyncio.to_thread(
        _esim_resolve_reader, body.get("reader_index", 0), body.get("reader"))
    se = await asyncio.to_thread(
        _esim_resolve_se, name, idx, body.get("se_id") or body.get("seId"), body.get("aid"),
        require=True)
    seq = body.get("seq")
    remove = bool(body.get("remove", True))
    if seq is None:
        coro = lpa.notification_process(
            name, all_notifications=True, autoremove=remove, aid=se.get("aid"))
    else:
        coro = lpa.notification_process(
            name, int(seq), autoremove=remove, aid=se.get("aid"))
    await _esim_run(name, idx, coro)
    return {"ok": True, "se_id": se["id"]}


@app.delete("/api/esim/notifications/{seq}")
async def api_esim_notification_remove(
    seq: int, reader_index: int = 0, reader: str | None = None,
    se_id: str | None = None, aid: str | None = None,
):
    name, idx = await asyncio.to_thread(_esim_resolve_reader, reader_index, reader)
    se = await asyncio.to_thread(_esim_resolve_se, name, idx, se_id, aid, require=True)
    await _esim_run(name, idx, lpa.notification_remove(name, seq, aid=se.get("aid")))
    return {"ok": True, "seq": seq, "se_id": se["id"]}


@app.get("/api/system/external-deps")
async def api_system_external_deps():
    """Inspect external runtime tools (lpac, asterisk, etc.) and return friendly guidance if missing."""
    lpac_path = lpa.lpac_bin()
    lpac_ok = lpa.lpac_available()
    return {
        "ok": True,
        "dependencies": {
            "lpac": {
                "name": "lpac (eSIM LPA tool)",
                "available": lpac_ok,
                "path": lpac_path,
                "guide": "If missing, download lpac from https://github.com/estkme-group/lpac/releases and place it in the program's data/lpac/ directory or install via package manager.",
                "required_for": ["eSIM profile discovery", "eSIM download", "eSIM enable/disable", "notification replay"],
            },
            "pcsc": {
                "name": "PC/SC Smartcard Daemon",
                "available": len(hub.cards_list()) >= 0, # pcscd service is active if query doesn't crash
                "readers_count": len(hub.cards_list()),
                "guide": "On Linux: sudo apt install pcscd. On Windows/macOS: PC/SC is built-in.",
            },
        },
    }



# ----------------------------- Remote modem control -----------------------------
@app.get("/api/cellular-sims")
async def api_cellular_sims():
    instances = cfg.list_instances()
    rows = []
    for item in modem_registry.list():
        inst = next((value for value in instances
                     if str(value.get("iccid") or "") == item.get("iccid")), None)
        rows.append({**item, "instance_id": str((inst or {}).get("id") or ""),
                     "line_name": (inst or {}).get("name") or "",
                     "phone": item.get("phone") or (inst or {}).get("phone") or ""})
    return {"sims": rows}


def _remote_modem_data_policy_blocked(status: dict, wanted: dict) -> bool:
    """Return whether persisted policy intentionally keeps cellular data offline.

    A modem can remain registered on a roaming network while packet data is forbidden.  That
    is a stable policy outcome, not a transient bearer failure: retrying ``cellular.ensure``
    cannot converge until either registration or persisted intent changes.
    """
    return (bool(wanted.get("cellular_enabled"))
            and not bool(wanted.get("roaming_enabled"))
            and str(status.get("registration") or "").casefold() == "roaming")


async def _reconcile_remote_modem_desired(attachment) -> bool:
    """Reapply persisted intent after an Agent restart without changing SIM identity/config."""
    device_id = _remote_modem_device_id(attachment.iccid)
    desired_doc = device_state.desired()
    wanted = ((desired_doc.get("devices") or {}).get(device_id) or
              desired_doc.get("defaults") or {})
    flight = bool(wanted.get("flight_mode"))
    cellular = bool(wanted.get("cellular_enabled"))
    roaming = bool(wanted.get("roaming_enabled"))
    status = getattr(attachment, "status", None) or {}

    async def apply(method: str, params: dict | None = None, timeout: float = 20) -> dict:
        result = await modem_registry.rpc(
            attachment.iccid, method, params or {}, timeout=timeout)
        if result.get("ok") is False:
            raise RuntimeError(str(result.get("error") or f"{method} was not applied"))
        return result

    try:
        if flight:
            if bool((status.get("proxy") or {}).get("ready")) or status.get("data_active"):
                await apply("cellular.disable")
            if status.get("radio_enabled") is not False:
                await apply("radio.set", {"enabled": False})
            return True
        # Missing CFUN is not proof that the radio is off.  A reconnect/status race must not
        # become an unsolicited AT+CFUN=1 write; explicit user toggles use the capability API.
        if status.get("radio_enabled") is False:
            await apply("radio.set", {"enabled": True})
        if status.get("roaming_allowed") is not roaming:
            await apply("cellular.roaming.set", {"enabled": roaming})
        if _remote_modem_data_policy_blocked(status, wanted):
            # Fail closed if an earlier session is still carrying traffic.  Once data is
            # offline this is converged; a setting, registration, or attachment transition
            # will make the regular reconciler evaluate it again.
            if bool((status.get("proxy") or {}).get("ready")) or status.get("data_active"):
                await apply("cellular.disable")
            return True
        if cellular:
            await apply("cellular.ensure", {"allow_roaming": roaming}, timeout=75)
        else:
            await apply("cellular.disable")
        return True
    except Exception as exc:  # The attachment/status API carries the actionable error.
        log.info("remote modem desired-state reconciliation failed for ICCID ending %s: %s",
                 attachment.iccid[-4:], exc)
        return False


async def _reconcile_remote_modem_desired_with_retry(attachment) -> None:
    """Converge one attachment after transient startup/reconnect races.

    This retries only idempotent radio/data desired state.  Paid SMS and call operations are
    deliberately outside this reconciler and are never replayed.
    """
    for delay in (0, 2, 5, 15, 30):
        if delay:
            await asyncio.sleep(delay)
        if modem_registry.resolve(attachment.iccid) is not attachment:
            return
        if await _reconcile_remote_modem_desired(attachment):
            return


def _remote_modem_needs_reconcile(attachment) -> bool:
    """Compare persisted intent with the live, fail-closed Agent snapshot."""
    desired_doc = device_state.desired()
    wanted = ((desired_doc.get("devices") or {}).get(
        _remote_modem_device_id(attachment.iccid)) or desired_doc.get("defaults") or {})
    status = attachment.status or {}
    proxy_ready = bool((status.get("proxy") or {}).get("ready"))
    reverse_ready = bool(attachment.reverse_server and attachment.reverse_port)
    data_active = bool(status.get("data_active") or status.get("data") == "connected")
    if wanted.get("flight_mode"):
        return status.get("radio_enabled") is not False or proxy_ready or data_active
    if status.get("radio_enabled") is False:
        return True
    roaming = status.get("roaming_allowed")
    if isinstance(roaming, bool) and roaming != bool(wanted.get("roaming_enabled")):
        return True
    if _remote_modem_data_policy_blocked(status, wanted):
        # A forbidden roaming bearer is intentionally offline.  Reconcile only if stale data
        # remains active; registration/intent changes naturally leave this branch.
        return proxy_ready or data_active
    return ((not proxy_ready or not reverse_ready or not data_active)
            if wanted.get("cellular_enabled")
            else (proxy_ready or data_active))


async def remote_modem_reconciler() -> None:
    """Restore idempotent radio/data intent after a later bearer or proxy failure.

    The attach-time retry covers startup races only.  Mobile broadband can drop minutes later;
    the Agent correctly fails closed at that point, but without this convergence loop an ON
    request remains permanently degraded until a person toggles it.  Retries are bounded per
    ICCID and never include paid SMS or call operations.
    """
    retry_at: dict[str, float] = {}
    failures: dict[str, int] = {}
    while True:
        now = time.monotonic()
        online = set()
        for row in modem_registry.list():
            if not row.get("online"):
                continue
            iccid = str(row.get("iccid") or "")
            attachment = modem_registry.resolve(iccid)
            if not attachment:
                continue
            online.add(iccid)
            if not _remote_modem_needs_reconcile(attachment):
                retry_at.pop(iccid, None)
                failures.pop(iccid, None)
                continue
            if now < retry_at.get(iccid, 0):
                continue
            ok = await _reconcile_remote_modem_desired(attachment)
            if ok:
                failures[iccid] = 0
                retry_at[iccid] = time.monotonic() + 15
            else:
                count = failures.get(iccid, 0) + 1
                failures[iccid] = count
                retry_at[iccid] = time.monotonic() + min(60, 5 * (2 ** min(count - 1, 4)))
        for iccid in set(retry_at) - online:
            retry_at.pop(iccid, None)
            failures.pop(iccid, None)
        await asyncio.sleep(5)


@app.websocket("/api/agent/modem/tunnel")
async def api_agent_modem_tunnel(websocket: WebSocket, token: str = None,
                                 session_id: str = "", tunnel_id: str = ""):
    req_token = token or websocket.query_params.get("token")
    if not req_token:
        header = websocket.headers.get("authorization", "")
        req_token = header[7:].strip() if header.lower().startswith("bearer ") else ""
    if not auth.verify_agent_token(req_token):
        await websocket.close(code=4003, reason="Unauthorized: Invalid agent token")
        return
    try:
        await modem_registry.accept_tunnel(session_id, tunnel_id, websocket)
    except ModemUnavailable as exc:
        await websocket.close(code=4404, reason=str(exc))


@app.websocket("/api/agent/health/ws")
async def api_agent_health_ws(websocket: WebSocket, token: str = None):
    """Receive one host-level health stream; it never controls modem or reader state."""
    req_token = token or websocket.query_params.get("token")
    if not req_token:
        header = websocket.headers.get("authorization", "")
        req_token = header[7:].strip() if header.lower().startswith("bearer ") else ""
    if not auth.verify_agent_token(req_token):
        await websocket.close(code=4003, reason="Unauthorized: Invalid agent token")
        return
    await websocket.accept()
    attachment = None
    try:
        raw = await asyncio.wait_for(websocket.receive_text(), 10)
        if len(raw.encode("utf-8")) > 65536:
            await websocket.close(code=4400, reason="message is too large")
            return
        hello = json.loads(raw)
        if (type(hello.get("version")) is not int or hello.get("version") != 1 or
                hello.get("type") != "agent.health.hello"):
            await websocket.close(code=4400, reason="Agent health protocol v1 hello required")
            return
        attachment = await agent_health_registry.attach(hello, websocket)
        receipt_requested = websocket.query_params.get("receipt") == "1"
        ack = {
            "version": 1, "type": "agent.health.ack",
            "session_id": attachment.session_id,
        }
        if receipt_requested:
            ack["receipt"] = "required-v1"
        await websocket.send_json(ack)
        if attachment.announce_attach:
            await hub.broadcast({
                "type": "agent-health", "agent_id": attachment.agent_id,
                "connection": "fresh", "online": True,
            })
        await _reconcile_all_remote_card_evidence()
        while True:
            raw = await asyncio.wait_for(websocket.receive_text(), timeout=45.0)
            if len(raw.encode("utf-8")) > 65536:
                await websocket.close(code=4400, reason="message is too large")
                return
            message = json.loads(raw)
            accepted, changed = await agent_health_registry.receive_result(attachment, message)
            if not accepted:
                await websocket.close(code=4409, reason="stale Agent health session")
                return
            if message.get("type") == "agent.health.shutdown":
                if await agent_health_registry.shutdown(attachment):
                    await hub.broadcast({
                        "type": "agent-health", "agent_id": attachment.agent_id,
                        "connection": "stopped", "online": False,
                    })
                    await _reconcile_all_remote_card_evidence()
                attachment = None
                return
            # The custom Agent transport responds to WebSocket Ping frames while receiving.
            # A receipt after every fixed 10-second Agent frame gives it one bounded receive
            # point, so Uvicorn keepalive cannot close an otherwise healthy one-way stream.
            if receipt_requested:
                await websocket.send_json({
                    "version": 1,
                    "type": "agent.health.received",
                    "session_id": attachment.session_id,
                    "seq": message.get("seq"),
                    "revision": message.get("revision"),
                })
            if changed:
                await hub.broadcast({
                    "type": "agent-health", "agent_id": attachment.agent_id,
                    "connection": attachment.connection_state(), "online": True,
                })
                await _reconcile_all_remote_card_evidence()
    except (WebSocketDisconnect, asyncio.CancelledError, asyncio.TimeoutError):
        pass
    except Exception as exc:  # noqa
        log.info("Agent health connection ended: %s", exc)
        try:
            await websocket.close(code=4400, reason="invalid Agent health message")
        except Exception:
            pass
    finally:
        if attachment:
            # Abnormal transport loss gets a freshness grace period.  The 2-second sweeper
            # emits delayed/offline only if the Agent does not reconnect in time.
            await agent_health_registry.transport_closed(attachment)
            await _reconcile_all_remote_card_evidence()


def _remote_modem_event(attachment, online: bool) -> dict:
    mapped_instance = next((str(item.get("id")) for item in cfg.list_instances()
                            if str(item.get("iccid") or "").strip() == attachment.iccid), "")
    return {
        "type": "remote-modem", "iccid": attachment.iccid,
        "instance": mapped_instance, "online": bool(online),
    }


@app.websocket("/api/agent/modem/ws")
async def api_agent_modem_ws(websocket: WebSocket, token: str = None):
    req_token = token or websocket.query_params.get("token")
    if not req_token:
        header = websocket.headers.get("authorization", "")
        req_token = header[7:].strip() if header.lower().startswith("bearer ") else ""
    if not auth.verify_agent_token(req_token):
        log.warning("remote modem control rejected token from %s (present=%s, length=%d)",
                    websocket.client, bool(req_token), len(str(req_token or "")))
        await websocket.close(code=4003, reason="Unauthorized: Invalid agent token")
        return
    await websocket.accept()
    attachment = None
    try:
        raw = await asyncio.wait_for(websocket.receive_text(), 10)
        if len(raw.encode("utf-8")) > 65536:
            await websocket.close(code=4400, reason="message is too large")
            return
        hello = json.loads(raw)
        if hello.get("version") != 1 or hello.get("type") != "hello":
            await websocket.close(code=4400, reason="version 1 hello required")
            return
        try:
            attachment = await modem_registry.attach(hello, websocket)
        except ModemConflict as exc:
            await websocket.close(code=4409, reason=str(exc))
            return
        await websocket.send_json({"version": 1, "type": "hello.ack",
                                   "session_id": attachment.session_id})
        asyncio.create_task(_reconcile_remote_modem_desired_with_retry(attachment))
        await hub.broadcast(_remote_modem_event(attachment, True))
        while True:
            raw = await asyncio.wait_for(websocket.receive_text(), timeout=45.0)
            if len(raw.encode("utf-8")) > 65536:
                await websocket.close(code=4400, reason="message is too large")
                return
            message = json.loads(raw)
            if message.get("session_id") not in (None, "", attachment.session_id):
                continue
            if message.get("modem_id") not in (None, "", attachment.modem_id):
                continue
            if message.get("type") == "ping":
                await websocket.send_json({"version": 1, "type": "pong",
                                           "session_id": attachment.session_id})
            else:
                changed = await modem_registry.receive(attachment, message)
                if changed:
                    await hub.broadcast(_remote_modem_event(attachment, True))
    except (WebSocketDisconnect, asyncio.CancelledError, asyncio.TimeoutError):
        pass
    except Exception as exc:  # noqa
        log.info("remote modem control ended: %s", exc)
    finally:
        if attachment:
            await modem_registry.detach(attachment)
            await hub.broadcast(_remote_modem_event(attachment, False))


@app.websocket("/api/agent/modem/media")
async def api_agent_modem_media(websocket: WebSocket, call_id: str = ""):
    """Attach one short-lived Agent PCM stream; the token is valid for this call only."""
    session = call_media.manager.get(call_id)
    header = websocket.headers.get("authorization", "")
    token = header[7:].strip() if header.lower().startswith("bearer ") else ""
    if not session or not token or not hmac.compare_digest(session.token, token):
        await websocket.close(code=4403, reason="invalid or expired media session")
        return
    await websocket.accept()
    try:
        await session.attach_agent(websocket, token)
    except call_media.MediaUnavailable as exc:
        try:
            await websocket.close(code=4409, reason=str(exc))
        except Exception:
            pass


# ----------------------------- VPCD Secure WSS Bridge -----------------------------
def _vpcd_frame(payload: bytes) -> bytes:
    if len(payload) > 0xFFFF:
        raise ValueError("VPCD frame exceeds 65535 bytes")
    return struct.pack(">H", len(payload)) + payload


async def _vpcd_read_frame(reader: asyncio.StreamReader) -> bytes:
    header = await reader.readexactly(2)
    length = struct.unpack(">H", header)[0]
    return await reader.readexactly(length) if length else b""


VPCD_CONNECT_TIMEOUT_SECONDS = float(os.environ.get("MDD_VPCD_CONNECT_TIMEOUT", "2"))


async def _forward_vpcd_websocket_to_tcp(websocket: WebSocket, tcp_writer,
                                          registry, claim) -> None:
    """Forward each binary WebSocket message as one VPCD frame.

    A zero-length binary message is a legal empty ATR response.  WebSocket close is signalled
    separately by ``receive_bytes`` raising ``WebSocketDisconnect``; truthiness must never be
    used as the transport-liveness test.
    """
    while True:
        data = await websocket.receive_bytes()
        registry.touch(claim)
        tcp_writer.write(_vpcd_frame(data))
        await tcp_writer.drain()


async def _claim_and_open_vpcd_transport(*, registry, claim_kwargs: dict,
                                         unavailable_slots: set[int]):
    """Claim a healthy local VPCD transport, skipping broken slots in auto mode."""
    requested = str(claim_kwargs.get("requested_slot") or "auto").strip().lower()
    automatic = requested in ("", "auto")
    failed_slots: set[int] = set()
    last_error: Exception | None = None

    for _ in range(registry.max_slots if automatic else 1):
        claim = registry.claim(
            **claim_kwargs,
            unavailable_slots=set(unavailable_slots) | failed_slots,
        )
        try:
            tcp_reader, tcp_writer = await asyncio.wait_for(
                asyncio.open_connection("127.0.0.1", claim.port),
                timeout=VPCD_CONNECT_TIMEOUT_SECONDS,
            )
            return claim, tcp_reader, tcp_writer
        except Exception as exc:
            registry.release(claim)
            failed_slots.add(claim.slot)
            last_error = exc
            log.warning("[VPCD-WS] Local slot %d port %d is unavailable: %s",
                        claim.slot, claim.port, exc)
            if not automatic:
                break

    if last_error is not None:
        raise last_error
    raise vpcd_slots.SlotFull("no healthy VPCD transport slot is available")


@app.websocket("/api/vpcd/ws")
async def api_vpcd_ws(
    websocket: WebSocket,
    token: str = None,
    slot: str = "auto",
    agent_id: str = "",
    reader_id: str = "",
    reader_name: str = "",
    card_id: str = "",
    imei: str = "",
    agent_run_id: str = "",
):
    """
    Encrypted & Authenticated VPCD Bridge over WebSocket.
    Allows remote smartcard agents (Go, Android, Python) to securely forward
    APDU traffic into the server's local pcscd / libifdvpcd socket without exposing
    raw port 35963 to the public internet.
    """
    # 1. Authenticate Token
    req_token = token or websocket.query_params.get("token")
    if not req_token:
        auth_header = websocket.headers.get("authorization", "")
        if auth_header.lower().startswith("bearer "):
            req_token = auth_header[7:].strip()
        else:
            req_token = websocket.headers.get("x-agent-token", "").strip()

    if not auth.verify_agent_token(req_token):
        log.warning("[VPCD-WS] Unauthorized connection attempt from %s (invalid token)", websocket.client)
        await websocket.close(code=4003, reason="Unauthorized: Invalid agent token")
        return

    # 2. Claim one transport slot.  Stable endpoint/card hints only influence which free
    # slot is preferred; they never turn the slot into business identity.  Old agents with
    # no metadata remain compatible and simply receive the next safe free slot.
    slot_param = websocket.query_params.get("slot") or slot or "auto"
    # Legacy vicc/Mac clients may connect directly to a raw VPCD port and never create a
    # registry claim. A present PC/SC card proves that transport is occupied, so exclude it
    # from WSS allocation and never replace a live legacy card.
    externally_occupied = {
        detected_slot
        for card in hub.cards_list()
        if card.get("present")
        and (detected_slot := vpcd_slots.slot_from_reader_name(card.get("name"))) is not None
    }
    try:
        claim, tcp_reader, tcp_writer = await _claim_and_open_vpcd_transport(
            registry=vpcd_registry,
            claim_kwargs=dict(
            agent_id=websocket.query_params.get("agent_id") or agent_id,
            reader_id=websocket.query_params.get("reader_id") or reader_id,
            reader_name=websocket.query_params.get("reader_name") or reader_name,
            requested_slot=slot_param,
            card_id=websocket.query_params.get("card_id") or card_id,
            imei=websocket.query_params.get("imei") or imei,
            agent_run_id=websocket.query_params.get("agent_run_id") or agent_run_id,
            peer=str(websocket.client or ""),
            ),
            unavailable_slots=externally_occupied,
        )
    except vpcd_slots.SlotBusy as exc:
        log.info("[VPCD-WS] Busy slot/reader rejected for %s: %s", websocket.client, exc)
        await websocket.close(code=4409, reason=str(exc))
        return
    except vpcd_slots.SlotFull as exc:
        log.warning("[VPCD-WS] Capacity exhausted for %s", websocket.client)
        await websocket.close(code=4503, reason=str(exc))
        return
    except vpcd_slots.SlotError as exc:
        await websocket.close(code=4400, reason=str(exc))
        return

    except Exception as e:
        log.error("[VPCD-WS] No healthy local VPCD socket is available: %s", e)
        await websocket.close(code=4503, reason=f"Local VPCD unavailable: {e}")
        return

    # 3. The claim already owns a verified local libifdvpcd connection.
    await websocket.accept()
    log.info("[VPCD-WS] Secure VPCD bridge connected from %s (slot=%d port=%d reader=%s)",
             websocket.client, claim.slot, claim.port,
             websocket.query_params.get("reader_name") or reader_name or "legacy")
    await _reconcile_all_remote_card_evidence()

    async def ws_to_tcp():
        try:
            await _forward_vpcd_websocket_to_tcp(
                websocket, tcp_writer, vpcd_registry, claim)
        except (WebSocketDisconnect, asyncio.CancelledError):
            pass
        except Exception as err:
            log.debug("[VPCD-WS] ws_to_tcp exception: %s", err)
        finally:
            tcp_writer.close()
            try:
                await tcp_writer.wait_closed()
            except Exception:
                pass

    async def tcp_to_ws():
        try:
            while True:
                payload = await _vpcd_read_frame(tcp_reader)
                if payload:
                    await websocket.send_bytes(payload)
        except (asyncio.IncompleteReadError, WebSocketDisconnect, asyncio.CancelledError):
            pass
        except Exception as err:
            log.debug("[VPCD-WS] tcp_to_ws exception: %s", err)
        finally:
            try:
                await websocket.close()
            except Exception:
                pass

    try:
        await asyncio.gather(ws_to_tcp(), tcp_to_ws())
    finally:
        vpcd_registry.release(claim)
        await _reconcile_all_remote_card_evidence()
        log.info("[VPCD-WS] Session closed for %s (slot=%d)", websocket.client, claim.slot)


@app.get("/api/vpcd/slots")
async def api_vpcd_slots():
    """Operational view of live and retained remote transport slots."""
    return {"base_port": vpcd_slots.BASE_PORT, "max_slots": vpcd_registry.max_slots,
            "slots": vpcd_registry.snapshot()}


# ----------------------------- WebSocket -----------------------------
@app.websocket("/ws")
async def ws_endpoint(ws: WebSocket):
    # Accept before the application-level close so browsers receive code 4401 instead of
    # treating the rejected handshake as an opaque HTTP 403 and reconnecting forever.
    await ws.accept()
    token = ws.query_params.get("token") or ws.cookies.get(auth.SESSION_COOKIE)
    if not auth.session(token):
        if ws.query_params.get("auth_close") == "1":
            await ws.close(code=4401)
        else:
            # A tab loaded before this fix does not understand 4401 and reconnects every two
            # seconds after any close. Keep that unauthenticated legacy socket out of the hub
            # but quietly open until the user reloads or closes the tab.
            try:
                await ws.receive_text()
            except Exception:
                pass
        return
    hub.clients.add(ws)
    try:
        while True:
            await ws.receive_text()  # keepalive / ignore inbound
    except WebSocketDisconnect:
        hub.clients.discard(ws)
    except Exception:
        hub.clients.discard(ws)


# ----------------------------- static WebUI -----------------------------
if os.path.isdir(WEBUI_DIR):
    app.mount("/assets", StaticFiles(directory=os.path.join(WEBUI_DIR, "assets")), name="assets")

    @app.get("/{full_path:path}")
    def spa(full_path: str):
        # Unknown API paths must stay real 404s. Returning index.html here makes clients parse a
        # missing endpoint as a successful empty feature (and hid the removed stack API).
        if full_path == "api" or full_path.startswith("api/"):
            return JSONResponse({"detail": "API endpoint not found"}, status_code=404)
        # Root static files are an explicit allowlist. Joining an attacker-controlled path to
        # WEBUI_DIR allowed ../ traversal and exposed arbitrary container files.
        if full_path == "logo.svg":
            candidate = os.path.join(WEBUI_DIR, "logo.svg")
            if os.path.isfile(candidate):
                return FileResponse(candidate)
        if _unsafe_spa_path(full_path):
            return JSONResponse({"detail": "Not found"}, status_code=404)
        index = os.path.join(WEBUI_DIR, "index.html")
        if os.path.isfile(index):
            return FileResponse(
                index,
                headers={
                    "Cache-Control": "no-cache, no-store, must-revalidate",
                    "Pragma": "no-cache",
                    "Expires": "0",
                },
            )
        return JSONResponse({"error": "webui not built"}, status_code=404)
