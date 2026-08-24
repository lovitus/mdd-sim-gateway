import json
import io
import plistlib
import threading
from types import SimpleNamespace
from unittest.mock import Mock, patch

from agent.call_audio import (
    CallAudioController, CallAudioProbe, _decode_miniaudio_id,
    _mac_microphone_permission, _windows_audio_inventory, prepare_call_audio_usb,
    probe_call_audio,
)


def _endpoint_id(value: str) -> str:
    return value.encode("utf-16-le").hex()


def test_decode_miniaudio_windows_endpoint_id():
    value = "{0.0.0.00000000}.{12345678-1234-1234-1234-123456789abc}"
    assert _decode_miniaudio_id(_endpoint_id(value)) == value
    assert _decode_miniaudio_id(_endpoint_id(value)[:-2]) == value


def test_windows_inventory_rejects_unresolved_or_injected_port():
    for value in ("auto", "COM14'; Remove-Item C:\\x; '", "ttyUSB0"):
        try:
            _windows_audio_inventory(value, runner=Mock())
        except RuntimeError as exc:
            assert "concrete COM port" in str(exc)
        else:
            raise AssertionError("unsafe port was accepted")


def test_probe_matches_only_same_container_endpoints_and_opens_explicit_ids():
    playback = "{0.0.0.00000000}.{aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa}"
    capture = "{0.0.1.00000000}.{bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb}"
    inventory = {
        "container_id": "{modem-container}",
        "endpoints": [
            {"kind": "playback", "instance_id": "SWD\\MMDEVAPI\\" + playback,
             "status": "OK"},
            {"kind": "capture", "instance_id": "SWD\\MMDEVAPI\\" + capture,
             "status": "OK"},
        ],
    }
    calls = []

    def runner(command, **kwargs):
        calls.append(command)
        if command[0] == "powershell.exe":
            return SimpleNamespace(returncode=0, stdout=json.dumps(inventory), stderr="")
        if "list" in command:
            return SimpleNamespace(returncode=0, stderr="", stdout=json.dumps({
                "ok": True, "version": 2,
                "devices": [
                    {"kind": "playback", "id": _endpoint_id(playback)},
                    {"kind": "capture", "id": _endpoint_id(capture)},
                    {"kind": "playback", "id": _endpoint_id("{default-host-speaker}")},
                ],
            }))
        return SimpleNamespace(returncode=0, stderr="", stdout=json.dumps({
            "ok": True, "version": 2, "sample_rate": 8000,
            "capture_channels": 1, "playback_channels": 1,
            "capture_frames": 4000,
        }))

    with patch("agent.call_audio.os.name", "nt"), \
            patch("agent.call_audio._helper_command", return_value=["audio-helper"]):
        result = probe_call_audio(
            "COM14", Mock(return_value=b"+QPCMV: (0,1),(0-2)\r\nOK\r\n"),
            runner=runner,
        )

    assert result.ready is True
    assert result.backend == "uac"
    probe_command = next(value for value in calls if "probe" in value)
    assert probe_command[probe_command.index("-playback-id") + 1] == _endpoint_id(playback)
    assert probe_command[probe_command.index("-capture-id") + 1] == _endpoint_id(capture)


def test_macos_probe_matches_coreaudio_ids_by_exact_usb_identity():
    capture = "AppleUSBAudioEngine:Quectel:EC20:SERIAL-ONE:6"
    playback = "AppleUSBAudioEngine:Quectel:EC20:SERIAL-ONE:7"
    inventory = [
        {"idVendor": 0x2C7C, "idProduct": 0x0125,
         "IOAudioEngineGlobalUniqueID": capture,
         "IORegistryEntryChildren": [
             {"IOObjectClass": "AppleUSBAudioStream", "IOAudioStreamDirection": 1}]},
        {"idVendor": 0x2C7C, "idProduct": 0x0125,
         "IOAudioEngineGlobalUniqueID": playback,
         "IORegistryEntryChildren": [
             {"IOObjectClass": "AppleUSBAudioStream", "IOAudioStreamDirection": 0}]},
        # Same model, different physical module: it must not be selected by name.
        {"idVendor": 0x2C7C, "idProduct": 0x0125,
         "IOAudioEngineGlobalUniqueID": "AppleUSBAudioEngine:Quectel:EC20:OTHER:7",
         "IORegistryEntryChildren": [
             {"IOObjectClass": "AppleUSBAudioStream", "IOAudioStreamDirection": 0}]},
    ]
    calls = []

    def runner(command, **kwargs):
        calls.append(command)
        if command[0] == "ioreg":
            return SimpleNamespace(returncode=0, stdout=plistlib.dumps(inventory), stderr=b"")
        if "list" in command:
            return SimpleNamespace(returncode=0, stderr="", stdout=json.dumps({
                "ok": True, "version": 2,
                "devices": [
                    {"kind": "playback", "id": playback.encode().hex()},
                    {"kind": "capture", "id": capture.encode().hex()},
                    {"kind": "playback", "id": b"host-speaker".hex()},
                ],
            }))
        return SimpleNamespace(returncode=0, stderr="", stdout=json.dumps({
            "ok": True, "version": 2, "sample_rate": 8000,
            "capture_channels": 1, "playback_channels": 1,
        }))

    with patch("agent.call_audio.os.name", "posix"), \
            patch("agent.call_audio.sys.platform", "darwin"), \
            patch("agent.call_audio._mac_microphone_permission",
                  return_value="authorized"), \
            patch("agent.call_audio._helper_command", return_value=["audio-helper"]):
        result = probe_call_audio(
            "usb:2c7c:0125:1:2:SERIAL-ONE",
            Mock(return_value=b"+QPCMV: (0,1),(0-2)\r\nOK\r\n"), runner=runner)

    assert result.ready is True
    assert result.backend == "uac"
    probe_command = next(value for value in calls if "probe" in value)
    assert probe_command[probe_command.index("-playback-id") + 1] == playback.encode().hex()
    assert probe_command[probe_command.index("-capture-id") + 1] == capture.encode().hex()


def test_probe_fails_closed_when_modem_has_no_activation_capability():
    with patch("agent.call_audio.os.name", "nt"), \
            patch("agent.call_audio._helper_command", return_value=["audio-helper"]):
        result = probe_call_audio("COM14", Mock(return_value=b"ERROR\r\n"), runner=Mock())
    assert result.ready is False
    assert "does not advertise UAC" in result.reason


def test_probe_rejects_helpers_without_strict_telemetry_version():
    playback = "{0.0.0.00000000}.{aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa}"
    capture = "{0.0.1.00000000}.{bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb}"
    inventory = {
        "container_id": "{modem-container}",
        "endpoints": [
            {"kind": "playback", "instance_id": "SWD\\MMDEVAPI\\" + playback,
             "status": "OK"},
            {"kind": "capture", "instance_id": "SWD\\MMDEVAPI\\" + capture,
             "status": "OK"},
        ],
    }
    listed = {"ok": True, "version": 2, "devices": [
        {"kind": "playback", "id": _endpoint_id(playback)},
        {"kind": "capture", "id": _endpoint_id(capture)},
    ]}
    for version in (None, 0, 1, "2", True):
        checked = {"ok": True, "version": version, "sample_rate": 8000,
                   "capture_channels": 1, "playback_channels": 1}
        with patch("agent.call_audio.os.name", "nt"), \
                patch("agent.call_audio._helper_command", return_value=["audio-helper"]), \
                patch("agent.call_audio._windows_audio_inventory", return_value=inventory), \
                patch("agent.call_audio._invoke_helper", side_effect=[listed, checked]):
            result = probe_call_audio(
                "COM14", Mock(return_value=b"+QPCMV: (0,1),(0-2)\r\nOK\r\n"))
        assert result.ready is False
        assert "telemetry version 2" in result.reason


def test_macos_probe_distinguishes_permission_denied_before_opening_audio():
    runner = Mock()
    with patch("agent.call_audio.os.name", "posix"), \
            patch("agent.call_audio.sys.platform", "darwin"), \
            patch("agent.call_audio._helper_command", return_value=["audio-helper"]), \
            patch("agent.call_audio._mac_microphone_permission",
                  return_value="permission_denied"):
        result = probe_call_audio(
            "usb:2c7c:0125:1:2:SERIAL-ONE",
            Mock(return_value=b"+QPCMV: (0,1),(0-2)\r\nOK\r\n"), runner=runner)
    assert result.ready is False
    assert "permission_denied" in result.reason
    runner.assert_not_called()


def test_macos_permission_prompt_is_explicit_and_bounded():
    class Device:
        status = 0
        requested = 0

        @classmethod
        def authorizationStatusForMediaType_(cls, _media):
            return cls.status

        @classmethod
        def requestAccessForMediaType_completionHandler_(cls, _media, completion):
            cls.requested += 1
            cls.status = 3
            completion(True)

    fake = type("AVFoundation", (), {
        "AVCaptureDevice": Device,
        "AVMediaTypeAudio": "audio",
        "AVAuthorizationStatusNotDetermined": 0,
        "AVAuthorizationStatusRestricted": 1,
        "AVAuthorizationStatusDenied": 2,
        "AVAuthorizationStatusAuthorized": 3,
    })
    with patch.dict("sys.modules", {"AVFoundation": fake}):
        assert _mac_microphone_permission(request=True) == "authorized"
    assert Device.requested == 1


def test_usb_preparation_is_read_only_when_uac_is_already_enabled():
    at = Mock(side_effect=[
        b"+QPCMV: (0,1),(0-2)\r\nOK\r\n",
        b'+QCFG: "usbcfg",0x2C7C,0x0125,1,1,1,1,1,0,1\r\nOK\r\n',
    ])

    result = prepare_call_audio_usb(at)

    assert result.supported is True
    assert result.enabled is True
    assert result.changed is False
    assert [call.args[0] for call in at.call_args_list] == [
        "AT+QPCMV=?", 'AT+QCFG="USBCFG"']


def test_usb_preparation_changes_only_uac_bit_and_requests_one_restart():
    at = Mock(side_effect=[
        b"+QPCMV: (0,1),(0-2)\r\nOK\r\n",
        b'+QCFG: "usbcfg",0x2C7C,0x0125,1,1,1,1,1,0,0\r\nOK\r\n',
        b"OK\r\n",
        b'+QCFG: "usbcfg",0x2C7C,0x0125,1,1,1,1,1,0,1\r\nOK\r\n',
        OSError("serial function disappeared during reset"),
    ])

    result = prepare_call_audio_usb(at)

    assert result.enabled is True
    assert result.changed is True
    assert result.restart_required is True
    assert [call.args[0] for call in at.call_args_list] == [
        "AT+QPCMV=?", 'AT+QCFG="USBCFG"',
        'AT+QCFG="USBCFG",0x2C7C,0x0125,1,1,1,1,1,0,1',
        'AT+QCFG="USBCFG"', "AT+CFUN=1,1"]


def test_usb_preparation_rejects_unknown_layout_without_writing():
    at = Mock(side_effect=[
        b"+QPCMV: (0,1),(0-2)\r\nOK\r\n",
        b'+QCFG: "usbcfg",0x2C7C,0x0125,1,1,1\r\nOK\r\n',
    ])

    result = prepare_call_audio_usb(at)

    assert result.supported is False
    assert result.changed is False
    assert "seven-flag" in result.reason
    assert at.call_count == 2


def test_controller_keeps_secret_out_of_argv_and_restores_qpcmv():
    class Process:
        def __init__(self):
            self.stdout = io.StringIO(json.dumps({"ok": True, "version": 2}) + "\n")
            self.stderr = io.StringIO("")
            self.done = threading.Event()
            self.code = None

        def poll(self):
            return self.code

        def terminate(self):
            self.code = 0
            self.done.set()

        def kill(self):
            self.terminate()

        def wait(self, timeout=None):
            if not self.done.wait(timeout):
                raise TimeoutError()
            return self.code

    process = Process()
    factory = Mock(return_value=process)
    at = Mock(return_value=b"OK")
    probe = CallAudioProbe(
        ready=True, backend="uac", activation="qpcmv-uac",
        details={"playback_id": "render-id", "capture_id": "capture-id",
                 "helper_version": 2},
    )
    with patch("agent.call_audio._helper_command", return_value=["audio-helper"]):
        controller = CallAudioController(probe, at, process_factory=factory)
    opened = controller.open(
        "a" * 32, "wss://gateway.example/media", "single-use-secret", "ab" * 32)
    assert opened["ready"] is True
    argv = factory.call_args.args[0]
    environment = factory.call_args.kwargs["env"]
    assert "single-use-secret" not in argv
    assert environment["MDD_MEDIA_TOKEN"] == "single-use-secret"
    assert controller.close("a" * 32)["closed"] is True
    assert [call.args[0] for call in at.call_args_list] == ["AT+QPCMV=1,2", "AT+QPCMV=0"]


def test_controller_rejects_downgraded_bridge_and_restores_qpcmv():
    class Process:
        def __init__(self):
            self.stdout = io.StringIO(json.dumps({"ok": True, "version": 1}) + "\n")
            self.stderr = io.StringIO("")
            self.done = threading.Event()
            self.code = None

        def poll(self):
            return self.code

        def terminate(self):
            self.code = 0
            self.done.set()

        def kill(self):
            self.terminate()

        def wait(self, timeout=None):
            if not self.done.wait(timeout):
                raise TimeoutError()
            return self.code

    at = Mock(return_value=b"OK")
    probe = CallAudioProbe(
        ready=True, backend="uac", activation="qpcmv-uac",
        details={"playback_id": "render-id", "capture_id": "capture-id",
                 "helper_version": 2})
    with patch("agent.call_audio._helper_command", return_value=["audio-helper"]):
        controller = CallAudioController(probe, at, process_factory=Mock(return_value=Process()))
    try:
        controller.open("b" * 32, "wss://gateway.example/media", "secret", "ab" * 32)
    except RuntimeError as exc:
        assert "required telemetry" in str(exc)
    else:
        raise AssertionError("a downgraded bridge was accepted")
    assert [call.args[0] for call in at.call_args_list] == ["AT+QPCMV=1,2", "AT+QPCMV=0"]
