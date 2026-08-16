import importlib.util
from datetime import datetime, timezone
import io
import os
from pathlib import Path
import tempfile
import unittest


def load_capture_module():
    path = Path(__file__).resolve().parent.parent / "engine" / "log_capture.py"
    spec = importlib.util.spec_from_file_location("mdd_log_capture", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class LogCaptureTests(unittest.TestCase):
    def setUp(self):
        self.module = load_capture_module()

    def test_timestamp_prefix_has_date_time_and_offset(self):
        now = datetime(2026, 8, 7, 11, 22, 33, tzinfo=timezone.utc)
        self.assertEqual(self.module.timestamp_line("STATE 2:\n", now),
                         "[2026-08-07 11:22:33+0000] STATE 2:\n")

    def test_existing_log_is_archived_before_new_stream(self):
        with tempfile.TemporaryDirectory() as temp:
            current = Path(temp) / "run" / "charon.log"
            archive = Path(temp) / "logs" / "ike"
            current.parent.mkdir()
            current.write_text("old evidence\n")

            self.module.capture(io.StringIO("STATE CONNECTED\n"), current, archive)

            saved = list(archive.glob("charon-*.log"))
            self.assertEqual(len(saved), 1)
            self.assertEqual(saved[0].read_text(), "old evidence\n")
            self.assertRegex(current.read_text(),
                             r"^\[\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}[+-]\d{4}\] STATE CONNECTED\n$")

    def test_pruning_enforces_age_and_total_size(self):
        with tempfile.TemporaryDirectory() as temp:
            archive = Path(temp)
            old = archive / "charon-20200101-000000.log"
            middle = archive / "charon-20260806-000000.log"
            newest = archive / "charon-20260807-000000.log"
            for index, path in enumerate((old, middle, newest)):
                path.write_bytes(b"x" * 10)
                os.utime(path, (100 + index, 100 + index))
            os.utime(old, (0, 0))

            self.module.prune_archives(archive, retention_days=7, max_total_bytes=10,
                                       now=7 * 86400 + 1)

            self.assertFalse(old.exists())
            self.assertFalse(middle.exists())
            self.assertTrue(newest.exists())


if __name__ == "__main__":
    unittest.main()
