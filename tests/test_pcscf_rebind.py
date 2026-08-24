import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import threading
import time
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

from control.app import engine


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "mdd_pcscf_state", ROOT / "engine" / "pcscf_state.py")
pcscf_state = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(pcscf_state)


def _render_probe(temp, delay=0):
    script = Path(temp) / "render_probe.py"
    script.write_text(
        "import os,time\n"
        f"time.sleep({delay!r})\n"
        "open(os.path.join(os.path.dirname(__file__), 'rendered'), 'w').write("
        "os.environ.get('MDD_PCSCF_OVERRIDE',''))\n")
    return str(script)


def _noisy_renderer(temp, *, exit_code=0):
    script = Path(temp) / "noisy_renderer.py"
    script.write_text(
        "import os,sys\n"
        "print('[render] pjsip.conf.j2 -> /etc/asterisk/pjsip.conf')\n"
        "print('[render] env -> /run/mdd-sim-gateway/engine.env')\n"
        "open(os.path.join(os.path.dirname(__file__), 'rendered'), 'w').write("
        "os.environ.get('MDD_PCSCF_OVERRIDE',''))\n"
        f"sys.exit({exit_code})\n")
    return str(script)


def _run_bootstrap_cli(temp, run_id, renderer):
    environment = dict(os.environ)
    environment["MDD_RUNDIR"] = temp
    return subprocess.run(
        [sys.executable, str(ROOT / "engine" / "pcscf_state.py"),
         "render-bootstrap", run_id, renderer],
        env=environment, text=True, capture_output=True, check=False)


def test_bootstrap_cli_keeps_noisy_renderer_out_of_fresh_protocol_stdout():
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        assert pcscf_state.publish_discovered(
            temp, "7", "run-a", "fd00::2", observed_at=100.0) == "pending"

        result = _run_bootstrap_cli(temp, "run-a", _noisy_renderer(temp))

        assert result.returncode == 0
        assert result.stdout == "fresh fd00::2\n"
        assert result.stderr.splitlines() == [
            "[render] pjsip.conf.j2 -> /etc/asterisk/pjsip.conf",
            "[render] env -> /run/mdd-sim-gateway/engine.env",
        ]
        assert (root / "rendered").read_text() == "fd00::2"
        assert (root / pcscf_state.APPLIED_NAME).read_text() == "fd00::2"
        assert (root / "pcscf").read_text() == "fd00::2"
        assert not (root / pcscf_state.MARKER_NAME).exists()


def test_bootstrap_cli_keeps_noisy_renderer_out_of_fallback_protocol_stdout():
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        assert pcscf_state.publish_discovered(
            temp, "1", "old-run", "10.0.0.2", observed_at=100.0) == "pending"
        for name in (pcscf_state.DISCOVERY_NAME, pcscf_state.APPLIED_NAME, "pcscf"):
            (root / name).unlink(missing_ok=True)

        result = _run_bootstrap_cli(temp, "new-run", _noisy_renderer(temp))

        assert result.returncode == 0
        assert result.stdout == "fallback 10.0.0.2\n"
        assert result.stderr.startswith("[render] pjsip.conf.j2")
        assert (root / "rendered").read_text() == "10.0.0.2"
        assert (root / pcscf_state.APPLIED_NAME).read_text() == "10.0.0.2"
        assert (root / "pcscf").read_text() == "10.0.0.2"
        assert (root / pcscf_state.MARKER_NAME).exists()


def test_bootstrap_cli_none_does_not_launch_renderer():
    with tempfile.TemporaryDirectory() as temp:
        missing = str(Path(temp) / "must-not-run.py")
        result = _run_bootstrap_cli(temp, "run-a", missing)
        assert result.returncode == 0
        assert result.stdout == "none\n"
        assert result.stderr == ""


def test_bootstrap_cli_renderer_failure_has_no_protocol_or_partial_commit():
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        assert pcscf_state.publish_discovered(
            temp, "1", "run-a", "fd00::2", observed_at=100.0) == "pending"
        for name in (pcscf_state.APPLIED_NAME, "pcscf"):
            (root / name).unlink(missing_ok=True)

        result = _run_bootstrap_cli(
            temp, "run-a", _noisy_renderer(temp, exit_code=7))

        assert result.returncode == 1
        assert result.stdout == ""
        assert "[render] pjsip.conf.j2" in result.stderr
        assert "P-CSCF fresh render failed (7)" in result.stderr
        assert not (root / pcscf_state.APPLIED_NAME).exists()
        assert not (root / "pcscf").exists()
        assert (root / pcscf_state.MARKER_NAME).exists()


def test_bootstrap_cli_timeout_has_no_protocol_or_partial_commit(capsys):
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        assert pcscf_state.publish_discovered(
            temp, "1", "run-a", "fd00::2", observed_at=100.0) == "pending"
        for name in (pcscf_state.APPLIED_NAME, "pcscf"):
            (root / name).unlink(missing_ok=True)

        with patch.object(
                pcscf_state.subprocess, "run",
                side_effect=subprocess.TimeoutExpired(["python3", "render.py"], 30)), \
                patch.dict(os.environ, {"MDD_RUNDIR": temp}):
            result = pcscf_state.main([
                "render-bootstrap", "run-a", str(root / "render.py")])

        captured = capsys.readouterr()
        assert result == 1
        assert captured.out == ""
        assert "timed out after 30 seconds" in captured.err
        assert not (root / pcscf_state.APPLIED_NAME).exists()
        assert not (root / "pcscf").exists()
        assert (root / pcscf_state.MARKER_NAME).exists()


def test_runtime_bootstrap_protocol_is_strict_for_ipv4_ipv6_and_extra_tokens():
    source = (ROOT / "engine" / "engine-runtime.sh").read_text()
    assert '[[ "$bootstrap" == "none" ]]' in source
    assert ('[[ "$bootstrap" =~ ^(fresh|fallback)[[:space:]]'
            '([^[:space:]]+)$ ]]') in source
    parser = r'''value=$1
if [[ "$value" == "none" ]]; then
  printf 'none\n'
elif [[ "$value" =~ ^(fresh|fallback)[[:space:]]([^[:space:]]+)$ ]]; then
  printf '%s %s\n' "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}"
else
  exit 64
fi'''
    for value in ("none", "fresh 10.0.0.2", "fallback fd00::2"):
        result = subprocess.run(
            ["bash", "-c", parser, "bootstrap-parser", value],
            text=True, capture_output=True, check=False)
        assert result.returncode == 0
        assert result.stdout == value + "\n"
    for value in ("fresh", "none extra", "fresh 10.0.0.2 extra",
                  "[render] noise\nfresh 10.0.0.2"):
        result = subprocess.run(
            ["bash", "-c", parser, "bootstrap-parser", value],
            text=True, capture_output=True, check=False)
        assert result.returncode == 64
        assert result.stdout == ""


def test_bootstrap_discovery_is_durable_then_locked_render_commits_same_snapshot():
    with tempfile.TemporaryDirectory() as temp:
        rundir = Path(temp)
        result = pcscf_state.publish_discovered(
            temp, "7", "run-a", "fd00::2", observed_at=100.0)
        assert result == "pending"
        assert (rundir / pcscf_state.MARKER_NAME).exists()
        kind, address = pcscf_state.render_bootstrap(
            temp, "run-a", _render_probe(temp))
        assert (kind, address) == ("fresh", "fd00::2")
        assert (rundir / "rendered").read_text() == "fd00::2"
        assert (rundir / pcscf_state.APPLIED_NAME).read_text() == "fd00::2"
        assert not (rundir / pcscf_state.MARKER_NAME).exists()


def test_latest_pcscf_coalesces_and_return_to_applied_requests_cancel():
    with tempfile.TemporaryDirectory() as temp:
        (Path(temp) / "pcscf.applied").write_text("fd00::1")
        assert pcscf_state.publish_discovered(
            temp, "1", "run-a", "fd00::2", observed_at=100.0) == "pending"
        assert pcscf_state.publish_discovered(
            temp, "1", "run-a", "fd00::3", observed_at=101.0) == "coalesced"
        marker = pcscf_state.read_marker(temp)
        assert marker["applied"] == "fd00::1"
        assert marker["desired"] == "fd00::3"
        assert marker["shutdown_reserved"] is False

        marker.update({"shutdown_reserved": True, "phase": "submitted"})
        pcscf_state._write_unlocked(temp, marker)
        assert pcscf_state.publish_discovered(
            temp, "1", "run-a", "fd00::1", observed_at=102.0
        ) == "cancel_requested"
        assert pcscf_state.read_marker(temp)["phase"] == "cancel_requested"


def test_late_first_discovery_after_asterisk_boot_requests_full_generation():
    with tempfile.TemporaryDirectory() as temp:
        assert pcscf_state.publish_discovered(
            temp, "1", "run-a", "fd00::2", observed_at=100.0) == "pending"
        marker = pcscf_state.read_marker(temp)
        assert marker["applied"] == ""
        assert marker["desired"] == "fd00::2"


def test_invalid_applied_fails_closed_and_marker_precedes_address_publication():
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        (root / pcscf_state.APPLIED_NAME).write_text("not-an-ip")
        writes = []
        original = pcscf_state._atomic_text

        def recording_write(path, value, mode=0o600):
            writes.append(Path(path).name)
            return original(path, value, mode)

        with patch.object(pcscf_state, "_atomic_text", side_effect=recording_write):
            action = pcscf_state.publish_discovered(
                temp, "1", "run-a", "fd00::2", observed_at=100.0)

        assert action == "pending"
        marker = pcscf_state.read_marker(temp)
        assert marker["applied"] == ""
        assert marker["desired"] == "fd00::2"
        assert writes.index(pcscf_state.MARKER_NAME) < writes.index("pcscf")
        assert writes.index(pcscf_state.MARKER_NAME) < writes.index(
            pcscf_state.DISCOVERY_NAME)


def test_replacement_can_stage_latest_target_but_stays_fenced_until_fresh_confirmation():
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        (root / "pcscf.applied").write_text("fd00::1")
        assert pcscf_state.publish_discovered(
            temp, "1", "old-run", "fd00::2", observed_at=100.0) == "pending"
        for name in ("pcscf", "pcscf.applied", pcscf_state.DISCOVERY_NAME):
            (root / name).unlink(missing_ok=True)
        kind, address = pcscf_state.render_bootstrap(
            temp, "new-run", _render_probe(temp))
        assert (kind, address) == ("fallback", "fd00::2")
        assert (Path(temp) / "pcscf.applied").read_text() == "fd00::2"
        assert (Path(temp) / "pcscf-rebind.json").exists()

        result = pcscf_state.publish_discovered(
            temp, "1", "new-run", "fd00::2", observed_at=101.0)
        assert result == "confirmed"
        assert not (Path(temp) / "pcscf-rebind.json").exists()


def test_discovery_arriving_during_locked_render_is_not_lost_or_miscommitted():
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        pcscf_state.publish_discovered(
            temp, "1", "new-run", "fd00::2", observed_at=100.0)
        rendered = []
        worker = threading.Thread(target=lambda: rendered.append(
            pcscf_state.render_bootstrap(temp, "new-run", _render_probe(temp, 0.2))))
        worker.start()
        time.sleep(0.05)
        action = pcscf_state.publish_discovered(
            temp, "1", "new-run", "fd00::3", observed_at=101.0)
        worker.join()
        assert rendered == [("fresh", "fd00::2")]
        assert (root / "rendered").read_text() == "fd00::2"
        assert (root / "pcscf.applied").read_text() == "fd00::2"
        assert action == "pending"
        marker = pcscf_state.read_marker(temp)
        assert marker["engine_run_id"] == "new-run"
        assert marker["applied"] == "fd00::2"
        assert marker["desired"] == "fd00::3"


def _marker(run_id="run-a", phase="pending", reserved=False):
    return {"version": 1, "instance": "1", "engine_run_id": run_id,
            "applied": "fd00::1", "desired": "fd00::2", "observed_at": 100.0,
            "phase": phase, "shutdown_reserved": reserved}


class _Container:
    id = "container-a"
    status = "running"

    def __init__(self, *, started_at="2026-08-22T12:00:00Z",
                 policy="unless-stopped", ready_rc=0, stop_rc=0, abort_rc=0):
        self.attrs = {
            "Config": {"Labels": {engine.MANAGED_LABEL: "true"},
                       "Image": "mdd-sim-gateway/engine"},
            "HostConfig": {"RestartPolicy": {"Name": policy}},
            "State": {"StartedAt": started_at},
        }
        self.commands = []
        self.ready_rc = ready_rc
        self.stop_rc = stop_rc
        self.abort_rc = abort_rc

    def reload(self):
        return None

    def exec_run(self, command):
        self.commands.append(command)
        if command[-1] == "core waitfullybooted":
            return self.ready_rc, b""
        if command[-1] == "core stop gracefully":
            return self.stop_rc, b""
        if command[-1] == "core abort shutdown":
            return self.abort_rc, b""
        return 0, b""


def _write_runtime(temp, marker):
    run = Path(temp) / "instances" / "1" / "run"
    run.mkdir(parents=True)
    (run / "engine-run-id").write_text("run-a\n")
    (run / "pcscf-rebind.json").write_text(json.dumps(marker))
    return run


def _client(container):
    return SimpleNamespace(
        containers=SimpleNamespace(get=lambda _name: container), close=lambda: None)


def test_control_reserves_graceful_stop_before_exec_and_never_submits_twice():
    with tempfile.TemporaryDirectory() as temp:
        run = _write_runtime(temp, _marker())
        container = _Container()
        with patch.object(engine, "DATA_DIR", temp), \
                patch.object(engine.docker, "from_env", return_value=_client(container)):
            first = engine.request_pcscf_rebind(
                "1", "container-a", "2026-08-22T12:00:00Z", "run-a")
            second = engine.request_pcscf_rebind(
                "1", "container-a", "2026-08-22T12:00:00Z", "run-a")

        assert first["status"] == "submitted"
        assert second["status"] == "already_submitted"
        assert container.commands == [
            ["asterisk", "-rx", "core waitfullybooted"],
            ["asterisk", "-rx", "core stop gracefully"],
        ]
        saved = json.loads((run / "pcscf-rebind.json").read_text())
        assert saved["shutdown_reserved"] is True
        assert saved["submit_result"] == "accepted"


def test_same_container_id_new_started_at_and_unmanaged_policy_cannot_shutdown():
    for container, expected in (
            (_Container(started_at="2026-08-22T12:01:00Z"), "incarnation_changed"),
            (_Container(policy="no"), "restart_policy_not_managed")):
        with tempfile.TemporaryDirectory() as temp:
            _write_runtime(temp, _marker())
            with patch.object(engine, "DATA_DIR", temp), \
                    patch.object(engine.docker, "from_env", return_value=_client(container)):
                result = engine.request_pcscf_rebind(
                    "1", "container-a", "2026-08-22T12:00:00Z", "run-a")
        assert result["status"] == expected
        assert container.commands == []


def test_bootstrap_marker_waits_without_reserving_until_asterisk_is_fully_booted():
    with tempfile.TemporaryDirectory() as temp:
        run = _write_runtime(temp, _marker())
        container = _Container(ready_rc=1)
        with patch.object(engine, "DATA_DIR", temp), \
                patch.object(engine.docker, "from_env", return_value=_client(container)):
            result = engine.request_pcscf_rebind(
                "1", "container-a", "2026-08-22T12:00:00Z", "run-a")
        assert result["status"] == "asterisk_not_ready"
        assert json.loads((run / "pcscf-rebind.json").read_text())[
            "shutdown_reserved"] is False
        assert container.commands == [["asterisk", "-rx", "core waitfullybooted"]]


def test_return_to_applied_before_submission_removes_marker_without_asterisk_command():
    with tempfile.TemporaryDirectory() as temp:
        run = _write_runtime(temp, {
            **_marker(phase="cancel_requested"), "desired": "fd00::1",
            "previous_phase": "pending"})
        container = _Container()
        with patch.object(engine, "DATA_DIR", temp), \
                patch.object(engine.docker, "from_env", return_value=_client(container)):
            result = engine.cancel_pcscf_rebind(
                "1", "container-a", "2026-08-22T12:00:00Z", "run-a")
        assert result["status"] == "cancelled"
        assert not (run / "pcscf-rebind.json").exists()
        assert container.commands == []


def test_return_to_applied_after_submission_aborts_once_and_clears_fence():
    with tempfile.TemporaryDirectory() as temp:
        run = _write_runtime(temp, {
            **_marker(phase="cancel_requested", reserved=True), "desired": "fd00::1",
            "previous_phase": "submitted"})
        container = _Container()
        with patch.object(engine, "DATA_DIR", temp), \
                patch.object(engine.docker, "from_env", return_value=_client(container)):
            result = engine.cancel_pcscf_rebind(
                "1", "container-a", "2026-08-22T12:00:00Z", "run-a")
        assert result["status"] == "aborted"
        assert container.commands == [["asterisk", "-rx", "core abort shutdown"]]
        assert not (run / "pcscf-rebind.json").exists()


def test_abort_response_cannot_clear_fence_after_same_container_restarted():
    with tempfile.TemporaryDirectory() as temp:
        run = _write_runtime(temp, {
            **_marker(phase="cancel_requested", reserved=True), "desired": "fd00::1",
            "previous_phase": "submitted"})
        container = _Container()

        def exec_and_restart(command):
            container.commands.append(command)
            if command[-1] == "core abort shutdown":
                container.attrs["State"]["StartedAt"] = "2026-08-22T12:01:00Z"
                (run / "engine-run-id").write_text("run-b\n")
            return 0, b""

        container.exec_run = exec_and_restart
        with patch.object(engine, "DATA_DIR", temp), \
                patch.object(engine.docker, "from_env", return_value=_client(container)):
            result = engine.cancel_pcscf_rebind(
                "1", "container-a", "2026-08-22T12:00:00Z", "run-a")
        assert result["status"] == "aborted_incarnation_changed"
        assert (run / "pcscf-rebind.json").exists()


def test_corrupt_marker_is_not_actionable_but_still_fences_admission():
    with tempfile.TemporaryDirectory() as temp:
        run = Path(temp) / "instances" / "1" / "run"
        run.mkdir(parents=True)
        (run / "pcscf-rebind.json").write_text("{broken")
        with patch.object(engine, "DATA_DIR", temp):
            assert engine.read_pcscf_rebind("1") is None
            assert engine.pcscf_rebind_pending("1") is True


def test_control_submission_boundary_totally_orders_marker_publication():
    with tempfile.TemporaryDirectory() as temp:
        run = Path(temp) / "instances" / "1" / "run"
        run.mkdir(parents=True)
        published = threading.Event()
        with patch.object(engine, "DATA_DIR", temp):
            handle = engine.acquire_pcscf_admission("1")
            assert handle is not None
            started = time.monotonic()
            assert engine.acquire_pcscf_admission("1") is None
            assert time.monotonic() - started < 0.1

            def publish():
                with engine._pcscf_rebind_locked("1"):
                    (run / "pcscf-rebind.json").write_text("pending")
                published.set()

            worker = threading.Thread(target=publish)
            worker.start()
            time.sleep(0.05)
            assert not published.is_set()
            engine.release_pcscf_admission(handle)
            worker.join()
            assert published.is_set()
            assert engine.acquire_pcscf_admission("1") is None


def test_rejected_graceful_stop_retries_with_backoff_then_requires_manual_action():
    with tempfile.TemporaryDirectory() as temp:
        run = _write_runtime(temp, _marker())
        container = _Container(stop_rc=1)
        with patch.object(engine, "DATA_DIR", temp), \
                patch.object(engine.docker, "from_env", return_value=_client(container)), \
                patch.object(engine.time, "time", return_value=100.0):
            first = engine.request_pcscf_rebind(
                "1", "container-a", "2026-08-22T12:00:00Z", "run-a")
            waiting = engine.request_pcscf_rebind(
                "1", "container-a", "2026-08-22T12:00:00Z", "run-a")
        assert first["status"] == "submit_rejected_retrying"
        assert waiting["status"] == "submit_retry_wait"
        assert json.loads((run / "pcscf-rebind.json").read_text())["shutdown_reserved"] is False

        for now, expected in ((106.0, "submit_rejected_retrying"),
                              (117.0, "submit_retry_exhausted")):
            with patch.object(engine, "DATA_DIR", temp), \
                    patch.object(engine.docker, "from_env", return_value=_client(container)), \
                    patch.object(engine.time, "time", return_value=now):
                result = engine.request_pcscf_rebind(
                    "1", "container-a", "2026-08-22T12:00:00Z", "run-a")
            assert result["status"] == expected

        saved = json.loads((run / "pcscf-rebind.json").read_text())
        assert saved["submit_rejections"] == engine._PCSCF_REBIND_RETRY_LIMIT
        assert saved["manual_required"] == "graceful_stop_rejected"
        assert saved["shutdown_reserved"] is False


def test_rejected_abort_clears_reservation_and_uses_bounded_retry_budget():
    with tempfile.TemporaryDirectory() as temp:
        run = _write_runtime(temp, {
            **_marker(phase="cancel_requested", reserved=True), "desired": "fd00::1",
            "previous_phase": "submitted"})
        container = _Container(abort_rc=1)
        result = None
        for now in (100.0, 106.0, 117.0):
            with patch.object(engine, "DATA_DIR", temp), \
                    patch.object(engine.docker, "from_env", return_value=_client(container)), \
                    patch.object(engine.time, "time", return_value=now):
                result = engine.cancel_pcscf_rebind(
                    "1", "container-a", "2026-08-22T12:00:00Z", "run-a")
        assert result["status"] == "abort_retry_exhausted"
        saved = json.loads((run / "pcscf-rebind.json").read_text())
        assert saved["abort_rejections"] == engine._PCSCF_REBIND_RETRY_LIMIT
        assert saved["manual_required"] == "abort_shutdown_rejected"
        assert saved["phase"] == "cancel_requested"
        assert "abort_reserved_at" not in saved


def test_engine_source_has_no_runtime_pjsip_reload_or_forced_register():
    source = (ROOT / "engine" / "swu_ike.py").read_text()
    apply_body = source.split("def swu_publish_pcscf", 1)[1].split("'''", 1)[0]
    restoration = source.split("def handle_pcscf_restoration", 1)[1].split(
        "def cp_attr_name", 1)[0]
    assert "module reload res_pjsip" not in apply_body
    assert "pjsip send register" not in apply_body
    assert "pjsip send register" not in restoration
