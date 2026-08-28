import os
from pathlib import Path
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

    def test_lookup_returns_internet_apn_by_mccmnc(self):
        with tempfile.NamedTemporaryFile(
                mode="w", suffix=".xml", encoding="utf-8", delete=False) as handle:
            handle.write(self._SAMPLE)
            path = handle.name
        original = apn_providers.DATA_PATHS
        try:
            apn_providers.DATA_PATHS = [path]
            apn_providers._load.cache_clear()
            self.assertEqual(
                apn_providers.lookup("454", "03"),
                [{"apn": "internet", "name": "internet", "plan": ""}])
        finally:
            apn_providers.DATA_PATHS = original
            apn_providers._load.cache_clear()
            os.unlink(path)

    def test_lookup_ignores_non_internet_usage(self):
        with tempfile.NamedTemporaryFile(
                mode="w", suffix=".xml", encoding="utf-8", delete=False) as handle:
            handle.write(self._SAMPLE)
            path = handle.name
        original = apn_providers.DATA_PATHS
        try:
            apn_providers.DATA_PATHS = [path]
            apn_providers._load.cache_clear()
            found = apn_providers.lookup("454", "03")
            self.assertNotIn("mms", [item["apn"] for item in found])
        finally:
            apn_providers.DATA_PATHS = original
            apn_providers._load.cache_clear()
            os.unlink(path)

    def test_lookup_by_imsi_tries_both_mnc_lengths(self):
        with tempfile.NamedTemporaryFile(
                mode="w", suffix=".xml", encoding="utf-8", delete=False) as handle:
            handle.write(self._SAMPLE)
            path = handle.name
        original = apn_providers.DATA_PATHS
        try:
            apn_providers.DATA_PATHS = [path]
            apn_providers._load.cache_clear()
            # Both 45403 (5 digits) and 454031 (6 digits) should find the same record.
            self.assertEqual(
                [item["apn"] for item in apn_providers.lookup_by_imsi("454031234567890")],
                ["internet"])
        finally:
            apn_providers.DATA_PATHS = original
            apn_providers._load.cache_clear()
            os.unlink(path)

    def test_lookup_unknown_mccmnc_returns_empty(self):
        self.assertEqual(apn_providers.lookup("999", "99"), [])

    def test_lookup_none_inputs_returns_empty(self):
        self.assertEqual(apn_providers.lookup(None, None), [])
        self.assertEqual(apn_providers.lookup("454", None), [])
        self.assertEqual(apn_providers.lookup(None, "03"), [])

    def test_lookup_by_imsi_too_short_returns_empty(self):
        self.assertEqual(apn_providers.lookup_by_imsi("1234"), [])
        self.assertEqual(apn_providers.lookup_by_imsi(""), [])
        self.assertEqual(apn_providers.lookup_by_imsi(None), [])

    def test_windows_single_file_build_bundles_the_provider_database(self):
        spec = Path(apn_providers.__file__).with_name("mdd-modem-agent.spec").read_text(
            encoding="utf-8")
        self.assertIn('("resources/serviceproviders.xml", "resources")', spec)


if __name__ == "__main__":
    unittest.main()
