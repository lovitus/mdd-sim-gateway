import unittest
import types
import asyncio
import os
import tempfile
import threading
from unittest.mock import AsyncMock, Mock, patch

from control.app import main


VALID_AGENT_PACKAGE_DIGEST = "a" * 64
OTHER_AGENT_PACKAGE_DIGEST = "b" * 64


def valid_call_contract(**overrides):
    value = {
        "version": 2,
        "audio_telemetry_version": 2,
        "package_digest": VALID_AGENT_PACKAGE_DIGEST,
    }
    value.update(overrides)
    return value


class RemoteModemDeviceTests(unittest.TestCase):
    def setUp(self):
        self.env = patch.dict(os.environ, {
            "MDD_ALLOWED_AGENT_PACKAGE_DIGESTS": VALID_AGENT_PACKAGE_DIGEST,
        })
        self.env.start()

    def tearDown(self):
        self.env.stop()

    def test_remote_modem_event_includes_fresh_line_mapping(self):
        attachment = types.SimpleNamespace(iccid="89852312388530152529")
        with patch.object(main.cfg, "list_instances", return_value=[
                {"id": "7", "iccid": attachment.iccid}]):
            self.assertEqual(main._remote_modem_event(attachment, True), {
                "type": "remote-modem", "iccid": attachment.iccid,
                "instance": "7", "online": True,
            })

    def test_remote_modem_rebuilds_country_from_saved_line_without_reader_cache(self):
        iccid = "89852312388530152529"
        remote = {
            "iccid": iccid, "online": True, "imsi": "455070885002522",
            "model": "3GPP modem", "capabilities": {"cellular_data": True},
            "status": {"registration": "home", "operator": "CHN-CT"},
        }
        instance = {
            "id": "5", "iccid": iccid, "mcc": "455", "mnc": "007",
            "proxy_country": "hk", "name": "455-07-2529",
        }
        egress_state = {
            "lines": {"5": {"node": "HK exit"}},
            "exits": {"hk": {"pinned_node": "HK Alpha", "pin_mode": "manual"}},
        }
        with patch.object(main.modem_registry, "list", return_value=[remote]), \
                patch.object(main.device_state, "desired", return_value={
                    "devices": {}, "defaults": {}}), \
                patch.object(main, "_match_instance_by_iccid", return_value=instance):
            device = main._merge_remote_modem_devices(
                [], available_countries=["gb", "hk"], egress_state=egress_state)[0]

        self.assertEqual(device["egress"]["detected_country"], "mo")
        self.assertEqual(device["egress"]["override"], "hk")
        self.assertEqual(device["egress"]["country"], "hk")
        self.assertEqual(device["egress"]["available_countries"], ["gb", "hk"])
        self.assertEqual(device["egress"]["node"], "HK exit")
        self.assertEqual(device["egress"]["pinned_node"], "HK Alpha")
        self.assertEqual(device["sim"]["carrier"]["current_network"], "CHN-CT")

    def test_remote_modem_country_is_independent_of_stale_vpcd_metadata(self):
        iccid = "89852312388530152529"
        stale = {
            "id": "reader-old", "device_type": "reader", "present": False,
            "sim": {"iccid": iccid, "mcc": "234"},
            "egress": {"country": "gb", "available_countries": ["gb"]},
        }
        remote = {
            "iccid": iccid, "online": True, "imsi": "455070885002522",
            "capabilities": {"cellular_data": True}, "status": {},
        }
        instance = {"id": "5", "iccid": iccid, "mcc": "", "proxy_country": ""}
        with patch.object(main.modem_registry, "list", return_value=[remote]), \
                patch.object(main.device_state, "desired", return_value={
                    "devices": {}, "defaults": {}}), \
                patch.object(main, "_match_instance_by_iccid", return_value=instance):
            device = main._merge_remote_modem_devices(
                [stale], available_countries=["hk"], egress_state={})[0]

        # The saved line is empty, so the live Agent IMSI fills the view only.  The stale
        # reader's GB country must neither override it nor be written into the line.
        self.assertEqual(device["egress"]["detected_country"], "mo")
        self.assertEqual(device["egress"]["country"], "mo")
        self.assertEqual(device["egress"]["available_countries"], ["hk"])

    def test_remote_modem_replaces_transport_reader_for_same_iccid(self):
        iccid = "89852312388530152529"
        reader = {
            "id": "reader-old", "device_type": "reader", "name": "Virtual PCD 00 04",
            "present": True, "imei": "862547055201716", "sim": {"iccid": iccid},
            "capabilities": {"vowifi": {"desired": True, "actual": "off"}},
        }
        remote = {
            "iccid": iccid, "online": True, "imei": "862547055201716", "model": "3GPP modem",
            "imsi": "455070885002578",
            "capabilities": {"cellular_data": True, "sms": True,
                             "call_signalling": False},
            "status": {"registration": "roaming", "operator": "China Telecom",
                       "operator_id": "46011",
                       "signal": 42, "radio_enabled": True, "data": "disconnected",
                       "roaming_allowed": False, "sms_ready": True},
        }
        with patch.object(main.modem_registry, "list", return_value=[remote]), \
                patch.object(main.device_state, "desired", return_value={
                    "devices": {}, "defaults": {"cellular_enabled": False,
                    "vowifi_enabled": True, "flight_mode": False,
                    "roaming_enabled": False}}), \
                patch.object(main, "_match_instance_by_iccid", return_value={"id": "5"}):
            stale = {"id": main._remote_modem_device_id(iccid), "device_type": "modem",
                     "name": "USB modem", "present": False, "sim": {"iccid": ""}}
            devices = main._merge_remote_modem_devices([reader, stale])
        self.assertEqual(len(devices), 1)
        self.assertEqual(devices[0]["device_type"], "modem")
        self.assertTrue(devices[0]["remote_modem"])
        self.assertEqual(devices[0]["cellular"]["registration"], "roaming")
        self.assertEqual(devices[0]["cellular"]["operator_id"], "46011")
        self.assertEqual(devices[0]["instance_id"], "5")
        self.assertEqual(devices[0]["iccid"], iccid)
        self.assertEqual(devices[0]["sim"]["imsi"], "455070885002578")
        self.assertEqual(devices[0]["sim"]["mcc"], "455")
        self.assertEqual(devices[0]["sim"]["mnc"], "07")
        self.assertEqual(devices[0]["sim"]["mnc_source"], "imsi+plmn-length")
        self.assertEqual(devices[0]["sim"]["identity_source"], "modem-provider")
        self.assertTrue(devices[0]["capabilities"]["roaming"]["available"])
        self.assertEqual(devices[0]["capabilities"]["sms"]["actual"], "on")
        self.assertEqual(devices[0]["ims_capabilities"]["sms"]["actual"], "on")
        self.assertEqual(devices[0]["ims_capabilities"]["voice"]["actual"], "off")

    def test_remote_modem_does_not_advertise_sms_when_runtime_is_not_ready(self):
        iccid = "89852312388530152529"
        remote = {"iccid": iccid, "online": True,
                  "capabilities": {"cellular_data": True, "sms": True},
                  "status": {"sms_ready": False, "sms_error": "0x80070490"}}
        with patch.object(main.modem_registry, "list", return_value=[remote]), \
                patch.object(main.device_state, "desired", return_value={
                    "devices": {}, "defaults": {}}), \
                patch.object(main, "_match_instance_by_iccid", return_value={"id": "5"}):
            device = main._merge_remote_modem_devices([])[0]
        self.assertEqual(device["capabilities"]["sms"]["actual"], "off")
        self.assertFalse(device["capabilities"]["sms"]["available"])
        self.assertIn("0x80070490", device["capabilities"]["sms"]["reason"])

    def test_remote_modem_publishes_only_agent_preflighted_recovery_actions(self):
        iccid = "89852312388530152529"
        recovery = {
            "refresh": {"available": True, "recommended": True, "failures": 1},
            "soft_restart": {"available": False, "recommended": False,
                             "reason": "target is ambiguous"},
        }
        remote = {"iccid": iccid, "online": True,
                  "capabilities": {"cellular_data": True, "sms": True},
                  "status": {"sms_ready": False, "sms_error": "0x8000000A",
                             "recovery": recovery}}
        with patch.object(main.modem_registry, "list", return_value=[remote]), \
                patch.object(main.device_state, "desired", return_value={
                    "devices": {}, "defaults": {}}), \
                patch.object(main, "_match_instance_by_iccid", return_value={"id": "5"}):
            device = main._merge_remote_modem_devices([])[0]
        self.assertEqual(device["sms_diagnostics"]["recovery"], recovery)
        self.assertFalse(device["sms_diagnostics"]["recovery"]["soft_restart"]["available"])

    def test_remote_modem_does_not_advertise_call_from_static_adapter_capability(self):
        iccid = "89852312388530152529"
        remote = {"iccid": iccid, "online": True,
                  "capabilities": {"cellular_data": True, "call_signalling": True,
                                   "call_contract": {
                                       "version": 2, "audio_telemetry_version": 2,
                                       "package_digest": VALID_AGENT_PACKAGE_DIGEST}},
                  "status": {"call_ready": False,
                             "call_error": "no CS registration or IMS session"}}
        with patch.object(main.modem_registry, "list", return_value=[remote]), \
                patch.object(main.device_state, "desired", return_value={
                    "devices": {}, "defaults": {}}), \
                patch.object(main, "_match_instance_by_iccid", return_value={"id": "5"}):
            device = main._merge_remote_modem_devices([])[0]
        self.assertEqual(device["capabilities"]["call"]["actual"], "off")
        self.assertFalse(device["capabilities"]["call"]["available"])
        self.assertEqual(device["ims_capabilities"]["voice"]["actual"], "off")
        self.assertIn("no CS registration", device["capabilities"]["call"]["reason"])

    def test_data_roaming_policy_does_not_gate_ready_cellular_call(self):
        iccid = "89852312388530152529"
        remote = {
            "iccid": iccid,
            "online": True,
            "capabilities": {
                "cellular_data": True,
                "call_signalling": True,
                "call_audio": True,
                "call_contract": valid_call_contract(),
            },
            "status": {
                "registration": "roaming",
                "roaming_allowed": False,
                "call_ready": True,
                "call_audio_ready": True,
            },
        }
        with patch.object(main.modem_registry, "list", return_value=[remote]), \
                patch.object(main.device_state, "desired", return_value={
                    "devices": {}, "defaults": {
                        "cellular_enabled": True,
                        "roaming_enabled": False,
                    }}), \
                patch.object(main, "_match_instance_by_iccid", return_value={"id": "5"}):
            device = main._merge_remote_modem_devices([])[0]

        self.assertEqual(device["capabilities"]["cellular"]["actual"], "error")
        self.assertIn("Data roaming is disabled",
                      device["capabilities"]["cellular"]["reason"])
        self.assertEqual(device["capabilities"]["call"]["actual"], "on")
        self.assertTrue(device["capabilities"]["call"]["available"])
        self.assertEqual(device["ims_capabilities"]["voice"]["actual"], "on")

    def test_remote_modem_old_package_self_reporting_v2_is_not_advertised(self):
        iccid = "89852312388530152529"
        remote = {
            "iccid": iccid,
            "online": True,
            "capabilities": {
                "cellular_data": True,
                "call_signalling": True,
                "call_audio": True,
                "call_contract": valid_call_contract(package_digest=OTHER_AGENT_PACKAGE_DIGEST),
            },
            "status": {"call_ready": True, "call_audio_ready": True},
        }
        with patch.object(main.modem_registry, "list", return_value=[remote]), \
                patch.object(main.device_state, "desired", return_value={
                    "devices": {}, "defaults": {}}), \
                patch.object(main, "_match_instance_by_iccid", return_value={"id": "5"}):
            device = main._merge_remote_modem_devices([])[0]

        self.assertEqual(device["capabilities"]["call"]["actual"], "off")
        self.assertFalse(device["capabilities"]["call"]["available"])
        self.assertIn("does not match", device["capabilities"]["call"]["reason"])

    def test_remote_modem_vowifi_is_unavailable_while_runtime_apdu_is_paused(self):
        iccid = "89852312388530152529"
        remote = {"iccid": iccid, "online": True,
                  "capabilities": {"cellular_data": True, "sim_apdu": True},
                  "status": {"sim_apdu_ready": False,
                             "sim_apdu_error": "APDU paused while data owns the SIM"}}
        with patch.object(main.modem_registry, "list", return_value=[remote]), \
                patch.object(main.device_state, "desired", return_value={
                    "devices": {}, "defaults": {"vowifi_enabled": True}}), \
                patch.object(main, "_match_instance_by_iccid", return_value={"id": "5"}):
            device = main._merge_remote_modem_devices([])[0]
        self.assertFalse(device["capabilities"]["vowifi"]["available"])
        self.assertEqual(device["capabilities"]["vowifi"]["actual"], "unsupported")
        self.assertIn("APDU paused", device["capabilities"]["vowifi"]["reason"])


class RemoteModemCapabilityApiTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.env = patch.dict(os.environ, {
            "MDD_ALLOWED_AGENT_PACKAGE_DIGESTS": VALID_AGENT_PACKAGE_DIGEST,
        })
        self.env.start()

    def tearDown(self):
        self.env.stop()

    async def test_remote_modem_phone_fills_empty_line_by_exact_iccid(self):
        iccid = "89852312388530152529"
        instance = {"id": "5", "iccid": iccid, "msisdn": ""}
        upsert = Mock()
        broadcast = AsyncMock()
        with patch.object(main.device_state, "status", return_value={"devices": {}}), \
                patch.object(main.modem_registry, "list", return_value=[{
                    "online": True, "iccid": iccid, "phone": "+85246094054"}]), \
                patch.object(main, "_match_instance_by_iccid", return_value=instance), \
                patch.object(main.cfg, "upsert_instance", upsert), \
                patch.object(main.hub, "ami", {}), \
                patch.object(main.hub, "broadcast", broadcast):
            await main.sync_modem_msisdns()

        upsert.assert_called_once_with({
            "id": "5", "msisdn": "+85246094054", "msisdn_source": "modem-provider"})
        broadcast.assert_awaited_once_with({
            "type": "engine", "instance": "5", "event": "msisdn_updated", "args": []})

    async def test_remote_modem_phone_never_overwrites_a_saved_line_number(self):
        iccid = "89852312388530152529"
        upsert = Mock()
        with patch.object(main.device_state, "status", return_value={"devices": {}}), \
                patch.object(main.modem_registry, "list", return_value=[{
                    "online": True, "iccid": iccid, "phone": "+85246094054"}]), \
                patch.object(main, "_match_instance_by_iccid", return_value={
                    "id": "5", "iccid": iccid, "msisdn": "+85240000000",
                    "msisdn_source": "manual"}), \
                patch.object(main.cfg, "upsert_instance", upsert):
            await main.sync_modem_msisdns()
        upsert.assert_not_called()

    async def test_offline_remote_modem_phone_is_not_learned(self):
        upsert = Mock()
        with patch.object(main.device_state, "status", return_value={"devices": {}}), \
                patch.object(main.modem_registry, "list", return_value=[{
                    "online": False, "iccid": "89852312388530152529",
                    "phone": "+85246094054"}]), \
                patch.object(main, "_match_instance_by_iccid") as match, \
                patch.object(main.cfg, "upsert_instance", upsert):
            await main.sync_modem_msisdns()
        match.assert_not_called()
        upsert.assert_not_called()

    async def test_delete_route_only_hides_offline_device_and_preserves_state(self):
        device = {"id": "reader-a", "device_type": "reader", "present": False}
        with patch.object(main, "_unified_devices", AsyncMock(return_value=[device])), \
                patch.object(main.device_state, "hide_device", Mock()) as hide, \
                patch.object(main.device_state, "remove_desired", Mock()) as remove_desired, \
                patch.object(main.device_state, "remove_hardware", Mock()) as remove_hardware, \
                patch.object(main.hub, "broadcast", AsyncMock()):
            result = await main.api_device_delete("reader-a")
        self.assertTrue(result["hidden"])
        self.assertTrue(result["data_preserved"])
        self.assertTrue(result["reappears_on_heartbeat"])
        hide.assert_called_once_with("reader-a")
        remove_desired.assert_not_called()
        remove_hardware.assert_not_called()

    async def test_cellular_voice_prepare_fails_before_allocation_without_audio_self_test(self):
        attachment = types.SimpleNamespace(
            capabilities={
                "call_signalling": True, "call_audio": False,
                "paid_call_lease_version": 1,
                "call_contract": valid_call_contract(),
            },
            status={"call_audio_ready": False, "call_audio_error": "UAC probe failed"})
        allocate = AsyncMock()
        with patch.object(main.cfg, "get_instance", return_value={"id": "5"}), \
                patch.object(main.cfg, "list_instances", return_value=[{
                    "id": "5", "iccid": "89852312388530152529"}]), \
                patch.object(main.remote_modem, "attached_iccid",
                             return_value="89852312388530152529"), \
                patch.object(main.modem_registry, "resolve", return_value=attachment), \
                patch.object(main.call_media.manager, "allocate", allocate):
            with self.assertRaises(main.HTTPException) as raised:
                await main.api_cellular_call_prepare(
                    "5", {"to": "22333322"}, types.SimpleNamespace())
        self.assertEqual(raised.exception.status_code, 409)
        self.assertIn("UAC probe failed", str(raised.exception.detail))
        allocate.assert_not_awaited()

    async def test_cellular_voice_prepare_fails_before_audio_when_registration_is_pending(self):
        attachment = types.SimpleNamespace(
            capabilities={
                "call_signalling": True, "call_audio": True,
                "paid_call_lease_version": 1,
                "call_contract": valid_call_contract(),
            },
            status={"call_audio_ready": True, "call_ready": False,
                    "call_error": "limited service without a voice bearer"})
        allocate = AsyncMock()
        with patch.object(main.cfg, "get_instance", return_value={"id": "5"}), \
                patch.object(main.cfg, "list_instances", return_value=[{
                    "id": "5", "iccid": "89852312388530152529"}]), \
                patch.object(main.remote_modem, "attached_iccid",
                             return_value="89852312388530152529"), \
                patch.object(main.modem_registry, "resolve", return_value=attachment), \
                patch.object(main.call_media.manager, "allocate", allocate):
            with self.assertRaises(main.HTTPException) as raised:
                await main.api_cellular_call_prepare(
                    "5", {"to": "22333322"}, types.SimpleNamespace())
        self.assertEqual(raised.exception.status_code, 409)
        self.assertIn("limited service", str(raised.exception.detail))
        allocate.assert_not_awaited()

    async def test_cellular_voice_prepare_rejects_old_agent_before_paid_action(self):
        attachment = types.SimpleNamespace(
            capabilities={"call_signalling": True, "call_audio": True},
            status={"call_audio_ready": True, "call_ready": True})
        allocate = AsyncMock()
        with patch.object(main.cfg, "get_instance", return_value={"id": "5"}), \
                patch.object(main.cfg, "list_instances", return_value=[{
                    "id": "5", "iccid": "89852312388530152529"}]), \
                patch.object(main.remote_modem, "attached_iccid",
                             return_value="89852312388530152529"), \
                patch.object(main.modem_registry, "resolve", return_value=attachment), \
                patch.object(main.call_media.manager, "allocate", allocate):
            with self.assertRaises(main.HTTPException) as raised:
                await main.api_cellular_call_prepare(
                    "5", {"to": "22333322"}, types.SimpleNamespace())
        self.assertEqual(raised.exception.status_code, 409)
        self.assertIn("Agent package is too old", str(raised.exception.detail))
        allocate.assert_not_awaited()

    async def test_cellular_voice_prepare_rejects_old_call_audio_helper_before_paid_action(self):
        attachment = types.SimpleNamespace(
            capabilities={
                "call_signalling": True, "call_audio": True,
                "paid_call_lease_version": 1,
                "call_contract": valid_call_contract(audio_telemetry_version=1),
            },
            status={"call_audio_ready": True, "call_ready": True})
        allocate = AsyncMock()
        with patch.object(main.cfg, "get_instance", return_value={"id": "5"}), \
                patch.object(main.cfg, "list_instances", return_value=[{
                    "id": "5", "iccid": "89852312388530152529"}]), \
                patch.object(main.remote_modem, "attached_iccid",
                             return_value="89852312388530152529"), \
                patch.object(main.modem_registry, "resolve", return_value=attachment), \
                patch.object(main.call_media.manager, "allocate", allocate):
            with self.assertRaises(main.HTTPException) as raised:
                await main.api_cellular_call_prepare(
                    "5", {"to": "22333322"}, types.SimpleNamespace())
        self.assertEqual(raised.exception.status_code, 409)
        self.assertIn("audio telemetry v2", str(raised.exception.detail))
        allocate.assert_not_awaited()

    async def test_cellular_voice_prepare_rejects_mismatched_agent_package_before_paid_action(self):
        attachment = types.SimpleNamespace(
            capabilities={
                "call_signalling": True, "call_audio": True,
                "paid_call_lease_version": 1,
                "call_contract": valid_call_contract(package_digest=OTHER_AGENT_PACKAGE_DIGEST),
            },
            status={"call_audio_ready": True, "call_ready": True})
        allocate = AsyncMock()
        with patch.object(main.cfg, "get_instance", return_value={"id": "5"}), \
                patch.object(main.cfg, "list_instances", return_value=[{
                    "id": "5", "iccid": "89852312388530152529"}]), \
                patch.object(main.remote_modem, "attached_iccid",
                             return_value="89852312388530152529"), \
                patch.object(main.modem_registry, "resolve", return_value=attachment), \
                patch.object(main.call_media.manager, "allocate", allocate):
            with self.assertRaises(main.HTTPException) as raised:
                await main.api_cellular_call_prepare(
                    "5", {"to": "22333322"}, types.SimpleNamespace())
        self.assertEqual(raised.exception.status_code, 409)
        self.assertIn("does not match", str(raised.exception.detail))
        allocate.assert_not_awaited()

    async def test_cellular_media_prepare_is_fenced_while_anchor_recovers(self):
        iccid = "89852312388530152529"
        allocate = AsyncMock()
        install = AsyncMock()
        rpc = AsyncMock()
        main.hub.engine_recovering.add("1")
        try:
            with patch.object(main, "_remote_voice_attachment",
                              return_value=(iccid, object())), \
                    patch.object(main.call_media.manager, "for_iccid", return_value=None), \
                    patch.object(main.store, "open_cellular_call_lease", return_value=None), \
                    patch.object(main, "_cellular_media_anchor", AsyncMock(return_value=(
                        "1", object(), {"running": True,
                                        "container_id": "generation-1",
                                        "webrtc_host_port": 8089}, 8089))), \
                    patch.object(main.call_media.manager, "allocate", allocate), \
                    patch.object(main, "_install_cellular_media_extension", install), \
                    patch.object(main.modem_registry, "rpc", rpc):
                with self.assertRaises(main.ModemUnavailable):
                    await main._prepare_remote_cellular_media(
                        "5", "22333322", types.SimpleNamespace(), "out")
        finally:
            main.hub.engine_recovering.discard("1")
        allocate.assert_not_awaited()
        install.assert_not_awaited()
        rpc.assert_not_awaited()

    async def test_cellular_media_prepare_revalidates_anchor_generation_under_gate(self):
        iccid = "89852312388530152529"
        allocate = AsyncMock()
        install = AsyncMock()
        rpc = AsyncMock()
        with patch.object(main, "_remote_voice_attachment",
                          return_value=(iccid, object())), \
                patch.object(main.call_media.manager, "for_iccid", return_value=None), \
                patch.object(main.store, "open_cellular_call_lease", return_value=None), \
                patch.object(main, "_cellular_media_anchor", AsyncMock(return_value=(
                    "1", object(), {"running": True,
                                    "container_id": "generation-1",
                                    "webrtc_host_port": 8089}, 8089))), \
                patch.object(main.hub.runtime, "get", AsyncMock(return_value={
                    "running": True, "container_id": "generation-2",
                    "webrtc_host_port": 8089})), \
                patch.object(main.cfg, "get_instance", return_value={
                    "id": "1", "ports": {"webrtc": 8089}}), \
                patch.object(main, "_webrtc_port_open", AsyncMock(return_value=True)), \
                patch.object(main.call_media.manager, "allocate", allocate), \
                patch.object(main, "_install_cellular_media_extension", install), \
                patch.object(main.modem_registry, "rpc", rpc):
            with self.assertRaises(main.ModemUnavailable):
                await main._prepare_remote_cellular_media(
                    "5", "22333322", types.SimpleNamespace(), "out")
        allocate.assert_not_awaited()
        install.assert_not_awaited()
        rpc.assert_not_awaited()

    async def test_cellular_call_commit_is_single_idempotent_paid_rpc_after_media_ready(self):
        ready = asyncio.Event(); ready.set()
        session = types.SimpleNamespace(
            call_id="a" * 32, iccid="89852312388530152529",
            number="22333322", direction="out", instance_iid="5", media_prepared=ready,
            commit_lock=asyncio.Lock(), commit_result=None,
            anchor_iid="", extension="", lease_task=None,
            media_status=lambda: {"phase": "signalling", "ready": True})
        rpc = AsyncMock(return_value={"ok": True, "status": "dialing"})
        record = {"id": 9, "status": "ringing"}
        with patch.object(main.call_media.manager, "get", return_value=session), \
                patch.object(main.cfg, "list_instances", return_value=[{
                    "id": "5", "iccid": session.iccid}]), \
                patch.object(main.remote_modem, "attached_iccid", return_value=session.iccid), \
                patch.object(main, "_prepared_media_still_live", AsyncMock(return_value=True)), \
                patch.object(main.modem_registry, "rpc", rpc), \
                patch.object(main.store, "save_cellular_call_lease"), \
                patch.object(main, "_supervise_paid_call_lease", AsyncMock()), \
                patch.object(main.store, "add_call", return_value=record), \
                patch.object(main.hub, "broadcast", AsyncMock()):
            result = await main.api_cellular_call_commit("5", session.call_id)
            retried = await main.api_cellular_call_commit("5", session.call_id)
        self.assertTrue(result["audio"])
        self.assertEqual(retried, result)
        rpc.assert_awaited_once_with(
            session.iccid, "call.dial", {
                "to": "22333322", "lease_id": session.call_id},
            operation_id=f"call-dial:{session.call_id}", timeout=90)

    async def test_incoming_browser_rings_once_without_answering_modem(self):
        session = types.SimpleNamespace(
            call_id="b" * 32, direction="in", instance_iid="5", anchor_iid="1",
            extension="881234567890", number="+85246094054",
            ring_lock=asyncio.Lock(), ring_result=None)
        ami = types.SimpleNamespace(originate=AsyncMock(return_value={
            "ok": True, "detail": "Originate successfully queued"}))
        with patch.object(main.call_media.manager, "get", return_value=session), \
                patch.object(main, "_media_anchor_still_live",
                             AsyncMock(return_value=True)), \
                patch.object(main.hub, "ami_for", AsyncMock(return_value=ami)):
            first = await main.api_cellular_incoming_ring("5", session.call_id)
            second = await main.api_cellular_incoming_ring("5", session.call_id)
        self.assertEqual(first, second)
        ami.originate.assert_awaited_once_with(
            session.extension, "webrtc", caller_id=session.number)

    async def test_incoming_answer_is_single_rpc_after_all_media_evidence_is_ready(self):
        ready = asyncio.Event(); ready.set()
        session = types.SimpleNamespace(
            call_id="c" * 32, iccid="89852312388530152529", direction="in",
            instance_iid="5", media_prepared=ready,
            commit_lock=asyncio.Lock(), commit_result=None,
            anchor_iid="", extension="", lease_task=None,
            media_status=lambda: {"phase": "signalling", "ready": True})
        rpc = AsyncMock(return_value={"ok": True, "status": "active"})
        incoming = {"id": 10, "transport": "cellular", "status": "ringing"}
        with patch.object(main.call_media.manager, "get", return_value=session), \
                patch.object(main.cfg, "list_instances", return_value=[{
                    "id": "5", "iccid": session.iccid}]), \
                patch.object(main.remote_modem, "attached_iccid", return_value=session.iccid), \
                patch.object(main, "_prepared_media_still_live", AsyncMock(return_value=True)), \
                patch.object(main.modem_registry, "rpc", rpc), \
                patch.object(main.store, "save_cellular_call_lease"), \
                patch.object(main, "_supervise_paid_call_lease", AsyncMock()), \
                patch.object(main.store, "get_open_call", return_value=incoming), \
                patch.object(main.store, "update_call") as update, \
                patch.object(main.hub, "broadcast", AsyncMock()):
            first = await main.api_cellular_incoming_answer("5", session.call_id)
            second = await main.api_cellular_incoming_answer("5", session.call_id)
        self.assertEqual(first, second)
        self.assertTrue(first["audio"])
        rpc.assert_awaited_once_with(
            session.iccid, "call.answer", {"lease_id": session.call_id},
            operation_id=f"call-answer:{session.call_id}", timeout=30)
        update.assert_called_once_with(incoming["id"], "answered")

    async def test_stopped_engine_disables_softphone_provisioning(self):
        request = types.SimpleNamespace(
            headers={"host": "gateway.example"},
            url=types.SimpleNamespace(hostname="gateway.example", scheme="https", port=443,
                                      path="/mdd/api/instances/5/softphone"),
            scope={"root_path": "/mdd"},
        )
        instance = {
            "id": "5", "mcc": "455", "mnc": "07",
            "sip": {"webrtc": {"enable": True, "username": "webrtc",
                                 "password": "secret"}},
            "ports": {"webrtc": 8129},
        }
        with patch.object(main.cfg, "get_instance", return_value=instance), \
                patch.object(main.hub.runtime, "get", AsyncMock(return_value={
                    "running": False, "container_id": "old-generation"})) as runtime_get:
            result = await main._softphone_provisioning("5", request)
        self.assertFalse(result["enabled"])
        self.assertEqual(result["state"], "stopped")
        self.assertEqual(result["generation"], "old-generation")
        self.assertEqual(result["password"], "")
        runtime_get.assert_awaited_once_with("5")

    async def test_running_softphone_provisioning_binds_ws_url_to_runtime_generation(self):
        request = types.SimpleNamespace(
            headers={"host": "gateway.example"},
            url=types.SimpleNamespace(hostname="gateway.example", scheme="https", port=443,
                                      path="/mdd/api/instances/5/softphone"),
            scope={"root_path": "/mdd"},
        )
        instance = {
            "id": "5", "mcc": "455", "mnc": "07",
            "sip": {"webrtc": {"enable": True, "username": "webrtc",
                                 "password": "secret"}},
            "ports": {"webrtc": 8129},
        }
        runtime = {"running": True, "container_id": "generation/with+symbols",
                   "webrtc_host_port": 8129, "rtp_mapping_exact": True}
        with patch.object(main.cfg, "get_instance", return_value=instance), \
                patch.object(main.media_ingress, "status", return_value={
                    "confirmed": True,
                    "candidate": {"id": "route", "address": "10.0.0.1",
                                  "interface": "tun0"},
                    "inventory_generation": "inventory", "reason": "ready"}):
            result = await main._softphone_provisioning("5", request, runtime=runtime)
        self.assertTrue(result["enabled"])
        self.assertEqual(result["state"], "running")
        self.assertTrue(result["ws_url"].endswith(
            "?generation=generation%2Fwith%2Bsymbols"))

    async def test_ambiguous_media_ingress_disables_only_softphone(self):
        request = types.SimpleNamespace(
            headers={"host": "gateway.example"},
            url=types.SimpleNamespace(hostname="gateway.example", scheme="https", port=443,
                                      path="/mdd/api/instances/5/softphone"),
            scope={"root_path": "/mdd"},
        )
        instance = {
            "id": "5", "mcc": "455", "mnc": "07",
            "sip": {"webrtc": {"enable": True}}, "ports": {"webrtc": 8129},
        }
        runtime = {"running": True, "container_id": "generation",
                   "webrtc_host_port": 8129, "rtp_mapping_exact": True}
        with patch.object(main.cfg, "get_instance", return_value=instance), \
                patch.object(main.media_ingress, "status", return_value={
                    "confirmed": False, "candidate": None,
                    "inventory_generation": "inventory",
                    "reason": "access_host_not_a_managed_ipv4"}):
            result = await main._softphone_provisioning("5", request, runtime=runtime)

        self.assertFalse(result["enabled"])
        self.assertEqual(result["state"], "media_unconfigured")
        self.assertFalse(result["media_ready"])
        self.assertEqual(result["password"], "")

    async def test_cellular_anchor_skips_stopped_preferred_instance_and_binds_generation(self):
        instances = [
            {"id": "5", "sip": {"webrtc": {"enable": True}},
             "ports": {"webrtc": 8129}},
            {"id": "1", "sip": {"webrtc": {"enable": True}},
             "ports": {"webrtc": 8089}},
        ]
        runtimes = {
            "5": {"running": False, "container_id": "dead",
                  "webrtc_host_port": None},
            "1": {"running": True, "container_id": "live-generation",
                  "webrtc_host_port": 8089},
        }
        ami = object()
        with patch.object(main.cfg, "list_instances", return_value=instances), \
                patch.object(main.cfg, "get_instance",
                             side_effect=lambda iid: next(i for i in instances if i["id"] == iid)), \
                patch.object(main.hub.runtime, "get", AsyncMock(
                    side_effect=lambda iid, force=False: runtimes[iid])) as runtime_get, \
                patch.object(main.hub, "ami_for", AsyncMock(return_value=ami)) as ami_for, \
                patch.object(main, "_webrtc_port_open", AsyncMock(return_value=True)) as probe:
            iid, selected, runtime, port = await main._cellular_media_anchor("5")
        self.assertEqual((iid, selected, runtime, port),
                         ("1", ami, runtimes["1"], 8089))
        self.assertEqual(runtime_get.await_count, 2)
        ami_for.assert_awaited_once_with("1", runtimes["1"])
        probe.assert_awaited_once_with(8089)

    async def test_paid_call_is_blocked_when_prepared_media_or_anchor_died(self):
        ready = asyncio.Event(); ready.set()
        session = types.SimpleNamespace(
            call_id="d" * 32, iccid="89852312388530152529", number="22333322",
            media_prepared=ready, commit_lock=asyncio.Lock(), commit_result=None,
            anchor_iid="5")
        rpc = AsyncMock()
        with patch.object(main.call_media.manager, "get", return_value=session), \
                patch.object(main.cfg, "list_instances", return_value=[{
                    "id": "5", "iccid": session.iccid}]), \
                patch.object(main.remote_modem, "attached_iccid", return_value=session.iccid), \
                patch.object(main, "_prepared_media_still_live",
                             AsyncMock(return_value=False)), \
                patch.object(main, "_close_cellular_media", AsyncMock()) as close_media, \
                patch.object(main.modem_registry, "rpc", rpc):
            with self.assertRaises(main.HTTPException) as raised:
                await main.api_cellular_call_commit("5", session.call_id)
        self.assertEqual(raised.exception.status_code, 409)
        self.assertIn("dial was not sent", str(raised.exception.detail))
        rpc.assert_not_awaited()
        close_media.assert_awaited_once_with(session)

    async def test_paid_call_commit_loses_atomic_pcscf_admission_before_agent_dial(self):
        ready = asyncio.Event(); ready.set()
        session = types.SimpleNamespace(
            call_id="a" * 32, iccid="89852312388530152529", number="22333322",
            media_prepared=ready, commit_lock=asyncio.Lock(), commit_result=None,
            anchor_iid="5")

        @main.asynccontextmanager
        async def denied(_iid):
            yield False

        rpc = AsyncMock()
        with patch.object(main.call_media.manager, "get", return_value=session), \
                patch.object(main.cfg, "list_instances", return_value=[{
                    "id": "5", "iccid": session.iccid}]), \
                patch.object(main.remote_modem, "attached_iccid", return_value=session.iccid), \
                patch.object(main, "_pcscf_admission_boundary", new=denied), \
                patch.object(main, "_prepared_media_still_live",
                             AsyncMock(return_value=True)), \
                patch.object(main.modem_registry, "rpc", rpc), \
                patch.object(main, "_close_cellular_media", AsyncMock()):
            with self.assertRaises(main.HTTPException) as raised:
                await main.api_cellular_call_commit("5", session.call_id)
        self.assertIn("carrier-route transition", str(raised.exception.detail))
        rpc.assert_not_awaited()

    async def test_paid_call_is_blocked_when_call_scoped_media_evidence_never_completes(self):
        waiting = types.SimpleNamespace(wait=lambda: asyncio.get_running_loop().create_future())
        session = types.SimpleNamespace(
            call_id="f" * 32, iccid="89852312388530152529", number="22333322",
            media_prepared=waiting, commit_lock=asyncio.Lock(), commit_result=None)
        rpc = AsyncMock()
        with patch.object(main.call_media.manager, "get", return_value=session), \
                patch.object(main.cfg, "list_instances", return_value=[{
                    "id": "5", "iccid": session.iccid}]), \
                patch.object(main.remote_modem, "attached_iccid", return_value=session.iccid), \
                patch.object(main.asyncio, "wait_for",
                             AsyncMock(side_effect=asyncio.TimeoutError)), \
                patch.object(main, "_close_cellular_media", AsyncMock()) as close_media, \
                patch.object(main.modem_registry, "rpc", rpc):
            with self.assertRaises(main.HTTPException) as raised:
                await main.api_cellular_call_commit("5", session.call_id)
        self.assertEqual(raised.exception.status_code, 409)
        self.assertIn("dial was not sent", str(raised.exception.detail))
        rpc.assert_not_awaited()
        close_media.assert_awaited_once_with(session)

    async def test_incoming_answer_transport_loss_is_uncertain_and_keeps_media(self):
        ready = asyncio.Event(); ready.set()
        session = types.SimpleNamespace(
            call_id="e" * 32, iccid="89852312388530152529", direction="in",
            instance_iid="5", media_prepared=ready,
            commit_lock=asyncio.Lock(), commit_result=None,
            anchor_iid="", extension="", lease_task=None,
            media_status=lambda: {"phase": "signalling", "ready": True})
        with patch.object(main.call_media.manager, "get", return_value=session), \
                patch.object(main.cfg, "list_instances", return_value=[{
                    "id": "5", "iccid": session.iccid}]), \
                patch.object(main.remote_modem, "attached_iccid", return_value=session.iccid), \
                patch.object(main, "_prepared_media_still_live",
                             AsyncMock(return_value=True)), \
                patch.object(main.modem_registry, "rpc",
                             AsyncMock(side_effect=[
                                 main.ModemTimeout("Agent timed out"),
                                 RuntimeError("Agent disconnected"),
                                 RuntimeError("Agent disconnected")])) as rpc, \
                patch.object(main.store, "save_cellular_call_lease"), \
                patch.object(main, "_supervise_paid_call_lease", AsyncMock()), \
                patch.object(main.store, "get_open_call", return_value=None), \
                patch.object(main, "_close_cellular_media", AsyncMock()) as close_media:
            result = await main.api_cellular_incoming_answer("5", session.call_id)
        self.assertTrue(result["uncertain"])
        self.assertEqual([call.args[1] for call in rpc.await_args_list],
                         ["call.answer", "operation.result", "call.status"])
        self.assertEqual(rpc.await_args_list[1].args[2], {
            "operation_id": rpc.await_args_list[0].kwargs["operation_id"]})
        close_media.assert_not_awaited()

    async def test_cancelled_paid_signal_uses_lookup_only_recovery_and_keeps_media(self):
        started = asyncio.Event()
        never = asyncio.Event()
        methods = []

        async def rpc(iccid, method, params=None, **kwargs):
            methods.append(method)
            if method == "call.dial":
                started.set()
                await never.wait()
            if method == "operation.result":
                return {"ok": True, "found": False, "result": None}
            if method == "call.status":
                return {"ok": True, "status": "active"}
            raise AssertionError(method)

        session = types.SimpleNamespace(
            call_id="9" * 32, iccid="89852312388530152529",
            number="22333322", commit_lock=asyncio.Lock(), commit_result=None,
            expiry_task=None, signalling_recovery_task=None)
        with patch.object(main.call_media.manager, "get", return_value=session), \
                patch.object(main.modem_registry, "rpc", side_effect=rpc), \
                patch.object(main, "_close_cellular_media", AsyncMock()) as close_media:
            task = asyncio.create_task(main._remote_call_signal_with_recovery(
                session, "call.dial", {"to": session.number},
                f"call-dial:{session.call_id}", 90))
            await started.wait()
            task.cancel()
            with self.assertRaises(asyncio.CancelledError):
                await task
            await session.signalling_recovery_task
        self.assertEqual(methods, ["call.dial", "operation.result", "call.status"])
        self.assertTrue(session.commit_result["uncertain"])
        self.assertEqual(session.commit_result["status"], "active")
        close_media.assert_not_awaited()

    async def test_ami_factory_never_caches_failed_or_cancelled_client(self):
        instance = {
            "id": "1", "mcc": "234", "mnc": "10", "ami_user": "vowifi",
            "ami_secret": "secret", "sip": {"webrtc": {"enable": True}},
        }
        runtime = {"running": True, "ip": "172.18.0.2", "container_id": "gen-1"}

        failed = types.SimpleNamespace(
            connected=False, close=AsyncMock(), connect=AsyncMock())
        hub = main.Hub()
        with patch.object(main.cfg, "get_instance", return_value=instance), \
                patch.object(main, "AmiClient", return_value=failed):
            self.assertIsNone(await hub.ami_for("1", runtime))
        failed.close.assert_awaited_once()
        self.assertNotIn("1", hub.ami)

        cancelled = types.SimpleNamespace(
            connected=False, close=AsyncMock(),
            connect=AsyncMock(side_effect=asyncio.CancelledError()))
        with patch.object(main.cfg, "get_instance", return_value=instance), \
                patch.object(main, "AmiClient", return_value=cancelled):
            with self.assertRaises(asyncio.CancelledError):
                await hub.ami_for("1", runtime)
        cancelled.close.assert_awaited_once()
        self.assertNotIn("1", hub.ami)

    async def test_runtime_lifecycle_broadcasts_generation_for_softphone_refresh(self):
        hub = main.Hub()
        hub.broadcast = AsyncMock()
        hub.drop_ami = AsyncMock()
        hub.runtime.get = AsyncMock(side_effect=AssertionError("no runtime probe"))
        hub.ami_for = AsyncMock(side_effect=AssertionError("no AMI probe"))
        hub.status_cache["5"] = {"state": "OK", "label": "Working",
                                 "reason_code": "ok", "detail": {
                                     "registration": "Registered"}}
        hub.status_sampled_at["5"] = main.time.monotonic()
        hub.ami_generation["5"] = "generation-1"
        runtime = {"running": False, "container_id": "generation-2",
                   "webrtc_host_port": None}
        with patch.object(main.cfg, "get_instance", return_value={
                "id": "5", "enabled": True}), patch.object(main, "hub", hub):
            await hub.runtime_changed("5", runtime, "stop")
            cached = main._cached_line_status({"id": "5", "enabled": True})

        hub.drop_ami.assert_awaited_once_with("5")
        self.assertNotIn("5", hub.status_cache)
        self.assertNotIn("5", hub.status_sampled_at)
        self.assertEqual(cached["state"], "STOPPED")
        self.assertEqual(cached["reason_code"], "engine_stopped")
        self.assertEqual(hub.status_transitions["5"]["status"]["reason_code"],
                         "engine_stopped")
        engine_event = hub.broadcast.await_args_list[0].args[0]
        self.assertEqual(engine_event["type"], "engine")
        self.assertEqual(engine_event["instance"], "5")
        self.assertEqual(engine_event["event"], "runtime_changed")
        self.assertFalse(engine_event["running"])
        self.assertEqual(engine_event["generation"], "generation-2")
        self.assertEqual(engine_event["webrtc_host_port"], None)
        self.assertEqual(engine_event["status_transition"]["reason_code"],
                         "engine_stopped")
        self.assertEqual(hub.broadcast.await_args_list[1].args[0]["type"], "status")
        self.assertEqual(hub.broadcast.await_args_list[1].args[0]["reason_code"],
                         "engine_stopped")
        hub.runtime.get.assert_not_awaited()
        hub.ami_for.assert_not_awaited()
        self.assertTrue(hub.status_wakeup.is_set())

    async def test_runtime_start_event_masks_old_ok_until_new_status_sample(self):
        hub = main.Hub()
        hub.broadcast = AsyncMock()
        hub.drop_ami = AsyncMock()
        hub.status_cache["5"] = {"state": "OK", "label": "Working",
                                 "reason_code": "ok", "detail": {
                                     "registration": "Registered"}}
        hub.status_sampled_at["5"] = main.time.monotonic()
        hub.ami_generation["5"] = "generation-1"
        runtime = {"running": True, "container_id": "generation-2",
                   "webrtc_host_port": 46090}
        with patch.object(main.cfg, "get_instance", return_value={
                "id": "5", "enabled": True}), patch.object(main, "hub", hub):
            await hub.runtime_changed("5", runtime, "start")
            cached = main._cached_line_status({"id": "5", "enabled": True})
            hub.status_cache["5"] = {"state": "OK", "label": "Working",
                                     "reason_code": "ok", "detail": {
                                         "registration": "Registered"}}
            hub.status_sampled_at["5"] = main.time.monotonic()
            authoritative = main._cached_line_status({"id": "5", "enabled": True})

        hub.drop_ami.assert_awaited_once_with("5")
        self.assertEqual(cached["state"], "REGISTERING")
        self.assertEqual(cached["reason_code"], "engine_changed")
        self.assertEqual(authoritative["state"], "OK")
        self.assertEqual(hub.broadcast.await_args_list[0].args[0][
                         "status_transition"]["reason_code"], "engine_changed")
        self.assertEqual(hub.broadcast.await_args_list[1].args[0]["reason_code"],
                         "engine_changed")

    async def test_runtime_event_keeps_disabled_line_stopped(self):
        hub = main.Hub()
        hub.broadcast = AsyncMock()
        hub.drop_ami = AsyncMock()
        hub.status_cache["5"] = {"state": "OK", "label": "Working",
                                 "reason_code": "ok", "detail": {}}
        hub.status_sampled_at["5"] = main.time.monotonic()
        runtime = {"running": True, "container_id": "generation-2",
                   "webrtc_host_port": 46090}
        with patch.object(main.cfg, "get_instance", return_value={
                "id": "5", "enabled": False}), patch.object(main, "hub", hub):
            await hub.runtime_changed("5", runtime, "start")
            cached = main._cached_line_status({"id": "5", "enabled": False})

        hub.drop_ami.assert_not_awaited()
        self.assertNotIn("5", hub.status_cache)
        self.assertEqual(cached["state"], "STOPPED")
        self.assertEqual(cached["reason_code"], "stopped")
        self.assertEqual(hub.broadcast.await_args_list[0].args[0][
                         "status_transition"]["reason_code"], "stopped")
        self.assertEqual(hub.broadcast.await_args_list[1].args[0]["reason_code"],
                         "stopped")

    def test_expired_runtime_transition_never_restores_old_ok(self):
        hub = main.Hub()
        hub.status_transitions["5"] = {
            "observed_at": main.time.monotonic() - main.STATUS_CACHE_MAX_AGE_SECONDS - 1,
            "status": {
                "state": "STOPPED", "label": "Stopped",
                "reason_code": "engine_stopped",
                "reason": "The VoWiFi engine stopped; refreshing line status.",
                "detail": {},
            },
        }
        with patch.object(main, "hub", hub):
            status = main._cached_line_status({"id": "5", "enabled": True})

        self.assertEqual(status["state"], "REGISTERING")
        self.assertEqual(status["reason_code"], "registering")

    async def test_inflight_status_poll_cannot_republish_ok_after_runtime_change(self):
        hub = main.Hub()
        hub.broadcast = AsyncMock()
        hub.runtime.get = AsyncMock(return_value={
            "running": True, "ip": "172.18.0.5", "container_id": "old-gen"})
        hub.ami_for = AsyncMock(return_value=object())
        entered = asyncio.Event()
        release = asyncio.Event()

        async def compute(_inst, _ami, _runtime):
            entered.set()
            await release.wait()
            return {"state": "OK", "label": "Working", "reason_code": "ok",
                    "reason": "Registered.", "detail": {
                        "registration": "Registered"}}

        with patch.object(main, "hub", hub), \
                patch.object(main.cfg, "get_instance", return_value={
                    "id": "5", "enabled": True}), \
                patch.object(main, "_reconcile_pcscf_rebind",
                             new=AsyncMock()), \
                patch.object(main.status_mod, "compute", new=compute), \
                patch.object(main, "_health_recovery_due", return_value=False), \
                patch.object(main, "_apply_health_with_recovery",
                             new=AsyncMock(side_effect=lambda _iid, _inst, st, _gen: st)), \
                patch.object(main, "_record_line_state", new=AsyncMock()):
            task = asyncio.create_task(main._poll_instance_status({
                "id": "5", "enabled": True}))
            await entered.wait()
            await hub.runtime_changed("5", {
                "running": False, "container_id": "new-gen",
                "webrtc_host_port": None}, "stop")
            release.set()
            await task
            cached = main._cached_line_status({"id": "5", "enabled": True})

        status_messages = [call.args[0] for call in hub.broadcast.await_args_list
                           if call.args[0].get("type") == "status"]
        self.assertEqual(len(status_messages), 1)
        self.assertEqual(status_messages[0]["reason_code"], "engine_stopped")
        self.assertNotIn("5", hub.status_cache)
        self.assertEqual(cached["reason_code"], "engine_stopped")

    async def test_inflight_push_status_cannot_republish_ok_after_runtime_change(self):
        hub = main.Hub()
        hub.broadcast = AsyncMock()
        hub.runtime.get = AsyncMock(return_value={
            "running": True, "ip": "172.18.0.5", "container_id": "old-gen"})
        hub.ami_for = AsyncMock(return_value=object())
        entered = asyncio.Event()
        release = asyncio.Event()

        async def compute(_inst, _ami, _runtime):
            entered.set()
            await release.wait()
            return {"state": "OK", "label": "Working", "reason_code": "ok",
                    "reason": "Registered.", "detail": {
                        "registration": "Registered"}}

        with patch.object(main, "hub", hub), \
                patch.object(main.cfg, "get_instance", return_value={
                    "id": "5", "enabled": True}), \
                patch.object(main, "_reconcile_pcscf_rebind",
                             new=AsyncMock()), \
                patch.object(main.status_mod, "compute", new=compute), \
                patch.object(main, "_apply_health_with_recovery",
                             new=AsyncMock(side_effect=lambda _iid, _inst, st, _gen: st)):
            task = asyncio.create_task(main.push_status("5"))
            await entered.wait()
            await hub.runtime_changed("5", {
                "running": True, "container_id": "new-gen",
                "webrtc_host_port": 46090}, "start")
            release.set()
            await task
            cached = main._cached_line_status({"id": "5", "enabled": True})

        status_messages = [call.args[0] for call in hub.broadcast.await_args_list
                           if call.args[0].get("type") == "status"]
        self.assertEqual(len(status_messages), 1)
        self.assertEqual(status_messages[0]["reason_code"], "engine_changed")
        self.assertNotIn("5", hub.status_cache)
        self.assertEqual(cached["reason_code"], "engine_changed")

    async def test_status_history_persistence_cannot_delay_runtime_transition(self):
        hub = main.Hub()
        hub.broadcast = AsyncMock()
        hub.runtime.get = AsyncMock(return_value={
            "running": True, "ip": "172.18.0.5", "container_id": "old-gen"})
        hub.ami_for = AsyncMock(return_value=object())
        record_started = asyncio.Event()
        release_record = asyncio.Event()

        async def record(_iid, _st):
            record_started.set()
            await release_record.wait()

        with patch.object(main, "hub", hub), \
                patch.object(main.cfg, "get_instance", return_value={
                    "id": "5", "enabled": True}), \
                patch.object(main, "_reconcile_pcscf_rebind",
                             new=AsyncMock()), \
                patch.object(main.status_mod, "compute",
                             new=AsyncMock(return_value={
                                 "state": "OK", "label": "Working",
                                 "reason_code": "ok", "reason": "Registered.",
                                 "detail": {"registration": "Registered"}})), \
                patch.object(main, "_health_recovery_due", return_value=False), \
                patch.object(main, "_apply_health_with_recovery",
                             new=AsyncMock(side_effect=lambda _iid, _inst, st, _gen: st)), \
                patch.object(main, "_record_line_state", new=record):
            task = asyncio.create_task(main._poll_instance_status({
                "id": "5", "enabled": True}))
            await record_started.wait()
            self.assertEqual(
                main._cached_line_status({"id": "5", "enabled": True})["state"],
                "OK")
            await asyncio.wait_for(hub.runtime_changed("5", {
                "running": False, "container_id": "new-gen",
                "webrtc_host_port": None}, "stop"), timeout=0.5)
            cached = main._cached_line_status({"id": "5", "enabled": True})
            release_record.set()
            await task

        self.assertEqual(cached["reason_code"], "engine_stopped")
        self.assertNotIn("5", hub.status_cache)

    async def test_cancel_only_closes_uncommitted_prepared_media(self):
        pending = types.SimpleNamespace(
            call_id="f" * 32, instance_iid="5", commit_result=None,
            iccid="89852312388530152529", direction="out",
            commit_lock=asyncio.Lock())
        with patch.object(main.call_media.manager, "get", return_value=pending), \
                patch.object(main.store, "save_cellular_call_lease"), \
                patch.object(main, "_close_cellular_media", AsyncMock()) as close_media:
            result = await main.api_cellular_call_cancel("5", pending.call_id)
        self.assertTrue(result["cancelled"])
        close_media.assert_awaited_once_with(pending)

        committed = types.SimpleNamespace(
            call_id="1" * 32, instance_iid="5", commit_result={"ok": True},
            iccid="89852312388530152529", direction="out",
            commit_lock=asyncio.Lock())
        with patch.object(main.call_media.manager, "get", return_value=committed), \
                patch.object(main.store, "save_cellular_call_lease"), \
                patch.object(main, "_close_cellular_media", AsyncMock()) as close_media:
            result = await main.api_cellular_call_cancel("5", committed.call_id)
        self.assertTrue(result["committed"])
        close_media.assert_not_awaited()

    async def test_prepare_ttl_delegates_to_atomic_abandon_finalizer(self):
        pending = types.SimpleNamespace(
            call_id="2" * 32, commit_result=None, commit_lock=asyncio.Lock(),
            closed=types.SimpleNamespace(is_set=lambda: False))
        with patch.object(main.call_media.manager, "get", return_value=pending), \
                patch.object(main, "_finalize_abandoned_cellular_media",
                             AsyncMock()) as finalize:
            await main._expire_prepared_cellular_media(pending, ttl=0)
        finalize.assert_awaited_once_with(pending)

    async def test_paid_call_lease_is_renewed_only_with_fresh_media(self):
        session = types.SimpleNamespace(
            call_id="6" * 32, iccid="89852312388530152529",
            lease_last_healthy_at=0.0,
            media_status=lambda: {"ready": True})
        rpc = AsyncMock(return_value={"ok": True, "status": "renewed"})
        with patch.object(main.call_media.manager, "get",
                          side_effect=[session, None]), \
                patch.object(main.modem_registry, "rpc", rpc), \
                patch.object(main.asyncio, "sleep", AsyncMock()):
            await main._supervise_paid_call_lease(session)
        rpc.assert_awaited_once_with(
            session.iccid, "call.lease.renew", {"lease_id": session.call_id}, timeout=6)

    async def test_paid_call_lease_loss_triggers_one_termination(self):
        session = types.SimpleNamespace(
            call_id="7" * 32, iccid="89852312388530152529",
            lease_last_healthy_at=0.0,
            media_status=lambda: {"ready": False})
        with patch.object(main.call_media.manager, "get", return_value=session), \
                patch.object(main, "PAID_CALL_MEDIA_GRACE_SECONDS", 0.0), \
                patch.object(main, "_finalize_abandoned_cellular_media",
                             AsyncMock()) as finalize:
            await main._supervise_paid_call_lease(session)
        finalize.assert_awaited_once_with(session)

    async def test_first_remote_idle_sample_does_not_end_history_or_media(self):
        session = types.SimpleNamespace(
            cellular_state="active", media_status=lambda: {"ready": True})
        result = {"ok": True, "status": "idle", "fresh": True,
                  "authoritative": True, "terminal_samples": 1}
        with patch.object(main.cfg, "get_instance", return_value={"id": "5"}), \
                patch.object(main.cfg, "list_instances", return_value=[{"id": "5"}]), \
                patch.object(main.remote_modem, "attached_iccid", return_value="iccid"), \
                patch.object(main.remote_modem, "instance_iccid", return_value="iccid"), \
                patch.object(main.remote_modem, "invoke", AsyncMock(return_value=result)), \
                patch.object(main.call_media.manager, "for_iccid", return_value=session), \
                patch.object(main, "_sync_cellular_call_record") as sync, \
                patch.object(main, "_close_confirmed_terminal_cellular_media",
                             AsyncMock()) as close:
            observed = await main.api_cellular_call_status("5")
        self.assertEqual(observed["terminal_samples"], 1)
        sync.assert_not_called()
        close.assert_not_awaited()

    async def test_release_atomically_hangs_up_committed_call_before_media_cleanup(self):
        committed = types.SimpleNamespace(
            call_id="3" * 32, instance_iid="5", iccid="89852312388530152529",
            direction="out",
            commit_result={"ok": True}, commit_lock=asyncio.Lock(),
            release_attempts=0, release_result=None, release_operation_id="",
            release_unknown=False, release_state="", release_deadline=0.0,
            termination_task=None)
        rpc = AsyncMock(side_effect=[
            {"ok": True, "status": "idle", "terminal_confirmed": True},
            {"ok": True, "status": "idle", "fresh": True, "authoritative": True,
             "terminal_samples": 2},
        ])
        with patch.object(main.call_media.manager, "get", return_value=committed), \
                patch.object(main.modem_registry, "rpc", rpc), \
                patch.object(main.store, "save_cellular_call_lease"), \
                patch.object(main, "_close_cellular_media", AsyncMock()) as close_media:
            result = await main.api_cellular_call_release("5", committed.call_id)
        self.assertTrue(result["released"])
        self.assertTrue(result["committed"])
        self.assertEqual([call.args[1] for call in rpc.await_args_list],
                         ["call.hangup", "call.status"])
        self.assertEqual(rpc.await_args_list[0].kwargs["operation_id"],
                         f"call-release:{committed.call_id}:1")
        close_media.assert_awaited_once_with(committed)

    async def test_hangup_uncommitted_incoming_prepare_signals_modem(self):
        incoming = types.SimpleNamespace(
            call_id="5" * 32, instance_iid="5", iccid="89852312388530152529",
            direction="in", commit_result=None, commit_lock=asyncio.Lock(),
            release_attempts=0, release_result=None, release_operation_id="",
            release_unknown=False, release_state="", release_deadline=0.0,
            termination_task=None)
        rpc = AsyncMock(side_effect=[
            {"ok": True, "status": "idle", "terminal_confirmed": True},
            {"ok": True, "status": "idle", "fresh": True, "authoritative": True,
             "terminal_samples": 2},
        ])
        with patch.object(main.cfg, "get_instance", return_value={"id": "5"}), \
                patch.object(main.cfg, "list_instances", return_value=[{"id": "5"}]), \
                patch.object(main.remote_modem, "instance_iccid",
                             return_value=incoming.iccid), \
                patch.object(main.remote_modem, "attached_iccid",
                             return_value=incoming.iccid), \
                patch.object(main.call_media.manager, "for_iccid",
                             return_value=incoming), \
                patch.object(main.call_media.manager, "get", return_value=incoming), \
                patch.object(main.modem_registry, "rpc", rpc), \
                patch.object(main.store, "mark_cellular_call_terminating"), \
                patch.object(main.store, "save_cellular_call_lease"), \
                patch.object(main.store, "list_open_cellular_call_leases",
                             return_value=[]), \
                patch.object(main, "_sync_cellular_call_record", return_value=None), \
                patch.object(main, "_close_cellular_media", AsyncMock()) as close_media, \
                patch.object(main, "_resolve_cellular_call_alert", AsyncMock()):
            result = await main.api_cellular_call_hangup("5")
        self.assertTrue(result["released"])
        self.assertFalse(result["committed"])
        self.assertTrue(result["physical_hangup"])
        self.assertTrue(result["terminal_confirmed"])
        self.assertNotIn("cancelled_prepare", result)
        self.assertEqual([call.args[1] for call in rpc.await_args_list],
                         ["call.hangup", "call.status"])
        self.assertEqual(rpc.await_args_list[0].kwargs["operation_id"],
                         f"call-release:{incoming.call_id}:1")
        close_media.assert_awaited_once_with(incoming)

    async def test_hangup_uncommitted_outbound_prepare_only_cancels_media(self):
        outgoing = types.SimpleNamespace(
            call_id="6" * 32, instance_iid="5", iccid="89852312388530152529",
            direction="out", commit_result=None, commit_lock=asyncio.Lock())
        rpc = AsyncMock()
        with patch.object(main.cfg, "get_instance", return_value={"id": "5"}), \
                patch.object(main.cfg, "list_instances", return_value=[{"id": "5"}]), \
                patch.object(main.remote_modem, "instance_iccid",
                             return_value=outgoing.iccid), \
                patch.object(main.remote_modem, "attached_iccid",
                             return_value=outgoing.iccid), \
                patch.object(main.call_media.manager, "for_iccid",
                             return_value=outgoing), \
                patch.object(main.call_media.manager, "get", return_value=outgoing), \
                patch.object(main.modem_registry, "rpc", rpc), \
                patch.object(main.store, "save_cellular_call_lease"), \
                patch.object(main, "_close_cellular_media", AsyncMock()) as close_media:
            result = await main.api_cellular_call_hangup("5")
        self.assertTrue(result["cancelled_prepare"])
        rpc.assert_not_awaited()
        close_media.assert_awaited_once_with(outgoing)

    async def test_cellular_termination_alert_is_persisted_and_resolved_per_call(self):
        first = types.SimpleNamespace(call_id="a" * 32, instance_iid="5")
        second = types.SimpleNamespace(call_id="b" * 32, instance_iid="7")
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(main.cfg, "DATA_DIR", directory), \
                patch.object(main.hub, "broadcast", AsyncMock()) as broadcast:
            await main._record_cellular_call_alert(first, "still active")
            await main._record_cellular_call_alert(second, "status unavailable")
            loaded = await main.api_cellular_call_alerts()
            self.assertEqual({item["call_id"] for item in loaded["alerts"]},
                             {first.call_id, second.call_id})
            self.assertTrue(os.path.exists(os.path.join(
                directory, "cellular-call-alerts.json")))

            result = await main.api_dismiss_cellular_call_alert(first.call_id)
            self.assertTrue(result["dismissed"])
            remaining = await main.api_cellular_call_alerts()
            self.assertEqual([item["call_id"] for item in remaining["alerts"]],
                             [second.call_id])
            broadcast.assert_awaited_with({
                "type": "cellular_call_alert_resolved", "call_id": first.call_id})

    async def test_corrupt_cellular_alert_store_is_never_overwritten(self):
        session = types.SimpleNamespace(call_id="c" * 32, instance_iid="5")
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(main.cfg, "DATA_DIR", directory), \
                patch.object(main.hub, "broadcast", AsyncMock()) as broadcast:
            path = os.path.join(directory, "cellular-call-alerts.json")
            damaged_values = [
                "{damaged evidence",
                "[]",
                '{"version":1,"alerts":{"evidence":"bad-entry"}}',
            ]
            for damaged in damaged_values:
                broadcast.reset_mock()
                with open(path, "w", encoding="utf-8") as handle:
                    handle.write(damaged)
                with self.assertRaises(ValueError):
                    await main._record_cellular_call_alert(session, "still active")
                with open(path, encoding="utf-8") as handle:
                    self.assertEqual(handle.read(), damaged)
                broadcast.assert_awaited_once()
                with self.assertRaises(ValueError):
                    await main.api_dismiss_cellular_call_alert(session.call_id)
                with open(path, encoding="utf-8") as handle:
                    self.assertEqual(handle.read(), damaged)

    async def test_release_failure_keeps_media_and_starts_bounded_supervisor(self):
        committed = types.SimpleNamespace(
            call_id="4" * 32, instance_iid="5", iccid="89852312388530152529",
            direction="out",
            commit_result={"ok": True}, commit_lock=asyncio.Lock(),
            release_attempts=0, release_result=None, release_operation_id="",
            release_unknown=False, release_state="", release_deadline=0.0,
            termination_task=None)
        gate = asyncio.Event()
        with patch.object(main.call_media.manager, "get", return_value=committed), \
                patch.object(main.store, "save_cellular_call_lease"), \
                patch.object(main, "_close_cellular_media", AsyncMock()) as close_media, \
                patch.object(main, "_supervise_cellular_termination",
                             new=lambda session: gate.wait()):
            request = asyncio.create_task(
                main.api_cellular_call_release("5", committed.call_id))
            for _ in range(5):
                await asyncio.sleep(0)
                if committed.termination_task is not None:
                    break
            self.assertIsNotNone(committed.termination_task)
            request.cancel()
            await asyncio.gather(request, return_exceptions=True)
            self.assertFalse(committed.termination_task.done())
        close_media.assert_not_awaited()
        committed.termination_task.cancel()
        await asyncio.gather(committed.termination_task, return_exceptions=True)

    async def test_concurrent_release_requests_share_one_server_owned_coordinator(self):
        committed = types.SimpleNamespace(
            call_id="8" * 32, instance_iid="5", iccid="89852312388530152529",
            direction="out", commit_result={"ok": True}, commit_lock=asyncio.Lock(),
            release_attempts=0, release_result=None, release_operation_id="",
            release_unknown=False, release_state="", release_deadline=0.0,
            termination_task=None)
        gate = asyncio.Event()
        coordinator = Mock(side_effect=lambda _session: gate.wait())
        with patch.object(main.call_media.manager, "get", return_value=committed), \
                patch.object(main, "_supervise_cellular_termination", coordinator):
            first = asyncio.create_task(main.api_cellular_call_release("5", committed.call_id))
            second = asyncio.create_task(main.api_cellular_call_release("5", committed.call_id))
            for _ in range(5):
                await asyncio.sleep(0)
                if committed.termination_task is not None:
                    break
            self.assertIsNotNone(committed.termination_task)
            self.assertEqual(coordinator.call_count, 1)
            first.cancel()
            second.cancel()
            await asyncio.gather(first, second, return_exceptions=True)
            self.assertFalse(committed.termination_task.done())
        committed.termination_task.cancel()
        await asyncio.gather(committed.termination_task, return_exceptions=True)

    async def test_http_cancel_during_hangup_does_not_cancel_termination_owner(self):
        committed = types.SimpleNamespace(
            call_id="9" * 32, instance_iid="5", iccid="89852312388530152529",
            direction="out", commit_result={"ok": True}, commit_lock=asyncio.Lock(),
            release_attempts=0, release_result=None, release_operation_id="",
            release_unknown=False, release_state="", release_deadline=0.0,
            termination_task=None)
        entered = asyncio.Event()
        finish = asyncio.Event()

        async def rpc(_iccid, method, _params, **_kwargs):
            if method == "call.hangup":
                entered.set()
                await finish.wait()
                return {"ok": True, "terminal_confirmed": True, "status": "idle"}
            return {"ok": True, "status": "idle", "fresh": True,
                    "authoritative": True, "terminal_samples": 2}

        with patch.object(main.call_media.manager, "get", return_value=committed), \
                patch.object(main.modem_registry, "rpc", side_effect=rpc) as rpc_mock, \
                patch.object(main.store, "save_cellular_call_lease"), \
                patch.object(main, "_close_cellular_media", AsyncMock()) as close_media, \
                patch.object(main, "_resolve_cellular_call_alert", AsyncMock()):
            request = asyncio.create_task(
                main.api_cellular_call_release("5", committed.call_id))
            await asyncio.wait_for(entered.wait(), 1)
            owner = committed.termination_task
            request.cancel()
            await asyncio.gather(request, return_exceptions=True)
            self.assertFalse(owner.done())
            finish.set()
            result = await asyncio.wait_for(owner, 1)

        self.assertTrue(result["released"])
        close_media.assert_awaited_once_with(committed)
        hangups = [call for call in rpc_mock.await_args_list
                   if call.args[1] == "call.hangup"]
        self.assertEqual(len(hangups), 1)
        self.assertEqual(hangups[0].kwargs["operation_id"],
                         f"call-release:{committed.call_id}:1")

    async def test_termination_persistence_failure_still_attempts_hangup(self):
        committed = types.SimpleNamespace(
            call_id="d" * 32, instance_iid="5", iccid="89852312388530152529",
            direction="out", commit_result={"ok": True}, commit_lock=asyncio.Lock(),
            release_attempts=0, release_result=None, release_operation_id="",
            release_unknown=False, release_state="", release_deadline=0.0,
            termination_task=None)
        rpc = AsyncMock(side_effect=[
            {"ok": True, "terminal_confirmed": True, "status": "idle"},
            {"ok": True, "status": "idle", "fresh": True,
             "authoritative": True, "terminal_samples": 2},
        ])
        mark = Mock(side_effect=RuntimeError("disk full"))
        with patch.object(main.call_media.manager, "get", return_value=committed), \
                patch.object(main.modem_registry, "rpc", rpc), \
                patch.object(main.store, "mark_cellular_call_terminating", mark), \
                patch.object(main.store, "save_cellular_call_lease"), \
                patch.object(main, "_close_cellular_media", AsyncMock()):
            result = await main.api_cellular_call_release("5", committed.call_id)
        self.assertTrue(result["released"])
        self.assertEqual([call.args[1] for call in rpc.await_args_list],
                         ["call.hangup", "call.status"])

    async def test_paid_lease_stops_before_next_renew_after_release_published(self):
        session = types.SimpleNamespace(
            call_id="e" * 32, iccid="89852312388530152529",
            release_state="terminating", lease_last_healthy_at=0.0,
            media_status=lambda: {"ready": True})
        rpc = AsyncMock()
        with patch.object(main.call_media.manager, "get", return_value=session), \
                patch.object(main.modem_registry, "rpc", rpc), \
                patch.object(main.store, "save_cellular_call_lease"), \
                patch.object(main, "_close_cellular_media", AsyncMock()):
            await main._supervise_paid_call_lease(session)
        rpc.assert_not_awaited()

    async def test_release_flag_blocks_carrier_signal_while_coordinator_waits_for_commit_lock(self):
        session = types.SimpleNamespace(
            call_id="f" * 32, instance_iid="5", iccid="89852312388530152529",
            direction="out", commit_result=None, commit_lock=asyncio.Lock(),
            release_attempts=0, release_result=None, release_operation_id="",
            release_unknown=False, release_requested=False, release_state="",
            release_deadline=0.0, release_coordinator_task=None,
            termination_task=None)
        rpc = AsyncMock()
        with patch.object(main.call_media.manager, "get", return_value=session), \
                patch.object(main.modem_registry, "rpc", rpc), \
                patch.object(main.store, "save_cellular_call_lease"), \
                patch.object(main, "_close_cellular_media", AsyncMock()):
            await session.commit_lock.acquire()
            release = asyncio.create_task(
                main.api_cellular_call_release("5", session.call_id))
            await asyncio.sleep(0)
            self.assertTrue(session.release_requested)
            self.assertFalse(session.release_coordinator_task.done())
            result = await main._remote_call_signal_with_recovery(
                session, "call.dial", {"to": "+44123"},
                f"call-dial:{session.call_id}", 30)
            self.assertEqual(result["status"], "cancelled")
            rpc.assert_not_awaited()
            session.commit_lock.release()
            released = await asyncio.wait_for(release, 1)
            self.assertFalse(released["committed"])

    async def test_release_during_signalling_lease_write_prevents_dial_and_closes_lease(self):
        entered = threading.Event()
        finish = threading.Event()
        states = []
        session = types.SimpleNamespace(
            call_id="1a" * 16, instance_iid="5", iccid="89852312388530152529",
            direction="out", number="+44123", anchor_iid="5",
            commit_result=None, commit_lock=asyncio.Lock(),
            media_prepared=asyncio.Event(), release_attempts=0, release_result=None,
            release_operation_id="", release_unknown=False, release_requested=False,
            release_state="", release_deadline=0.0, release_coordinator_task=None,
            termination_task=None)
        session.media_prepared.set()

        def save(_call_id, _iid, _iccid, _direction, state):
            states.append(state)
            if state == "signalling":
                entered.set()
                self.assertTrue(finish.wait(2))
            return {}

        @main.asynccontextmanager
        async def admitted(_iid):
            yield True

        rpc = AsyncMock()
        with patch.object(main.call_media.manager, "get", return_value=session), \
                patch.object(main.remote_modem, "attached_iccid", return_value=session.iccid), \
                patch.object(main.cfg, "list_instances", return_value=[{"id": "5"}]), \
                patch.object(main, "_pcscf_admission_boundary", new=admitted), \
                patch.object(main, "_prepared_media_still_live", new=AsyncMock(return_value=True)), \
                patch.object(main.modem_registry, "rpc", rpc), \
                patch.object(main.store, "save_cellular_call_lease", side_effect=save), \
                patch.object(main, "_close_cellular_media", new=AsyncMock()):
            commit = asyncio.create_task(
                main.api_cellular_call_commit("5", session.call_id))
            self.assertTrue(await asyncio.to_thread(entered.wait, 1))
            release = asyncio.create_task(
                main.api_cellular_call_release("5", session.call_id))
            await asyncio.sleep(0)
            self.assertTrue(session.release_requested)
            finish.set()
            committed, released = await asyncio.gather(commit, release)

        self.assertEqual(committed["status"], "cancelled")
        self.assertFalse(released["committed"])
        rpc.assert_not_awaited()
        self.assertIn("signalling", states)
        self.assertIn("cancelled", states)

    async def test_release_during_media_live_check_owns_real_manager_close(self):
        iccid = "89852312388530152529"
        for direction in ("out", "in"):
            with self.subTest(direction=direction):
                manager = main.call_media.CallMediaManager()
                entered = asyncio.Event()
                finish = asyncio.Event()
                states = ["prepared"]

                async def media_live(_session):
                    entered.set()
                    await finish.wait()
                    return True

                @main.asynccontextmanager
                async def admitted(_iid):
                    yield True

                with patch.object(main.call_media, "manager", manager):
                    session = await manager.allocate(iccid, bind_host="127.0.0.1")
                    session.instance_iid = "5"
                    session.anchor_iid = "5"
                    session.direction = direction
                    session.number = "+44123"
                    session.media_prepared.set()

                    def save(_call_id, _iid, _iccid, _direction, state):
                        states.append(state)
                        return {}

                    rpc = AsyncMock()
                    if direction == "in":
                        rpc.side_effect = [
                            {"ok": True, "status": "idle", "terminal_confirmed": True},
                            {"ok": True, "status": "idle", "fresh": True,
                             "authoritative": True, "terminal_samples": 2},
                        ]
                    with patch.object(main.remote_modem, "attached_iccid", return_value=iccid), \
                            patch.object(main.cfg, "list_instances", return_value=[{"id": "5"}]), \
                            patch.object(main, "_pcscf_admission_boundary", new=admitted), \
                            patch.object(main, "_prepared_media_still_live", new=media_live), \
                            patch.object(main.modem_registry, "resolve", return_value=None), \
                            patch.object(main.modem_registry, "rpc", rpc), \
                            patch.object(main.store, "mark_cellular_call_terminating"), \
                            patch.object(main.store, "save_cellular_call_lease", side_effect=save), \
                            patch.object(main, "_resolve_cellular_call_alert", AsyncMock()):
                        endpoint = (main.api_cellular_call_commit if direction == "out"
                                    else main.api_cellular_incoming_answer)
                        commit = asyncio.create_task(endpoint("5", session.call_id))
                        await asyncio.wait_for(entered.wait(), 1)
                        release = asyncio.create_task(
                            main.api_cellular_call_release("5", session.call_id))
                        await asyncio.sleep(0)
                        self.assertTrue(session.release_requested)
                        finish.set()
                        commit_result, released = await asyncio.gather(
                            commit, release, return_exceptions=True)

                    self.assertIsInstance(commit_result, main.HTTPException)
                    self.assertTrue(released["released"])
                    self.assertFalse(released["committed"])
                    if direction == "in":
                        self.assertTrue(released["physical_hangup"])
                        self.assertEqual([call.args[1] for call in rpc.await_args_list],
                                         ["call.hangup", "call.status"])
                        self.assertIn("terminal_confirmed", states)
                    else:
                        rpc.assert_not_awaited()
                        self.assertIn("cancelled", states)
                    self.assertIsNone(manager.get(session.call_id))

    async def test_release_published_while_failure_close_awaits_cannot_remove_session(self):
        iccid = "89852312388530152529"
        manager = main.call_media.CallMediaManager()
        close_entered = asyncio.Event()
        close_continue = asyncio.Event()
        states = ["prepared"]
        audio_closes = 0

        async def rpc(_iccid, method, _params=None, **_kwargs):
            nonlocal audio_closes
            self.assertEqual(method, "audio.close")
            audio_closes += 1
            if audio_closes == 1:
                close_entered.set()
                await close_continue.wait()
            return {"ok": True}

        @main.asynccontextmanager
        async def admitted(_iid):
            yield True

        with patch.object(main.call_media, "manager", manager):
            session = await manager.allocate(iccid, bind_host="127.0.0.1")
            session.instance_iid = "5"
            session.anchor_iid = ""
            session.direction = "out"
            session.number = "+44123"
            session.media_prepared.set()

            def save(_call_id, _iid, _iccid, _direction, state):
                states.append(state)
                return {}

            with patch.object(main.remote_modem, "attached_iccid", return_value=iccid), \
                    patch.object(main.cfg, "list_instances", return_value=[{"id": "5"}]), \
                    patch.object(main, "_pcscf_admission_boundary", new=admitted), \
                    patch.object(main, "_prepared_media_still_live",
                                 new=AsyncMock(return_value=False)), \
                    patch.object(main.modem_registry, "resolve", return_value=object()), \
                    patch.object(main.modem_registry, "rpc", new=AsyncMock(side_effect=rpc)), \
                    patch.object(main.store, "save_cellular_call_lease", side_effect=save):
                commit = asyncio.create_task(
                    main.api_cellular_call_commit("5", session.call_id))
                await asyncio.wait_for(close_entered.wait(), 1)
                release = asyncio.create_task(
                    main.api_cellular_call_release("5", session.call_id))
                await asyncio.sleep(0)
                self.assertTrue(session.release_requested)
                close_continue.set()
                commit_result, released = await asyncio.gather(
                    commit, release, return_exceptions=True)

            self.assertIsInstance(commit_result, main.HTTPException)
            self.assertTrue(released["released"])
            self.assertIn("cancelled", states)
            self.assertIsNone(manager.get(session.call_id))
            self.assertGreaterEqual(audio_closes, 2)

    async def test_late_authoritative_terminal_closes_hangup_failed_session(self):
        iccid = "89852312388530152529"
        manager = main.call_media.CallMediaManager()
        with patch.object(main.call_media, "manager", manager):
            session = await manager.allocate(iccid, bind_host="127.0.0.1")
            session.instance_iid = "5"
            session.direction = "out"
            session.commit_result = {"ok": True, "status": "active"}
            session.release_requested = True
            session.release_state = "hangup_failed"
            observed = {"ok": True, "status": "idle", "fresh": True,
                        "authoritative": True, "terminal_samples": 2}
            with patch.object(main.modem_registry, "rpc",
                              new=AsyncMock(return_value=observed)), \
                    patch.object(main.modem_registry, "resolve", return_value=None), \
                    patch.object(main.store, "save_cellular_call_lease") as save, \
                    patch.object(main, "_resolve_cellular_call_alert", new=AsyncMock()):
                closed = await main._close_confirmed_terminal_cellular_media(session)

            self.assertTrue(closed)
            self.assertEqual(session.release_state, "terminated")
            self.assertIsNone(manager.get(session.call_id))
            self.assertEqual(save.call_args.args[-1], "terminal_confirmed")

    async def test_vowifi_enable_is_rejected_without_sim_apdu_capability(self):
        device_id = main._remote_modem_device_id("89852312388530152529")
        device = {"id": device_id, "capabilities": {}}
        with patch.object(main, "_unified_devices", AsyncMock(return_value=[device])), \
                patch.object(main, "_remote_modem_for_device", return_value={
                    "iccid": "89852312388530152529", "online": True,
                    "capabilities": {"cellular_data": True, "sim_apdu": False}}):
            with self.assertRaises(main.HTTPException) as raised:
                await main.api_device_capabilities(device_id, {"vowifi_enabled": True})
        self.assertEqual(raised.exception.status_code, 409)

    async def test_agent_reconnect_reapplies_persisted_radio_roaming_and_data_intent(self):
        iccid = "89852312388530152529"
        device_id = main._remote_modem_device_id(iccid)
        attachment = types.SimpleNamespace(iccid=iccid, status={"radio_enabled": False})
        rpc = AsyncMock(return_value={"ok": True})
        with patch.object(main.device_state, "desired", return_value={"devices": {
                device_id: {"flight_mode": False, "cellular_enabled": True,
                            "roaming_enabled": True}}}), \
                patch.object(main.modem_registry, "rpc", rpc):
            await main._reconcile_remote_modem_desired(attachment)
        self.assertEqual([call.args[1] for call in rpc.await_args_list], [
            "radio.set", "cellular.roaming.set", "cellular.ensure"])
        self.assertEqual(rpc.await_args_list[1].args[2], {"enabled": True})
        self.assertEqual(rpc.await_args_list[2].args[2], {"allow_roaming": True})

    async def test_agent_reconnect_retries_transient_attachment_race(self):
        iccid = "89852312388530152529"
        device_id = main._remote_modem_device_id(iccid)
        attachment = types.SimpleNamespace(iccid=iccid, status={"radio_enabled": False})
        rpc = AsyncMock(side_effect=[main.ModemUnavailable("modem disconnected"),
                                     {"ok": True}, {"ok": True}, {"ok": True}])
        with patch.object(main.device_state, "desired", return_value={"devices": {
                device_id: {"flight_mode": False, "cellular_enabled": True,
                            "roaming_enabled": True}}}), \
                patch.object(main.modem_registry, "resolve", return_value=attachment), \
                patch.object(main.modem_registry, "rpc", rpc), \
                patch.object(main.asyncio, "sleep", AsyncMock()) as sleep:
            await main._reconcile_remote_modem_desired_with_retry(attachment)
        sleep.assert_awaited_once_with(2)
        self.assertEqual([call.args[1] for call in rpc.await_args_list], [
            "radio.set", "radio.set", "cellular.roaming.set", "cellular.ensure"])

    async def test_reconcile_skips_radio_and_roaming_when_only_reverse_exit_is_missing(self):
        iccid = "89852312388530152529"
        device_id = main._remote_modem_device_id(iccid)
        attachment = types.SimpleNamespace(
            iccid=iccid,
            status={"radio_enabled": True, "roaming_allowed": True,
                    "data_active": True, "proxy": {"ready": False}},
        )
        rpc = AsyncMock(return_value={"ok": True, "proxy": {"ready": True}})
        with patch.object(main.device_state, "desired", return_value={"devices": {
                device_id: {"flight_mode": False, "cellular_enabled": True,
                            "roaming_enabled": True}}}), \
                patch.object(main.modem_registry, "rpc", rpc):
            self.assertTrue(await main._reconcile_remote_modem_desired(attachment))

        self.assertEqual([call.args[1] for call in rpc.await_args_list], ["cellular.ensure"])

    async def test_agent_reconcile_retries_an_unsuccessful_rpc_result(self):
        iccid = "89852312388530152529"
        device_id = main._remote_modem_device_id(iccid)
        attachment = types.SimpleNamespace(iccid=iccid, status={})
        rpc = AsyncMock(side_effect=[{"ok": True},
                                     {"ok": False, "error": "profile unavailable"}])
        with patch.object(main.device_state, "desired", return_value={"devices": {
                device_id: {"flight_mode": False, "cellular_enabled": True,
                            "roaming_enabled": True}}}), \
                patch.object(main.modem_registry, "rpc", rpc):
            self.assertFalse(await main._reconcile_remote_modem_desired(attachment))

    def test_remote_modem_reconcile_detects_later_proxy_loss(self):
        iccid = "89852312388530152529"
        device_id = main._remote_modem_device_id(iccid)
        attachment = types.SimpleNamespace(
            iccid=iccid,
            reverse_server=None, reverse_port=0,
            status={"data": "disconnected", "data_active": False,
                    "proxy": {"ready": False}, "radio_enabled": True,
                    "roaming_allowed": True})
        with patch.object(main.device_state, "desired", return_value={"devices": {
                device_id: {"flight_mode": False, "cellular_enabled": True,
                            "roaming_enabled": True}}}):
            self.assertTrue(main._remote_modem_needs_reconcile(attachment))
            attachment.status.update(
                {"data": "connected", "data_active": True, "proxy": {"ready": True}})
            attachment.reverse_server = object()
            attachment.reverse_port = 37177
            self.assertFalse(main._remote_modem_needs_reconcile(attachment))

    async def test_roaming_policy_block_is_converged_without_retrying_data(self):
        iccid = "89852312388530152529"
        device_id = main._remote_modem_device_id(iccid)
        attachment = types.SimpleNamespace(
            iccid=iccid,
            reverse_server=None, reverse_port=0,
            status={"registration": "roaming", "radio_enabled": True,
                    "roaming_allowed": False, "data_active": False,
                    "proxy": {"ready": False}},
        )
        rpc = AsyncMock(return_value={"ok": True})
        with patch.object(main.device_state, "desired", return_value={"devices": {
                device_id: {"flight_mode": False, "cellular_enabled": True,
                            "roaming_enabled": False}}}), \
                patch.object(main.modem_registry, "rpc", rpc):
            self.assertFalse(main._remote_modem_needs_reconcile(attachment))
            self.assertTrue(await main._reconcile_remote_modem_desired(attachment))
        rpc.assert_not_awaited()

    async def test_roaming_policy_block_disconnects_a_stale_bearer_once(self):
        iccid = "89852312388530152529"
        device_id = main._remote_modem_device_id(iccid)
        attachment = types.SimpleNamespace(
            iccid=iccid,
            reverse_server=object(), reverse_port=37177,
            status={"registration": "roaming", "radio_enabled": True,
                    "roaming_allowed": False, "data_active": True,
                    "proxy": {"ready": True}},
        )
        rpc = AsyncMock(return_value={"ok": True})
        with patch.object(main.device_state, "desired", return_value={"devices": {
                device_id: {"flight_mode": False, "cellular_enabled": True,
                            "roaming_enabled": False}}}), \
                patch.object(main.modem_registry, "rpc", rpc):
            self.assertTrue(main._remote_modem_needs_reconcile(attachment))
            self.assertTrue(await main._reconcile_remote_modem_desired(attachment))
        self.assertEqual([call.args[1] for call in rpc.await_args_list], ["cellular.disable"])

    def test_home_registration_retries_data_with_roaming_disabled(self):
        iccid = "89852312388530152529"
        device_id = main._remote_modem_device_id(iccid)
        attachment = types.SimpleNamespace(
            iccid=iccid,
            reverse_server=None, reverse_port=0,
            status={"registration": "home", "radio_enabled": True,
                    "roaming_allowed": False, "data_active": False,
                    "proxy": {"ready": False}},
        )
        with patch.object(main.device_state, "desired", return_value={"devices": {
                device_id: {"flight_mode": False, "cellular_enabled": True,
                            "roaming_enabled": False}}}):
            self.assertTrue(main._remote_modem_needs_reconcile(attachment))

    async def test_profile_save_is_forwarded_without_echoing_credentials(self):
        device_id = main._remote_modem_device_id("89852312388530152529")
        rpc = AsyncMock(return_value={"ok": True, "name": "MDD-HK", "apn": "ctnet",
                                      "platform": "windows"})
        with patch.object(main, "_remote_modem_for_device", return_value={
                "iccid": "89852312388530152529", "online": True}), \
                patch.object(main.modem_registry, "rpc", rpc):
            result = await main.api_device_cellular_profile_save(device_id, {
                "name": "MDD-HK", "apn": "ctnet", "auth": "PAP",
                "username": "alice", "password": "secret"})
        self.assertNotIn("password", result)
        self.assertNotIn("username", result)
        rpc.assert_awaited_once_with(
            "89852312388530152529", "cellular.profile.save",
            {"name": "MDD-HK", "apn": "ctnet", "auth": "PAP",
             "username": "alice", "password": "secret"}, timeout=30)

    async def test_profile_list_uses_current_iccid_attachment(self):
        device_id = main._remote_modem_device_id("89852312388530152529")
        rpc = AsyncMock(return_value={"ok": True, "supported": True,
                                      "profiles": [{"name": "MDD-HK"}]})
        with patch.object(main, "_remote_modem_for_device", return_value={
                "iccid": "89852312388530152529", "online": True}), \
                patch.object(main.modem_registry, "rpc", rpc):
            result = await main.api_device_cellular_profiles(device_id)
        self.assertEqual(result["profiles"], [{"name": "MDD-HK"}])
        rpc.assert_awaited_once_with(
            "89852312388530152529", "cellular.profile.list", timeout=20)

    async def test_remote_roaming_switch_uses_iccid_rpc_and_persists_intent(self):
        device_id = main._remote_modem_device_id("89852312388530152529")
        device = {
            "id": device_id, "device_type": "modem", "instance_id": "5",
            "capabilities": {
                "cellular": {"desired": False}, "vowifi": {"desired": False},
                "flight": {"desired": False}, "roaming": {"desired": False}},
        }
        rpc = AsyncMock(return_value={"ok": True, "roaming_allowed": True})
        desired = {"devices": {}, "defaults": {}}
        with patch.object(main, "_unified_devices", AsyncMock(return_value=[device])), \
                patch.object(main, "_remote_modem_for_device", return_value={
                    "iccid": "89852312388530152529", "online": True}), \
                patch.object(main.device_state, "desired", return_value=desired), \
                patch.object(main.device_state, "set_desired", Mock()) as save, \
                patch.object(main.modem_registry, "rpc", rpc), \
                patch.object(main.egress, "publish", Mock()), \
                patch.object(main.hub, "broadcast", AsyncMock()):
            result = await main.api_device_capabilities(
                device_id, {"roaming_enabled": True})
        self.assertEqual(result["id"], device_id)
        rpc.assert_awaited_once_with(
            "89852312388530152529", "cellular.roaming.set", {"enabled": True})
        self.assertTrue(save.call_args.kwargs["roaming_enabled"])


if __name__ == "__main__":
    unittest.main()
