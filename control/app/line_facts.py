"""One generation-bound, presentation-safe view of a VoWiFi line.

The control plane has several independent safety state machines.  They must not be
collapsed or weakened just to make a green badge.  This module is deliberately a
pure projection: callers provide observations collected from the current Engine
generation and it explains what is known, stale, blocked, or contradictory.
"""
from __future__ import annotations

from typing import Any


FACT_STATES = frozenset({"ready", "degraded", "blocked", "unknown"})


def _fact(state: str, code: str, *, source: str, observed_at: int,
          detail: dict[str, Any] | None = None) -> dict[str, Any]:
    if state not in FACT_STATES:
        raise ValueError(f"invalid fact state: {state}")
    return {"state": state, "code": code, "source": source,
            "observed_at": int(observed_at), "detail": detail or {}}


def _status_observation(status: dict[str, Any] | None) -> dict[str, Any]:
    return status if isinstance(status, dict) else {}


def _pin_fact(status: dict[str, Any], probe: dict[str, Any], now: int) -> dict[str, Any]:
    pin = probe.get("pin")
    if not isinstance(pin, dict):
        pin = (status.get("detail") or {}).get("pin")
    state = str((pin or {}).get("state") or "")
    if state in {"NO_CARD", "WRONG_PIN", "PIN_BLOCKED", "AUTH_FAIL"}:
        return _fact("blocked", f"pin_{state.lower()}", source="engine.pin_status",
                     observed_at=now, detail={"pin_state": state})
    if state:
        return _fact("ready", "pin_usable", source="engine.pin_status",
                     observed_at=now, detail={"pin_state": state})
    return _fact("unknown", "pin_unobserved", source="engine.pin_status",
                 observed_at=now)


def _tunnel_fact(status: dict[str, Any], probe: dict[str, Any], now: int) -> dict[str, Any]:
    installed = probe.get("tunnel_installed")
    if installed is True:
        return _fact("ready", "tunnel_installed", source="engine.run",
                     observed_at=now, detail={"pcscf": probe.get("pcscf") or ""})
    if installed is False:
        return _fact("degraded", "tunnel_not_installed", source="engine.run",
                     observed_at=now, detail={"reason_code": status.get("reason_code") or ""})
    state = str(status.get("state") or "")
    if state in {"TUNNEL_DOWN", "EPDG_UNRESOLVED"}:
        return _fact("degraded", str(status.get("reason_code") or state.lower()),
                     source="status.sample", observed_at=now)
    return _fact("unknown", "tunnel_unobserved", source="engine.run", observed_at=now)


def _ims_fact(status: dict[str, Any], probe: dict[str, Any], now: int) -> dict[str, Any]:
    registration = str(probe.get("registration") or
                       (status.get("detail") or {}).get("registration") or "")
    if registration == "Registered":
        return _fact("ready", "sip_registered", source="asterisk.pjsip",
                     observed_at=now, detail={"registration": registration})
    if registration in {"Rejected", "Unregistered"}:
        return _fact("degraded", f"sip_{registration.lower()}", source="asterisk.pjsip",
                     observed_at=now, detail={"registration": registration,
                                               "reason_code": status.get("reason_code") or ""})
    return _fact("unknown", "sip_unobserved", source="asterisk.pjsip", observed_at=now,
                 detail={"registration": registration or "unknown"})


def build_line_facts(*, inst: dict[str, Any], runtime: dict[str, Any] | None,
                     status: dict[str, Any] | None, status_age_seconds: float | None,
                     card_route: dict[str, Any] | None, admission: dict[str, Any] | None,
                     probe: dict[str, Any] | None = None, now: int) -> dict[str, Any]:
    """Project independently collected facts without performing I/O.

    ``status`` remains evidence rather than truth: a registered PJSIP row can only make the
    IMS fact ready; it cannot hide a missing card route, an action fence, or a generation
    change.  ``probe`` is optional because the ordinary UI refresh must not shell out.
    """
    runtime = runtime if isinstance(runtime, dict) else {}
    status = _status_observation(status)
    route = card_route if isinstance(card_route, dict) else {}
    admission = admission if isinstance(admission, dict) else {}
    probe = probe if isinstance(probe, dict) else {}
    running = bool(runtime.get("running"))
    generation = {
        "container_id": str(runtime.get("container_id") or ""),
        "engine_run_id": str(runtime.get("engine_run_id") or ""),
        "started_at": str(runtime.get("started_at") or ""),
        "vpcd_slot": route.get("slot"),
        "vpcd_session_generation": str(route.get("session_generation") or ""),
    }
    route_state = str(route.get("state") or "unknown")
    if route_state not in FACT_STATES:
        route_state = "unknown"
    generation_current = probe.get("generation_current")
    facts = {
        "engine": _fact(
            ("unknown" if generation_current is False else
             "ready" if running and all(generation[key] for key in
                                        ("container_id", "engine_run_id", "started_at")) else
             "degraded" if not running else "unknown"),
            "generation_changed_during_probe" if generation_current is False
            else ("engine_running" if running else "engine_not_running"),
            source="docker.runtime", observed_at=now,
            detail={"generation_complete": all(generation[key] for key in
                                                 ("container_id", "engine_run_id", "started_at")),
                    "probe_generation_current": generation_current}),
        "card_route": _fact(route_state, str(route.get("code") or "route_unobserved"),
                             source=str(route.get("source") or "card.monitor"), observed_at=now,
                             detail=dict(route.get("detail") or {})),
        "pin": _pin_fact(status, probe, now),
        "tunnel": _tunnel_fact(status, probe, now),
        "ims": _ims_fact(status, probe, now),
        "admission": _fact(
            "blocked" if admission.get("blocked") is True
            else ("ready" if admission.get("blocked") is False else "unknown"),
            str(admission.get("code") or ("admission_blocked" if admission.get("blocked")
                                            else "admission_unobserved")),
            source=str(admission.get("source") or "control.admission"), observed_at=now,
            detail=dict(admission.get("detail") or {})),
    }
    channels = probe.get("active_channels")
    if type(channels) is int and channels >= 0:
        facts["work"] = _fact("ready", "active_call" if channels else "idle",
                               source="asterisk.channels", observed_at=now,
                               detail={"active_channels": channels})
    else:
        facts["work"] = _fact("unknown", "active_channels_unobserved",
                               source="asterisk.channels", observed_at=now)
    media = probe.get("media") if isinstance(probe.get("media"), dict) else {}
    media_state = str(media.get("state") or "unknown")
    facts["media"] = _fact(
        media_state if media_state in FACT_STATES else "unknown",
        str(media.get("code") or "browser_media_not_verified"),
        source=str(media.get("source") or "browser.wss"), observed_at=now,
        detail=dict(media.get("detail") or {}))

    required = ("engine", "card_route", "pin", "tunnel", "ims", "admission")
    blockers = [name for name in ("engine", "card_route", "pin", "admission")
                if facts[name]["state"] == "blocked"]
    degraded = [name for name, value in facts.items() if value["state"] == "degraded"]
    unknown = [name for name, value in facts.items() if value["state"] == "unknown"]
    required_degraded = [name for name in required if facts[name]["state"] == "degraded"]
    required_unknown = [name for name in required if facts[name]["state"] == "unknown"]
    if not inst.get("enabled", True):
        summary_state, summary_code = "blocked", "line_disabled"
        blockers.insert(0, "line")
    elif blockers:
        summary_state, summary_code = "blocked", "action_blocked"
    elif required_degraded:
        summary_state, summary_code = "degraded", "line_degraded"
    elif required_unknown:
        summary_state, summary_code = "unknown", "evidence_incomplete"
    else:
        summary_state, summary_code = "ready", "line_ready"
    return {
        "version": 1,
        "instance": str(inst.get("id") or ""),
        "sampled_at": int(now),
        "generation": generation,
        "status_source": {
            "state": str(status.get("state") or ""),
            "reason_code": str(status.get("reason_code") or ""),
            "age_seconds": (round(float(status_age_seconds), 3)
                            if isinstance(status_age_seconds, (int, float)) else None),
        },
        "facts": facts,
        "summary": {"state": summary_state, "code": summary_code,
                    "blockers": blockers, "degraded": degraded, "unknown": unknown},
    }
