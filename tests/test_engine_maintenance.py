import json
import os
import asyncio
import threading
import tempfile
from types import SimpleNamespace
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from fastapi.testclient import TestClient

from control.app import engine, main


def _create_spec(iid="7"):
    base = Path(engine.HOST_DATA_DIR) / "instances" / str(iid)
    return {
        "version": 1, "instance": str(iid),
        "environment": {"MDD_ID": str(iid), "SWU_LIVENESS_PERIOD": "0"},
        "binds": [
            {"host": str(base / "instance.json"),
             "container": "/config/instance.json", "mode": "ro"},
            {"host": str(base / "logs"), "container": "/logs", "mode": "rw"},
            {"host": str(base / "run"),
             "container": "/run/mdd-sim-gateway", "mode": "rw"},
            {"host": str(engine.PCSCD_SOCK), "container": "/run/pcscd", "mode": "rw"},
        ],
        "ports": [
            {"container_port": "8089/tcp", "host_ip": "127.0.0.1",
             "host_port": 8089},
            {"container_port": "10000/udp", "host_ip": "", "host_port": 10000},
        ],
        "devices": [{"host": "/dev/net/tun", "container": "/dev/net/tun",
                     "permissions": "rwm"}], "privileged": True,
        "restart_policy": {"Name": "unless-stopped", "MaximumRetryCount": 0},
        "network_mode": "bridge", "extra_hosts": ["host.docker.internal:host-gateway"],
        "sysctls": dict(engine._CREATE_SPEC_SYSCTLS),
        "labels": {engine.MANAGED_LABEL: "true",
                   "io.mdd-sim-gateway.component": "engine"},
    }


def _record(iid="7", **updates):
    spec = _create_spec(iid)
    value = {
        "version": 1,
        "txid": "deploy-20260823-0001",
        "instance": str(iid),
        "phase": "prepared",
        "source": {
            "container_id": "a" * 64,
            "started_at": "2026-08-22T15:48:33.000000000Z",
            "restart_count": 0,
            "pid": 321,
            "run_id": "run-old-7",
            "run_id_mode": "present",
            "image_id": "sha256:" + "b" * 64,
        },
        "target_image_digest": "sha256:" + "c" * 64,
        "target": None,
        "rollback": None,
        "attempts": 0,
        "manual_required": False,
        "source_create_spec": spec,
        "source_create_spec_digest": engine._canonical_digest(spec),
        "rollback_image_ref": (
            "mdd-sim-gateway/engine-rollback:deploy-20260823-0001-" + str(iid)),
    }
    value.update(updates)
    return value


def _roots(tmp_path):
    return patch.multiple(
        engine, DATA_DIR=str(tmp_path), HOST_DATA_DIR=str(tmp_path))


def test_engine_maintenance_roundtrip_is_strict_and_durable(tmp_path):
    with _roots(tmp_path):
        written = engine.write_engine_maintenance("7", _record())
        path = tmp_path / "instances" / "7" / "run" / engine.ENGINE_MAINTENANCE_NAME
        assert path.exists()
        assert oct(path.stat().st_mode & 0o777) == "0o600"
        assert engine.engine_maintenance_pending("7") is True
        assert engine.read_engine_maintenance("7") == written


def test_corrupt_or_wrong_identity_marker_fails_closed(tmp_path):
    with _roots(tmp_path):
        path = tmp_path / "instances" / "7" / "run" / engine.ENGINE_MAINTENANCE_NAME
        path.parent.mkdir(parents=True)
        path.write_text("{broken", encoding="utf-8")
        assert engine.engine_maintenance_pending("7") is True
        with pytest.raises(engine.MaintenanceStateError):
            engine.read_engine_maintenance("7")

        path.write_text(json.dumps(_record(instance="8")), encoding="utf-8")
        with pytest.raises(engine.MaintenanceStateError):
            engine.read_engine_maintenance("7")


@pytest.mark.parametrize("updates", [
    {"phase": "unknown"},
    {"target_image_digest": "latest"},
    {"attempts": -1},
    {"manual_required": True},
    {"version": True},
])
def test_invalid_records_are_never_written(tmp_path, updates):
    with _roots(tmp_path), pytest.raises(engine.MaintenanceStateError):
        engine.write_engine_maintenance("7", _record(**updates))


def test_clear_runtime_preserves_maintenance_fence(tmp_path):
    with _roots(tmp_path):
        base = tmp_path / "instances" / "7"
        run = base / "run"
        run.mkdir(parents=True)
        engine.write_engine_maintenance("7", _record())
        (run / "engine-run-id").write_text("old", encoding="utf-8")
        (run / "swu_status.json").write_text("{}", encoding="utf-8")

        engine._clear_runtime_state(str(base))

        assert (run / engine.ENGINE_MAINTENANCE_NAME).exists()
        assert not (run / "engine-run-id").exists()
        assert not (run / "swu_status.json").exists()


def test_global_marker_existence_is_fail_closed_without_creating_directories(tmp_path):
    with _roots(tmp_path):
        assert engine.global_maintenance_pending() is False
        marker = tmp_path / "orchestrator" / engine.CONTROL_UPGRADE_NAME
        marker.parent.mkdir()
        marker.write_text("not json", encoding="utf-8")
        assert engine.global_maintenance_pending() is True
        assert engine.engine_maintenance_pending("does-not-exist") is False
        assert not (tmp_path / "instances" / "does-not-exist").exists()

        committed = {
            "version": 1, "txid": "deploy-20260823-0001",
            "phase": "rollback_committed", "owner": {}, "source_control": {},
            "rollback_control": {},
            "proxy": {"container_id": "a" * 64,
                      "image_id": "sha256:" + "b" * 64},
            "rollback_upstream": {},
            "lines": [{"phase": "rollback_verified"}],
        }
        marker.write_text(json.dumps(committed), encoding="utf-8")
        assert engine.global_maintenance_pending() is False
        fence = marker.parent / engine.CONTROL_MAINTENANCE_FENCE_NAME
        fence.write_text("damaged but fail closed", encoding="utf-8")
        assert engine.global_maintenance_pending() is True
        fence.unlink()
        assert engine.global_maintenance_pending() is False
        committed["lines"][0]["phase"] = "rollback_started"
        marker.write_text(json.dumps(committed), encoding="utf-8")
        assert engine.global_maintenance_pending() is True


def test_pcscf_boundary_rechecks_global_fence_after_shared_lock_acquisition():
    handle = MagicMock()

    async def scenario():
        with patch.object(engine, "acquire_pcscf_admission", return_value=handle), \
                patch.object(main, "_durable_maintenance_pending", return_value=True), \
                patch.object(engine, "release_pcscf_admission") as release:
            async with main._pcscf_admission_boundary("7") as admitted:
                assert admitted is False
            release.assert_called_once_with(handle)

    asyncio.run(scenario())


def test_maintenance_lock_is_nonblocking_and_reusable(tmp_path):
    with _roots(tmp_path):
        with engine.engine_maintenance_locked("7"):
            with pytest.raises(BlockingIOError):
                with engine.engine_maintenance_locked("7", blocking=False):
                    pass
        with engine.engine_maintenance_locked("7", blocking=False):
            pass


def _container(facts, channels=0):
    container = MagicMock()
    container.id = facts["container_id"]
    container.name = "mdd-sim-gateway-engine-7"
    container.status = "running"
    container.attrs = {
        "Config": {"Labels": {engine.MANAGED_LABEL: "true"}},
        "State": {"Status": "running", "StartedAt": facts["started_at"],
                  "Pid": facts["pid"]},
        "RestartCount": facts["restart_count"], "Image": facts["image_id"],
    }
    container.exec_run.return_value = (
        0, f"{channels} active channels\n1 active call\n".encode())
    return container


def test_exact_phase_cas_requires_absence_and_new_target_generation(tmp_path):
    source = _record()["source"]
    target = {
        "container_id": "d" * 64,
        "started_at": "2026-08-23T01:30:00.000000000Z",
        "restart_count": 0,
        "pid": 654,
        "run_id": "run-new-7",
        "run_id_mode": "present",
        "image_id": "sha256:" + "c" * 64,
    }
    current = {"container": _container(source)}

    class Containers:
        def get(self, _name):
            if current["container"] is None:
                raise engine.docker.errors.NotFound("absent")
            return current["container"]

    client = SimpleNamespace(containers=Containers())
    with _roots(tmp_path), patch.object(engine, "_client", return_value=client), \
            patch.object(engine, "capture_engine_create_spec",
                         return_value=_create_spec("7")):
        run = tmp_path / "instances" / "7" / "run"
        run.mkdir(parents=True)
        (run / "engine-run-id").write_text(source["run_id"], encoding="utf-8")
        begun = engine.begin_engine_maintenance(
            "7", "deploy-20260823-0001", source["container_id"],
            "sha256:" + "c" * 64,
            "mdd-sim-gateway/engine-rollback:deploy-20260823-0001-7")
        assert begun["phase"] == "prepared"
        engine.transition_engine_maintenance(
            "7", begun["txid"], "prepared", "source_quiescing")
        with pytest.raises(engine.MaintenanceStateError, match="absent"):
            engine.transition_engine_maintenance(
                "7", begun["txid"], "source_quiescing", "source_removed")

        current["container"] = None
        engine.transition_engine_maintenance(
            "7", begun["txid"], "source_quiescing", "source_removed")
        engine.transition_engine_maintenance(
            "7", begun["txid"], "source_removed", "target_starting")
        current["container"] = _container(target)
        (run / "engine-run-id").write_text(target["run_id"], encoding="utf-8")
        started = engine.transition_engine_maintenance(
            "7", begun["txid"], "target_starting", "target_started")
        assert started["target"] == target
        verified = engine.transition_engine_maintenance(
            "7", begun["txid"], "target_started", "verified",
            target=target)
        assert verified["phase"] == "verified"
        current["container"].attrs["Config"]["Labels"].update({
            engine.ENGINE_REPLACEMENT_TX_LABEL: begun["txid"],
            engine.ENGINE_REPLACEMENT_INTENT_LABEL: "target",
            engine.ENGINE_REPLACEMENT_SOURCE_SPEC_LABEL:
                begun["source_create_spec_digest"],
        })
        engine._write_engine_start_receipt("7", {
            "version": 1, "instance": "7", "txid": begun["txid"],
            "intent": "target", "phase": "created",
            "image_id": target["image_id"],
            "source_create_spec_digest": begun["source_create_spec_digest"],
            "attestation": "tx_label", "container_id": target["container_id"],
            "generation": None, "attempts": 1, "created_at": 1787661000.0,
            "updated_at": 1787661001.0,
        })
        real_clear_receipt = engine.clear_engine_start_receipt
        with patch.object(engine, "clear_engine_start_receipt",
                          side_effect=RuntimeError("simulated cleanup crash")), \
                pytest.raises(RuntimeError, match="simulated cleanup crash"):
            engine.clear_engine_maintenance("7", begun["txid"], "verified")
        assert engine.engine_maintenance_pending("7") is False
        assert engine.read_engine_start_receipt("7")["phase"] == "clearing"
        with patch.object(engine, "clear_engine_start_receipt",
                          side_effect=real_clear_receipt):
            engine.clear_engine_maintenance("7", begun["txid"], "verified")
        assert engine.engine_maintenance_pending("7") is False
        assert engine.read_engine_start_receipt("7") is None


def test_begin_fails_closed_on_active_or_changed_source(tmp_path):
    source = _record()["source"]
    container = _container(source, channels=1)
    client = SimpleNamespace(containers=SimpleNamespace(get=lambda _name: container))
    with _roots(tmp_path), patch.object(engine, "_client", return_value=client), \
            patch.object(engine, "capture_engine_create_spec",
                         return_value=_create_spec("7")):
        run = tmp_path / "instances" / "7" / "run"
        run.mkdir(parents=True)
        (run / "engine-run-id").write_text(source["run_id"], encoding="utf-8")
        with pytest.raises(engine.MaintenanceStateError, match="active call"):
            engine.begin_engine_maintenance(
                "7", "deploy-20260823-0001", source["container_id"],
                "sha256:" + "c" * 64,
                "mdd-sim-gateway/engine-rollback:deploy-20260823-0001-7")
        assert engine.engine_maintenance_pending("7") is False


def test_legacy_source_without_run_id_is_explicit_but_new_generations_require_one(tmp_path):
    source = _record()["source"]
    source["run_id"], source["run_id_mode"] = "", "legacy_absent"
    container = _container(source)
    client = SimpleNamespace(containers=SimpleNamespace(get=lambda _name: container))
    with _roots(tmp_path), patch.object(engine, "_client", return_value=client), \
            patch.object(engine, "capture_engine_create_spec",
                         return_value=_create_spec("7")):
        (tmp_path / "instances" / "7" / "run").mkdir(parents=True)
        begun = engine.begin_engine_maintenance(
            "7", "deploy-20260823-0001", source["container_id"],
            "sha256:" + "c" * 64,
            "mdd-sim-gateway/engine-rollback:deploy-20260823-0001-7")
        assert begun["source"]["run_id_mode"] == "legacy_absent"
        with pytest.raises(engine.MaintenanceStateError, match="run id"):
            engine.engine_generation_facts("7")


def test_abort_and_rollback_have_explicit_safe_terminal_phases(tmp_path):
    source = _record()["source"]
    current = {"container": _container(source)}

    class Containers:
        def get(self, _name):
            if current["container"] is None:
                raise engine.docker.errors.NotFound("absent")
            return current["container"]

    client = SimpleNamespace(containers=Containers())
    with _roots(tmp_path), patch.object(engine, "_client", return_value=client):
        run = tmp_path / "instances" / "7" / "run"
        run.mkdir(parents=True)
        (run / "engine-run-id").write_text(source["run_id"], encoding="utf-8")
        aborted = _record(phase="rollback_required")
        engine.write_engine_maintenance("7", aborted)
        terminal = engine.transition_engine_maintenance(
            "7", aborted["txid"], "rollback_required", "aborted")
        assert terminal["phase"] == "aborted"
        engine.clear_engine_maintenance("7", aborted["txid"], "aborted")

        current["container"] = None
        rollback_record = _record(phase="source_removed")
        engine.write_engine_maintenance("7", rollback_record)
        engine.transition_engine_maintenance(
            "7", rollback_record["txid"], "source_removed", "rollback_starting")
        rollback = {
            **source, "container_id": "9" * 64,
            "started_at": "2026-08-23T02:00:00Z", "pid": 777,
            "run_id": "rollback-run-7", "run_id_mode": "present",
        }
        current["container"] = _container(rollback)
        (run / "engine-run-id").write_text(rollback["run_id"], encoding="utf-8")
        started = engine.transition_engine_maintenance(
            "7", rollback_record["txid"], "rollback_starting", "rollback_started")
        assert started["rollback"] == rollback
        verified = engine.transition_engine_maintenance(
            "7", rollback_record["txid"], "rollback_started", "rollback_verified",
            rollback=rollback)
        assert verified["phase"] == "rollback_verified"
        engine.clear_engine_maintenance(
            "7", rollback_record["txid"], "rollback_verified")


def test_start_absent_never_removes_or_clears_an_existing_name(tmp_path):
    existing = _container(_record()["source"])
    inspected = SimpleNamespace(
        id="sha256:" + "c" * 64,
            attrs={"Config": {"Labels": {
                engine.ENGINE_ADMISSION_ABI_LABEL: engine.ENGINE_ADMISSION_ABI,
                engine.ENGINE_MEDIA_WEBSOCKET_LABEL: engine.ENGINE_MEDIA_WEBSOCKET_ABI,
                engine.ENGINE_BROWSER_OUTBOUND_LABEL: engine.ENGINE_BROWSER_OUTBOUND_ABI,
        }}},
    )
    client = SimpleNamespace(
        containers=SimpleNamespace(get=lambda _name: existing),
        images=SimpleNamespace(get=lambda _image: inspected),
    )
    with _roots(tmp_path), patch.object(engine, "_client", return_value=client), \
            patch.object(engine.egress, "ensure_line", return_value={"ready": True}), \
            patch.object(engine.cfg, "write_instance_json"), \
            patch.object(engine, "_clear_runtime_state") as clear:
        marker = _record(phase="target_starting")
        engine.write_engine_maintenance("7", marker)
        with pytest.raises(engine.EngineAlreadyExists):
            engine.start_absent(
                {"id": "7", "ports": {"webrtc": 8089, "rtp_start": 10000,
                                        "rtp_span": 1}}, {},
                "sha256:" + "c" * 64, marker["txid"])
    existing.remove.assert_not_called()
    clear.assert_not_called()


def test_start_absent_requires_exact_txid_phase_and_digest(tmp_path):
    with _roots(tmp_path), patch.object(engine, "_start_container") as start:
        marker = _record(phase="target_starting")
        engine.write_engine_maintenance("7", marker)
        inst = {"id": "7"}
        for txid, digest in [
                ("another-transaction-0001", marker["target_image_digest"]),
                (marker["txid"], "sha256:" + "9" * 64)]:
            with pytest.raises(engine.MaintenanceStateError, match="ownership"):
                engine.start_absent(inst, {}, digest, txid)
        saved = engine.read_engine_maintenance("7")
        saved["phase"] = "source_removed"
        engine.write_engine_maintenance("7", saved)
        with pytest.raises(engine.MaintenanceStateError, match="ownership"):
            engine.start_absent(
                inst, {}, marker["target_image_digest"], marker["txid"])
    start.assert_not_called()


def test_dialplan_has_final_maintenance_guards_before_paid_or_persistent_work():
    text = (Path(__file__).parents[1] / "engine" / "templates" /
            "extensions.conf.j2").read_text(encoding="utf-8")
    guard = "STAT(e,/run/mdd-sim-gateway/engine-maintenance.json)"
    assert text.count(guard) == 2

    incoming_call = text.split("[volte_ims]", 1)[1].split("[volte_ims_msg]", 1)[0]
    incoming_sms = text.split("[volte_ims_msg]", 1)[1].split(
        "[browser-media-canary]", 1)[0]
    local_call = text.split("exten => _[+0-9].,1", 1)[1].split(
        "[ims-outbound-headers]", 1)[0]
    local_sms = text.split("[msg-from-local]", 1)[1]
    assert incoming_call.index(guard) < incoming_call.index("FILE(/logs/calls.txt")
    # MT was committed in the patched PJSIP pre-200 hook; a second short-lease check here
    # would discard carrier-acknowledged work while it waits in the dialplan queue.
    assert guard not in incoming_sms
    # AMI-originated media WebSocket threads correctly block escalating STAT(). The custom
    # Engine-local gate synchronously checks both markers and authority on every native call.
    local_gate = 'MDD_ADMISSION(call_out)'
    assert local_call.index(local_gate) < local_call.index("notify.py call_out")
    assert local_call.index(local_gate) < local_call.index("Dial(PJSIP/${EXTEN}@volte_ims")
    assert local_sms.index(guard) < local_sms.index("FILE(/logs/messages.txt")


@pytest.mark.asyncio
async def test_line_admission_and_status_sampler_fail_closed_on_marker():
    inst = {"id": "7", "enabled": False}
    main.hub.status_cache.pop("7", None)
    main.hub.status_sampled_at.pop("7", None)
    with patch.object(engine, "global_maintenance_pending", return_value=False), \
            patch.object(engine, "engine_maintenance_pending", return_value=True), \
            patch.object(main.hub.runtime, "get", new=AsyncMock()) as runtime, \
            patch.object(engine, "stop") as stop, \
            patch.object(main.hub, "broadcast", new=AsyncMock()) as broadcast:
        assert await main._line_admission_blocked("7") is True
        await main._poll_instance_status(inst)

    runtime.assert_not_awaited()
    stop.assert_not_called()
    assert main.hub.status_cache["7"]["reason_code"] == "maintenance_rebuild"
    assert main.hub.status_cache["7"]["detail"]["maintenance_pending"] is True
    broadcast.assert_awaited_once()
    main.hub.status_cache.pop("7", None)
    main.hub.status_sampled_at.pop("7", None)
    main.hub.health.pop("7", None)


def test_start_checked_never_calls_engine_while_fenced():
    with patch.object(engine, "global_maintenance_pending", return_value=False), \
            patch.object(engine, "engine_maintenance_pending", return_value=True), \
            patch.object(engine, "start") as start:
        with pytest.raises(main.HTTPException) as raised:
            main._start_engine_checked({"id": "7"}, {})
    assert raised.value.status_code == 409
    start.assert_not_called()


def test_http_mutation_fence_allows_hangup_and_authenticated_engine_drain():
    app = main.app
    with patch.object(engine, "global_maintenance_pending", return_value=True), \
            patch("control.app.auth.session", return_value={
                "user": "admin", "csrf": "test-csrf"}), \
            patch.object(main.cfg, "internal_event_token", return_value="event-token"), \
            patch.object(main, "hangup_on_line", new=AsyncMock(return_value={"ok": True})), \
            patch.object(main, "api_engine_event", new=AsyncMock(return_value={"ok": True})):
        client = TestClient(app, cookies={"mdd_session": "test-session"})
        blocked = client.post(
            "/api/instances/7/start",
            headers={"x-mdd-csrf-token": "test-csrf"}, json={})
        hangup = client.post(
            "/api/instances/7/hangup",
            headers={"x-mdd-csrf-token": "test-csrf"}, json={})
        release = client.post(
            "/api/instances/7/cellular-call/missing-call/release",
            headers={"x-mdd-csrf-token": "test-csrf"}, json={})
        answer = client.post(
            "/api/instances/7/cellular-call/answer",
            headers={"x-mdd-csrf-token": "test-csrf"}, json={})

    assert blocked.status_code == 503
    assert blocked.json()["detail"]["code"] == "maintenance_in_progress"
    assert hangup.status_code == 200
    assert release.status_code == 200
    assert answer.status_code == 503


@pytest.mark.asyncio
async def test_event_status_paths_drain_without_lifecycle_mutation_during_maintenance():
    with patch.object(main, "_durable_maintenance_pending", return_value=True), \
            patch.object(main, "_reconcile_pcscf_rebind", new=AsyncMock()) as reconcile, \
            patch.object(main.hub.runtime, "get", new=AsyncMock()) as runtime, \
            patch.object(main.hub, "ami_for", new=AsyncMock()) as ami, \
            patch.object(main.hub, "broadcast", new=AsyncMock()) as broadcast, \
            patch.object(main.cfg, "get_instance", return_value={"id": "7"}):
        event = await main.api_engine_event({
            "instance": "7", "event": "pcscf_rebind", "args": ["fd00::2"],
            "engine_run_id": "run-7"})
        await main.push_status("7")

    assert event == {"ok": True, "accepted": False, "reason": "maintenance_deferred"}
    reconcile.assert_not_awaited()
    runtime.assert_not_awaited()
    ami.assert_not_awaited()
    assert any(call.args[0].get("maintenance_deferred")
               for call in broadcast.await_args_list)
    assert any(call.args[0].get("detail", {}).get("maintenance_pending")
               for call in broadcast.await_args_list)
    main.hub.status_cache.pop("7", None)
    main.hub.status_sampled_at.pop("7", None)
    main.hub.health.pop("7", None)


@pytest.mark.asyncio
async def test_cellular_paid_paths_have_final_shared_admission_gate():
    @main.asynccontextmanager
    async def denied(_iid):
        yield False

    with patch.object(main, "_maintenance_submission_boundary", new=denied), \
            patch.object(main.cfg, "list_instances") as instances, \
            patch.object(main.remote_modem, "attached_iccid", return_value="iccid") as attached, \
            patch.object(main.remote_modem, "invoke", new=AsyncMock()) as invoke, \
            patch.object(main.cellular_call, "dial") as dial, \
            patch.object(main.store, "save_cellular_call_lease") as lease, \
            patch.object(main.cfg, "get_instance", return_value={
                "id": "7", "iccid": "test-iccid"}):
        sms = await main._send_sms_cellular("7", "+44123", "hello")
        attached.return_value = ""
        with pytest.raises(main.HTTPException) as call:
            await main.api_cellular_call("7", {"to": "+44123"})

    assert sms["status"] == "maintenance"
    assert call.value.status_code == 503
    assert instances.call_count == 2
    invoke.assert_not_awaited()
    dial.assert_not_called()
    lease.assert_not_called()


@pytest.mark.asyncio
async def test_remote_sms_persists_pending_before_rpc_and_final_state_before_unlock():
    order = []

    @main.asynccontextmanager
    async def admitted(_iid):
        order.append("lock")
        yield True
        order.append("unlock")

    def add_message(*_args, **_kwargs):
        order.append("pending")
        return {"id": 5, "status": "pending"}

    async def invoke(*_args, **_kwargs):
        order.append("rpc")
        return {"ok": True, "status": "sent"}

    def set_status(*_args, **_kwargs):
        order.append("final")

    with patch.object(main, "_maintenance_submission_boundary", new=admitted), \
            patch.object(main.cfg, "list_instances", return_value=[{"id": "7"}]), \
            patch.object(main.remote_modem, "attached_iccid", return_value="iccid"), \
            patch.object(main.remote_modem, "invoke", new=AsyncMock(side_effect=invoke)), \
            patch.object(main.store, "add_message", side_effect=add_message), \
            patch.object(main.store, "set_message_status", side_effect=set_status), \
            patch.object(main.hub, "broadcast", new=AsyncMock()):
        result = await main._send_sms_cellular("7", "+44123", "hello")

    assert result["ok"] is True
    assert order == ["lock", "pending", "rpc", "final", "unlock"]


@pytest.mark.asyncio
async def test_remote_sms_unavailable_closes_pending_before_unlock():
    order = []

    @main.asynccontextmanager
    async def admitted(_iid):
        order.append("lock")
        yield True
        order.append("unlock")

    def add_message(*_args, **_kwargs):
        order.append("pending")
        return {"id": 9, "status": "pending"}

    def set_status(_mid, status, _error):
        order.append(status)

    with patch.object(main, "_maintenance_submission_boundary", new=admitted), \
            patch.object(main.cfg, "list_instances", return_value=[{"id": "7"}]), \
            patch.object(main.remote_modem, "attached_iccid", return_value="iccid"), \
            patch.object(main.remote_modem, "invoke",
                         new=AsyncMock(side_effect=main.ModemUnavailable("offline"))), \
            patch.object(main.store, "add_message", side_effect=add_message), \
            patch.object(main.store, "set_message_status", side_effect=set_status), \
            patch.object(main.hub, "broadcast", new=AsyncMock()):
        result = await main._send_sms_cellular("7", "+44123", "hello")

    assert result["unavailable"] is True
    assert order == ["lock", "pending", "failed", "unlock"]


@pytest.mark.asyncio
async def test_local_sms_cancel_does_not_release_worker_maintenance_flock():
    started = threading.Event()
    finish = threading.Event()
    order = []

    def send(*_args, **_kwargs):
        order.append("send")
        started.set()
        assert finish.wait(5)
        return {"ok": True, "status": "sent", "uncertain": False,
                "unavailable": False, "error": None}

    def add_message(*_args, **_kwargs):
        order.append("pending")
        return {"id": 11, "instance": "7", "direction": "out", "peer": "+44123",
                "body": "hello", "status": "pending", "error": None,
                "transport": "cellular"}

    def set_status(_mid, status, _error):
        order.append(status)

    with tempfile.TemporaryDirectory() as directory, \
            patch.object(engine, "DATA_DIR", directory), \
            patch.object(engine, "HOST_DATA_DIR", directory), \
            patch.object(main, "_durable_maintenance_pending", return_value=False), \
            patch.object(main.cellular_sms, "send", side_effect=send), \
            patch.object(main.store, "add_message", side_effect=add_message), \
            patch.object(main.store, "set_message_status", side_effect=set_status), \
            patch.object(main.hub, "broadcast", new=AsyncMock()):
        worker = asyncio.create_task(main._send_local_sms_guarded(
            "7", "+44123", "hello", [{"id": "7"}]))

        async def request_waiter():
            return await asyncio.shield(worker)

        request = asyncio.create_task(request_waiter())
        assert await asyncio.to_thread(started.wait, 2)
        request.cancel()
        request.cancel()
        with pytest.raises(asyncio.CancelledError):
            await request

        def can_take_lock():
            try:
                with engine.engine_maintenance_locked("7", blocking=False):
                    return True
            except BlockingIOError:
                return False

        assert await asyncio.to_thread(can_take_lock) is False
        assert order[:2] == ["pending", "send"]
        finish.set()
        result = await asyncio.wait_for(worker, 2)
        assert result["ok"] is True
        assert order == ["pending", "send", "sent"]
        assert await asyncio.to_thread(can_take_lock) is True


@pytest.mark.asyncio
async def test_public_sms_cancel_blocks_same_and_cross_transport_retry():
    started = asyncio.Event()
    finish = asyncio.Event()
    cellular = AsyncMock()
    vowifi = AsyncMock()

    async def submit(*_args, **_kwargs):
        started.set()
        await finish.wait()
        return {"ok": True, "status": "sent", "message": {"id": 1}}

    cellular.side_effect = submit
    main.hub.sms_submission_tasks.pop("7", None)
    with patch.object(main, "_send_sms_cellular", cellular), \
            patch.object(main, "_send_sms_vowifi", vowifi), \
            patch.object(main.store, "begin_sms_submission", return_value={
                "created": True, "conflict": False}), \
            patch.object(main.store, "finish_sms_submission", return_value=True):
        first = asyncio.create_task(
            main.send_sms_on_line("7", "+44123", "hello", "cellular"))
        await asyncio.wait_for(started.wait(), 1)
        first.cancel()
        await asyncio.gather(first, return_exceptions=True)

        same = await main.send_sms_on_line("7", "+44123", "hello", "cellular")
        cross = await main.send_sms_on_line("7", "+44123", "hello", "vowifi")
        assert same["status"] in {"busy", "unknown"}
        assert cross["status"] in {"busy", "unknown"}
        assert cellular.await_count == 1
        vowifi.assert_not_awaited()

        finish.set()
        owner = main.hub.sms_submission_tasks["7"]["task"]
        await asyncio.wait_for(owner, 1)
        after_done = await main.send_sms_on_line("7", "+44123", "hello", "auto")
        assert after_done["status"] == "unknown"
        assert cellular.await_count == 1
        vowifi.assert_not_awaited()
    main.hub.sms_submission_tasks.pop("7", None)


@pytest.mark.asyncio
async def test_sms_cancel_after_owner_done_still_publishes_orphan_tombstone():
    delivered = asyncio.Event()
    release_delivery = asyncio.Event()
    real_shield = asyncio.shield

    async def delayed_shield(awaitable):
        result = await real_shield(awaitable)
        delivered.set()
        await release_delivery.wait()
        return result

    main.hub.sms_submission_tasks.pop("7", None)
    with patch.object(main, "_send_sms_on_line_owned", new=AsyncMock(return_value={
                "ok": True, "status": "sent", "message": {"id": 2}})), \
            patch.object(main.asyncio, "shield", side_effect=delayed_shield), \
            patch.object(main.store, "begin_sms_submission", return_value={
                "created": True, "conflict": False}), \
            patch.object(main.store, "finish_sms_submission", return_value=True):
        request = asyncio.create_task(
            main.send_sms_on_line("7", "+44123", "hello", "cellular"))
        await asyncio.wait_for(delivered.wait(), 1)
        request.cancel()
        await asyncio.gather(request, return_exceptions=True)
        entry = main.hub.sms_submission_tasks.get("7")
        assert entry and entry["task"].done() and entry["orphaned"] is True
        retry = await main.send_sms_on_line("7", "+44123", "hello", "vowifi")
        assert retry["status"] == "unknown"
    release_delivery.set()
    main.hub.sms_submission_tasks.pop("7", None)


@pytest.mark.asyncio
async def test_ack_response_loss_replays_receipt_without_submitting_again():
    operation_id = "12345678-1234-4234-9234-123456789abc"
    submit = AsyncMock()
    with patch.object(main, "_send_sms_on_line_owned", new=submit), \
            patch.object(main.store, "begin_sms_submission", return_value={
                "created": False, "conflict": False, "acknowledged": True,
                "state": "acknowledged", "result": {
                    "ok": True, "status": "sent", "message": {"id": 8}}}):
        result = await main.send_sms_on_line(
            "7", "+44123", "hello", "cellular", operation_id)
    assert result["ok"] is True
    assert result["submission_acknowledged"] is True
    assert result["replayed_result"] is True
    submit.assert_not_awaited()


@pytest.mark.asyncio
async def test_stale_health_and_disabled_stop_recheck_marker_inside_recovery_lock():
    sampled = {"state": "ERROR", "reason_code": "failed", "reason": "failed",
               "detail": {}}
    with patch.object(main, "apply_health", return_value={
                **sampled, "_engine_recovery": {"fast_unanswered": False}}), \
            patch.object(main, "_durable_maintenance_pending", return_value=True), \
            patch.object(main.engine, "capture_and_stop_if_idle") as capture:
        result = await main._apply_health_with_recovery(
            "7", {"id": "7"}, sampled, "container-7")
    assert result["reason_code"] == "maintenance_rebuild"
    capture.assert_not_called()

    disabled = {"id": "7", "enabled": False}
    with patch.object(main, "_durable_maintenance_pending", side_effect=[False, True]), \
            patch.object(main.hub.runtime, "get", new=AsyncMock(return_value={
                "running": True, "container_id": "container-7"})), \
            patch.object(main.cfg, "get_instance", return_value=disabled), \
            patch.object(main.engine, "stop") as stop, \
            patch.object(main.hub, "broadcast", new=AsyncMock()):
        await main._poll_instance_status(disabled)
    stop.assert_not_called()
    main.hub.status_cache.pop("7", None)
    main.hub.status_sampled_at.pop("7", None)
    main.hub.health.pop("7", None)
