import json
import sys
import threading
import time
import types
from unittest.mock import patch

import pytest

sys.modules.setdefault("websocket", types.SimpleNamespace())
from agent.health_reporter import AgentHealthReporter, semantic_fingerprint
from control.app.agent_health_registry import AgentHealthRegistry, validate_snapshot


def health_snapshot(overall="healthy", runtime="online"):
    return {
        "support": "supported",
        "overall": overall,
        "runtime": {"state": runtime, "last_error_code": ""},
        "manager": {"kind": "cli", "host_mode": "cli", "autostart": False,
                    "session_scope": "user"},
        "config": {"state": "ok", "token_configured": True},
        "isolation": {"state": "ok", "backend": "private-userspace",
                      "reason_code": ""},
        "inventory": {"modems_total": 1, "modems_connected": 1},
        "resources": {"storage": {"state": "ok", "used_percent": 20,
                                    "free_mb": 1024}},
        "started_at": 123.0,
    }


def health_snapshot_v2(*, runtime="online", discovery="ok", readers=None):
    value = health_snapshot(runtime=runtime)
    value["inventory"]["pcsc"] = {
        "version": 2,
        "discovery": discovery,
        "generation": 3,
        "readers": list(readers or [{
            "reader_id": "reader-a", "name": "USB Reader A", "card_present": True,
        }]),
    }
    return value


def hello_v2(**kwargs):
    value = hello(snapshot=health_snapshot_v2(), **kwargs)
    value["meta"] = dict(value["meta"], collector="native-v2")
    return value


def hello(snapshot=None, agent_id="agent-a", run_id="run-a", revision=1):
    return {
        "version": 1, "type": "agent.health.hello", "agent_id": agent_id,
        "run_id": run_id, "session_id": "", "seq": 1, "revision": revision,
        "meta": {"platform": "macos", "arch": "arm64", "agent_version": "1.0",
                 "manager": "user-process", "collector": "native-v1",
                 "support": "supported"},
        "snapshot": snapshot or health_snapshot(),
    }


def frame(attachment, kind, *, sequence, revision, snapshot=None):
    value = {
        "version": 1, "type": kind, "agent_id": attachment.agent_id,
        "run_id": attachment.run_id, "session_id": attachment.session_id,
        "seq": sequence, "revision": revision,
    }
    if kind != "agent.health.heartbeat":
        value.update({"meta": attachment.meta, "snapshot": snapshot or attachment.snapshot})
    return value


class ServerWebSocket:
    def __init__(self):
        self.closed = []

    async def close(self, **kwargs):
        self.closed.append(kwargs)


@pytest.mark.asyncio
async def test_heartbeat_updates_memory_without_persisting_or_semantic_change(tmp_path):
    with patch("control.app.agent_health_registry.cfg.DATA_DIR", str(tmp_path)):
        registry = AgentHealthRegistry()
        websocket = ServerWebSocket()
        attachment = await registry.attach(hello(), websocket)
        with patch.object(registry, "_safe_persist") as persist:
            before = attachment.seen_at
            for sequence in range(2, 102):
                with patch("control.app.agent_health_registry.time.time",
                           return_value=before + sequence * 10):
                    changed = await registry.receive(
                        attachment, frame(attachment, "agent.health.heartbeat",
                                          sequence=sequence, revision=1))
                assert changed is False
            assert attachment.seen_at == before + 1010
            persist.assert_not_called()


@pytest.mark.asyncio
async def test_receive_result_distinguishes_accepted_heartbeat_from_fenced_frame(tmp_path):
    with patch("control.app.agent_health_registry.cfg.DATA_DIR", str(tmp_path)):
        registry = AgentHealthRegistry()
        first = await registry.attach(hello(), ServerWebSocket())
        accepted, changed = await registry.receive_result(
            first, frame(first, "agent.health.heartbeat", sequence=2, revision=1))
        assert (accepted, changed) == (True, False)

        await registry.attach(hello(run_id="run-b"), ServerWebSocket())
        accepted, changed = await registry.receive_result(
            first, frame(first, "agent.health.heartbeat", sequence=3, revision=1))
        assert (accepted, changed) == (False, False)


@pytest.mark.asyncio
async def test_reader_authority_requires_v2_current_open_same_run_and_ready_runtime(tmp_path):
    with patch("control.app.agent_health_registry.cfg.DATA_DIR", str(tmp_path)):
        registry = AgentHealthRegistry()
        legacy = await registry.attach(hello(), ServerWebSocket())
        assert await registry.reader_authority("agent-a", "run-a") is None
        await registry.shutdown(legacy)

        current = await registry.attach(hello_v2(run_id="run-v2"), ServerWebSocket())
        authority = await registry.reader_authority("agent-a", "run-v2")
        assert authority["run_id"] == "run-v2"
        assert authority["pcsc"]["readers"][0]["reader_id"] == "reader-a"
        assert await registry.reader_authority("agent-a", "other-run") is None

        await registry.transport_closed(current)
        # Public freshness can still be inside its grace window, but destructive authority is
        # revoked immediately when the actual WebSocket has closed.
        assert registry.list()[0]["connection"] == "fresh"
        assert registry.list()[0]["transport_open"] is False
        assert await registry.reader_authority("agent-a", "run-v2") is None


@pytest.mark.asyncio
async def test_reader_authority_rejects_stopping_and_discovery_error(tmp_path):
    with patch("control.app.agent_health_registry.cfg.DATA_DIR", str(tmp_path)):
        registry = AgentHealthRegistry()
        stopping = hello_v2(run_id="stopping")
        stopping["snapshot"] = health_snapshot_v2(runtime="stopping")
        await registry.attach(stopping, ServerWebSocket())
        assert await registry.reader_authority("agent-a", "stopping") is None
        failed = hello_v2(run_id="discovery-error")
        failed["snapshot"] = health_snapshot_v2(discovery="error")
        await registry.attach(failed, ServerWebSocket())
        assert await registry.reader_authority("agent-a", "discovery-error") is None


@pytest.mark.asyncio
async def test_semantic_status_persists_once_and_requires_new_revision(tmp_path):
    with patch("control.app.agent_health_registry.cfg.DATA_DIR", str(tmp_path)):
        registry = AgentHealthRegistry()
        attachment = await registry.attach(hello(), ServerWebSocket())
        changed_snapshot = health_snapshot("degraded")
        with patch.object(registry, "_safe_persist") as persist:
            assert await registry.receive(
                attachment, frame(attachment, "agent.health.status", sequence=2,
                                  revision=2, snapshot=changed_snapshot)) is True
            persist.assert_called_once()
            with pytest.raises(ValueError, match="one new semantic revision"):
                await registry.receive(
                    attachment, frame(attachment, "agent.health.status", sequence=3,
                                      revision=2, snapshot=changed_snapshot))
            with pytest.raises(ValueError, match="one new semantic revision"):
                await registry.receive(
                    attachment, frame(attachment, "agent.health.status", sequence=4,
                                      revision=2,
                                      snapshot=health_snapshot("failed", "failed")))
            with pytest.raises(ValueError, match="does not match"):
                await registry.receive(
                    attachment, frame(attachment, "agent.health.heartbeat", sequence=5,
                                      revision=1))


@pytest.mark.asyncio
async def test_persisted_active_health_stays_online_but_loads_offline(tmp_path):
    with patch("control.app.agent_health_registry.cfg.DATA_DIR", str(tmp_path)):
        registry = AgentHealthRegistry()
        attachment = await registry.attach(hello(), ServerWebSocket())
        changed_snapshot = health_snapshot("degraded")
        assert await registry.receive(
            attachment, frame(attachment, "agent.health.status", sequence=2,
                              revision=2, snapshot=changed_snapshot)) is True

        document = json.loads(
            (tmp_path / "orchestrator" / "agent-health.json").read_text(
                encoding="utf-8"))
        record = document["agents"]["agent-a"]
        assert record["online"] is True
        assert record["connection"] == "fresh"
        assert "session_id" not in record
        assert record["meta"] == attachment.meta
        assert record["snapshot"] == changed_snapshot

        loaded = AgentHealthRegistry()
        loaded_record = loaded.list()[0]
        assert loaded_record["online"] is False
        assert loaded_record["connection"] == "offline"
        assert "session_id" not in loaded_record
        assert loaded_record["snapshot"] == changed_snapshot


@pytest.mark.asyncio
async def test_new_session_fences_old_messages_and_detach(tmp_path):
    with patch("control.app.agent_health_registry.cfg.DATA_DIR", str(tmp_path)):
        registry = AgentHealthRegistry()
        first_ws, second_ws = ServerWebSocket(), ServerWebSocket()
        first = await registry.attach(hello(), first_ws)
        second = await registry.attach(hello(run_id="run-b", revision=1), second_ws)
        assert first_ws.closed
        assert await registry.receive(
            first, frame(first, "agent.health.status", sequence=2, revision=2,
                         snapshot=health_snapshot("failed", "failed"))) is False
        await registry.transport_closed(first)
        current = registry.list()[0]
        assert current["session_id"] == second.session_id
        assert current["snapshot"]["overall"] == "healthy"


@pytest.mark.asyncio
async def test_freshness_transitions_are_bounded_and_persist_only_offline(tmp_path):
    with patch("control.app.agent_health_registry.cfg.DATA_DIR", str(tmp_path)):
        registry = AgentHealthRegistry()
        attachment = await registry.attach(hello(), ServerWebSocket())
        start = attachment.monotonic_seen_at
        with patch.object(registry, "_safe_persist") as persist:
            with patch("control.app.agent_health_registry.time.monotonic",
                       return_value=start + 26):
                changes = await registry.sweep()
            assert [item["connection"] for item in changes] == ["delayed"]
            persist.assert_not_called()
            with patch("control.app.agent_health_registry.time.monotonic",
                       return_value=start + 41):
                changes = await registry.sweep()
            assert [item["connection"] for item in changes] == ["offline"]
            persist.assert_called_once()
            assert attachment.websocket.closed
            # A frame from the fenced 40-45s socket cannot revive it; the forced close makes
            # the reporter create a fresh hello/session, which becomes active normally.
            assert await registry.receive(
                attachment, frame(attachment, "agent.health.heartbeat",
                                  sequence=2, revision=1)) is False
            replacement = await registry.attach(hello(run_id="run-reconnected"),
                                                ServerWebSocket())
            assert registry.list()[0]["session_id"] == replacement.session_id
            assert registry.list()[0]["connection"] == "fresh"


def test_snapshot_schema_rejects_sensitive_or_unbounded_fields():
    value = health_snapshot()
    value["hostname"] = "private-host"
    with pytest.raises(ValueError, match="unsupported fields"):
        validate_snapshot(value)


@pytest.mark.asyncio
async def test_protocol_rejects_unknown_envelope_and_inconsistent_support(tmp_path):
    with patch("control.app.agent_health_registry.cfg.DATA_DIR", str(tmp_path)):
        registry = AgentHealthRegistry()
        bad = hello()
        bad["hostname"] = "private-host"
        with pytest.raises(ValueError, match="invalid envelope"):
            await registry.attach(bad, ServerWebSocket())
        inconsistent = hello()
        inconsistent["meta"] = dict(inconsistent["meta"], support="unsupported",
                                    collector="unsupported")
        with pytest.raises(ValueError, match="native|inconsistent"):
            await registry.attach(inconsistent, ServerWebSocket())
        boolean_version = hello()
        boolean_version["version"] = True
        with pytest.raises(ValueError, match="protocol v1"):
            await registry.attach(boolean_version, ServerWebSocket())


@pytest.mark.asyncio
async def test_fresh_reconnect_with_same_semantics_does_not_persist_or_announce(tmp_path):
    with patch("control.app.agent_health_registry.cfg.DATA_DIR", str(tmp_path)):
        registry = AgentHealthRegistry()
        await registry.attach(hello(), ServerWebSocket())
        with patch.object(registry, "_safe_persist") as persist:
            replacement = await registry.attach(hello(run_id="run-b"), ServerWebSocket())
        assert replacement.announce_attach is False
        persist.assert_not_called()


@pytest.mark.asyncio
async def test_shutdown_revision_is_strict_for_changed_and_unchanged_snapshot(tmp_path):
    with patch("control.app.agent_health_registry.cfg.DATA_DIR", str(tmp_path)):
        registry = AgentHealthRegistry()
        unchanged = await registry.attach(hello(), ServerWebSocket())
        assert await registry.receive(
            unchanged, frame(unchanged, "agent.health.shutdown", sequence=2,
                             revision=1)) is False
        changed = await registry.attach(hello(run_id="run-b"), ServerWebSocket())
        assert await registry.receive(
            changed, frame(changed, "agent.health.shutdown", sequence=2, revision=2,
                           snapshot=health_snapshot("degraded"))) is True
        invalid = await registry.attach(hello(run_id="run-c"), ServerWebSocket())
        with pytest.raises(ValueError, match="retain"):
            await registry.receive(
                invalid, frame(invalid, "agent.health.shutdown", sequence=2,
                               revision=2))


@pytest.mark.asyncio
async def test_wall_clock_jump_cannot_expire_a_fresh_session(tmp_path):
    with patch("control.app.agent_health_registry.cfg.DATA_DIR", str(tmp_path)):
        registry = AgentHealthRegistry()
        attachment = await registry.attach(hello(), ServerWebSocket())
        with patch("control.app.agent_health_registry.time.time", return_value=10**12), \
             patch("control.app.agent_health_registry.time.monotonic",
                   return_value=attachment.monotonic_seen_at + 10):
            assert await registry.sweep() == []
            assert registry.list()[0]["connection"] == "fresh"


@pytest.mark.asyncio
async def test_stale_sequence_does_not_refresh_seen_at(tmp_path):
    with patch("control.app.agent_health_registry.cfg.DATA_DIR", str(tmp_path)):
        registry = AgentHealthRegistry()
        attachment = await registry.attach(hello(), ServerWebSocket())
        before = attachment.seen_at
        message = frame(attachment, "agent.health.heartbeat", sequence=1, revision=1)
        with patch("control.app.agent_health_registry.time.time", return_value=before + 10):
            assert await registry.receive(attachment, message) is False
        assert attachment.seen_at == before


@pytest.mark.asyncio
async def test_persistence_failure_is_diagnostic_only(tmp_path):
    with patch("control.app.agent_health_registry.cfg.DATA_DIR", str(tmp_path)):
        registry = AgentHealthRegistry()
        with patch.object(registry, "_persist", side_effect=OSError("disk full")):
            attachment = await registry.attach(hello(), ServerWebSocket())
            assert attachment.agent_id == "agent-a"


def test_semantic_fingerprint_is_stable_for_key_order():
    first = semantic_fingerprint({"b": 2, "a": 1}, {"y": 2, "x": 1})
    second = semantic_fingerprint({"a": 1, "b": 2}, {"x": 1, "y": 2})
    assert first == second


def test_schema_rejects_coerced_numbers_nonfinite_time_and_impossible_counts():
    from control.app.agent_health_registry import validate_meta
    value = health_snapshot()
    value["inventory"]["modems_total"] = "1"
    with pytest.raises(ValueError, match="integer"):
        validate_snapshot(value)
    value = health_snapshot()
    value["started_at"] = float("nan")
    with pytest.raises(ValueError, match="number"):
        validate_snapshot(value)
    value = health_snapshot()
    value["inventory"] = {"modems_total": 0, "modems_connected": 1}
    with pytest.raises(ValueError, match="exceeds"):
        validate_snapshot(value)
    with pytest.raises(ValueError, match="native"):
        validate_meta({"platform": "macos", "arch": "arm64", "agent_version": "1",
                       "manager": "user-process", "collector": "unsupported",
                       "support": "supported"})


def test_health_schema_accepts_optional_modem_mode_and_rejects_coercion():
    legacy = validate_snapshot(health_snapshot())
    assert "modem_enabled" not in legacy["config"]
    current = health_snapshot()
    current["config"]["modem_enabled"] = False
    current["inventory"] = {"modems_total": 0, "modems_connected": 0}
    current["isolation"] = {"state": "ok", "backend": "not-applicable",
                            "reason_code": ""}
    assert validate_snapshot(current)["config"]["modem_enabled"] is False
    current["config"]["modem_enabled"] = "false"
    with pytest.raises(ValueError, match="must be boolean"):
        validate_snapshot(current)


def test_health_connect_uses_pinned_transport_and_never_puts_token_in_url():
    reporter = AgentHealthReporter(
        config={"server": "gateway.example:8443", "token": "CANARY", "pin": "AB"},
        agent_id="agent-a", snapshot_provider=health_snapshot)
    transport = ClientWebSocket()
    transport.settimeout = lambda value: setattr(transport, "timeout", value)
    with patch("agent.health_reporter.connect_wss", return_value=transport) as connect:
        assert reporter._connect() is transport
    connect.assert_called_once_with(
        "gateway.example", 8443, "/mdd/api/agent/health/ws?receipt=1", token="CANARY",
        explicit_pin="AB", timeout=2.0)
    assert transport.timeout == 3


class ClientWebSocket:
    def __init__(self):
        self.messages = []
        self.closed = False
        self.lock = threading.Lock()

    def send(self, value):
        with self.lock:
            self.messages.append(json.loads(value))

    def recv(self):
        last = self.messages[-1] if self.messages else {}
        if last.get("type") == "agent.health.hello":
            return json.dumps({"version": 1, "type": "agent.health.ack",
                               "session_id": "session-a", "receipt": "required-v1"})
        return json.dumps({"version": 1, "type": "agent.health.received",
                           "session_id": "session-a", "seq": last.get("seq"),
                           "revision": last.get("revision")})

    def close(self):
        self.closed = True


def test_reporter_stop_interrupts_blocked_ack_without_retaining_host():
    class BlockingAck(ClientWebSocket):
        def __init__(self):
            super().__init__()
            self.receiving = threading.Event()
            self.aborted = threading.Event()

        def recv(self):
            self.receiving.set()
            self.aborted.wait(5)
            raise ConnectionError("aborted")

        def abort(self):
            self.aborted.set()

    websocket = BlockingAck()
    reporter = AgentHealthReporter(
        config={"server": "example:8443", "token": "secret"}, agent_id="agent-a",
        snapshot_provider=health_snapshot)
    reporter._connect = lambda: websocket
    reporter.start()
    assert websocket.receiving.wait(1)
    started = time.monotonic()
    assert reporter.stop(timeout=1)
    assert time.monotonic() - started < 0.8
    assert websocket.aborted.is_set()


def test_reporter_stop_interrupts_blocked_shutdown_send():
    class BlockingSend(ClientWebSocket):
        def __init__(self):
            super().__init__()
            self.aborted = threading.Event()
            self.shutdown_send = threading.Event()

        def send(self, value):
            if self.messages:
                self.shutdown_send.set()
                self.aborted.wait(5)
                raise ConnectionError("aborted")
            super().send(value)

        def abort(self):
            self.aborted.set()

    websocket = BlockingSend()
    reporter = AgentHealthReporter(
        config={"server": "example:8443", "token": "secret"}, agent_id="agent-a",
        snapshot_provider=health_snapshot)
    reporter._connect = lambda: websocket
    reporter.start()
    deadline = time.monotonic() + 1
    while not websocket.messages and time.monotonic() < deadline:
        time.sleep(0.005)
    started = time.monotonic()
    assert reporter.stop(timeout=1)
    assert time.monotonic() - started < 0.8
    assert websocket.shutdown_send.is_set()
    assert websocket.aborted.is_set()


def test_reporter_connect_phase_is_bounded_below_host_stop_window():
    entered = threading.Event()
    reporter = AgentHealthReporter(
        config={"server": "example:8443", "token": "secret"}, agent_id="agent-a",
        snapshot_provider=health_snapshot)

    def bounded_blackhole():
        entered.set()
        time.sleep(0.35)
        raise TimeoutError("connect timeout")

    reporter._connect = bounded_blackhole
    reporter.start()
    assert entered.wait(1)
    started = time.monotonic()
    assert reporter.stop(timeout=1)
    assert time.monotonic() - started < 0.8


def test_stopped_generation_cannot_attach_after_blocked_initial_snapshot_returns():
    entered = threading.Event()
    release = threading.Event()
    old_ws = ClientWebSocket()

    def blocked_snapshot():
        entered.set()
        release.wait(5)
        return health_snapshot()

    old = AgentHealthReporter(
        config={"server": "example:8443", "token": "secret"}, agent_id="agent-a",
        snapshot_provider=blocked_snapshot)
    old._connect = lambda: old_ws
    old.start()
    assert entered.wait(1)
    assert old.stop(timeout=0.05) is False

    new_ws = ClientWebSocket()
    new = AgentHealthReporter(
        config={"server": "example:8443", "token": "secret"}, agent_id="agent-a",
        snapshot_provider=health_snapshot)
    new._connect = lambda: new_ws
    new.start()
    deadline = time.monotonic() + 1
    while not new_ws.messages and time.monotonic() < deadline:
        time.sleep(0.005)
    assert new_ws.messages[0]["type"] == "agent.health.hello"

    release.set()
    assert old.stop(timeout=1)
    assert old_ws.messages == []
    assert new.stop(timeout=1)


def test_reporter_sends_heartbeats_only_when_semantics_are_unchanged():
    websocket = ClientWebSocket()
    snapshot = health_snapshot()
    reporter = AgentHealthReporter(
        config={"server": "example:8443", "token": "secret"}, agent_id="agent-a",
        snapshot_provider=lambda: snapshot)
    reporter._connect = lambda: websocket
    with patch("agent.health_reporter.HEARTBEAT_INTERVAL", 0.02):
        reporter.start()
        time.sleep(0.065)
        assert reporter.stop()
    types = [message["type"] for message in websocket.messages]
    assert types[0] == "agent.health.hello"
    assert "agent.health.heartbeat" in types
    assert "agent.health.status" not in types
    assert types[-1] == "agent.health.shutdown"


def test_reporter_does_not_wait_for_receipts_an_old_server_did_not_negotiate():
    class LegacyAck(ClientWebSocket):
        def __init__(self):
            super().__init__()
            self.receives = 0

        def recv(self):
            self.receives += 1
            return json.dumps({"version": 1, "type": "agent.health.ack",
                               "session_id": "session-a"})

    websocket = LegacyAck()
    reporter = AgentHealthReporter(
        config={"server": "example:8443", "token": "secret"}, agent_id="agent-a",
        snapshot_provider=health_snapshot)
    reporter._connect = lambda: websocket
    with patch("agent.health_reporter.HEARTBEAT_INTERVAL", 0.02):
        reporter.start()
        time.sleep(0.05)
        assert reporter.stop()
    assert websocket.receives == 1
    assert "agent.health.heartbeat" in [item["type"] for item in websocket.messages]


def test_reporter_rejects_a_mismatched_scheduled_frame_receipt():
    class WrongReceipt(ClientWebSocket):
        def recv(self):
            last = self.messages[-1] if self.messages else {}
            if last.get("type") == "agent.health.hello":
                return super().recv()
            return json.dumps({"version": 1, "type": "agent.health.received",
                               "session_id": "session-a", "seq": 999,
                               "revision": last.get("revision")})

    websocket = WrongReceipt()
    reporter = AgentHealthReporter(
        config={"server": "example:8443", "token": "secret"}, agent_id="agent-a",
        snapshot_provider=health_snapshot)
    with patch("agent.health_reporter.HEARTBEAT_INTERVAL", 0.02):
        with pytest.raises(RuntimeError, match="invalid frame receipt"):
            reporter._serve(websocket)


def test_reporter_wakes_for_one_semantic_status_change():
    websocket = ClientWebSocket()
    current = {"value": health_snapshot()}
    reporter = AgentHealthReporter(
        config={"server": "example:8443", "token": "secret"}, agent_id="agent-a",
        snapshot_provider=lambda: current["value"])
    reporter._connect = lambda: websocket
    with patch("agent.health_reporter.HEARTBEAT_INTERVAL", 0.03):
        reporter.start()
        time.sleep(0.02)
        current["value"] = health_snapshot("degraded")
        for _ in range(20):
            reporter.notify_changed()
        assert [message["type"] for message in websocket.messages] == [
            "agent.health.hello"]
        # Callback bursts are coalesced into the next fixed scheduled frame.
        time.sleep(0.025)
        assert reporter.stop()
    statuses = [message for message in websocket.messages
                if message["type"] == "agent.health.status"]
    assert len(statuses) == 1
    assert statuses[0]["snapshot"]["overall"] == "degraded"
