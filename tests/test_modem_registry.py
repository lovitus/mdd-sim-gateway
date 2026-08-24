import asyncio
import json
import os
import socket
import tempfile
import unittest
from unittest.mock import AsyncMock, patch

from control.app.modem_registry import (
    ModemConflict, ModemOperationRejected, ModemRegistry, ModemTimeout,
    ModemUnavailable, _UdpQueue,
    _read_socks_address, _reverse_listener_settings, _socks_address,
)


VALID_AGENT_PACKAGE_DIGEST = "a" * 64
OTHER_AGENT_PACKAGE_DIGEST = "b" * 64


def valid_call_contract(**overrides):
    value = {
        "version": 2,
        "audio_telemetry_version": 2,
        "package_digest": VALID_AGENT_PACKAGE_DIGEST,
    }
    value.update(overrides)
    return value


class FakeWebSocket:
    def __init__(self):
        self.sent = []

    async def send_json(self, value):
        self.sent.append(value)


class FailingWebSocket:
    async def send_json(self, _value):
        raise ConnectionResetError("frame outcome is unknown")


class BlockingWebSocket:
    async def send_json(self, _value):
        await asyncio.Event().wait()


class ModemRegistryTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.env = patch.dict(os.environ, {
            "MDD_ALLOWED_AGENT_PACKAGE_DIGESTS": VALID_AGENT_PACKAGE_DIGEST,
        })
        self.env.start()
        self.registry = ModemRegistry()
        self.persist = patch.object(self.registry, "_persist")
        self.persist.start()

    async def asyncTearDown(self):
        self.persist.stop()
        self.env.stop()

    async def test_rpc_resolves_by_iccid_and_rejects_stale_session(self):
        ws = FakeWebSocket()
        attachment = await self.registry.attach({
            "iccid": "89852312388530152529", "agent_id": "host-a", "modem_id": "m-a",
            "capabilities": {"sms": True}}, ws)
        task = asyncio.create_task(self.registry.rpc(attachment.iccid, "sms.list"))
        await asyncio.sleep(0)
        request = ws.sent[0]
        await self.registry.receive(attachment, {"type": "rpc.result", "id": request["id"],
                                                "ok": True, "result": {"messages": []}})
        self.assertEqual(await task, {"messages": []})
        with self.assertRaises(ModemConflict):
            await self.registry.attach({
                "iccid": attachment.iccid, "agent_id": "host-b", "modem_id": "m-b"},
                FakeWebSocket())
        await self.registry.detach(attachment)
        replacement = await self.registry.attach({
            "iccid": attachment.iccid, "agent_id": "host-b", "modem_id": "m-b"},
            FakeWebSocket())
        await self.registry.receive(attachment, {"type": "status", "status": {"bad": True}})
        self.assertIs(self.registry.resolve(attachment.iccid), replacement)
        self.assertNotIn("bad", replacement.status)

    async def test_rpc_send_failure_is_unknown_and_never_reported_definite(self):
        attachment = await self.registry.attach({
            "iccid": "89852312388530152529", "agent_id": "host-a", "modem_id": "m-a",
        }, FailingWebSocket())
        with self.assertRaisesRegex(ModemTimeout, "outcome is unknown"):
            await self.registry.rpc(attachment.iccid, "call.dial", {"to": "123"},
                                    timeout=0.2, operation_id="paid-operation")
        self.assertFalse(attachment.pending)

    async def test_rpc_timeout_bounds_send_and_response_as_one_budget(self):
        attachment = await self.registry.attach({
            "iccid": "89852312388530152529", "agent_id": "host-a", "modem_id": "m-a",
        }, BlockingWebSocket())
        started = asyncio.get_running_loop().time()
        with self.assertRaises(ModemTimeout):
            await self.registry.rpc(attachment.iccid, "call.status", timeout=0.03)
        self.assertLess(asyncio.get_running_loop().time() - started, 0.15)
        self.assertFalse(attachment.pending)

    async def test_explicit_agent_rejection_is_not_wrapped_as_unknown_transport(self):
        ws = FakeWebSocket()
        attachment = await self.registry.attach({
            "iccid": "89852312388530152529", "agent_id": "host-a", "modem_id": "m-a",
        }, ws)
        task = asyncio.create_task(self.registry.rpc(
            attachment.iccid, "call.dial", {"to": "123"},
            operation_id="paid-operation"))
        await asyncio.sleep(0)
        request = ws.sent[0]
        await self.registry.receive(attachment, {
            "type": "rpc.result", "id": request["id"], "ok": False,
            "error": "paid call lease is unavailable"})
        with self.assertRaisesRegex(ModemOperationRejected, "lease is unavailable"):
            await task

    async def test_rpc_received_paid_result_survives_local_persist_failure(self):
        ws = FakeWebSocket()
        attachment = await self.registry.attach({
            "iccid": "89852312388530152529", "agent_id": "host-a", "modem_id": "m-a",
        }, ws)
        task = asyncio.create_task(self.registry.rpc(
            attachment.iccid, "call.dial", {"to": "123"}, operation_id="paid-operation"))
        await asyncio.sleep(0)
        request = ws.sent[0]
        with patch.object(self.registry, "_persist", side_effect=OSError("disk full")):
            await self.registry.receive(attachment, {
                "type": "rpc.result", "id": request["id"], "ok": True,
                "result": {"ok": True, "status": "dialing"},
            })
            result = await task
        self.assertTrue(result["ok"])
        self.assertEqual(result["status"], "dialing")

    async def test_attach_preserves_provider_subscriber_identity(self):
        attachment = await self.registry.attach({
            "iccid": "89852312388530153089", "imsi": "455070885002578",
            "agent_id": "host-a", "modem_id": "modem-a"}, FakeWebSocket())

        self.assertEqual(attachment.imsi, "455070885002578")
        self.assertEqual(self.registry.list()[0]["imsi"], "455070885002578")

    async def test_status_atomically_refreshes_dynamic_audio_capability(self):
        attachment = await self.registry.attach({
            "iccid": "89852312388530153089", "agent_id": "host-a",
            "modem_id": "modem-a", "capabilities": {
                "call_control": True, "call_signalling": False, "call_audio": False,
                "paid_call_lease_version": 1,
                "call_contract": valid_call_contract(),
            }}, FakeWebSocket())

        changed = await self.registry.receive(attachment, {
            "type": "status", "capabilities": {
                "sms": True, "call_control": True, "call_signalling": True,
                "call_audio": True, "paid_call_lease_version": 1,
                "call_contract": valid_call_contract(),
                "cellular_data": True,
            }, "status": {"call_ready": True, "call_audio_ready": True},
        })

        self.assertTrue(attachment.capabilities["call_audio"])
        self.assertTrue(self.registry.list()[0]["capabilities"]["call_signalling"])
        self.assertTrue(changed)

    async def test_status_broadcast_hint_only_tracks_call_presentation(self):
        attachment = await self.registry.attach({
            "iccid": "89852312388530153089", "agent_id": "host-a",
            "modem_id": "modem-a", "phone": "+85212345678",
            "capabilities": {
                "call_control": True, "call_signalling": True, "call_audio": True,
                "paid_call_lease_version": 1,
                "call_contract": valid_call_contract(),
            }, "status": {
                "call_ready": True, "call_audio_ready": True,
                "voice_registration": {"ready": True},
                "signal": 10,
            }}, FakeWebSocket())

        unchanged = await self.registry.receive(attachment, {
            "type": "status", "status": {"signal": 20, "data_active": True},
        })
        changed = await self.registry.receive(attachment, {
            "type": "status", "status": {
                "call_audio_ready": False, "call_audio_error": "UAC probe failed"},
        })
        heartbeat = await self.registry.receive(attachment, {"type": "ping"})

        self.assertFalse(unchanged)
        self.assertTrue(changed)
        self.assertFalse(heartbeat)

    async def test_each_call_presentation_reason_wakes_consumers_once(self):
        attachment = await self.registry.attach({
            "iccid": "89852312388530153089", "agent_id": "host-a",
            "modem_id": "modem-a", "capabilities": {
                "call_control": True, "call_signalling": True, "call_audio": True,
                "paid_call_lease_version": 1,
                "call_contract": valid_call_contract(),
            }}, FakeWebSocket())

        changes = [
            {"call_ready": True},
            {"call_audio_ready": True},
            {"call_error": "registration unavailable"},
            {"call_audio_error": "UAC unavailable"},
            {"voice_registration": {"ready": False, "reason": "searching"}},
            {"uicc_health": {"ready": False, "reason": "SIM locked"}},
        ]
        for status in changes:
            with self.subTest(status=status):
                self.assertTrue(await self.registry.receive(
                    attachment, {"type": "status", "status": status}))
                self.assertFalse(await self.registry.receive(
                    attachment, {"type": "status", "status": status}))

    async def test_dynamic_capability_update_fails_closed_without_paid_call_lease(self):
        attachment = await self.registry.attach({
            "iccid": "89852312388530153089", "agent_id": "host-a",
            "modem_id": "modem-a", "capabilities": {
                "call_control": True, "call_signalling": False, "call_audio": False,
                "paid_call_lease_version": 1,
                "call_contract": valid_call_contract(),
            }}, FakeWebSocket())
        original = dict(attachment.capabilities)

        with self.assertRaisesRegex(ValueError, "paid-call safety lease"):
            await self.registry.receive(attachment, {
                "type": "status", "capabilities": {
                    "call_control": True, "call_signalling": True, "call_audio": True,
                }, "status": {"call_audio_ready": True},
            })

        self.assertEqual(attachment.capabilities, original)

    async def test_dynamic_capability_update_rejects_string_booleans(self):
        attachment = await self.registry.attach({
            "iccid": "89852312388530153089", "agent_id": "host-a",
            "modem_id": "modem-a", "capabilities": {
                "call_control": True, "call_signalling": False, "call_audio": False,
                "paid_call_lease_version": 1,
                "call_contract": valid_call_contract(),
            }}, FakeWebSocket())
        original = dict(attachment.capabilities)

        with self.assertRaisesRegex(ValueError, "must be boolean"):
            await self.registry.receive(attachment, {
                "type": "status", "capabilities": {
                    "call_control": True, "call_signalling": "false", "call_audio": "false",
                    "paid_call_lease_version": 1,
                }, "status": {"call_audio_ready": False},
            })

        self.assertEqual(attachment.capabilities, original)

    async def test_hello_rejects_string_booleans_and_old_voice_fails_closed(self):
        with self.assertRaisesRegex(ValueError, "must be boolean"):
            await self.registry.attach({
                "iccid": "89852312388530153089", "agent_id": "host-a",
                "modem_id": "modem-a", "capabilities": {
                    "call_control": True, "call_signalling": "false", "call_audio": "false",
                }}, FakeWebSocket())

        attachment = await self.registry.attach({
            "iccid": "89852312388530153089", "agent_id": "host-a",
            "modem_id": "modem-a", "capabilities": {
                "call_control": True, "call_signalling": True, "call_audio": True,
            }}, FakeWebSocket())
        self.assertTrue(attachment.capabilities["call_control"])
        self.assertFalse(attachment.capabilities["call_signalling"])
        self.assertFalse(attachment.capabilities["call_audio"])
        self.assertIn("too old", attachment.capabilities["call_contract_error"])

    async def test_old_call_audio_helper_fails_voice_closed_even_with_ready_flags(self):
        attachment = await self.registry.attach({
            "iccid": "89852312388530153089", "agent_id": "host-a",
            "modem_id": "modem-a", "capabilities": {
                "call_control": True, "call_signalling": True, "call_audio": True,
                "paid_call_lease_version": 1,
                "call_contract": valid_call_contract(audio_telemetry_version=1),
            }}, FakeWebSocket())
        self.assertTrue(attachment.capabilities["call_control"])
        self.assertFalse(attachment.capabilities["call_signalling"])
        self.assertFalse(attachment.capabilities["call_audio"])
        self.assertIn("telemetry v2", attachment.capabilities["call_contract_error"])

    async def test_unknown_or_mismatched_agent_package_digest_fails_voice_closed(self):
        unknown = await self.registry.attach({
            "iccid": "89852312388530153089", "agent_id": "host-a",
            "modem_id": "modem-a", "capabilities": {
                "call_control": True, "call_signalling": True, "call_audio": True,
                "paid_call_lease_version": 1,
                "call_contract": {"version": 2, "audio_telemetry_version": 2,
                                  "package_version": "1.3.13"},
            }}, FakeWebSocket())
        self.assertFalse(unknown.capabilities["call_signalling"])
        self.assertFalse(unknown.capabilities["call_audio"])
        self.assertIn("package identity is unknown", unknown.capabilities["call_contract_error"])

        mismatched = await self.registry.attach({
            "iccid": "89852312388530153090", "agent_id": "host-b",
            "modem_id": "modem-b", "capabilities": {
                "call_control": True, "call_signalling": True, "call_audio": True,
                "paid_call_lease_version": 1,
                "call_contract": valid_call_contract(package_digest=OTHER_AGENT_PACKAGE_DIGEST),
            }}, FakeWebSocket())
        self.assertFalse(mismatched.capabilities["call_signalling"])
        self.assertFalse(mismatched.capabilities["call_audio"])
        self.assertIn("does not match", mismatched.capabilities["call_contract_error"])

    async def test_disconnect_preserves_offline_identity_and_fails_closed(self):
        attachment = await self.registry.attach({
            "iccid": "89852312388530152529", "agent_id": "host-a", "modem_id": "m-a"},
            FakeWebSocket())
        attachment.status.update({
            "data": "connected", "data_active": True, "ip": "10.191.87.210",
            "proxy": {"ready": True, "port": 37177},
            "cellular": {"ok": True, "status": "ready",
                         "proxy": {"ready": True, "port": 37177}},
        })
        await self.registry.detach(attachment)
        offline = self.registry.list()[0]
        self.assertFalse(offline["online"])
        self.assertFalse(offline["status"]["proxy"]["ready"])
        self.assertFalse(offline["status"]["data_active"])
        self.assertNotIn("ip", offline["status"])
        self.assertEqual(offline["status"]["cellular"]["status"], "unavailable")
        with self.assertRaises(ModemUnavailable):
            await self.registry.rpc(attachment.iccid, "cellular.ensure")

    def test_load_clears_ephemeral_proxy_state_from_offline_records(self):
        record = self.registry._offline_record({
            "online": True,
            "status": {"data": "connected", "data_active": True,
                       "ip": "10.191.87.210", "proxy": {"ready": True},
                       "cellular": {"isolation": {"ready": True}}},
        })
        self.assertFalse(record["online"])
        self.assertFalse(record["status"]["proxy"]["ready"])
        self.assertFalse(record["status"]["data_active"])
        self.assertNotIn("ip", record["status"])
        self.assertNotIn("isolation", record["status"]["cellular"])

    async def test_status_revokes_reverse_listener_when_agent_proxy_stops(self):
        attachment = await self.registry.attach({
            "iccid": "89852312388530152529", "agent_id": "host-a", "modem_id": "m-a",
            "status": {"cellular": {"error": "Windows MBN connection failed"}}},
            FakeWebSocket())
        with patch.object(self.registry, "_close_reverse", new=AsyncMock()) as close:
            await self.registry.receive(
                attachment, {"type": "status", "status": {"proxy": {"ready": False}}})
        close.assert_awaited_once_with(attachment)
        self.assertFalse(attachment.status["cellular"]["proxy"]["ready"])
        self.assertEqual(attachment.status["cellular"]["status"], "unavailable")
        self.assertEqual(attachment.status["cellular"]["error"],
                         "Windows MBN connection failed")

    async def test_inactive_data_snapshot_removes_stale_ip_address(self):
        attachment = await self.registry.attach({
            "iccid": "89852312388530152529", "agent_id": "host-a", "modem_id": "m-a",
            "status": {"data_active": True, "ip": "10.193.229.205"}}, FakeWebSocket())
        with patch.object(self.registry, "_close_reverse", new=AsyncMock()):
            await self.registry.receive(attachment, {
                "type": "status",
                "status": {"data": "disconnected", "data_active": False,
                           "proxy": {"ready": False}},
            })
        self.assertNotIn("ip", attachment.status)

    async def test_recovered_proxy_snapshot_clears_previous_cellular_error(self):
        attachment = await self.registry.attach({
            "iccid": "89852312388530152529", "agent_id": "host-a", "modem_id": "m-a",
            "status": {"cellular": {"ok": False, "status": "unavailable",
                                     "error": "Cellular proxy stopped on the Agent."}}},
            FakeWebSocket())
        attachment.reverse_server = object()
        attachment.reverse_port = 37177
        attachment.status["proxy"] = {"ready": True, "port": 37177, "reverse": True}

        await self.registry.receive(attachment, {
            "type": "status",
            "status": {"data": "disconnected", "data_active": False,
                       "proxy": {"ready": True}},
        })

        self.assertEqual(attachment.status["cellular"]["status"], "starting")
        self.assertIsNone(attachment.status["cellular"]["error"])
        self.assertTrue(attachment.status["cellular"]["proxy"]["ready"])

    async def test_agent_local_proxy_does_not_impersonate_missing_reverse_listener(self):
        attachment = await self.registry.attach({
            "iccid": "89852312388530152529", "agent_id": "host-a", "modem_id": "m-a",
            "status": {"data": "connected", "data_active": True,
                       "proxy": {"ready": True, "port": 61357}}}, FakeWebSocket())

        self.assertTrue(attachment.status["agent_proxy"]["ready"])
        self.assertFalse(attachment.status["proxy"]["ready"])
        await self.registry.receive(attachment, {
            "type": "status", "status": {"data": "connected", "data_active": True,
                                             "proxy": {"ready": True, "port": 61357}},
        })
        self.assertFalse(attachment.status["proxy"]["ready"])
        self.assertEqual(attachment.status["cellular"]["status"], "starting")

    async def test_socks_address_round_trip_ipv4_and_domain(self):
        for host, port in (("192.0.2.9", 53), ("example.test", 443)):
            encoded = _socks_address(host, port)
            reader = asyncio.StreamReader()
            reader.feed_data(encoded[1:])
            reader.feed_eof()
            self.assertEqual(await _read_socks_address(reader, encoded[0]), (host, port))

    async def test_udp_queue_locks_to_control_peer_and_first_source_port(self):
        protocol = _UdpQueue("192.0.2.10")
        protocol.datagram_received(b"wrong host", ("192.0.2.11", 1000))
        self.assertTrue(protocol.queue.empty())
        protocol.datagram_received(b"first", ("192.0.2.10", 1000))
        protocol.datagram_received(b"wrong port", ("192.0.2.10", 1001))
        self.assertEqual(await protocol.queue.get(), b"first")
        self.assertTrue(protocol.queue.empty())

    async def test_persisted_sim_loads_offline_and_keeps_stable_reverse_port(self):
        with tempfile.TemporaryDirectory() as directory, \
                patch("control.app.modem_registry.cfg.DATA_DIR", directory):
            path = os.path.join(directory, "orchestrator", "remote-modems.json")
            os.makedirs(os.path.dirname(path))
            with socket.socket() as probe:
                probe.bind(("127.0.0.1", 0))
                port = probe.getsockname()[1]
            with open(path, "w", encoding="utf-8") as handle:
                json.dump({"version": 2, "sims": {"8985": {"iccid": "8985",
                          "online": True}}, "ports": {"8985": port}}, handle)
            registry = ModemRegistry()
            self.assertFalse(registry.list()[0]["online"])
            attachment = await registry.attach(
                {"iccid": "8985", "agent_id": "host-a", "modem_id": "m-a"},
                FakeWebSocket())
            with patch.dict(os.environ, {"MDD_REMOTE_MODEM_PORT_MIN": str(port),
                                         "MDD_REMOTE_MODEM_PORT_MAX": str(port)}):
                endpoint = await registry._reverse_endpoint(attachment, 9000)
            self.assertEqual(endpoint["port"], port)
            await registry.detach(attachment)
            self.assertEqual(ModemRegistry()._ports["8985"], port)

    async def test_reverse_proxy_defaults_to_loopback_and_never_advertises_wildcard(self):
        attachment = await self.registry.attach({
            "iccid": "89852312388530152529", "agent_id": "host-a", "modem_id": "m-a"},
            FakeWebSocket())
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", 0))
            port = probe.getsockname()[1]
        with patch.dict(os.environ, {
                "MDD_REMOTE_MODEM_BIND_HOST": "",
                "MDD_REMOTE_MODEM_ADVERTISE_HOST": "",
                "MDD_REMOTE_MODEM_PORT_MIN": str(port),
                "MDD_REMOTE_MODEM_PORT_MAX": str(port),
        }, clear=False):
            endpoint = await self.registry._reverse_endpoint(attachment, 9000)
        self.assertEqual(endpoint["host"], "127.0.0.1")
        self.assertEqual(attachment.reverse_server.sockets[0].getsockname()[0], "127.0.0.1")
        await self.registry.detach(attachment)

    def test_reverse_proxy_rejects_wildcard_advertised_host(self):
        with patch.dict(os.environ, {
                "MDD_REMOTE_MODEM_BIND_HOST": "0.0.0.0",
                "MDD_REMOTE_MODEM_ADVERTISE_HOST": "0.0.0.0",
        }, clear=False):
            with self.assertRaisesRegex(ModemUnavailable, "non-wildcard"):
                _reverse_listener_settings()

    async def test_reverse_proxy_caps_concurrent_tunnels_per_sim(self):
        websocket = FakeWebSocket()
        attachment = await self.registry.attach({
            "iccid": "89852312388530152529", "agent_id": "host-a", "modem_id": "m-a"},
            websocket)
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", 0))
            port = probe.getsockname()[1]
        with patch.dict(os.environ, {
                "MDD_REMOTE_MODEM_MAX_TUNNELS": "1",
                "MDD_REMOTE_MODEM_PORT_MIN": str(port),
                "MDD_REMOTE_MODEM_PORT_MAX": str(port),
        }, clear=False):
            await self.registry._reverse_endpoint(attachment, 9000)
            first_reader, first_writer = await asyncio.open_connection("127.0.0.1", port)
            first_writer.write(b"\x05\x01\x00")
            await first_writer.drain()
            self.assertEqual(await first_reader.readexactly(2), b"\x05\x00")
            first_writer.write(b"\x05\x01\x00\x01\x01\x01\x01\x01\x00\x50")
            await first_writer.drain()
            for _ in range(20):
                if attachment.tunnel_waiters:
                    break
                await asyncio.sleep(0)
            self.assertEqual(len(attachment.tunnel_waiters), 1)

            second_reader, second_writer = await asyncio.open_connection("127.0.0.1", port)
            self.assertEqual(await asyncio.wait_for(second_reader.read(1), 1), b"")
            second_writer.close()
            await second_writer.wait_closed()
            first_writer.close()
            await first_writer.wait_closed()
        await self.registry.detach(attachment)

    async def test_stale_heartbeat_marks_attachment_offline(self):
        attachment = await self.registry.attach({
            "iccid": "89852312388530152529", "agent_id": "host-a", "modem_id": "m-a"},
            FakeWebSocket())
        self.assertTrue(self.registry.list()[0]["online"])
        # Simulate 50 seconds passing without messages
        attachment.seen_at -= 50.0
        self.assertFalse(self.registry.list()[0]["online"])
        # Receive a message to refresh seen_at
        await self.registry.receive(attachment, {"type": "ping"})
        self.assertTrue(self.registry.list()[0]["online"])
