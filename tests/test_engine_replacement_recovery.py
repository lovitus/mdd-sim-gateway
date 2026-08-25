import json
from contextlib import nullcontext
from types import SimpleNamespace
from unittest.mock import Mock, patch

import pytest

from control.app import engine
from host.mdd_engine_replacement import (
    EngineReplacement, read_manifest, validate_manifest, _atomic_json)


TXID = "engine-replace-incident-0001"
CANDIDATE = "sha256:" + "c" * 64


def facts(iid, *, generation="source"):
    salt = iid if generation == "source" else "9"
    return {
        "container_id": salt * 64, "image_id": "sha256:" + "b" * 64,
        "started_at": ("2026-08-25T11:00:20Z" if generation == "source"
                       else "2026-08-25T11:10:20Z"),
        "restart_count": 0, "pid": 100 + int(iid),
        "run_id": f"run-{generation}-{iid}", "run_id_mode": "present",
    }


def create_spec(root, iid):
    base = root / "instances" / iid
    return {
        "version": 1, "instance": iid,
        "environment": {"MDD_ID": iid, "SWU_LIVENESS_PERIOD": "0"},
        "binds": [
            {"host": str(base / "instance.json"),
             "container": "/config/instance.json", "mode": "ro"},
            {"host": str(base / "logs"), "container": "/logs", "mode": "rw"},
            {"host": str(base / "run"),
             "container": "/run/mdd-sim-gateway", "mode": "rw"},
            {"host": str(root / "pcscd"), "container": "/run/pcscd", "mode": "rw"},
        ],
        "ports": [
            {"container_port": "8089/tcp", "host_ip": "127.0.0.1",
             "host_port": 8089 + int(iid)},
            {"container_port": f"{10000 + int(iid)}/udp", "host_ip": "",
             "host_port": 10000 + int(iid)},
        ],
        "devices": [{"host": "/dev/net/tun", "container": "/dev/net/tun",
                     "permissions": "rwm"}],
        "privileged": True,
        "restart_policy": {"Name": "unless-stopped", "MaximumRetryCount": 0},
        "network_mode": "bridge", "extra_hosts": ["host.docker.internal:host-gateway"],
        "sysctls": dict(engine._CREATE_SPEC_SYSCTLS),
        "labels": {engine.MANAGED_LABEL: "true",
                   "io.mdd-sim-gateway.component": "engine"},
    }


def marker(root, iid, phase, source, *, rollback=None):
    spec = create_spec(root, iid)
    value = {
        "version": 1, "txid": TXID, "instance": iid, "phase": phase,
        "source": source, "target_image_digest": CANDIDATE,
        "target": None, "rollback": rollback, "attempts": 0,
        "manual_required": phase == "manual_required",
        "source_create_spec": spec,
        "source_create_spec_digest": engine._canonical_digest(spec),
        "rollback_image_ref": f"mdd-sim-gateway/engine-rollback:{TXID}-{iid}",
    }
    with patch.multiple(
            engine, DATA_DIR=str(root), HOST_DATA_DIR=str(root),
            PCSCD_SOCK=str(root / "pcscd")):
        return engine._validate_engine_maintenance(value, iid)


def manifest(global_phase, line_phase, source1, source7, *, terminal1=None,
             line7_phase="aborted"):
    return validate_manifest({
        "version": 2, "phase": global_phase, "txid": TXID,
        "candidate_image": CANDIDATE, "promote_default": False,
        "iids": ["1", "7"], "started_at": 1787661991.0,
        "updated_at": 1787662042.0, "unscoped": [],
        "lines": [
            {"iid": "1", "phase": line_phase, "source": source1,
             "terminal": terminal1,
             "error": "line 1 target start receipt is unreadable"},
            {"iid": "7", "phase": line7_phase, "source": source7,
             "terminal": None,
             "error": ("transaction aborted before target creation"
                       if line7_phase == "aborted" else "")},
        ],
    })


def install_exact_deny(root):
    for iid in ("1", "7"):
        deny = root / "instances" / iid / "run" / "admission-deny"
        deny.parent.mkdir(parents=True, exist_ok=True)
        deny.write_text(json.dumps({
            "version": 1, "reason": "global_engine_replacement_in_flight",
            "updated_at": 1787662041,
        }), encoding="utf-8")
        deny.chmod(0o600)


def configure_replacement(root, current, marker1, marker7, source7, rollback1):
    replacement = EngineReplacement(
        root, root, ["1", "7"], CANDIDATE,
        recover_precreate_missing_target="1")
    engine_api = SimpleNamespace(
        read_engine_maintenance=lambda iid: marker1 if iid == "1" else marker7,
        engine_generation_facts=Mock(return_value=source7),
        active_channel_count=Mock(return_value=0),
        begin_engine_maintenance=Mock(return_value=marker(
            root, "7", "prepared", source7)),
        transition_engine_maintenance=Mock(return_value=marker(
            root, "7", "aborted", source7)),
        usim_recovery_fence_pending=Mock(return_value=False),
        recover_precreate_missing_target_to_rollback=Mock(return_value=marker(
            root, "1", "rollback_starting", current["lines"][0]["source"])),
    )
    replacement.engine = engine_api
    replacement.promote_default = False
    replacement._verify_unscoped = Mock()
    replacement._paid_zero = Mock()
    replacement._zero_channels = Mock()
    replacement._wait_gate = Mock()
    replacement._retain_source_image = Mock(return_value="rollback:7")
    replacement._scoped_mutation_locked = lambda *_args: nullcontext()

    def sync(value, iid, durable, **_kwargs):
        line = replacement._line(value, iid)
        line["phase"] = durable["phase"]
        if durable["phase"] == "rollback_verified":
            line["terminal"] = durable["rollback"]
        value["phase"] = "running"
        return value

    def rollback(value, iid, _cause):
        line = replacement._line(value, iid)
        line["phase"], line["terminal"] = "rollback_verified", rollback1
        return value

    replacement._sync_line = sync
    replacement._rollback = rollback
    install_exact_deny(root)
    return replacement, engine_api


@pytest.mark.parametrize(("global_phase", "line_phase", "marker_phase", "uses_cas"), [
    ("manual_required", "manual_required", "manual_required", True),
    ("manual_required", "manual_required", "rollback_starting", False),
    ("running", "rollback_starting", "rollback_starting", False),
    ("running", "rollback_started", "rollback_started", False),
    ("running", "rollback_verified", "rollback_verified", False),
])
def test_precreate_recovery_resumes_failed_line_write_windows(
        tmp_path, global_phase, line_phase, marker_phase, uses_cas):
    source1, source7 = facts("1"), facts("7")
    rollback1 = facts("1", generation="rollback")
    terminal = rollback1 if line_phase == "rollback_verified" else None
    durable = manifest(global_phase, line_phase, source1, source7, terminal1=terminal)
    rollback_marker = rollback1 if marker_phase in {
        "rollback_started", "rollback_verified"} else None
    marker1 = marker(tmp_path, "1", marker_phase, source1, rollback=rollback_marker)
    marker7 = marker(tmp_path, "7", "aborted", source7)
    replacement, engine_api = configure_replacement(
        tmp_path, durable, marker1, marker7, source7, rollback1)

    recovered = replacement._recover_precreate_missing_target_failure(durable)
    assert [line["phase"] for line in recovered["lines"]] == [
        "rollback_verified", "aborted"]
    assert replacement._wait_gate.call_count == 2
    assert engine_api.active_channel_count.call_count == 2
    assert engine_api.recover_precreate_missing_target_to_rollback.call_count == int(uses_cas)


@pytest.mark.parametrize("abort_marker_phase", [None, "prepared", "aborted"])
def test_precreate_recovery_resumes_pending_abort_write_windows(
        tmp_path, abort_marker_phase):
    source1, source7 = facts("1"), facts("7")
    rollback1 = facts("1", generation="rollback")
    durable = manifest(
        "manual_required", "manual_required", source1, source7,
        line7_phase="pending")
    marker1 = marker(tmp_path, "1", "manual_required", source1)
    marker7 = (None if abort_marker_phase is None else marker(
        tmp_path, "7", abort_marker_phase, source7))
    replacement, engine_api = configure_replacement(
        tmp_path, durable, marker1, marker7, source7, rollback1)

    recovered = replacement._recover_precreate_missing_target_failure(durable)
    assert [line["phase"] for line in recovered["lines"]] == [
        "rollback_verified", "aborted"]
    assert engine_api.begin_engine_maintenance.call_count == int(
        abort_marker_phase is None)
    assert engine_api.transition_engine_maintenance.call_count == int(
        abort_marker_phase != "aborted")


def test_precreate_recovery_rejects_non_exact_admission_deny(tmp_path):
    source1, source7 = facts("1"), facts("7")
    durable = manifest("manual_required", "manual_required", source1, source7)
    replacement, _engine_api = configure_replacement(
        tmp_path, durable,
        marker(tmp_path, "1", "manual_required", source1),
        marker(tmp_path, "7", "aborted", source7), source7,
        facts("1", generation="rollback"))
    deny = tmp_path / "instances" / "1" / "run" / "admission-deny"
    deny.write_text(json.dumps({
        "version": 1, "reason": "wrong", "updated_at": 1,
    }), encoding="utf-8")
    deny.chmod(0o600)
    with pytest.raises(Exception, match="admission deny is not exact"):
        replacement._recover_precreate_missing_target_failure(durable)


@pytest.mark.parametrize("source_present", [True, False])
def test_usim_fenced_pending_source_rebuilds_only_retained_old_image(
        tmp_path, source_present):
    source1, source7 = facts("1"), facts("7")
    rollback7 = {
        **facts("7", generation="rollback"),
        "container_id": "8" * 64, "run_id": "run-rollback-7",
    }
    durable = manifest(
        "manual_required", "manual_required", source1, source7,
        line7_phase="pending")
    replacement = EngineReplacement(
        tmp_path, tmp_path, ["1", "7"], CANDIDATE,
        recover_precreate_missing_target="1")
    rollback_start = marker(tmp_path, "7", "rollback_starting", source7)
    engine_api = SimpleNamespace(
        read_run_json=Mock(return_value={
            "state": "AUTH_UNAVAILABLE", "cause_class": "pcsc_service_unavailable",
            "engine_run_id": source7["run_id"], "auth_seq": 4,
        }),
        registration_state=Mock(return_value="Rejected"),
        engine_generation_facts=Mock(return_value=source7),
        active_channel_count=Mock(return_value=0),
        prepare_usim_fenced_source_rollback=Mock(return_value=rollback_start),
        capture_and_stop_if_idle=Mock(return_value={"status": "stopped"}),
    )
    replacement.engine = engine_api
    replacement.cfg = SimpleNamespace(get_instance=lambda _iid: {"id": "7"})
    replacement._wait_gate = Mock()
    replacement._paid_zero = Mock()
    replacement._retain_source_image = Mock(return_value="rollback:7")
    replacement._scoped_mutation_locked = lambda *_args: nullcontext()
    replacement._current_generation = Mock(
        side_effect=([source7, None] if source_present else [None]))

    def sync(value, iid, durable_marker, **_kwargs):
        replacement._line(value, iid)["phase"] = durable_marker["phase"]
        value["phase"] = "running"
        return value

    def rollback(value, iid, _cause):
        line = replacement._line(value, iid)
        line["phase"], line["terminal"] = "rollback_verified", rollback7
        return value

    replacement._sync_line = sync
    replacement._rollback = rollback
    install_exact_deny(tmp_path)
    recovered = replacement._recover_usim_fenced_pending_source(
        durable, replacement._line(durable, "7"), None)
    assert replacement._line(recovered, "7")["phase"] == "rollback_verified"
    engine_api.prepare_usim_fenced_source_rollback.assert_called_once()
    if source_present:
        engine_api.capture_and_stop_if_idle.assert_called_once_with(
            "7", {"id": "7"}, "engine-replacement-usim-fenced-rollback",
            source7["container_id"])
    else:
        engine_api.capture_and_stop_if_idle.assert_not_called()


def test_outer_recovery_accepts_exact_running_usim_rollback_phase(tmp_path):
    source1, source7 = facts("1"), facts("7")
    rollback1 = facts("1", generation="rollback")
    rollback7 = {
        **facts("7", generation="rollback"),
        "container_id": "8" * 64, "run_id": "run-rollback-7",
    }
    durable = manifest(
        "running", "manual_required", source1, source7,
        line7_phase="rollback_starting")
    durable["lines"][1]["error"] = (
        "restarting source after exact PC/SC service outage")
    durable = validate_manifest(durable)
    marker1 = marker(tmp_path, "1", "manual_required", source1)
    marker7 = marker(tmp_path, "7", "rollback_starting", source7)
    replacement, engine_api = configure_replacement(
        tmp_path, durable, marker1, marker7, source7, rollback1)

    def recover_line7(value, line, _marker):
        line["phase"], line["terminal"] = "rollback_verified", rollback7
        return value

    replacement._recover_usim_fenced_pending_source = Mock(side_effect=recover_line7)
    engine_api.engine_generation_facts = Mock(side_effect=lambda iid, _cid: (
        rollback7 if iid == "7" else source1))
    recovered = replacement._recover_precreate_missing_target_failure(durable)
    assert [line["phase"] for line in recovered["lines"]] == [
        "rollback_verified", "rollback_verified"]
    replacement._recover_usim_fenced_pending_source.assert_called_once()


@pytest.mark.parametrize("source_present_on_resume", [True, False])
def test_usim_marker_to_manifest_crash_persists_phase_and_reason_atomically(
        tmp_path, source_present_on_resume):
    source1, source7 = facts("1"), facts("7")
    rollback1 = facts("1", generation="rollback")
    rollback7 = {
        **facts("7", generation="rollback"),
        "container_id": "8" * 64, "run_id": "run-rollback-7",
    }
    durable = manifest(
        "manual_required", "manual_required", source1, source7,
        line7_phase="pending")
    replacement = EngineReplacement(
        tmp_path, tmp_path, ["1", "7"], CANDIDATE,
        recover_precreate_missing_target="1")
    rollback_start = marker(tmp_path, "7", "rollback_starting", source7)
    replacement.engine = SimpleNamespace()
    replacement._current_generation = Mock(side_effect=RuntimeError("simulated crash"))
    _atomic_json(replacement.manifest_path, durable)
    with pytest.raises(RuntimeError, match="simulated crash"):
        replacement._recover_usim_fenced_pending_source(
            durable, replacement._line(durable, "7"), rollback_start)
    persisted = read_manifest(replacement.manifest_path)
    line7 = replacement._line(persisted, "7")
    assert persisted["phase"] == "running"
    assert line7["phase"] == "rollback_starting"
    assert line7["error"] == "restarting source after exact PC/SC service outage"

    marker1 = marker(tmp_path, "1", "manual_required", source1)
    engine_api = SimpleNamespace(
        read_engine_maintenance=lambda iid: marker1 if iid == "1" else rollback_start,
        engine_generation_facts=Mock(side_effect=lambda iid, _cid: (
            rollback7 if iid == "7" else source1)),
        active_channel_count=Mock(return_value=0),
        read_run_json=Mock(return_value={
            "state": "AUTH_UNAVAILABLE", "cause_class": "pcsc_service_unavailable",
            "engine_run_id": source7["run_id"], "auth_seq": 4,
        }),
        registration_state=Mock(return_value="Rejected"),
        capture_and_stop_if_idle=Mock(return_value={"status": "stopped"}),
        recover_precreate_missing_target_to_rollback=Mock(return_value=marker(
            tmp_path, "1", "rollback_starting", source1)),
    )
    replacement.engine = engine_api
    replacement.cfg = SimpleNamespace(get_instance=lambda iid: {"id": iid})
    replacement.promote_default = False
    replacement._verify_unscoped = Mock()
    replacement._paid_zero = Mock()
    replacement._zero_channels = Mock()
    replacement._wait_gate = Mock()
    replacement._scoped_mutation_locked = lambda *_args: nullcontext()
    replacement._current_generation = Mock(side_effect=(
        [source7, None] if source_present_on_resume else [None]))

    def sync(value, iid, durable_marker, **kwargs):
        line = replacement._line(value, iid)
        line["phase"] = durable_marker["phase"]
        if durable_marker["phase"] == "rollback_verified":
            line["terminal"] = durable_marker["rollback"]
        if kwargs.get("error") is not None:
            line["error"] = kwargs["error"]
        value["phase"] = "running"
        return value

    def rollback(value, iid, cause):
        line = replacement._line(value, iid)
        line["phase"] = "rollback_verified"
        line["terminal"] = rollback7 if iid == "7" else rollback1
        line["error"] = str(cause)
        return value

    replacement._sync_line = sync
    replacement._rollback = rollback
    install_exact_deny(tmp_path)
    recovered = replacement._recover_precreate_missing_target_failure(persisted)
    assert [line["phase"] for line in recovered["lines"]] == [
        "rollback_verified", "rollback_verified"]
    if source_present_on_resume:
        engine_api.capture_and_stop_if_idle.assert_called_once()
    else:
        engine_api.capture_and_stop_if_idle.assert_not_called()


def test_missing_runid_exception_salvage_attests_existing_old_image_generation(
        tmp_path):
    source = facts("7")
    rollback = {
        **facts("7", generation="rollback"),
        "container_id": "8" * 64, "run_id": "run-rollback-7",
        "started_at": "2026-08-25T13:36:24.491813768Z",
    }
    manual = marker(tmp_path, "7", "manual_required", source)

    class Containers:
        def get(self, identity):
            if identity == source["container_id"]:
                raise engine.docker.errors.NotFound("old source absent")
            raise AssertionError(identity)

        def list(self, **_kwargs):
            return []

    client = SimpleNamespace(
        containers=Containers(),
        images=SimpleNamespace(get=Mock(return_value=SimpleNamespace(
            id=source["image_id"]))))
    with patch.multiple(
            engine, DATA_DIR=str(tmp_path), HOST_DATA_DIR=str(tmp_path),
            PCSCD_SOCK=str(tmp_path / "pcscd")), \
            patch.object(engine, "engine_maintenance_locked", return_value=nullcontext()), \
            patch.object(engine, "read_engine_maintenance", return_value=manual), \
            patch.object(engine, "engine_generation_facts", return_value=rollback), \
            patch.object(engine, "_client", return_value=client), \
            patch.object(engine, "capture_engine_create_spec",
                         return_value=manual["source_create_spec"]), \
            patch.object(engine, "acquire_pcscf_admission",
                         side_effect=AssertionError("must not acquire P-CSCF")), \
            patch.object(engine, "submit_usim_recovery_register",
                         side_effect=AssertionError("must not submit REGISTER")):
        recovered = engine.recover_usim_rollback_started_after_missing_runid_exception(
            "7", TXID, rollback["container_id"], 1.0, 9_999_999_999.0)
    assert recovered["phase"] == "rollback_started"
    assert recovered["rollback"] == rollback
    assert recovered["manual_required"] is False


def test_new_generation_missing_runid_uses_specific_retry_exception(tmp_path):
    container = SimpleNamespace(
        id="a" * 64, status="running",
        attrs={
            "Config": {"Labels": {engine.MANAGED_LABEL: "true"}},
            "State": {"Status": "running", "StartedAt": "2026-08-25T11:00:20Z",
                      "Pid": 123}, "RestartCount": 0,
            "Image": "sha256:" + "b" * 64,
        },
        reload=lambda: None,
    )
    client = SimpleNamespace(containers=SimpleNamespace(get=lambda _name: container))
    with patch.object(engine, "DATA_DIR", str(tmp_path)), \
            patch.object(engine, "_client", return_value=client), \
            pytest.raises(engine.EngineRunIdUnavailable):
        engine.engine_generation_facts("7")
