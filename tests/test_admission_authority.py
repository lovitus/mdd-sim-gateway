import json
import os
from pathlib import Path
import time

from engine import admission_gate
from host import mdd_admission_authority as authority


RUN_ID = "11111111-2222-4333-8444-555555555555"


def observation(iid="7"):
    return authority.EngineObservation(
        iid=iid,
        container_id="b" * 64,
        image_id="sha256:" + "c" * 64,
        started_at="2026-08-23T00:00:00.000000000Z",
        restart_count=0,
        run_id=RUN_ID,
    )


def test_allow_writer_persists_epoch_and_current_identity(tmp_path, monkeypatch):
    writer = authority.NormalAuthorityWriter(tmp_path, boot_id="a" * 32)
    obs = observation()
    calls = []

    def allowed(iid, *, epoch, min_seq, identity_digest, state_digest, timeout=1.0):
        calls.append((iid, epoch, min_seq, identity_digest, state_digest))
        return min_seq >= 2

    monkeypatch.setattr(writer, "_wait_allowed", allowed)
    status = writer._publish_allow(obs, {"version": 1, "lines": {}})

    assert status["healthy"] is True
    assert status["authority_epoch"] == 1
    assert status["lease_seq"] == 2
    path = tmp_path / "instances" / "7" / "run" / authority.AUTHORITY_NAME
    payload = json.loads(path.read_text(encoding="utf-8"))
    assert payload["mode"] == "normal_committed"
    assert payload["issuer_boot_id"] == "a" * 32
    assert payload["normal"]["commit_id"] == payload["normal"]["state_digest"][:32]
    state = json.loads(writer.state_path.read_text(encoding="utf-8"))
    assert state["lines"]["7"]["authority_epoch"] == 1
    assert state["lines"]["7"]["lease_seq"] == 2
    assert calls[-1][2] == 2


def test_identity_change_advances_epoch_instead_of_reusing_old_health(tmp_path, monkeypatch):
    writer = authority.NormalAuthorityWriter(tmp_path, boot_id="a" * 32)
    monkeypatch.setattr(writer, "_wait_allowed", lambda *a, **k: k["min_seq"] >= 2)
    first_state = {"version": 1, "lines": {}}
    writer._publish_allow(observation("7"), first_state)

    restarted = authority.EngineObservation(
        iid="7",
        container_id="b" * 64,
        image_id="sha256:" + "c" * 64,
        started_at="2026-08-23T00:01:00.000000000Z",
        restart_count=1,
        run_id="22222222-3333-4333-8444-555555555555",
    )
    state = json.loads(writer.state_path.read_text(encoding="utf-8"))
    status = writer._publish_allow(restarted, state)
    assert status["authority_epoch"] == 2
    assert status["lease_seq"] == 2


def test_deny_writes_local_fence_and_removes_stale_authority(tmp_path, monkeypatch):
    writer = authority.NormalAuthorityWriter(tmp_path, boot_id="a" * 32)
    run = tmp_path / "instances" / "7" / "run"
    run.mkdir(parents=True)
    (run / authority.AUTHORITY_NAME).write_text("stale", encoding="utf-8")
    monkeypatch.setattr(writer, "_wait_denied", lambda iid: True)

    status = writer._publish_deny("7", "line_pcscf_rebind")

    assert status["healthy"] is True
    assert (run / authority.DENY_NAME).exists()
    assert not (run / authority.AUTHORITY_NAME).exists()
    line_status = json.loads((run / authority.STATUS_NAME).read_text(encoding="utf-8"))
    assert line_status["state"] == "deny"
    assert line_status["reason"] == "line_pcscf_rebind"


def test_deny_write_failure_still_removes_stale_authority(tmp_path, monkeypatch):
    writer = authority.NormalAuthorityWriter(tmp_path, boot_id="a" * 32)
    run = tmp_path / "instances" / "7" / "run"
    run.mkdir(parents=True)
    (run / authority.AUTHORITY_NAME).write_text("stale", encoding="utf-8")
    original_atomic = authority._atomic_json

    def fail_deny(path, value, mode=0o600):
        if Path(path).name == authority.DENY_NAME:
            raise OSError("simulated deny write failure")
        return original_atomic(path, value, mode)

    monkeypatch.setattr(authority, "_atomic_json", fail_deny)
    monkeypatch.setattr(writer, "_wait_denied", lambda iid: False)

    status = writer._publish_deny("7", "global_maintenance_unknown")

    assert status["state"] == "deny"
    assert status["healthy"] is False
    assert status["reason"] == "deny_write_failed_not_proven"
    assert status["authority_removed"] is True
    assert not (run / authority.AUTHORITY_NAME).exists()
    line_status = json.loads((run / authority.STATUS_NAME).read_text(encoding="utf-8"))
    assert line_status["reason"] == "deny_write_failed_not_proven"


def test_deny_write_failure_waits_until_gate_observes_removed_authority(tmp_path, monkeypatch):
    writer = authority.NormalAuthorityWriter(tmp_path, boot_id="a" * 32)
    run = tmp_path / "instances" / "7" / "run"
    run.mkdir(parents=True)
    socket_path = Path(os.environ["TMPDIR"]) / f"mdd-deny-fail-{os.getpid()}-{time.time_ns()}.sock"
    state = admission_gate.GateState("7", RUN_ID, ttl=3.0)
    service = admission_gate.GateService(
        state, run / authority.AUTHORITY_NAME, socket_path,
        run / "admission-gate-status.json", interval=10.0,
        fence_paths=((run / authority.DENY_NAME, "local_fence_admission_deny"),))
    service.start()
    original_atomic = authority._atomic_json
    original_remove = writer._remove_or_poison_authority
    during_remove_probe = []

    def fail_deny(path, value, mode=0o600):
        if Path(path).name == authority.DENY_NAME:
            raise OSError("simulated deny write failure")
        return original_atomic(path, value, mode)

    def remove_and_probe(run_dir):
        result = original_remove(run_dir)
        during_remove_probe.append(admission_gate.probe(socket_path, "sms_in")["allowed"])
        return result

    try:
        monkeypatch.setattr(
            writer, "_probe",
            lambda iid: admission_gate.probe(socket_path, "media_check"))
        monkeypatch.setattr(writer, "_remove_or_poison_authority", remove_and_probe)
        auth1 = writer._authority(observation("7"), 1, 1)[0]
        auth2 = writer._authority(observation("7"), 1, 2)[0]
        original_atomic(run / authority.AUTHORITY_NAME, auth1)
        assert admission_gate.probe(socket_path, "sms_in")["allowed"] is False
        original_atomic(run / authority.AUTHORITY_NAME, auth2)
        assert admission_gate.probe(socket_path, "sms_in")["allowed"] is True
        monkeypatch.setattr(authority, "_atomic_json", fail_deny)

        status = writer._publish_deny("7", "global_maintenance_unknown")

        assert status["state"] == "deny"
        assert status["healthy"] is True
        assert status["reason"] == "deny_write_failed"
        assert during_remove_probe == [False]
        assert not (run / authority.AUTHORITY_NAME).exists()
        assert admission_gate.probe(socket_path, "sms_in")["allowed"] is False
    finally:
        service.stop()
        socket_path.unlink(missing_ok=True)


def test_allow_requires_prior_deny_proof_before_removing_deny(tmp_path, monkeypatch):
    writer = authority.NormalAuthorityWriter(tmp_path, boot_id="a" * 32)
    run = tmp_path / "instances" / "7" / "run"
    run.mkdir(parents=True)
    (run / authority.DENY_NAME).write_text("{}", encoding="utf-8")
    monkeypatch.setattr(writer, "_wait_denied", lambda iid, timeout=1.0: False)
    monkeypatch.setattr(writer, "_wait_allowed", lambda *a, **k: True)

    status = writer._publish_allow(observation("7"), {"version": 1, "lines": {}})

    assert status == {
        "iid": "7", "state": "deny", "healthy": False,
        "reason": "deny_not_proven_before_allow",
    }
    assert (run / authority.DENY_NAME).exists()
    assert not (run / authority.AUTHORITY_NAME).exists()
    line_status = json.loads((run / authority.STATUS_NAME).read_text(encoding="utf-8"))
    assert line_status["reason"] == "deny_not_proven_before_allow"


def test_allow_not_proven_actively_revokes_gate_authority(tmp_path, monkeypatch):
    writer = authority.NormalAuthorityWriter(tmp_path, boot_id="a" * 32)
    run = tmp_path / "instances" / "7" / "run"
    run.mkdir(parents=True)
    socket_path = Path(os.environ["TMPDIR"]) / f"mdd-authority-{os.getpid()}-{time.time_ns()}.sock"
    state = admission_gate.GateState("7", RUN_ID, ttl=3.0)
    service = admission_gate.GateService(
        state, run / authority.AUTHORITY_NAME, socket_path,
        run / "admission-gate-status.json", interval=0.02,
        fence_paths=((run / authority.DENY_NAME, "local_fence_admission_deny"),))
    service.start()
    try:
        monkeypatch.setattr(writer, "_wait_allowed", lambda *a, **k: False)
        monkeypatch.setattr(
            writer, "_probe",
            lambda iid: admission_gate.probe(socket_path, "media_check"))

        status = writer._publish_allow(observation("7"), {"version": 1, "lines": {}})

        assert status["state"] == "deny"
        assert status["reason"] == "allow_not_proven"
        assert (run / authority.DENY_NAME).exists()
        assert not (run / authority.AUTHORITY_NAME).exists()
        assert admission_gate.probe(socket_path, "sms_in")["allowed"] is False
    finally:
        service.stop()
        socket_path.unlink(missing_ok=True)


def test_engine_list_failure_denies_known_lines(tmp_path, monkeypatch):
    writer = authority.NormalAuthorityWriter(tmp_path, boot_id="a" * 32)
    run = tmp_path / "instances" / "7" / "run"
    run.mkdir(parents=True)
    (run / authority.AUTHORITY_NAME).write_text("stale", encoding="utf-8")
    writer._save_state({"lines": {"7": {"authority_epoch": 1, "lease_seq": 2}}})
    monkeypatch.setattr(writer, "_running_engine_names",
                        lambda: (_ for _ in ()).throw(authority.AuthorityWriterError(
                            "engine_list_unavailable")))
    monkeypatch.setattr(writer, "_wait_denied", lambda iid: True)

    aggregate = writer.reconcile_once()

    assert aggregate["state"] == "unhealthy"
    assert aggregate["lines"]["7"]["state"] == "deny"
    assert (run / authority.DENY_NAME).exists()
    assert not (run / authority.AUTHORITY_NAME).exists()


def test_malformed_committed_upgrade_manifest_is_global_deny(tmp_path, monkeypatch):
    writer = authority.NormalAuthorityWriter(tmp_path, boot_id="a" * 32)
    run = tmp_path / "instances" / "7" / "run"
    run.mkdir(parents=True)
    writer.root.mkdir(parents=True)
    (writer.root / authority.CONTROL_UPGRADE_NAME).write_text(
        json.dumps({"phase": "committed"}), encoding="utf-8")
    monkeypatch.setattr(writer, "_running_engine_names",
                        lambda: [("b" * 64, "mdd-sim-gateway-engine-7")])
    monkeypatch.setattr(writer, "_stable_observation", lambda *_: observation("7"))
    monkeypatch.setattr(writer, "_image_abi", lambda _image: authority.ENGINE_ADMISSION_ABI)
    monkeypatch.setattr(writer, "_wait_denied", lambda iid: True)

    aggregate = writer.reconcile_once()

    assert aggregate["lines"]["7"]["state"] == "deny"
    assert json.loads((run / authority.DENY_NAME).read_text())[
        "reason"] == "global_maintenance_unknown"


def test_reconcile_denies_when_local_pcscf_fence_exists(tmp_path, monkeypatch):
    writer = authority.NormalAuthorityWriter(tmp_path, boot_id="a" * 32)
    run = tmp_path / "instances" / "7" / "run"
    run.mkdir(parents=True)
    (run / "pcscf-rebind.json").write_text("{}", encoding="utf-8")
    monkeypatch.setattr(writer, "_running_engine_names",
                        lambda: [("b" * 64, "mdd-sim-gateway-engine-7")])
    monkeypatch.setattr(writer, "_stable_observation", lambda *_: observation("7"))
    monkeypatch.setattr(writer, "_image_abi", lambda _image: authority.ENGINE_ADMISSION_ABI)
    monkeypatch.setattr(writer, "_wait_denied", lambda iid: True)

    aggregate = writer.reconcile_once()

    assert aggregate["state"] == "healthy"
    assert aggregate["lines"]["7"]["state"] == "deny"
    assert json.loads((run / authority.DENY_NAME).read_text())["reason"] == "line_pcscf_rebind"
