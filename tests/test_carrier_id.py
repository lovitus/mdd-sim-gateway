import unittest

from control.app import carrier_id


class CarrierIdTests(unittest.TestCase):
    def test_plain_plmn_resolves_the_home_network(self):
        value = carrier_id.lookup({"mcc": "234", "mnc": "10"})
        self.assertEqual(value["name"], "O2")
        self.assertEqual(value["home_network"], "O2")
        self.assertEqual(value["plmn"], "234-10")
        self.assertFalse(value["specific"])

    def test_spn_selects_a_specific_mvno(self):
        value = carrier_id.lookup({
            "mcc": "234", "mnc": "10",
            "carrier_identity": {"spn": "GIFFGAFF"},
        })
        self.assertEqual(value["name"], "giffgaff")
        self.assertEqual(value["home_network"], "O2")
        self.assertEqual(value["match_source"], "mccmnc+spn")
        self.assertTrue(value["specific"])

    def test_gid_prefix_is_case_insensitive_and_tolerates_sim_padding(self):
        value = carrier_id.lookup({
            "mcc": "310", "mnc": "240",
            "carrier_identity": {"gid1": "354dffffffff"},
        })
        self.assertEqual(value["name"], "Ultra/Univision")
        self.assertEqual(value["home_network"], "T-Mobile - US")

    def test_legacy_three_digit_padding_recovers_a_two_digit_mnc(self):
        value = carrier_id.lookup({"mcc": "234", "mnc": "015"})
        self.assertEqual(value["name"], "Vodafone")
        self.assertEqual(value["plmn"], "234-15")

    def test_unknown_plmn_stays_explicit(self):
        value = carrier_id.lookup({"mcc": "999", "mnc": "99"})
        self.assertEqual(value["name"], "")
        self.assertEqual(value["plmn"], "999-99")
        self.assertEqual(value["database"], "none")

    def test_imsi_mnc_is_inferred_only_for_unambiguous_mcc_length(self):
        self.assertEqual(carrier_id.infer_mnc_from_imsi("455070885002578"),
                         ("07", "imsi+plmn-length"))

    def test_imsi_mnc_stays_unknown_for_mixed_length_mcc(self):
        # North America contains both two- and three-digit assignments in the table.
        self.assertEqual(carrier_id.infer_mnc_from_imsi("310260123456789"), ("", ""))


if __name__ == "__main__":
    unittest.main()
