import asyncio
import importlib.util
import json
from pathlib import Path
from unittest.mock import patch

import pytest


ROOT = Path(__file__).parents[1]
SPEC = importlib.util.spec_from_file_location(
    "mdd_maintenance_proxy", ROOT / "host" / "mdd_maintenance_proxy.py")
proxy = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(proxy)


def manifest(phase="rollback_committed"):
    return {
        "version": 1,
        "txid": "deploy-20260823-0001",
        "phase": phase,
        "owner": {"id": "owner-process-1", "epoch": 1},
        "source_control": {
            "container_id": "a" * 64,
            "image_id": "sha256:" + "b" * 64,
            "started_at": "2026-08-22T15:44:45.000000000Z",
            "network_mode": "host",
        },
        "rollback_control": {
            "container_id": "a" * 64,
            "image_id": "sha256:" + "b" * 64,
            "started_at": "2026-08-22T16:00:00.000000000Z",
            "pid": 101, "restart_count": 0, "network_mode": "host",
            "create_spec_hash": "2" * 64,
        },
        "proxy": {
            "container_id": "c" * 64,
            "image_id": "sha256:" + "d" * 64,
        },
        "rollback_upstream": {
            "tls_host": "127.0.0.1", "tls_port": 18443,
            "plain_host": "127.0.0.1", "plain_port": 18000,
            "engine_peers": ["172.18.0.3", "172.18.0.4"],
        },
        "lines": [{
            "instance": "7", "source_container_id": "e" * 64,
            "source_image_id": "sha256:" + "f" * 64,
            "target_image_digest": "sha256:" + "1" * 64,
            "phase": "source_removed",
        }],
    }


@pytest.mark.parametrize("path", [
    "/api/vpcd/ws", "/mdd/api/vpcd/ws?agent=one",
    "/api/agent/modem/tunnel", "/mdd/api/agent/health/ws",
    "/api/agent/modem/ws", "/api/agent/modem/media",
])
def test_narrow_websocket_whitelist_accepts_exact_normalized_paths(path):
    headers = {"connection": "keep-alive, Upgrade", "upgrade": "websocket"}
    assert proxy.maintenance_allows("GET", path, headers, "10.0.0.2", set())


@pytest.mark.parametrize("path", [
    "/mdd/mdd/api/vpcd/ws", "/api//vpcd/ws", "/api/./vpcd/ws",
    "/api/x/../vpcd/ws", "/api/vpcd%2fws", "/api/vpcd\\ws",
    "//api/vpcd/ws", "https://host/api/vpcd/ws", "/api/vpcd/ws/",
    "/api/instances/1/calls", "/api/instances/1/messages",
])
def test_narrow_whitelist_rejects_ambiguous_or_paid_paths(path):
    headers = {"connection": "Upgrade", "upgrade": "websocket"}
    assert not proxy.maintenance_allows("GET", path, headers, "10.0.0.2", set())


def test_engine_callback_uses_real_peer_method_and_non_websocket():
    peers = {"172.18.0.3"}
    assert proxy.maintenance_allows(
        "POST", "/api/engine/event?drain=1", {}, "172.18.0.3", peers)
    assert not proxy.maintenance_allows(
        "POST", "/api/engine/event", {}, "172.18.0.9", peers)
    assert not proxy.maintenance_allows(
        "GET", "/api/engine/event", {}, "172.18.0.3", peers)
    assert not proxy.maintenance_allows(
        "POST", "/api/engine/event",
        {"connection": "upgrade", "upgrade": "websocket"}, "172.18.0.3", peers)


@pytest.mark.parametrize("path", [
    "/api/instances/7/hangup",
    "/mdd/api/instances/line-7/cellular-call/hangup",
    "/api/instances/7/cellular-call/call-123/release",
])
def test_narrow_whitelist_allows_only_exact_authenticated_termination_routes(path):
    assert proxy.maintenance_allows("POST", path, {}, "10.0.0.2", set())
    assert not proxy.maintenance_allows("GET", path, {}, "10.0.0.2", set())
    assert not proxy.maintenance_allows(
        "POST", path, {"connection": "upgrade", "upgrade": "websocket"},
        "10.0.0.2", set())


@pytest.mark.parametrize("path", [
    "/api/instances/7/cellular-call/call-123/answer",
    "/api/instances/7/cellular-call/call-123/commit",
    "/api/instances/7/cellular-call",
    "/api/instances/7/messages",
    "/api/instances/7/cellular-call/call-123/release/extra",
])
def test_narrow_whitelist_still_rejects_paid_or_ambiguous_mutations(path):
    assert not proxy.maintenance_allows("POST", path, {}, "10.0.0.2", set())


def test_manifest_schema_is_strict_and_normalizes_peers():
    checked = proxy.validate_manifest(manifest())
    assert checked["phase"] == "rollback_committed"
    bad = manifest()
    bad["unexpected"] = True
    with pytest.raises(proxy.ProxyStateError):
        proxy.validate_manifest(bad)
    bad = manifest()
    bad["rollback_upstream"]["engine_peers"].append("172.18.0.3")
    with pytest.raises(proxy.ProxyStateError, match="duplicate"):
        proxy.validate_manifest(bad)


def _grant_full(auth, value, epoch):
    proxy._atomic_json(auth.mode_path, {
        "version": 1, "txid": auth.txid,
        "container_id": auth.container_id, "image_id": auth.image_id,
        "process_boot_id": auth.process_boot_id,
        "host_boot_id": auth.host_boot_id,
        "supervisor_boot_id": "9" * 32, "lease_seq": 1, "epoch": epoch,
        "state": "full", "active_full": 0, "forwarding_full": 0,
        "manifest_digest": proxy._digest(value), "updated_at": 1,
    })


def test_each_process_restart_starts_deny_and_invalidates_old_full_boot(tmp_path):
    manifest_path = tmp_path / "control-upgrade.json"
    mode_path = tmp_path / "maintenance-proxy.json"
    value = manifest()
    manifest_path.write_text(json.dumps(value), encoding="utf-8")

    first = proxy.Authorization(
        manifest_path, mode_path, value["txid"],
        value["proxy"]["container_id"], value["proxy"]["image_id"])
    ready = tmp_path / "ready.json"
    denied = first.initialize_deny(ready)
    assert denied["state"] == "deny"
    _grant_full(first, value, denied["epoch"] + 1)
    epoch = first.begin_full()
    assert epoch == denied["epoch"] + 1
    first.finish_full(epoch)

    second = proxy.Authorization(
        manifest_path, mode_path, value["txid"],
        value["proxy"]["container_id"], value["proxy"]["image_id"])
    second_record = second.initialize_deny(tmp_path / "ready-2.json")
    assert second.process_boot_id != first.process_boot_id
    assert second_record["state"] == "deny"
    assert second_record["process_boot_id"] == second.process_boot_id
    assert second.begin_full() is None


def test_maintenance_manifest_can_supply_peers_without_opening_full(tmp_path):
    value = manifest("rollback_starting")
    auth = proxy.Authorization(
        tmp_path / "manifest", tmp_path / "mode", value["txid"],
        value["proxy"]["container_id"], value["proxy"]["image_id"])
    assert auth.admit_maintenance_manifest(value)
    assert auth.manifest["rollback_upstream"]["engine_peers"] == [
        "172.18.0.3", "172.18.0.4"]


def test_full_request_is_cas_and_never_opens_on_changed_manifest(tmp_path):
    path = tmp_path / "control-upgrade.json"
    mode = tmp_path / "mode.json"
    before = manifest()
    path.write_text(json.dumps(before), encoding="utf-8")
    auth = proxy.Authorization(
        path, mode, before["txid"], before["proxy"]["container_id"],
        before["proxy"]["image_id"])
    denied = auth.initialize_deny(tmp_path / "ready")
    _grant_full(auth, before, denied["epoch"] + 1)
    changed = manifest()
    changed["owner"]["epoch"] = 2
    path.write_text(json.dumps(changed), encoding="utf-8")
    assert auth.begin_full() is None


def test_revoking_waits_for_active_full_then_writes_deny_applied(tmp_path):
    path = tmp_path / "control-upgrade.json"
    mode = tmp_path / "mode.json"
    value = manifest()
    path.write_text(json.dumps(value), encoding="utf-8")
    auth = proxy.Authorization(
        path, mode, value["txid"], value["proxy"]["container_id"],
        value["proxy"]["image_id"])
    denied = auth.initialize_deny(tmp_path / "ready")
    _grant_full(auth, value, denied["epoch"] + 1)
    epoch = auth.begin_full()
    record = json.loads(mode.read_text())
    record["state"] = "revoking"
    proxy._atomic_json(mode, record)
    auth.observe_mode()
    assert auth.revoke_event.is_set()
    auth.finish_full(epoch)
    applied = json.loads(mode.read_text())
    assert applied["state"] == "deny_applied"
    assert applied["active_full"] == 0


def test_zero_active_revocation_is_acknowledged_by_monitor(tmp_path):
    path = tmp_path / "control-upgrade.json"
    mode = tmp_path / "mode.json"
    value = manifest()
    path.write_text(json.dumps(value), encoding="utf-8")
    auth = proxy.Authorization(
        path, mode, value["txid"], value["proxy"]["container_id"],
        value["proxy"]["image_id"])
    denied = auth.initialize_deny(tmp_path / "ready")
    _grant_full(auth, value, denied["epoch"] + 1)
    record = json.loads(mode.read_text())
    record["state"] = "revoking"
    proxy._atomic_json(mode, record)
    observed = auth.observe_mode()
    assert observed["state"] == "deny_applied"
    assert json.loads(mode.read_text())["state"] == "deny_applied"


def test_manifest_change_revokes_existing_full_tunnels(tmp_path):
    path = tmp_path / "control-upgrade.json"
    mode = tmp_path / "mode.json"
    value = manifest()
    path.write_text(json.dumps(value), encoding="utf-8")
    auth = proxy.Authorization(
        path, mode, value["txid"], value["proxy"]["container_id"],
        value["proxy"]["image_id"])
    denied = auth.initialize_deny(tmp_path / "ready")
    _grant_full(auth, value, denied["epoch"] + 1)
    auth.observe_mode()
    assert not auth.revoke_event.is_set()
    path.write_text("{damaged", encoding="utf-8")
    auth.observe_mode()
    assert auth.revoke_event.is_set()
    assert json.loads(mode.read_text())["state"] == "deny_applied"
    path.write_text(json.dumps(value), encoding="utf-8")
    assert auth.begin_full() is None


def test_corrupt_mode_cannot_be_restored_to_resurrect_same_boot_full(tmp_path):
    path = tmp_path / "control-upgrade.json"
    mode = tmp_path / "mode.json"
    value = manifest()
    path.write_text(json.dumps(value), encoding="utf-8")
    auth = proxy.Authorization(
        path, mode, value["txid"], value["proxy"]["container_id"],
        value["proxy"]["image_id"])
    denied = auth.initialize_deny(tmp_path / "ready")
    _grant_full(auth, value, denied["epoch"] + 1)
    valid_full = mode.read_text(encoding="utf-8")

    mode.write_text("{damaged", encoding="utf-8")
    assert auth.observe_mode() is None
    assert auth.authorization_lost is True
    mode.write_text(valid_full, encoding="utf-8")
    assert auth.begin_full() is None
    assert auth.commit_forward(denied["epoch"] + 1) is False
    assert auth.revoke_event.is_set()


def test_failed_durable_revocation_latches_authorization_loss(tmp_path):
    path = tmp_path / "control-upgrade.json"
    mode = tmp_path / "mode.json"
    value = manifest()
    path.write_text(json.dumps(value), encoding="utf-8")
    auth = proxy.Authorization(
        path, mode, value["txid"], value["proxy"]["container_id"],
        value["proxy"]["image_id"])
    denied = auth.initialize_deny(tmp_path / "ready")
    _grant_full(auth, value, denied["epoch"] + 1)
    path.write_text("{damaged", encoding="utf-8")
    with patch.object(proxy, "_atomic_json", side_effect=OSError("disk full")):
        assert auth.observe_mode() is None
    assert auth.authorization_lost is True
    path.write_text(json.dumps(value), encoding="utf-8")
    assert auth.begin_full() is None


def test_supervisor_lease_expiry_revokes_without_allowing_old_full_to_resume(tmp_path):
    path = tmp_path / "control-upgrade.json"
    mode = tmp_path / "mode.json"
    value = manifest()
    path.write_text(json.dumps(value), encoding="utf-8")
    auth = proxy.Authorization(
        path, mode, value["txid"], value["proxy"]["container_id"],
        value["proxy"]["image_id"], lease_timeout=0.25)
    denied = auth.initialize_deny(tmp_path / "ready")
    _grant_full(auth, value, denied["epoch"] + 1)
    with patch.object(proxy.time, "monotonic", side_effect=[10.0, 11.0]):
        assert auth.begin_full() == denied["epoch"] + 1
        auth.finish_full(denied["epoch"] + 1)
        observed = auth.observe_mode()
    assert observed["state"] == "deny_applied"
    assert auth.authorization_lost is False
    assert auth.begin_full() is None


def test_advancing_supervisor_lease_sequence_extends_the_same_epoch(tmp_path):
    path = tmp_path / "control-upgrade.json"
    mode = tmp_path / "mode.json"
    value = manifest()
    path.write_text(json.dumps(value), encoding="utf-8")
    auth = proxy.Authorization(
        path, mode, value["txid"], value["proxy"]["container_id"],
        value["proxy"]["image_id"], lease_timeout=0.25)
    denied = auth.initialize_deny(tmp_path / "ready")
    _grant_full(auth, value, denied["epoch"] + 1)
    with patch.object(proxy.time, "monotonic", side_effect=[10.0, 10.2, 10.4]):
        auth.observe_mode()
        record = json.loads(mode.read_text())
        record["lease_seq"] = 2
        proxy._atomic_json(mode, record)
        assert auth.observe_mode()["state"] == "full"
        assert auth.begin_full() == denied["epoch"] + 1
    auth.finish_full(denied["epoch"] + 1)


def test_unchanged_mode_does_not_rewrite_ready_on_each_monitor_poll(tmp_path):
    value = manifest()
    manifest_path = tmp_path / "manifest.json"
    mode_path = tmp_path / "mode.json"
    manifest_path.write_text(json.dumps(value), encoding="utf-8")
    auth = proxy.Authorization(
        manifest_path, mode_path, value["txid"], value["proxy"]["container_id"],
        value["proxy"]["image_id"])
    auth.initialize_deny(tmp_path / "ready.json")
    with patch.object(proxy, "_atomic_json", wraps=proxy._atomic_json) as write:
        assert auth.observe_mode()["state"] == "deny"
        assert auth.observe_mode()["state"] == "deny"
    assert write.call_count == 0


@pytest.mark.parametrize("data_path", ["begin", "recheck", "commit"])
def test_valid_changed_manifest_cannot_be_restored_to_resurrect_full(tmp_path, data_path):
    path = tmp_path / "control-upgrade.json"
    mode = tmp_path / "mode.json"
    value = manifest()
    path.write_text(json.dumps(value), encoding="utf-8")
    auth = proxy.Authorization(
        path, mode, value["txid"], value["proxy"]["container_id"],
        value["proxy"]["image_id"])
    denied = auth.initialize_deny(tmp_path / "ready")
    epoch = denied["epoch"] + 1
    _grant_full(auth, value, epoch)
    if data_path != "begin":
        assert auth.begin_full() == epoch

    changed = manifest()
    changed["owner"]["epoch"] = 2
    path.write_text(json.dumps(changed), encoding="utf-8")
    result = (auth.begin_full() if data_path == "begin" else
              auth.recheck_full(epoch) if data_path == "recheck" else
              auth.commit_forward(epoch))
    assert result in {None, False}
    assert auth.authorization_lost is True
    assert auth.revoke_event.is_set()

    path.write_text(json.dumps(value), encoding="utf-8")
    assert auth.begin_full() is None


def test_revoking_between_reservation_and_forward_commit_sends_nothing(tmp_path):
    path = tmp_path / "control-upgrade.json"
    mode = tmp_path / "mode.json"
    value = manifest()
    path.write_text(json.dumps(value), encoding="utf-8")
    auth = proxy.Authorization(
        path, mode, value["txid"], value["proxy"]["container_id"],
        value["proxy"]["image_id"])
    denied = auth.initialize_deny(tmp_path / "ready")
    _grant_full(auth, value, denied["epoch"] + 1)
    epoch = auth.begin_full()
    record = json.loads(mode.read_text())
    record["state"] = "revoking"
    proxy._atomic_json(mode, record)
    assert auth.commit_forward(epoch) is False
    auth.finish_full(epoch)
    assert json.loads(mode.read_text())["state"] == "deny_applied"


def test_self_facts_bind_full_inspect_id_to_this_container_hostname(tmp_path):
    path = tmp_path / "proxy-self.json"
    value = {
        "version": 1, "txid": "deploy-20260823-0001",
        "container_id": "a" * 64, "image_id": "sha256:" + "b" * 64,
    }
    path.write_text(json.dumps(value), encoding="utf-8")
    with patch.object(proxy.socket, "gethostname", return_value="a" * 12):
        assert proxy.read_self_facts(path, value["txid"]) == (
            value["container_id"], value["image_id"])
    with patch.object(proxy.socket, "gethostname", return_value="c" * 12), \
            pytest.raises(proxy.ProxyStateError, match="this container"):
        proxy.read_self_facts(path, value["txid"])

    cidfile = tmp_path / "proxy.cid"
    cidfile.write_text(value["container_id"] + "\n", encoding="ascii")
    with patch.object(proxy.socket, "gethostname", return_value="custom-hostname"):
        assert proxy.read_self_facts(path, value["txid"], cidfile) == (
            value["container_id"], value["image_id"])
    cidfile.write_text("c" * 64, encoding="ascii")
    with pytest.raises(proxy.ProxyStateError, match="container id file"):
        proxy.read_self_facts(path, value["txid"], cidfile)


def test_committed_full_chunked_response_has_one_absolute_drain_deadline(tmp_path):
    value = manifest()
    manifest_path = tmp_path / "manifest.json"
    manifest_path.write_text(json.dumps(value), encoding="utf-8")

    class Auth:
        def __init__(self):
            self.manifest_path = manifest_path
            self.revoke_event = asyncio.Event()
            self.finished = []

        def admit_maintenance_manifest(self, _value):
            return True

        def begin_full(self):
            return 2

        def commit_forward(self, epoch):
            return epoch == 2

        def finish_full(self, epoch, forwarding=False):
            self.finished.append((epoch, forwarding))

    async def scenario():
        async def upstream(reader, writer):
            try:
                await reader.readuntil(b"\r\n\r\n")
                writer.write(b"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n")
                await writer.drain()
                while True:
                    writer.write(b"1\r\nx\r\n")
                    await writer.drain()
                    await asyncio.sleep(0.01)
            except (OSError, asyncio.CancelledError):
                pass
            finally:
                writer.close()
                await writer.wait_closed()

        upstream_server = await asyncio.start_server(upstream, "127.0.0.1", 0)
        upstream_port = upstream_server.sockets[0].getsockname()[1]
        auth = Auth()
        app = proxy.MaintenanceProxy(auth, ("127.0.0.1", upstream_port),
                                     ("127.0.0.1", upstream_port))
        proxy_server = await asyncio.start_server(
            lambda reader, writer: app.handle(reader, writer, False), "127.0.0.1", 0)
        proxy_port = proxy_server.sockets[0].getsockname()[1]
        try:
            reader, writer = await asyncio.open_connection("127.0.0.1", proxy_port)
            writer.write(b"GET /api/instances/7/calls HTTP/1.1\r\nHost: local\r\n\r\n")
            await writer.drain()
            async def revoke():
                await asyncio.sleep(0.03)
                auth.revoke_event.set()
            asyncio.create_task(revoke())
            await asyncio.wait_for(reader.read(), 0.5)
            writer.close()
            await writer.wait_closed()
            assert auth.finished == [(2, True)]
        finally:
            proxy_server.close()
            upstream_server.close()
            await proxy_server.wait_closed()
            await upstream_server.wait_closed()

    with patch.object(proxy, "FULL_DRAIN_TIMEOUT", 0.05), \
            patch.object(proxy, "READ_TIMEOUT", 0.25):
        asyncio.run(scenario())


def test_healthy_full_websocket_has_no_fixed_lifetime(tmp_path):
    value = manifest()
    manifest_path = tmp_path / "manifest.json"
    manifest_path.write_text(json.dumps(value), encoding="utf-8")

    class Auth:
        def __init__(self):
            self.manifest_path = manifest_path
            self.revoke_event = asyncio.Event()
            self.finished = []

        def admit_maintenance_manifest(self, _value): return True
        def begin_full(self): return 2
        def commit_forward(self, _epoch): return True
        def finish_full(self, epoch, forwarding=False):
            self.finished.append((epoch, forwarding))

    async def scenario():
        async def upstream(reader, writer):
            try:
                await reader.readuntil(b"\r\n\r\n")
                writer.write(b"HTTP/1.1 101 Switching Protocols\r\n"
                             b"Connection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
                await writer.drain()
                await reader.read()
            finally:
                writer.close()
                await writer.wait_closed()

        upstream_server = await asyncio.start_server(upstream, "127.0.0.1", 0)
        upstream_port = upstream_server.sockets[0].getsockname()[1]
        auth = Auth()
        app = proxy.MaintenanceProxy(auth, ("127.0.0.1", upstream_port),
                                     ("127.0.0.1", upstream_port))
        proxy_server = await asyncio.start_server(
            lambda reader, writer: app.handle(reader, writer, False), "127.0.0.1", 0)
        proxy_port = proxy_server.sockets[0].getsockname()[1]
        try:
            reader, writer = await asyncio.open_connection("127.0.0.1", proxy_port)
            writer.write(b"GET /api/agent/modem/ws HTTP/1.1\r\nHost: local\r\n"
                         b"Connection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
            await writer.drain()
            await asyncio.wait_for(reader.readuntil(b"\r\n\r\n"), 0.2)
            await asyncio.sleep(0.1)
            assert auth.finished == []
            assert not reader.at_eof()
            auth.revoke_event.set()
            await asyncio.wait_for(reader.read(), 0.2)
            writer.close()
            await writer.wait_closed()
            assert auth.finished == [(2, True)]
        finally:
            proxy_server.close()
            upstream_server.close()
            await proxy_server.wait_closed()
            await upstream_server.wait_closed()

    with patch.object(proxy, "FULL_DRAIN_TIMEOUT", 0.05):
        asyncio.run(scenario())


@pytest.mark.parametrize(
    "method,status,length,body,expected_body_reads",
    [
        ("GET", 200, 2, b"ok", 1),
        ("GET", 200, 0, b"", 0),
        ("HEAD", 200, 2, b"", 0),
        ("GET", 204, 2, b"", 0),
    ],
)
def test_fixed_length_or_bodyless_keepalive_response_finishes_without_waiting_for_close(
        method, status, length, body, expected_body_reads):
    class Reader:
        def __init__(self):
            self.body_reads = 0

        async def readuntil(self, _separator):
            return (f"HTTP/1.1 {status} OK\r\nContent-Length: {length}\r\n"
                    "Connection: keep-alive\r\n\r\n").encode()

        async def readexactly(self, size):
            self.body_reads += 1
            assert size == length
            return body

        async def read(self, _size):
            raise AssertionError("fixed/bodyless response entered close-delimited reading")

    class Writer:
        def __init__(self):
            self.data = b""

        def write(self, value):
            self.data += value

        async def drain(self):
            return None

    async def scenario():
        upstream_reader = Reader()
        upstream_writer = Writer()
        client_writer = Writer()
        await proxy._forward_exchange(
            method=method, request_headers={}, raw=b"GET / HTTP/1.1\r\n\r\n",
            body=b"", client_reader=Reader(), client_writer=client_writer,
            upstream_reader=upstream_reader, upstream_writer=upstream_writer,
            revoke_event=None)
        assert upstream_reader.body_reads == expected_body_reads
        assert client_writer.data.endswith(body) if body else True

    asyncio.run(scenario())
