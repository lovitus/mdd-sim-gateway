import json
import hashlib
import io
import os
import subprocess
import sys
import threading
import time
import types

import pytest

sys.modules.setdefault("websocket", types.SimpleNamespace())

from agent import cli, config_store
from agent.config_store import ConfigError, ConfigStore, validate_config
from agent.control_contract import (
    ADMIN, MAX_MESSAGE_BYTES, METHOD_PERMISSIONS, ProtocolError, decode_request, encode_message,
    request,
)
from agent.local_control import ControlClient, ControlServer, _open_windows_pipe, _unix_socket_path
from agent.agent_host import AgentHost, HostConflictError, InstallationLease
from agent.managed_runtime import ManagedAgentRuntime
from agent.state_store import StateCorruptError, TransactionalJsonState
from control.app.agent_health_registry import validate_snapshot as validate_health_snapshot


class FakeRuntime:
    def __init__(self):
        self.calls = []

    def execute(self, method, params, role):
        self.calls.append((method, params, role))
        return {"method": method, "params": params, "role": role}


class FakeServices:
    def __init__(self):
        self.actions = []

    def status(self):
        return {"installed": True, "state": "running"}

    def action(self, action):
        self.actions.append(action)
        return {"installed": True, "state": "running", "action": action}


class FakeClient:
    def __init__(self):
        self.calls = []

    def call(self, method, params=None, deadline_ms=15000):
        self.calls.append((method, params or {}))
        if method == "status":
            return {"runtime": "online", "state_revision": 4}
        if method in {"doctor", "self-test"}:
            return {"healthy": True}
        if method == "maintenance.prepare-install":
            return {"ready": True, "status": "maintenance_ready", "nonce": "n" * 32}
        if method == "maintenance.cancel-install":
            return {"cancelled": True, "status": "maintenance_cancelled"}
        return {"ok": True, "method": method}


def test_config_store_round_trip_and_redacts_token(tmp_path, monkeypatch):
    monkeypatch.setenv("MDD_AGENT_DATA_DIR", str(tmp_path))
    store = ConfigStore(keychain=False)
    shown = store.save({"server": "gateway.example:8443", "token": "secret"})
    assert shown["token"] == {"configured": True, "value": "<redacted>"}
    assert store.load()["token"] == "secret"
    assert json.loads(store.config_path.read_text(encoding="utf-8"))["token"] == "secret"
    assert store.config_path.stat().st_mode & 0o077 == 0
    assert not store.secret_path.exists()


def test_legacy_secret_is_migrated_to_config_on_explicit_save(tmp_path):
    legacy = ConfigStore(tmp_path, keychain=False)
    legacy.ensure_dirs()
    payload = json.dumps({"token": "legacy-secret"}).encode()
    legacy.secret_path.write_bytes(ConfigStore._protect(payload))
    os.chmod(legacy.secret_path, 0o600)
    assert legacy.load()["token"] == "legacy-secret"
    legacy.save({"server": "gateway.example:8443"})
    assert json.loads(legacy.config_path.read_text(encoding="utf-8"))["token"] == "legacy-secret"
    assert not legacy.secret_path.exists()


def test_saved_token_precedes_session_fallback(tmp_path):
    store = ConfigStore(tmp_path, keychain=False)
    store.set_session_token("ephemeral")
    assert store.load()["token"] == "ephemeral"
    store.save({"token": "saved"})
    assert store.load()["token"] == "saved"
    assert not store.secret_path.exists()


def test_config_store_rejects_unsafe_or_symlinked_token_file(tmp_path):
    store = ConfigStore(tmp_path / "data", keychain=False)
    store.save({"token": "secret"})
    os.chmod(store.config_path, 0o644)
    with pytest.raises(ConfigError, match="0600"):
        store.load()
    os.chmod(store.config_path, 0o600)
    target = tmp_path / "target.json"
    target.write_text(store.config_path.read_text(encoding="utf-8"), encoding="utf-8")
    os.chmod(target, 0o600)
    store.config_path.unlink()
    store.config_path.symlink_to(target)
    with pytest.raises(ConfigError, match="invalid type"):
        store.load()


def test_replace_preserves_token_unless_explicitly_cleared(tmp_path):
    store = ConfigStore(tmp_path, keychain=False)
    store.save({"token": "preserved", "server": "old.example:8443"})
    store.save({"server": "new.example:8443"}, replace=True)
    assert store.load()["token"] == "preserved"
    store.save({"token": ""})
    assert store.load()["token"] == ""


def test_windows_dpapi_uses_stable_machine_scope_flag(monkeypatch):
    calls = []
    fake = types.SimpleNamespace(
        CryptProtectData=lambda *args: calls.append(args) or b"protected"
    )
    monkeypatch.setitem(sys.modules, "win32crypt", fake)
    monkeypatch.setattr(config_store.os, "name", "nt")
    assert ConfigStore._protect(b"secret") == b"DPAPI1\0protected"
    assert calls[0][-1] == 0x4


def test_windows_save_keeps_token_in_dpapi_not_config(monkeypatch, tmp_path):
    fake = types.SimpleNamespace(
        CryptProtectData=lambda raw, *_args: b"protected:" + raw,
        CryptUnprotectData=lambda raw, *_args: (None, raw.removeprefix(b"protected:")),
    )
    store = ConfigStore(tmp_path, keychain=False)
    monkeypatch.setitem(sys.modules, "win32crypt", fake)
    monkeypatch.setattr(config_store.os, "name", "nt")
    store.save({"token": "windows-secret"})
    assert "token" not in json.loads(store.config_path.read_text(encoding="utf-8"))
    assert store.secret_path.read_bytes().startswith(b"DPAPI1\0")
    assert store.load()["token"] == "windows-secret"


def test_config_validation_rejects_unknown_and_invalid_values():
    with pytest.raises(ConfigError, match="unknown"):
        validate_config({"version": 1, "surprise": True})
    with pytest.raises(ConfigError, match="host:port"):
        validate_config({"version": 1, "server": "gateway"})


def test_control_contract_is_versioned_and_bounded():
    value = request("status")
    assert decode_request(encode_message(value))["method"] == "status"
    bad = dict(value, version=999)
    with pytest.raises(ProtocolError, match="version"):
        decode_request(encode_message(bad))
    with pytest.raises(ProtocolError, match="1 MiB"):
        decode_request(b"x" * (MAX_MESSAGE_BYTES + 1))
    expired = dict(value, created_ms=1)
    with pytest.raises(ProtocolError, match="deadline"):
        decode_request(encode_message(expired))
    assert METHOD_PERMISSIONS["config.set"] == ADMIN


def test_local_control_scrubs_persistent_and_request_tokens(tmp_path):
    canary = "CANARY-TOKEN-DO-NOT-LEAK"
    store = ConfigStore(tmp_path, keychain=False)
    store.save({"token": canary})

    class Runtime:
        def __init__(self):
            self.store = store

        def execute(self, method, params, _role):
            if method == "config.set":
                raise RuntimeError(f"write failed around {params['changes']['token']}")
            return {"detail": f"connected with {canary}", "token": canary}

    server = ControlServer(Runtime(), tmp_path)
    success = json.loads(server._dispatch(encode_message(request("status")), "admin"))
    assert canary not in json.dumps(success)
    assert success["result"]["token"] == "<redacted>"
    failed = json.loads(server._dispatch(encode_message(request(
        "config.set", {"changes": {"token": canary}})), "admin"))
    assert canary not in json.dumps(failed)
    assert "<redacted>" in failed["error"]["message"]

    class FailureWithoutSecretParams:
        def __init__(self):
            self.store = store

        def execute(self, _method, _params, _role):
            raise RuntimeError(f"transport rejected persisted credential {canary}")

    server = ControlServer(FailureWithoutSecretParams(), tmp_path)
    failed = json.loads(server._dispatch(encode_message(request("status")), "admin"))
    assert canary not in json.dumps(failed)
    assert "<redacted>" in failed["error"]["message"]


@pytest.mark.skipif(os.name == "nt", reason="portable socket test covers non-Windows transport")
def test_control_client_and_server_use_same_contract(tmp_path):
    runtime = FakeRuntime()
    server = ControlServer(runtime, tmp_path)
    server.start()
    path = __import__("pathlib").Path(_unix_socket_path(tmp_path))
    deadline = time.monotonic() + 2
    while not path.exists() and time.monotonic() < deadline:
        time.sleep(0.01)
    try:
        result = ControlClient(tmp_path).call("devices", {"detail": True})
        assert result == {"method": "devices", "params": {"detail": True}, "role": "admin"}
        assert runtime.calls == [("devices", {"detail": True}, "admin")]
    finally:
        server.stop()


@pytest.mark.skipif(os.name == "nt", reason="flock lease is the macOS/POSIX host boundary")
def test_installation_lease_rejects_duplicate_host(tmp_path):
    first = InstallationLease(tmp_path / "state")
    second = InstallationLease(tmp_path / "state")
    first.acquire()
    try:
        with pytest.raises(HostConflictError):
            second.acquire()
        assert (tmp_path / "state" / "host.lock").stat().st_mode & 0o077 == 0
    finally:
        first.release()


@pytest.mark.skipif(os.name == "nt", reason="POSIX host lifecycle")
def test_agent_host_owns_runtime_and_control_under_one_lease(tmp_path):
    events = []

    class Runtime:
        def start(self):
            events.append("runtime.start")

        def stop(self):
            events.append("runtime.stop")

    class Control:
        def start(self):
            events.append("control.start")

        def stop(self):
            events.append("control.stop")

    class Lease:
        def acquire(self):
            events.append("lease.acquire")

        def release(self):
            events.append("lease.release")

    store = ConfigStore(tmp_path, keychain=False)
    host = AgentHost(store, runtime=Runtime(), control=Control(),
                     lease=Lease())
    host.start()
    host.stop()

    assert events == ["lease.acquire", "control.start", "runtime.start",
                      "control.stop", "runtime.stop", "lease.release"]


@pytest.mark.skipif(os.name == "nt", reason="POSIX host lifecycle")
def test_agent_host_retains_lease_when_runtime_does_not_stop(tmp_path):
    events = []
    runtime = types.SimpleNamespace(
        start=lambda: None, stop=lambda: False)
    control = types.SimpleNamespace(
        start=lambda: None, stop=lambda: True)
    lease = types.SimpleNamespace(
        acquire=lambda: events.append("acquire"), release=lambda: events.append("release"))
    host = AgentHost(ConfigStore(tmp_path, keychain=False), runtime=runtime,
                     control=control, lease=lease)
    host.start()

    with pytest.raises(RuntimeError, match="lease retained"):
        host.stop()

    assert events == ["acquire"]


@pytest.mark.skipif(os.name == "nt", reason="POSIX host lifecycle")
def test_failed_startup_cleanup_cannot_be_released_by_gui_or_stop(tmp_path):
    events = []
    runtime = types.SimpleNamespace(
        start=lambda: (_ for _ in ()).throw(RuntimeError("start failed")),
        stop=lambda: False)
    control = types.SimpleNamespace(start=lambda: None, stop=lambda: True)
    lease = types.SimpleNamespace(
        acquire=lambda: events.append("acquire"),
        release=lambda: events.append("release"))
    host = AgentHost(ConfigStore(tmp_path, keychain=False), runtime=runtime,
                     control=control, lease=lease)

    with pytest.raises(RuntimeError, match="start failed"):
        host.start()
    assert host.release_lease_if_idle() is False
    with pytest.raises(RuntimeError, match="cleanup is still blocked"):
        host.stop()
    assert events == ["acquire"]


@pytest.mark.skipif(os.name == "nt", reason="POSIX host lifecycle")
def test_startup_cleanup_exception_still_stops_other_component_and_retains_lease(tmp_path):
    events = []
    runtime = types.SimpleNamespace(
        start=lambda: (_ for _ in ()).throw(RuntimeError("start failed")),
        stop=lambda: events.append("runtime.stop") or True)
    control = types.SimpleNamespace(
        start=lambda: None,
        stop=lambda: (_ for _ in ()).throw(RuntimeError("control stuck")))
    lease = types.SimpleNamespace(
        acquire=lambda: events.append("acquire"),
        release=lambda: events.append("release"))
    host = AgentHost(ConfigStore(tmp_path, keychain=False), runtime=runtime,
                     control=control, lease=lease)

    with pytest.raises(RuntimeError, match="start failed"):
        host.start()

    assert events == ["acquire", "runtime.stop"]
    assert host.release_lease_if_idle() is False


@pytest.mark.skipif(os.name == "nt", reason="portable POSIX listener readiness")
def test_control_start_reports_bind_failure_before_runtime_can_start(tmp_path, monkeypatch):
    server = ControlServer(FakeRuntime(), tmp_path)
    monkeypatch.setattr("agent.local_control.socket.socket.bind",
                        lambda *_args, **_kwargs: (_ for _ in ()).throw(OSError("bind failed")))
    with pytest.raises(OSError, match="bind failed"):
        server.start()


def test_control_rejects_mutating_request_after_shutdown_begins(tmp_path):
    runtime = FakeRuntime()
    server = ControlServer(runtime, tmp_path)
    server._stop.set()
    payload = encode_message(request("reconnect"))
    value = json.loads(server._dispatch(payload, "admin"))
    assert value["ok"] is False
    assert value["error"]["code"] == "shutting_down"
    assert runtime.calls == []


def test_runtime_exposes_multi_modem_contract_with_singular_compatibility(tmp_path):
    first = types.SimpleNamespace(imei="111", iccid="a", model="EC20", port_name="raw:1",
                                  capabilities={"sms": True}, connection=object())
    second = types.SimpleNamespace(imei="222", iccid="b", model="RM500", port_name="raw:2",
                                   capabilities={"sms": True}, connection=object())
    runtime = ManagedAgentRuntime(ConfigStore(tmp_path, keychain=False))

    runtime._update("online", modems=[first, second])
    snapshot = runtime.snapshot()

    assert [item["imei"] for item in snapshot["modems"]] == ["111", "222"]
    assert snapshot["modem"] == snapshot["modems"][0]
    assert snapshot["modems"][1]["identity"] == "imei:222"


def test_runtime_status_deduplicates_provider_views_of_one_physical_modem(tmp_path):
    first = types.SimpleNamespace(
        imei="111", iccid="8901", model="modem", port_name="COM1",
        capabilities={"sms": True}, connection=object())
    second = types.SimpleNamespace(
        imei="111", iccid="8901", model="modem", port_name="COM1",
        capabilities={"sms": True}, connection=object())
    runtime = ManagedAgentRuntime(ConfigStore(tmp_path, keychain=False))
    runtime._modems = {"provider:wwan": first, "provider:serial": second}

    snapshot = runtime.snapshot()

    assert len(snapshot["modems"]) == 1
    assert snapshot["modems"][0]["identity"] == "imei:111"
    assert runtime.health_snapshot()["inventory"] == {
        "modems_total": 1, "modems_connected": 1,
    }


def test_runtime_install_maintenance_delegates_to_live_modem_control(tmp_path):
    calls = []
    control = types.SimpleNamespace(
        prepare_install_maintenance=lambda: calls.append("prepare") or {
            "ready": True, "status": "maintenance_ready", "nonce": "n" * 32},
        cancel_install_maintenance=lambda nonce: calls.append(("cancel", nonce)) or {
            "cancelled": True, "status": "maintenance_cancelled"},
    )
    runtime = ManagedAgentRuntime(ConfigStore(tmp_path, keychain=False))
    runtime._state = "online"
    runtime._control = control

    prepared = runtime.execute("maintenance.prepare-install", {}, "admin")
    cancelled = runtime.execute(
        "maintenance.cancel-install", {"nonce": prepared["nonce"]}, "admin")

    assert prepared["ready"] is True
    assert cancelled["cancelled"] is True
    assert calls == ["prepare", ("cancel", "n" * 32)]


def test_cli_install_maintenance_uses_private_control_contract(tmp_path):
    client = FakeClient()
    assert cli.run_cli(
        ["maintenance", "prepare-install", "--json"],
        store=ConfigStore(tmp_path, keychain=False), client=client,
        services=FakeServices()) == 0
    assert client.calls == [("maintenance.prepare-install", {})]


def test_cli_explicitly_forwards_supervised_legacy_migration(monkeypatch, tmp_path):
    calls = []

    class Services(FakeServices):
        def prepare(self):
            calls.append("prepare")

        def install(self, **kwargs):
            calls.append(("install", kwargs))
            return {"installed": True, "state": "running"}

    store = ConfigStore(tmp_path, keychain=False)
    store.save({"token": "saved"})
    monkeypatch.setattr(cli, "is_windows_admin", lambda: True)

    assert cli.run_cli([
        "service", "install", "--supervised-legacy-idle-migration", "--json",
    ], store=store, client=FakeClient(), services=Services()) == 0
    assert calls == ["prepare", ("install", {
        "reader_only": False, "supervised_legacy_idle_migration": True,
    })]


def test_runtime_persists_isolation_failure_until_a_worker_is_online(tmp_path):
    runtime = ManagedAgentRuntime(ConfigStore(tmp_path, keychain=False))
    runtime._update(
        "ready", modems=[],
        isolation_error="isolation_not_proven: macOS routes changed")

    failed = runtime.snapshot()
    assert failed["isolation"] == {
        "ready": False,
        "error": "isolation_not_proven: macOS routes changed",
    }

    runtime._update("online", modems=[], isolation_error="")
    assert runtime.snapshot()["isolation"] == {"ready": True, "error": None}


def test_runtime_health_snapshot_is_cached_redacted_and_non_invasive(tmp_path, monkeypatch):
    modem = types.SimpleNamespace(
        hardware_id="PRIVATE-HARDWARE-ID", imei="PRIVATE-IMEI", iccid="PRIVATE-ICCID",
        model="PRIVATE-MODEL", port_name="PRIVATE-PORT", capabilities={},
        connection=object())
    runtime = ManagedAgentRuntime(ConfigStore(tmp_path, keychain=False))
    runtime._state = "online"
    runtime._modems = {"one": modem}
    monkeypatch.setattr(runtime, "devices", lambda: (_ for _ in ()).throw(
        AssertionError("health must not enumerate hardware")))
    monkeypatch.setattr(runtime, "doctor", lambda: (_ for _ in ()).throw(
        AssertionError("health must not run probes")))

    snapshot = runtime.health_snapshot()
    validate_health_snapshot(snapshot)
    encoded = json.dumps(snapshot)
    assert snapshot["inventory"] == {"modems_total": 1, "modems_connected": 1}
    for secret in ("PRIVATE-HARDWARE-ID", "PRIVATE-IMEI", "PRIVATE-ICCID",
                   "PRIVATE-MODEL", "PRIVATE-PORT"):
        assert secret not in encoded


def test_linux_health_transport_reports_collection_unsupported(tmp_path, monkeypatch):
    monkeypatch.setattr("agent.managed_runtime.os.name", "posix")
    monkeypatch.setattr("agent.managed_runtime.sys.platform", "linux")
    runtime = ManagedAgentRuntime(ConfigStore(tmp_path, keychain=False))
    runtime._state = "online"
    snapshot = runtime.health_snapshot()
    assert snapshot["support"] == "unsupported"
    assert snapshot["overall"] == "unsupported"
    assert snapshot["isolation"]["state"] == "unsupported"
    validate_health_snapshot(snapshot)


def test_cleanup_blocked_runtime_keeps_health_reporter_until_worker_exits(tmp_path):
    events = []

    class Thread:
        alive = True

        def is_alive(self):
            return self.alive

        def join(self, _timeout):
            events.append("runtime.join")

    class Reporter:
        def notify_changed(self):
            events.append("health.changed")

        def stop(self):
            events.append("health.stop")
            return True

    runtime = ManagedAgentRuntime(ConfigStore(tmp_path, keychain=False))
    thread = Thread()
    runtime._thread = thread
    runtime._health_reporter = Reporter()

    assert runtime.stop(timeout=0) is False
    assert "health.stop" not in events
    assert runtime._health_reporter is not None

    thread.alive = False
    assert runtime.stop(timeout=0) is True
    assert events.count("health.stop") == 1
    assert runtime._health_reporter is None


def test_health_reporter_timeout_never_retains_hardware_installation_lease(tmp_path):
    class Reporter:
        def stop(self):
            return False

    runtime = ManagedAgentRuntime(ConfigStore(tmp_path, keychain=False))
    reporter = Reporter()
    runtime._health_reporter = reporter
    assert runtime._stop_health_reporter() is True
    assert runtime._health_reporter is None


def test_transactional_action_state_fails_closed_when_corrupt(tmp_path):
    path = tmp_path / "actions.json"
    path.write_text("{not-json", encoding="utf-8")
    state = TransactionalJsonState(path)

    with pytest.raises(StateCorruptError, match="action state is unreadable"):
        state.load()
    with pytest.raises(StateCorruptError, match="action state is unreadable"):
        state.update(lambda value: value.__setitem__("unsafe-action", 1))

    assert path.read_text(encoding="utf-8") == "{not-json"


def test_windows_control_client_retries_pipe_instance_rotation():
    class PipeError(Exception):
        def __init__(self, winerror):
            super().__init__(winerror)
            self.winerror = winerror

    class FakeFile:
        OPEN_EXISTING = 3

        def __init__(self):
            self.attempts = 0

        def CreateFile(self, *_args):
            self.attempts += 1
            if self.attempts == 1:
                raise PipeError(231)
            return "pipe-handle"

    fake_file = FakeFile()
    fake_pipe = types.SimpleNamespace(WaitNamedPipe=lambda *_args: None)
    fake_errors = types.SimpleNamespace(error=PipeError)
    assert _open_windows_pipe(fake_file, fake_pipe, fake_errors, 1000) == "pipe-handle"
    assert fake_file.attempts == 2


def test_cli_status_combines_scm_and_runtime(capsys, tmp_path):
    code = cli.run_cli(["status", "--json"],
                       store=ConfigStore(tmp_path, keychain=False),
                       client=FakeClient(), services=FakeServices())
    assert code == 0
    value = json.loads(capsys.readouterr().out)
    assert value["service"]["state"] == "running"
    assert value["runtime"]["state_revision"] == 4


def test_cli_json_is_ascii_safe_for_windows_ssh(capsys):
    value = {"log": "设备已锁定 🔒"}
    cli._emit(value, True)
    output = capsys.readouterr().out
    assert output.isascii()
    assert "\\u8bbe\\u5907" in output
    assert "\\ud83d\\udd12" in output
    assert json.loads(output) == value


def test_cli_non_json_falls_back_only_for_unencodable_gbk(monkeypatch):
    binary = io.BytesIO()
    stdout = io.TextIOWrapper(binary, encoding="gbk", errors="strict")
    monkeypatch.setattr(cli.sys, "stdout", stdout)

    cli._emit({"log": "设备已锁定 🔒"}, False)
    stdout.flush()

    rendered = binary.getvalue().decode("gbk")
    assert "设备已锁定" in rendered
    assert "\\U0001f512" in rendered


def test_cli_non_json_does_not_swallow_pipe_errors(monkeypatch):
    class BrokenStdout:
        encoding = "gbk"

        def write(self, _value):
            raise BrokenPipeError("closed")

        def flush(self):
            return None

    monkeypatch.setattr(cli.sys, "stdout", BrokenStdout())
    with pytest.raises(BrokenPipeError, match="closed"):
        cli._emit("status", False)


def test_cli_service_action_and_config_share_clients(capsys, tmp_path):
    services = FakeServices()
    client = FakeClient()
    assert cli.run_cli(["service", "restart", "--json"],
                       store=ConfigStore(tmp_path, keychain=False),
                       client=client, services=services) == 0
    assert services.actions == ["restart"]
    assert cli.run_cli(["config", "set", "server", "gateway.example:8443", "--json"],
                       store=ConfigStore(tmp_path, keychain=False), client=client,
                       services=services) == 0
    assert client.calls[-1] == ("config.set", {"changes": {"server": "gateway.example:8443"}})


def test_cli_refuses_token_in_process_arguments(capsys, tmp_path):
    result = cli.run_cli(["config", "set", "token", "dont-put-this-in-argv"],
                         store=ConfigStore(tmp_path, keychain=False), client=FakeClient(),
                         services=FakeServices())
    assert result == 3
    assert "--stdin" in capsys.readouterr().err


@pytest.mark.skipif(os.name == "nt", reason="offline bootstrap is a user-session host feature")
def test_cli_can_bootstrap_config_before_posix_host_starts(capsys, tmp_path):
    class MissingClient:
        def call(self, *_args, **_kwargs):
            raise FileNotFoundError("host is not running")

    store = ConfigStore(tmp_path, keychain=False)
    result = cli.run_cli(
        ["config", "set", "server", "gateway.example:8443", "--json"],
        store=store, client=MissingClient(), services=FakeServices())

    assert result == 0
    assert store.load()["server"] == "gateway.example:8443"
    assert json.loads(capsys.readouterr().out)["offline"] is True


@pytest.mark.skipif(os.name == "nt", reason="macOS/POSIX host command")
def test_cli_duplicate_host_has_stable_conflict_exit_code(capsys, tmp_path):
    class ConflictingHost:
        def run_forever(self):
            raise HostConflictError("already running")

    result = cli.run_cli(
        ["run"], store=ConfigStore(tmp_path, keychain=False), client=FakeClient(),
        services=FakeServices(),
        host_factory=lambda _store: ConflictingHost(),
    )

    assert result == 9
    assert "already running" in capsys.readouterr().err


@pytest.mark.skipif(os.name == "nt", reason="macOS/POSIX host command")
def test_cli_run_token_precedence_is_config_argument_environment(monkeypatch, tmp_path):
    observed = []

    class Host:
        def __init__(self, store):
            self.store = store

        def run_forever(self):
            observed.append(self.store.load()["token"])

    monkeypatch.setenv("MDD_AGENT_TOKEN", "environment-secret")
    store = ConfigStore(tmp_path / "argument", keychain=False)
    assert cli.run_cli(
        ["run", "--token", "argument-secret"], store=store, client=FakeClient(),
        services=FakeServices(), host_factory=Host) == 0
    assert observed == ["argument-secret"]

    store = ConfigStore(tmp_path / "saved", keychain=False)
    store.save({"token": "saved-secret"})
    assert cli.run_cli(
        ["run", "--token", "ignored-argument"], store=store, client=FakeClient(),
        services=FakeServices(), host_factory=Host) == 0
    assert observed == ["argument-secret", "saved-secret"]

    store = ConfigStore(tmp_path / "environment", keychain=False)
    assert cli.run_cli(
        ["run"], store=store, client=FakeClient(), services=FakeServices(),
        host_factory=Host) == 0
    assert observed == ["argument-secret", "saved-secret", "environment-secret"]
    assert not store.secret_path.exists()


@pytest.mark.skipif(os.name == "nt", reason="macOS/POSIX host command")
def test_saved_cli_token_does_not_consume_token_stdin(monkeypatch, tmp_path):
    class UnreadableStdin:
        def readline(self):
            raise AssertionError("saved config must prevent stdin consumption")

    class Host:
        def __init__(self, store):
            self.store = store

        def run_forever(self):
            assert self.store.load()["token"] == "saved"

    store = ConfigStore(tmp_path, keychain=False)
    store.save({"token": "saved"})
    monkeypatch.setattr(sys, "stdin", UnreadableStdin())
    assert cli.run_cli(
        ["run", "--token-stdin"], store=store, client=FakeClient(),
        services=FakeServices(), host_factory=Host) == 0


@pytest.mark.skipif(os.name == "nt", reason="macOS/POSIX host command")
def test_macos_cli_requests_permission_only_after_host_started(monkeypatch, tmp_path):
    events = []

    class Host:
        runtime = object()

        def __init__(self, _store):
            pass

        def run_forever(self, after_start=None):
            events.append("started")
            after_start(self)

    monkeypatch.setattr(cli.sys, "platform", "darwin")
    monkeypatch.setattr(cli, "_request_macos_cli_audio_permission",
                        lambda _host: events.append("permission"))
    assert cli.run_cli(
        ["run"], store=ConfigStore(tmp_path, keychain=False), client=FakeClient(),
        services=FakeServices(), host_factory=Host) == 0
    assert events == ["started", "permission"]


def test_macos_cli_permission_grant_reprobes_only_audio(monkeypatch, capsys):
    events = []
    runtime = types.SimpleNamespace(
        reprobe_audio=lambda: events.append("audio.reprobe") or {"ready": True})
    monkeypatch.setattr("agent.call_audio._mac_microphone_permission",
                        lambda **_kwargs: "authorized")

    cli._request_macos_cli_audio_permission(types.SimpleNamespace(runtime=runtime))

    assert events == ["audio.reprobe"]
    assert "call audio is ready" in capsys.readouterr().err


def test_windows_installer_establishes_protected_trust_boundary():
    script = (__import__("pathlib").Path(__file__).parents[1] /
              "agent" / "windows" / "install.ps1").read_text(encoding="utf-8")
    assert "$env:ProgramFiles" in script
    assert "*S-1-5-18" in script
    assert "*S-1-5-32-544" in script
    assert "FileSystemRights]::WriteData" in script
    assert "FileSystemRights]::DeleteSubdirectoriesAndFiles" in script
    assert "Invoke-NativeChecked" in script
    assert "Manifest hash mismatch" in script
    assert "Assert-CallAudioHelperProtocol" in script
    assert "Call-audio helper protocol v2 or newer is required" in script
    assert "Staged manifest hash mismatch" in script
    assert 'Assert-CallAudioHelperProtocol -Path (Join-Path $stage' in script
    assert 'GetType().FullName' in script
    assert '"System.Boolean"' in script
    assert '"^[0-9a-f]{32}$"' in script
    assert "Required payload is not covered by the manifest" in script
    assert "invalid, duplicate, or unsupported component name" in script
    assert "Could not remove failed staging directory" in script
    assert "Staged payload set does not exactly match the signed manifest" in script
    assert '@("maintenance", "prepare-install", "--json")' in script
    assert "function Invoke-NativeCaptured" in script
    assert '$ErrorActionPreference = "Continue"' in script
    assert '$ErrorActionPreference = $previousErrorAction' in script
    assert '$global:LASTEXITCODE = $null' in script
    assert '$code = $global:LASTEXITCODE' in script
    assert 'Get-Command $Path -CommandType Application' in script
    assert 'Invoke-NativeCaptured -Path $installedBinary' in script
    assert '@("status", "--json")' in script
    assert '@("doctor", "--json")' in script
    assert '@("self-test", "--json")' in script
    assert "$runtimePackageDigest" in script
    assert "$manifestDigest" in script
    assert "[StringComparer]::Ordinal" in script
    assert "Manifest size mismatch" in script
    assert "must be flat and contain no reparse points" in script
    assert '"control-agent-allowlist.env"' in script
    assert "Package trust anchor does not match" in script
    assert "$expectedStageNames" in script
    assert "paid-call-*.json" in script
    assert "AllowLegacyMaintenancePreflight" in script
    assert "if ($serviceExisted) {" in script
    assert "Assert-LegacyTaskMaintenance" in script
    assert r'Join-Path $profilePath ".mdd-agent\state"' in script
    assert "$legacyTaskStopAttempted = $true" in script
    assert "$legacyWasRunning" in script
    assert "($legacyWasEnabled -or $legacyWasRunning)" in script
    assert "Get-LegacyTaskProfilePath" in script
    assert "Start-ScheduledTask -TaskName $legacyTaskName -ErrorAction Stop" in script
    assert "Stop it explicitly; the installer will not terminate it" in script
    assert "Get-CimInstance Win32_Group" in script
    assert "Get-CimAssociatedInstance" in script
    assert 'Invoke-NativeChecked -FilePath "net.exe"' in script
    assert "[ADSI]" not in script
    assert "Invoke-CimMethod -ClassName Win32_Service -MethodName Create" in script
    assert "-MethodName Change" in script
    assert "ServiceType = [byte]16" in script
    assert "ErrorControl = [byte]1" in script
    assert "Wait-AgentLeaseReleased" in script
    assert "Global\\MDDUnifiedAgent-v1" in script
    assert "param([int]$Seconds = 90)" in script
    assert script.count("Wait-AgentLeaseReleased") >= 4
    uninstall = script[script.index('if ($Action -eq "Uninstall") {'):]
    uninstall = uninstall[:uninstall.index("if (-not (Test-Path -LiteralPath $BinaryPath")]
    assert uninstall.index("Stop-ServiceBounded") < uninstall.index("Wait-AgentLeaseReleased")
    assert uninstall.index("Wait-AgentLeaseReleased") < uninstall.index('@("delete", $serviceName)')
    assert "if (-not $rollbackSafeToMutate)" in script
    assert "Rollback stopped safely before changing files" in script
    assert "Import-LegacyAgentIdentity" in script
    assert "Win32_UserProfile" in script
    assert '"^[0-9a-fA-F]{32}$"' in script
    assert "-not $serviceExisted -and $legacyTask" in script
    assert "if (Test-Path -LiteralPath $TargetPath -PathType Leaf)" in script
    assert "if ($sid -ne $InstallerSid)" in script
    assert "refusing to manufacture a replacement identity" in script
    assert "Move-Item -LiteralPath $temporary -Destination $TargetPath -Force" not in script
    assert "$legacyIdentityCreated = Import-LegacyAgentIdentity" in script
    rollback = script[script.index("} catch {"):]
    assert "if ($legacyIdentityCreated)" in rollback
    assert 'Remove-Item -LiteralPath (Join-Path $DataDir "state\\identity.json") -Force' in rollback
    assert '"MODEM_AGENT.md"' in script
    assert '@("create", $serviceName' not in script
    assert '@("config", $serviceName' not in script


def test_windows_package_assembly_requires_prebuilt_helpers():
    script = (__import__("pathlib").Path(__file__).parents[1] /
              "agent" / "windows" / "Build-Windows-Package.ps1").read_text(encoding="utf-8")
    for name in ("mdd-network-guard.exe", "mdd-windows-mbn.exe",
                 "mdd-call-audio-helper.exe", "mdd-agent-gui.exe"):
        assert name in script
    assert "package_manifest.py" in script
    assert "--expect-architecture windows-amd64" in script
    assert "--no-allowlist" not in script
    assert "[switch]$Overwrite" in script
    assert "protected system tree" in script
    assert "Assert-NoReparseTree" in script
    assert "containing a reparse point" in script
    assert "overlaps HelperDir" in script
    assert "$defaultPackageOutput" in script
    assert "Only the declared Windows package directory" in script
    assert "GetFinalPathNameByHandle" in script
    assert "CreateFile" in script
    assert "UNC and extended/device package paths are not allowed" in script
    assert "Mapped network package paths are not allowed" in script
    assert "bytes =" not in script
    assert "Assert-CallAudioHelperProtocol" in script
    assert "Call-audio helper protocol v2 or newer is required" in script
    assert '"System.Boolean"' in script


def test_agent_package_manifest_builder_writes_digest_and_allowlist(tmp_path):
    from agent.package_manifest import (
        PackageManifestError, verify_package_manifest, write_package_metadata,
    )

    root = tmp_path / "package"
    (root / "MDD Agent.app" / "Contents" / "MacOS").mkdir(parents=True)
    (root / "mdd-agent").write_text("cli", encoding="utf-8")
    (root / "mdd-cellular-io").write_text("cellular", encoding="utf-8")
    (root / "mdd-call-audio-helper").write_text("audio", encoding="utf-8")
    (root / "MDD Agent.app" / "Contents" / "MacOS" / "mdd-agent-gui").write_text(
        "gui", encoding="utf-8")

    digest = write_package_metadata(root, architecture="macos-arm64")

    manifest_path = root / "manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    names = {entry["name"] for entry in manifest["files"]}
    assert manifest["architecture"] == "macos-arm64"
    assert {"mdd-agent", "mdd-cellular-io", "mdd-call-audio-helper",
            "MDD Agent.app/Contents/MacOS/mdd-agent-gui"} <= names
    assert "manifest.json" not in names
    assert "control-agent-allowlist.env" not in names
    assert hashlib.sha256(manifest_path.read_bytes()).hexdigest() == digest
    assert verify_package_manifest(manifest_path, expect_digest=digest) == digest
    assert verify_package_manifest(
        manifest_path, expect_digest=digest,
        expect_architecture="macos-arm64") == digest
    with pytest.raises(PackageManifestError, match="does not match"):
        verify_package_manifest(manifest_path, expect_architecture="windows-amd64")
    assert (root / "control-agent-allowlist.env").read_text(encoding="utf-8") == \
        f"MDD_ALLOWED_AGENT_PACKAGE_DIGESTS={digest}\n"

    digest_without_allowlist = write_package_metadata(
        root, architecture="macos-arm64", emit_allowlist=False)
    assert digest_without_allowlist
    assert not (root / "control-agent-allowlist.env").exists()

    nested = tmp_path / "nested-metadata"
    (nested / "sub").mkdir(parents=True)
    (nested / "sub" / "manifest.json").write_text("bad", encoding="utf-8")
    with pytest.raises(PackageManifestError):
        write_package_metadata(nested, architecture="macos-arm64")

    symlinked = tmp_path / "symlinked"
    symlinked.mkdir()
    target = tmp_path / "target"
    target.write_text("target", encoding="utf-8")
    try:
        (symlinked / "payload").symlink_to(target)
    except (OSError, NotImplementedError):
        pass
    else:
        with pytest.raises(PackageManifestError):
            write_package_metadata(symlinked, architecture="macos-arm64")


def test_agent_package_manifest_rejects_extra_schema_properties(tmp_path):
    from agent.package_manifest import PackageManifestError, verify_package_manifest

    root = tmp_path / "package"
    root.mkdir()
    payload = root / "mdd-agent.exe"
    payload.write_bytes(b"agent")
    entry = {
        "name": payload.name, "size": payload.stat().st_size,
        "sha256": hashlib.sha256(payload.read_bytes()).hexdigest(),
    }
    manifest = {"version": 1, "architecture": "windows-amd64", "files": [entry]}
    path = root / "manifest.json"
    for mutate in (
            lambda value: value.__setitem__("unexpected", True),
            lambda value: value["files"][0].__setitem__("bytes", 5)):
        value = json.loads(json.dumps(manifest))
        mutate(value)
        path.write_text(json.dumps(value), encoding="utf-8")
        with pytest.raises(PackageManifestError, match="schema|object"):
            verify_package_manifest(path, expect_architecture="windows-amd64")


def test_agent_release_store_is_atomic_persistent_and_unioned(tmp_path, monkeypatch):
    from agent import package_manifest

    repo = tmp_path / "repo"
    package = repo / "agent" / "dist" / "mdd-agent-windows-amd64"
    package.mkdir(parents=True)
    (package / "mdd-agent.exe").write_bytes(b"agent")
    digest = package_manifest.write_package_metadata(
        package, architecture="windows-amd64")
    explicit = "a" * 64
    data = tmp_path / "data"

    assert package_manifest.collect_release_allowlist(
        repo, data, raw_digests=explicit) == sorted([explicit, digest])
    stored = data / "agent-releases" / "windows-amd64" / digest
    assert package_manifest.verify_package_manifest(
        stored / "manifest.json", expect_digest=digest,
        expect_architecture="windows-amd64") == digest

    # The persistent release remains trusted after a source refresh removes agent/dist.
    import shutil
    shutil.rmtree(repo / "agent" / "dist")
    assert package_manifest.collect_release_allowlist(repo, data) == [digest]

    # Interrupted publication never exposes a digest-shaped partial directory.
    package.mkdir(parents=True)
    (package / "mdd-agent.exe").write_bytes(b"new-agent")
    new_digest = package_manifest.write_package_metadata(
        package, architecture="windows-amd64")
    real_rename = package_manifest.os.rename

    def interrupted(source, destination):
        if str(source).find(".staging-") >= 0:
            raise OSError("simulated interruption")
        return real_rename(source, destination)

    monkeypatch.setattr(package_manifest.os, "rename", interrupted)
    with pytest.raises(OSError, match="simulated interruption"):
        package_manifest.collect_release_allowlist(repo, data)
    architecture_root = data / "agent-releases" / "windows-amd64"
    assert not (architecture_root / new_digest).exists()
    assert not any(path.name.startswith(".staging-") for path in architecture_root.iterdir())


def test_agent_release_store_rejects_links_partial_artifacts_and_conflicts(tmp_path):
    from agent import package_manifest

    repo = tmp_path / "repo"
    partial = repo / "agent" / "dist" / "mdd-agent-windows-amd64"
    partial.mkdir(parents=True)
    data = tmp_path / "data"
    with pytest.raises(package_manifest.PackageManifestError, match="cannot read"):
        package_manifest.collect_release_allowlist(repo, data)

    import shutil
    shutil.rmtree(repo / "agent" / "dist")
    linked_target = tmp_path / "linked-target"
    linked_target.mkdir()
    release_root = data / "agent-releases"
    shutil.rmtree(release_root)
    release_root.symlink_to(linked_target, target_is_directory=True)
    with pytest.raises(package_manifest.PackageManifestError, match="symlink"):
        package_manifest.collect_release_allowlist(repo, data)

    release_root.unlink()
    package = repo / "agent" / "dist" / "mdd-agent-windows-amd64"
    package.mkdir(parents=True)
    (package / "mdd-agent.exe").write_bytes(b"agent")
    digest = package_manifest.write_package_metadata(
        package, architecture="windows-amd64")
    conflict = data / "agent-releases" / "windows-amd64" / digest
    conflict.mkdir(parents=True)
    (conflict / "manifest.json").write_text("{}", encoding="utf-8")
    with pytest.raises(package_manifest.PackageManifestError):
        package_manifest.collect_release_allowlist(repo, data)
    assert (conflict / "manifest.json").read_text(encoding="utf-8") == "{}"


def test_agent_release_store_requires_matching_anchor_and_skips_marked_unsigned(tmp_path):
    from agent import package_manifest

    repo = tmp_path / "repo"
    windows = repo / "agent" / "dist" / "mdd-agent-windows-amd64"
    windows.mkdir(parents=True)
    (windows / "mdd-agent.exe").write_bytes(b"agent")
    package_manifest.write_package_metadata(windows, architecture="windows-amd64")
    (windows / "control-agent-allowlist.env").write_text(
        f"MDD_ALLOWED_AGENT_PACKAGE_DIGESTS={'f' * 64}\n", encoding="utf-8")
    with pytest.raises(package_manifest.PackageManifestError, match="trust anchor"):
        package_manifest.collect_release_allowlist(repo, tmp_path / "data")

    (windows / "UNSIGNED_DEVELOPMENT_ARTIFACT").write_bytes(b"")
    package_manifest.write_package_metadata(
        windows, architecture="windows-amd64", emit_allowlist=False)
    with pytest.raises(package_manifest.PackageManifestError, match="trust anchor"):
        package_manifest.collect_release_allowlist(repo, tmp_path / "windows-marker-data")

    import shutil
    shutil.rmtree(repo / "agent" / "dist")
    mac = repo / "agent" / "dist" / "mdd-agent-macos-arm64"
    mac.mkdir(parents=True)
    (mac / "mdd-agent").write_bytes(b"agent")
    signed_digest = package_manifest.write_package_metadata(
        mac, architecture="macos-arm64")
    data = tmp_path / "unsigned-data"
    assert package_manifest.collect_release_allowlist(repo, data) == [signed_digest]

    shutil.rmtree(mac)
    mac.mkdir(parents=True)
    (mac / "mdd-agent").write_bytes(b"unsigned-agent")
    (mac / "UNSIGNED_DEVELOPMENT_ARTIFACT").write_bytes(b"")
    package_manifest.write_package_metadata(
        mac, architecture="macos-arm64", emit_allowlist=False)
    assert package_manifest.collect_release_allowlist(repo, data) == [signed_digest]
    stored = data / "agent-releases" / "macos-arm64"
    assert [path.name for path in stored.iterdir() if len(path.name) == 64] == [signed_digest]


def test_agent_release_store_reports_post_rename_fsync_failure_and_recovers(
        tmp_path, monkeypatch):
    from agent import package_manifest

    repo = tmp_path / "repo"
    data = tmp_path / "data"
    assert package_manifest.collect_release_allowlist(repo, data) == []
    package = repo / "agent" / "dist" / "mdd-agent-windows-amd64"
    package.mkdir(parents=True)
    (package / "mdd-agent.exe").write_bytes(b"agent")
    digest = package_manifest.write_package_metadata(
        package, architecture="windows-amd64")
    architecture_root = data / "agent-releases" / "windows-amd64"
    real_fsync_directory = package_manifest._fsync_directory

    def fail_after_publish(path):
        path = __import__("pathlib").Path(path)
        if path == architecture_root and (architecture_root / digest).is_dir():
            raise OSError("simulated directory fsync failure")
        return real_fsync_directory(path)

    monkeypatch.setattr(package_manifest, "_fsync_directory", fail_after_publish)
    with pytest.raises(OSError, match="directory fsync failure"):
        package_manifest.collect_release_allowlist(repo, data)

    # Rename already happened, so the complete package remains but the operation did not
    # claim success. A later invocation must fully verify and reuse it.
    stored = architecture_root / digest
    assert package_manifest.verify_package_manifest(
        stored / "manifest.json", expect_digest=digest,
        expect_architecture="windows-amd64") == digest
    retried_fsync = []

    def record_retry(path):
        retried_fsync.append(__import__("pathlib").Path(path))
        return real_fsync_directory(path)

    monkeypatch.setattr(package_manifest, "_fsync_directory", record_retry)
    assert package_manifest.collect_release_allowlist(repo, data) == [digest]
    assert architecture_root in retried_fsync


def test_macos_package_assembly_generates_manifest_and_control_allowlist():
    script = (__import__("pathlib").Path(__file__).parents[1] /
              "agent" / "macos" / "Build-MacOS-Package.sh").read_text(encoding="utf-8")
    assert "MDD_CELLULAR_IO_BINARY" in script
    assert "MDD_CALL_AUDIO_BINARY" in script
    assert "MDD_PYTHON" in script
    assert "--pyinstaller-workpath" in script
    assert "--overwrite" in script
    assert "package output already exists" in script
    assert "refusing to overwrite unsafe package output path" in script
    assert "--target-arch arm64" in script
    assert "mdd-agent-macos-arm64" in script
    assert "package_manifest.py" in script
    assert "control-agent-allowlist.env" in script
    assert "Contents/Resources/manifest.json" not in script
    assert "--no-allowlist" in script
    assert "--verify" in script
    assert "unsigned development artifact: no Control allowlist generated" in script
    assert "--dist-dir" in script


def test_macos_release_build_script_has_release_safety_gates():
    script = (__import__("pathlib").Path(__file__).parents[1] /
              "agent" / "macos" / "Build-MacOS-Release.sh").read_text(encoding="utf-8")
    assert "--build-root is required" in script
    assert "--output-dir is required" in script
    assert "must not use a macOS system temporary directory" in script
    assert "must be outside the Git worktree" in script
    assert "--output-dir already exists" in script
    assert "brew install" not in script
    assert "wheelhouse-manifest-sha256" in script
    assert "--no-index --find-links" in script
    assert "sha256 mismatch" in script
    assert "unsafe archive path" in script
    assert "unsafe archive link entry" in script
    assert "go mod verify" in script
    assert "GOARCH=arm64" in script
    assert "GOOS=darwin" in script
    assert "--target-arch arm64" in script
    assert "lipo -archs" in script
    assert "verify_macho_tree_arm64" in script
    assert "ctest --test-dir" in script
    assert "otool -L" in script
    assert "dynamic libusb" in script
    assert "codesign --verify --deep --strict" in script
    assert "--unsigned-development" in script
    assert "control-agent-allowlist.env" in script


def test_macos_package_assembly_script_executes_with_prebuilt_inputs(tmp_path):
    root = __import__("pathlib").Path(__file__).parents[1]
    script = root / "agent" / "macos" / "Build-MacOS-Package.sh"
    dist = tmp_path / "dist"
    output = tmp_path / "out"
    helper_dir = tmp_path / "helpers"
    (dist / "MDD Agent.app" / "Contents" / "MacOS").mkdir(parents=True)
    helper_dir.mkdir()
    for path, content in (
            (dist / "mdd-agent", "cli"),
            (dist / "MDD Agent.app" / "Contents" / "MacOS" / "mdd-agent-gui", "gui"),
            (helper_dir / "mdd-cellular-io", "cellular"),
            (helper_dir / "mdd-call-audio-helper", "audio")):
        path.write_text(content, encoding="utf-8")
        path.chmod(0o755)

    env = {
        **os.environ,
        "MDD_CELLULAR_IO_BINARY": str(helper_dir / "mdd-cellular-io"),
        "MDD_CALL_AUDIO_BINARY": str(helper_dir / "mdd-call-audio-helper"),
    }
    subprocess.run([
        "bash", str(script), "--skip-pyinstaller", "--dist-dir", str(dist),
        "--output-dir", str(output),
    ], cwd=root, env=env, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
       text=True)

    manifest = output / "manifest.json"
    runtime_manifest = output / "MDD Agent.app" / "Contents" / "Resources" / "manifest.json"
    digest = hashlib.sha256(manifest.read_bytes()).hexdigest()
    assert not runtime_manifest.exists()
    assert (output / "control-agent-allowlist.env").read_text(encoding="utf-8") == \
        f"MDD_ALLOWED_AGENT_PACKAGE_DIGESTS={digest}\n"
    names = {entry["name"] for entry in json.loads(manifest.read_text(encoding="utf-8"))["files"]}
    assert {"mdd-agent", "mdd-cellular-io", "mdd-call-audio-helper",
            "MDD Agent.app/Contents/MacOS/mdd-agent-gui"} <= names

    dev_output = tmp_path / "dev-out"
    subprocess.run([
        "bash", str(script), "--skip-pyinstaller", "--dist-dir", str(dist),
        "--output-dir", str(dev_output), "--unsigned-development",
    ], cwd=root, env=env, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
       text=True)
    assert (dev_output / "manifest.json").exists()
    assert (dev_output / "UNSIGNED_DEVELOPMENT_ARTIFACT").exists()
    assert not (dev_output / "control-agent-allowlist.env").exists()


def test_install_exports_agent_package_allowlist_to_control():
    script = (__import__("pathlib").Path(__file__).parents[1] /
              "install.sh").read_text(encoding="utf-8")
    assert "agent_package_allowlist_digests()" in script
    assert "--collect-release-allowlist" in script
    assert 'raw="${MDD_ALLOWED_AGENT_PACKAGE_DIGESTS:-},${MDD_ALLOWED_AGENT_PACKAGE_DIGEST:-}"' in script
    assert "Environment=MDD_ALLOWED_AGENT_PACKAGE_DIGESTS=$AGENT_PACKAGE_DIGESTS" in script
    assert '-e MDD_ALLOWED_AGENT_PACKAGE_DIGESTS="${AGENT_PACKAGE_DIGESTS}"' in script


def test_service_manager_forwards_supervised_legacy_migration_to_powershell():
    source = (__import__("pathlib").Path(__file__).parents[1] /
              "agent" / "service_manager.py").read_text(encoding="utf-8")
    assert "supervised_legacy_idle_migration" in source
    assert 'command.append("-AllowLegacyMaintenancePreflight")' in source


def test_windows_service_restart_treats_mutex_access_denied_as_still_owned():
    source = (__import__("pathlib").Path(__file__).parents[1] /
              "agent" / "service_manager.py").read_text(encoding="utf-8")
    assert "OpenMutexW" in source
    assert "error_access_denied = 5" in source
    assert "existed = True" in source
    start_block = source[source.index('if action in {"start", "restart"}:'):]
    assert start_block.index("state = current_state()") < start_block.index(
        "_wait_agent_lease_released()")
    assert start_block.index("if state == win32service.SERVICE_START_PENDING:") < \
        start_block.index("_wait_agent_lease_released()")
    assert "timeout=300" in source


def test_windows_specs_collect_pywin32_runtime_loaders():
    agent_dir = __import__("pathlib").Path(__file__).parents[1] / "agent"
    for spec_name in ("mdd-agent.spec", "mdd-agent-gui.spec"):
        source = (agent_dir / spec_name).read_text(encoding="utf-8")
        assert '"pywintypes"' in source
        assert '"pythoncom"' in source
        assert '"win32timezone"' in source


def test_windows_gui_package_has_brand_icon_and_native_tray():
    agent_dir = __import__("pathlib").Path(__file__).parents[1] / "agent"
    spec = (agent_dir / "mdd-agent-gui.spec").read_text(encoding="utf-8")
    tray = (agent_dir / "windows" / "tray.py").read_text(encoding="utf-8")
    gui_source = (agent_dir / "gui.py").read_text(encoding="utf-8")
    assert (agent_dir / "assets" / "mdd-agent.png").is_file()
    assert (agent_dir / "assets" / "mdd-agent.ico").is_file()
    assert 'icon="assets/mdd-agent.ico"' in spec
    assert '("assets/mdd-agent.png", "assets")' in spec
    assert '("assets/mdd-agent.ico", "assets")' in spec
    assert '"windows.tray"' in spec and '"win32gui"' in spec
    assert "Shell_NotifyIcon" in tray
    assert 'RegisterWindowMessage("TaskbarCreated")' in tray
    assert "退出 GUI（服务继续运行）" in tray
    assert 'root.protocol("WM_DELETE_WINDOW", self.hide_to_tray)' in gui_source
    assert "self.root.withdraw()" in gui_source
    assert "self.tray.stop()" in gui_source


def test_macos_packages_inspect_permission_and_gui_declares_microphone_usage():
    agent_dir = __import__("pathlib").Path(__file__).parents[1] / "agent"
    cli_spec = (agent_dir / "mdd-agent.spec").read_text(encoding="utf-8")
    gui_spec = (agent_dir / "mdd-agent-gui.spec").read_text(encoding="utf-8")
    assert '"AVFoundation"' in cli_spec
    assert '"AVFoundation"' in gui_spec
    assert '"NSMicrophoneUsageDescription"' in gui_spec


def test_macos_gui_is_a_single_agent_host_with_menu_bar_client_window():
    agent_dir = __import__("pathlib").Path(__file__).parents[1] / "agent"
    gui_source = (agent_dir / "gui.py").read_text(encoding="utf-8")
    tray_source = (agent_dir / "macos" / "tray.py").read_text(encoding="utf-8")
    window_source = (agent_dir / "macos" / "window.py").read_text(encoding="utf-8")
    entrypoint = (agent_dir / "mdd_agent_gui.py").read_text(encoding="utf-8")
    assert 'AgentHost(store, host_mode="gui")' in gui_source
    assert "MacAgentWindow(client)" in tray_source
    assert "subprocess.Popen" not in tray_source
    assert "windowShouldClose_" in window_source and "orderOut_" in window_source
    assert "client.call(\"status\"" in tray_source
    assert "host.stop()" in tray_source
    assert "HostConflictError" in entrypoint and "EXIT_CONFLICT" in entrypoint
    assert "token-fifo" not in entrypoint and "token-stdin" not in entrypoint
    assert "_mac_microphone_permission(request=True" not in gui_source
    assert "check_microphone_on_startup" in tray_source
    assert "requestAccessForMediaType_completionHandler_" in tray_source
    assert 'client.call("audio.reprobe"' in tray_source


def test_gui_entrypoint_uses_saved_config_without_token_transport(monkeypatch, tmp_path):
    from agent import mdd_agent_gui

    store = ConfigStore(tmp_path, keychain=False)
    store.save({"token": "saved-only"})
    observed = {}
    monkeypatch.setattr(mdd_agent_gui, "run_gui", lambda **kwargs:
                        observed.update(kwargs) or 0)

    assert mdd_agent_gui.main([], store=store) == 0
    assert observed["store"].load(include_secrets=True)["token"] == "saved-only"
    assert not store.secret_path.exists()


def test_runtime_never_prompts_for_microphone_permission(tmp_path):
    from agent.managed_runtime import _args_from_config

    store = ConfigStore(tmp_path, keychain=False)
    config = store.load(include_secrets=False)
    config["server"] = "gateway.example.com:8443"
    assert _args_from_config(config, host_mode="gui").allow_audio_permission_prompt is False
    assert _args_from_config(config, host_mode="cli").allow_audio_permission_prompt is False
    assert _args_from_config(config, host_mode="service").allow_audio_permission_prompt is False


def test_runtime_package_digest_is_verified_once_per_process(monkeypatch, tmp_path):
    from agent import managed_runtime

    calls = []
    monkeypatch.setattr(
        managed_runtime, "_installed_runtime_package_digest",
        lambda: calls.append("verified") or "c" * 64)
    runtime = managed_runtime.ManagedAgentRuntime(ConfigStore(tmp_path, keychain=False))

    assert runtime.snapshot()["package_digest"] == "c" * 64
    assert runtime.snapshot()["package_digest"] == "c" * 64
    assert calls == ["verified"]


def test_audio_reprobe_is_narrow_and_does_not_restart_runtime(tmp_path):
    events = []

    class Modem:
        hardware_id = "modem-one"
        imei = ""
        port_name = "usb:test"

        def reprobe_call_audio(self):
            events.append("audio.reprobe")
            return {"ready": True, "backend": "uac"}

    runtime = ManagedAgentRuntime(ConfigStore(tmp_path, keychain=False))
    runtime._state = "online"
    runtime._modems = {"one": Modem()}
    result = runtime.execute("audio.reprobe", {}, "admin")
    assert result["ready"] is True
    assert events == ["audio.reprobe"]


def test_windows_service_does_not_require_event_log_service():
    source = (__import__("pathlib").Path(__file__).parents[1] /
              "agent" / "windows" / "service_host.py").read_text(encoding="utf-8")
    assert "def _service_log" in source
    assert "getattr(servicemanager, method)" in source
    assert "servicemanager.LogInfoMsg(" not in source
    assert "servicemanager.LogErrorMsg(" not in source


def test_doctor_does_not_count_pcsc_error_as_reader(tmp_path, monkeypatch):
    store = ConfigStore(tmp_path, keychain=False)
    store.save({"server": "gateway.example:8443", "token": "secret"})
    runtime = ManagedAgentRuntime(store)
    runtime._state = "online"
    monkeypatch.setattr(runtime, "devices", lambda: {
        "serial_ports": [{"device": "COM34"}],
        "pcsc_readers": [{"error": "resource manager is stopped"}],
    })

    result = runtime.doctor()
    assert result["healthy"] is True
    assert result["checks"][-1]["detail"] == "1 serial port(s), 0 PC/SC reader(s)"
