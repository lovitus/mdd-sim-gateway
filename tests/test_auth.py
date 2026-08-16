import json
import os
import tempfile
import unittest
from unittest.mock import patch

from control.app import auth


class AuthTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.path_patch = patch.object(auth, "AUTH_PATH", os.path.join(self.temp.name, "auth.json"))
        self.path_patch.start()
        auth._sessions.clear()
        auth._failures.clear()

    def tearDown(self):
        self.path_patch.stop()
        self.temp.cleanup()

    def test_setup_hashes_password_and_creates_session(self):
        auth.setup("correct horse battery", "admin")
        self.assertEqual(auth.username(), "admin")
        with open(auth.AUTH_PATH, encoding="utf-8") as handle:
            stored = json.load(handle)
        self.assertNotIn("correct horse battery", json.dumps(stored))
        token, csrf = auth.login("admin", "correct horse battery", "127.0.0.1")
        self.assertEqual(auth.session(token)["csrf"], csrf)

    def test_wrong_password_is_rate_limited(self):
        auth.setup("correct horse battery")
        for _ in range(5):
            self.assertIsNone(auth.login("admin", "wrong password", "192.0.2.4"))
        self.assertGreater(auth.throttled("192.0.2.4"), 0)

    def test_short_password_and_second_setup_are_rejected(self):
        with self.assertRaises(ValueError):
            auth.setup("short")
        auth.setup("ten-characters")
        with self.assertRaises(ValueError):
            auth.setup("another-password")

    def test_password_change_revokes_sessions(self):
        auth.setup("correct horse battery")
        token, _ = auth.login("admin", "correct horse battery", "127.0.0.1")
        auth.change_password("correct horse battery", "different safe password")
        self.assertIsNone(auth.session(token))
        self.assertIsNone(auth.login("admin", "correct horse battery", "127.0.0.1"))
        self.assertIsNotNone(auth.login("admin", "different safe password", "127.0.0.1"))


if __name__ == "__main__":
    unittest.main()
