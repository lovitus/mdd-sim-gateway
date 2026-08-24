import time
import unittest

from control.app.main import (
    _ESIM_CACHE_TTL,
    _esim_cache_for_card,
    _esim_cache_store,
    _esim_cache_write,
)


class EsimCacheTtlTests(unittest.TestCase):
    def setUp(self):
        self._sample_ses = [{
            "eid": "89049032000001000000000000000001",
            "profiles": [
                {"iccid": "89852312388530152529", "profileName": "Test eSIM", "profileState": "enabled"}
            ]
        }]
        _esim_cache_write({})

    def tearDown(self):
        _esim_cache_write({})

    def test_cache_hits_when_valid_and_fresh(self):
        _esim_cache_store(self._sample_ses, "123456789012345", {"iccid": "89852312388530152529"})
        card_info = {"iccid": "89852312388530152529", "present": True}
        entry = _esim_cache_for_card(card_info)
        self.assertIsNotNone(entry)
        self.assertEqual(entry["imei"], "123456789012345")
        self.assertTrue(time.time() - entry["ts"] < _ESIM_CACHE_TTL)

    def test_cache_miss_when_card_info_empty(self):
        _esim_cache_store(self._sample_ses, "123456789012345", {"iccid": "89852312388530152529"})
        entry = _esim_cache_for_card({})
        self.assertIsNone(entry)

    def test_cache_ttl_expiration(self):
        past_ts = int(time.time()) - (_ESIM_CACHE_TTL + 60)
        cache_data = {
            "89049032000001000000000000000001": {
                "ses": self._sample_ses,
                "imei": "123456789012345",
                "ts": past_ts,
                "endpoint_key": "",
            }
        }
        _esim_cache_write(cache_data)
        card_info = {"iccid": "89852312388530152529", "present": True}
        entry = _esim_cache_for_card(card_info)
        self.assertIsNotNone(entry)
        self.assertTrue((time.time() - entry.get("ts", 0)) > _ESIM_CACHE_TTL)


if __name__ == "__main__":
    unittest.main()
