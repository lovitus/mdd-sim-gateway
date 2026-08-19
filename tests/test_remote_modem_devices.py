import unittest
import types
import asyncio
from unittest.mock import AsyncMock, Mock, patch

from control.app import main


class RemoteModemDeviceTests(unittest.TestCase):
    def test_remote_modem_replaces_transport_reader_for_same_iccid(self):
        iccid = "89852312388530152529"
        reader = {
            "id": "reader-old", "device_type": "reader", "name": "Virtual PCD 00 04",
            "present": True, "imei": "862547055201716", "sim": {"iccid": iccid},
            "capabilities": {"vowifi": {"desired": True, "actual": "off"}},
        }
        remote = {
            "iccid": iccid, "online": True, "imei": "862547055201716", "model": "3GPP modem",
            "capabilities": {"cellular_data": True, "sms": True,
                             "call_signalling": False},
            "status": {"registration": "roaming", "operator": "China Telecom",
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
        self.assertEqual(devices[0]["instance_id"], "5")
        self.assertEqual(devices[0]["iccid"], iccid)
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

    def test_remote_modem_does_not_advertise_call_from_static_adapter_capability(self):
        iccid = "89852312388530152529"
        remote = {"iccid": iccid, "online": True,
                  "capabilities": {"cellular_data": True, "call_signalling": True},
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
    async def test_cellular_voice_prepare_fails_before_allocation_without_audio_self_test(self):
        attachment = types.SimpleNamespace(
            capabilities={"call_signalling": True, "call_audio": False},
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

    async def test_cellular_call_commit_is_single_idempotent_paid_rpc_after_media_ready(self):
        ready = asyncio.Event(); ready.set()
        session = types.SimpleNamespace(
            call_id="a" * 32, iccid="89852312388530152529",
            number="22333322", asterisk_ready=ready,
            commit_lock=asyncio.Lock(), commit_result=None,
            anchor_iid="", extension="")
        rpc = AsyncMock(return_value={"ok": True, "status": "dialing"})
        record = {"id": 9, "status": "ringing"}
        with patch.object(main.call_media.manager, "get", return_value=session), \
                patch.object(main.cfg, "list_instances", return_value=[{
                    "id": "5", "iccid": session.iccid}]), \
                patch.object(main.remote_modem, "attached_iccid", return_value=session.iccid), \
                patch.object(main.modem_registry, "rpc", rpc), \
                patch.object(main.store, "add_call", return_value=record), \
                patch.object(main.hub, "broadcast", AsyncMock()):
            result = await main.api_cellular_call_commit("5", session.call_id)
            retried = await main.api_cellular_call_commit("5", session.call_id)
        self.assertTrue(result["audio"])
        self.assertEqual(retried, result)
        rpc.assert_awaited_once_with(
            session.iccid, "call.dial", {"to": "22333322"},
            operation_id=f"call-dial:{session.call_id}", timeout=90)

    async def test_incoming_browser_rings_once_without_answering_modem(self):
        session = types.SimpleNamespace(
            call_id="b" * 32, direction="in", instance_iid="5", anchor_iid="1",
            extension="881234567890", number="+85246094054",
            ring_lock=asyncio.Lock(), ring_result=None)
        ami = types.SimpleNamespace(originate=AsyncMock(return_value={
            "ok": True, "detail": "Originate successfully queued"}))
        with patch.object(main.call_media.manager, "get", return_value=session), \
                patch.object(main.hub, "ami_for", AsyncMock(return_value=ami)):
            first = await main.api_cellular_incoming_ring("5", session.call_id)
            second = await main.api_cellular_incoming_ring("5", session.call_id)
        self.assertEqual(first, second)
        ami.originate.assert_awaited_once_with(
            session.extension, "webrtc", caller_id=session.number)

    async def test_incoming_answer_is_single_rpc_after_audio_socket_ready(self):
        ready = asyncio.Event(); ready.set()
        session = types.SimpleNamespace(
            call_id="c" * 32, iccid="89852312388530152529", direction="in",
            instance_iid="5", asterisk_ready=ready,
            commit_lock=asyncio.Lock(), commit_result=None)
        rpc = AsyncMock(return_value={"ok": True, "status": "active"})
        incoming = {"id": 10, "transport": "cellular", "status": "ringing"}
        with patch.object(main.call_media.manager, "get", return_value=session), \
                patch.object(main.cfg, "list_instances", return_value=[{
                    "id": "5", "iccid": session.iccid}]), \
                patch.object(main.remote_modem, "attached_iccid", return_value=session.iccid), \
                patch.object(main.modem_registry, "rpc", rpc), \
                patch.object(main.store, "get_open_call", return_value=incoming), \
                patch.object(main.store, "update_call") as update, \
                patch.object(main.hub, "broadcast", AsyncMock()):
            first = await main.api_cellular_incoming_answer("5", session.call_id)
            second = await main.api_cellular_incoming_answer("5", session.call_id)
        self.assertEqual(first, second)
        self.assertTrue(first["audio"])
        rpc.assert_awaited_once_with(
            session.iccid, "call.answer", {},
            operation_id=f"call-answer:{session.call_id}", timeout=30)
        update.assert_called_once_with(incoming["id"], "answered")

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
        attachment = types.SimpleNamespace(iccid=iccid, status={})
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
        attachment = types.SimpleNamespace(iccid=iccid)
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
        attachment = types.SimpleNamespace(iccid=iccid)
        rpc = AsyncMock(side_effect=[{"ok": True}, {"ok": True},
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
