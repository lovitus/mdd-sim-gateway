import os
import tempfile
import unittest
from unittest import mock

from agent import sms_history


class SmsHistoryTests(unittest.TestCase):
    def setUp(self):
        self.original = sms_history.HISTORY_PATH

    def tearDown(self):
        sms_history.HISTORY_PATH = self.original

    def _temp_store(self):
        handle = tempfile.NamedTemporaryFile(
            mode="w", suffix=".json", encoding="utf-8", delete=False)
        handle.write("{}")
        handle.close()
        sms_history.HISTORY_PATH = handle.name
        self.addCleanup(os.unlink, handle.name)
        return handle.name

    def test_record_and_load(self):
        self._temp_store()
        sms_history.record("89852312388530152529", "+85362101201")
        self.assertEqual(
            sms_history.get("89852312388530152529"),
            {"service_center": "+85362101201", "timestamp": mock.ANY})

    def test_changed_detects_difference_from_recorded(self):
        self._temp_store()
        sms_history.record("89852312388530152529", "+85362101201")
        self.assertTrue(sms_history.changed("89852312388530152529", "+85290000000"))

    def test_changed_is_false_when_value_matches(self):
        self._temp_store()
        sms_history.record("89852312388530152529", "+85362101201")
        self.assertFalse(sms_history.changed("89852312388530152529", "+85362101201"))

    def test_changed_is_false_without_record(self):
        self._temp_store()
        self.assertFalse(sms_history.changed("89852312388530152529", "+85362101201"))

    def test_record_ignores_empty_iccid(self):
        self._temp_store()
        sms_history.record("", "+85362101201")
        self.assertEqual(sms_history.load(), {})

    def test_record_ignores_empty_service_center(self):
        self._temp_store()
        sms_history.record("89852312388530152529", "")
        self.assertEqual(sms_history.load(), {})

    def test_load_recovers_from_corrupt_json(self):
        path = self._temp_store()
        with open(path, "w") as f:
            f.write("{corrupt json!!!")
        self.assertEqual(sms_history.load(), {})

    def test_changed_with_none_iccid(self):
        self._temp_store()
        self.assertFalse(sms_history.changed(None, "+85362101201"))

    def test_changed_does_not_treat_unreadable_current_value_as_a_change(self):
        self._temp_store()
        sms_history.record("89852312388530152529", "+85362101201")
        self.assertFalse(sms_history.changed("89852312388530152529", ""))
        self.assertFalse(sms_history.changed("89852312388530152529", None))


if __name__ == "__main__":
    unittest.main()
