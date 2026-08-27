import json

import pytest

from control.app import engine


def write_exhausted(run, **overrides):
    run.mkdir(parents=True, exist_ok=True)
    record = {
        "version": 2, "instance": "1", "phase": "exhausted",
        "campaign_epoch": "c" * 64, "stable_card_key": "eid:test-card",
        "line_config_epoch": "d" * 64, "route_generation": "route-current",
        "sample_generation": "9" * 64, "container_id": "a" * 64,
        "started_at": "2026-08-27T00:00:00.000000000Z", "engine_run_id": "run-old",
        "auth_seq_baseline": 7, "permit_nonce": "e" * 32,
        "dispatch_count": 1, "dispatch_receipt_digest": "", "result_auth_seq": 0,
        "rearm_ack": None, "deadline": 1000.0, "next_probe": 1000.0, "cooldown": 0.0,
        "last_repair": "exhausted_after_retries", "updated_at": 1000.0,
    }
    record.update(overrides)
    (run / "usim-auth-recovery.json").write_text(json.dumps(record), encoding="utf-8")
    (run / "usim-auth-recovery.fence").write_text(json.dumps({
        "version": 1, "engine_run_id": record["engine_run_id"], "auth_seq": 7,
        "cause_class": "pcsc_service_unavailable", "created_at": 999.0,
    }), encoding="utf-8")
    (run / "usim-registration-permit.json").write_text(
        json.dumps({"permit_nonce": record["permit_nonce"]}), encoding="utf-8")
    (run / "usim-registration-dispatch.json").write_text(
        json.dumps({"permit_nonce": record["permit_nonce"], "dispatch_count": 1}),
        encoding="utf-8")
    return record


IDENTITY = {"engine_run_id": "run-old", "auth_seq_baseline": 7, "campaign_epoch": "c" * 64}
TXID = "engine-replace-1787810000-abcdef012345"
OTHER_TXID = "engine-replace-1787820000-abcdef012345"


def test_archive_and_clear_exhausted_removes_all_four_artifacts(tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    write_exhausted(run)

    result = engine.archive_and_clear_exhausted_usim_recovery(
        "1", txid=TXID, **IDENTITY)
    assert result["status"] == "archived" and result["terminal"] is True
    assert set(result["artifacts"]) == {
        "usim-auth-recovery.json", "usim-auth-recovery.fence",
        "usim-registration-permit.json", "usim-registration-dispatch.json",
    }
    for name in result["artifacts"]:
        assert not (run / name).exists()

    manifest_path = (tmp_path / "orchestrator" / "usim-recovery-exhausted-archive"
                     / f"1-{TXID}.json")
    manifest = json.loads(manifest_path.read_text())
    assert manifest["record"]["phase"] == "exhausted"
    assert manifest["artifacts"] == result["artifacts"]


def test_archive_and_clear_exhausted_is_idempotent_for_the_same_txid(
        tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    write_exhausted(run)

    first = engine.archive_and_clear_exhausted_usim_recovery("1", txid=TXID, **IDENTITY)
    # Replaying with the exact same txid after the files are already gone must not
    # raise or re-derive different digests -- it reads back the prior manifest.
    second = engine.archive_and_clear_exhausted_usim_recovery("1", txid=TXID, **IDENTITY)
    assert second == first


def test_archive_and_clear_exhausted_rejects_wrong_phase(tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    write_exhausted(run, phase="submitted_unknown")

    result = engine.archive_and_clear_exhausted_usim_recovery(
        "1", txid=TXID, **IDENTITY)
    assert result == {"status": "stale_identity", "terminal": False, "artifacts": {}}
    assert (run / "usim-auth-recovery.json").exists()


@pytest.mark.parametrize("field,value", [
    ("engine_run_id", "run-different"),
    ("auth_seq_baseline", 8),
    ("campaign_epoch", "f" * 64),
])
def test_archive_and_clear_exhausted_rejects_identity_mismatch(
        tmp_path, monkeypatch, field, value):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    write_exhausted(run)

    identity = {**IDENTITY, field: value}
    result = engine.archive_and_clear_exhausted_usim_recovery(
        "1", txid=TXID, **identity)
    assert result["status"] == "stale_identity"
    assert (run / "usim-auth-recovery.json").exists()


def test_archive_and_clear_exhausted_rejects_a_different_txid_after_first_archive(
        tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    write_exhausted(run)
    engine.archive_and_clear_exhausted_usim_recovery("1", txid=TXID, **IDENTITY)

    # The exact record no longer exists (files were removed and the record itself
    # is gone), so a second, different transaction cannot claim to be archiving
    # the same incident.
    result = engine.archive_and_clear_exhausted_usim_recovery(
        "1", txid=OTHER_TXID, **IDENTITY)
    assert result["status"] == "stale_identity"


def test_archive_and_clear_exhausted_rejects_malformed_txid(tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    write_exhausted(run)
    with pytest.raises(engine.UsimRecoveryStateError):
        engine.archive_and_clear_exhausted_usim_recovery(
            "1", txid="not-a-real-txid", **IDENTITY)
    assert (run / "usim-auth-recovery.json").exists()


def test_containment_boundary_required_phase_accepts_exact_exhausted(
        tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    write_exhausted(run)
    events = []
    with engine.usim_recovery_containment_boundary(
            "1", publish_fence=lambda: True,
            pending_paid=lambda: {
                "open_call_leases": 0, "pending_messages": 0,
                "pending_allowance_queries": 0},
            zero_channels=lambda: True,
            expected_recovery_identity=IDENTITY,
            required_phase="exhausted") as ctx:
        events.append(ctx)
    assert events and events[0]["fenced"] is True


def test_containment_boundary_required_phase_rejects_non_exhausted(
        tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    write_exhausted(run, phase="submitted_unknown")
    with pytest.raises(engine.UsimRecoveryStateError):
        with engine.usim_recovery_containment_boundary(
                "1", publish_fence=lambda: True,
                pending_paid=lambda: {
                    "open_call_leases": 0, "pending_messages": 0,
                    "pending_allowance_queries": 0},
                zero_channels=lambda: True,
                expected_recovery_identity=IDENTITY,
                required_phase="exhausted"):
            pass


def write_current_generation(run, *, run_id="run-new", healthy=True):
    (run / "engine-run-id").write_text(run_id, encoding="utf-8")
    (run / "usim_status.json").write_text(json.dumps({
        "state": "AUTH_OK" if healthy else "AUTH_UNAVAILABLE",
        "auth_seq": 1, "version": 2, "engine_run_id": run_id, "ts": 2000.0,
    }), encoding="utf-8")


RECONCILE_TXID = "usim-reconcile-1787810000-abcdef012345"


def test_reconcile_archives_a_stale_fence_behind_a_healthy_new_generation(
        tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    write_exhausted(run)
    write_current_generation(run, run_id="run-new")

    result = engine.reconcile_stale_exhausted_usim_recovery(
        "1", txid=RECONCILE_TXID,
        registration_state_fn=lambda iid: "Registered",
        active_channel_count_fn=lambda iid: 0)
    assert result["status"] == "archived"
    assert result["stale_engine_run_id"] == "run-old"
    assert result["current_engine_run_id"] == "run-new"
    assert not (run / "usim-auth-recovery.json").exists()


def test_reconcile_does_nothing_when_the_generation_has_not_changed(
        tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    write_exhausted(run)
    write_current_generation(run, run_id="run-old")  # same as the fenced record

    result = engine.reconcile_stale_exhausted_usim_recovery(
        "1", txid=RECONCILE_TXID,
        registration_state_fn=lambda iid: "Registered",
        active_channel_count_fn=lambda iid: 0)
    assert result == {"status": "same_generation"}
    assert (run / "usim-auth-recovery.json").exists()


def test_reconcile_does_nothing_when_not_exhausted(tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    write_exhausted(run, phase="submitted_unknown")
    write_current_generation(run, run_id="run-new")

    result = engine.reconcile_stale_exhausted_usim_recovery(
        "1", txid=RECONCILE_TXID,
        registration_state_fn=lambda iid: "Registered",
        active_channel_count_fn=lambda iid: 0)
    assert result == {"status": "not_exhausted"}


@pytest.mark.parametrize("registration,channels,usim_healthy,reason", [
    ("Rejected", 0, True, "not_registered"),
    ("Registered", 1, True, "channels_not_proven_zero"),
    ("Registered", 0, False, "usim_not_auth_ok"),
])
def test_reconcile_refuses_to_clear_behind_an_unhealthy_new_generation(
        tmp_path, monkeypatch, registration, channels, usim_healthy, reason):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    write_exhausted(run)
    write_current_generation(run, run_id="run-new", healthy=usim_healthy)

    result = engine.reconcile_stale_exhausted_usim_recovery(
        "1", txid=RECONCILE_TXID,
        registration_state_fn=lambda iid: registration,
        active_channel_count_fn=lambda iid: channels)
    assert result["status"] == "unhealthy" and result["reason"] == reason
    assert (run / "usim-auth-recovery.json").exists()


def test_reconcile_when_current_generation_marker_is_missing(tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    write_exhausted(run)
    # No engine-run-id file at all: cannot even tell whether the generation changed.
    result = engine.reconcile_stale_exhausted_usim_recovery(
        "1", txid=RECONCILE_TXID,
        registration_state_fn=lambda iid: "Registered",
        active_channel_count_fn=lambda iid: 0)
    assert result == {"status": "current_generation_unknown"}
    assert (run / "usim-auth-recovery.json").exists()


def test_containment_boundary_without_required_phase_still_accepts_other_phases(
        tmp_path, monkeypatch):
    """Backward compatibility: existing callers that never pass required_phase must
    keep working exactly as before for non-exhausted, non-recovered phases."""
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    write_exhausted(run, phase="submitted_unknown")
    events = []
    with engine.usim_recovery_containment_boundary(
            "1", publish_fence=lambda: True,
            pending_paid=lambda: {
                "open_call_leases": 0, "pending_messages": 0,
                "pending_allowance_queries": 0},
            zero_channels=lambda: True,
            expected_recovery_identity=IDENTITY) as ctx:
        events.append(ctx)
    assert events
