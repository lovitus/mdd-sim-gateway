"""
status.py - Per-instance status state machine with failure classification.

Returns a live snapshot: {state, label, reason_code, reason, detail}. The manager's
health tracker (main.py) overlays retry counters and, on exhaustion, an ERROR state.

States:      STOPPED, NO_CARD, PIN_PROBLEM, EPDG_UNRESOLVED, TUNNEL_DOWN, REGISTERING, OK
reason_code: machine key for the WebUI; `reason` is a user-friendly sentence.
detail:      raw signals (pin, pcscf, registration, ike classification) for advanced view.
"""
from __future__ import annotations

import asyncio
import hashlib
import logging
import math
import re
import socket
import threading
import time
from datetime import datetime

from . import engine

log = logging.getLogger("vowifi.status")

LABELS = {
    "STOPPED": "Stopped",
    "NO_CARD": "No SIM card",
    "PIN_PROBLEM": "PIN error",
    "EPDG_UNRESOLVED": "Cannot resolve ePDG",
    "TUNNEL_DOWN": "Establishing VoWiFi tunnel",
    "REGISTERING": "Registering to IMS",
    "OK": "Working",
    "ERROR": "Failed",
}

# reason_code -> user-friendly message
REASONS = {
    "no_card": "No SIM card detected in the reader.",
    "pin_wrong": "SIM PIN is incorrect.",
    "pin_blocked": "SIM PIN is blocked — PUK required.",
    "epdg_unresolved": "Can't resolve the carrier's VoWiFi (ePDG) address — the carrier may "
                       "not support Wi-Fi Calling, or check DNS / internet connectivity.",
    "tunnel_network": "Can't establish the VoWiFi tunnel — network problem (no response from "
                      "the carrier's ePDG).",
    "tunnel_child_rekey_timeout": "The carrier ePDG did not answer the CHILD_SA rekey; "
                                  "the current Engine is retrying the tunnel.",
    "tunnel_ike_rekey_timeout": "The carrier ePDG did not answer the IKE_SA rekey; "
                                "the current Engine is retrying the tunnel.",
    "tunnel_rekey_send_error": "The client could not send an IPsec rekey request; "
                               "the current Engine is retrying the tunnel.",
    "tunnel_sim_auth": "Can't establish the VoWiFi tunnel — SIM authentication (EAP-AKA) was "
                       "rejected by the carrier.",
    "tunnel_not_authorized": "Can't establish the VoWiFi tunnel — the carrier's ePDG refused the "
                             "identity before checking the SIM. The line is likely not provisioned "
                             "for Wi-Fi Calling, or the ePDG blocks connections from this network/region.",
    "tunnel_proposal": "Can't establish the VoWiFi tunnel — the carrier rejected the encryption "
                       "settings (IKE proposal).",
    "tunnel_setup": "Establishing the VoWiFi (IPsec/ePDG) tunnel…",
    "registering": "VoWiFi tunnel is up — registering to the carrier's IMS…",
    "local_bootstrap_unready": "The Engine did not publish its SIM bootstrap state.",
    "local_registration_unreadable": "The Engine's local Asterisk registration state is unreadable.",
    "local_registration_stalled": "The Engine's local IMS registration did not start.",
    "local_usim_unavailable": "The local smart-card service interrupted IMS authentication; "
                              "the same Engine will retry once the assigned card path is back.",
    "reg_rejected": "Can't register to the carrier's IMS (authentication or provisioning issue).",
    "reg_temporary": "The carrier's IMS is temporarily unavailable; Asterisk will retry "
                     "this registration in place.",
    "reg_unanswered": "The carrier's IMS stopped answering registration; the current Engine "
                      "will retry in place while bounded recovery checks whether replacement "
                      "is safe.",
    "ok": "Working — connected to the carrier over Wi-Fi.",
}

LOCAL_BOOTSTRAP_GRACE_SECONDS = 120
LOCAL_REGISTRATION_GRACE_SECONDS = 300
LOCAL_USIM_FAILURE_MAX_AGE_SECONDS = 3700
_registration_evidence_lock = threading.RLock()
# A successful live registration must fence an older persisted failure even when the data
# volume is temporarily full/read-only.  This is diagnostic state only; process restart will
# immediately rebuild the fence from a current Registered sample before accepting new evidence.
_registration_success_fences: dict[tuple[str, str, str, str], dict] = {}
_HEX64 = re.compile(r"^[0-9a-f]{64}$")
_ENGINE_RUN_ID = re.compile(r"^[A-Za-z0-9_.:-]{1,128}$")
_LOCAL_USIM_RECOVERY_CAUSES = frozenset({
    "pcsc_service_unavailable", "pcsc_card_reset",
})


def current_local_usim_unavailable(value: object, runtime: dict | None,
                                   now: float | None = None) -> dict | None:
    """Accept only a fresh, exact-generation local PC/SC outage marker.

    This marker changes presentation and authorizes a separately fenced recovery reconciler;
    malformed, stale or cross-generation files must fall back to the carrier evidence instead.
    """
    if not isinstance(value, dict) or value.get("state") != "AUTH_UNAVAILABLE":
        return None
    run_id = value.get("engine_run_id")
    auth_seq = value.get("auth_seq")
    observed = value.get("ts")
    if (not isinstance(run_id, str) or not _ENGINE_RUN_ID.fullmatch(run_id)
            or not isinstance(runtime, dict)
            or str(runtime.get("engine_run_id") or "") != run_id
            or type(auth_seq) is not int or auth_seq <= 0
            or value.get("cause_class") not in _LOCAL_USIM_RECOVERY_CAUSES
            or not isinstance(observed, (int, float)) or isinstance(observed, bool)
            or not math.isfinite(float(observed))):
        return None
    current = time.time() if now is None else float(now)
    age = current - float(observed)
    if not math.isfinite(current) or age < 0 or age > LOCAL_USIM_FAILURE_MAX_AGE_SECONDS:
        return None
    return {"engine_run_id": run_id, "auth_seq": auth_seq,
            "cause_class": value["cause_class"], "ts": float(observed)}


def _registration_event_time(line: str) -> float | None:
    match = re.match(r"^\[(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}[+-]\d{4})\]", line)
    if not match:
        return None
    try:
        return datetime.strptime(match.group(1), "%Y-%m-%d %H:%M:%S%z").timestamp()
    except ValueError:
        return None


def _registration_failure_event(log_tail: str) -> dict:
    """Classify the newest concrete REGISTER failure and retain its SIP response code.

    Asterisk reports both as "Rejected", but they are different events: a "Fatal response
    '403'" is the IMS refusing this line, while "No response received" is the IMS no longer
    hearing it — on this gateway almost always an ESP session the carrier aged out while
    the IKE side still answered keepalives. The newest marker in the log decides.
    """
    for line in reversed(log_tail.splitlines()):
        low = line.lower()
        registration_attempt = re.search(r"on (?:register|registration) attempt\b", low)
        event_at = _registration_event_time(line)
        # The real Asterisk message says "on registration attempt", not "on REGISTER
        # attempt".  ``registration`` does not contain the substring ``register``, so the
        # old extra guard made this production path unreachable.  This exact marker is emitted
        # by outbound registration's timeout path and is already the evidence retained by
        # engine._SIP_EVIDENCE.  A Docker log read failure is returned as "error: ...", which
        # deliberately does not match and therefore remains on the conservative slow path.
        if "no response received" in low and registration_attempt:
            return {"kind": "unanswered", "event_at": event_at,
                    "event_key": hashlib.sha256(line.encode()).hexdigest()}
        legacy_fatal = re.search(r"fatal response '(\d{3})'", low)
        pinned_fatal = re.search(r"'(\d{3})' fatal response received", low)
        if ((legacy_fatal and registration_attempt)
                or (pinned_fatal and re.search(r"retrying in '\d{1,6}' seconds", low))):
            match = legacy_fatal or pinned_fatal
            return {"kind": "rejected", "sip_status": int(match.group(1)),
                    "event_at": event_at,
                    "event_key": hashlib.sha256(line.encode()).hexdigest()}
        # Asterisk owns the retry schedule for a *temporal* outbound-registration
        # response.  Keep this deliberately narrower than a generic 503 match: both
        # the registration-attempt marker and the retry delay must be present on the
        # same log line, otherwise an unrelated SIP transaction or old history could
        # suppress the gateway's normal bounded recovery policy.
        temporary = re.search(r"temporal response '(\d{3})'", low)
        retry = re.search(r"retrying in '(\d{1,6})'", low)
        if temporary and retry and registration_attempt:
            return {
                "kind": "temporary",
                "sip_status": int(temporary.group(1)),
                "retry_after_seconds": max(1, min(86_400, int(retry.group(1)))),
                "event_at": event_at,
                "event_key": hashlib.sha256(line.encode()).hexdigest(),
            }
    return {"kind": "unknown"}


def registration_failure_evidence(log_tail: str) -> dict:
    """Public, stable view of the newest registration failure marker."""
    result = _registration_failure_event(log_tail)
    result.pop("event_key", None)
    result.pop("event_at", None)
    return result


def _runtime_age(runtime: dict | None) -> float | None:
    started = (runtime or {}).get("started_at_epoch")
    if type(started) not in (int, float):
        return None
    age = time.time() - float(started)
    return max(0.0, age) if age < 365 * 24 * 3600 else None


def _registration_evidence_owner(
        inst: dict, runtime: dict | None) -> tuple[str, str, str]:
    generation = str((runtime or {}).get("container_id") or "")
    started = (runtime or {}).get("started_at_epoch")
    incarnation = ""
    if (type(started) in (int, float) and math.isfinite(started)
            and 0 < float(started) < time.time() + 300):
        incarnation = f"{float(started):.6f}"
    iccid = str(inst.get("iccid") or "")
    fingerprint = hashlib.sha256(iccid.encode()).hexdigest() if iccid else ""
    return generation, incarnation, fingerprint


def _registration_owner_key(inst: dict, runtime: dict | None) -> tuple[str, str, str, str]:
    generation, incarnation, fingerprint = _registration_evidence_owner(inst, runtime)
    return str(inst.get("id") or ""), generation, incarnation, fingerprint


def _valid_registration_evidence_schema(
        value: dict, generation: str, incarnation: str, fingerprint: str) -> bool:
    observed_at = value.get("observed_at")
    event_key = value.get("event_key")
    return bool(
        type(value.get("version")) is int and value.get("version") == 1
        and generation and incarnation and _HEX64.fullmatch(fingerprint)
        and value.get("generation") == generation
        and value.get("incarnation") == incarnation
        and value.get("sim_fingerprint") == fingerprint
        and type(observed_at) in (int, float) and math.isfinite(observed_at)
        and observed_at > 0
        and isinstance(event_key, str) and _HEX64.fullmatch(event_key)
    )


def _validated_saved_registration_evidence(inst: dict, runtime: dict | None) -> dict:
    generation, incarnation, fingerprint = _registration_evidence_owner(inst, runtime)
    owner_key = _registration_owner_key(inst, runtime)
    with _registration_evidence_lock:
        # A live Registered sample is newer than any saved failure for this exact owner.
        # This in-memory fence also covers a full/read-only data volume where the tombstone
        # could not be persisted.  Only a new, timestamped live failure may supersede it via
        # _saved_registration_evidence; a log-rollover fallback may not.
        if owner_key in _registration_success_fences:
            return {"kind": "unknown"}
        value = engine.read_registration_evidence(
            str(inst["id"]), generation, incarnation) or {}
        if not _valid_registration_evidence_schema(
                value, generation, incarnation, fingerprint):
            return {"kind": "unknown"}
        kind = value.get("kind")
        if kind not in {"unanswered", "temporary", "rejected"}:
            return {"kind": "unknown"}
        retry_until = value.get("retry_until")
        if kind == "temporary":
            if (type(retry_until) not in (int, float) or not math.isfinite(retry_until)
                    or retry_until <= time.time() - 5
                    or retry_until > time.time() + 86_405):
                return {"kind": "unknown"}
        result = {"kind": kind}
        if type(value.get("sip_status")) is int:
            result["sip_status"] = value["sip_status"]
        if kind == "temporary":
            result["retry_after_seconds"] = max(
                1, min(86_400, int(retry_until - time.time())))
        return result


def _saved_registration_evidence(inst: dict, runtime: dict | None, evidence: dict) -> dict:
    generation, incarnation, fingerprint = _registration_evidence_owner(inst, runtime)
    if not generation or not incarnation or not fingerprint:
        return evidence
    event_key = str(evidence.get("event_key") or "")
    event_at = evidence.get("event_at")
    owner_key = _registration_owner_key(inst, runtime)
    with _registration_evidence_lock:
        existing = engine.read_registration_evidence(
            str(inst["id"]), generation, incarnation) or {}
        success = _registration_success_fences.get(owner_key)
        if existing.get("kind") == "registered":
            success = existing
        if success:
            success_at = success.get("observed_at")
            if (not _HEX64.fullmatch(event_key)
                    or event_key == success.get("fenced_event_key")
                    or type(event_at) not in (int, float) or not math.isfinite(event_at)
                    or type(success_at) not in (int, float) or not math.isfinite(success_at)
                    or event_at <= success_at):
                return {"kind": "unknown"}
        if (existing.get("generation") == generation
                and existing.get("incarnation") == incarnation
                and existing.get("sim_fingerprint") == fingerprint
                and existing.get("event_key") == event_key):
            return _validated_saved_registration_evidence(inst, runtime)
        # An undated line can classify the current live sample, but it is not durable evidence:
        # after log rotation there is no way to order it against a later successful REGISTER.
        if (type(event_at) not in (int, float) or not math.isfinite(event_at)
                or event_at <= 0 or not _HEX64.fullmatch(event_key)):
            return evidence
        value = {
            "version": 1, "generation": generation, "incarnation": incarnation,
            "sim_fingerprint": fingerprint, "kind": evidence["kind"],
            "observed_at": float(event_at), "event_key": event_key,
        }
        if type(evidence.get("sip_status")) is int:
            value["sip_status"] = evidence["sip_status"]
        if evidence["kind"] == "temporary":
            value["retry_until"] = float(event_at) + int(evidence["retry_after_seconds"])
        try:
            accepted = engine.write_registration_evidence(str(inst["id"]), value)
        except OSError as exc:
            # Durability improves classification after log rotation; it must never make the
            # current authoritative Asterisk result disappear merely because the data volume
            # is read-only.
            log.warning("could not persist registration evidence for line %s: %s", inst["id"], exc)
            return evidence
        if not accepted:
            # A concurrent Registered sample won the ordered write.  Re-read it through the
            # success fence instead of reviving the failure that this worker sampled earlier.
            current = engine.read_registration_evidence(
                str(inst["id"]), generation, incarnation) or {}
            if current.get("kind") == "registered":
                _registration_success_fences[owner_key] = current
                return {"kind": "unknown"}
            # A still-newer failure may already have won the file CAS.  It supersedes the
            # in-memory success fence just like this accepted live failure would.
            if current.get("kind") in {"unanswered", "temporary", "rejected"}:
                _registration_success_fences.pop(owner_key, None)
        else:
            # This source-timestamped failure is provably newer than the Registered sample.
            # Keep disk and memory ownership in sync so log rollover can use it durably.
            _registration_success_fences.pop(owner_key, None)
    return evidence


def _mark_registration_success(inst: dict, runtime: dict | None) -> None:
    """Fence earlier failures without an unlink race against a replacement generation."""
    generation, incarnation, fingerprint = _registration_evidence_owner(inst, runtime)
    if not generation or not incarnation or not fingerprint:
        return
    owner_key = _registration_owner_key(inst, runtime)
    with _registration_evidence_lock:
        existing = engine.read_registration_evidence(
            str(inst["id"]), generation, incarnation) or {}
        if (existing.get("generation") == generation
                and existing.get("incarnation") == incarnation
                and existing.get("sim_fingerprint") == fingerprint
                and existing.get("kind") == "registered"):
            _registration_success_fences[owner_key] = existing
            return
        tombstone = {
            "version": 1, "generation": generation, "incarnation": incarnation,
            "sim_fingerprint": fingerprint, "kind": "registered",
            "observed_at": time.time(),
            "fenced_event_key": str(existing.get("event_key") or ""),
        }
        # Install the in-memory fence before touching disk.  A full/read-only data volume must
        # not resurrect a failure already disproved by the live Registered state.
        _registration_success_fences[owner_key] = tombstone
        if len(_registration_success_fences) > 1024:
            oldest = min(
                _registration_success_fences,
                key=lambda key: _registration_success_fences[key].get("observed_at", 0))
            if oldest != owner_key:
                _registration_success_fences.pop(oldest, None)
        try:
            engine.write_registration_evidence(str(inst["id"]), tombstone)
        except OSError as exc:
            log.warning("could not persist registration success for line %s: %s", inst["id"], exc)


def registration_unanswered(log_tail: str) -> bool:
    """Compatibility predicate used by tests and the fast-recovery policy."""
    return registration_failure_evidence(log_tail)["kind"] == "unanswered"


def resolve_epdg(fqdn: str) -> bool:
    try:
        socket.getaddrinfo(fqdn, None)
        return True
    except Exception:
        return False


def nameservers() -> list[str]:
    """The resolvers a failed lookup was tried against — evidence for the outage record."""
    try:
        with open("/etc/resolv.conf", encoding="utf-8") as handle:
            return [line.split()[1] for line in handle
                    if line.strip().startswith("nameserver") and len(line.split()) > 1]
    except OSError:
        return []


def classify_ike(iid: str) -> tuple[str, str]:
    """Inspect recent charon (IKE) log to classify why the tunnel isn't up."""
    log = engine.charon_log(iid, 400)
    usim = engine.usim_status(iid)
    swu = engine.read_run_json(iid, "swu_status.json") or {}
    low = log.lower()
    # swu_ike publishes the exact terminal action before its supervisor re-establishes the
    # tunnel. Prefer that machine-readable evidence over generic words in the log tail.
    terminal = str(swu.get("reason_code") or "")
    if terminal == "rekey_timeout":
        return "tunnel_child_rekey_timeout", REASONS["tunnel_child_rekey_timeout"]
    if terminal == "ike_rekey_timeout":
        return "tunnel_ike_rekey_timeout", REASONS["tunnel_ike_rekey_timeout"]
    if terminal in {"rekey_send_error", "ike_rekey_send_error"}:
        return "tunnel_rekey_send_error", REASONS["tunnel_rekey_send_error"]
    # ePDG refused the IKE_AUTH identity BEFORE any EAP-AKA challenge (SIM never queried). This
    # is an authorization/subscription/geo decision, not a SIM/PIN fault — classify it distinctly
    # so the UI doesn't wrongly blame the SIM. swu_ike logs a clear marker for this case.
    if "before any eap-aka challenge" in low or "authentication_failed before" in low or \
            "not provisioned for vowifi" in low:
        return "tunnel_not_authorized", REASONS["tunnel_not_authorized"]
    # SIM auth failure (EAP-AKA)
    if usim.get("state") in ("AUTH_FAIL", "PIN_FAIL", "NO_CARD") or \
            "eap_aka failed" in low or "eap-aka failed" in low or \
            "authentication_failed" in low or "eap method eap_aka fail" in low or \
            "received auth_failed" in low or "authentication failed" in low:
        return "tunnel_sim_auth", REASONS["tunnel_sim_auth"]
    # Carrier rejected our crypto proposal / message
    if "invalid_syntax" in low or "no_proposal_chosen" in low or "invalid_ke" in low or \
            "no proposal" in low:
        return "tunnel_proposal", REASONS["tunnel_proposal"]
    # No response / retransmits -> network
    if "retransmit" in low or "giving up" in low or "no route" in low or \
            "destination unreachable" in low or "timeout" in low:
        return "tunnel_network", REASONS["tunnel_network"]
    # Not enough info yet -> still setting up
    return "tunnel_setup", REASONS["tunnel_setup"]


async def compute(inst: dict, ami_client=None, runtime: dict | None = None) -> dict:
    iid = str(inst["id"])
    mcc, mnc = inst["mcc"], str(inst["mnc"]).zfill(3)
    epdg = inst.get("epdg") or f"epdg.epc.mnc{mnc}.mcc{mcc}.pub.3gppnetwork.org"

    detail = {"msisdn": inst.get("msisdn") or None, "smsc": inst.get("smsc") or None,
              "iccid": inst.get("iccid") or None, "epdg_fqdn": epdg}

    def out(state, code):
        return {"state": state, "label": LABELS[state],
                "reason_code": code, "reason": REASONS.get(code, ""), "detail": detail}

    running = (bool(runtime.get("running")) if runtime is not None
               else await asyncio.to_thread(engine.is_running, iid))
    if not inst.get("enabled", True) or not running:
        return {"state": "STOPPED", "label": LABELS["STOPPED"],
                "reason_code": "stopped", "reason": "Stopped.", "detail": detail}

    pin = await asyncio.to_thread(engine.read_run_json, iid, "pin_status.json") or {}
    detail["pin"] = pin
    pstate = pin.get("state")
    if pstate is None:
        # A fresh/rebuilt container removes stale runtime observations before pin_keeper has
        # written its first result. A missing file is no evidence that the physical SIM was
        # removed, so make this an unreadable startup sample rather than a false card outage.
        detail["registration"] = "unknown"
        code = ("local_bootstrap_unready"
                if (_runtime_age(runtime) or 0) >= LOCAL_BOOTSTRAP_GRACE_SECONDS
                else "registering")
        return out("REGISTERING", code)
    if pstate == "NO_CARD":
        return out("NO_CARD", "no_card")
    if pstate == "WRONG_PIN":
        return out("PIN_PROBLEM", "pin_wrong")
    if pstate == "PIN_BLOCKED":
        return out("PIN_PROBLEM", "pin_blocked")

    if not await asyncio.to_thread(engine.tunnel_installed, iid):
        # DNS only matters while there is no tunnel: an established tunnel talks to an
        # address, not a name. Checking it first used to chart healthy lines as down for a
        # minute whenever the upstream resolver blipped — the ePDG records rotate every ~30s,
        # so they are always a cache miss and always the first names to fail.
        if not await asyncio.to_thread(resolve_epdg, epdg):
            # Which resolvers refused the name is the difference between "DNS was down"
            # and knowing whose DNS was down.
            detail["nameservers"] = nameservers()
            return out("EPDG_UNRESOLVED", "epdg_unresolved")
        code, _ = await asyncio.to_thread(classify_ike, iid)
        r = out("TUNNEL_DOWN", code)
        detail["ike_reason"] = code
        return r

    detail["pcscf"] = await asyncio.to_thread(engine.read_pcscf, iid)
    # The persistent AMI connection avoids a Docker exec on every sample. Its implementation
    # deliberately uses AMI Command rather than PJSIPShowRegistrationsDetailed, which hangs on
    # some supported IMS-patched builds. A bounded local CLI remains the authoritative fallback
    # while AMI is connecting or recovering.
    try:
        reg = await ami_client.registration_state() if ami_client is not None else "unknown"
    except Exception:
        reg = "unknown"
    if reg == "unknown":
        try:
            reg = await asyncio.wait_for(asyncio.to_thread(engine.registration_state, iid), 5)
        except Exception:
            reg = "unknown"
    detail["registration"] = reg
    if reg == "Registered":
        await asyncio.to_thread(_mark_registration_success, inst, runtime)
        return out("OK", "ok")
    if reg == "Rejected":
        local_usim = current_local_usim_unavailable(
            await asyncio.to_thread(engine.usim_status, iid), runtime)
        if local_usim is not None:
            detail["local_auth"] = {
                "state": "AUTH_UNAVAILABLE",
                "cause_class": local_usim["cause_class"],
                "auth_seq": local_usim["auth_seq"],
            }
            return out("REGISTERING", "local_usim_unavailable")
        # One extra docker-logs read, only in the rare Rejected state: "no answer" and
        # "refused" need different fixes, so they must not share a label.
        tail = await asyncio.to_thread(engine.logs, iid, 200)
        evidence = _registration_failure_event(tail)
        if evidence["kind"] != "unknown":
            evidence = await asyncio.to_thread(
                _saved_registration_evidence, inst, runtime, evidence)
        else:
            evidence = await asyncio.to_thread(
                _validated_saved_registration_evidence, inst, runtime)
        unanswered = evidence["kind"] == "unanswered"
        temporary = evidence["kind"] == "temporary"
        result = out("REGISTERING", (
            "reg_unanswered" if unanswered else
            "reg_temporary" if temporary else
            "reg_rejected"))
        if evidence.get("sip_status") is not None:
            result["detail"]["sip_status"] = evidence["sip_status"]
        if evidence.get("retry_after_seconds") is not None:
            result["detail"]["retry_after_seconds"] = evidence["retry_after_seconds"]
        if unanswered:
            # Unknown is intentionally different from zero: if AMI cannot prove there are no
            # active channels, try the same bounded local CLI view before failing closed.  A
            # disconnected AMI during a stale registration must not make a genuinely idle line
            # unrecoverable, while an unreadable CLI must never be interpreted as zero.
            try:
                channels = (await ami_client.active_channel_count()
                            if ami_client is not None else None)
            except Exception:
                channels = None
            if type(channels) is not int:
                try:
                    channels = await asyncio.to_thread(engine.active_channel_count, iid)
                except Exception:
                    channels = None
            result["detail"]["active_channels"] = (
                channels if type(channels) is int else None)
        return result
    age = _runtime_age(runtime)
    if age is not None and age >= LOCAL_REGISTRATION_GRACE_SECONDS:
        return out("REGISTERING", (
            "local_registration_unreadable" if reg == "unknown"
            else "local_registration_stalled"))
    return out("REGISTERING", "registering")
