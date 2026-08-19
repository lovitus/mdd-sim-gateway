import asyncio
import json
import os
import socket
import tempfile
import unittest
from unittest.mock import AsyncMock, patch

from control.app.modem_registry import (
    ModemConflict, ModemRegistry, ModemUnavailable, _UdpQueue,
    _read_socks_address, _socks_address,
)


class FakeWebSocket:
    def __init__(self):
        self.sent = []

    async def send_json(self, value):
        self.sent.append(value)


class ModemRegistryTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.registry = ModemRegistry()
        self.persist = patch.object(self.registry, "_persist")
        self.persist.start()

    async def asyncTearDown(self):
        self.persist.stop()

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

    async def test_disconnect_preserves_offline_identity_and_fails_closed(self):
        attachment = await self.registry.attach({
            "iccid": "89852312388530152529", "agent_id": "host-a", "modem_id": "m-a"},
            FakeWebSocket())
        await self.registry.detach(attachment)
        self.assertFalse(self.registry.list()[0]["online"])
        with self.assertRaises(ModemUnavailable):
            await self.registry.rpc(attachment.iccid, "cellular.ensure")

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
