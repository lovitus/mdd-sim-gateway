import os
import tempfile
import unittest

from agent import apn_providers


class ApnProviderDatabaseTests(unittest.TestCase):
    _SAMPLE = """<?xml version="1.0"?>
<serviceproviders format="2.0">
  <country code="hk">
    <name>Hong Kong</name>
    <provider>
      <name>Test Carrier</name>
      <gsm>
        <network-id mcc="454" mnc="03"/>
        <apn value="internet" usage="internet"/>
        <apn value="mms" usage="mms"/>
      </gsm>
    </provider>
  </country>
</serviceproviders>
"""

    def setUp(self):
        self.original = apn_providers._load.__wrapped__ if hasattr(
            apn_providers._load, "__wrapped__") else apn_providers._load

    def test_lookup_returns_internet_apn_by_mccmnc(self):
        with tempfile.NamedTemporaryFile(
                mode="w", suffix=".xml", encoding="utf-8", delete=False) as handle:
            handle.write(self._SAMPLE)
            path = handle.name
        original = apn_providers.DATA_PATHS
        try:
            apn_providers.DATA_PATHS = [path]
            self.assertEqual(
                apn_providers.lookup("454", "03"),
                [{"apn": "internet", "name": "internet", "plan": ""}])
        finally:
            apn_providers.DATA_PATHS = original
            os.unlink(path)

    def test_lookup_ignores_non_internet_usage(self):
        with tempfile.NamedTemporaryFile(
                mode="w", suffix=".xml", encoding="utf-8", delete=False) as handle:
            handle.write(self._SAMPLE)
            path = handle.name
        original = apn_providers.DATA_PATHS
        try:
            apn_providers.DATA_PATHS = [path]
            found = apn_providers.lookup("454", "03")
            self.assertNotIn("mms", [item["apn"] for item in found])
        finally:
            apn_providers.DATA_PATHS = original
            os.unlink(path)

    def test_lookup_by_imsi_tries_both_mnc_lengths(self):
        with tempfile.NamedTemporaryFile(
                mode="w", suffix=".xml", encoding="utf-8", delete=False) as handle:
            handle.write(self._SAMPLE)
            path = handle.name
        original = apn_providers.DATA_PATHS
        try:
            apn_providers.DATA_PATHS = [path]
            # Both 45403 (5 digits) and 454031 (6 digits) should find the same record.
            self.assertEqual(
                [item["apn"] for item in apn_providers.lookup_by_imsi("454031234567890")],
                ["internet"])
        finally:
            apn_providers.DATA_PATHS = original
            os.unlink(path)


if __name__ == "__main__":
    unittest.main()
