import importlib
import json
import sys
import tempfile
import unittest
from datetime import timedelta, timezone
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import MagicMock, patch


def admitted_images(image_id="sha256:" + "a" * 64,
                    abi="mdd-admission-v1"):
    inspected = SimpleNamespace(
        id=image_id,
        attrs={"Config": {"Labels": {
            "io.mdd-sim-gateway.admission-abi": abi,
        }}},
    )
    return SimpleNamespace(get=lambda _image: inspected)


class EnginePathTests(unittest.TestCase):
    @staticmethod
    def engine_module():
        fake_docker = SimpleNamespace(
            from_env=lambda: None,
            errors=SimpleNamespace(NotFound=type("NotFound", (Exception,), {})),
        )
        with patch.dict(sys.modules, {"docker": fake_docker}):
            sys.modules.pop("control.app.engine", None)
            return importlib.import_module("control.app.engine")

    def test_docker_tls_path_maps_to_native_data_directory(self):
        engine = self.engine_module()
        with tempfile.TemporaryDirectory() as temp:
            expected = Path(temp) / "certs" / "gateway.pem"
            expected.parent.mkdir()
            expected.write_text("certificate")
            with patch.object(engine, "DATA_DIR", temp):
                self.assertEqual(engine._runtime_data_path("/data/certs/gateway.pem"),
                                 str(expected))

    def test_missing_tls_path_remains_unchanged(self):
        engine = self.engine_module()
        with tempfile.TemporaryDirectory() as temp, patch.object(engine, "DATA_DIR", temp):
            self.assertEqual(engine._runtime_data_path("/data/certs/missing.pem"),
                             "/data/certs/missing.pem")

    def test_normal_docker_calls_reuse_one_client(self):
        engine = self.engine_module()
        client = SimpleNamespace(close=lambda: None)
        with patch.object(engine.docker, "from_env", return_value=client) as factory:
            self.assertIs(engine._client(), client)
            self.assertIs(engine._client(), client)
            factory.assert_called_once_with(timeout=30)
            engine.close_client()

    def test_ami_debug_port_is_published_on_loopback_only(self):
        """The optional AMI diagnostic port must never reach the LAN."""
        engine = self.engine_module()
        captured = {}

        class _Containers:
            def get(self, name):
                raise engine.docker.errors.NotFound(name)

            def run(self, image, **kwargs):
                captured.update(kwargs)
                return SimpleNamespace(id="container-id", name=kwargs.get("name", ""))

        client = SimpleNamespace(containers=_Containers(), images=admitted_images())
        inst = {"id": "sim1", "ports": {"sip_udp": 5060, "sip_tls": 5061, "webrtc": 8089,
                                        "ami": 5038, "rtp_start": 10000}}
        with tempfile.TemporaryDirectory() as temp, \
                patch.object(engine, "_client", lambda: client), \
                patch.object(engine, "_instance_paths", lambda iid: (temp, temp)), \
                patch.object(engine, "_clear_runtime_state", lambda base: None), \
                patch.object(engine.egress, "ensure_line", lambda i, s: None), \
                patch.object(engine.cfg, "write_instance_json", lambda i, s: None):
            engine.start(inst, {"debug": {"ami": True}})

        bindings = captured["ports"]
        self.assertEqual(bindings["5038/tcp"], ("127.0.0.1", 5038))
        self.assertEqual(captured["volumes"]["/etc/localtime"],
                         {"bind": "/etc/localtime", "mode": "ro"})
        # Asterisk WSS is reachable only through Control's authenticated/admission-fenced
        # loopback bridge. RTP remains reachable at the separately selected media address.
        self.assertEqual(bindings["8089/tcp"], ("127.0.0.1", 8089))
        self.assertNotIsInstance(bindings["10000/udp"], tuple)
        self.assertNotIn("5060/udp", bindings)
        self.assertNotIn("5061/tcp", bindings)

    def test_default_engine_has_no_host_ami_mapping_and_uses_configured_rtp_span(self):
        engine = self.engine_module()
        captured = {}

        class _Containers:
            def get(self, name):
                raise engine.docker.errors.NotFound(name)

            def run(self, image, **kwargs):
                captured.update(kwargs)
                return SimpleNamespace(id="container-id", name=kwargs.get("name", ""))

        client = SimpleNamespace(containers=_Containers(), images=admitted_images())
        inst = {"id": "sim1", "ports": {"sip_udp": 5060, "sip_tls": 5061,
                "webrtc": 8089, "ami": 5038, "rtp_start": 10000, "rtp_span": 12}}
        with tempfile.TemporaryDirectory() as temp, \
                patch.object(engine, "_client", lambda: client), \
                patch.object(engine, "_instance_paths", lambda iid: (temp, temp)), \
                patch.object(engine, "_clear_runtime_state", lambda base: None), \
                patch.object(engine.egress, "ensure_line", lambda i, s: None), \
                patch.object(engine.cfg, "write_instance_json", lambda i, s: None):
            engine.start(inst, {})

        bindings = captured["ports"]
        self.assertNotIn("5038/tcp", bindings)
        self.assertEqual(len([key for key in bindings if key.endswith("/udp")]), 12)
        self.assertIn("10011/udp", bindings)
        self.assertNotIn("10012/udp", bindings)

    def test_country_tun_mtu_is_passed_to_swu_without_changing_line_country(self):
        engine = self.engine_module()
        captured = {}

        class _Containers:
            def get(self, name):
                raise engine.docker.errors.NotFound(name)

            def run(self, image, **kwargs):
                captured.update(kwargs)
                return SimpleNamespace(id="container-id", name=kwargs.get("name", ""))

        client = SimpleNamespace(containers=_Containers(), images=admitted_images())
        # A French SIM deliberately roaming over the configured GB exit must consume the GB
        # TUN state; Engine startup must not reinterpret its MCC or change proxy_country.
        inst = {"id": "fr", "mcc": "208", "mnc": "15", "proxy_country": "gb",
                "ports": {"webrtc": 8089, "rtp_start": 10000, "rtp_span": 1}}
        settings = {"proxy": {"enabled": True}}
        state = {"ready": True, "mode": "manual", "interface": "mdd-gb",
                 "outer_mtu": 1280}
        with tempfile.TemporaryDirectory() as temp, \
                patch.object(engine, "_client", lambda: client), \
                patch.object(engine, "_instance_paths", lambda iid: (temp, temp)), \
                patch.object(engine, "_clear_runtime_state", lambda base: None), \
                patch.object(engine.egress, "ensure_line", return_value=state) as ensure, \
                patch.object(engine.cfg, "write_instance_json", lambda i, s: None):
            engine.start(inst, settings)

        ensure.assert_called_once_with(inst, settings)
        self.assertEqual(inst["proxy_country"], "gb")
        self.assertEqual(captured["environment"]["SWU_OUTER_MTU"], "1280")

    def test_direct_engine_does_not_receive_country_tun_mtu(self):
        engine = self.engine_module()
        captured = {}

        class _Containers:
            def get(self, name):
                raise engine.docker.errors.NotFound(name)

            def run(self, image, **kwargs):
                captured.update(kwargs)
                return SimpleNamespace(id="container-id", name=kwargs.get("name", ""))

        client = SimpleNamespace(containers=_Containers(), images=admitted_images())
        inst = {"id": "direct", "ports": {"webrtc": 8089, "rtp_start": 10000,
                                             "rtp_span": 1}}
        settings = {"proxy": {"enabled": True}}
        with tempfile.TemporaryDirectory() as temp, \
                patch.object(engine, "_client", lambda: client), \
                patch.object(engine, "_instance_paths", lambda iid: (temp, temp)), \
                patch.object(engine, "_clear_runtime_state", lambda base: None), \
                patch.object(engine.egress, "ensure_line", return_value={
                    "ready": True, "mode": "direct"}), \
                patch.object(engine.cfg, "write_instance_json", lambda i, s: None):
            engine.start(inst, settings)
        self.assertNotIn("SWU_OUTER_MTU", captured["environment"])

    def test_proxy_engine_fails_closed_without_authoritative_mtu(self):
        engine = self.engine_module()
        inst = {"id": "gb"}
        settings = {"proxy": {"enabled": True}}
        client = SimpleNamespace(containers=SimpleNamespace(), images=admitted_images())
        with patch.object(engine, "_client", return_value=client), \
                patch.object(engine.egress, "ensure_line", return_value={
                "ready": True, "mode": "manual", "interface": "mdd-gb"}):
            with self.assertRaisesRegex(engine.egress.EgressError,
                                       "authoritative outer MTU"):
                engine.start(inst, settings)

    def test_wrong_admission_abi_rejects_before_every_mutation(self):
        engine = self.engine_module()
        inspected = SimpleNamespace(
            id="sha256:" + "4" * 64,
            attrs={"Config": {"Labels": {
                engine.ENGINE_ADMISSION_ABI_LABEL: "old-abi",
            }}},
        )
        client = SimpleNamespace(
            images=SimpleNamespace(get=lambda _image: inspected),
            containers=SimpleNamespace(get=MagicMock()),
        )
        inst = {"id": "7"}
        with patch.object(engine, "_client", return_value=client), \
                patch.object(engine.egress, "ensure_line") as ensure, \
                patch.object(engine.cfg, "write_instance_json") as write, \
                patch.object(engine, "_clear_runtime_state") as clear, \
                self.assertRaises(engine.EngineAdmissionABIError):
            engine.start(inst, {})
        ensure.assert_not_called()
        write.assert_not_called()
        client.containers.get.assert_not_called()
        clear.assert_not_called()

    def test_verified_tag_is_created_from_inspected_canonical_image_id(self):
        engine = self.engine_module()
        captured = {}
        canonical = "sha256:" + "5" * 64

        class _Containers:
            def get(self, name):
                raise engine.docker.errors.NotFound(name)

            def run(self, image, **kwargs):
                captured["image"] = image
                return SimpleNamespace(id="container-id", name=kwargs.get("name", ""))

        client = SimpleNamespace(containers=_Containers(), images=admitted_images(canonical))
        inst = {"id": "sim1", "ports": {
            "webrtc": 8089, "rtp_start": 10000, "rtp_span": 1}}
        with tempfile.TemporaryDirectory() as temp, \
                patch.object(engine, "_client", return_value=client), \
                patch.object(engine, "_instance_paths", return_value=(temp, temp)), \
                patch.object(engine, "_clear_runtime_state"), \
                patch.object(engine.egress, "ensure_line", return_value=None), \
                patch.object(engine.cfg, "write_instance_json"):
            engine.start(inst, {})
        self.assertEqual(captured["image"], canonical)

    def test_immutable_image_request_must_match_inspected_id(self):
        engine = self.engine_module()
        requested = "sha256:" + "6" * 64
        client = SimpleNamespace(images=admitted_images("sha256:" + "7" * 64))
        with self.assertRaisesRegex(engine.EngineAdmissionABIError, "different image ID"):
            engine._require_engine_admission_abi(client, requested)

    def test_container_runtime_reports_generation_and_actual_webrtc_binding(self):
        engine = self.engine_module()
        rtp_ports = {f"{port}/udp": [{"HostIp": "0.0.0.0", "HostPort": str(port)}]
                     for port in range(12000, 12004)}
        container = SimpleNamespace(
            status="running", id="generation-1",
            attrs={"State": {"StartedAt": "2026-08-22T12:00:00Z"}, "NetworkSettings": {
                "Networks": {"mdd": {"IPAddress": "172.18.0.5"}},
                "Ports": {"8089/tcp": [{"HostIp": "0.0.0.0", "HostPort": "8159"}],
                          **rtp_ports},
            }},
        )
        client = SimpleNamespace(containers=SimpleNamespace(get=lambda name: container))
        with patch.object(engine, "_client", lambda: client), \
                patch.object(engine.cfg, "get_instance", return_value={
                    "ports": {"rtp_start": 12000, "rtp_span": 4}}):
            runtime = engine.container_runtime("7")
        self.assertEqual(runtime, {
            "running": True, "ip": "172.18.0.5", "container_id": "generation-1",
            "webrtc_host_port": 8159,
            "rtp_mapping_exact": True,
            "started_at_epoch": 1787400000.0,
            "started_at": "2026-08-22T12:00:00Z",
            "restart_policy": "no",
            "engine_run_id": "",
        })

        container.attrs["NetworkSettings"]["Ports"].pop("12003/udp")
        with patch.object(engine, "_client", lambda: client), \
                patch.object(engine.cfg, "get_instance", return_value={
                    "ports": {"rtp_start": 12000, "rtp_span": 4}}):
            self.assertFalse(engine.container_runtime("7")["rtp_mapping_exact"])

    def test_engine_recreation_clears_stale_runtime_observations(self):
        engine = self.engine_module()
        with tempfile.TemporaryDirectory() as temp:
            run = Path(temp) / "run"
            run.mkdir()
            stale = ["swu_status.json", "pcscf", "pcscf-discovery.json",
                     "pcscf-rebind.json", "engine-run-id", "pin_status.json",
                     "usim_status.json", "registration_evidence.json",
                     "registration_evidence.abc.json"]
            for name in stale:
                (run / name).write_text("old")
            (run / "charon.log").write_text("keep diagnostics")

            engine._clear_runtime_state(temp)

            self.assertTrue(all(not (run / name).exists() for name in stale))
            self.assertEqual((run / "charon.log").read_text(), "keep diagnostics")

    def test_registration_evidence_is_generation_scoped(self):
        engine = self.engine_module()
        with tempfile.TemporaryDirectory() as temp, patch.object(engine, "DATA_DIR", temp):
            old = {"generation": "old-generation", "incarnation": "1000.000000",
                   "kind": "unanswered", "observed_at": 1001.0}
            new = {"generation": "new-generation", "incarnation": "2000.000000",
                   "kind": "registered", "observed_at": 2001.0}
            engine.write_registration_evidence("1", new)
            # A delayed old status sample writes afterwards, but cannot overwrite the new file.
            engine.write_registration_evidence("1", old)
            self.assertEqual(engine.read_registration_evidence(
                "1", "new-generation", "2000.000000"), new)
            self.assertEqual(engine.read_registration_evidence(
                "1", "old-generation", "1000.000000"), old)

    def test_registration_evidence_is_asterisk_incarnation_scoped(self):
        engine = self.engine_module()
        with tempfile.TemporaryDirectory() as temp, patch.object(engine, "DATA_DIR", temp):
            first = {"generation": "same-container", "incarnation": "1000.000000",
                     "kind": "unanswered", "observed_at": 1001.0}
            second = {"generation": "same-container", "incarnation": "2000.000000",
                      "kind": "registered", "observed_at": 2001.0}
            self.assertTrue(engine.write_registration_evidence("1", first))
            self.assertTrue(engine.write_registration_evidence("1", second))
            self.assertEqual(engine.read_registration_evidence(
                "1", "same-container", "1000.000000"), first)
            self.assertEqual(engine.read_registration_evidence(
                "1", "same-container", "2000.000000"), second)

    def test_older_failure_cannot_replace_registered_tombstone(self):
        engine = self.engine_module()
        with tempfile.TemporaryDirectory() as temp, patch.object(engine, "DATA_DIR", temp):
            owner = {"generation": "generation-1", "incarnation": "1000.000000"}
            registered = {**owner, "kind": "registered", "observed_at": 2000.0}
            stale = {**owner, "kind": "unanswered", "observed_at": 1900.0}
            self.assertTrue(engine.write_registration_evidence("1", registered))
            self.assertFalse(engine.write_registration_evidence("1", stale))
            self.assertEqual(engine.read_registration_evidence(
                "1", "generation-1", "1000.000000"), registered)

    def test_docker_log_event_time_is_rendered_in_local_ike_format(self):
        engine = self.engine_module()
        raw = ("2026-08-07T03:17:18.123456789Z [Aug  7 11:17:18] "
               "REGISTER sip:ims.example SIP/2.0\n"
               "2026-08-07T03:17:19Z Via: SIP/2.0/TCP example\n")
        rendered = engine._format_docker_logs(raw, timezone(timedelta(hours=8)))
        self.assertEqual(rendered,
                         "[2026-08-07 11:17:18+0800] REGISTER sip:ims.example SIP/2.0\n"
                         "[2026-08-07 11:17:19+0800] Via: SIP/2.0/TCP example\n")

    def test_engine_log_read_requests_docker_source_timestamps(self):
        engine = self.engine_module()
        captured = {}

        class _Container:
            def logs(self, **kwargs):
                captured.update(kwargs)
                return b"2026-08-07T03:17:18Z Asterisk Ready.\n"

        client = SimpleNamespace(containers=SimpleNamespace(
            get=lambda name: _Container()))
        with patch.object(engine, "_client", lambda: client):
            rendered = engine.logs("1", 25, since=123)

        self.assertEqual(captured, {"tail": 25, "timestamps": True, "since": 123})
        self.assertRegex(rendered,
                         r"^\[2026-08-07 \d{2}:17:18[+-]\d{4}\] Asterisk Ready\.\n$")


class DiagnosticCaptureTests(unittest.TestCase):
    """A line stuck rebuilding destroys its own evidence every couple of minutes."""

    @staticmethod
    def _instance_dir(temp):
        base = Path(temp)
        (base / "run").mkdir()
        (base / "logs").mkdir()
        return base

    def test_snapshot_records_registration_tunnel_and_exit_evidence(self):
        engine = EnginePathTests.engine_module()
        with tempfile.TemporaryDirectory() as temp:
            base = self._instance_dir(temp)
            (base / "run" / "charon.log").write_text(
                "sending IKE_SA_INIT\n"
                "[swu_ike] IKE request retransmit 2/3 (message_id=0, same bytes)\n"
                "[swu_ike] IKE request retransmit 3/3 (message_id=0, same bytes)\n"
                "TIMEOUT : TIMEOUT\n"
                "[2026-08-07 11:17:18+0800] STATE 2:\n")
            engine_log = ("Asterisk ready, triggering registration...\n"
                          "unrelated module chatter\n"
                          "res_pjsip: SIP/2.0 403 Forbidden\n")
            with patch.object(engine, "DATA_DIR", temp), \
                    patch.object(engine, "registration_state", lambda iid: "Unregistered"), \
                    patch.object(engine, "read_run_json", lambda iid, name: {"state": "CONNECTED"}), \
                    patch.object(engine, "read_pcscf", lambda iid: "fd00::5"), \
                    patch.object(engine, "logs", lambda iid, tail: engine_log), \
                    patch.object(engine.egress, "line_country", lambda inst: "us"), \
                    patch.object(engine.egress, "status", lambda: {"exits": {"us": {
                        "node": "US Bravo", "selection": "manual", "candidate_count": 11,
                        "ready": True}}}):
                engine.capture_diagnostics("1", {"mcc": "310"}, str(base), "auto-recover:ims")

            records = [json.loads(line) for line
                       in (base / "logs" / "diagnostics.jsonl").read_text().splitlines() if line]
            self.assertEqual(len(records), 1)
            record = records[0]
            self.assertEqual(record["reason"], "auto-recover:ims")
            self.assertEqual(record["registration"], "Unregistered")
            # Lossy-exit signature: what distinguishes a bad node from a carrier rejection.
            self.assertEqual(record["charon"]["retransmits"], 2)
            self.assertEqual(record["charon"]["timeouts"], 1)
            self.assertEqual(record["charon"]["last_state"],
                             "[2026-08-07 11:17:18+0800] STATE 2:")
            self.assertEqual(record["egress"]["node"], "US Bravo")
            self.assertIn("SIP/2.0 403 Forbidden", "\n".join(record["sip"]))
            self.assertNotIn("unrelated module chatter", "\n".join(record["sip"]))

    def test_idle_stop_rechecks_same_generation_immediately_before_remove(self):
        engine = EnginePathTests.engine_module()

        class _Container:
            id = "generation-1"
            status = "running"
            attrs = {"Config": {"Labels": {engine.MANAGED_LABEL: "true"},
                                "Image": "mdd-sim-gateway/engine"},
                     "HostConfig": {"RestartPolicy": {"Name": "unless-stopped"}},
                     "State": {"Pid": 101, "StartedAt": "2026-08-22T00:00:00Z"},
                     "RestartCount": 0}

            def exec_run(self, command):
                if command[-1] == "core stop gracefully":
                    self.status = "exited"
                return 0, b"0 active channels\n0 active calls\n"

            def reload(self):
                return None

            def top(self):
                return {"Processes": []}

            def stop(self, timeout=0):
                self.stops = getattr(self, "stops", []) + [timeout]

            def remove(self, force=False):
                self.removed = True
                self.remove_force = force

            def update(self, **kwargs):
                self.updated = getattr(self, "updated", []) + [kwargs]

        container = _Container()
        container.removed = False
        client = SimpleNamespace(
            containers=SimpleNamespace(get=lambda _name: container), close=lambda: None)
        with patch.object(engine.docker, "from_env", return_value=client), \
                patch.object(engine, "capture_diagnostics") as capture:
            result = engine.capture_and_stop_if_idle(
                "1", {"id": "1"}, "health-freeze:registering", "generation-1")

        self.assertEqual(result["status"], "stopped")
        self.assertTrue(container.removed)
        self.assertFalse(container.remove_force)
        self.assertEqual(container.updated, [{"restart_policy": {"Name": "no"}}])
        capture.assert_called_once()

    def test_running_container_that_does_not_stop_is_bounded_and_never_removed(self):
        engine = EnginePathTests.engine_module()

        class _Container:
            id = "generation-1"
            status = "running"
            attrs = {"Config": {"Labels": {engine.MANAGED_LABEL: "true"},
                                "Image": "mdd-sim-gateway/engine"},
                     "HostConfig": {"RestartPolicy": {"Name": "unless-stopped"}},
                     "State": {"Pid": 101, "StartedAt": "2026-08-22T00:00:00Z"},
                     "RestartCount": 0}

            def __init__(self):
                self.removes = 0
                self.updates = []

            def exec_run(self, _command):
                return 0, b"0 active channels\n0 active calls\n"

            def reload(self):
                return None

            def top(self):
                return {"Processes": []}

            def remove(self, force=False):
                self.removes += 1
                raise AssertionError("running container must never be removed")

            def update(self, **kwargs):
                name = kwargs["restart_policy"]["Name"]
                self.updates.append(name)
                self.attrs["HostConfig"]["RestartPolicy"] = {"Name": name}

        container = _Container()
        client = SimpleNamespace(
            containers=SimpleNamespace(get=lambda _name: container), close=lambda: None)
        with patch.object(engine.docker, "from_env", return_value=client), \
                patch.object(engine, "capture_diagnostics"), \
                patch.object(engine.time, "sleep"):
            result = engine.capture_and_stop_if_idle(
                "1", {"id": "1"}, "health-freeze:registering", "generation-1")
        self.assertEqual(result["status"], "quiesce_restart_race")
        self.assertFalse(result["stopped"])
        self.assertEqual(container.removes, 0)
        self.assertEqual(container.updates, ["no", "unless-stopped"])

    def test_idle_stop_fails_closed_when_call_appears_after_capture(self):
        engine = EnginePathTests.engine_module()

        class _Container:
            id = "generation-1"
            status = "running"
            attrs = {"Config": {"Labels": {engine.MANAGED_LABEL: "true"},
                                "Image": "mdd-sim-gateway/engine"},
                     "HostConfig": {"RestartPolicy": {"Name": "unless-stopped"}},
                     "State": {"Pid": 101, "StartedAt": "2026-08-22T00:00:00Z"},
                     "RestartCount": 0}
            removed = False

            def exec_run(self, _command):
                return 0, b"2 active channels\n1 active call\n"

            def reload(self):
                return None

            def remove(self, force=False):
                self.removed = True
                self.remove_force = force

            def update(self, **kwargs):
                self.updates = getattr(self, "updates", []) + [kwargs]
                self.attrs["HostConfig"]["RestartPolicy"] = dict(
                    kwargs["restart_policy"])
                self.attrs["HostConfig"]["RestartPolicy"] = dict(
                    kwargs["restart_policy"])
                self.attrs["HostConfig"]["RestartPolicy"] = dict(
                    kwargs["restart_policy"])

        container = _Container()
        client = SimpleNamespace(
            containers=SimpleNamespace(get=lambda _name: container), close=lambda: None)
        with patch.object(engine.docker, "from_env", return_value=client), \
                patch.object(engine, "capture_diagnostics"):
            result = engine.capture_and_stop_if_idle(
                "1", {"id": "1"}, "health-freeze:registering", "generation-1")

        self.assertEqual(result["status"], "active_call")
        self.assertFalse(container.removed)

    def test_manual_stop_that_never_reaches_terminal_is_bounded(self):
        engine = EnginePathTests.engine_module()

        class _Container:
            id = "generation-1"
            status = "running"
            attrs = {"Config": {"Labels": {engine.MANAGED_LABEL: "true"},
                                "Image": "mdd-sim-gateway/engine"},
                     "HostConfig": {"RestartPolicy": {"Name": "unless-stopped"}},
                     "State": {"Pid": 101, "StartedAt": "2026-08-22T00:00:00Z"},
                     "RestartCount": 0}
            removed = False

            def __init__(self):
                self.commands = []

            def exec_run(self, command):
                self.commands.append(command[-1])
                return 0, b"0 active channels\n0 active calls\n"

            def reload(self):
                return None

            def top(self):
                return {"Processes": [["1", "asterisk", "/usr/sbin/asterisk"]]}

            def stop(self, timeout=0):
                self.stops = getattr(self, "stops", []) + [timeout]

            def remove(self, force=False):
                self.removed = force

            def update(self, **kwargs):
                self.updates = getattr(self, "updates", []) + [kwargs]
                self.attrs["HostConfig"]["RestartPolicy"] = dict(
                    kwargs["restart_policy"])

        container = _Container()
        client = SimpleNamespace(
            containers=SimpleNamespace(get=lambda _name: container), close=lambda: None)
        with patch.object(engine.docker, "from_env", return_value=client), \
                patch.object(engine, "capture_diagnostics"), \
                patch.object(engine.time, "sleep"):
            result = engine.capture_and_stop_if_idle(
                "1", {"id": "1"}, "health-freeze:registering", "generation-1")

        self.assertEqual(result["status"], "quiesce_restart_race")
        self.assertIn("core stop gracefully", container.commands)
        self.assertIn("core abort shutdown", container.commands)
        self.assertNotIn("core stop now", container.commands)
        self.assertFalse(container.removed)
        self.assertEqual(container.updates, [
            {"restart_policy": {"Name": "no"}},
            {"restart_policy": {"Name": "unless-stopped"}},
        ])
        self.assertEqual(container.stops, [2, 2])

    def test_raced_active_call_aborts_graceful_and_is_never_removed(self):
        engine = EnginePathTests.engine_module()

        class _Container:
            id = "generation-1"
            status = "running"
            attrs = {"Config": {"Labels": {engine.MANAGED_LABEL: "true"},
                                "Image": "mdd-sim-gateway/engine"},
                     "HostConfig": {"RestartPolicy": {"Name": "unless-stopped"}},
                     "State": {"Pid": 101, "StartedAt": "2026-08-22T00:00:00Z"},
                     "RestartCount": 0}

            def __init__(self):
                self.channel_reads = 0
                self.commands = []
                self.removes = 0

            def exec_run(self, command):
                command = command[-1]
                self.commands.append(command)
                if command == "core show channels count":
                    self.channel_reads += 1
                    active = 0 if self.channel_reads == 1 else 1
                    return 0, f"{active} active channels\n{active} active calls\n".encode()
                return 0, b""

            def reload(self):
                return None

            def top(self):
                return {"Processes": [["1", "asterisk"]]}

            def remove(self, force=False):
                self.removes += 1

            def update(self, **_kwargs):
                return None

        container = _Container()
        client = SimpleNamespace(
            containers=SimpleNamespace(get=lambda _name: container), close=lambda: None)
        with patch.object(engine.docker, "from_env", return_value=client), \
                patch.object(engine, "capture_diagnostics"), \
                patch.object(engine.time, "sleep"):
            result = engine.capture_and_stop_if_idle(
                "1", {"id": "1"}, "health-freeze:registering", "generation-1")
        self.assertEqual(result["status"], "active_call")
        self.assertEqual(result["active_channels"], 1)
        self.assertIn("core abort shutdown", container.commands)
        self.assertNotIn("core stop now", container.commands)
        self.assertEqual(container.removes, 0)

    def test_transient_unknown_during_graceful_waits_for_terminal_without_abort(self):
        engine = EnginePathTests.engine_module()

        class _Container:
            id = "generation-1"
            status = "running"
            attrs = {"Config": {"Labels": {engine.MANAGED_LABEL: "true"},
                                "Image": "mdd-sim-gateway/engine"},
                     "HostConfig": {"RestartPolicy": {"Name": "unless-stopped"}},
                     "State": {"Pid": 101, "StartedAt": "2026-08-22T00:00:00Z"},
                     "RestartCount": 0}

            def __init__(self):
                self.channel_reads = 0
                self.graceful = False
                self.graceful_reloads = 0
                self.commands = []
                self.removes = []

            def exec_run(self, command):
                command = command[-1]
                self.commands.append(command)
                if command == "core stop gracefully":
                    self.graceful = True
                    return 0, b""
                if command == "core show channels count":
                    self.channel_reads += 1
                    if self.channel_reads == 2:
                        return 1, b"CLI is exiting"
                    return 0, b"0 active channels\n0 active calls\n"
                return 0, b""

            def reload(self):
                if self.graceful:
                    self.graceful_reloads += 1
                    if self.graceful_reloads >= 2:
                        self.status = "exited"

            def top(self):
                return {"Processes": [["1", "asterisk"]]}

            def remove(self, force=False):
                self.removes.append(force)

            def update(self, **_kwargs):
                return None

        container = _Container()
        client = SimpleNamespace(
            containers=SimpleNamespace(get=lambda _name: container), close=lambda: None)
        with patch.object(engine.docker, "from_env", return_value=client), \
                patch.object(engine, "capture_diagnostics"), \
                patch.object(engine.time, "sleep"):
            result = engine.capture_and_stop_if_idle(
                "1", {"id": "1"}, "health-freeze:registering", "generation-1")
        self.assertEqual(result["status"], "stopped")
        self.assertNotIn("core abort shutdown", container.commands)
        self.assertEqual(container.removes, [False])

    def test_active_abort_delayed_terminal_is_removed_before_policy_restore(self):
        engine = EnginePathTests.engine_module()

        class _Container:
            id = "generation-1"
            status = "running"
            attrs = {"Config": {"Labels": {engine.MANAGED_LABEL: "true"},
                                "Image": "mdd-sim-gateway/engine"},
                     "HostConfig": {"RestartPolicy": {"Name": "unless-stopped"}},
                     "State": {"Pid": 101, "StartedAt": "2026-08-22T00:00:00Z"},
                     "RestartCount": 0}

            def __init__(self):
                self.channel_reads = 0
                self.aborted = False
                self.abort_reloads = 0
                self.commands = []
                self.updates = []
                self.removes = []

            def exec_run(self, command):
                command = command[-1]
                self.commands.append(command)
                if command == "core show channels count":
                    self.channel_reads += 1
                    active = 0 if self.channel_reads == 1 else 1
                    return 0, f"{active} active channels\n{active} active calls\n".encode()
                if command == "core abort shutdown":
                    self.aborted = True
                return 0, b""

            def reload(self):
                if self.aborted:
                    self.abort_reloads += 1
                    if self.abort_reloads >= 3:
                        self.status = "exited"

            def top(self):
                return {"Processes": [["1", "asterisk"]]}

            def remove(self, force=False):
                self.removes.append(force)

            def update(self, **kwargs):
                name = kwargs["restart_policy"]["Name"]
                self.updates.append(name)
                self.attrs["HostConfig"]["RestartPolicy"] = {"Name": name}

        container = _Container()
        client = SimpleNamespace(
            containers=SimpleNamespace(get=lambda _name: container), close=lambda: None)
        with patch.object(engine.docker, "from_env", return_value=client), \
                patch.object(engine, "capture_diagnostics"), \
                patch.object(engine.time, "sleep"):
            result = engine.capture_and_stop_if_idle(
                "1", {"id": "1"}, "health-freeze:registering", "generation-1")
        self.assertEqual(result["status"], "stopped")
        self.assertIn("core abort shutdown", container.commands)
        self.assertEqual(container.removes, [False])
        self.assertEqual(container.updates, ["no"])

    def test_same_container_new_asterisk_incarnation_restarts_whole_transaction_once(self):
        engine = EnginePathTests.engine_module()

        class _Container:
            id = "generation-1"
            status = "running"
            attrs = {
                "Config": {"Labels": {engine.MANAGED_LABEL: "true"},
                           "Image": "mdd-sim-gateway/engine"},
                "HostConfig": {"RestartPolicy": {"Name": "unless-stopped"}},
                "State": {"Pid": 101, "StartedAt": "first"},
                "RestartCount": 0,
            }

            def __init__(self):
                self.graceful = 0
                self.stops = []
                self.removes = []

            def exec_run(self, command):
                command = command[-1]
                if command == "core stop gracefully":
                    self.graceful += 1
                    if self.graceful == 1:
                        self.attrs["State"] = {"Pid": 202, "StartedAt": "second"}
                        self.attrs["RestartCount"] = 1
                    return 0, b""
                return 0, b"0 active channels\n0 active calls\n"

            def reload(self):
                return None

            def update(self, **_kwargs):
                return None

            def stop(self, timeout=0):
                self.stops.append(timeout)
                self.status = "exited"

            def remove(self, force=False):
                self.removes.append(force)

        container = _Container()
        client = SimpleNamespace(
            containers=SimpleNamespace(get=lambda _name: container), close=lambda: None)
        with patch.object(engine.docker, "from_env", return_value=client), \
                patch.object(engine, "capture_diagnostics"), \
                patch.object(engine.time, "sleep"):
            result = engine.capture_and_stop_if_idle(
                "1", {"id": "1"}, "health-freeze:registering", "generation-1")
        self.assertEqual(result["status"], "stopped")
        self.assertEqual(container.graceful, 2)
        self.assertEqual(container.stops, [2])
        self.assertEqual(container.removes, [False])

    def test_second_incarnation_change_restores_policy_without_touching_new_pid(self):
        engine = EnginePathTests.engine_module()

        class _Container:
            id = "generation-1"
            status = "running"

            def __init__(self):
                self.attrs = {
                    "Config": {"Labels": {engine.MANAGED_LABEL: "true"},
                               "Image": "mdd-sim-gateway/engine"},
                    "HostConfig": {"RestartPolicy": {"Name": "unless-stopped"}},
                    "State": {"Pid": 101, "StartedAt": "first"},
                    "RestartCount": 0,
                }
                self.graceful = 0
                self.commands = []
                self.stops = 0
                self.updates = []

            def exec_run(self, command):
                command = command[-1]
                self.commands.append(command)
                if command == "core stop gracefully":
                    self.graceful += 1
                    next_pid = 101 + self.graceful
                    self.attrs["State"] = {
                        "Pid": next_pid, "StartedAt": f"start-{next_pid}"}
                    self.attrs["RestartCount"] = self.graceful
                    return 0, b""
                return 0, b"0 active channels\n0 active calls\n"

            def reload(self):
                return None

            def update(self, **kwargs):
                name = kwargs["restart_policy"]["Name"]
                self.updates.append(name)
                self.attrs["HostConfig"]["RestartPolicy"] = {"Name": name}

            def stop(self, timeout=0):
                self.stops += 1

        container = _Container()
        client = SimpleNamespace(
            containers=SimpleNamespace(get=lambda _name: container), close=lambda: None)
        with patch.object(engine.docker, "from_env", return_value=client), \
                patch.object(engine, "capture_diagnostics"), \
                patch.object(engine.time, "sleep"):
            result = engine.capture_and_stop_if_idle(
                "1", {"id": "1"}, "health-freeze:registering", "generation-1")
        self.assertEqual(result["status"], "quiesce_restart_race")
        self.assertEqual(container.graceful, 2)
        self.assertEqual(container.stops, 0)
        self.assertNotIn("core abort shutdown", container.commands)
        self.assertEqual(container.updates, ["no", "unless-stopped"])

    def test_unknown_second_channel_sample_never_reaches_manual_stop(self):
        engine = EnginePathTests.engine_module()

        class _Container:
            id = "generation-1"
            status = "running"
            attrs = {
                "Config": {"Labels": {engine.MANAGED_LABEL: "true"},
                           "Image": "mdd-sim-gateway/engine"},
                "HostConfig": {"RestartPolicy": {"Name": "unless-stopped"}},
                "State": {"Pid": 101, "StartedAt": "first"},
                "RestartCount": 0,
            }

            def __init__(self):
                self.channel_reads = 0
                self.stops = 0
                self.updates = []

            def exec_run(self, command):
                command = command[-1]
                if command == "core show channels count":
                    self.channel_reads += 1
                    if self.channel_reads == 2:
                        return 1, b"CLI unavailable"
                    return 0, b"0 active channels\n0 active calls\n"
                return 0, b""

            def reload(self):
                return None

            def update(self, **kwargs):
                name = kwargs["restart_policy"]["Name"]
                self.updates.append(name)
                self.attrs["HostConfig"]["RestartPolicy"] = {"Name": name}

            def stop(self, timeout=0):
                self.stops += 1

        container = _Container()
        client = SimpleNamespace(
            containers=SimpleNamespace(get=lambda _name: container), close=lambda: None)
        with patch.object(engine.docker, "from_env", return_value=client), \
                patch.object(engine, "capture_diagnostics"), \
                patch.object(engine.time, "sleep"):
            result = engine.capture_and_stop_if_idle(
                "1", {"id": "1"}, "health-freeze:registering", "generation-1")
        self.assertEqual(result["status"], "call_state_unknown")
        self.assertEqual(container.stops, 0)
        self.assertEqual(container.updates, ["no", "unless-stopped"])

    def test_transient_inspect_after_stop_cleans_up_same_or_new_incarnation(self):
        engine = EnginePathTests.engine_module()

        for new_incarnation in (False, True):
            with self.subTest(new_incarnation=new_incarnation):
                class _Container:
                    id = "generation-1"
                    status = "running"

                    def __init__(self):
                        self.attrs = {
                            "Config": {"Labels": {engine.MANAGED_LABEL: "true"},
                                       "Image": "mdd-sim-gateway/engine"},
                            "HostConfig": {"RestartPolicy": {"Name": "unless-stopped"}},
                            "State": {"Pid": 101, "StartedAt": "first"},
                            "RestartCount": 0,
                        }
                        self.after_stop = False
                        self.failed_reload = False
                        self.changed_token = False
                        self.commands = []
                        self.updates = []

                    def exec_run(self, command):
                        command = command[-1]
                        self.commands.append(command)
                        return 0, b"0 active channels\n0 active calls\n"

                    def reload(self):
                        if self.after_stop and not self.failed_reload:
                            self.failed_reload = True
                            raise RuntimeError("transient inspect failure")
                        if (self.after_stop and new_incarnation and
                                self.failed_reload and not self.changed_token):
                            self.changed_token = True
                            self.attrs["State"] = {"Pid": 202, "StartedAt": "second"}
                            self.attrs["RestartCount"] = 1

                    def update(self, **kwargs):
                        name = kwargs["restart_policy"]["Name"]
                        self.updates.append(name)
                        self.attrs["HostConfig"]["RestartPolicy"] = {"Name": name}

                    def stop(self, timeout=0):
                        self.after_stop = True

                container = _Container()
                client = SimpleNamespace(
                    containers=SimpleNamespace(get=lambda _name: container),
                    close=lambda: None)
                with patch.object(engine.docker, "from_env", return_value=client), \
                        patch.object(engine, "capture_diagnostics"), \
                        patch.object(engine.time, "sleep"):
                    result = engine.capture_and_stop_if_idle(
                        "1", {"id": "1"}, "health-freeze:registering",
                        "generation-1")
                self.assertFalse(result["stopped"])
                self.assertEqual(container.updates, ["no", "unless-stopped"])
                if new_incarnation:
                    self.assertNotIn("core abort shutdown", container.commands)
                else:
                    self.assertIn("core abort shutdown", container.commands)

    def test_restore_policy_response_loss_is_recognized_as_success(self):
        engine = EnginePathTests.engine_module()

        class _Container:
            id = "generation-1"
            status = "running"

            def __init__(self):
                self.attrs = {
                    "Config": {"Labels": {engine.MANAGED_LABEL: "true"},
                               "Image": "mdd-sim-gateway/engine"},
                    "HostConfig": {"RestartPolicy": {"Name": "unless-stopped"}},
                    "State": {"Pid": 101, "StartedAt": "first"},
                    "RestartCount": 0,
                }
                self.updates = []

            def exec_run(self, command):
                return 0, b"1 active channels\n1 active call\n"

            def reload(self):
                return None

            def update(self, **kwargs):
                name = kwargs["restart_policy"]["Name"]
                self.updates.append(name)
                self.attrs["HostConfig"]["RestartPolicy"] = dict(
                    kwargs["restart_policy"])
                if name == "unless-stopped":
                    raise RuntimeError("response lost after commit")

        container = _Container()
        client = SimpleNamespace(
            containers=SimpleNamespace(get=lambda _name: container), close=lambda: None)
        with patch.object(engine.docker, "from_env", return_value=client), \
                patch.object(engine, "capture_diagnostics"), \
                patch.object(engine.time, "sleep"):
            result = engine.capture_and_stop_if_idle(
                "1", {"id": "1"}, "health-freeze:registering", "generation-1")
        self.assertEqual(result["status"], "active_call")
        self.assertEqual(container.updates, ["no", "unless-stopped"])

    def test_graceful_barrier_never_removes_ambiguous_container_states(self):
        engine = EnginePathTests.engine_module()

        for ambiguous_status in ("paused", "restarting", "unknown", "created"):
            with self.subTest(status=ambiguous_status):
                class _Container:
                    id = "generation-1"
                    status = "running"
                    attrs = {
                        "Config": {"Labels": {engine.MANAGED_LABEL: "true"},
                                   "Image": "mdd-sim-gateway/engine"},
                        "HostConfig": {"RestartPolicy": {"Name": "unless-stopped"}},
                        "State": {"Pid": 101,
                                  "StartedAt": "2026-08-22T00:00:00Z"},
                        "RestartCount": 0,
                    }

                    def __init__(self):
                        self.removes = 0
                        self.updates = []

                    def exec_run(self, command):
                        if command[-1] == "core stop gracefully":
                            self.status = ambiguous_status
                        return 0, b"0 active channels\n0 active calls\n"

                    def reload(self):
                        return None

                    def top(self):
                        raise AssertionError("top must not run for an ambiguous state")

                    def remove(self, force=False):
                        self.removes += 1
                        raise AssertionError("ambiguous state must never be removed")

                    def update(self, **kwargs):
                        self.updates.append(kwargs["restart_policy"]["Name"])

                container = _Container()
                client = SimpleNamespace(
                    containers=SimpleNamespace(get=lambda _name: container),
                    close=lambda: None)
                with patch.object(engine.docker, "from_env", return_value=client), \
                        patch.object(engine, "capture_diagnostics"), \
                        patch.object(engine.time, "sleep"):
                    result = engine.capture_and_stop_if_idle(
                        "1", {"id": "1"}, "health-freeze:registering",
                        "generation-1")
                self.assertEqual(result["status"], "quiesce_state_unknown")
                self.assertFalse(result["stopped"])
                self.assertEqual(container.removes, 0)
                self.assertEqual(container.updates, ["no"])

    def test_nonzero_graceful_exec_still_finalizes_a_stopped_generation(self):
        engine = EnginePathTests.engine_module()

        class _Container:
            id = "generation-1"
            status = "running"
            attrs = {"Config": {"Labels": {engine.MANAGED_LABEL: "true"},
                                "Image": "mdd-sim-gateway/engine"},
                     "HostConfig": {"RestartPolicy": {"Name": "unless-stopped"}},
                     "State": {"Pid": 101, "StartedAt": "2026-08-22T00:00:00Z"},
                     "RestartCount": 0}
            removed = False

            def exec_run(self, command):
                if command[-1] == "core stop gracefully":
                    self.status = "exited"
                    return 1, b"remote CLI disconnected"
                return 0, b"0 active channels\n0 active calls\n"

            def reload(self):
                return None

            def update(self, **_kwargs):
                return None

            def remove(self, force=False):
                self.removed = True
                self.remove_force = force

        container = _Container()
        client = SimpleNamespace(
            containers=SimpleNamespace(get=lambda _name: container), close=lambda: None)
        with patch.object(engine.docker, "from_env", return_value=client), \
                patch.object(engine, "capture_diagnostics"):
            result = engine.capture_and_stop_if_idle(
                "1", {"id": "1"}, "health-freeze:registering", "generation-1")
        self.assertEqual(result["status"], "stopped")
        self.assertTrue(container.removed)
        self.assertFalse(container.remove_force)

    def test_restart_policy_restore_failure_is_reported(self):
        engine = EnginePathTests.engine_module()

        class _Container:
            id = "generation-1"
            status = "running"
            attrs = {"Config": {"Labels": {engine.MANAGED_LABEL: "true"},
                                "Image": "mdd-sim-gateway/engine"},
                     "HostConfig": {"RestartPolicy": {"Name": "unless-stopped"}},
                     "State": {"Pid": 101, "StartedAt": "2026-08-22T00:00:00Z"},
                     "RestartCount": 0}

            def __init__(self):
                self.update_count = 0

            def exec_run(self, command):
                if command[-1] == "core show channels count":
                    return 0, b"1 active channels\n1 active call\n"
                return 0, b""

            def reload(self):
                return None

            def top(self):
                return {"Processes": [["1", "asterisk"]]}

            def update(self, **kwargs):
                self.update_count += 1
                if self.update_count > 1:
                    raise RuntimeError("cannot restore")
                self.attrs["HostConfig"]["RestartPolicy"] = dict(
                    kwargs["restart_policy"])

        container = _Container()
        client = SimpleNamespace(
            containers=SimpleNamespace(get=lambda _name: container), close=lambda: None)
        with patch.object(engine.docker, "from_env", return_value=client), \
                patch.object(engine, "capture_diagnostics"), \
                patch.object(engine.time, "sleep"):
            result = engine.capture_and_stop_if_idle(
                "1", {"id": "1"}, "health-freeze:registering", "generation-1")
        self.assertEqual(result["status"], "restart_policy_restore_failed")
        self.assertFalse(result["stopped"])

    def test_initial_restart_policy_update_is_classified_on_the_exact_handle(self):
        engine = EnginePathTests.engine_module()

        for mode, expected in (("applied", "stopped"),
                               ("terminal", "stopped"),
                               ("not_applied", "restart_policy_disable_failed"),
                               ("missing", "missing")):
            with self.subTest(mode=mode):
                class _Container:
                    id = "generation-1"
                    status = "running"

                    def __init__(self):
                        self.attrs = {
                            "Config": {"Labels": {engine.MANAGED_LABEL: "true"},
                                       "Image": "mdd-sim-gateway/engine"},
                            "HostConfig": {"RestartPolicy": {"Name": "unless-stopped"}},
                            "State": {"Pid": 101, "StartedAt": "first"},
                            "RestartCount": 0,
                        }
                        self.update_failed = False
                        self.stops = 0
                        self.removes = []

                    def exec_run(self, command):
                        return 0, b"0 active channels\n0 active calls\n"

                    def reload(self):
                        if mode == "missing" and self.update_failed:
                            raise engine.docker.errors.NotFound("gone")

                    def update(self, **kwargs):
                        if kwargs["restart_policy"]["Name"] != "no":
                            return None
                        self.update_failed = True
                        if mode == "applied":
                            self.attrs["HostConfig"]["RestartPolicy"] = {"Name": "no"}
                        elif mode == "terminal":
                            self.status = "exited"
                        raise RuntimeError("ambiguous daemon response")

                    def stop(self, timeout=0):
                        self.stops += 1
                        self.status = "exited"

                    def remove(self, force=False):
                        self.removes.append(force)

                container = _Container()
                client = SimpleNamespace(
                    containers=SimpleNamespace(get=lambda _name: container),
                    close=lambda: None)
                with patch.object(engine.docker, "from_env", return_value=client), \
                        patch.object(engine, "capture_diagnostics"), \
                        patch.object(engine.time, "sleep"):
                    result = engine.capture_and_stop_if_idle(
                        "1", {"id": "1"}, "health-freeze:registering",
                        "generation-1")
                self.assertEqual(result["status"], expected)
                if expected == "stopped":
                    self.assertEqual(container.removes, [False])
                else:
                    self.assertEqual(container.removes, [])
                self.assertLessEqual(container.stops, 1)

    def test_initial_policy_update_terminal_restart_retries_disable_once(self):
        engine = EnginePathTests.engine_module()

        for applied_before_restart in (False, True):
            with self.subTest(applied_before_restart=applied_before_restart):
                class _Container:
                    id = "generation-1"
                    status = "running"

                    def __init__(self):
                        self.attrs = {
                            "Config": {"Labels": {engine.MANAGED_LABEL: "true"},
                                       "Image": "mdd-sim-gateway/engine"},
                            "HostConfig": {"RestartPolicy": {"Name": "unless-stopped"}},
                            "State": {"Pid": 101, "StartedAt": "first"},
                            "RestartCount": 0,
                        }
                        self.failed = False
                        self.terminal_reloads = 0
                        self.restarted_once = False
                        self.updates = []
                        self.removes = []

                    def exec_run(self, command):
                        return 0, b"0 active channels\n0 active calls\n"

                    def reload(self):
                        if self.failed and self.status == "exited" and not self.restarted_once:
                            self.terminal_reloads += 1
                            if self.terminal_reloads >= 2:
                                self.restarted_once = True
                                self.status = "running"
                                self.attrs["State"] = {"Pid": 202, "StartedAt": "second"}
                                self.attrs["RestartCount"] = 1

                    def update(self, **kwargs):
                        name = kwargs["restart_policy"]["Name"]
                        self.updates.append(name)
                        if name == "no" and not self.failed:
                            self.failed = True
                            self.status = "exited"
                            if applied_before_restart:
                                self.attrs["HostConfig"]["RestartPolicy"] = {"Name": "no"}
                            raise RuntimeError("response lost during exit")

                    def stop(self, timeout=0):
                        self.status = "exited"

                    def remove(self, force=False):
                        self.removes.append(force)

                container = _Container()
                client = SimpleNamespace(
                    containers=SimpleNamespace(get=lambda _name: container),
                    close=lambda: None)
                with patch.object(engine.docker, "from_env", return_value=client), \
                        patch.object(engine, "capture_diagnostics"), \
                        patch.object(engine.time, "sleep"):
                    result = engine.capture_and_stop_if_idle(
                        "1", {"id": "1"}, "health-freeze:registering",
                        "generation-1")
                self.assertEqual(result["status"], "stopped")
                expected_updates = ["no"] if applied_before_restart else ["no", "no"]
                self.assertEqual(container.updates, expected_updates)
                self.assertEqual(container.removes, [False])

    def test_restore_policy_race_finalizes_generation_that_just_exited(self):
        engine = EnginePathTests.engine_module()

        class _Container:
            id = "generation-1"
            status = "running"
            attrs = {"Config": {"Labels": {engine.MANAGED_LABEL: "true"},
                                "Image": "mdd-sim-gateway/engine"},
                     "HostConfig": {"RestartPolicy": {"Name": "unless-stopped"}},
                     "State": {"Pid": 101, "StartedAt": "2026-08-22T00:00:00Z"},
                     "RestartCount": 0}

            def __init__(self):
                self.lifecycle = []
                self.update_count = 0

            def exec_run(self, command):
                if command[-1] == "core show channels count":
                    return 0, b"1 active channels\n1 active call\n"
                return 0, b""

            def reload(self):
                return None

            def top(self):
                return {"Processes": [["1", "asterisk"]]}

            def update(self, **kwargs):
                policy = kwargs["restart_policy"]["Name"]
                self.lifecycle.append(("update", policy))
                self.update_count += 1
                if self.update_count == 2:
                    self.status = "exited"
                    raise RuntimeError("cannot update stopped container")
                self.attrs["HostConfig"]["RestartPolicy"] = dict(
                    kwargs["restart_policy"])

            def remove(self, force=False):
                self.lifecycle.append(("remove", force))

        container = _Container()
        client = SimpleNamespace(
            containers=SimpleNamespace(get=lambda _name: container), close=lambda: None)
        with patch.object(engine.docker, "from_env", return_value=client), \
                patch.object(engine, "capture_diagnostics"), \
                patch.object(engine.time, "sleep"):
            result = engine.capture_and_stop_if_idle(
                "1", {"id": "1"}, "health-freeze:registering", "generation-1")
        self.assertEqual(result, {
            "status": "stopped", "active_channels": 0, "stopped": True})
        self.assertEqual(container.lifecycle, [
            ("update", "no"), ("update", "unless-stopped"),
            ("remove", False)])

    def test_abort_boundary_exit_finalizes_without_restarting(self):
        engine = EnginePathTests.engine_module()

        class _Container:
            id = "generation-1"
            status = "running"
            attrs = {"Config": {"Labels": {engine.MANAGED_LABEL: "true"},
                                "Image": "mdd-sim-gateway/engine"},
                     "HostConfig": {"RestartPolicy": {"Name": "unless-stopped"}},
                     "State": {"Pid": 101, "StartedAt": "2026-08-22T00:00:00Z"},
                     "RestartCount": 0}

            def __init__(self):
                self.lifecycle = []
                self.removed = False

            def exec_run(self, command):
                return 0, b"0 active channels\n0 active calls\n"

            def stop(self, timeout=0):
                self.lifecycle.append(("stop", timeout))
                self.status = "exited"

            def reload(self):
                return None

            def top(self):
                return {"Processes": [["1", "asterisk"]]}

            def remove(self, force=False):
                self.lifecycle.append(("remove", force))
                self.removed = True

            def update(self, **kwargs):
                name = kwargs["restart_policy"]["Name"]
                self.lifecycle.append(("update", name))
                self.attrs["HostConfig"]["RestartPolicy"] = {"Name": name}

        container = _Container()
        client = SimpleNamespace(
            containers=SimpleNamespace(get=lambda _name: container), close=lambda: None)
        with patch.object(engine.docker, "from_env", return_value=client), \
                patch.object(engine, "capture_diagnostics"), \
                patch.object(engine.time, "sleep"):
            result = engine.capture_and_stop_if_idle(
                "1", {"id": "1"}, "health-freeze:registering", "generation-1")
        self.assertEqual(result, {
            "status": "stopped", "active_channels": 0, "stopped": True})
        self.assertTrue(container.removed)
        self.assertEqual(container.lifecycle, [
            ("update", "no"), ("stop", 2), ("remove", False)])

    def test_abort_boundary_remove_race_fails_closed(self):
        engine = EnginePathTests.engine_module()

        class _Container:
            id = "generation-1"
            status = "running"
            attrs = {"Config": {"Labels": {engine.MANAGED_LABEL: "true"},
                                "Image": "mdd-sim-gateway/engine"},
                     "HostConfig": {"RestartPolicy": {"Name": "unless-stopped"}},
                     "State": {"Pid": 101, "StartedAt": "2026-08-22T00:00:00Z"},
                     "RestartCount": 0}

            def __init__(self):
                self.lifecycle = []

            def exec_run(self, command):
                return 0, b"0 active channels\n0 active calls\n"

            def stop(self, timeout=0):
                self.lifecycle.append(("stop", timeout))
                self.status = "exited"

            def reload(self):
                return None

            def top(self):
                return {"Processes": [["1", "asterisk"]]}

            def remove(self, force=False):
                self.lifecycle.append(("remove", force))
                self.status = "running"
                raise RuntimeError("container restarted concurrently")

            def update(self, **kwargs):
                name = kwargs["restart_policy"]["Name"]
                self.lifecycle.append(("update", name))
                self.attrs["HostConfig"]["RestartPolicy"] = {"Name": name}

        container = _Container()
        client = SimpleNamespace(
            containers=SimpleNamespace(get=lambda _name: container), close=lambda: None)
        with patch.object(engine.docker, "from_env", return_value=client), \
                patch.object(engine, "capture_diagnostics"), \
                patch.object(engine.time, "sleep"):
            result = engine.capture_and_stop_if_idle(
                "1", {"id": "1"}, "health-freeze:registering", "generation-1")
        self.assertEqual(result["status"], "quiesce_restart_race")
        self.assertFalse(result["stopped"])
        self.assertEqual(container.lifecycle, [
            ("update", "no"), ("stop", 2), ("remove", False),
            ("stop", 2), ("remove", False),
            ("update", "unless-stopped")])

    def test_abort_boundary_generation_change_never_updates_replacement(self):
        engine = EnginePathTests.engine_module()

        class _Container:
            id = "generation-1"
            status = "running"
            attrs = {"Config": {"Labels": {engine.MANAGED_LABEL: "true"},
                                "Image": "mdd-sim-gateway/engine"},
                     "HostConfig": {"RestartPolicy": {"Name": "unless-stopped"}},
                     "State": {"Pid": 101, "StartedAt": "2026-08-22T00:00:00Z"},
                     "RestartCount": 0}

            def __init__(self):
                self.lifecycle = []

            def exec_run(self, command):
                return 0, b"0 active channels\n0 active calls\n"

            def stop(self, timeout=0):
                self.lifecycle.append(("stop", timeout))
                self.status = "exited"

            def reload(self):
                return None

            def top(self):
                return {"Processes": [["1", "asterisk"]]}

            def remove(self, force=False):
                self.lifecycle.append(("remove", force))
                self.id = "generation-2"
                self.status = "running"
                raise RuntimeError("generation replaced concurrently")

            def update(self, **kwargs):
                self.lifecycle.append(("update", kwargs["restart_policy"]["Name"]))

        container = _Container()
        client = SimpleNamespace(
            containers=SimpleNamespace(get=lambda _name: container), close=lambda: None)
        with patch.object(engine.docker, "from_env", return_value=client), \
                patch.object(engine, "capture_diagnostics"), \
                patch.object(engine.time, "sleep"):
            result = engine.capture_and_stop_if_idle(
                "1", {"id": "1"}, "health-freeze:registering", "generation-1")
        self.assertEqual(result["status"], "generation_changed")
        self.assertEqual(container.lifecycle, [
            ("update", "no"), ("stop", 2), ("remove", False)])

    def test_snapshots_are_bounded(self):
        engine = EnginePathTests.engine_module()
        with tempfile.TemporaryDirectory() as temp:
            base = self._instance_dir(temp)
            with patch.object(engine, "DIAGNOSTIC_RECORDS", 3):
                for index in range(6):
                    engine._append_diagnostic(str(base), {"index": index})
            records = [json.loads(line) for line
                       in (base / "logs" / "diagnostics.jsonl").read_text().splitlines() if line]
            self.assertEqual([x["index"] for x in records], [3, 4, 5])

    def test_sip_evidence_keeps_protocol_lines_and_drops_debug_chatter(self):
        """Built from a real capture: the debug stream names the registration module on
        almost every line, which crowded the actual failure out of the bounded tail."""
        engine = EnginePathTests.engine_module()
        raw = "\n".join([
            "\x1b[1;30m    -- \x1b[0mRemote UNIX connection",
            "[Aug  4 09:19:06] \x1b[1;32mDEBUG\x1b[0m[1562]: "
            "\x1b[1;37mres_pjsip_outbound_registration.c\x1b[0m:1241 handle_client_registration",
            "[Aug  4 09:19:06] DEBUG[1562]: res_pjsip/pjsip_resolver.c:495 "
            "sip_resolve: Transport 'volte_ims' ...",
            "[Aug  4 09:16:28] WARNING[133]: res_pjsip_outbound_registration.c:1522 "
            "registration_transport_shutdown_cb: PJSIP transport 'volte_ims' failed.",
            "[2026-08-07 11:17:18+0800] REGISTER "
            "sip:ims.mnc240.mcc310.3gppnetwork.org SIP/2.0",
            "[Aug  4 09:18:36] WARNING[1562]: res_pjsip_outbound_registration.c:1456 "
            "schedule_retry: No response received from 'sip:ims.mnc240.mcc310...'",
            "Temporal response '503' received on registration attempt, retrying in '300'",
            "'403' fatal response received on registration attempt, retrying in '3600' seconds",
            "\x1b[1;32mSIP/2.0 401 Unauthorized\x1b[0m",
            "Status: Rejected",
        ])
        kept = engine._sip_evidence(raw)

        self.assertNotIn("Remote UNIX connection", "\n".join(kept))
        # Module names in DEBUG lines must not qualify as evidence on their own.
        self.assertFalse(any("handle_client_registration" in line for line in kept))
        self.assertFalse(any("sip_resolve" in line for line in kept))
        self.assertEqual(kept, [
            "[Aug  4 09:16:28] WARNING[133]: res_pjsip_outbound_registration.c:1522 "
            "registration_transport_shutdown_cb: PJSIP transport 'volte_ims' failed.",
            "[2026-08-07 11:17:18+0800] REGISTER "
            "sip:ims.mnc240.mcc310.3gppnetwork.org SIP/2.0",
            "[Aug  4 09:18:36] WARNING[1562]: res_pjsip_outbound_registration.c:1456 "
            "schedule_retry: No response received from 'sip:ims.mnc240.mcc310...'",
            "Temporal response '503' received on registration attempt, retrying in '300'",
            "'403' fatal response received on registration attempt, retrying in '3600' seconds",
            # Colour escapes stripped so the stored record stays greppable.
            "SIP/2.0 401 Unauthorized",
            "Status: Rejected",
        ])

    def test_sip_evidence_is_bounded_to_the_newest_lines(self):
        engine = EnginePathTests.engine_module()
        raw = "\n".join(f"SIP/2.0 {200 + index % 100} OK" for index in range(120))
        kept = engine._sip_evidence(raw)
        self.assertEqual(len(kept), engine.SIP_EVIDENCE_LINES)
        self.assertEqual(kept[-1], "SIP/2.0 219 OK")

    def test_health_freeze_captures_before_removing_the_container(self):
        """The freeze path removes the container and only rebuilds after a cooldown, so a
        capture that runs at start() time finds nothing left to read."""
        engine = EnginePathTests.engine_module()
        order = []
        with tempfile.TemporaryDirectory() as temp:
            base = self._instance_dir(temp)
            with patch.object(engine, "_instance_paths", lambda iid: (str(base), str(base))), \
                    patch.object(engine, "capture_diagnostics",
                                 lambda iid, inst, b, reason: order.append(("capture", reason))), \
                    patch.object(engine, "stop", lambda iid: order.append(("stop", iid)) or True):
                engine.capture_and_stop("1", {"mcc": "310"}, "health-freeze:registering")
        self.assertEqual(order, [("capture", "health-freeze:registering"), ("stop", "1")])

    def test_late_capture_never_removes_a_replacement_container(self):
        engine = EnginePathTests.engine_module()
        current = SimpleNamespace(id="new", name="mdd-sim-gateway-engine-1",
                                  attrs={"Config": {"Labels": {
                                      engine.MANAGED_LABEL: "true"}}})
        current.remove = lambda force: self.fail("replacement container was removed")
        client = SimpleNamespace(containers=SimpleNamespace(get=lambda name: current))
        with tempfile.TemporaryDirectory() as temp, \
                patch.object(engine, "_client", lambda: client), \
                patch.object(engine, "_instance_paths", lambda iid: (temp, temp)), \
                patch.object(engine, "capture_diagnostics") as capture:
            stopped = engine.capture_and_stop(
                "1", {"mcc": "310"}, "health-freeze:registering", "old")
        self.assertFalse(stopped)
        capture.assert_not_called()

    def test_capture_failure_never_blocks_the_rebuild(self):
        engine = EnginePathTests.engine_module()
        with tempfile.TemporaryDirectory() as temp:
            base = self._instance_dir(temp)
            with patch.object(engine, "registration_state",
                              side_effect=RuntimeError("docker is unreachable")):
                engine.capture_diagnostics("1", {}, str(base), "rebuild")
            self.assertFalse((base / "logs" / "diagnostics.jsonl").exists())


if __name__ == "__main__":
    unittest.main()


class RenderEnvTests(unittest.TestCase):
    """The engine reads its policy from engine.env, so a value that cannot survive the trip
    is a policy that cannot be set."""

    @staticmethod
    def _render():
        root = Path(__file__).resolve().parent.parent
        spec = importlib.util.spec_from_file_location(
            "mdd_render", root / "engine" / "render.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module

    def test_zero_survives_as_zero(self):
        # rekey_minutes 0 is how a line turns proactive rekey off. Written as an empty string
        # it came back as swu_ike's 30-minute default, silently ignoring the setting.
        value = self._render().env_value(0)
        self.assertEqual(value, "0")
        self.assertEqual(float(value or 30), 0.0)

    def test_none_is_still_written_as_empty(self):
        self.assertEqual(self._render().env_value(None), "''")

    def test_values_needing_quotes_stay_one_shell_word(self):
        self.assertEqual(self._render().env_value("a b"), "'a b'")
