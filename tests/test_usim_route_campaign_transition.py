import json
from types import SimpleNamespace
from unittest.mock import AsyncMock, Mock

import pytest

from control.app import engine, main, vpcd_slots


def _identity(route="route-new", sample="9" * 64):
    return {
        "exact_current": True,
        "campaign_epoch": "c" * 64,
        "stable_card_key": "eid:stable-card",
        "line_config_epoch": "d" * 64,
        "current_route_generation": route,
        "sample_generation": sample,
        "container_id": "a" * 64,
        "started_at": "2026-08-27T00:00:00.000000000Z",
        "engine_run_id": "run-current",
        "auth_seq_baseline": 7,
    }


def _write_record(root, *, phase="pending", dispatch_count=0, permit_nonce=None):
    run = root / "instances" / "1" / "run"
    run.mkdir(parents=True)
    record = {
        "version": 2,
        "instance": "1",
        "phase": phase,
        "campaign_epoch": "c" * 64,
        "stable_card_key": "eid:stable-card",
        "line_config_epoch": "d" * 64,
        "route_generation": "route-old",
        "sample_generation": "sample-old",
        "container_id": "a" * 64,
        "started_at": "2026-08-27T00:00:00.000000000Z",
        "engine_run_id": "run-current",
        "auth_seq_baseline": 7,
        "permit_nonce": permit_nonce,
        "dispatch_count": dispatch_count,
        "dispatch_receipt_digest": "",
        "result_auth_seq": 0,
        "rearm_ack": None,
        "deadline": 0.0,
        "next_probe": 0.0,
        "cooldown": 0.0,
        "last_repair": "",
        "updated_at": 1000.0,
    }
    (run / "usim-auth-recovery.json").write_text(json.dumps(record), encoding="utf-8")
    return run


def test_pending_same_campaign_can_follow_a_new_unspent_route(tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    _write_record(tmp_path)

    result = engine.reconcile_usim_recovery_route(
        "1", _identity(), topology_digest="e" * 64)

    assert result["status"] == "route_updated"
    record = engine.read_usim_recovery("1")
    assert record["phase"] == "pending"
    assert record["campaign_epoch"] == "c" * 64
    assert record["route_generation"] == "route-new"
    assert record["sample_generation"] == "9" * 64
    assert record["dispatch_count"] == 0
    assert record["permit_nonce"] is None


def test_route_change_after_permit_is_exhausted_without_replay(tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    nonce = "f" * 32
    run = _write_record(
        tmp_path, phase="permit_issued", dispatch_count=0, permit_nonce=nonce)
    (run / "usim-registration-permit.json").write_text(
        json.dumps({"permit_nonce": nonce}), encoding="utf-8")

    result = engine.reconcile_usim_recovery_route(
        "1", _identity(), topology_digest="e" * 64)

    assert result["status"] == "route_changed_after_permit"
    record = engine.read_usim_recovery("1")
    assert record["phase"] == "exhausted"
    assert record["route_generation"] == "route-old"
    assert record["dispatch_count"] == 0
    assert record["permit_nonce"] == nonce


def test_pending_record_with_dispatch_debris_is_exhausted_without_replay(
        tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = _write_record(tmp_path)
    (run / "usim-registration-dispatch.json").write_text(
        json.dumps({"untrusted_partial_receipt": True}), encoding="utf-8")

    result = engine.reconcile_usim_recovery_route(
        "1", _identity(), topology_digest="e" * 64)

    assert result["status"] == "route_changed_after_permit"
    record = engine.read_usim_recovery("1")
    assert record["phase"] == "exhausted"
    assert record["route_generation"] == "route-old"
    assert record["dispatch_count"] == 0


def test_legacy_pending_route_change_is_exhausted_without_guessing_same_card(
        tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances" / "1" / "run"
    run.mkdir(parents=True)
    legacy = {
        "version": 1, "instance": "1", "container_id": "a" * 64,
        "started_at": "2026-08-27T00:00:00.000000000Z",
        "engine_run_id": "run-current", "auth_seq": 7,
        "cause_class": "pcsc_service_unavailable", "topology_digest": "b" * 64,
        "phase": "pending", "attempts": 0, "next_attempt_at": 1001.0,
        "updated_at": 1000.0, "submitted_at": 0.0, "result_class": "",
    }
    (run / "usim-auth-recovery.json").write_text(json.dumps(legacy), encoding="utf-8")

    result = engine.reconcile_usim_recovery_route(
        "1", _identity(), topology_digest="e" * 64)

    assert result["status"] == "legacy_route_unproven"
    record = engine.read_usim_recovery("1")
    assert record["version"] == 1
    assert record["phase"] == "exhausted"
    assert record["attempts"] == 0
    assert record["topology_digest"] == "b" * 64


@pytest.mark.asyncio
@pytest.mark.parametrize("route_status,has_failure,ami_calls", [
    ("route_updated", True, 1),
    ("route_updated", False, 0),
    ("unchanged", True, 1),
    ("unchanged", False, 0),
    ("route_changed_after_permit", True, 0),
    ("exhausted", True, 0),
])
async def test_control_reconciles_v2_route_under_line_lock_before_ami(
        monkeypatch, route_status, has_failure, ami_calls):
    iid = "route-wire"
    runtime = {
        "running": True,
        "container_id": "a" * 64,
        "started_at": "2026-08-27T00:00:00.000000000Z",
        "engine_run_id": "run-current",
    }
    identity = _identity()
    topology = ("e" * 64, {
        "slot": 0,
        "eid": "stable-card", "iccid": "iccid-current",
        "session_generation": "route-new",
    })
    deadline = __import__("time").time() + 100
    durable = {
        "version": 2, "phase": "pending", "auth_seq_baseline": 7,
        "campaign_epoch": "c" * 64, "engine_run_id": "run-current",
        "container_id": "a" * 64,
        "started_at": "2026-08-27T00:00:00.000000000Z", "deadline": deadline,
    }
    failure = {
        "engine_run_id": "run-current", "auth_seq": 7,
        "cause_class": "pcsc_service_unavailable", "ts": 1000.0,
    }
    route_record = {**durable, "phase": (
        "pending" if route_status in {"route_updated", "unchanged"} else "exhausted")}
    permit_record = {
        **route_record, "phase": "permit_issued", "permit_nonce": "f" * 32,
    }
    ami = SimpleNamespace(
        connected=True, _mgr=SimpleNamespace(protocol=object()),
        zero_usim_recovery_call_channels_complete=AsyncMock(return_value=True),
        registration_state=AsyncMock(return_value="Rejected"),
        submit_registration_permit=AsyncMock(return_value={"ok": True}),
    )
    ami_for = AsyncMock(return_value=ami)
    reconcile = Mock(return_value={"status": route_status, "record": route_record})
    issue = Mock(return_value={"status": "permit_issued", "record": permit_record})
    route_reservation = vpcd_slots.RecoveryReservation(
        slot=0, token="1" * 32, campaign_epoch="c" * 64,
        expected_session_generation="route-new",
        current_identity_digest=main.vpcd_registry.current_identity_digest(topology[1]),
        deadline=deadline)

    main.hub.engine_recovery_locks.pop(iid, None)
    try:
        monkeypatch.setattr(main, "_durable_maintenance_pending", lambda _iid: False)
        monkeypatch.setattr(main.cfg, "get_instance", lambda _iid: {"id": iid})
        monkeypatch.setattr(main.engine, "usim_recovery_fence_pending", lambda _iid: True)
        monkeypatch.setattr(main.hub.runtime, "get", AsyncMock(return_value=runtime))
        monkeypatch.setattr(main.engine, "usim_status", lambda _iid: {})
        monkeypatch.setattr(
            main.status_mod, "current_local_usim_unavailable",
            lambda *_args: failure if has_failure else None)
        monkeypatch.setattr(main.engine, "read_usim_recovery", lambda _iid: durable)
        monkeypatch.setattr(main.engine, "expire_usim_recovery_deadline", Mock(
            return_value={"status": "waiting", "terminal": False, "record": durable}))
        monkeypatch.setattr(main, "_remote_usim_recovery_topology", lambda _inst: topology)
        monkeypatch.setattr(main, "_same_remote_usim_recovery_topology", lambda *_args: True)
        monkeypatch.setattr(main, "_line_auto_start_allowed", lambda _inst: (True, ""))
        monkeypatch.setattr(main, "_usim_recovery_campaign_identity", lambda *_args: identity)
        monkeypatch.setattr(main, "_pcscf_rebind_pending", AsyncMock(return_value=False))
        monkeypatch.setattr(main.engine, "reconcile_usim_recovery_route", reconcile)
        monkeypatch.setattr(main.engine, "issue_usim_registration_permit", issue)
        monkeypatch.setattr(main.engine, "sync_usim_registration_dispatch", Mock(
            return_value={"status": "permit_issued", "record": permit_record}))
        monkeypatch.setattr(
            main.vpcd_registry, "begin_recovery_reservation",
            Mock(return_value=route_reservation))
        monkeypatch.setattr(
            main.vpcd_registry, "validate_recovery_reservation", Mock(return_value=True))
        monkeypatch.setattr(
            main.vpcd_registry, "recovery_reservation_for_campaign", Mock(return_value=None))
        monkeypatch.setattr(main.hub, "ami_for", ami_for)

        await main._reconcile_usim_auth_recovery({"id": iid})
    finally:
        main.hub.engine_recovery_locks.pop(iid, None)

    reconcile.assert_called_once_with(
        iid, identity, topology_digest="e" * 64)
    assert ami_for.await_count == ami_calls
    assert issue.call_count == ami_calls


@pytest.mark.asyncio
async def test_control_exhausts_unproven_legacy_route_without_ami_or_replay(monkeypatch):
    iid = "legacy-route-wire"
    runtime = {
        "running": True, "container_id": "a" * 64,
        "started_at": "2026-08-27T00:00:00.000000000Z",
        "engine_run_id": "run-current",
    }
    failure = {
        "engine_run_id": "run-current", "auth_seq": 7,
        "cause_class": "pcsc_service_unavailable", "ts": 1000.0,
    }
    legacy = {
        "version": 1, "phase": "pending", "auth_seq": 7,
        "container_id": runtime["container_id"], "started_at": runtime["started_at"],
        "engine_run_id": runtime["engine_run_id"],
    }
    identity = _identity()
    topology = ("e" * 64, {
        "slot": 0,
        "eid": "stable-card", "iccid": "iccid-current",
        "session_generation": "route-new",
    })
    reconcile = Mock(return_value={
        "status": "legacy_route_unproven", "record": {"phase": "exhausted"}})
    ami_for = AsyncMock()

    main.hub.engine_recovery_locks.pop(iid, None)
    try:
        monkeypatch.setattr(main, "_durable_maintenance_pending", lambda _iid: False)
        monkeypatch.setattr(main.cfg, "get_instance", lambda _iid: {"id": iid})
        monkeypatch.setattr(main.engine, "usim_recovery_fence_pending", lambda _iid: True)
        monkeypatch.setattr(main.hub.runtime, "get", AsyncMock(return_value=runtime))
        monkeypatch.setattr(main.engine, "usim_status", lambda _iid: {})
        monkeypatch.setattr(
            main.status_mod, "current_local_usim_unavailable", lambda *_args: failure)
        monkeypatch.setattr(main.engine, "read_usim_recovery", lambda _iid: legacy)
        monkeypatch.setattr(main.engine, "reserve_usim_recovery_attempt", Mock(
            return_value={"status": "topology_changed", "record": legacy}))
        monkeypatch.setattr(main, "_remote_usim_recovery_topology", lambda _inst: topology)
        monkeypatch.setattr(main, "_same_remote_usim_recovery_topology", lambda *_args: True)
        monkeypatch.setattr(main, "_line_auto_start_allowed", lambda _inst: (True, ""))
        monkeypatch.setattr(main, "_usim_recovery_campaign_identity", lambda *_args: identity)
        monkeypatch.setattr(main, "_pcscf_rebind_pending", AsyncMock(return_value=False))
        monkeypatch.setattr(main.engine, "reconcile_usim_recovery_route", reconcile)
        monkeypatch.setattr(main.hub, "ami_for", ami_for)

        await main._reconcile_usim_auth_recovery({"id": iid})
    finally:
        main.hub.engine_recovery_locks.pop(iid, None)

    reconcile.assert_called_once_with(
        iid, identity, topology_digest="e" * 64)
    ami_for.assert_not_awaited()
