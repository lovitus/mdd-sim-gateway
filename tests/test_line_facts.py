from unittest.mock import patch

from control.app import main
from control.app.line_facts import build_line_facts


def _runtime():
    return {"running": True, "container_id": "c1", "engine_run_id": "run1",
            "started_at": "2026-08-28T00:00:00Z"}


def _route(state="ready"):
    return {"state": state, "code": "route_current", "source": "card.monitor",
            "slot": 10, "session_generation": "session-1"}


def _admission(blocked=False):
    return {"blocked": blocked, "code": "admission_allowed" if not blocked else "usim_fence"}


def _probe(**more):
    return {"pin": {"state": "READY"}, "tunnel_installed": True,
            "pcscf": "10.0.0.1", "registration": "Registered",
            "active_channels": 0, **more}


def test_all_current_facts_are_ready_without_reusing_display_status():
    value = build_line_facts(inst={"id": "1", "enabled": True}, runtime=_runtime(),
                             status={"state": "REGISTERING", "reason_code": "old_display"},
                             status_age_seconds=1, card_route=_route(), admission=_admission(),
                             probe=_probe(), now=100)
    assert value["summary"] == {"state": "ready", "code": "line_ready",
                                  "blockers": [], "degraded": [], "unknown": ["media"]}
    assert value["facts"]["ims"]["code"] == "sip_registered"
    assert value["generation"]["vpcd_session_generation"] == "session-1"


def test_registered_cannot_mask_a_stale_card_route_or_fence():
    value = build_line_facts(inst={"id": "1", "enabled": True}, runtime=_runtime(),
                             status={"state": "OK", "detail": {"registration": "Registered"}},
                             status_age_seconds=1, card_route=_route("degraded"),
                             admission=_admission(True), probe=_probe(), now=100)
    assert value["facts"]["ims"]["state"] == "ready"
    assert value["summary"]["state"] == "blocked"
    assert set(value["summary"]["blockers"]) == {"admission"}
    assert "card_route" in value["summary"]["degraded"]


def test_missing_observations_are_unknown_not_healthy():
    value = build_line_facts(inst={"id": "1", "enabled": True}, runtime=_runtime(),
                             status=None, status_age_seconds=None, card_route=None,
                             admission=None, probe={}, now=100)
    assert value["summary"]["state"] == "unknown"
    assert "ims" in value["summary"]["unknown"]
    assert value["facts"]["work"]["code"] == "active_channels_unobserved"


def test_disabled_line_is_blocked_even_if_old_probe_was_ready():
    value = build_line_facts(inst={"id": "1", "enabled": False}, runtime=_runtime(),
                             status={}, status_age_seconds=0, card_route=_route(),
                             admission=_admission(), probe=_probe(), now=100)
    assert value["summary"]["state"] == "blocked"
    assert value["summary"]["blockers"][0] == "line"


def test_generation_change_invalidates_a_passive_sample_instead_of_mixing_runs():
    value = build_line_facts(inst={"id": "1", "enabled": True}, runtime=_runtime(),
                             status={}, status_age_seconds=0, card_route=_route(),
                             admission=_admission(),
                             probe=_probe(generation_current=False), now=100)
    assert value["facts"]["engine"]["code"] == "generation_changed_during_probe"
    assert value["summary"]["state"] == "unknown"


def test_dashboard_facts_use_only_cached_runtime_and_do_not_inspect_docker():
    inst = {"id": "1", "enabled": True, "iccid": "8901"}
    status = {"state": "OK", "reason_code": "ok", "detail": {
        "pin": {"state": "READY"}, "registration": "Registered"}}
    with patch.object(main, "_browser_call_recovery_global_pending", False), \
            patch.object(main, "_browser_call_recovery_pending_lines", set()), \
            patch.object(main.hub, "engine_recovering", set()), \
            patch.object(main.hub.runtime, "peek", return_value=_runtime()), \
            patch.object(main.hub.runtime, "get", side_effect=AssertionError("no Docker probe")), \
            patch.object(main.hub, "cards_list", return_value=[{
                "present": True, "iccid": "8901", "remote": False}]), \
            patch.object(main.engine, "global_maintenance_pending", return_value=False), \
            patch.object(main.engine, "engine_maintenance_pending", return_value=False), \
            patch.object(main.engine, "engine_start_quarantine_pending", return_value=False), \
            patch.object(main.engine, "usim_recovery_fence_pending", return_value=False), \
            patch.object(main.engine, "pcscf_rebind_pending", return_value=False):
        value = main._cached_line_facts(inst, status)
    assert value["summary"]["state"] == "ready"
    assert value["facts"]["tunnel"]["code"] == "tunnel_sampled"
