import json
import subprocess
import unittest
from unittest.mock import AsyncMock, patch

from control.app import cellular_call, main


class Result:
    def __init__(self, stdout="", returncode=0, stderr=""):
        self.stdout = stdout
        self.returncode = returncode
        self.stderr = stderr


class CellularCallTests(unittest.TestCase):
    modem = "/org/freedesktop/ModemManager1/Modem/2"
    sim = "/org/freedesktop/ModemManager1/SIM/2"
    call = "/org/freedesktop/ModemManager1/Call/9"
    instances = [{"id": "3", "iccid": "card-b"}]

    def base(self, args):
        if args == ["mmcli", "-L"]:
            return Result(self.modem)
        if args == ["mmcli", "-m", self.modem, "--output-json"]:
            return Result(json.dumps({"modem": {
                "generic": {"sim": self.sim, "state": "registered"},
                "3gpp": {"registration-state": "home"},
            }}))
        if args == ["mmcli", "-i", self.sim, "--output-json"]:
            return Result(json.dumps({"sim": {"properties": {"iccid": "card-b"}}}))
        if args == ["mmcli", "-m", self.modem, "--voice-status", "--output-json"]:
            return Result(json.dumps({"modem": {"voice": {"emergency-only": "no"}}}))
        return None

    def test_dial_matches_iccid_checks_registration_and_starts_call(self):
        calls = []

        def runner(args, **kwargs):
            calls.append(tuple(args))
            self.assertNotIn("shell", kwargs)
            common = self.base(args)
            if common:
                return common
            if args == ["mmcli", "-m", self.modem,
                        "--voice-create-call=number=+441234567890", "--output-json"]:
                return Result(json.dumps({"modem": {"voice": {"created-call": self.call}}}))
            if args == ["mmcli", "-o", self.call, "--start", "--output-json"]:
                return Result("{}")
            return Result(returncode=1)

        result = cellular_call.dial(
            self.instances, "3", "+441234567890", runner=runner)
        self.assertTrue(result["ok"])
        self.assertEqual(result["status"], "dialing")
        self.assertFalse(result["audio"])
        self.assertIn(("mmcli", "-o", self.call, "--start", "--output-json"), calls)

    def test_disabled_modem_is_not_enabled_and_never_dials(self):
        calls = []

        def runner(args, **_kwargs):
            calls.append(tuple(args))
            if args == ["mmcli", "-L"]:
                return Result(self.modem)
            if args == ["mmcli", "-m", self.modem, "--output-json"]:
                return Result(json.dumps({"modem": {
                    "generic": {"sim": self.sim, "state": "disabled"},
                    "3gpp": {"registration-state": "unknown"}}}))
            if args == ["mmcli", "-i", self.sim, "--output-json"]:
                return Result(json.dumps({"sim": {"properties": {"iccid": "card-b"}}}))
            if args == ["mmcli", "-m", self.modem, "--voice-status", "--output-json"]:
                return Result("{}")
            return Result(returncode=1)

        result = cellular_call.dial(self.instances, "3", "12345", runner=runner)
        self.assertTrue(result["unavailable"])
        self.assertIn("disabled", result["error"].lower())
        self.assertFalse(any("--enable" in item for call in calls for item in call))
        self.assertFalse(any("--voice-create-call" in item for call in calls for item in call))

    def test_start_failure_deletes_only_the_unused_created_call(self):
        calls = []

        def runner(args, **_kwargs):
            calls.append(tuple(args))
            common = self.base(args)
            if common:
                return common
            if "--voice-create-call=number=12345" in args:
                return Result(json.dumps({"modem.voice.created-call": self.call}))
            if args == ["mmcli", "-o", self.call, "--start", "--output-json"]:
                return Result(returncode=1, stderr="network rejected")
            if args == ["mmcli", "-m", self.modem, f"--voice-delete-call={self.call}"]:
                return Result()
            return Result(returncode=1)

        result = cellular_call.dial(self.instances, "3", "12345", runner=runner)
        self.assertFalse(result["ok"])
        self.assertIn("network rejected", result["error"])
        self.assertIn(("mmcli", "-m", self.modem,
                       f"--voice-delete-call={self.call}"), calls)

    def test_status_reads_active_call_and_hangup_uses_matched_modem(self):
        calls = []

        def runner(args, **_kwargs):
            calls.append(tuple(args))
            common = self.base(args)
            if common:
                return common
            if args == ["mmcli", "-m", self.modem, "--voice-list-calls", "--output-json"]:
                return Result(json.dumps({"modem.voice.call": [self.call]}))
            if args == ["mmcli", "-o", self.call, "--output-json"]:
                return Result(json.dumps({"call": {"properties": {
                    "state": "active", "state-reason": "accepted",
                    "number": "+441234567890", "direction": "outgoing"}}}))
            if args == ["mmcli", "-m", self.modem, "--voice-hangup-all", "--output-json"]:
                return Result("{}")
            return Result(returncode=1)

        status = cellular_call.status(self.instances, "3", runner=runner)
        ended = cellular_call.hangup(self.instances, "3", runner=runner)
        self.assertEqual(status["status"], "active")
        self.assertEqual(status["call"]["direction"], "outgoing")
        self.assertFalse(status["audio"])
        self.assertTrue(ended["ok"])
        self.assertIn(("mmcli", "-m", self.modem, "--voice-hangup-all", "--output-json"), calls)

    def test_start_timeout_is_uncertain_and_not_retried(self):
        starts = 0

        def runner(args, **_kwargs):
            nonlocal starts
            common = self.base(args)
            if common:
                return common
            if "--voice-create-call=number=12345" in args:
                return Result(json.dumps({"modem.voice.created-call": self.call}))
            if args == ["mmcli", "-o", self.call, "--start", "--output-json"]:
                starts += 1
                raise subprocess.TimeoutExpired(args, 30)
            return Result(returncode=1)

        result = cellular_call.dial(self.instances, "3", "12345", runner=runner)
        self.assertTrue(result["uncertain"])
        self.assertEqual(result["status"], "unknown")
        self.assertEqual(starts, 1)


class CellularCallApiTests(unittest.IsolatedAsyncioTestCase):
    async def test_api_logs_cellular_transport_after_dial_starts(self):
        result = {"ok": True, "status": "dialing", "unavailable": False,
                  "uncertain": False, "audio": False}
        record = {"id": 8, "instance": "3", "direction": "out", "peer": "12345",
                  "status": "ringing", "transport": "cellular", "start_ts": 10}
        with patch.object(main.cfg, "get_instance", return_value={"id": "3"}), \
                patch.object(main.cfg, "list_instances", return_value=[
                    {"id": "3", "iccid": "card-b"}]), \
                patch.object(main.cellular_call, "dial", return_value=result) as dial, \
                patch.object(main.store, "save_cellular_call_lease"), \
                patch.object(main.store, "add_call", return_value=record) as add, \
                patch.object(main.hub, "broadcast", new=AsyncMock()) as broadcast:
            response = await main.api_cellular_call("3", {"to": "12345"})

        self.assertTrue(response["ok"])
        dial.assert_called_once()
        add.assert_called_once_with("3", "out", "12345", status="ringing",
                                    transport="cellular")
        broadcast.assert_awaited_once()

    async def test_unavailable_call_does_not_create_history(self):
        result = {"ok": False, "status": "unavailable", "unavailable": True,
                  "error": "radio disabled"}
        with patch.object(main.cfg, "get_instance", return_value={"id": "3"}), \
                patch.object(main.cfg, "list_instances", return_value=[]), \
                patch.object(main.cellular_call, "dial", return_value=result), \
                patch.object(main.store, "add_call") as add:
            with self.assertRaises(main.HTTPException) as raised:
                await main.api_cellular_call("3", {"to": "12345"})
        self.assertEqual(raised.exception.status_code, 409)
        add.assert_not_called()


if __name__ == "__main__":
    unittest.main()
