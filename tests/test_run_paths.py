import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from control import run


class RuntimePathTests(unittest.TestCase):
    def test_container_data_path_maps_to_native_data_directory(self):
        with tempfile.TemporaryDirectory() as temp:
            expected = Path(temp) / "certs" / "gateway.pem"
            expected.parent.mkdir()
            expected.write_text("certificate")
            with patch.object(run.cfg, "DATA_DIR", temp):
                self.assertEqual(run._runtime_path("/data/certs/gateway.pem"), str(expected))

    def test_missing_or_external_path_is_not_rewritten(self):
        with tempfile.TemporaryDirectory() as temp, patch.object(run.cfg, "DATA_DIR", temp):
            self.assertEqual(run._runtime_path("/data/certs/missing.pem"),
                             "/data/certs/missing.pem")
            self.assertEqual(run._runtime_path("/etc/cert.pem"), "/etc/cert.pem")


if __name__ == "__main__":
    unittest.main()
