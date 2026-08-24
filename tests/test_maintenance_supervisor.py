import json
import os
from pathlib import Path
import threading
import time
from unittest.mock import Mock, call, patch

import pytest

from host import mdd_maintenance_supervisor as supervisor


def manifest():
    return {
        "version": 1, "txid": "deploy-20260823-0001",
        "phase": "rollback_committed",
        "owner": {"id": "owner-process-1", "epoch": 1},
        "source_control": {
            "container_id": "a" * 64, "image_id": "sha256:" + "b" * 64,
            "started_at": "2026-08-22T15:44:45.000000000Z", "network_mode": "host",
        },
        "rollback_control": {
            "container_id": "a" * 64, "image_id": "sha256:" + "b" * 64,
            "started_at": "2026-08-22T15:44:45.000000000Z",
            "pid": 101, "restart_count": 0, "network_mode": "host",
            "create_spec_hash": "5" * 64,
        },
        "proxy": {"container_id": "c" * 64, "image_id": "sha256:" + "d" * 64},
        "rollback_upstream": {
            "tls_host": "127.0.0.1", "tls_port": 18443,
            "plain_host": "127.0.0.1", "plain_port": 18000,
            "engine_peers": [],
        },
        "lines": [{
            "instance": "7", "source_container_id": "e" * 64,
            "source_image_id": "sha256:" + "f" * 64,
            "target_image_digest": "sha256:" + "1" * 64,
            "phase": "rollback_verified",
        }],
    }


class FakeInspector:
    def __init__(self):
        self.proxy = supervisor.ContainerFact(
            "c" * 64, "sha256:" + "d" * 64, "proxy-start", 201, 0, "host", (),
            (("io.mdd-sim-gateway.component", "maintenance-proxy"),), (), "4" * 64)
        self.control = supervisor.ContainerFact(
            "a" * 64, "sha256:" + "b" * 64,
            "2026-08-22T15:44:45.000000000Z", 101, 0, "host", (),
            (("io.mdd-sim-gateway.component", "control"),), (), "5" * 64)
        self.engine = supervisor.ContainerFact(
            "9" * 64, "sha256:" + "f" * 64, "engine-start", 301, 0, "bridge", (),
            (("io.mdd-sim-gateway.component", "engine"),), (), "6" * 64)

    def container(self, container_id):
        facts = (self.proxy, self.control, self.engine, *getattr(self, "extra_engines", ()))
        return {item.container_id: item for item in facts}[container_id]

    def engine_channels_zero(self):
        if getattr(self, "channels_active", False):
            raise supervisor.SupervisorError("asterisk_channels_active")
        return (self.engine, *getattr(self, "extra_engines", ()))

    def engine_facts(self):
        return (self.engine, *getattr(self, "extra_engines", ()))


def prepare(tmp_path):
    data = tmp_path / "data"
    root = data / "orchestrator"
    root.mkdir(parents=True)
    app = supervisor.MaintenanceSupervisor(
        data, socket_path=tmp_path / "supervisor.sock",
        inspector=FakeInspector(), require_root=False)
    value = manifest()
    app.manifest_path.write_text(json.dumps(value), encoding="utf-8")
    mode = {
        "version": 1, "txid": value["txid"],
        "container_id": value["proxy"]["container_id"],
        "image_id": value["proxy"]["image_id"],
        "process_boot_id": "8" * 32, "host_boot_id": app.host_boot,
        "supervisor_boot_id": "", "lease_seq": 0, "epoch": 1,
        "state": "deny", "active_full": 0, "forwarding_full": 0,
        "manifest_digest": "", "updated_at": 1,
    }
    supervisor._atomic_json(app.mode_path, mode)
    supervisor._atomic_json(app.ready_path, {
        "version": 1, "txid": value["txid"],
        "container_id": value["proxy"]["container_id"],
        "image_id": value["proxy"]["image_id"],
        "process_boot_id": "8" * 32, "mode_epoch": 1,
        "entry": {"bind": "0.0.0.0", "tls_port": 8443, "plain_port": 8000,
                  "admin_bind": "127.0.0.1", "admin_port": 19090},
        "ready_at": 1,
    })
    (data / "mdd-sim-gateway.sqlite").touch()
    run = data / "instances" / "7" / "run"
    run.mkdir(parents=True)
    run.joinpath("engine-run-id").write_text("rollback-run-7", encoding="utf-8")
    supervisor._atomic_json(run / "engine-maintenance.json", {
        "version": 1, "txid": value["txid"], "instance": "7",
        "phase": "rollback_verified",
        "source": {
            "container_id": "e" * 64, "image_id": "sha256:" + "f" * 64,
            "started_at": "source-start", "pid": 300, "restart_count": 0,
            "run_id": "source-run-7", "run_id_mode": "present",
        },
        "target_image_digest": "sha256:" + "1" * 64, "target": None,
        "rollback": {
            "container_id": app.inspector.engine.container_id,
            "image_id": app.inspector.engine.image_id,
            "started_at": app.inspector.engine.started_at,
            "pid": app.inspector.engine.pid,
            "restart_count": app.inspector.engine.restart_count,
            "run_id": "rollback-run-7", "run_id_mode": "present",
        },
        "attempts": 1, "manual_required": False,
    })
    return app


def add_second_line(app):
    value = json.loads(app.manifest_path.read_text())
    value["lines"].append({
        "instance": "8", "source_container_id": "7" * 64,
        "source_image_id": "sha256:" + "6" * 64,
        "target_image_digest": "sha256:" + "2" * 64,
        "phase": "rollback_verified",
    })
    supervisor._atomic_json(app.manifest_path, value)
    engine2 = supervisor.ContainerFact(
        "8" * 64, "sha256:" + "6" * 64, "engine-start-8", 302, 0,
        "bridge", (), (("io.mdd-sim-gateway.component", "engine"),), (), "3" * 64)
    app.inspector.extra_engines = (engine2,)
    run = app.data / "instances" / "8" / "run"
    run.mkdir(parents=True)
    run.joinpath("engine-run-id").write_text("rollback-run-8", encoding="utf-8")
    supervisor._atomic_json(run / "engine-maintenance.json", {
        "version": 1, "txid": value["txid"], "instance": "8",
        "phase": "rollback_verified",
        "source": {
            "container_id": "7" * 64, "image_id": "sha256:" + "6" * 64,
            "started_at": "source-start-8", "pid": 299, "restart_count": 0,
            "run_id": "source-run-8", "run_id_mode": "present",
        },
        "target_image_digest": "sha256:" + "2" * 64, "target": None,
        "rollback": {
            "container_id": engine2.container_id, "image_id": engine2.image_id,
            "started_at": engine2.started_at, "pid": engine2.pid,
            "restart_count": engine2.restart_count,
            "run_id": "rollback-run-8", "run_id_mode": "present",
        },
        "attempts": 1, "manual_required": False,
    })
    return app


def health_from_mode(app, *, lost=False):
    mode = json.loads(app.mode_path.read_text(encoding="utf-8"))
    ready = json.loads(app.ready_path.read_text(encoding="utf-8"))
    ready["mode_epoch"] = mode["epoch"]
    supervisor._atomic_json(app.ready_path, ready)
    eligible = (not lost and mode["state"] in {"deny", "deny_applied"}
                and mode["active_full"] == mode["forwarding_full"] == 0)
    return {
        "ok": True, "txid": mode["txid"], "container_id": mode["container_id"],
        "image_id": mode["image_id"], "process_boot_id": mode["process_boot_id"],
        "host_boot_id": mode["host_boot_id"],
        "supervisor_boot_id": mode["supervisor_boot_id"],
        "lease_seq": mode["lease_seq"], "epoch": mode["epoch"],
        "state": mode["state"], "active_full": mode["active_full"],
        "forwarding_full": mode["forwarding_full"],
        "authorization_lost": lost, "authorization_eligible": eligible,
    }


def proof_patches(app, *, lost=False):
    return (
        patch.object(app, "_network_proof"),
        patch.object(supervisor, "probe_upstreams"),
        patch.object(supervisor, "pending_paid_work", return_value={
            "open_call_leases": 0, "pending_messages": 0,
            "pending_allowance_queries": 0}),
        patch.object(supervisor, "read_admin_health",
                     side_effect=lambda *_args: health_from_mode(app, lost=lost)),
        patch.object(supervisor, "prove_proxy_ingress"),
    )


def recover_with_partial_line_release(app):
    """Recover two lines while injecting failure after the first marker is removed."""
    patches = proof_patches(app)
    original_unlink = app._durable_unlink
    count = 0

    def fail_second(path):
        nonlocal count
        if Path(path).name == "engine-maintenance.json":
            count += 1
            if count == 2:
                raise OSError("injected partial release")
        return original_unlink(path)

    with patches[0], patches[1], patches[2], patches[3], patches[4], \
            patch.object(app, "_durable_unlink", side_effect=fail_second):
        status = app.recover()
    assert status["state"] == "release_pending"
    assert app.entry_fence_path.exists()
    return app


def test_explicit_recover_is_the_only_path_that_grants_and_renew_advances_lease(tmp_path):
    app = prepare(tmp_path)
    patches = proof_patches(app)
    with patches[0], patches[1], patches[2], patches[3], patches[4]:
        status = app.recover()
        granted = json.loads(app.mode_path.read_text())
        assert status["state"] == "full"
        assert granted["epoch"] == 2
        assert granted["supervisor_boot_id"] == app.supervisor_boot_id
        assert granted["lease_seq"] == 1
        app.renew()
        app.renew()
    renewed = json.loads(app.mode_path.read_text())
    assert renewed["lease_seq"] == 3
    assert renewed["epoch"] == granted["epoch"]
    assert not app.entry_fence_path.exists()
    run = app.data / "instances" / "7" / "run"
    assert not (run / "engine-maintenance.json").exists()
    assert app.line_proof_path.exists()


def test_planned_revoke_publishes_control_fence_before_cas(tmp_path):
    app = prepare(tmp_path)
    patches = proof_patches(app)
    with patches[0], patches[1], patches[2], patches[3], patches[4]:
        app.recover()
        result = app.revoke(wait=False)
    fence = json.loads(app.entry_fence_path.read_text())
    mode = json.loads(app.mode_path.read_text())
    assert fence["state"] == "draining"
    assert fence["mode_epoch"] == 2
    assert mode["state"] == "revoking"
    assert result["state"] == "revoking"
    assert (app.data / "instances" / "7" / "run" /
            "engine-maintenance.json").exists()


def test_recover_fails_closed_when_proxy_reports_authorization_loss(tmp_path):
    app = prepare(tmp_path)
    patches = proof_patches(app, lost=True)
    with patches[0], patches[1], patches[2], patches[3], patches[4], \
            pytest.raises(supervisor.SupervisorError,
                          match="proxy_not_authorization_eligible"):
        app.recover()
    assert json.loads(app.mode_path.read_text())["state"] == "deny"
    assert json.loads(app.status_path.read_text())["state"] == "manual_required"
    assert app.entry_fence_path.exists()


def test_damaged_foreign_entry_fence_is_never_guessed_or_deleted(tmp_path):
    app = prepare(tmp_path)
    app.entry_fence_path.write_text("{damaged", encoding="utf-8")
    with pytest.raises(supervisor.SupervisorError, match="state_unreadable"):
        app.recover()
    assert app.entry_fence_path.read_text(encoding="utf-8") == "{damaged"
    assert json.loads(app.mode_path.read_text())["state"] == "deny"


def test_unrelated_engine_generation_is_rejected_even_when_count_matches(tmp_path):
    app = prepare(tmp_path)
    unrelated = supervisor.ContainerFact(
        "7" * 64, app.inspector.engine.image_id, app.inspector.engine.started_at,
        app.inspector.engine.pid, 0, "bridge", (),
        (("io.mdd-sim-gateway.component", "engine"),), (), "6" * 64)
    app.inspector.engine = unrelated
    patches = proof_patches(app)
    with patches[0], patches[1], patches[2], patches[3], patches[4], \
            pytest.raises(supervisor.SupervisorError, match="engine_topology_mismatch"):
        app.recover()
    assert json.loads(app.mode_path.read_text())["state"] == "deny"


def test_revoke_write_failure_stops_lease_renewal_and_keeps_fence(tmp_path):
    app = prepare(tmp_path)
    patches = proof_patches(app)
    with patches[0], patches[1], patches[2], patches[3], patches[4]:
        app.recover()
        original = supervisor._atomic_json

        def fail_mode(path, value):
            if Path(path) == app.mode_path and value.get("state") == "revoking":
                raise OSError("disk full")
            return original(path, value)

        app.DRAIN_QUIET = 0.01
        with patch.object(supervisor, "_atomic_json", side_effect=fail_mode), \
                pytest.raises(supervisor.SupervisorError, match="revoke_write_failed"):
            app.revoke(wait=False)
    assert app._proof is None
    assert app.entry_fence_path.exists()
    assert json.loads(app.mode_path.read_text())["state"] == "full"


def test_revoke_write_failure_cannot_be_resurrected_by_an_old_heartbeat(tmp_path):
    app = prepare(tmp_path)
    patches = proof_patches(app)
    heartbeat_inside_write = threading.Event()
    allow_heartbeat = threading.Event()
    original = supervisor._atomic_json

    with patches[0], patches[1], patches[2], patches[3], patches[4]:
        app.recover()

        def interleaved_write(path, value):
            if (Path(path) == app.mode_path and value.get("state") == "full"
                    and value.get("lease_seq") == 2):
                heartbeat_inside_write.set()
                assert allow_heartbeat.wait(1)
            if Path(path) == app.mode_path and value.get("state") == "revoking":
                raise OSError("disk full")
            return original(path, value)

        errors = []
        with patch.object(supervisor, "_atomic_json", side_effect=interleaved_write):
            heartbeat = threading.Thread(target=app._heartbeat)
            heartbeat.start()
            assert heartbeat_inside_write.wait(1)

            def revoke():
                try:
                    app.revoke(wait=False, urgent=True)
                except Exception as exc:
                    errors.append(exc)

            revoker = threading.Thread(target=revoke)
            revoker.start()
            time.sleep(0.02)
            assert revoker.is_alive()
            allow_heartbeat.set()
            heartbeat.join(timeout=1)
            revoker.join(timeout=1)

    assert len(errors) == 1
    assert isinstance(errors[0], supervisor.SupervisorError)
    assert errors[0].code == "revoke_write_failed"
    assert app._proof is None
    before = json.loads(app.mode_path.read_text())
    app._heartbeat()
    assert json.loads(app.mode_path.read_text()) == before


def test_snapshot_write_failure_keeps_all_admission_fences_and_line_marker(tmp_path):
    app = prepare(tmp_path)
    patches = proof_patches(app)
    original = supervisor._atomic_json

    def fail_snapshot(path, value):
        if Path(path) == app.line_proof_path:
            raise OSError("disk full")
        return original(path, value)

    with patches[0], patches[1], patches[2], patches[3], patches[4], \
            patch.object(supervisor, "_atomic_json", side_effect=fail_snapshot):
        status = app.recover()
    run = app.data / "instances" / "7" / "run"
    assert status["state"] == "release_pending"
    assert json.loads(app.mode_path.read_text())["state"] == "full"
    assert app._proof is not None
    assert (run / "engine-maintenance.json").exists()
    assert app.entry_fence_path.exists()
    assert not app.line_proof_path.exists()
    retry_patches = proof_patches(app)
    with (retry_patches[0], retry_patches[1], retry_patches[2], retry_patches[3],
          retry_patches[4]):
        app.renew()
    assert not (run / "engine-maintenance.json").exists()
    assert not app.entry_fence_path.exists()
    assert app.line_proof_path.exists()


def test_explicit_recover_resumes_from_snapshot_after_partial_marker_cleanup(tmp_path):
    app = prepare(tmp_path)
    patches = proof_patches(app)
    with patches[0], patches[1], patches[2], patches[3], patches[4]:
        app.recover()
        app.revoke(wait=False)
        mode = json.loads(app.mode_path.read_text())
        mode.update({"state": "deny_applied", "active_full": 0,
                     "forwarding_full": 0})
        supervisor._atomic_json(app.mode_path, mode)
        status = app.recover()
        app.renew()
    assert status["state"] == "full"
    assert not app.entry_fence_path.exists()
    assert not (app.data / "instances" / "7" / "run" /
                "engine-maintenance.json").exists()


def test_two_line_partial_release_keeps_full_then_new_supervisor_finishes_idempotently(
        tmp_path):
    app = add_second_line(prepare(tmp_path))
    patches = proof_patches(app)
    original_unlink = app._durable_unlink
    marker_unlinks = 0

    def fail_second_marker(path):
        nonlocal marker_unlinks
        if Path(path).name == "engine-maintenance.json":
            marker_unlinks += 1
            if marker_unlinks == 2:
                raise OSError("injected second-line unlink failure")
        return original_unlink(path)

    with patches[0], patches[1], patches[2], patches[3], patches[4], \
            patch.object(app, "_durable_unlink", side_effect=fail_second_marker), \
            patch.object(app, "revoke", wraps=app.revoke) as revoke:
        status = app.recover()
    run7 = app.data / "instances" / "7" / "run" / "engine-maintenance.json"
    run8 = app.data / "instances" / "8" / "run" / "engine-maintenance.json"
    assert status["state"] == "release_pending"
    assert json.loads(app.mode_path.read_text())["state"] == "full"
    assert app._proof is not None
    assert not run7.exists() and run8.exists()
    assert app.entry_fence_path.exists() and app.line_proof_path.exists()
    revoke.assert_not_called()

    # Model proxy lease expiry/new supervisor generation. Explicit recovery reconstructs the
    # missing old-Engine marker from the durable snapshot before it proves and clears both.
    mode = json.loads(app.mode_path.read_text())
    mode.update({"state": "deny_applied", "active_full": 0, "forwarding_full": 0})
    supervisor._atomic_json(app.mode_path, mode)
    replacement = supervisor.MaintenanceSupervisor(
        app.data, socket_path=tmp_path / "replacement.sock",
        inspector=app.inspector, require_root=False)
    replacement_patches = proof_patches(replacement)
    with (replacement_patches[0], replacement_patches[1], replacement_patches[2],
          replacement_patches[3], replacement_patches[4]):
        resumed = replacement.recover()
        replacement.renew()
    assert resumed["state"] == "full"
    assert not run7.exists() and not run8.exists()
    assert not replacement.entry_fence_path.exists()
    assert replacement.line_proof_path.exists()


def test_global_fence_unlink_response_loss_is_treated_as_completed_release(tmp_path):
    app = prepare(tmp_path)
    patches = proof_patches(app)
    original_unlink = app._durable_unlink

    def response_lost(path):
        original_unlink(path)
        if Path(path) == app.entry_fence_path:
            raise OSError("directory fsync response lost")

    with patches[0], patches[1], patches[2], patches[3], patches[4], \
            patch.object(app, "_durable_unlink", side_effect=response_lost):
        status = app.recover()
    assert status["state"] == "full"
    assert app._proof is not None
    assert not app.entry_fence_path.exists()
    assert not (app.data / "instances" / "7" / "run" /
                "engine-maintenance.json").exists()


def test_partial_release_with_new_active_call_finishes_without_revoke_but_generation_change_fails(
        tmp_path):
    app = add_second_line(prepare(tmp_path))
    patches = proof_patches(app)
    original_unlink = app._durable_unlink
    count = 0

    def fail_second(path):
        nonlocal count
        if Path(path).name == "engine-maintenance.json":
            count += 1
            if count == 2:
                raise OSError("injected partial release")
        return original_unlink(path)

    with patches[0], patches[1], patches[2], patches[3], patches[4], \
            patch.object(app, "_durable_unlink", side_effect=fail_second):
        assert app.recover()["state"] == "release_pending"
    app.inspector.channels_active = True
    before_seq = json.loads(app.mode_path.read_text())["lease_seq"]
    active_patches = proof_patches(app)
    with active_patches[0], active_patches[1], active_patches[2], \
            active_patches[3], active_patches[4], \
            patch.object(app, "revoke", wraps=app.revoke) as revoke:
        app.renew()
        app.renew()
    assert revoke.call_count == 0
    assert json.loads(app.mode_path.read_text())["state"] == "full"
    assert json.loads(app.mode_path.read_text())["lease_seq"] >= before_seq + 2
    assert app._proof is not None and not app.entry_fence_path.exists()

    old = app.inspector.extra_engines[0]
    app.inspector.extra_engines = (supervisor.ContainerFact(
        old.container_id, old.image_id, old.started_at, old.pid + 1,
        old.restart_count, old.network_mode, old.networks, old.labels,
        old.port_bindings, old.create_spec_hash),)
    app.renew()
    assert app._proof is None
    assert json.loads(app.mode_path.read_text())["state"] == "revoking"


def test_partial_release_commit_accepts_heartbeat_proof_refresh_in_same_generation(
        tmp_path):
    app = recover_with_partial_line_release(add_second_line(prepare(tmp_path)))
    old_proof = app._proof
    old_generation = app._lease_generation
    patches = proof_patches(app)
    with patches[0], patches[1], patches[2], patches[3], patches[4]:
        app._heartbeat()
        assert app._proof is not old_proof
        assert app._lease_generation == old_generation
        fence = app._read_entry_fence()
        app._commit_entry_fence(
            fence, old_proof, lease_generation=old_generation)
    assert not app.entry_fence_path.exists()
    assert not (app.data / "instances" / "8" / "run" /
                "engine-maintenance.json").exists()


def test_partial_release_commit_cannot_unlink_after_heartbeat_invalidation(tmp_path):
    app = recover_with_partial_line_release(add_second_line(prepare(tmp_path)))
    app.ADMISSION_LOCK_TIMEOUT = 0.5
    app.inspector.channels_active = True
    line_lock = app.data / "instances" / "7" / "run" / \
        ".engine-maintenance.lock"
    heartbeat_done = threading.Event()
    original_heartbeat = app._heartbeat

    def heartbeat_then_signal():
        original_heartbeat()
        heartbeat_done.set()

    patches = proof_patches(app)
    with line_lock.open("a+") as held, \
            patch.object(app, "_durable_unlink", wraps=app._durable_unlink) as unlink, \
            patch.object(app, "_heartbeat", side_effect=heartbeat_then_signal), \
            patches[0], patches[1], patches[2], patches[3], patches[4]:
        __import__("fcntl").flock(held.fileno(), __import__("fcntl").LOCK_EX)
        worker = threading.Thread(target=app.renew)
        worker.start()
        assert heartbeat_done.wait(1)
        assert app._invalidate_proof()
        __import__("fcntl").flock(held.fileno(), __import__("fcntl").LOCK_UN)
        worker.join(timeout=1)
    assert not worker.is_alive()
    assert unlink.call_count == 0
    assert app.entry_fence_path.exists()
    assert (app.data / "instances" / "8" / "run" /
            "engine-maintenance.json").exists()
    status = json.loads(app.status_path.read_text())
    assert status["state"] == "release_pending"
    assert status["error_code"] == "commit_generation_changed"


def test_partial_release_commit_cannot_unlink_after_stop_intent(tmp_path):
    app = recover_with_partial_line_release(add_second_line(prepare(tmp_path)))
    app.ADMISSION_LOCK_TIMEOUT = 0.5
    app.inspector.channels_active = True
    line_lock = app.data / "instances" / "7" / "run" / \
        ".engine-maintenance.lock"
    heartbeat_done = threading.Event()
    original_heartbeat = app._heartbeat

    def heartbeat_then_signal():
        original_heartbeat()
        heartbeat_done.set()

    patches = proof_patches(app)
    with line_lock.open("a+") as held, \
            patch.object(app, "_durable_unlink", wraps=app._durable_unlink) as unlink, \
            patch.object(app, "_heartbeat", side_effect=heartbeat_then_signal), \
            patches[0], patches[1], patches[2], patches[3], patches[4]:
        __import__("fcntl").flock(held.fileno(), __import__("fcntl").LOCK_EX)
        worker = threading.Thread(target=app.renew)
        worker.start()
        assert heartbeat_done.wait(1)
        started = time.monotonic()
        app.stop()
        assert time.monotonic() - started < 0.75
        __import__("fcntl").flock(held.fileno(), __import__("fcntl").LOCK_UN)
        worker.join(timeout=1)
    assert not worker.is_alive()
    assert unlink.call_count == 0
    assert app.entry_fence_path.exists()
    assert (app.data / "instances" / "8" / "run" /
            "engine-maintenance.json").exists()
    status = json.loads(app.status_path.read_text())
    assert status["state"] == "release_pending"
    assert status["error_code"] == "commit_generation_changed"
    assert app._active_generation is None


@pytest.mark.parametrize("lock_kind", ["manifest", "mode"])
def test_recover_cas_lock_contention_is_bounded_and_never_grants(tmp_path, lock_kind):
    app = prepare(tmp_path)
    app.ADMISSION_LOCK_TIMEOUT = 0.02
    lock_path = (app.manifest_path.with_suffix(app.manifest_path.suffix + ".lock")
                 if lock_kind == "manifest"
                 else app.mode_path.with_suffix(app.mode_path.suffix + ".lock"))
    patches = proof_patches(app)
    with lock_path.open("a+") as held:
        __import__("fcntl").flock(held.fileno(), __import__("fcntl").LOCK_EX)
        started = time.monotonic()
        with patches[0], patches[1], patches[2], patches[3], patches[4], \
                pytest.raises(supervisor.SupervisorError,
                              match=f"recover_{lock_kind}_lock_timeout"):
            app.recover()
        assert time.monotonic() - started < 0.5
    assert json.loads(app.mode_path.read_text())["state"] == "deny"
    assert app._proof is None


@pytest.mark.parametrize("lock_kind", ["manifest", "mode"])
def test_release_pending_commit_lock_contention_is_bounded_and_keeps_full(tmp_path, lock_kind):
    app = add_second_line(prepare(tmp_path))
    patches = proof_patches(app)
    original_unlink = app._durable_unlink
    count = 0

    def fail_second(path):
        nonlocal count
        if Path(path).name == "engine-maintenance.json":
            count += 1
            if count == 2:
                raise OSError("injected partial release")
        return original_unlink(path)

    with patches[0], patches[1], patches[2], patches[3], patches[4], \
            patch.object(app, "_durable_unlink", side_effect=fail_second):
        app.recover()
    app.ADMISSION_LOCK_TIMEOUT = 0.03
    lock_path = (app.manifest_path.with_suffix(app.manifest_path.suffix + ".lock")
                 if lock_kind == "manifest"
                 else app.mode_path.with_suffix(app.mode_path.suffix + ".lock"))
    acquired = threading.Event()
    release = threading.Event()
    original_heartbeat = app._heartbeat

    def heartbeat_then_contend():
        original_heartbeat()

        def holder():
            with lock_path.open("a+") as handle:
                __import__("fcntl").flock(handle.fileno(), __import__("fcntl").LOCK_EX)
                acquired.set()
                release.wait(1)

        threading.Thread(target=holder, daemon=True).start()
        assert acquired.wait(1)

    retry_patches = proof_patches(app)
    try:
        with retry_patches[0], retry_patches[1], retry_patches[2], \
                retry_patches[3], retry_patches[4], \
                patch.object(app, "_heartbeat",
                             side_effect=heartbeat_then_contend):
            started = time.monotonic()
            app.renew()
            assert time.monotonic() - started < 0.5
    finally:
        release.set()
    status = json.loads(app.status_path.read_text())
    assert status["state"] == "release_pending"
    assert status["error_code"] == f"commit_{lock_kind}_lock_timeout"
    assert app._proof is not None
    assert json.loads(app.mode_path.read_text())["state"] == "full"


def test_recover_cannot_publish_fences_while_a_line_admission_lock_is_held(tmp_path):
    app = prepare(tmp_path)
    app.ADMISSION_LOCK_TIMEOUT = 0.02
    lock_path = app.data / "instances" / "7" / "run" / ".engine-maintenance.lock"
    with lock_path.open("a+") as held:
        __import__("fcntl").flock(held.fileno(), __import__("fcntl").LOCK_EX)
        with pytest.raises(supervisor.SupervisorError, match="admission_lock_timeout"):
            app.recover()
    assert json.loads(app.mode_path.read_text())["state"] == "deny"
    assert not app.entry_fence_path.exists()


def test_new_supervisor_does_not_inherit_an_old_full_generation(tmp_path):
    app = prepare(tmp_path)
    mode = json.loads(app.mode_path.read_text())
    mode.update({"state": "full", "supervisor_boot_id": "7" * 32,
                 "lease_seq": 9, "manifest_digest": supervisor._digest(manifest())})
    supervisor._atomic_json(app.mode_path, mode)
    app._startup_revoke()
    assert json.loads(app.mode_path.read_text())["state"] == "revoking"
    # Startup loss cuts the proxy lease but does not leave a permanent Control mutation fence.
    assert not app.entry_fence_path.exists()


def test_singleton_flock_rejects_a_second_supervisor(tmp_path):
    first = prepare(tmp_path)
    second = supervisor.MaintenanceSupervisor(
        first.data, socket_path=tmp_path / "second.sock",
        inspector=FakeInspector(), require_root=False)
    first._acquire_singleton()
    try:
        with pytest.raises(supervisor.SupervisorError, match="supervisor_already_running"):
            second._acquire_singleton()
    finally:
        first._lock_handle.close()
        first._lock_handle = None


def test_watch_never_auto_grants_a_committed_manifest(tmp_path):
    app = prepare(tmp_path)
    app.socket_path = Path(f"/Volumes/micron512g/tmp-project/mdd-sup-{os.getpid()}.sock")
    thread = threading.Thread(target=app.run, daemon=True)
    thread.start()
    deadline = time.monotonic() + 1
    while not app.socket_path.exists() and time.monotonic() < deadline:
        time.sleep(0.01)
    try:
        status = supervisor.command(app.socket_path, "status", timeout=1)
        assert status["state"] == "deny"
        assert json.loads(app.mode_path.read_text())["state"] == "deny"
    finally:
        app.stop()
        thread.join(timeout=1)
    assert not thread.is_alive()


def test_planned_revoke_keeps_full_but_fenced_when_paid_work_cannot_drain(tmp_path):
    app = prepare(tmp_path)
    app.REVOKE_TIMEOUT = 0.01
    patches = proof_patches(app)
    with patches[0], patches[1], patches[2], patches[3], patches[4]:
        app.recover()
        assert not app.entry_fence_path.exists()
        with patch.object(supervisor, "pending_paid_work", return_value={
                "open_call_leases": 1, "pending_messages": 0,
                "pending_allowance_queries": 0}), \
                pytest.raises(supervisor.SupervisorError, match="paid_drain_timeout"):
            app.revoke(wait=False)
    assert app.entry_fence_path.exists()
    assert json.loads(app.mode_path.read_text())["state"] == "full"
    status = json.loads(app.status_path.read_text())
    assert status["state"] == "manual_required"
    assert status["error_code"] == "paid_drain_timeout"


def test_planned_drain_runs_complete_renew_audit_instead_of_lease_only(tmp_path):
    app = prepare(tmp_path)
    app.DRAIN_QUIET = 0.03
    app.LEASE_INTERVAL = 0.005
    patches = proof_patches(app)
    with patches[0], patches[1], patches[2], patches[3], patches[4]:
        app.recover()
        with patch.object(app, "renew", wraps=app.renew) as renew:
            app.revoke(wait=False)
    assert renew.call_count >= 1


def test_heartbeat_manifest_lock_wait_is_bounded_and_stop_generation_writes_nothing(
        tmp_path):
    app = prepare(tmp_path)
    app.ADMISSION_LOCK_TIMEOUT = 0.03
    patches = proof_patches(app)
    with patches[0], patches[1], patches[2], patches[3], patches[4]:
        app.recover()
    manifest_lock = app.manifest_path.with_suffix(app.manifest_path.suffix + ".lock")
    before = app.mode_path.read_bytes()
    errors = []
    with manifest_lock.open("a+") as held:
        __import__("fcntl").flock(held.fileno(), __import__("fcntl").LOCK_EX)

        def beat():
            try:
                app._heartbeat()
            except Exception as exc:
                errors.append(exc)

        thread = threading.Thread(target=beat)
        thread.start()
        thread.join(timeout=0.5)
    assert not thread.is_alive()
    assert errors and errors[0].code == "lease_manifest_lock_timeout"
    app.stop_event.set()
    with app._lease_lock:
        app._active_generation = None
    app._heartbeat()
    assert app.mode_path.read_bytes() == before


def test_run_keeps_singleton_until_blocked_heartbeat_generation_has_stopped(tmp_path):
    app = prepare(tmp_path)
    app.socket_path = Path(f"/Volumes/micron512g/tmp-project/mdd-stop-{os.getpid()}.sock")
    app.ADMISSION_LOCK_TIMEOUT = 0.03
    app.LEASE_INTERVAL = 0.01
    patches = proof_patches(app)
    with patches[0], patches[1], patches[2], patches[3], patches[4]:
        thread = threading.Thread(target=app.run)
        thread.start()
        deadline = time.monotonic() + 1
        while not app.socket_path.exists() and time.monotonic() < deadline:
            time.sleep(0.01)
        assert app.socket_path.exists()
        supervisor.command(app.socket_path, "recover", timeout=1)
        manifest_lock = app.manifest_path.with_suffix(app.manifest_path.suffix + ".lock")
        with manifest_lock.open("a+") as held:
            __import__("fcntl").flock(held.fileno(), __import__("fcntl").LOCK_EX)
            time.sleep(0.02)
            before_stop = app.mode_path.read_bytes()
            app.stop()
            thread.join(timeout=1)
        assert not thread.is_alive()
        assert app.mode_path.read_bytes() == before_stop
        replacement = supervisor.MaintenanceSupervisor(
            app.data, socket_path=tmp_path / "replacement.sock",
            inspector=FakeInspector(), require_root=False)
        replacement._acquire_singleton()
        replacement._lock_handle.close()
        replacement._lock_handle = None


@pytest.mark.parametrize("field,value", [
    ("lease_seq", True), ("epoch", True), ("active_full", True),
    ("forwarding_full", True), ("updated_at", True),
])
def test_mode_schema_rejects_bool_as_integer(tmp_path, field, value):
    app = prepare(tmp_path)
    mode = json.loads(app.mode_path.read_text())
    mode[field] = value
    supervisor._atomic_json(app.mode_path, mode)
    with pytest.raises(supervisor.SupervisorError, match="mode_invalid"):
        supervisor.read_mode(app.mode_path, app.host_boot)


def test_host_proxy_ingress_proves_all_three_exact_process_owned_listeners():
    fact = FakeInspector().proxy
    entry = {"bind": "0.0.0.0", "tls_port": 8443, "plain_port": 8000,
             "admin_bind": "127.0.0.1", "admin_port": 19090}
    with patch.object(supervisor, "_container_cgroup_pids", return_value={201}), \
            patch.object(supervisor, "_prove_owned_listeners") as prove:
        supervisor.prove_proxy_ingress(fact, entry)
    assert prove.call_args_list == [
        call(8443, {"0.0.0.0"}, {201}),
        call(8000, {"0.0.0.0"}, {201}),
        call(19090, {"127.0.0.1"}, {201}),
    ]


def test_bridge_control_allows_original_network_only_when_alternate_binds_maintenance_ip():
    inspector = FakeInspector()
    control = supervisor.ContainerFact(
        inspector.control.container_id, inspector.control.image_id,
        inspector.control.started_at, inspector.control.pid, 0, "bridge",
        (("engine-net", "172.18.0.2"), ("maintenance-net", "172.31.0.2")),
        (("io.mdd-sim-gateway.maintenance-upstream", "true"),), (), "5" * 64)
    proxy_fact = supervisor.ContainerFact(
        inspector.proxy.container_id, inspector.proxy.image_id,
        inspector.proxy.started_at, inspector.proxy.pid, 0, "bridge",
        (("maintenance-net", "172.31.0.3"),),
        (("io.mdd-sim-gateway.component", "maintenance-proxy"),),
        (("8443/tcp", "0.0.0.0", "8443"),
         ("8000/tcp", "0.0.0.0", "8000")), "4" * 64)
    value = manifest()
    value["rollback_upstream"].update({"tls_host": "172.31.0.2",
                                       "plain_host": "172.31.0.2"})
    inspector.network = Mock(return_value={
        "Labels": {"io.mdd-sim-gateway.maintenance": "true"},
        "Containers": {control.container_id: {}, proxy_fact.container_id: {}},
    })
    with patch.object(supervisor, "_container_cgroup_pids", return_value={101}), \
            patch.object(supervisor, "_listeners",
                         side_effect=lambda _port, _pid: [("172.31.0.2", "55")]), \
            patch.object(supervisor, "_socket_inode_owners", return_value={101}):
        supervisor.prove_bridge(value, proxy_fact, control, inspector)

    with patch.object(supervisor, "_container_cgroup_pids", return_value={101}), \
            patch.object(supervisor, "_listeners",
                         side_effect=lambda _port, _pid: [("0.0.0.0", "55")]), \
            patch.object(supervisor, "_socket_inode_owners", return_value={101}), \
            pytest.raises(supervisor.SupervisorError, match="control_listener_missing"):
        supervisor.prove_bridge(value, proxy_fact, control, inspector)


def test_bridge_proxy_without_userland_proxy_fails_with_actionable_preflight_code():
    fact = FakeInspector().proxy
    bridged = supervisor.ContainerFact(
        fact.container_id, fact.image_id, fact.started_at, fact.pid, fact.restart_count,
        "bridge", (("maintenance-net", "172.31.0.3"),), fact.labels,
        (("8443/tcp", "0.0.0.0", "8443"),
         ("8000/tcp", "0.0.0.0", "8000")), fact.create_spec_hash)
    entry = {"bind": "0.0.0.0", "tls_port": 8443, "plain_port": 8000,
             "admin_bind": "172.31.0.3", "admin_port": 19090}
    with patch.object(supervisor, "_listeners", side_effect=lambda port, _pid: (
            [("172.31.0.3", "55")] if port == 19090 else [])), \
            patch.object(supervisor, "_container_cgroup_pids", return_value={201}), \
            patch.object(supervisor, "_socket_inode_owners", return_value={201}), \
            pytest.raises(supervisor.SupervisorError,
                          match="proxy_bridge_userland_proxy_required"):
        supervisor.prove_proxy_ingress(bridged, entry)


def test_orchestrator_signal_path_only_stops_supervisor_without_revoke_io(tmp_path):
    from host.mdd_orchestrator import Orchestrator

    app = Orchestrator(tmp_path, Path.cwd(), dry_run=True)
    managed = Mock()
    authority = Mock()
    app._maintenance_supervisor = managed
    app._admission_authority = authority
    app.request_stop()
    managed.stop.assert_called_once_with()
    managed.revoke.assert_not_called()
    authority.stop.assert_called_once_with()
