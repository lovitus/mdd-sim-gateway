import types
import sys
import re
import unittest
from unittest.mock import Mock, patch

from agent.cellular_isolation import IsolationGuard
sys.modules.setdefault("websocket", types.SimpleNamespace())
from agent.modem_agent import ModemCard, ModemControl, windows_mbn_profile_xml


class ModemAgentSafetyTests(unittest.TestCase):
    def test_cellular_interface_reuses_active_guard_attachment(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(imei="123456789012345")
        control = ModemControl(args, modem)
        control.isolation = types.SimpleNamespace(interface="Cellular 2")
        with patch("agent.modem_agent.subprocess.run") as run:
            self.assertEqual(control._cellular_interface(), "Cellular 2")
        run.assert_not_called()
        control.stop.set()

    def _control(self, **modem_fields):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(imei="123456789012345", **modem_fields)
        control = ModemControl(args, modem)
        self.addCleanup(control.stop.set)
        return control

    def test_apn_guidance_names_the_network_when_no_candidate_exists(self):
        """"No profile is configured" is a dead end for a carrier outside any APN database."""
        control = self._control(imsi="999991234567890")
        control._modem_apn_candidates = Mock(return_value=[])

        message = control._apn_guidance([])

        self.assertIn("no usable APN", message)
        self.assertIn("MCC/MNC 99999", message)
        self.assertNotIn("999991234567890", message)  # never echo the full IMSI

    def test_apn_guidance_lists_the_candidates_the_modem_reported(self):
        control = self._control(imsi="454031234567890")
        control._modem_apn_candidates = Mock(return_value=["ctnet", "ctwap"])

        message = control._apn_guidance([])

        self.assertIn("2 APN candidates", message)
        self.assertIn("ctnet, ctwap", message)

    def test_apn_guidance_reports_ambiguous_system_profiles_first(self):
        control = self._control(imsi="")
        control._modem_apn_candidates = Mock(return_value=["ctnet"])

        message = control._apn_guidance(["Profile A", "Profile B"])

        self.assertIn("More than one mobile-broadband profile", message)
        self.assertIn("Profile A, Profile B", message)

    def test_apn_guidance_includes_operator_name_when_known(self):
        control = self._control(imsi="999991234567890", operator="China Telecom")
        control._modem_apn_candidates = Mock(return_value=[])

        message = control._apn_guidance([])

        self.assertIn("no usable APN", message)
        self.assertIn("China Telecom (MCC/MNC 99999)", message)

    def test_windows_cellular_ip_falls_back_to_netsh_when_cim_fails(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="Cellular 2", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(imei="123456789012345")
        control = ModemControl(args, modem)
        failed_cim = types.SimpleNamespace(stdout="255.255.0.0\n",
                                           stderr="A general error occurred")
        netsh = types.SimpleNamespace(stdout=(
            'Configuration for interface "Cellular 2"\n'
            '    IP Address: 10.191.87.210\n'
            '    Subnet Prefix: 10.191.87.208/30\n'))
        with patch("agent.modem_agent.os.name", "nt"), \
                patch("agent.modem_agent.subprocess.run",
                      side_effect=[failed_cim, netsh]) as run:
            self.assertEqual(control._cellular_ip("Cellular 2"), "10.191.87.210")
        self.assertEqual(run.call_args_list[1].args[0][:5],
                         ["netsh", "interface", "ipv4", "show", "addresses"])
        control.stop.set()

    def test_sms_bearer_prefers_packet_domain_when_only_lte_is_registered(self):
        modem = ModemCard("COM16", 115200)
        modem._at = Mock(side_effect=[
            b"+CREG: 2,2\r\nOK\r\n",
            b'+CGREG: 2,5,"7721","6DD2102",7\r\nOK\r\n',
            b"+CEREG: 0,5\r\nOK\r\n",
            b"+CGSMS: (0-3)\r\nOK\r\n",
            b"+CGSMS: 1\r\nOK\r\n",
            b"OK\r\n",
        ])

        result = modem._prepare_sms_bearer()

        self.assertTrue(result["changed"])
        self.assertEqual(result["selected"], 2)
        self.assertEqual(modem._at.call_args_list[-1].args[0], "AT+CGSMS=2")

    def test_sms_bearer_does_not_change_a_registered_circuit_switched_path(self):
        modem = ModemCard("COM16", 115200)
        modem._at = Mock(side_effect=[
            b"+CREG: 2,5\r\nOK\r\n",
            b"+CGREG: 2,5\r\nOK\r\n",
            b"+CEREG: 0,5\r\nOK\r\n",
        ])

        result = modem._prepare_sms_bearer()

        self.assertFalse(result["changed"])
        self.assertEqual(modem._at.call_count, 3)

    def test_sms_readiness_rejects_lte_without_cs_or_ims(self):
        modem = ModemCard("COM16", 115200)
        modem._at = Mock(side_effect=[
            b"+CREG: 0,2\r\nOK\r\n",
            b"+CGREG: 0,5\r\nOK\r\n",
            b"+CEREG: 0,5\r\nOK\r\n",
            b'+QCFG: "ims",1,0\r\nOK\r\n',
        ])

        result = modem.sms_submit_readiness(force=True)

        self.assertFalse(result["ready"])
        self.assertEqual(result["ims"], 0)
        self.assertIn("neither circuit-switched registration nor an available IMS", result["reason"])

    def test_sms_send_does_not_invoke_provider_without_network_bearer(self):
        provider = Mock()
        provider.status.return_value = {"sms_readiness_authoritative": False}
        modem = ModemCard("COM16", 115200, platform_provider=provider)
        modem.sms_submit_readiness = Mock(return_value={
            "ready": False, "reason": "SMS bearer unavailable",
        })

        with self.assertRaisesRegex(RuntimeError, "SMS bearer unavailable"):
            modem.sms_send("+85246094148", "test")
        provider.sms_send.assert_not_called()

    def test_authoritative_platform_sms_bypasses_auxiliary_at_bearer_guess(self):
        provider = Mock()
        provider.status.return_value = {
            "sms_readiness_authoritative": True, "sms_ready": True,
            "sms_provider": "windows_mbn",
        }
        provider.sms_send.return_value = {"ok": True, "status": "sent"}
        modem = ModemCard("COM16", 115200, platform_provider=provider)
        modem.sms_submit_readiness = Mock()
        modem._prepare_sms_bearer = Mock()

        result = modem.sms_send("+85246094148", "test")

        self.assertTrue(result["ok"])
        provider.sms_send.assert_called_once_with("+85246094148", "test")
        modem.sms_submit_readiness.assert_not_called()
        modem._prepare_sms_bearer.assert_not_called()

    def test_revision_is_read_from_a_labelled_line_not_a_field_position(self):
        modem = ModemCard("COM16", 115200)
        self.assertEqual(
            modem._revision_from(["Quectel", "EC20F", "Revision: EC20CEHDLGR08A06M1G"]),
            "EC20CEHDLGR08A06M1G")
        self.assertEqual(
            modem._revision_from("\r\nEC20CEHDLGR06A13M1G\r\n\r\nOK\r\n"),
            "EC20CEHDLGR06A13M1G")

    def test_revision_stays_empty_when_the_modem_reports_none(self):
        """An invented revision would be checked against the matrix and could mislead."""
        modem = ModemCard("COM16", 115200)
        for output in ("", "OK", "ATI\r\nOK", "Quectel\r\nEC20F\r\nOK"):
            self.assertEqual(modem._revision_from(output), "", output)

    def test_service_centre_is_read_only_and_cached(self):
        modem = ModemCard("COM16", 115200)
        modem._at = Mock(return_value=b'+CSCA: "+85362101201",145\r\nOK\r\n')

        self.assertEqual(modem.service_centre(), "+85362101201")
        self.assertEqual(modem.service_centre(), "+85362101201")

        modem._at.assert_called_once_with("AT+CSCA?")

    def test_absent_service_centre_is_reported_without_a_write(self):
        modem = ModemCard("COM16", 115200)
        modem._at = Mock(return_value=b'+CSCA: "",129\r\nOK\r\n')

        self.assertEqual(modem.service_centre(), "")

        self.assertEqual([call.args[0] for call in modem._at.call_args_list], ["AT+CSCA?"])

    def test_empty_platform_service_centre_falls_back_to_the_at_view(self):
        """Observed on real hardware: MBN reports no centre while submission works.

        An empty platform field is missing information, not an absent centre, so it must not
        become the published answer while an AT function can still read EF_SMSP.
        """
        provider = Mock()
        provider.status.return_value = {"sms_service_center": ""}
        modem = ModemCard("COM16", 115200, platform_provider=provider)
        modem._at = Mock(return_value=b'+CSCA: "+85362101201",145\r\nOK\r\n')

        self.assertEqual(modem.service_centre(), "+85362101201")

    def test_platform_service_centre_wins_when_it_is_present(self):
        provider = Mock()
        provider.status.return_value = {"sms_service_center": "+8613800100500"}
        modem = ModemCard("COM16", 115200, platform_provider=provider)
        modem._at = Mock()

        self.assertEqual(modem.service_centre(), "+8613800100500")

        modem._at.assert_not_called()

    def test_submit_failure_names_the_missing_sms_centre(self):
        """An unspecified submit error is indistinguishable from a gateway defect."""
        provider = Mock()
        provider.status.return_value = {"sms_readiness_authoritative": True, "sms_ready": True,
                                        "sms_service_center": ""}
        provider.sms_send.side_effect = RuntimeError("error 350, message reference=-1")
        modem = ModemCard("COM16", 115200, platform_provider=provider)

        with self.assertRaises(Exception) as caught:
            modem.sms_send("+85246094148", "test")

        detail = str(caught.exception)
        self.assertIn("error 350", detail)
        self.assertIn("no SMS centre", detail)
        provider.sms_send.assert_called_once()

    def test_submit_failure_records_the_centre_that_was_in_effect(self):
        provider = Mock()
        provider.status.return_value = {"sms_readiness_authoritative": True, "sms_ready": True,
                                        "sms_service_center": "+85362101201"}
        provider.sms_send.side_effect = RuntimeError("submit rejected")
        modem = ModemCard("COM16", 115200, platform_provider=provider)

        with self.assertRaises(Exception) as caught:
            modem.sms_send("+85246094148", "test")

        self.assertIn("SMS centre +85362101201", str(caught.exception))

    def test_call_dial_does_not_invoke_provider_without_network_bearer(self):
        provider = Mock()
        modem = ModemCard("COM16", 115200, platform_provider=provider)
        modem.capabilities["call"] = True
        modem.sms_submit_readiness = Mock(return_value={
            "ready": False, "reason": "No CS registration or IMS session",
        })
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="Cellular 2", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        control = ModemControl(args, modem)

        result = control.execute("call.dial", {"to": "22333322"})

        self.assertTrue(result["unavailable"])
        provider.call_dial.assert_not_called()
        control.stop.set()

    def test_windows_mbn_identity_falls_back_when_at_sim_channel_is_unavailable(self):
        modem = ModemCard("COM14", 115200)
        modem.imei = "862547055201716"
        interfaces = types.SimpleNamespace(
            stdout=("Name : Cellular 2\nDevice Id : 862547055201716\n"))
        ready = types.SimpleNamespace(
            stdout=("Subscriber Id : 455070885002522\n"
                    "SIM ICC Id : 89852312388530152529\n"))
        with patch("agent.modem_agent.os.name", "nt"), \
                patch("agent.modem_agent.subprocess.run", side_effect=[interfaces, ready]):
            self.assertEqual(modem._windows_mbn_identity(),
                             ("89852312388530152529", "455070885002522"))

    def test_windows_mbn_owned_sim_does_not_advertise_at_sms_or_call(self):
        modem = ModemCard("COM14", 115200)
        modem.sim_via_mbn = True
        modem.capabilities.update(sms=False, call=False)
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="Cellular 2", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        control = ModemControl(args, modem)
        sms = control.execute("sms.send", {"to": "+85360000000", "body": "test"})
        call = control.execute("call.dial", {"to": "22333322"})
        self.assertTrue(sms["unavailable"])
        self.assertTrue(call["unavailable"])
        control.stop.set()

    def test_reverse_tunnel_reuses_the_guarded_proxy_admission_snapshot(self):
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345")
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="Cellular 2", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        control = ModemControl(args, modem)
        control.socks_server = types.SimpleNamespace(ready=True, source_ip="10.0.0.8")
        control.isolation = types.SimpleNamespace(active=True, interface="Cellular 2")
        control._cellular_ip = Mock(return_value="10.0.0.8")
        control._status = Mock(return_value={"data": "connected"})
        self.assertEqual(control._reverse_tunnel_source_ip(), "10.0.0.8")
        control._cellular_ip.assert_not_called()
        control._status.assert_not_called()
        control.isolation.active = False
        with self.assertRaisesRegex(OSError, "isolation"):
            control._reverse_tunnel_source_ip()
        control.stop.set()

    def test_vpcd_reset_keeps_healthy_at_port_when_cpin_is_owned_by_mbim(self):
        modem = ModemCard("COM14", 115200)
        modem.serial = Mock()
        modem._at = Mock(side_effect=[RuntimeError("SIM failure"), b"OK"])
        modem.close = Mock()
        modem.reset()
        self.assertEqual([call.args[0] for call in modem._at.call_args_list],
                         ["AT+CPIN?", "AT"])
        modem.close.assert_not_called()

    def test_vpcd_reset_closes_dead_at_port(self):
        modem = ModemCard("COM14", 115200)
        modem.serial = Mock()
        modem._at = Mock(side_effect=RuntimeError("port gone"))
        modem.close = Mock()
        modem.reset()
        modem.close.assert_called_once()

    def test_auto_port_is_rediscovered_after_usb_renumbering(self):
        modem = ModemCard("auto", 115200)
        modem.port_name = "COM7"  # the previously successful, now stale attachment
        modem._connect_port = Mock(side_effect=lambda value: value == "COM14")
        ports = [types.SimpleNamespace(device="COM8", description="USB serial"),
                 types.SimpleNamespace(device="COM14", description="USB serial")]
        with patch("agent.modem_agent.list_ports.comports", return_value=ports):
            self.assertTrue(modem.connect())
        self.assertEqual([call.args[0] for call in modem._connect_port.call_args_list],
                         ["COM8", "COM14"])

    def test_explicit_port_remains_pinned(self):
        modem = ModemCard("COM9", 115200)
        modem._connect_port = Mock(return_value=False)
        with patch("agent.modem_agent.list_ports.comports") as enumerate_ports:
            self.assertFalse(modem.connect())
        modem._connect_port.assert_called_once_with("COM9")
        enumerate_ports.assert_not_called()

    def test_auto_port_prioritizes_at_port_over_bluetooth_dm_and_nmea(self):
        modem = ModemCard("auto", 115200)
        modem._connect_port = Mock(side_effect=lambda value: value == "COM14")
        ports = [
            types.SimpleNamespace(device="COM7", description="Bluetooth serial link"),
            types.SimpleNamespace(device="COM13", description="USB DM Port"),
            types.SimpleNamespace(device="COM15", description="USB NMEA Port"),
            types.SimpleNamespace(device="COM14", description="USB AT Port"),
        ]
        with patch("agent.modem_agent.list_ports.comports", return_value=ports):
            self.assertTrue(modem.connect())
        modem._connect_port.assert_called_once_with("COM14")

    def test_windows_never_falls_back_to_at_when_mbn_is_temporarily_missing(self):
        modem = ModemCard("COM14", 115200)
        modem._at = Mock(side_effect=[b"OK", b"OK", b"OK", b"862547055201716\r\nOK"])
        with patch("agent.modem_agent.os.name", "nt"), \
                patch("agent.modem_agent.serial.Serial", return_value=Mock()), \
                patch("agent.modem_agent.WindowsMbnProvider.discover", return_value=None):
            self.assertFalse(modem._connect_port("COM14"))
        self.assertIsNone(modem.connection)
        self.assertFalse(modem.capabilities["sms"])
        self.assertFalse(modem.capabilities["call"])

    def test_cnum_pattern_accepts_international_number_and_not_empty_response(self):
        raw = '+CNUM: "","+85361234567",145\r\nOK\r\n'
        match = re.search(r'"(\+?\d{5,20})"', raw)
        self.assertEqual(match.group(1), "+85361234567")
        self.assertIsNone(re.search(r'"(\+?\d{5,20})"', "OK\r\n"))

    def test_windows_profile_uses_imsi_and_never_sets_sim_iccid(self):
        value = windows_mbn_profile_xml(
            "MDD & SIM", "460031234567890", "ctnet&x", "PAP", "alice", "s<ecret")
        self.assertIn("<SubscriberID>460031234567890</SubscriberID>", value)
        self.assertNotIn("SimIccID", value)
        self.assertIn("MDD &amp; SIM", value)
        self.assertIn("s&lt;ecret", value)

    def test_windows_profile_rejects_iccid_as_subscriber_id(self):
        with self.assertRaisesRegex(RuntimeError, "IMSI"):
            windows_mbn_profile_xml("MDD", "89852312388530152529", "ctnet")

    def test_windows_empty_profile_exit_code_is_an_empty_list(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="Cellular 2", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345")
        control = ModemControl(args, modem)
        empty = types.SimpleNamespace(returncode=1, stdout="\n", stderr="")
        with patch("agent.modem_agent.os.name", "nt"), \
                patch("agent.modem_agent.subprocess.run", return_value=empty):
            result = control._cellular_profiles()
        self.assertEqual(result["profiles"], [])
        self.assertTrue(result["supported"])
        control.stop.set()

    def test_windows_profile_list_accepts_valid_output_with_exit_one(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="Cellular 2", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345")
        control = ModemControl(args, modem)
        listed = types.SimpleNamespace(
            returncode=1, stderr="",
            stdout="Profiles on interface Cellular 2:\n-------------------------------------\n    MDD-test\n")
        with patch("agent.modem_agent.os.name", "nt"), \
                patch("agent.modem_agent.subprocess.run", return_value=listed):
            result = control._cellular_profiles()
        self.assertEqual(result["profiles"], [{"name": "MDD-test"}])
        control.stop.set()

    def test_modem_apn_discovery_excludes_service_contexts(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="wwan0", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345",
                                      _at=Mock(return_value=(
                                          b'+CGDCONT: 1,"IP","internet"\r\n'
                                          b'+CGDCONT: 2,"IPV4V6","ims"\r\n'
                                          b'+CGDCONT: 3,"IP","sos.example"\r\nOK\r\n')))
        control = ModemControl(args, modem)
        self.assertEqual(control._modem_apn_candidates(), ["internet"])
        control.stop.set()

    def test_modem_apn_falls_back_to_network_assigned_context(self):
        """3GPP TS 27.007 CGCONTRDP exposes the APN the network assigned to a live context."""
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="wwan0", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        responses = iter([
            b'+CGDCONT: 1,"IP","ims"\r\n'
            b'+CGDCONT: 2,"IPV4V6","sos"\r\nOK\r\n',
            b'+CGCONTRDP: 1,"IP","ctnet","10.0.0.1","255.255.255.0"\r\nOK\r\n',
        ])
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345",
                                      _at=Mock(side_effect=lambda _command: next(responses)))
        control = ModemControl(args, modem)
        candidates = control._modem_profile_candidates()
        self.assertEqual([item["apn"] for item in candidates], ["ctnet"])
        self.assertEqual(candidates[0]["source"], "network")
        control.stop.set()

    def test_windows_connect_auto_provisions_one_unambiguous_modem_apn(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="Cellular 2", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345",
                                      iccid="89852312388530152529")
        control = ModemControl(args, modem)
        control._modem_apn_candidates = Mock(return_value=["internet"])
        control._save_cellular_profile = Mock(return_value={"ok": True})
        results = [types.SimpleNamespace(returncode=0, stdout="", stderr=""),
                   types.SimpleNamespace(returncode=0, stdout="", stderr=""),
                   types.SimpleNamespace(returncode=1, stdout="\n", stderr=""),
                   types.SimpleNamespace(returncode=0, stdout="", stderr="")]
        with patch("agent.modem_agent.os.name", "nt"), \
                patch("agent.modem_agent.subprocess.run", side_effect=results):
            self.assertEqual(control._connect_cellular("Cellular 2"), "")
        control._save_cellular_profile.assert_called_once_with({
            "name": "MDD-Auto-2529", "apn": "internet", "auth": "NONE"})
        control.stop.set()

    def test_windows_connect_enables_data_and_all_roaming_before_connect(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="Cellular 2", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345",
                                      iccid="89852312388530152529")
        control = ModemControl(args, modem)
        control.allow_roaming = True
        control.selected_profile = "MDD-test"
        # Windows 10 applies both policy changes but returns 1 with no diagnostic.
        policy_applied = types.SimpleNamespace(returncode=1, stdout="", stderr="")
        ok = types.SimpleNamespace(returncode=0, stdout="", stderr="")
        listed = types.SimpleNamespace(
            returncode=0, stderr="", stdout="All User Profile : MDD-test\n")
        with patch("agent.modem_agent.os.name", "nt"), \
                patch("agent.modem_agent.subprocess.run",
                      side_effect=[policy_applied, policy_applied, listed, ok]) as run:
            self.assertEqual(control._connect_cellular("Cellular 2"), "")
        self.assertEqual(run.call_args_list[0].args[0][-2:],
                         ["profileset=internet", "mode=yes"])
        self.assertEqual(run.call_args_list[1].args[0][-2:],
                         ["profileset=internet", "state=all"])
        self.assertEqual(run.call_args_list[-1].args[0][-2:],
                         ["connmode=name", "name=MDD-test"])
        control.stop.set()

    def test_macos_reports_system_managed_profile_support_honestly(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="en9", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345",
                                      _at=Mock(return_value=b'+CGDCONT: 1,"IP","internet"\r\nOK\r\n'))
        control = ModemControl(args, modem)
        with patch("agent.modem_agent.sys.platform", "darwin"):
            result = control._cellular_profiles()
        self.assertFalse(result["supported"])
        self.assertTrue(result["system_managed"])
        self.assertEqual(result["suggested_apns"], ["internet"])
        control.stop.set()

    def test_windows_profile_nonzero_exit_is_success_when_postcondition_exists(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="Cellular 2", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345",
                                      imsi="460031234567890")
        control = ModemControl(args, modem)
        failed = types.SimpleNamespace(returncode=1, stdout="", stderr="")
        control._cellular_profiles = Mock(return_value={"profiles": [{"name": "MDD-test"}]})
        with patch("agent.modem_agent.os.name", "nt"), \
                patch("agent.modem_agent.subprocess.run", return_value=failed):
            result = control._save_cellular_profile({
                "name": "MDD-test", "apn": "internet", "auth": "NONE"})
        self.assertTrue(result["ok"])
        self.assertEqual(control.selected_profile, "MDD-test")
        control.stop.set()

    def test_sms_list_has_content_identity_and_real_direction(self):
        modem = ModemCard("unused", 115200)
        responses = iter([
            b"OK\r\n", b"OK\r\n",
            (b'\r\n+CMGL: 7,"REC UNREAD","+85360000000",,"26/08/17,23:10:00+32"\r\n'
             b'hello\r\n+CMGL: 8,"STO SENT","+85361111111"\r\nout\r\nOK\r\n'),
        ])
        modem._at = Mock(side_effect=lambda _command: next(responses))
        messages = modem.sms_list()
        self.assertEqual([item["direction"] for item in messages], ["in", "out"])
        self.assertEqual(messages[0]["body"], "hello")
        self.assertEqual(len(messages[0]["fingerprint"]), 64)

    def test_at_error_names_the_rejected_command(self):
        modem = ModemCard("unused", 115200, timeout=0.1)
        modem.serial = Mock()
        modem.serial.read.side_effect = [b"\r\nERROR\r\n"]
        with self.assertRaisesRegex(RuntimeError, r"AT\+CMGF=1:.*ERROR"):
            modem._at("AT+CMGF=1")

    def test_missing_isolation_guard_is_fail_closed(self):
        result = IsolationGuard("/definitely/missing/mdd-network-guard").ensure("wwan0", "")
        self.assertFalse(result["ready"])
        self.assertEqual(result["mode"], "strict")

    def test_data_connection_is_not_started_before_isolation(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="wwan0", advertise_host="",
            socks_port=11080, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345")
        control = ModemControl(args, modem)
        control.isolation = Mock()
        control.isolation.ensure.return_value = {
            "ready": False, "mode": "strict", "error": "guard unavailable"}
        control._connect_cellular = Mock(return_value="")
        result = control._cellular_ensure({})
        self.assertTrue(result["unavailable"])
        self.assertFalse(result["proxy"]["ready"])
        control._connect_cellular.assert_not_called()
        control.stop.set()

    def test_connection_failure_removes_guard_and_keeps_proxy_closed(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="wwan0", advertise_host="",
            socks_port=11080, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345")
        control = ModemControl(args, modem)
        control.isolation = Mock()
        control.isolation.active = False
        control.isolation.ensure.return_value = {
            "ready": True, "mode": "strict", "backend": "test-guard"}
        control._status = Mock(return_value={"data": "disconnected"})
        control._connect_cellular = Mock(return_value="no cellular profile")
        control._disconnect_cellular = Mock(return_value="")
        result = control._cellular_ensure({})
        self.assertFalse(result["ok"])
        self.assertFalse(result["proxy"]["ready"])
        control.isolation.close.assert_called_once()
        control._disconnect_cellular.assert_called_once_with("wwan0")
        control.stop.set()

    def test_status_reports_unconfirmed_address_without_mutating_data_plane(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="Cellular 2", advertise_host="",
            socks_port=11080, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345",
                                      _at=Mock(side_effect=RuntimeError("unsupported")))
        control = ModemControl(args, modem)
        control.socks_server = Mock()
        control.socks_server.ready = True
        control.socks_server.source_ip = ""
        control.isolation = Mock()
        control._cellular_ip = Mock(return_value="")
        with patch("agent.modem_agent.os.name", "posix"):
            result = control._status()
        self.assertFalse(result["proxy"]["ready"])
        self.assertEqual(result["cellular"]["status"], "unavailable")
        control.socks_server.close.assert_not_called()
        control.isolation.close.assert_not_called()
        control.stop.set()

    def test_status_reuses_source_address_of_established_guarded_proxy(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="Cellular 2", advertise_host="",
            socks_port=11080, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345",
                                      _at=Mock(side_effect=RuntimeError("unsupported")))
        control = ModemControl(args, modem)
        control.socks_server = Mock(ready=True, source_ip="10.191.87.210")
        control._cellular_ip = Mock(return_value="10.191.87.210")

        result = control._status()

        self.assertTrue(result["proxy"]["ready"])
        self.assertEqual(result["ip"], "10.191.87.210")
        control._cellular_ip.assert_called_once_with("Cellular 2")
        control.stop.set()

    def test_status_revokes_proxy_only_after_three_missing_source_samples(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="Cellular 2", advertise_host="",
            socks_port=11080, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345",
                                      port_name="COM14",
                                      _at=Mock(side_effect=RuntimeError("unsupported")),
                                      close=Mock())
        control = ModemControl(args, modem)
        server = Mock(ready=True, source_ip="10.191.87.210")
        control.socks_server = server
        control.isolation = Mock(active=True, interface="Cellular 2")
        control._cellular_ip = Mock(return_value="")
        control._modem_transport_present = Mock(return_value=False)

        self.assertTrue(control._status()["proxy"]["ready"])
        self.assertTrue(control._status()["proxy"]["ready"])
        result = control._status()

        self.assertFalse(result["proxy"]["ready"])
        self.assertFalse(result["data_active"])
        self.assertEqual(result["cellular"]["status"], "unavailable")
        server.close.assert_called_once()
        control.isolation.close.assert_called_once()
        modem.close.assert_called_once()
        control.stop.set()

    def test_disabling_radio_closes_data_before_cfun(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="wwan0", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345", _at=Mock())
        control = ModemControl(args, modem)
        control._cellular_disable = Mock(return_value={"ok": True})
        result = control.execute("radio.set", {"enabled": False})
        self.assertEqual(result["radio_enabled"], False)
        control._cellular_disable.assert_called_once()
        modem._at.assert_called_once_with("AT+CFUN=4")
        control.stop.set()

    def test_windows_mbn_radio_uses_wwan_powerstate_not_cfun(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="Cellular 2", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345",
                                      sim_via_mbn=True, _at=Mock())
        control = ModemControl(args, modem)
        control._cellular_disable = Mock(return_value={"ok": True})
        applied = types.SimpleNamespace(returncode=1, stdout="", stderr="")
        with patch("agent.modem_agent.os.name", "nt"), \
                patch("agent.modem_agent.subprocess.run", return_value=applied) as run:
            result = control.execute("radio.set", {"enabled": False})
        self.assertTrue(result["ok"])
        self.assertEqual(run.call_args.args[0][-1], "state=off")
        modem._at.assert_not_called()
        control.stop.set()

    def test_disabling_roaming_tears_down_data_while_registered_roaming(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="wwan0", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345")
        control = ModemControl(args, modem)
        control._status = Mock(return_value={"registration": "roaming"})
        control._cellular_disable = Mock(return_value={"ok": True, "status": "off"})
        result = control.execute("cellular.roaming.set", {"enabled": False})
        self.assertFalse(result["roaming_allowed"])
        control._cellular_disable.assert_called_once()
        control.stop.set()

    def test_windows_roaming_switch_updates_system_policy_immediately(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="Cellular 2", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345")
        control = ModemControl(args, modem)
        control._status = Mock(return_value={"registration": "home"})
        applied = types.SimpleNamespace(returncode=1, stdout="", stderr="")
        with patch("agent.modem_agent.os.name", "nt"), \
                patch("agent.modem_agent.subprocess.run", return_value=applied) as run:
            result = control.execute("cellular.roaming.set", {"enabled": True})
        self.assertTrue(result["ok"])
        self.assertEqual(run.call_args.args[0][-1], "state=all")
        control.stop.set()

    def test_windows_disconnect_already_off_is_successful_postcondition(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="Cellular 2", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345")
        control = ModemControl(args, modem)
        inactive = types.SimpleNamespace(
            returncode=1, stdout="Disconnect Failure: Context Not Activated.", stderr="")
        control._cellular_ip = Mock(return_value="")
        with patch("agent.modem_agent.os.name", "nt"), \
                patch("agent.modem_agent.subprocess.run", return_value=inactive):
            self.assertEqual(control._disconnect_cellular("Cellular 2"), "")
        control.stop.set()


if __name__ == "__main__":
    unittest.main()
