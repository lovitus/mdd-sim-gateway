import unittest
from types import SimpleNamespace
from unittest.mock import AsyncMock, patch

from control.app import config, main


class AutoProvisionTests(unittest.TestCase):
    def setUp(self):
        self.draft = {
            "id": "2", "name": "234-33", "provisioning_state": "draft",
            "enabled": False, "imsi": "234330123456789", "mcc": "234", "mnc": "33",
            "iccid": "8944110000000000000", "smsc": "+447700900000",
            "debug": {"asterisk": True, "charon": False},
        }
        self.card = {
            "present": True, "index": 4, "name": "VoWiFi Modem test 00 01",
            "hardware_kind": "modem", "hardware_id": "test", "reader_port": "",
            "imsi": self.draft["imsi"], "mcc": "234", "mnc": "33",
            "iccid": self.draft["iccid"], "smsc": self.draft["smsc"],
            "pin_enabled": False,
            "virtual_slots": [
                {"index": 3, "name": "slot 0"},
                {"index": 4, "name": "slot 1"},
                {"index": 5, "name": "slot 2"},
            ],
        }

    @patch.object(main.egress, "publish")
    @patch.object(main.cfg, "upsert_instance")
    @patch.object(main, "_hardware_imei_for_card")
    def test_complete_draft_is_promoted_and_bound_to_modem_slots(
            self, hardware_imei, upsert, _publish):
        hardware_imei.return_value = ("490154203237518", "test", "modem")
        upsert.side_effect = lambda value, **kwargs: value

        result = main._auto_promote_card_draft(self.draft, self.card, [self.card])

        self.assertEqual(result["provisioning_state"], "ready")
        self.assertTrue(result["enabled"])
        self.assertEqual(result["reader_index"], 4)
        self.assertEqual(result["swu_reader"], "slot 1")
        self.assertEqual(result["imei_source_device_id"], "test")
        self.assertFalse(result["debug"]["asterisk"])
        self.assertEqual(len(result["imeisv"]), 16)

    @patch.object(main.cfg, "upsert_instance")
    @patch.object(main, "_hardware_imei_for_card")
    def test_incomplete_draft_stays_stopped(self, hardware_imei, upsert):
        hardware_imei.return_value = ("", "reader-test", "reader")

        result = main._auto_promote_card_draft(self.draft, self.card, [self.card])

        self.assertEqual(result["provisioning_state"], "draft")
        self.assertFalse(result["enabled"])
        self.assertIn("IMEI", result["auto_provision_missing"])
        upsert.assert_not_called()

    @patch.object(main.cfg, "upsert_instance")
    @patch.object(main, "_hardware_imei_for_card")
    def test_pin_locked_draft_waits_for_saved_pin(self, hardware_imei, upsert):
        hardware_imei.return_value = ("490154203237518", "test", "modem")
        self.card["pin_enabled"] = True

        result = main._auto_promote_card_draft(self.draft, self.card, [self.card])

        self.assertIn("SIM PIN", result["auto_provision_missing"])
        upsert.assert_not_called()

    @patch.object(main.cfg, "upsert_instance")
    @patch.object(main, "_hardware_imei_for_card")
    def test_ready_disabled_line_is_never_promoted(self, hardware_imei, upsert):
        ready = {**self.draft, "provisioning_state": "ready", "enabled": False}

        result = main._auto_promote_card_draft(ready, self.card, [self.card])

        self.assertIs(result, ready)
        hardware_imei.assert_not_called()
        upsert.assert_not_called()

    def test_giffgaff_profile_rebuilds_required_sip_identity(self):
        first = config.carrier_sip_defaults("234", "10", "test-card")
        again = config.carrier_sip_defaults("234", "010", "test-card")

        self.assertEqual(first, again)
        self.assertEqual(first["access_type"], "wlan1")
        self.assertTrue(first["user_eq_phone"])
        self.assertIn("country=GB", first["pani"])
        self.assertNotIn("ffffffffffff", first["pani"])

    def test_unknown_carrier_does_not_invent_sip_identity(self):
        self.assertEqual(config.carrier_sip_defaults("001", "01", "test-card"), {})

    def test_engine_render_uses_carrier_profile_but_keeps_explicit_overrides(self):
        base = {
            "id": "3", "index": 0, "imsi": "234100000000000",
            "mcc": "234", "mnc": "10", "iccid": "test-card",
            "imei": "490154203237518", "ami_secret": "test-secret",
            "sip": {"webrtc": {"enable": True, "password": "test-password"},
                    "access_type": "custom-access"},
        }

        rendered = config.render_instance_json(base, {})

        self.assertEqual(rendered["sip"]["access_type"], "custom-access")
        self.assertTrue(rendered["sip"]["user_eq_phone"])
        self.assertIn("country=GB", rendered["sip"]["pani"])

    def test_blank_sip_identity_fields_restore_carrier_defaults(self):
        merged = config.merge_carrier_sip_defaults("234", "10", "test-card", {
            "pani": "", "access_type": "", "user_eq_phone": False,
        })

        self.assertIn("country=GB", merged["pani"])
        self.assertEqual(merged["access_type"], "wlan1")
        self.assertFalse(merged["user_eq_phone"])

    @patch.object(main.egress, "publish")
    @patch.object(main.cfg, "upsert_instance")
    @patch.object(main, "_hardware_imei_for_card")
    def test_giffgaff_draft_promotion_applies_carrier_profile(
            self, hardware_imei, upsert, _publish):
        draft = {**self.draft, "mcc": "234", "mnc": "10", "sip": {
            "listen_addr": "0.0.0.0", "transport": "udp"}}
        card = {**self.card, "mcc": "234", "mnc": "10"}
        hardware_imei.return_value = ("490154203237518", "test", "modem")
        upsert.side_effect = lambda value, **kwargs: value

        result = main._auto_promote_card_draft(draft, card, [card])

        self.assertEqual(result["sip"]["access_type"], "wlan1")
        self.assertTrue(result["sip"]["user_eq_phone"])
        self.assertIn("country=GB", result["sip"]["pani"])


class ImsIdentityLearningTests(unittest.IsolatedAsyncioTestCase):
    def test_modemmanager_number_requires_ims_confirmation(self):
        self.assertTrue(main._needs_ims_msisdn_learning({
            "msisdn": "447000000000", "msisdn_source": "modemmanager"}))
        self.assertTrue(main._needs_ims_msisdn_learning({"msisdn": ""}))
        self.assertFalse(main._needs_ims_msisdn_learning({
            "msisdn": "+447000000001", "msisdn_source": "ims"}))
        self.assertFalse(main._needs_ims_msisdn_learning({
            "msisdn": "+447000000001", "msisdn_source": "manual"}))

    async def test_ims_correction_is_persisted_and_applied_to_running_engine(self):
        current = {"id": "2", "msisdn": "447000000000",
                   "msisdn_source": "modemmanager"}
        corrected = {**current, "msisdn": "+447000000001", "msisdn_source": "ims"}
        with patch.object(main.asyncio, "sleep", new=AsyncMock()), \
                patch.object(main.engine, "exec_cli", return_value=""), \
                patch.object(main, "extract_msisdn", return_value=corrected["msisdn"]), \
                patch.object(main.cfg, "get_instance", return_value=current), \
                patch.object(main.cfg, "upsert_instance", return_value=corrected) as upsert, \
                patch.object(main.cfg, "get_settings", return_value={}), \
                patch.object(main.engine, "is_running", return_value=True), \
                patch.object(main, "_start_engine_checked") as restart, \
                patch.object(main.hub, "drop_ami", new=AsyncMock()) as drop_ami, \
                patch.object(main.hub, "broadcast", new=AsyncMock()), \
                patch.object(main.hub, "reset_health"):
            await main.learn_msisdn("2")

        upsert.assert_called_once_with({"id": "2", "msisdn": "+447000000001",
                                        "msisdn_source": "ims"})
        drop_ami.assert_awaited_once_with("2")
        restart.assert_called_once_with(corrected, {}, False)


class EsimProfileRefreshTests(unittest.IsolatedAsyncioTestCase):
    async def test_new_active_profile_creates_line_and_schedules_auto_start(self):
        card = SimpleNamespace(
            iccid="89441000400130120985", imsi="234100000000001",
            mcc="234", mnc="10", pin_enabled=False, pin_tries=3,
            smsc="+447785016005",
        )
        draft = {"id": "5", "iccid": card.iccid, "provisioning_state": "draft"}
        scheduled = []

        def capture(coro):
            scheduled.append(coro)
            coro.close()

        with patch.dict(main.hub.cards, {"Reader": {
                "index": 4, "name": "Reader", "present": True,
                "reader_port": "1-1.2", "matched": "4",
                "iccid": "89441000400130000000",
        }}, clear=True), \
                patch.object(main.sim, "read_card", return_value=card), \
                patch.object(main, "_match_instance_by_iccid", return_value=None), \
                patch.object(main.cfg, "card_auto_create_suppressed", return_value=False), \
                patch.object(main, "_ensure_card_draft", return_value=draft) as ensure, \
                patch.object(main.hub, "broadcast", new=AsyncMock()) as broadcast, \
                patch.object(main.asyncio, "create_task", side_effect=capture):
            result = await main._esim_refresh_card("Reader", 4)

        self.assertEqual(result["iccid"], card.iccid)
        self.assertEqual(result["matched"], "5")
        ensure.assert_called_once()
        broadcast.assert_awaited_once()
        self.assertEqual(len(scheduled), 1)


if __name__ == "__main__":
    unittest.main()
