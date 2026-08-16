import asyncio
import os
import tempfile
import unittest
from unittest.mock import patch

from starlette.testclient import TestClient

from control.app import auth
from control.app.main import app


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


if __name__ == "__main__":
    unittest.main()
