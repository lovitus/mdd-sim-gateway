import json
import io
import subprocess
from unittest.mock import Mock, patch

from agent.modem_providers import (
    AuxiliaryAtProvider, CompositeModemProvider, GammuCliProvider, ProviderError,
    WindowsMbnProvider, WindowsPnpLease,
)


def test_auxiliary_at_exposes_cuad_directory_and_opens_logical_usim_channel():
    provider = AuxiliaryAtProvider("COM16", Mock())
    provider.directory_records = [bytes.fromhex(
        "61184F10A0000000871002FF86FF0389FFFFFFFF50045553494D")]
    provider._open_logical_channel = Mock()

    assert provider.transmit(bytes.fromhex("00A40004022F0000")) == bytes.fromhex("6108")
    fcp = provider.transmit(bytes.fromhex("00C0000008"))
    assert fcp[-2:] == bytes.fromhex("9000")
    assert fcp[7] == len(provider.directory_records[0])
    record = provider.transmit(bytes.fromhex("00B201041A"))
    assert record.startswith(bytes.fromhex("61184F10A0000000871002"))
    assert record[-2:] == bytes.fromhex("9000")

    selected = provider.transmit(bytes.fromhex(
        "00A4040410A0000000871002FF86FF0389FFFFFFFF"))
    assert selected == bytes.fromhex("6100")
    provider._open_logical_channel.assert_called_once_with(
        "A0000000871002FF86FF0389FFFFFFFF")


def test_auxiliary_at_call_status_ignores_data_contexts_and_tracks_voice_only():
    provider = AuxiliaryAtProvider("COM16", Mock())
    provider._at = Mock(return_value=(
        b'+CLCC: 1,1,0,1,0,"",128\r\n'
        b'+CLCC: 3,0,3,0,0,"22333322",129\r\n'
        b'+CLCC: 4,1,0,1,0,"",128\r\nOK\r\n'))

    status = provider.call_status()
    assert status["status"] == "ringing-out"
    assert status["direction"] == "out"
    assert status["number"] == "22333322"

    provider._at.return_value = (
        b'+CLCC: 1,1,0,1,0,"",128\r\n'
        b'+CLCC: 4,1,0,1,0,"",128\r\nOK\r\n')
    assert provider.call_status()["status"] == "ended"


def test_auxiliary_at_call_status_preserves_incoming_direction_and_number():
    provider = AuxiliaryAtProvider("COM16", Mock())
    provider._at = Mock(return_value=(
        b'RING\r\n+CLIP: "+85246094148",145\r\n'
        b'+CLCC: 2,1,4,0,0,"+85246094148",145\r\nOK\r\n'))

    status = provider.call_status()

    assert status == {
        "ok": True,
        "status": "ringing-in",
        "direction": "in",
        "number": "+85246094148",
        "audio": False,
        "state_source": "auxiliary_at",
    }


def completed(payload, returncode=0):
    return Mock(stdout=json.dumps(payload), stderr="", returncode=returncode)


class FakeLeaseProcess:
    def __init__(self, payload=None, code=0):
        self.stdout = io.StringIO(json.dumps(payload or {
            "ready": True, "backend": "windows-pnp-lease", "problem_code": 22}) + "\n")
        self.stderr = io.StringIO("")
        self.stdin = io.StringIO()
        self.code = code

    def poll(self):
        return None

    def wait(self, timeout=None):
        return self.code


def snapshot(**changes):
    value = {
        "interface_id": "{mbn-interface}",
        "device_id": "862547055201716",
        "sim_iccid": "89852312388530152529",
        "subscriber_id": "455070885002522",
        "ready_state": "MBN_READY_STATE_INITIALIZED",
        "sms_caps": 3,
        "sms_ready": True,
        "sms_configured": True,
        "sms_error": "",
        "sms_service_center": "+85362101201",
        "data_class": 63,
        "registration": "MBN_REGISTER_STATE_ROAMING",
        "provider_name": "China Telecom",
        "provider_id": "46011",
        "signal": 68,
        "software_radio": "MBN_RADIO_ON",
        "activation_state": "MBN_ACTIVATION_STATE_DEACTIVATED",
        "profile_name": "",
    }
    value.update(changes)
    return value


def test_windows_mbn_provider_maps_native_capabilities_and_state():
    runner = Mock(return_value=completed({"ok": True, "interfaces": [snapshot()]}))
    provider = WindowsMbnProvider(["mbn-helper"], "862547055201716", runner)
    provider.refresh()

    assert provider.identity() == ("89852312388530152529", "455070885002522")
    assert provider.capabilities.cellular_data is True
    assert provider.capabilities.sms_list is True
    assert provider.capabilities.sms_send is True
    assert provider.capabilities.call_signalling is False
    assert provider.status(refresh=False) == {
        "sim": "ready",
        "registration": "roaming", "operator": "China Telecom", "operator_id": "46011",
        "signal": 68, "radio_enabled": True, "data": "disconnected",
        "data_active": False, "profile": "", "provider": "windows_mbn",
        "owner": "system_managed",
        "sms_ready": True, "sms_configured": True,
        "sms_error": "", "sms_service_center": "+85362101201",
        "sms_provider": "windows_mbn", "sms_readiness_authoritative": True,
    }


def test_windows_mbn_provider_blocks_send_before_submission_when_sms_is_not_ready():
    runner = Mock(return_value=completed({"ok": True, "interfaces": [snapshot(
        sms_ready=False, sms_error="0x8000000A")]}))
    provider = WindowsMbnProvider(["mbn-helper"], "862547055201716", runner)
    provider.refresh()

    assert provider.capabilities.sms_list is True
    assert provider.capabilities.sms_send is True
    result = provider.sms_send("+85346094148", "test")
    assert result["ok"] is False
    assert result["unavailable"] is True
    assert "0x8000000A" in result["error"]
    assert all("sms-send" not in call.args[0] for call in runner.call_args_list)


def test_windows_mbn_provider_does_not_treat_readable_configuration_as_send_readiness():
    responses = [
        completed({"ok": True, "interfaces": [snapshot(sms_ready=None)]}),
        completed({"ok": True, "interfaces": [snapshot(sms_ready=None)]}),
        completed({"ok": True, "status": "sent", "message_reference": 7}),
    ]
    runner = Mock(side_effect=responses)
    provider = WindowsMbnProvider(["mbn-helper"], "862547055201716", runner)
    provider.refresh()

    status = provider.status(refresh=False)
    assert status["sms_configured"] is True
    assert status["sms_ready"] is None
    assert status["sms_readiness_authoritative"] is False
    assert provider.sms_send("+85246094148", "test")["ok"] is True


def test_windows_mbn_provider_selects_interface_by_device_id():
    runner = Mock(return_value=completed({"ok": True, "interfaces": [
        snapshot(device_id="another"), snapshot(interface_id="{correct}"),
    ]}))
    provider = WindowsMbnProvider(["mbn-helper"], "862547055201716", runner)
    provider.refresh()
    assert provider.interface_id == "{correct}"


def test_windows_mbn_provider_never_uses_modem_model_for_selection():
    runner = Mock(return_value=completed({"ok": True, "interfaces": [
        snapshot(device_id="another", model="Quectel EC20F"),
    ]}))
    provider = WindowsMbnProvider(["mbn-helper"], "862547055201716", runner)
    try:
        provider.refresh()
    except ProviderError as exc:
        assert "did not enumerate" in str(exc)
    else:
        raise AssertionError("model name must not select a platform attachment")


def test_windows_mbn_provider_preserves_structured_connect_failure():
    runner = Mock(side_effect=[
        completed({"ok": True, "interfaces": [snapshot()]}),
        completed({"ok": False, "status": "failed", "hresult": "0xC0040004",
                   "network_error": 0}, returncode=1),
    ])
    provider = WindowsMbnProvider(["mbn-helper"], "862547055201716", runner)
    provider.refresh()
    result = provider.connect("MDD-2529-ctnet")
    assert result["hresult"] == "0xC0040004"
    assert runner.call_args_list[-1].args[0] == [
        "mbn-helper", "connect", "{mbn-interface}", "MDD-2529-ctnet"]


def test_windows_mbn_provider_uses_system_trigger_but_native_state_as_postcondition():
    runner = Mock(side_effect=[
        completed({"ok": False, "status": "completed", "hresult": "0x00000000",
                   "activation_state": "MBN_ACTIVATION_STATE_DEACTIVATED"}),
        Mock(stdout="", stderr="", returncode=1),
        completed({"ok": True, "interfaces": [snapshot(
            activation_state="MBN_ACTIVATION_STATE_ACTIVATED")]}),
    ])
    provider = WindowsMbnProvider(["mbn-helper"], "862547055201716", runner)
    provider.interface_id = "{mbn-interface}"

    result = provider.connect("MDD-2529-ctnet", "Cellular 2")

    assert result["ok"] is True
    assert result["compatibility_trigger"] == "windows_mbn_system_command"
    assert runner.call_args_list[1].args[0] == [
        "netsh", "mbn", "connect", "interface=Cellular 2", "connmode=name",
        "name=MDD-2529-ctnet"]


def test_windows_mbn_provider_does_not_publish_cached_identity_when_sim_is_not_ready():
    runner = Mock(return_value=completed({"ok": True, "interfaces": [snapshot(
        ready_state="MBN_READY_STATE_SIM_NOT_INSERTED")]}))
    provider = WindowsMbnProvider(["mbn-helper"], "862547055201716", runner)
    provider.refresh()

    assert provider.identity() == ("", "")
    assert provider.status(refresh=False)["sim"] == "unavailable"


def test_windows_mbn_provider_waits_for_system_enumeration_before_at_fallback():
    runner = Mock(side_effect=[
        completed({"ok": True, "interfaces": []}),
        completed({"ok": True, "interfaces": [snapshot()]}),
    ])
    with patch("agent.modem_providers._windows_mbn_helper_command",
               return_value=["mbn-helper"]), patch("agent.modem_providers.time.sleep"):
        provider = WindowsMbnProvider.discover("862547055201716", runner, wait_seconds=1)

    assert provider is not None
    assert runner.call_count == 2


def test_windows_pnp_lease_discovers_interface_without_model_matching():
    runner = Mock(return_value=completed({
        "Name": "Cellular 2", "PnPDeviceID": "USB\\VID_1234&MI_04\\1"}))
    lease = WindowsPnpLease.discover("Cellular 2", runner)
    assert lease.pnp_device_id == "USB\\VID_1234&MI_04\\1"
    assert "Get-NetAdapter -Name 'Cellular 2'" in runner.call_args.args[0][-1]


def test_windows_pnp_lease_validates_disable_and_enable_postconditions():
    process = FakeLeaseProcess()
    runner = Mock(return_value=Mock(stdout='{"ok":true}', stderr="", returncode=0))
    lease = WindowsPnpLease("Cellular 2", "USB\\DEVICE", runner=runner,
                            process_factory=Mock(return_value=process), helper="guard.exe")
    assert lease.acquire()["problem_code"] == 22
    assert lease.acquired is True
    assert lease.release()["problem_code"] == 0
    assert lease.acquired is False
    assert "--signal-event" in runner.call_args.args[0]


def test_windows_pnp_lease_rolls_back_uncertain_disable():
    process = FakeLeaseProcess({"ready": False, "error": "postcondition failed"})
    lease = WindowsPnpLease("Cellular 2", "USB\\DEVICE",
                            process_factory=Mock(return_value=process), helper="guard.exe")
    try:
        lease.acquire()
    except ProviderError:
        pass
    else:
        raise AssertionError("disable without ProblemCode 22 must fail")
    assert process.stdin.closed


def test_gammu_discovery_requires_matching_imei_and_probes_capabilities():
    outputs = [
        Mock(returncode=0, stdout=("Device : COM9:\nManufacturer : Other\n"
                                  "IMEI : 111111111111111\n"), stderr=""),
        Mock(returncode=0, stdout=("Device : COM16:\nManufacturer : Quectel\n"
                                  "Model : unknown (EC20F)\nFirmware : test\n"
                                  "IMEI : 862547055201716\nSIM IMSI : 455070885002522\n"),
             stderr=""),
        Mock(returncode=0, stdout=(
            "Enabling info about incoming SMS : No error.\n"
            "Enabling info about calls : No error.\n"), stderr=""),
    ]
    runner = Mock(side_effect=outputs)
    with patch("agent.modem_providers.GammuCliProvider.command_path",
               return_value=["gammu"]):
        provider = GammuCliProvider.discover(
            "862547055201716", ["COM9", "COM16"], runner=runner)
    assert provider.port == "COM16"
    assert provider.capabilities.sms_send is True
    assert provider.capabilities.call_signalling is True
    assert all("-c" in call.args[0] for call in runner.call_args_list)
    assert all(call.kwargs["encoding"] == "utf-8" for call in runner.call_args_list)
    assert all(call.kwargs["errors"] == "replace" for call in runner.call_args_list)


def test_gammu_sms_parser_preserves_opaque_location_and_content_identity():
    output = '''Location 7, folder "Inbox", SIM memory, Inbox folder
SMS message
SMSC number        : "+85362101201"
Sent               : Mon 18 Aug 2026 15:00:00
Remote number      : "+85346094148"
Status             : UnRead

hello from gammu

Location 8, folder "Outbox", phone memory
SMS message
Remote number      : "22333322"
Status             : Sent

outbound
'''
    messages = GammuCliProvider._parse_messages(output)
    assert [item["id"] for item in messages] == ["gammu:1:7", "gammu:2:8"]
    assert [item["direction"] for item in messages] == ["in", "out"]
    assert messages[0]["peer"] == "+85346094148"
    assert messages[0]["body"] == "hello from gammu"
    assert len(messages[0]["fingerprint"]) == 64


def test_gammu_sms_list_uses_short_cache_to_avoid_starving_status_heartbeats():
    output = 'Location 7, folder "Inbox"\nSMS message\nRemote number : "+1"\n\nhello\n'
    runner = Mock(return_value=Mock(returncode=0, stdout=output, stderr=""))
    provider = GammuCliProvider(["gammu"], "COM16", "862547055201716", runner)
    first = provider.sms_list()
    second = provider.sms_list()
    assert first == second
    assert runner.call_count == 1


def test_gammu_uses_argv_for_billable_operations_and_opaque_delete_id():
    runner = Mock(return_value=Mock(returncode=0, stdout="reference 42", stderr=""))
    provider = GammuCliProvider(["gammu"], "COM16", "862547055201716", runner)
    sent = provider.sms_send("+85346094148", "hello; no shell")
    provider.sms_delete("gammu:1:7")
    provider.call_dial("22333322")
    assert sent["reference"] == 42
    assert runner.call_args_list[0].args[0][-5:] == [
        "sendsms", "TEXT", "+85346094148", "-textutf8", "hello; no shell"]
    assert runner.call_args_list[1].args[0][-3:] == ["deletesms", "1", "7"]
    assert runner.call_args_list[2].args[0][-2:] == ["dialvoice", "22333322"]


def test_gammu_never_marks_timed_out_billable_operations_retryable():
    runner = Mock(side_effect=subprocess.TimeoutExpired(["gammu"], 1))
    provider = GammuCliProvider(["gammu"], "COM16", "862547055201716", runner)
    sms = provider.sms_send("+85346094148", "one attempt")
    call = provider.call_dial("22333322")
    assert sms == {
        "ok": False, "status": "unknown", "retryable": False,
        "error": "Gammu operation timed out: sendsms", "provider": "gammu",
    }
    assert call == {
        "ok": False, "status": "unknown", "retryable": False,
        "error": "Gammu operation timed out: dialvoice", "audio": False,
    }


def test_gammu_failure_preserves_stdout_error_not_only_progress_stderr():
    runner = Mock(return_value=Mock(
        returncode=1,
        stdout="Sending SMS 1/1... error 42",
        stderr="If you want break, press Ctrl+C",
    ))
    provider = GammuCliProvider(["gammu"], "COM16", "862547055201716", runner)
    try:
        provider.sms_send("+85346094148", "one attempt")
    except ProviderError as exc:
        assert "error 42" in str(exc)
        assert "press Ctrl+C" in str(exc)
    else:
        raise AssertionError("a nonzero Gammu exit must fail")


def test_gammu_call_status_uses_owned_operation_state_when_display_is_unsupported():
    runner = Mock(side_effect=[
        Mock(returncode=0, stdout="", stderr=""),
        Mock(returncode=1, stdout="", stderr="Functionality not implemented"),
        Mock(returncode=0, stdout="", stderr=""),
        Mock(returncode=1, stdout="", stderr="Functionality not implemented"),
    ])
    provider = GammuCliProvider(["gammu"], "COM16", "862547055201716", runner)
    assert provider.call_dial("22333322")["status"] == "dialing"
    assert provider.call_status() == {
        "ok": True, "status": "dialing", "audio": False,
        "state_source": "gammu_operation",
    }
    assert provider.call_hangup()["status"] == "ended"
    assert provider.call_status()["status"] == "ended"
    assert provider.call_status()["status"] == "ended"
    assert runner.call_count == 3


def test_composite_provider_keeps_mbn_data_and_replaces_only_signalling():
    data = Mock()
    data.snapshot = {"model": "QUECTEL Mobile Broadband Module"}
    data.name = "windows_mbn"
    data.capabilities = type("Caps", (), {
        "cellular_data": True, "sim_apdu": False,
        "sms_list": False, "sms_send": False})()
    data.status.return_value = {"data": "connected", "sms_ready": False,
                                "provider": "windows_mbn"}
    signalling = Mock()
    signalling.sms_supported = True
    signalling.capabilities = type("Caps", (), {
        "sms_list": True, "sms_send": True, "call_signalling": True,
        "call_audio": False})()
    provider = CompositeModemProvider(data, signalling)
    assert provider.capabilities.cellular_data is True
    assert provider.capabilities.sms_send is True
    assert provider.capabilities.call_signalling is True
    assert provider.status()["data"] == "connected"
    assert provider.status()["sms_ready"] is True


def test_composite_provider_prefers_authoritative_native_sms_over_auxiliary():
    data = Mock()
    data.name = "windows_mbn"
    data.snapshot = {"sms_ready": True}
    data.capabilities = type("Caps", (), {
        "cellular_data": True, "sim_apdu": False,
        "sms_list": True, "sms_send": True})()
    data.status.return_value = {"data": "connected", "sms_ready": True,
                                "sms_readiness_authoritative": True,
                                "sms_service_center": "+85362101201"}
    data.sms_send.return_value = {"ok": True, "status": "sent"}
    signalling = Mock()
    signalling.name = "gammu"
    signalling.sms_supported = True
    signalling.capabilities = type("Caps", (), {
        "sms_list": True, "sms_send": True, "call_signalling": True,
        "call_audio": False})()
    provider = CompositeModemProvider(data, signalling)

    status = provider.status()
    result = provider.sms_send("+85246094148", "test")

    assert status["sms_provider"] == "windows_mbn"
    assert status["sms_readiness_authoritative"] is True
    assert result["ok"] is True
    data.sms_send.assert_called_once_with("+85246094148", "test")
    signalling.sms_send.assert_not_called()


def test_composite_provider_falls_back_when_native_sms_is_authoritatively_unavailable():
    data = Mock()
    data.name = "windows_mbn"
    data.snapshot = {"sms_ready": False}
    data.capabilities = type("Caps", (), {
        "cellular_data": True, "sim_apdu": False,
        "sms_list": True, "sms_send": True})()
    data.status.return_value = {
        "data": "connected", "sms_ready": False,
        "sms_readiness_authoritative": True, "sms_error": "0x8000000A",
    }
    signalling = Mock()
    signalling.name = "auxiliary_at"
    signalling.sms_supported = True
    signalling.capabilities = type("Caps", (), {
        "sms_list": True, "sms_send": True, "call_signalling": True,
        "call_audio": False})()
    signalling.sms_send.return_value = {
        "ok": True, "status": "sent", "provider": "auxiliary_at"}
    provider = CompositeModemProvider(data, signalling)

    status = provider.status()
    result = provider.sms_send("+85246094148", "test")

    assert status["sms_ready"] is True
    assert status["sms_provider"] == "auxiliary_at"
    assert status["sms_readiness_authoritative"] is False
    assert status["native_sms_error"] == "0x8000000A"
    assert result["ok"] is True
    signalling.sms_send.assert_called_once_with("+85246094148", "test")
    data.sms_send.assert_not_called()
