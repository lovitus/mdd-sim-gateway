import tempfile
import unittest
import subprocess
from pathlib import Path
from unittest.mock import patch

from control.app import config


class ProductBoundaryTests(unittest.TestCase):
    def temp_config(self):
        temp = tempfile.TemporaryDirectory()
        paths = patch.multiple(
            config,
            DATA_DIR=temp.name,
            CONFIG_PATH=str(Path(temp.name) / "config.yaml"),
        )
        return temp, paths

    def test_saved_sim_lines_are_not_limited_by_active_slot_count(self):
        temp, paths = self.temp_config()
        with temp, paths:
            # Exercise the full default VPCD slot scale. Port allocation, rather than an
            # unrelated product-policy count, remains the real resource boundary.
            for iid in range(1, 17):
                config.upsert_instance({"id": str(iid), "name": f"SIM {iid}"})
            self.assertEqual(len(config.list_instances()), 16)
            edited = config.upsert_instance({"id": "16", "name": "kept"})
            self.assertEqual(edited["name"], "kept")

    def test_stale_remote_controls_are_removed_on_load_and_save(self):
        temp, paths = self.temp_config()
        with temp, paths:
            config.save({
                "settings": {"telegram": {"commands": {"enabled": True}}},
                "instances": {"1": {"id": "1", "sip": {
                    "external": [{"username": "remote", "password": "secret"}]}}},
            })
            loaded = config.load()
            self.assertNotIn("commands", loaded["settings"]["telegram"])
            self.assertEqual(loaded["instances"]["1"]["sip"]["external"], [])

            saved = config.upsert_instance({"id": "1", "sip": {
                "external": [{"username": "remote", "password": "secret"}]}})
            self.assertEqual(saved["sip"]["external"], [])

    def test_engine_overlay_does_not_start_a_temporary_container(self):
        """Legacy images contain file-volume declarations that make Docker RUN fail."""
        overlay = (Path(__file__).resolve().parents[1] / "engine" /
                   "Dockerfile.overlay").read_text(encoding="utf-8")
        instructions = [line.strip().split(maxsplit=1)[0].upper()
                        for line in overlay.splitlines()
                        if line.strip() and not line.lstrip().startswith("#")]
        self.assertNotIn("RUN", instructions)

    def test_control_runtime_overlay_is_download_and_compile_free(self):
        overlay = (Path(__file__).resolve().parents[1] / "control" /
                   "Dockerfile.runtime-overlay").read_text(encoding="utf-8")
        instructions = [line.strip().split(maxsplit=1)[0].upper()
                        for line in overlay.splitlines()
                        if line.strip() and not line.lstrip().startswith("#")]
        self.assertNotIn("RUN", instructions)
        self.assertIn("COPY", instructions)

    def test_docker_preflight_recognizes_owned_host_network_control_listener(self):
        install = (Path(__file__).resolve().parents[1] / "install.sh").read_text(
            encoding="utf-8")
        self.assertIn('[ "${MODE:-}" = docker ] && control_running &&', install)
        self.assertIn('docker_container_owned "$CONTROL_NAME"', install)

    def test_ambiguous_media_address_never_falls_back_to_public_default_route(self):
        install = (Path(__file__).resolve().parents[1] / "install.sh").read_text()
        self.assertNotIn("ip route get 1.1.1.1", install)
        self.assertNotIn("MDD_ADVERTISE_ADDR", install)
        self.assertIn('/sys/class/net/$interface/bridge', install)
        detect = install.split("detect_lan_ip() {", 1)[1].split("\n}", 1)[0]
        self.assertNotIn("host_global_ipv4s | sed -n '1p'", detect)
        self.assertIn('[ "$count" = 1 ]', detect)
        shell = f'''set -e
    host_global_ipv4s() {{ printf '%s\\n' 192.0.2.10 198.51.100.20; }}
detect_lan_ip() {{{detect}
}}
LAN_IP="$(detect_lan_ip)"
test -z "$LAN_IP"
printf survived
'''
        result = subprocess.run(
            ["bash", "-c", shell], check=False, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "survived")

    def test_agent_runtime_has_no_site_specific_gateway_default(self):
        root = Path(__file__).resolve().parents[1]
        from agent.config_store import DEFAULT_CONFIG

        self.assertEqual(DEFAULT_CONFIG["server"], "")
        runtime_sources = [
            root / "agent" / "config_store.py",
            root / "agent" / "modem_agent.py",
            root / "agent" / "macos" / "tray.py",
            root / "control" / "app" / "media_ingress.py",
            root / "control" / "app" / "sip_media_proxy.py",
            root / "control" / "run.py",
            root / "host" / "mdd_orchestrator.py",
            root / "agent" / "run-macos.command",
            root / "agent" / "android" / "app" / "src" / "main" / "java" / "com" /
            "mdd" / "cardagent" / "MainActivity.kt",
            root / "agent" / "android" / "app" / "src" / "main" / "java" / "com" /
            "mdd" / "cardagent" / "service" / "CardForwarderService.kt",
        ]
        for path in runtime_sources:
            source = path.read_text(encoding="utf-8")
            self.assertNotIn("10.44.0.23", source, str(path))

    def test_browser_endpoint_disables_sip_transfer_bypass(self):
        template = (Path(__file__).resolve().parents[1] / "engine" / "templates" /
                    "pjsip.conf.j2").read_text(encoding="utf-8")
        local_endpoint = template.split("[endpoint-local](!)", 1)[1].split(
            "[auth-local](!)", 1)[0]
        self.assertIn("allow_transfer=no", local_endpoint)

    def test_browser_media_route_is_session_rewritten_not_engine_global(self):
        root = Path(__file__).resolve().parents[1]
        rtp = (root / "engine" / "templates" / "rtp.conf.j2").read_text(
            encoding="utf-8")
        pjsip = (root / "engine" / "templates" / "pjsip.conf.j2").read_text(
            encoding="utf-8")
        ingress = (root / "control" / "app" / "media_ingress.py").read_text(
            encoding="utf-8")
        proxy = (root / "control" / "app" / "sip_media_proxy.py").read_text(
            encoding="utf-8")
        self.assertNotIn("[ice_host_candidates]", rtp)
        self.assertNotIn("external_media_address", pjsip)
        self.assertNotIn("ip route get", ingress)
        self.assertNotIn("default route", proxy.casefold())
        self.assertIn("rewrite_engine_sdp", proxy)

    def test_status_polling_cannot_trigger_an_ims_register(self):
        """REGISTER is an explicit operator action, never a status/metadata side effect."""
        source = (Path(__file__).resolve().parents[1] / "control" / "app" /
                  "main.py").read_text(encoding="utf-8")
        command = 'pjsip send register volte_ims'
        self.assertEqual(source.count(command), 1)
        endpoint = source.split('@app.post("/api/instances/{iid}/register")', 1)[1]
        self.assertIn(command, endpoint.split("# ----------------------------- SMS", 1)[0])
        for forbidden in ("learn_msisdn", "_verify_ims_msisdn", "extract_msisdn",
                          "msisdn_pending_apply"):
            self.assertNotIn(forbidden, source)

    def test_fatal_registration_rejections_are_paced_without_slowing_no_response(self):
        template = (Path(__file__).resolve().parents[1] / "engine" / "templates" /
                    "pjsip.conf.j2").read_text(encoding="utf-8")
        self.assertIn("retry_interval=30", template)
        self.assertIn("fatal_retry_interval=3600", template)

if __name__ == "__main__":
    unittest.main()
