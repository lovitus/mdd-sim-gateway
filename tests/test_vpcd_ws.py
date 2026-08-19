import asyncio
import os
import tempfile
import unittest
from unittest.mock import patch

from starlette.testclient import TestClient

from control.app import auth
from control.app.main import app, _vpcd_frame, _vpcd_read_frame


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


if __name__ == "__main__":
    unittest.main()
