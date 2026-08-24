import hashlib
import json
import os
import signal
import subprocess
import sys
import threading
import time
from pathlib import Path

import pytest

from engine import admission_gate


RUN_ID = "11111111-2222-4333-8444-555555555555"


class Clock:
    def __init__(self):
        self.value = 100.0

    def __call__(self):
        return self.value

    def advance(self, seconds):
        self.value += seconds


def authority(seq=1, *, mode="maintenance", iid="7", run_id=RUN_ID,
              issuer="a" * 32, epoch=4):
    engine = {
        "container_id": "b" * 64,
        "image_id": "sha256:" + "c" * 64,
        "started_at": "2026-08-23T00:00:00.000000000Z",
        "restart_count": 0,
        "run_id": run_id,
    }
    value = {
        "version": 1,
        "protocol": admission_gate.PROTOCOL,
        "mode": mode,
        "iid": iid,
        "issuer_boot_id": issuer,
        "authority_epoch": epoch,
        "lease_seq": seq,
        "engine": engine,
        "engine_generation_digest": hashlib.sha256(json.dumps(
            engine, sort_keys=True, separators=(",", ":")).encode()).hexdigest(),
        "maintenance": None,
        "normal": None,
    }
    if mode == "maintenance":
        value["maintenance"] = {
            "txid": "upgrade-123",
            "manifest_digest": "d" * 64,
            "proxy_process_boot_id": "e" * 32,
            "supervisor_boot_id": issuer,
            "proxy_mode_epoch": 8,
        }
    else:
        value["normal"] = {"commit_id": "f" * 32, "state_digest": "1" * 64}
    return value


def wait_for(predicate, timeout=2.0):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(0.02)
    raise AssertionError("condition did not become true")


def atomic_authority(path: Path, value: object) -> None:
    tmp = path.with_name(f".{path.name}.{os.getpid()}.{time.time_ns()}")
    tmp.write_text(json.dumps(value), encoding="utf-8")
    os.replace(tmp, path)


def test_first_sequence_only_warms_up_and_forward_progress_allows():
    clock = Clock()
    state = admission_gate.GateState("7", RUN_ID, ttl=3.0, monotonic=clock)
    state.observe(authority(10))
    assert state.check("call_in").reason == "warmup"
    state.observe(authority(11))
    decision = state.check("call_in")
    assert decision.allowed
    assert (decision.authority_epoch, decision.lease_seq) == (4, 11)


def test_same_sequence_never_refreshes_ttl_and_expiry_requires_two_new_steps():
    clock = Clock()
    state = admission_gate.GateState("7", RUN_ID, ttl=3.0, monotonic=clock)
    state.observe(authority(1))
    state.observe(authority(2))
    clock.advance(2.9)
    state.observe(authority(2))
    clock.advance(0.2)
    assert state.check("call_out").reason == "lease_expired"
    state.observe(authority(3))
    assert not state.check("call_out").allowed
    state.observe(authority(4))
    assert state.check("call_out").allowed


def test_replay_fails_closed_and_does_not_recover_on_one_increment():
    state = admission_gate.GateState("7", RUN_ID)
    state.observe(authority(20))
    state.observe(authority(21))
    assert state.check("sms_out").allowed
    state.observe(authority(20))
    assert state.check("sms_out").reason == "lease_replay"
    state.observe(authority(22))
    assert not state.check("sms_out").allowed
    state.observe(authority(23))
    assert state.check("sms_out").allowed


@pytest.mark.parametrize("mutate", [
    lambda value: value.update(extra=True),
    lambda value: value.update(version=True),
    lambda value: value.update(lease_seq=True),
    lambda value: value.update(mode=[]),
    lambda value: value["engine"].update(started_at="yesterday"),
    lambda value: value["engine"].update(image_id="c" * 64),
    lambda value: value["engine"].update(run_id=RUN_ID.replace("-", "")),
    lambda value: value["maintenance"].update(txid="short"),
    lambda value: value.update(engine_generation_digest="0" * 64),
    lambda value: value["maintenance"].update(supervisor_boot_id="2" * 32),
])
def test_schema_identity_and_bool_as_int_are_rejected(mutate):
    value = authority(1)
    mutate(value)
    with pytest.raises(admission_gate.AuthorityError):
        admission_gate.parse_authority(value, iid="7", engine_run_id=RUN_ID)


def test_mode_or_issuer_change_requires_fresh_warmup():
    state = admission_gate.GateState("7", RUN_ID)
    state.observe(authority(1))
    state.observe(authority(2))
    assert state.check("media_check").allowed
    state.observe(authority(1, mode="normal_committed", issuer="2" * 32, epoch=1))
    assert state.check("media_check").reason == "warmup"
    state.observe(authority(2, mode="normal_committed", issuer="2" * 32, epoch=1))
    assert state.check("media_check").allowed


def test_unknown_operation_is_always_denied():
    state = admission_gate.GateState("7", RUN_ID)
    state.observe(authority(1))
    state.observe(authority(2))
    assert state.check("hangup").reason == "unknown_operation"


def test_unix_protocol_missing_corrupt_and_concurrent_requests(tmp_path):
    authority_path = tmp_path / "admission-authority.json"
    # AF_UNIX paths are limited to about 104 bytes on macOS; pytest's nested tmp_path exceeds it.
    socket_path = Path(os.environ["TMPDIR"]) / f"mdd-gate-{os.getpid()}-{time.time_ns()}.sock"
    status_path = tmp_path / "status.json"
    state = admission_gate.GateState("7", RUN_ID, ttl=1.0)
    service = admission_gate.GateService(
        state, authority_path, socket_path, status_path, interval=0.02)
    service.start()
    try:
        wait_for(lambda: socket_path.exists())
        assert admission_gate.probe(socket_path, "call_in")["allowed"] is False
        atomic_authority(authority_path, authority(1))
        wait_for(lambda: state.check("call_in").reason == "warmup")
        atomic_authority(authority_path, authority(2))
        wait_for(lambda: state.check("call_in").allowed)
        results = []

        def query():
            results.append(admission_gate.probe(socket_path, "call_in"))

        threads = [threading.Thread(target=query) for _ in range(8)]
        for thread in threads:
            thread.start()
        for thread in threads:
            thread.join()
        assert len(results) == 8 and all(result["allowed"] for result in results)

        authority_path.write_text("{broken")
        wait_for(lambda: state.check("call_in").reason == "authority_invalid")
        assert admission_gate.probe(socket_path, "call_in")["allowed"] is False
        # Re-publishing the last accepted sequence cannot recover from a damaged observation.
        atomic_authority(authority_path, authority(2))
        time.sleep(0.1)
        assert not state.check("call_in").allowed
        atomic_authority(authority_path, authority(3))
        wait_for(lambda: state.check("call_in").reason == "warmup")
        atomic_authority(authority_path, authority(4))
        wait_for(lambda: state.check("call_in").allowed)
    finally:
        service.stop()
        socket_path.unlink(missing_ok=True)


def test_local_fence_clears_cached_authority_and_status_exposes_identity(tmp_path):
    authority_path = tmp_path / "admission-authority.json"
    socket_path = Path(os.environ["TMPDIR"]) / f"mdd-gate-fence-{os.getpid()}-{time.time_ns()}.sock"
    status_path = tmp_path / "status.json"
    fence = tmp_path / "pcscf-rebind.json"
    state = admission_gate.GateState("7", RUN_ID, ttl=3.0)
    service = admission_gate.GateService(
        state, authority_path, socket_path, status_path, interval=0.02,
        fence_paths=((fence, "local_fence_pcscf_rebind"),))
    service.start()
    try:
        wait_for(lambda: socket_path.exists())
        atomic_authority(authority_path, authority(1, mode="normal_committed"))
        wait_for(lambda: state.check("call_in").reason == "warmup")
        atomic_authority(authority_path, authority(2, mode="normal_committed"))
        wait_for(lambda: admission_gate.probe(socket_path, "call_in")["allowed"])
        wait_for(lambda: json.loads(status_path.read_text(
            encoding="utf-8")).get("normal_state_digest") == "1" * 64)
        allowed_status = json.loads(status_path.read_text(encoding="utf-8"))
        assert allowed_status["authority_identity_digest"]
        assert allowed_status["normal_state_digest"] == "1" * 64

        fence.write_text("present", encoding="utf-8")
        wait_for(lambda: admission_gate.probe(socket_path, "sms_in")["allowed"] is False)
        wait_for(lambda: json.loads(status_path.read_text(
            encoding="utf-8")).get("reason") == "local_fence_pcscf_rebind")
        denied_status = json.loads(status_path.read_text(encoding="utf-8"))
        assert denied_status["reason"] == "local_fence_pcscf_rebind"
        assert denied_status["authority_identity_digest"] == ""

        fence.unlink()
        time.sleep(0.1)
        assert admission_gate.probe(socket_path, "sms_in")["allowed"] is False
        atomic_authority(authority_path, authority(3, mode="normal_committed"))
        wait_for(lambda: state.check("sms_in").reason == "warmup")
        atomic_authority(authority_path, authority(4, mode="normal_committed"))
        wait_for(lambda: admission_gate.probe(socket_path, "sms_in")["allowed"])
    finally:
        service.stop()
        socket_path.unlink(missing_ok=True)


def test_socket_request_synchronously_denies_removed_or_poisoned_authority(tmp_path):
    authority_path = tmp_path / "admission-authority.json"
    socket_path = Path(os.environ["TMPDIR"]) / f"mdd-gate-sync-{os.getpid()}-{time.time_ns()}.sock"
    status_path = tmp_path / "status.json"
    state = admission_gate.GateState("7", RUN_ID, ttl=3.0)
    service = admission_gate.GateService(
        state, authority_path, socket_path, status_path, interval=10.0)
    service.start()
    try:
        wait_for(lambda: socket_path.exists())
        atomic_authority(authority_path, authority(1, mode="normal_committed"))
        assert admission_gate.probe(socket_path, "sms_in")["allowed"] is False
        atomic_authority(authority_path, authority(2, mode="normal_committed"))
        assert admission_gate.probe(socket_path, "sms_in")["allowed"] is True

        authority_path.unlink()
        missing = admission_gate.probe(socket_path, "sms_in")
        assert missing["allowed"] is False
        assert missing["reason"] == "authority_missing"

        authority_path.write_text('{"mode":"deny_poison"}', encoding="utf-8")
        invalid = admission_gate.probe(socket_path, "sms_in")
        assert invalid["allowed"] is False
        assert invalid["reason"].startswith("authority_")
    finally:
        service.stop()
        socket_path.unlink(missing_ok=True)


def test_dialplan_uses_one_mt_commit_and_records_mo_only_after_submit_success():
    source = (Path(__file__).resolve().parents[1] / "engine" / "templates" /
              "extensions.conf.j2").read_text()
    expectations = {
        "[volte_ims]": ("MDD_ADMISSION(call_in)", "Progress()"),
        "exten => mdd-media-check": ("MDD_ADMISSION(media_check)", "Answer()"),
        "exten => _[+0-9].": ("MDD_ADMISSION(call_out)", "Set(MDD_CALL_ADMITTED=1)"),
        "[msg-from-local]": ("MDD_ADMISSION(sms_out)", "MessageSend("),
    }
    for anchor, (gate, side_effect) in expectations.items():
        section = source.split(anchor, 1)[1]
        assert section.index(gate) < section.index(side_effect), anchor
    for handler in source.split("exten => h,1")[1:]:
        assert "MDD_ADMISSION" not in handler.split("\n\n", 1)[0]
    mt = source.split("[volte_ims_msg]", 1)[1].split("\n[", 1)[0]
    assert "MDD_ADMISSION" not in mt
    assert "engine-maintenance.json" not in mt
    assert "pcscf-rebind.json" not in mt
    mo = source.split("[msg-from-local]", 1)[1].split("\n[", 1)[0]
    assert mo.index("MessageSend(") < mo.index("MESSAGE_SEND_STATUS") < mo.index(
        "Set(FILE(/logs/messages.txt") < mo.index("notify.py sms_out")
    assert '${MESSAGE_SEND_STATUS}" != "SUCCESS"' in mo


def test_engine_overlay_requires_exact_compiled_admission_abi():
    root = Path(__file__).resolve().parents[1]
    install = (root / "install.sh").read_text()
    dockerfile = (root / "engine" / "Dockerfile").read_text()
    assert 'ENGINE_ADMISSION_ABI="mdd-admission-v1"' in install
    assert 'io.mdd-sim-gateway.admission-abi="mdd-admission-v1"' in dockerfile
    assert "engine image predates fingerprinting" not in install
    assert "trusted local engine base lacks the exact base fingerprint/admission ABI" in install
    overlay_guard = install.split("engine_overlay_build() {", 1)[1].split(
        "overlay_container=", 1)[0]
    assert "io.mdd-sim-gateway.admission-abi" in overlay_guard


def test_reload_public_entrypoints_are_gated_until_replacement_wrapper_exists():
    root = Path(__file__).resolve().parents[1]
    install = (root / "install.sh").read_text()
    reload_body = install.split("cmd_reload() {", 1)[1].split("\n}\n\ncmd_start()", 1)[0]
    assert "preflight_reload_engine_admission" in reload_body
    assert "wait_engine_admission_authority" in reload_body
    assert "--engines is disabled until the production Engine replacement wrapper" in reload_body
    assert "docker rm -f \"$n\"" not in reload_body
    wait_body = install.split("admission_status_healthy() {", 1)[1].split(
        "\n}\n\nwait_engine_admission_authority()", 1)[0]
    assert "updated_at_ns" in wait_body
    assert "authority_identity_digest" in wait_body
    assert "normal_state_digest" in wait_body
    assert "grep -q" not in wait_body


def test_outer_and_runtime_scripts_are_published_as_one_runtime_abi():
    root = Path(__file__).resolve().parents[1]
    entrypoint = (root / "engine" / "entrypoint.sh").read_text()
    runtime = (root / "engine" / "engine-runtime.sh").read_text()
    dockerfile = (root / "engine" / "Dockerfile").read_text()
    overlay = (root / "engine" / "Dockerfile.overlay").read_text()
    install = (root / "install.sh").read_text()
    control = (root / "control" / "app" / "engine.py").read_text()
    assert "uuid.uuid4()" in entrypoint
    assert "supervise -- /bin/bash /engine-runtime.sh" in entrypoint
    assert "MDD_MANAGED_CHILD_PIDS" not in entrypoint
    assert "MDD_MANAGED_CHILD_PIDS" not in runtime
    assert "MDD_ENGINE_RUN_ID=" not in runtime
    assert "exec asterisk -f" in runtime
    for packaging in (dockerfile, overlay, install, control):
        assert "entrypoint.sh" in packaging
        assert "engine-runtime.sh" in packaging
    runtime_files = install.split('ENGINE_RUNTIME_FILES="', 1)[1].split('"', 1)[0]
    assert "entrypoint.sh" in runtime_files and "engine-runtime.sh" in runtime_files
    copy_map = install.split("overlay_ok=1", 1)[1].split("done", 1)[0]
    assert 'entrypoint.sh) destination="/entrypoint.sh"' in copy_map
    assert 'engine-runtime.sh) destination="/engine-runtime.sh"' in copy_map


class FakeService:
    def __init__(self, healthy=True):
        self.is_healthy = healthy
        self.started = 0
        self.stop_requested = 0
        self.stopped = 0

    def start(self):
        self.started += 1

    def healthy(self):
        return self.is_healthy

    def request_stop(self):
        self.stop_requested += 1

    def stop(self, *, deadline=None, timeout=2.0):
        self.stopped += 1


def test_supervisor_preserves_natural_asterisk_exit_status():
    service = FakeService()
    rc = admission_gate.supervise(
        service, [sys.executable, "-c", "raise SystemExit(7)"], stop_timeout=0.2)
    assert rc == 7
    assert (service.started, service.stopped) == (1, 1)


def test_gate_failure_boundedly_stops_asterisk_and_returns_nonzero():
    service = FakeService(healthy=False)
    before = time.monotonic()
    rc = admission_gate.supervise(
        service,
        [sys.executable, "-c",
         "import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(30)"],
        stop_timeout=0.15,
    )
    assert rc == 70
    assert time.monotonic() - before < 1.5
    assert service.stopped == 1


@pytest.mark.skipif(sys.platform == "win32", reason="POSIX signal lifecycle")
def test_signal_during_gate_start_prevents_runtime_spawn(monkeypatch):
    class SignalOnStart(FakeService):
        def start(self):
            super().start()
            os.kill(os.getpid(), signal.SIGTERM)

    spawned = []

    def forbidden_popen(*args, **kwargs):
        spawned.append((args, kwargs))
        raise AssertionError("runtime must not spawn after an early stop intent")

    monkeypatch.setattr(admission_gate.subprocess, "Popen", forbidden_popen)
    service = SignalOnStart()
    rc = admission_gate.supervise(service, ["never"], stop_timeout=0.2)
    assert rc == 128 + signal.SIGTERM
    assert spawned == []
    assert (service.started, service.stop_requested, service.stopped) == (1, 1, 1)


@pytest.mark.skipif(sys.platform == "win32", reason="POSIX signal lifecycle")
def test_signal_inside_popen_window_stops_spawned_process(monkeypatch, tmp_path):
    original_popen = subprocess.Popen
    child_pid = tmp_path / "spawned-pid"

    def signal_then_spawn(*args, **kwargs):
        os.kill(os.getpid(), signal.SIGTERM)
        process = original_popen(*args, **kwargs)
        child_pid.write_text(str(process.pid))
        return process

    monkeypatch.setattr(admission_gate.subprocess, "Popen", signal_then_spawn)
    service = FakeService()
    rc = admission_gate.supervise(
        service, [sys.executable, "-c", "import time; time.sleep(30)"], stop_timeout=0.3)
    pid = int(child_pid.read_text())
    assert rc != 0
    with pytest.raises(ProcessLookupError):
        os.kill(pid, 0)
    assert service.stopped == 1


@pytest.mark.skipif(sys.platform == "win32", reason="POSIX signal lifecycle")
def test_natural_leader_exit_still_terminates_same_group_helper(tmp_path):
    helper_pid = tmp_path / "helper-pid"
    helper_stopped = tmp_path / "helper-stopped"
    helper = tmp_path / "helper.py"
    leader = tmp_path / "leader.py"
    helper.write_text(
        "import os,signal,time\n"
        f"pid={str(helper_pid)!r}; stopped={str(helper_stopped)!r}\n"
        "open(pid,'w').write(str(os.getpid()))\n"
        "def term(*_):\n"
        "  open(stopped,'w').write('TERM')\n"
        "  raise SystemExit(0)\n"
        "signal.signal(signal.SIGTERM, term)\n"
        "while True: time.sleep(0.05)\n"
    )
    leader.write_text(
        "import subprocess,sys,time\n"
        f"subprocess.Popen([sys.executable, {str(helper)!r}])\n"
        f"p={str(helper_pid)!r}\n"
        "deadline=time.monotonic()+2\n"
        "while not __import__('os').path.exists(p) and time.monotonic()<deadline: time.sleep(.01)\n"
        "raise SystemExit(7)\n"
    )
    service = FakeService()
    rc = admission_gate.supervise(
        service, [sys.executable, str(leader)], stop_timeout=0.8)
    assert rc == 7
    wait_for(helper_stopped.exists)
    assert helper_stopped.read_text() == "TERM"
    assert service.stopped == 1


@pytest.mark.skipif(sys.platform == "win32", reason="POSIX signal lifecycle")
def test_pid1_forwards_duplicate_term_only_once(tmp_path):
    # Keep the actual socket on the external disk while staying under Darwin's 104-byte limit.
    short_root = Path(__file__).resolve().parents[2] / "codex-audit-tmp" / f"g-{os.getpid()}"
    short_root.mkdir()
    ready = tmp_path / "child-ready"
    signals = tmp_path / "child-signals"
    child = tmp_path / "child.py"
    child.write_text(
        "import os,signal,time\n"
        f"ready={str(ready)!r}; signals={str(signals)!r}\n"
        "def stop(*_):\n"
        "  with open(signals,'a') as handle: handle.write('TERM\\n')\n"
        "  time.sleep(0.15)\n"
        "  raise SystemExit(0)\n"
        "signal.signal(signal.SIGTERM, stop)\n"
        "open(ready,'w').write(str(os.getpid()))\n"
        "while True: time.sleep(0.05)\n"
    )
    command = [
        sys.executable, str(Path(admission_gate.__file__)),
        "--rundir", str(short_root), "--iid", "7", "--engine-run-id", RUN_ID,
        "supervise", "--", sys.executable, str(child),
    ]
    process = subprocess.Popen(command)
    try:
        wait_for(ready.exists)
        os.kill(process.pid, signal.SIGTERM)
        os.kill(process.pid, signal.SIGTERM)
        assert process.wait(timeout=3) == 0
        assert signals.read_text().splitlines() == ["TERM"]
    finally:
        if process.poll() is None:
            process.kill()
            process.wait()
        for path in short_root.iterdir():
            path.unlink()
        short_root.rmdir()
