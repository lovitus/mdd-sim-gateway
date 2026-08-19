import json
import io
import threading
from types import SimpleNamespace
from unittest.mock import Mock, patch

from agent.call_audio import (
    CallAudioController, CallAudioProbe, _decode_miniaudio_id,
    _windows_audio_inventory, probe_call_audio,
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
                "ok": True, "version": 1,
                "devices": [
                    {"kind": "playback", "id": _endpoint_id(playback)},
                    {"kind": "capture", "id": _endpoint_id(capture)},
                    {"kind": "playback", "id": _endpoint_id("{default-host-speaker}")},
                ],
            }))
        return SimpleNamespace(returncode=0, stderr="", stdout=json.dumps({
            "ok": True, "version": 1, "sample_rate": 8000,
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


def test_probe_fails_closed_when_modem_has_no_activation_capability():
    with patch("agent.call_audio.os.name", "nt"), \
            patch("agent.call_audio._helper_command", return_value=["audio-helper"]):
        result = probe_call_audio("COM14", Mock(return_value=b"ERROR\r\n"), runner=Mock())
    assert result.ready is False
    assert "does not advertise UAC" in result.reason


def test_controller_keeps_secret_out_of_argv_and_restores_qpcmv():
    class Process:
        def __init__(self):
            self.stdout = io.StringIO(json.dumps({"ok": True}) + "\n")
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
        details={"playback_id": "render-id", "capture_id": "capture-id"},
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
