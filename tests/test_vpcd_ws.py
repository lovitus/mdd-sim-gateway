import asyncio
import os
import tempfile
import unittest
from unittest.mock import patch

from starlette.testclient import TestClient

from control.app import auth
from control.app import vpcd_slots
from control.app.main import (
    app,
    _claim_and_open_vpcd_transport,
    _forward_vpcd_websocket_to_tcp,
    _vpcd_frame,
    _vpcd_read_frame,
)


class VpcdWebSocketTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.path_patch = patch.object(auth, "AUTH_PATH", os.path.join(self.temp.name, "auth.json"))
        self.path_patch.start()
        auth._sessions.clear()
        auth._failures.clear()
        auth.setup("admin-strong-password", "admin")
        self.valid_token = auth.get_or_create_agent_token()
        self.client = TestClient(app)

    def tearDown(self):
        self.path_patch.stop()
        self.temp.cleanup()

    def test_websocket_rejects_missing_or_invalid_token(self):
        # 1. Missing token -> rejected with 4003 or connection refused
        with self.assertRaises(Exception):
            with self.client.websocket_connect("/api/vpcd/ws") as ws:
                pass

        # 2. Invalid token -> rejected
        with self.assertRaises(Exception):
            with self.client.websocket_connect("/api/vpcd/ws?token=invalid_token") as ws:
                pass

    def test_cert_fingerprint_generation(self):
        fingerprint = auth.get_cert_fingerprint()
        # Should return a string (empty if no cert yet or colon-separated SHA-256 hex)
        self.assertIsInstance(fingerprint, str)
        if fingerprint:
            self.assertTrue(len(fingerprint) >= 64)
            self.assertIn(":", fingerprint)

    def test_vpcd_tcp_framing_is_removed_at_websocket_boundary(self):
        payload = bytes.fromhex("00A4040007A0000000871002")
        async def read():
            reader = asyncio.StreamReader()
            reader.feed_data(_vpcd_frame(payload))
            reader.feed_eof()
            return await _vpcd_read_frame(reader)
        self.assertEqual(asyncio.run(read()), payload)

    def test_empty_binary_atr_is_forwarded_without_closing_transport(self):
        class WebSocket:
            def __init__(self):
                self.frames = [b"", b"next"]

            async def receive_bytes(self):
                if self.frames:
                    return self.frames.pop(0)
                from starlette.websockets import WebSocketDisconnect
                raise WebSocketDisconnect()

        class Writer:
            def __init__(self):
                self.data = []

            def write(self, value):
                self.data.append(value)

            async def drain(self):
                return None

        class Registry:
            def __init__(self):
                self.touches = 0

            def touch(self, _claim):
                self.touches += 1

        async def forward():
            websocket, writer, registry = WebSocket(), Writer(), Registry()
            with self.assertRaises(Exception):
                await _forward_vpcd_websocket_to_tcp(
                    websocket, writer, registry, object())
            return writer.data, registry.touches

        data, touches = asyncio.run(forward())
        self.assertEqual(data, [_vpcd_frame(b""), _vpcd_frame(b"next")])
        self.assertEqual(touches, 2)

    def test_auto_slot_skips_unreachable_local_vpcd_port(self):
        registry = vpcd_slots.VpcdSlotRegistry(
            os.path.join(self.temp.name, "slots.json"), max_slots=3)
        attempts = []

        async def connect(_host, port):
            attempts.append(port)
            if port == vpcd_slots.BASE_PORT:
                raise OSError("stale listener")
            return object(), object()

        async def run():
            with patch("control.app.main.asyncio.open_connection", side_effect=connect):
                return await _claim_and_open_vpcd_transport(
                    registry=registry,
                    claim_kwargs={
                        "agent_id": "agent-a",
                        "reader_id": "reader-a",
                        "reader_name": "USB Reader",
                        "requested_slot": "auto",
                        "card_id": "",
                        "imei": "",
                        "peer": "test",
                    },
                    unavailable_slots=set(),
                )

        claim, _reader, _writer = asyncio.run(run())
        self.assertEqual(attempts, [vpcd_slots.BASE_PORT, vpcd_slots.BASE_PORT + 1])
        self.assertEqual(claim.slot, 1)
        snapshot = {item["slot"]: item for item in registry.snapshot()}
        self.assertFalse(snapshot[0]["online"])
        self.assertTrue(snapshot[1]["online"])
        registry.release(claim)


if __name__ == "__main__":
    unittest.main()
