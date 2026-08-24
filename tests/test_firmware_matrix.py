import unittest
from unittest.mock import patch

from control.app import firmware_matrix, main


class RevisionParsingTests(unittest.TestCase):
    def test_branch_and_baseline_come_from_the_revision_not_the_model(self):
        parsed = firmware_matrix.parse_revision("EC20CEHDLGR08A06M1G")
        self.assertEqual(parsed["branch"], "EC20CEHDLG")
        self.assertEqual(parsed["baseline"], "R08")
        self.assertEqual(parsed["build"], "A06M1G")

    def test_unparseable_revision_never_invents_a_branch(self):
        for value in ("", "EC20F", "Quectel EC20F", "unknown"):
            parsed = firmware_matrix.parse_revision(value)
            self.assertEqual(parsed["branch"], "", value)
            self.assertEqual(parsed["baseline"], "", value)


class AdviceTests(unittest.TestCase):
    def test_verified_baseline_produces_no_prompt(self):
        advice = firmware_matrix.advise("EC20CEHDLGR08A06M1G", model="Quectel EC20F")
        self.assertEqual(advice["state"], "verified")
        self.assertEqual(advice["reason"], "")
        self.assertFalse(advice["guided_upgrade"])
        self.assertEqual(advice["impact"], [])

    def test_known_deficient_baseline_names_the_affected_capability(self):
        advice = firmware_matrix.advise("EC20CEHDLGR06A13M1G", model="Quectel EC20F")
        self.assertEqual(advice["state"], "action_required")
        self.assertIn("sms", advice["impact"])
        self.assertEqual(advice["recommended"], "EC20CEHDLGR08A06M1G")
        self.assertIn("IMS", advice["reason"])

    def test_cross_baseline_move_is_never_offered_as_a_guided_upgrade(self):
        advice = firmware_matrix.advise("EC20CEHDLGR06A13M1G")
        self.assertFalse(advice["same_baseline"])
        self.assertFalse(advice["guided_upgrade"])
        self.assertTrue(advice["requires_service"])
        self.assertTrue(advice["doc"])

    def test_same_baseline_without_a_recorded_package_digest_is_not_guided(self):
        """A guided upgrade must prove which signed image would be installed."""
        branch = firmware_matrix.Branch(
            verified=frozenset({"XXNNNNR08A09"}), target="XXNNNNR08A09",
            deficient={"XXNNNNR08A01": firmware_matrix.Deficiency(
                impact=("sms",), detail="Baseline defect.")},
            target_package_sha256="", cross_baseline_requires_service=False)
        with patch.dict(firmware_matrix.MATRIX, {"XXNNNN": branch}, clear=False):
            advice = firmware_matrix.advise("XXNNNNR08A01")
        self.assertTrue(advice["same_baseline"])
        self.assertFalse(advice["guided_upgrade"])
        self.assertTrue(advice["requires_service"])

    def test_same_baseline_with_a_recorded_digest_is_guided(self):
        branch = firmware_matrix.Branch(
            verified=frozenset({"XXNNNNR08A09"}), target="XXNNNNR08A09",
            deficient={"XXNNNNR08A01": firmware_matrix.Deficiency(
                impact=("sms",), detail="Baseline defect.")},
            target_package_sha256="a" * 64, cross_baseline_requires_service=False)
        with patch.dict(firmware_matrix.MATRIX, {"XXNNNN": branch}, clear=False):
            advice = firmware_matrix.advise("XXNNNNR08A01")
        self.assertTrue(advice["guided_upgrade"])
        self.assertFalse(advice["requires_service"])

    def test_unrecorded_branch_asks_for_manual_verification_only(self):
        advice = firmware_matrix.advise("ZZ99XYR01A01")
        self.assertEqual(advice["state"], "unknown")
        self.assertEqual(advice["recommended"], "")
        self.assertFalse(advice["guided_upgrade"])
        self.assertEqual(advice["impact"], [])

    def test_known_branch_with_an_unverified_baseline_stays_unknown(self):
        advice = firmware_matrix.advise("EC20CEHDLGR07A01M1G")
        self.assertEqual(advice["state"], "unknown")
        self.assertFalse(advice["guided_upgrade"])
        self.assertEqual(advice["impact"], [])

    def test_missing_revision_is_reported_as_unreported(self):
        advice = firmware_matrix.advise("", model="Quectel EC20F")
        self.assertEqual(advice["state"], "unreported")
        self.assertIn("AT+GMR", advice["reason"])
        self.assertFalse(advice["guided_upgrade"])

    def test_the_matrix_records_no_download_or_flash_action(self):
        """Detection only: an entry may name a document, never a URL or command."""
        for name, branch in firmware_matrix.MATRIX.items():
            self.assertNotIn("://", branch.doc, name)
            for revision in branch.verified | frozenset(branch.deficient):
                self.assertRegex(revision, r"^[A-Z0-9]+$", name)


class DevicePayloadTests(unittest.TestCase):
    """The device page is the only place this verdict can prevent a repeat investigation."""

    ICCID = "89852312388530152529"

    def _merge(self, *, firmware: str, status: dict) -> dict:
        remote = {
            "iccid": self.ICCID, "online": True, "imei": "862547055201716",
            "model": "Quectel EC20F", "firmware": firmware,
            "capabilities": {"cellular_data": True, "sms": True},
            "status": {"registration": "home", "radio_enabled": True, **status},
        }
        with patch.object(main.modem_registry, "list", return_value=[remote]), \
                patch.object(main.device_state, "desired", return_value={
                    "devices": {}, "defaults": {"cellular_enabled": True,
                                                "vowifi_enabled": True, "flight_mode": False,
                                                "roaming_enabled": True}}), \
                patch.object(main, "_match_instance_by_iccid", return_value=None):
            devices = main._merge_remote_modem_devices([])
        self.assertEqual(len(devices), 1)
        return devices[0]

    def test_deficient_baseline_reaches_the_device_page_with_its_sms_impact(self):
        device = self._merge(firmware="EC20CEHDLGR06A13M1G",
                             status={"sms_ready": True,
                                     "sms_service_center": "+85362101201"})
        self.assertEqual(device["firmware"], "EC20CEHDLGR06A13M1G")
        self.assertEqual(device["firmware_advice"]["state"], "action_required")
        # `sms_ready` is True here: the advisory must still be published, because that is
        # exactly the combination that produced an unexplained submit failure.
        self.assertTrue(device["sms_diagnostics"]["advisory"])
        self.assertEqual(device["sms_diagnostics"]["service_center"], "+85362101201")

    def test_absent_sms_centre_is_published_as_a_fact_not_a_warning(self):
        """Real hardware reported an empty MBN centre while SMS submission worked.

        Warning on emptiness would mark a working device as suspect, which is the kind of
        false signal that makes the page untrustworthy.
        """
        device = self._merge(firmware="EC20CEHDLGR08A06M1G",
                             status={"sms_ready": True, "sms_service_center": ""})
        self.assertEqual(device["firmware_advice"]["state"], "verified")
        self.assertEqual(device["sms_diagnostics"]["service_center"], "")
        self.assertEqual(device["sms_diagnostics"]["advisory"], [])

    def test_verified_baseline_with_a_centre_raises_no_advisory(self):
        device = self._merge(firmware="EC20CEHDLGR08A06M1G",
                             status={"sms_ready": True,
                                     "sms_service_center": "+85362101201"})
        self.assertEqual(device["sms_diagnostics"]["advisory"], [])
        self.assertEqual(device["firmware_advice"]["reason"], "")

    def test_changed_sms_centre_advisory_reaches_the_device_page(self):
        device = self._merge(firmware="EC20CEHDLGR08A06M1G", status={
            "sms_ready": True,
            "sms_service_center": "+85290000000",
            "sms_service_center_changed": True,
            "sms_service_center_advisory": "SMSC changed after the last successful send.",
        })
        self.assertEqual(device["sms_diagnostics"]["service_center"], "+85290000000")
        self.assertEqual(device["sms_diagnostics"]["advisory"],
                         ["SMSC changed after the last successful send."])


if __name__ == "__main__":
    unittest.main()
