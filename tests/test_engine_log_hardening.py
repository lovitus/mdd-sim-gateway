import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import yaml

from control.app import config


class ProductionDebugTests(unittest.TestCase):
    def _paths(self, temp):
        return patch.multiple(
            config, DATA_DIR=temp, CONFIG_PATH=os.path.join(temp, "config.yaml"))

    def test_legacy_saved_asterisk_debug_is_disabled_on_load(self):
        with tempfile.TemporaryDirectory() as temp, self._paths(temp):
            with open(config.CONFIG_PATH, "w", encoding="utf-8") as handle:
                yaml.safe_dump({
                    "settings": {"debug": {"asterisk": True, "charon": True}},
                    "instances": {"1": {
                        "id": "1", "debug": {"asterisk": True, "charon": True},
                    }},
                }, handle)

            loaded = config.load()

            self.assertFalse(loaded["instances"]["1"]["debug"]["asterisk"])
            self.assertTrue(loaded["instances"]["1"]["debug"]["charon"])

    def test_upsert_cannot_persist_asterisk_debug(self):
        with tempfile.TemporaryDirectory() as temp, self._paths(temp), \
                patch.object(config, "alloc_ports_auto", return_value=config._alloc_ports(0)):
            saved = config.upsert_instance({
                "id": "1", "debug": {"asterisk": True, "charon": True},
            })

            self.assertFalse(saved["debug"]["asterisk"])
            self.assertTrue(saved["debug"]["charon"])

    def test_engine_contract_forces_debug_off_even_for_imported_config(self):
        inst = {
            "id": "1", "imsi": "001010000000001", "mcc": "001", "mnc": "01",
            "imei": "123456789012345", "ami_secret": "secret",
            "sip": {"webrtc": {"password": "password"}},
            "debug": {"asterisk": True, "charon": True},
        }
        settings = {**config.DEFAULTS["settings"],
                    "debug": {"asterisk": True, "charon": False}}
        with tempfile.TemporaryDirectory() as temp, self._paths(temp):
            rendered = config.render_instance_json(inst, settings)

        self.assertFalse(rendered["debug"]["asterisk"])
        self.assertTrue(rendered["debug"]["charon"])

    def test_tls_domain_is_not_used_as_an_ice_host_candidate(self):
        settings = {**config.DEFAULTS["settings"],
                    "tls": {"domain": "gateway.example.test"}}
        with patch.dict(os.environ, {"MDD_ADVERTISE_ADDR": "192.0.2.25"}, clear=False):
            self.assertEqual(config.advertise_address(settings), "gateway.example.test")
            self.assertEqual(config.ice_advertise_address(settings), "192.0.2.25")

    def test_non_ip_ice_override_falls_back_to_detected_lan_ip(self):
        settings = {**config.DEFAULTS["settings"], "advertise_address": "not-an-ip"}
        with patch.dict(os.environ, {"MDD_ADVERTISE_ADDR": "also-not-an-ip"}, clear=False), \
                patch.object(config, "_host_lan_ipv4", return_value="198.51.100.8"):
            self.assertEqual(config.ice_advertise_address(settings), "198.51.100.8")


class AsteriskModulePolicyTests(unittest.TestCase):
    def test_unused_error_generating_modules_are_excluded(self):
        root = Path(__file__).resolve().parent.parent
        policy = (root / "engine" / "templates" / "modules.conf.j2").read_text()

        self.assertIn("autoload = yes", policy)
        for module in (
                "app_adsiprog.so", "app_getcpeid.so", "codec_vevs.so", "res_adsi.so",
                "res_ari.so", "res_config_ldap.so",
                "res_odbc.so", "res_phoneprov.so", "res_pjsip_config_wizard.so"):
            self.assertIn(f"noload => {module}", policy)
        for required in ("chan_pjsip.so", "codec_amr.so", "res_http_websocket.so",
                         "res_pjsip_messaging.so", "res_rtp_asterisk.so"):
            self.assertNotIn(f"noload => {required}", policy)

    def test_catch_all_dialplan_avoids_bare_dot_wildcard(self):
        root = Path(__file__).resolve().parent.parent
        dialplan = (root / "engine" / "templates" / "extensions.conf.j2").read_text()

        self.assertIn("{% set any_extension = '_[!-~]!' %}", dialplan)
        self.assertNotIn("exten => _.,", dialplan)
        self.assertEqual(dialplan.count("exten => {{ any_extension }},1"), 3)

    def test_private_resolve_fields_use_nodoc_registration(self):
        root = Path(__file__).resolve().parent.parent
        patcher = (root / "engine" / "patches" / "asterisk" /
                   "resolve_config_docs.py").read_text()

        self.assertIn("ast_sorcery_object_field_register_nodoc", patcher)
        self.assertIn("expected 3 resolve field registrations", patcher)

    def test_missing_security_server_reauth_keeps_established_sas(self):
        root = Path(__file__).resolve().parent.parent
        patcher = (root / "engine" / "patches" / "asterisk" /
                   "reauth_missing_security_server.py").read_text()

        self.assertIn("handle_volte_unauthorized", patcher)
        self.assertIn("transport_state->volte.registered", patcher)
        self.assertIn("VOLTE_STATE_RESPONSE", patcher)
        # The fallback must only swallow an ABSENT header; parse failures stay fatal.
        self.assertIn("pjsip_msg_find_hdr_by_name", patcher)

    def test_swu_workers_keep_fork_semantics_on_python_314(self):
        root = Path(__file__).resolve().parent.parent
        swu = (root / "engine" / "swu_ike.py").read_text()

        self.assertIn('multiprocessing.set_start_method("fork", force=True)', swu)


if __name__ == "__main__":
    unittest.main()
