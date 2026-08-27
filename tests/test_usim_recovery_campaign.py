import json
import fcntl
from pathlib import Path
import threading
from concurrent.futures import ThreadPoolExecutor
from unittest.mock import Mock

import pytest

from control.app import engine, main
from engine import ami_usim


NEW_PHASES = {
    "pending", "permit_issued", "submitted_unknown", "exhausted",
    "recovered_pending_release", "recovered",
}


def v1(phase):
    return {
        "version": 1, "instance": "1", "container_id": "a" * 64,
        "started_at": "2026-08-27T00:00:00.000000000Z", "engine_run_id": "run-current",
        "auth_seq": 7, "cause_class": "pcsc_service_unavailable",
        "topology_digest": "b" * 64, "phase": phase, "attempts": 1,
        "next_attempt_at": 0.0, "updated_at": 1000.0,
        "submitted_at": 1000.0 if phase.startswith("submitted") else 0.0,
        "result_class": "old_unknown" if phase.startswith("submitted") else "",
    }


def current_evidence(exact=True):
    return {
        "exact_current": exact, "campaign_epoch": "c" * 64,
        "stable_card_key": "eid:test-card", "line_config_epoch": "d" * 64,
        "current_route_generation": "route-current", "sample_generation": "9" * 64,
        "container_id": "a" * 64,
        "started_at": "2026-08-27T00:00:00.000000000Z",
        "engine_run_id": "run-current",
        "auth_seq_baseline": 7,
    }


def test_control_campaign_identity_ignores_display_ports_and_exit_but_tracks_ims_owner():
    runtime = {
        "container_id": "a" * 64,
        "started_at": "2026-08-27T00:00:00.000000000Z",
        "engine_run_id": "run-current",
    }
    card = {"eid": "eid-a", "iccid": "iccid-a", "session_generation": "route-a"}
    inst = {
        "id": "1", "name": "display", "iccid": "iccid-a", "imsi": "imsi-a",
        "mcc": "234", "mnc": "010", "reader_index": 1, "apn": "ims",
        "proxy_country": "gb", "ports": {"webrtc": 8089},
        "sip": {"pani": "pani-a", "access_type": "wlan1"},
    }
    first = main._usim_recovery_campaign_identity(inst, runtime, card, 7)
    display_only = main._usim_recovery_campaign_identity({
        **inst, "name": "renamed", "proxy_country": "fr",
        "reader_index": 9, "reader_port": 46300,
        "ports": {"webrtc": 9000}, "retry": {"max": 99}}, runtime, card, 7)
    assert first == display_only
    assert first["stable_card_key"] == "eid:eid-a"
    changed = main._usim_recovery_campaign_identity({
        **inst, "apn": "carrier-ims"}, runtime, card, 7)
    assert changed["line_config_epoch"] != first["line_config_epoch"]
    assert changed["campaign_epoch"] != first["campaign_epoch"]
    new_route = main._usim_recovery_campaign_identity(
        inst, runtime, {**card, "session_generation": "route-b"}, 7)
    assert new_route["campaign_epoch"] == first["campaign_epoch"]
    assert new_route["current_route_generation"] == "route-b"


def seed_v2(root, phase="submitted_unknown"):
    record = {
        "version": 2, "instance": "1", "container_id": "a" * 64,
        "started_at": "2026-08-27T00:00:00.000000000Z", "engine_run_id": "run-current",
        "auth_seq": 7, "cause_class": "pcsc_service_unavailable",
        "topology_digest": "b" * 64, "phase": phase, "attempts": 1,
        "next_attempt_at": 0.0, "updated_at": 1000.0, "submitted_at": 1000.0,
        "result_class": "dispatch_recorded_send_unknown", "campaign_epoch": "c" * 64,
        "auth_seq_baseline": 7, "permit_nonce": "e" * 32, "dispatch_count": 1,
        "dispatch_receipt": {"permit_nonce": "e" * 32}, "result_auth_seq": 0,
        "rearm_ack": None,
    }
    (root / "usim-auth-recovery.json").write_text(json.dumps(record), encoding="utf-8")
    (root / "usim-registration-dispatch.json").write_text(json.dumps({
        "permit_nonce": "e" * 32, "dispatch_count": 1}), encoding="utf-8")
    return record


def write_v2(run, phase="submitted_unknown"):
    run.mkdir(parents=True, exist_ok=True)
    record = {
        "version": 2, "instance": "1", "phase": phase,
        "campaign_epoch": "c" * 64, "stable_card_key": "eid:test-card",
        "line_config_epoch": "d" * 64, "route_generation": "route-current",
        "sample_generation": "9" * 64, "container_id": "a" * 64,
        "started_at": "2026-08-27T00:00:00.000000000Z", "engine_run_id": "run-current",
        "auth_seq_baseline": 7, "permit_nonce": "e" * 32,
        "dispatch_count": 1, "dispatch_receipt_digest": "", "result_auth_seq": 0, "rearm_ack": None,
        "deadline": 4102444800.0, "next_probe": 1001.0, "cooldown": 0.0,
        "last_repair": "", "updated_at": 1000.0,
    }
    (run / "usim-auth-recovery.json").write_text(json.dumps(record), encoding="utf-8")
    receipt = {
        "version": 1, "campaign_epoch": record["campaign_epoch"],
        "permit_nonce": record["permit_nonce"], "engine_run_id": record["engine_run_id"],
        "dispatch_count": 1, "receipt_at": 1000.5,
    }
    (run / "usim-registration-dispatch.json").write_text(
        json.dumps(receipt), encoding="utf-8")
    raw = (run / "usim-registration-dispatch.json").read_text()
    record["dispatch_receipt_digest"] = __import__("hashlib").sha256(raw.encode()).hexdigest()
    (run / "usim-auth-recovery.json").write_text(json.dumps(record), encoding="utf-8")
    return record


def test_durable_phase_contract_contains_no_unproven_dispatched_state():
    assert engine._USIM_RECOVERY_PHASES == NEW_PHASES
    assert "dispatched" not in engine._USIM_RECOVERY_PHASES


@pytest.mark.parametrize("phase,exact,expected", [
    ("pending", True, "pending"),
    ("pending", False, "exhausted"),
    ("submitted", True, "exhausted"),
    ("submitted_unknown", True, "exhausted"),
])
def test_v1_migration_never_replays_an_ambiguous_submission(phase, exact, expected):
    migrated = engine.migrate_usim_recovery_v1(v1(phase), current_evidence(exact))
    assert migrated["version"] == 2 and migrated["phase"] == expected
    assert migrated["dispatch_count"] == 0
    assert migrated["auth_seq_baseline"] == 7
    assert migrated.get("permit_nonce") in (None, "")


def test_prepare_campaign_fsyncs_v2_deadline_before_route_reservation_and_expires_offline(
        tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    monkeypatch.setattr(engine.time, "time", lambda: 1000.0)
    run = tmp_path / "instances/1/run"
    run.mkdir(parents=True)
    legacy = v1("pending")
    (run / "usim-auth-recovery.json").write_text(json.dumps(legacy), encoding="utf-8")
    (run / "usim-auth-recovery.fence").write_text(json.dumps({
        "version": 1, "engine_run_id": "run-current", "auth_seq": 7,
        "cause_class": "pcsc_service_unavailable", "created_at": 999.0,
    }), encoding="utf-8")

    prepared = engine.prepare_usim_recovery_campaign(
        "1", current_evidence(), topology_digest="b" * 64, deadline=1100.0)

    assert prepared["status"] == "prepared" and prepared["deadline"] == 1100.0
    record = engine.read_usim_recovery("1")
    assert record["version"] == 2 and record["phase"] == "pending"
    assert record["deadline"] == 1100.0 and record["last_repair"] == "campaign_prepared"
    assert not (run / "usim-registration-permit.json").exists()
    assert not (run / "usim-registration-dispatch.json").exists()
    before = (run / "usim-auth-recovery.json").read_bytes()
    repeated = engine.prepare_usim_recovery_campaign(
        "1", current_evidence(), topology_digest="b" * 64, deadline=1100.0)
    assert repeated["status"] == "prepared"
    assert (run / "usim-auth-recovery.json").read_bytes() == before

    expired = engine.expire_usim_recovery_deadline(
        "1", campaign_epoch="c" * 64, engine_run_id="run-current",
        auth_seq_baseline=7, now=1100.0)
    assert expired["status"] == "exhausted" and expired["terminal"] is True
    assert (run / "usim-auth-recovery.fence").exists()


def test_prepare_campaign_never_migrates_unproven_legacy_route(tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    monkeypatch.setattr(engine.time, "time", lambda: 1000.0)
    run = tmp_path / "instances/1/run"
    run.mkdir(parents=True)
    (run / "usim-auth-recovery.json").write_text(json.dumps(v1("pending")), encoding="utf-8")
    (run / "usim-auth-recovery.fence").write_text(json.dumps({
        "version": 1, "engine_run_id": "run-current", "auth_seq": 7,
        "cause_class": "pcsc_service_unavailable", "created_at": 999.0,
    }), encoding="utf-8")

    result = engine.prepare_usim_recovery_campaign(
        "1", current_evidence(), topology_digest="e" * 64, deadline=1100.0)

    assert result["status"] == "legacy_route_unproven"
    record = engine.read_usim_recovery("1")
    assert record["version"] == 1 and record["phase"] == "exhausted"
    assert record["topology_digest"] == "b" * 64


def test_permit_publish_is_cas_bound_to_prepared_campaign_and_absolute_deadline(
        tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    monkeypatch.setattr(engine.time, "time", lambda: 1000.0)
    run = tmp_path / "instances/1/run"
    run.mkdir(parents=True)
    (run / "usim-auth-recovery.json").write_text(json.dumps(v1("pending")), encoding="utf-8")
    (run / "usim-auth-recovery.fence").write_text(json.dumps({
        "version": 1, "engine_run_id": "run-current", "auth_seq": 7,
        "cause_class": "pcsc_service_unavailable", "created_at": 999.0,
    }), encoding="utf-8")
    identity = current_evidence()
    unprepared = engine.issue_usim_registration_permit(
        "1", container_id="a" * 64,
        started_at="2026-08-27T00:00:00.000000000Z",
        engine_run_id="run-current", auth_seq_baseline=7,
        campaign_identity=identity, deadline=1100.0)
    assert unprepared["status"] == "campaign_not_prepared"
    assert not (run / "usim-registration-permit.json").exists()
    engine.prepare_usim_recovery_campaign(
        "1", identity, topology_digest="b" * 64, deadline=1100.0)

    stale = engine.issue_usim_registration_permit(
        "1", container_id="a" * 64,
        started_at="2026-08-27T00:00:00.000000000Z",
        engine_run_id="run-current", auth_seq_baseline=7,
        campaign_identity=identity, deadline=1101.0)
    assert stale["status"] == "stale_identity"
    assert not (run / "usim-registration-permit.json").exists()

    issued = engine.issue_usim_registration_permit(
        "1", container_id="a" * 64,
        started_at="2026-08-27T00:00:00.000000000Z",
        engine_run_id="run-current", auth_seq_baseline=7,
        campaign_identity=identity, deadline=1100.0)
    assert issued["status"] == "permit_issued"
    assert issued["record"]["deadline"] == 1100.0
    assert (run / "usim-registration-permit.json").is_file()


def test_v1_pending_route_change_is_exhausted_because_card_campaign_was_not_persisted(
        tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    run.mkdir(parents=True)
    original = v1("pending")
    path = run / "usim-auth-recovery.json"
    path.write_text(json.dumps(original), encoding="utf-8")
    identity = current_evidence()
    identity["sample_generation"] = "9" * 64

    missing = engine.reconcile_usim_recovery_route("1", identity)
    assert missing["status"] == "stale_identity"
    assert engine.read_usim_recovery("1") == original

    exhausted = engine.reconcile_usim_recovery_route(
        "1", identity, topology_digest="e" * 64)
    assert exhausted["status"] == "legacy_route_unproven"
    assert exhausted["record"]["version"] == 1
    assert exhausted["record"]["phase"] == "exhausted"
    assert exhausted["record"]["topology_digest"] == original["topology_digest"]
    assert exhausted["record"]["result_class"] == "legacy_route_unproven"
    assert not (run / "usim-registration-permit.json").exists()
    assert not (run / "usim-registration-dispatch.json").exists()


@pytest.mark.parametrize("name", [
    "usim-auth-recovery.json", "usim-registration-permit.json",
    "usim-registration-dispatch.json",
])
def test_nonrecovered_record_or_debris_is_an_engine_start_fence(tmp_path, monkeypatch, name):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    run.mkdir(parents=True)
    (run / name).write_text("{}", encoding="utf-8")
    assert engine.usim_recovery_fence_pending("1") is True


def test_auth_ok_publication_never_clears_fence_before_control_consumes_result(tmp_path, monkeypatch):
    monkeypatch.setattr(ami_usim, "RUNDIR", str(tmp_path))
    monkeypatch.setattr(ami_usim, "ENGINE_RUN_ID", "run-current")
    (tmp_path / "engine-run-id").write_text("run-current\n", encoding="utf-8")
    fence = {
        "version": 1, "engine_run_id": "run-current", "auth_seq": 7,
        "cause_class": "pcsc_service_unavailable", "created_at": 1000.0,
    }
    path = tmp_path / ami_usim.USIM_RECOVERY_FENCE_NAME
    path.write_text(json.dumps(fence), encoding="utf-8")
    assert ami_usim.write_status(state="AUTH_OK", auth_seq=8) is True
    assert path.exists(), "AUTH_OK must remain evidence; only Control may release the fence"


def test_auth_sync_or_baseline_auth_ok_cannot_complete_campaign(tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    run.mkdir(parents=True)
    rearm = Mock()
    for status in ({"state": "AUTH_SYNC", "auth_seq": 8},
                   {"state": "AUTH_OK", "auth_seq": 7}):
        write_v2(run)
        result = engine.consume_usim_recovery_auth_result(
            "1", campaign_epoch="c" * 64, permit_nonce="e" * 32,
            current_identity=current_evidence(), auth_status=status,
            rearm_timer=rearm)
        assert result["status"] == "waiting"
    rearm.assert_not_called()


@pytest.mark.parametrize("phase", ["submitted_unknown", "recovered_pending_release"])
def test_nonfresh_auth_result_exhausts_at_fixed_campaign_deadline(
        tmp_path, monkeypatch, phase):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    monkeypatch.setattr(engine.time, "time", lambda: 1200.0)
    run = tmp_path / "instances/1/run"
    run.mkdir(parents=True)
    record = write_v2(run, phase)
    record["deadline"] = 1100.0
    if phase == "recovered_pending_release":
        record.update(result_auth_seq=8,
                      rearm_ack={"timer_id": "timer-one", "sent_register": False})
    (run / "usim-auth-recovery.json").write_text(json.dumps(record), encoding="utf-8")
    before_receipt = (run / "usim-registration-dispatch.json").read_bytes()
    rearm = Mock()

    result = engine.consume_usim_recovery_auth_result(
        "1", campaign_epoch="c" * 64, permit_nonce="e" * 32,
        current_identity=current_evidence(),
        auth_status={"state": "AUTH_UNAVAILABLE", "auth_seq": 8},
        rearm_timer=rearm)

    assert result["status"] == "exhausted"
    assert engine.read_usim_recovery("1")["phase"] == "exhausted"
    assert (run / "usim-registration-dispatch.json").read_bytes() == before_receipt
    rearm.assert_not_called()


def test_nonfresh_auth_result_waits_before_fixed_campaign_deadline(tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    monkeypatch.setattr(engine.time, "time", lambda: 1000.0)
    run = tmp_path / "instances/1/run"
    run.mkdir(parents=True)
    write_v2(run)

    result = engine.consume_usim_recovery_auth_result(
        "1", campaign_epoch="c" * 64, permit_nonce="e" * 32,
        current_identity=current_evidence(),
        auth_status={"state": "AUTH_SYNC", "auth_seq": 8},
        rearm_timer=Mock())

    assert result["status"] == "waiting"
    assert engine.read_usim_recovery("1")["phase"] == "submitted_unknown"


@pytest.mark.parametrize("phase", [
    "pending", "permit_issued", "submitted_unknown", "recovered_pending_release",
])
def test_absolute_deadline_api_exhausts_every_preterminal_v2_phase_and_keeps_artifacts(
        tmp_path, monkeypatch, phase):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    run.mkdir(parents=True)
    record = write_v2(run, phase)
    record["deadline"] = 1100.0
    if phase == "recovered_pending_release":
        record.update(result_auth_seq=8,
                      rearm_ack={"timer_id": "timer-one", "sent_register": False})
    (run / "usim-auth-recovery.json").write_text(json.dumps(record), encoding="utf-8")
    fence = run / "usim-auth-recovery.fence"
    fence.write_bytes(b"exact-fence-evidence")
    permit = run / "usim-registration-permit.json"
    permit.write_bytes(b'{"preserve":"permit"}')
    dispatch = run / "usim-registration-dispatch.json"
    dispatch_before = dispatch.read_bytes()

    result = engine.expire_usim_recovery_deadline(
        "1", campaign_epoch="c" * 64, engine_run_id="run-current",
        auth_seq_baseline=7, now=1200.0)

    assert result["status"] == "exhausted"
    assert result["terminal"] is True and result["transitioned"] is True
    assert engine.read_usim_recovery("1")["phase"] == "exhausted"
    assert fence.read_bytes() == b"exact-fence-evidence"
    assert permit.read_bytes() == b'{"preserve":"permit"}'
    assert dispatch.read_bytes() == dispatch_before
    repeated = engine.expire_usim_recovery_deadline(
        "1", campaign_epoch="c" * 64, engine_run_id="run-current",
        auth_seq_baseline=7, now=1201.0)
    assert repeated["status"] == "exhausted"
    assert repeated["terminal"] is True and repeated["transitioned"] is False


@pytest.mark.parametrize("change", ["campaign", "run", "auth"])
def test_absolute_deadline_api_wrong_identity_never_changes_campaign(
        tmp_path, monkeypatch, change):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    run.mkdir(parents=True)
    record = write_v2(run)
    record["deadline"] = 1100.0
    path = run / "usim-auth-recovery.json"
    path.write_text(json.dumps(record), encoding="utf-8")
    before = path.read_bytes()
    values = {"campaign_epoch": "c" * 64, "engine_run_id": "run-current",
              "auth_seq_baseline": 7}
    if change == "campaign": values["campaign_epoch"] = "f" * 64
    if change == "run": values["engine_run_id"] = "run-other"
    if change == "auth": values["auth_seq_baseline"] = 8

    result = engine.expire_usim_recovery_deadline("1", now=1200.0, **values)

    assert result["status"] == "stale_identity" and result["terminal"] is False
    assert path.read_bytes() == before


def test_absolute_deadline_api_waits_without_sliding_or_writing(tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    run.mkdir(parents=True)
    record = write_v2(run, "permit_issued")
    record["deadline"] = 1100.0
    path = run / "usim-auth-recovery.json"
    path.write_text(json.dumps(record), encoding="utf-8")
    before = path.read_bytes()

    result = engine.expire_usim_recovery_deadline(
        "1", campaign_epoch="c" * 64, engine_run_id="run-current",
        auth_seq_baseline=7, now=1099.0)

    assert result["status"] == "waiting" and result["terminal"] is False
    assert path.read_bytes() == before


def test_absolute_deadline_cas_serializes_behind_dispatch_consumer(tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    run.mkdir(parents=True)
    record = write_v2(run, "permit_issued")
    record["deadline"] = 1100.0
    (run / "usim-auth-recovery.json").write_text(json.dumps(record), encoding="utf-8")
    lock_path = run / ".usim-registration-dispatch.lock"
    started = threading.Event()

    def expire():
        started.set()
        return engine.expire_usim_recovery_deadline(
            "1", campaign_epoch="c" * 64, engine_run_id="run-current",
            auth_seq_baseline=7, now=1200.0)

    with lock_path.open("a+") as held, ThreadPoolExecutor(max_workers=1) as pool:
        fcntl.flock(held.fileno(), fcntl.LOCK_EX)
        future = pool.submit(expire)
        assert started.wait(1) and not future.done()
        fcntl.flock(held.fileno(), fcntl.LOCK_UN)
        assert future.result(timeout=1)["status"] == "exhausted"


def test_late_auth_ok_is_exhausted_before_rearm_or_fence_clear(tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    monkeypatch.setattr(engine.time, "time", lambda: 1200.0)
    run = tmp_path / "instances/1/run"
    run.mkdir(parents=True)
    record = write_v2(run)
    record["deadline"] = 1100.0
    (run / "usim-auth-recovery.json").write_text(json.dumps(record), encoding="utf-8")
    rearm = Mock()

    result = engine.consume_usim_recovery_auth_result(
        "1", campaign_epoch="c" * 64, permit_nonce="e" * 32,
        current_identity=current_evidence(),
        auth_status={"state": "AUTH_OK", "auth_seq": 8},
        rearm_timer=rearm)

    assert result["status"] == "exhausted" and result["terminal"] is True
    rearm.assert_not_called()


def test_late_auth_ok_after_absolute_deadline_resumes_exact_dispatch(tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    monkeypatch.setattr(engine.time, "time", lambda: 1200.0)
    run = tmp_path / "instances/1/run"
    record = write_v2(run, "exhausted")
    record.update(last_repair="absolute_deadline_exhausted", deadline=1100.0)
    (run / "usim-auth-recovery.json").write_text(json.dumps(record), encoding="utf-8")
    rearm = Mock(return_value={"ok": True, "timer_id": "timer-one", "sent_register": False})

    result = engine.consume_usim_recovery_auth_result(
        "1", campaign_epoch="c" * 64, permit_nonce="e" * 32,
        current_identity=current_evidence(),
        auth_status={"state": "AUTH_OK", "auth_seq": 8,
                     "engine_run_id": "run-current"},
        rearm_timer=rearm)

    assert result["status"] == "recovered" and result["terminal"] is True
    assert result["record"]["phase"] == "recovered"
    rearm.assert_called_once()


def test_deadline_crossing_during_rearm_cannot_publish_recovered(tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    clock = iter((1000.0, 1000.0, 1200.0))
    monkeypatch.setattr(engine.time, "time", lambda: next(clock))
    run = tmp_path / "instances/1/run"
    run.mkdir(parents=True)
    record = write_v2(run)
    record["deadline"] = 1100.0
    (run / "usim-auth-recovery.json").write_text(json.dumps(record), encoding="utf-8")
    rearm = Mock(return_value={
        "ok": True, "timer_id": "timer-one", "sent_register": False})

    result = engine.consume_usim_recovery_auth_result(
        "1", campaign_epoch="c" * 64, permit_nonce="e" * 32,
        current_identity=current_evidence(),
        auth_status={"state": "AUTH_OK", "auth_seq": 8},
        rearm_timer=rearm)

    assert result["status"] == "exhausted" and result["terminal"] is True
    assert engine.read_usim_recovery("1")["phase"] == "exhausted"
    rearm.assert_called_once()


def test_rearm_ack_and_recovered_are_durable_before_fence_release(tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    write_v2(run)
    fence = run / "usim-auth-recovery.fence"
    fence.write_text("fenced", encoding="utf-8")
    order = []
    def rearm():
        assert fence.exists()
        order.append("rearm")
        return {"ok": True, "timer_id": "timer-one", "sent_register": False}
    result = engine.consume_usim_recovery_auth_result(
        "1", campaign_epoch="c" * 64, permit_nonce="e" * 32,
        current_identity=current_evidence(), auth_status={"state": "AUTH_OK", "auth_seq": 8},
        rearm_timer=rearm)
    assert result["status"] == "recovered" and result["terminal"] is True
    assert result["cleanup_required"] is True and order == ["rearm"]
    assert fence.exists()
    order.append("registry-clear")
    finalized = engine.finalize_usim_recovery_cleanup(
        "1", campaign_epoch="c" * 64, permit_nonce="e" * 32)
    order.append("engine-cleanup")
    assert finalized == {"status": "finalized", "terminal": True}
    assert order == ["rearm", "registry-clear", "engine-cleanup"]
    assert not fence.exists()


def test_recovered_plus_fence_retries_cleanup_without_rearming_or_exhausting(tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    record = write_v2(run, "recovered")
    record.update(rearm_ack={"timer_id": "timer-one"}, result_auth_seq=8)
    (run / "usim-auth-recovery.json").write_text(json.dumps(record), encoding="utf-8")
    (run / "usim-auth-recovery.fence").write_text("fenced", encoding="utf-8")
    rearm = Mock()
    result = engine.consume_usim_recovery_auth_result(
        "1", campaign_epoch="c" * 64, permit_nonce="e" * 32,
        current_identity=current_evidence(), auth_status={"state": "AUTH_OK", "auth_seq": 8},
        rearm_timer=rearm)
    assert result["status"] == "recovered" and result["terminal"] is True
    assert result["cleanup_required"] is True
    rearm.assert_not_called()
    assert (run / "usim-auth-recovery.fence").exists()
    assert engine.finalize_usim_recovery_cleanup(
        "1", campaign_epoch="c" * 64,
        permit_nonce="e" * 32)["status"] == "finalized"


@pytest.mark.parametrize("change", [None, "phase", "campaign", "nonce"])
def test_public_fence_clear_requires_exact_recovered_rearmed_campaign(tmp_path, monkeypatch, change):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"; run.mkdir(parents=True)
    record = write_v2(run, "recovered")
    record.update(rearm_ack={"timer_id": "timer-one", "sent_register": False},
                  result_auth_seq=8)
    if change == "phase": record["phase"] = "recovered_pending_release"
    if change == "campaign": record["campaign_epoch"] = "f" * 64
    if change == "nonce": record["permit_nonce"] = "a" * 32
    (run / "usim-auth-recovery.json").write_text(json.dumps(record), encoding="utf-8")
    fence = run / "usim-auth-recovery.fence"; fence.write_text("fenced", encoding="utf-8")
    finalized = engine.finalize_usim_recovery_cleanup(
        "1", campaign_epoch="c" * 64, permit_nonce="e" * 32)
    assert finalized["status"] == ("finalized" if change is None else "stale_identity")
    assert finalized["terminal"] is (change is None)
    assert fence.exists() is (change is not None)
    if change is None:
        assert not (run / "usim-registration-dispatch.json").exists()
        assert not (run / "usim-auth-recovery.json").exists()
        assert (tmp_path / "orchestrator/usim-recovery-history" /
                f"1-{'c' * 64}.json").is_file()
        assert engine.usim_recovery_fence_pending("1") is False


@pytest.mark.parametrize("paid,channels,allowed", [
    ({"open_call_leases": 0, "pending_messages": 0, "pending_allowance_queries": 0}, True, True),
    ({"open_call_leases": 1, "pending_messages": 0, "pending_allowance_queries": 0}, True, False),
    ({"open_call_leases": 0, "pending_messages": 0, "pending_allowance_queries": 0}, False, False),
])
def test_containment_fences_before_paid_channel_recheck_and_restart_authority(
        tmp_path, monkeypatch, paid, channels, allowed):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"; run.mkdir(parents=True)
    legacy = v1("pending")
    (run / "usim-auth-recovery.json").write_text(json.dumps(legacy), encoding="utf-8")
    events = []
    def publish():
        (run / "usim-auth-recovery.fence").write_text("fenced", encoding="utf-8")
        events.append("fence")
        return True
    monkeypatch.setattr(engine, "_acquire_usim_recovery_admission",
                        lambda _iid: events.append("pcscf") or object())
    monkeypatch.setattr(engine, "release_pcscf_admission", lambda _handle: events.append("release"))
    context = engine.usim_recovery_containment_boundary(
        "1", publish_fence=publish,
        pending_paid=lambda: events.append("paid") or paid,
        zero_channels=lambda: events.append("channels") or channels,
        expected_recovery_identity={
            "engine_run_id": legacy["engine_run_id"],
            "auth_seq_baseline": legacy["auth_seq"], "campaign_epoch": ""})
    if allowed:
        with context:
            events.append("restart-authorized")
        assert events == ["fence", "pcscf", "paid", "channels", "restart-authorized", "release"]
    else:
        with pytest.raises(engine.UsimRecoveryStateError):
            with context:
                events.append("restart-authorized")
        assert "restart-authorized" not in events
