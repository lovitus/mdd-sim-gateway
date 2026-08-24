import os
import tempfile
import unittest
from unittest.mock import patch

from control.app import config


class PortAllocationTests(unittest.TestCase):
    def test_new_blocks_use_the_compact_rtp_span(self):
        block = config._alloc_ports(0)
        self.assertEqual(block["rtp_span"], config.DEFAULT_RTP_SPAN)
        self.assertEqual(config.rtp_span(block), 12)
        self.assertEqual(len(config._block_ports(block)), 16)

    def test_saved_blocks_without_span_keep_legacy_width(self):
        block = {key: value for key, value in config._alloc_ports(0).items()
                 if key != "rtp_span"}
        self.assertEqual(config.rtp_span(block), config.LEGACY_RTP_SPAN)
        self.assertIn(block["rtp_start"] + 59, config._block_ports(block))

    def test_rendered_rtp_end_matches_the_effective_span(self):
        base = {
            "id": "1", "imsi": "001010000000001", "mcc": "001", "mnc": "01",
            "imei": "123456789012345", "ami_secret": "secret",
            "sip": {"webrtc": {"password": "password"}},
        }
        with tempfile.TemporaryDirectory() as temp, \
                patch.object(config, "DATA_DIR", temp), \
                patch.object(config, "CONFIG_PATH", os.path.join(temp, "config.yaml")):
            compact = {**base, "ports": config._alloc_ports(0)}
            rendered = config.render_instance_json(compact, config.DEFAULTS["settings"])
            self.assertEqual(rendered["rtp_end"], rendered["rtp_start"] + 11)

            legacy_ports = {key: value for key, value in config._alloc_ports(0).items()
                            if key != "rtp_span"}
            legacy = {**base, "ports": legacy_ports}
            rendered = config.render_instance_json(legacy, config.DEFAULTS["settings"])
            self.assertEqual(rendered["rtp_end"], rendered["rtp_start"] + 59)

    def test_auto_allocation_skips_host_udp_listener_inside_rtp_span(self):
        data = {"instances": {
            str(index): {"ports": config._alloc_ports(index)} for index in range(6)
        }}
        with patch.object(config, "_host_port_free",
                          side_effect=lambda port: port != 22000):
            block = config.alloc_ports_auto(data)
        self.assertEqual(block["rtp_start"], 24000)

    def test_manual_allocation_rejects_host_rtp_listener(self):
        with patch.object(config, "_host_port_free",
                          side_effect=lambda port: port != 10005):
            with self.assertRaisesRegex(ValueError, "10005 .*RTP/UDP"):
                config.ports_from_sip_base({"instances": {}}, 5060)

    def test_legacy_nominal_block_is_treated_as_automatic(self):
        self.assertTrue(config.instance_uses_auto_ports({
            "index": 6, "ports": config._alloc_ports(6),
        }))
        self.assertFalse(config.instance_uses_auto_ports({
            "index": 6, "port_mode": "manual", "ports": config._alloc_ports(6),
        }))


if __name__ == "__main__":
    unittest.main()
