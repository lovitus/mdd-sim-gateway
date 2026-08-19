import unittest

from control.app.main import _auth_path, _unsafe_spa_path


class StaticWebSecurityTests(unittest.TestCase):
    def test_rejects_plain_and_multiply_encoded_parent_traversal(self):
        for path in (
            "../../../../etc/shadow",
            "%2e%2e/%2e%2e/etc/shadow",
            "%252e%252e/%252e%252e/etc/shadow",
            "..%2f..%2fetc%2fshadow",
            "..\\..\\etc\\shadow",
        ):
            with self.subTest(path=path):
                self.assertTrue(_unsafe_spa_path(path))

    def test_allows_normal_spa_routes(self):
        for path in ("", "devices", "settings/security", "logo.svg"):
            with self.subTest(path=path):
                self.assertFalse(_unsafe_spa_path(path))

    def test_context_prefixed_management_api_is_still_authenticated(self):
        self.assertEqual(_auth_path("/mdd/api/devices"), "/api/devices")
        self.assertEqual(_auth_path("/api/devices"), "/api/devices")
        self.assertEqual(_auth_path("/mdd"), "/")


if __name__ == "__main__":
    unittest.main()
