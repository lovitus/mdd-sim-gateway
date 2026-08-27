import time
from types import SimpleNamespace
from unittest.mock import AsyncMock, Mock

import pytest

from control.app import main, vpcd_slots


def campaign_identity(runtime):
    return {
        "exact_current": True, "campaign_epoch": "c" * 64,
        "stable_card_key": "eid:stable", "line_config_epoch": "d" * 64,
        "current_route_generation": "route-one", "sample_generation": "9" * 64,
        "container_id": runtime["container_id"], "started_at": runtime["started_at"],
        "engine_run_id": runtime["engine_run_id"], "auth_seq_baseline": 7,
    }


@pytest.mark.asyncio
@pytest.mark.parametrize("reservation_valid", [True, False])
async def test_prepare_is_durable_before_route_reservation_and_permit(
        monkeypatch, reservation_valid):
    iid, inst = "route-linear", {"id": "route-linear"}
    runtime = {
        "running": True, "container_id": "a" * 64,
        "started_at": "2026-08-27T00:00:00.000000000Z",
        "engine_run_id": "run-current",
    }
    failure = {"engine_run_id": "run-current", "auth_seq": 7,
               "cause_class": "pcsc_service_unavailable", "ts": 1000.0}
    topology_identity = {"slot": 0, "session_generation": "route-one",
                         "eid": "stable", "iccid": "iccid-one", "matched": iid}
    topology = ("e" * 64, topology_identity)
    identity = campaign_identity(runtime)
    deadline = time.time() + 100
    legacy = {"version": 1, "phase": "pending", "auth_seq": 7,
              "container_id": runtime["container_id"],
              "started_at": runtime["started_at"],
              "engine_run_id": runtime["engine_run_id"]}
    prepared = {"version": 2, "phase": "pending", "campaign_epoch": "c" * 64,
                "engine_run_id": "run-current", "auth_seq_baseline": 7,
                "deadline": deadline, "permit_nonce": None}
    permitted = {**prepared, "phase": "permit_issued", "permit_nonce": "f" * 32}
    route_reservation = vpcd_slots.RecoveryReservation(
        slot=0, token="1" * 32, campaign_epoch="c" * 64,
        expected_session_generation="route-one",
        current_identity_digest=main.vpcd_registry.current_identity_digest(
            topology_identity), deadline=deadline)
    events = []
    prepare = Mock(side_effect=lambda *_args, **_kwargs: (
        events.append("prepare") or {"status": "prepared", "record": prepared,
                                     "deadline": deadline}))
    begin = Mock(side_effect=lambda *_args, **_kwargs: (
        events.append("reserve") or route_reservation))
    validate = Mock(
        side_effect=lambda _reservation: events.append("validate") or reservation_valid)
    issue = Mock(side_effect=lambda *_args, **_kwargs: (
        events.append("permit") or {"status": "permit_issued", "record": permitted}))
    ami = SimpleNamespace(
        connected=True, _mgr=SimpleNamespace(protocol=object()),
        zero_usim_recovery_call_channels_complete=AsyncMock(return_value=True),
        registration_state=AsyncMock(return_value="Rejected"),
        submit_registration_permit=AsyncMock(
            side_effect=lambda _nonce: events.append("submit") or {"ok": True}),
    )

    main.hub.engine_recovery_locks.pop(iid, None)
    try:
        monkeypatch.setattr(main, "_durable_maintenance_pending", lambda _iid: False)
        monkeypatch.setattr(main.cfg, "get_instance", lambda _iid: inst)
        monkeypatch.setattr(main.engine, "usim_recovery_fence_pending", lambda _iid: True)
        monkeypatch.setattr(main.hub.runtime, "get", AsyncMock(return_value=runtime))
        monkeypatch.setattr(main.engine, "usim_status", lambda _iid: {})
        monkeypatch.setattr(
            main.status_mod, "current_local_usim_unavailable", lambda *_args: failure)
        monkeypatch.setattr(main.engine, "read_usim_recovery", lambda _iid: legacy)
        monkeypatch.setattr(main.engine, "reserve_usim_recovery_attempt", Mock(
            return_value={"status": "reserved", "record": legacy}))
        monkeypatch.setattr(main, "_remote_usim_recovery_topology", lambda _inst: topology)
        monkeypatch.setattr(main, "_same_remote_usim_recovery_topology", lambda *_args: True)
        monkeypatch.setattr(main, "_line_auto_start_allowed", lambda _inst: (True, ""))
        monkeypatch.setattr(main, "_usim_recovery_campaign_identity", lambda *_args: identity)
        monkeypatch.setattr(main, "_pcscf_rebind_pending", AsyncMock(return_value=False))
        monkeypatch.setattr(main.hub, "ami_for", AsyncMock(return_value=ami))
        monkeypatch.setattr(main.engine, "prepare_usim_recovery_campaign", prepare)
        monkeypatch.setattr(main.vpcd_registry, "begin_recovery_reservation", begin)
        monkeypatch.setattr(main.vpcd_registry, "validate_recovery_reservation", validate)
        monkeypatch.setattr(main.engine, "issue_usim_registration_permit", issue)
        monkeypatch.setattr(main.engine, "sync_usim_registration_dispatch", Mock(
            return_value={"status": "permit_issued", "record": permitted}))

        await main._reconcile_usim_auth_recovery(inst)
    finally:
        main.hub.engine_recovery_locks.pop(iid, None)

    assert events.index("prepare") < events.index("reserve") < events.index("validate")
    if reservation_valid:
        assert events.index("validate") < events.index("permit") < events.index("submit")
        assert validate.call_count >= 3
        assert issue.call_args.kwargs["deadline"] == route_reservation.deadline
    else:
        assert "permit" not in events and "submit" not in events
        issue.assert_not_called()


@pytest.mark.asyncio
@pytest.mark.parametrize("phase,finalize_calls", [("exhausted", 0), ("recovered", 1)])
async def test_offline_terminal_clears_route_after_engine_cas_then_finalizes_recovered(
        monkeypatch, phase, finalize_calls):
    iid, inst = "route-terminal", {"id": "route-terminal"}
    runtime = {"running": True, "container_id": "a" * 64,
               "started_at": "2026-08-27T00:00:00.000000000Z",
               "engine_run_id": "run-current"}
    record = {"version": 2, "phase": phase, "campaign_epoch": "c" * 64,
              "engine_run_id": "run-current", "auth_seq_baseline": 7,
              "permit_nonce": "f" * 32, "deadline": time.time() - 1}
    reservation = vpcd_slots.RecoveryReservation(
        slot=0, token="1" * 32, campaign_epoch="c" * 64,
        expected_session_generation="route-old", current_identity_digest="d" * 64,
        deadline=record["deadline"])
    events = []
    finalize = Mock(side_effect=lambda *_args, **_kwargs: (
        events.append("finalize") or {"status": "finalized", "terminal": True}))

    monkeypatch.setattr(main, "_durable_maintenance_pending", lambda _iid: False)
    monkeypatch.setattr(main.engine, "usim_recovery_fence_pending", lambda _iid: True)
    monkeypatch.setattr(main.hub.runtime, "get", AsyncMock(return_value=runtime))
    monkeypatch.setattr(main.engine, "usim_status", lambda _iid: {})
    monkeypatch.setattr(main.status_mod, "current_local_usim_unavailable", lambda *_args: None)
    monkeypatch.setattr(main.engine, "read_usim_recovery", lambda _iid: record)
    monkeypatch.setattr(main.engine, "expire_usim_recovery_deadline", Mock(
        side_effect=lambda *_args, **_kwargs: events.append("engine-terminal") or {
            "status": phase, "terminal": True, "record": record}))
    monkeypatch.setattr(main.vpcd_registry, "recovery_reservation_for_campaign", Mock(
        side_effect=lambda _campaign: events.append("lookup") or reservation))
    monkeypatch.setattr(main.vpcd_registry, "clear_recovery_reservation", Mock(
        side_effect=lambda _reservation: events.append("route-clear") or True))
    monkeypatch.setattr(main.engine, "finalize_usim_recovery_cleanup", finalize)
    topology = Mock(side_effect=AssertionError("terminal cleanup must not require online VPCD"))
    monkeypatch.setattr(main, "_remote_usim_recovery_topology", topology)

    await main._reconcile_usim_auth_recovery(inst)

    assert events[:3] == ["engine-terminal", "lookup", "route-clear"]
    assert finalize.call_count == finalize_calls
    if finalize_calls:
        assert events[-1] == "finalize"
    topology.assert_not_called()


@pytest.mark.asyncio
async def test_late_exhausted_auth_ok_resumes_only_exact_dispatched_campaign(monkeypatch):
    iid = "late-auth"
    inst = {"id": iid}
    runtime = {
        "running": True, "container_id": "a" * 64,
        "started_at": "2026-08-27T00:00:00.000000000Z",
        "engine_run_id": "run-current",
    }
    durable = {
        "version": 2, "phase": "exhausted", "campaign_epoch": "c" * 64,
        "stable_card_key": "eid:stable", "line_config_epoch": "d" * 64,
        "route_generation": "route-one", "sample_generation": "9" * 64,
        "container_id": runtime["container_id"], "started_at": runtime["started_at"],
        "engine_run_id": runtime["engine_run_id"], "auth_seq_baseline": 7,
        "permit_nonce": "f" * 32, "dispatch_count": 1,
        "dispatch_receipt_digest": "e" * 64, "result_auth_seq": 0,
        "rearm_ack": None, "deadline": time.time() - 1, "next_probe": 0.0,
        "cooldown": 0.0, "last_repair": "absolute_deadline_exhausted", "updated_at": 1000.0,
    }
    topology = ("e" * 64, {
        "slot": 0, "session_generation": "route-one", "eid": "stable",
        "iccid": "iccid-one", "matched": iid,
    })
    identity = {
        "exact_current": True, "campaign_epoch": "c" * 64,
        "stable_card_key": "eid:stable", "line_config_epoch": "d" * 64,
        "current_route_generation": "route-one", "sample_generation": "9" * 64,
        "container_id": runtime["container_id"], "started_at": runtime["started_at"],
        "engine_run_id": runtime["engine_run_id"], "auth_seq_baseline": 7,
    }
    reservation = vpcd_slots.RecoveryReservation(
        slot=0, token="1" * 32, campaign_epoch="c" * 64,
        expected_session_generation="route-one",
        current_identity_digest=main.vpcd_registry.current_identity_digest(topology[1]),
        deadline=time.time() + 30)
    ami = SimpleNamespace(
        connected=True, _mgr=SimpleNamespace(protocol=object()),
        zero_usim_recovery_call_channels_complete=AsyncMock(return_value=True),
    )
    route = Mock(return_value={"status": "exhausted", "record": durable})
    consume = Mock(return_value={
        "status": "recovered", "terminal": True,
        "record": {**durable, "phase": "recovered"},
    })
    expire = Mock(side_effect=AssertionError("late exact AUTH_OK must not expire again"))
    finish = AsyncMock(return_value=True)
    main.hub.engine_recovery_locks.pop(iid, None)
    try:
        monkeypatch.setattr(main, "_durable_maintenance_pending", lambda _iid: False)
        monkeypatch.setattr(main.cfg, "get_instance", lambda _iid: inst)
        monkeypatch.setattr(main.engine, "usim_recovery_fence_pending", lambda _iid: True)
        monkeypatch.setattr(main.hub.runtime, "get", AsyncMock(return_value=runtime))
        monkeypatch.setattr(main.engine, "usim_status", lambda _iid: {
            "state": "AUTH_OK", "auth_seq": 8, "engine_run_id": "run-current"})
        monkeypatch.setattr(main.status_mod, "current_local_usim_unavailable", lambda *_args: None)
        monkeypatch.setattr(main.engine, "read_usim_recovery", lambda _iid: durable)
        monkeypatch.setattr(main.engine, "expire_usim_recovery_deadline", expire)
        monkeypatch.setattr(main, "_remote_usim_recovery_topology", lambda _inst: topology)
        monkeypatch.setattr(main, "_same_remote_usim_recovery_topology", lambda *_args: True)
        monkeypatch.setattr(main, "_line_auto_start_allowed", lambda _inst: (True, ""))
        monkeypatch.setattr(main, "_usim_recovery_campaign_identity", lambda *_args: identity)
        monkeypatch.setattr(main, "_pcscf_rebind_pending", AsyncMock(return_value=False))
        monkeypatch.setattr(main.engine, "reconcile_usim_recovery_route", route)
        monkeypatch.setattr(main.vpcd_registry, "begin_recovery_reservation",
                            Mock(return_value=reservation))
        monkeypatch.setattr(main.vpcd_registry, "validate_recovery_reservation",
                            Mock(return_value=True))
        monkeypatch.setattr(main.hub, "ami_for", AsyncMock(return_value=ami))
        monkeypatch.setattr(main.engine, "consume_usim_recovery_auth_result", consume)
        monkeypatch.setattr(main, "_finish_usim_recovery_route_terminal", finish)

        await main._reconcile_usim_auth_recovery(inst)
    finally:
        main.hub.engine_recovery_locks.pop(iid, None)

    expire.assert_not_called()
    route.assert_called_once()
    consume.assert_called_once()
    finish.assert_called_once()


@pytest.mark.asyncio
async def test_fresh_config_change_aborts_before_campaign_prepare(monkeypatch):
    iid, inst = "route-config", {"id": "route-config", "apn": "old"}
    runtime = {"running": True, "container_id": "a" * 64,
               "started_at": "2026-08-27T00:00:00.000000000Z",
               "engine_run_id": "run-current"}
    failure = {"auth_seq": 7}
    legacy = {"version": 1, "phase": "pending", "auth_seq": 7}
    prepare = Mock()
    ami_for = AsyncMock()
    main.hub.engine_recovery_locks.pop(iid, None)
    try:
        monkeypatch.setattr(main, "_durable_maintenance_pending", lambda _iid: False)
        monkeypatch.setattr(main.cfg, "get_instance", lambda _iid: {**inst, "apn": "new"})
        monkeypatch.setattr(main.engine, "usim_recovery_fence_pending", lambda _iid: True)
        monkeypatch.setattr(main.hub.runtime, "get", AsyncMock(return_value=runtime))
        monkeypatch.setattr(main.engine, "usim_status", lambda _iid: {})
        monkeypatch.setattr(
            main.status_mod, "current_local_usim_unavailable", lambda *_args: failure)
        monkeypatch.setattr(main.engine, "read_usim_recovery", lambda _iid: legacy)
        monkeypatch.setattr(main.engine, "reserve_usim_recovery_attempt", Mock(
            return_value={"status": "reserved", "record": legacy}))
        topology = ("e" * 64, {"slot": 0, "session_generation": "route-one",
                               "eid": "stable", "iccid": "iccid-one"})
        monkeypatch.setattr(main, "_remote_usim_recovery_topology", lambda _inst: topology)
        monkeypatch.setattr(main, "_line_auto_start_allowed", lambda _inst: (True, ""))
        monkeypatch.setattr(main, "_usim_recovery_campaign_identity", Mock(
            return_value=campaign_identity(runtime)))
        monkeypatch.setattr(main.engine, "prepare_usim_recovery_campaign", prepare)
        monkeypatch.setattr(main.hub, "ami_for", ami_for)

        await main._reconcile_usim_auth_recovery(inst)
    finally:
        main.hub.engine_recovery_locks.pop(iid, None)

    prepare.assert_not_called()
    ami_for.assert_not_awaited()
