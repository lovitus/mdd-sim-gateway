import types
import sys
import re
import errno
import os
import json
import tempfile
import threading
import time
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

from agent.cellular_isolation import IsolationGuard
sys.modules.setdefault("websocket", types.SimpleNamespace())
from agent import modem_agent as modem_agent_module
from agent.modem_agent import (
    ModemCard, ModemControl, PrivateUsbModemCard, _PrivateUsbSerialAdapter,
    _close_mac_workers, _expected_private_data_vpcd_pause,
    _is_local_vpcd_close_error, _wait_data_owner_release,
    run as run_agent, windows_mbn_profile_xml,
)
from agent.uicc_health import UiccHealthMaintainer


def test_agent_package_digest_reads_manifest_and_fails_unknown_without_one(monkeypatch, tmp_path):
    from agent.package_manifest import write_package_metadata

    package = tmp_path / "package"
    package.mkdir()
    (package / "mdd-agent").write_text("cli", encoding="utf-8")
    (package / "mdd-call-audio-helper").write_text("audio", encoding="utf-8")
    expected = write_package_metadata(package, architecture="macos-arm64")
    monkeypatch.setenv("MDD_AGENT_MANIFEST_FILE", str(package / "manifest.json"))
    monkeypatch.delenv("MDD_AGENT_PACKAGE_DIGEST", raising=False)
    assert modem_agent_module._agent_package_digest() == expected

    original_manifest = json.loads((package / "manifest.json").read_text(encoding="utf-8"))
    malformed_cases = [
        lambda value: value.__setitem__("version", True),
        lambda value: value["files"][0].__setitem__("name", 123),
        lambda value: value["files"][0].__setitem__("sha256", 123),
        lambda value: value["files"][0].__setitem__("size", True),
        lambda value: value["files"][0].__setitem__("size", "3"),
        lambda value: value["files"][0].__setitem__("size", 3.0),
    ]
    for mutate in malformed_cases:
        value = json.loads(json.dumps(original_manifest))
        mutate(value)
        (package / "manifest.json").write_text(json.dumps(value), encoding="utf-8")
        assert modem_agent_module._agent_package_digest() == "unknown"
    (package / "manifest.json").write_text(
        json.dumps(original_manifest, sort_keys=True, separators=(",", ":")) + "\n",
        encoding="utf-8")

    (package / "mdd-call-audio-helper").write_text("mutated", encoding="utf-8")
    assert modem_agent_module._agent_package_digest() == "unknown"

    extra_package = tmp_path / "extra-package"
    extra_package.mkdir()
    (extra_package / "mdd-agent").write_text("cli", encoding="utf-8")
    write_package_metadata(extra_package, architecture="macos-arm64")
    (extra_package / "unexpected").write_text("extra", encoding="utf-8")
    monkeypatch.setenv("MDD_AGENT_MANIFEST_FILE", str(extra_package / "manifest.json"))
    assert modem_agent_module._agent_package_digest() == "unknown"

    nested_metadata_package = tmp_path / "nested-metadata-package"
    nested_metadata_package.mkdir()
    (nested_metadata_package / "mdd-agent").write_text("cli", encoding="utf-8")
    write_package_metadata(nested_metadata_package, architecture="macos-arm64")
    (nested_metadata_package / "sub").mkdir()
    (nested_metadata_package / "sub" / "manifest.json").write_text("extra", encoding="utf-8")
    monkeypatch.setenv(
        "MDD_AGENT_MANIFEST_FILE", str(nested_metadata_package / "manifest.json"))
    assert modem_agent_module._agent_package_digest() == "unknown"
    (nested_metadata_package / "sub" / "manifest.json").unlink()
    (nested_metadata_package / "sub" / "control-agent-allowlist.env").write_text(
        "extra", encoding="utf-8")
    assert modem_agent_module._agent_package_digest() == "unknown"

    symlink_package = tmp_path / "symlink-package"
    symlink_package.mkdir()
    (symlink_package / "mdd-agent").write_text("cli", encoding="utf-8")
    write_package_metadata(symlink_package, architecture="macos-arm64")
    link_target = tmp_path / "outside-dir"
    link_target.mkdir()
    try:
        (symlink_package / "linked-dir").symlink_to(link_target, target_is_directory=True)
    except OSError:
        pass
    else:
        monkeypatch.setenv("MDD_AGENT_MANIFEST_FILE", str(symlink_package / "manifest.json"))
        assert modem_agent_module._agent_package_digest() == "unknown"

    internal_symlink_package = tmp_path / "internal-symlink-package"
    internal_symlink_package.mkdir()
    (internal_symlink_package / "payload").write_text("payload", encoding="utf-8")
    try:
        (internal_symlink_package / "payload-link").symlink_to("payload")
    except OSError:
        pass
    else:
        internal_symlink_digest = write_package_metadata(
            internal_symlink_package, architecture="macos-arm64")
        monkeypatch.setenv(
            "MDD_AGENT_MANIFEST_FILE", str(internal_symlink_package / "manifest.json"))
        assert modem_agent_module._agent_package_digest() == internal_symlink_digest

    real_package = tmp_path / "real-package"
    real_package.mkdir()
    (real_package / "mdd-agent").write_text("cli", encoding="utf-8")
    write_package_metadata(real_package, architecture="macos-arm64")
    linked_package = tmp_path / "linked-package"
    try:
        linked_package.symlink_to(real_package, target_is_directory=True)
    except OSError:
        pass
    else:
        monkeypatch.setenv("MDD_AGENT_MANIFEST_FILE", str(linked_package / "manifest.json"))
        assert modem_agent_module._agent_package_digest() == "unknown"

    gui_package = tmp_path / "gui-package"
    gui_exe = gui_package / "MDD Agent.app" / "Contents" / "MacOS" / "mdd-agent-gui"
    gui_exe.parent.mkdir(parents=True)
    gui_exe.write_text(
        "gui", encoding="utf-8")
    gui_digest = write_package_metadata(gui_package, architecture="macos-arm64")
    monkeypatch.delenv("MDD_AGENT_MANIFEST_FILE", raising=False)
    monkeypatch.setattr(modem_agent_module.sys, "executable", str(gui_exe))
    assert modem_agent_module._agent_package_digest() == gui_digest
    app_resource = gui_package / "MDD Agent.app" / "Contents" / "Resources" / "manifest.json"
    app_resource.parent.mkdir(parents=True)
    app_resource.write_text("legacy embedded manifest", encoding="utf-8")
    assert modem_agent_module._agent_package_digest() == "unknown"

    monkeypatch.delenv("MDD_AGENT_MANIFEST_FILE", raising=False)
    isolated = tmp_path / "isolated" / "agent" / "modem_agent.py"
    isolated.parent.mkdir(parents=True)
    isolated.write_text("", encoding="utf-8")
    monkeypatch.setattr(modem_agent_module, "__file__", str(isolated))
    monkeypatch.setattr(modem_agent_module.sys, "executable", str(tmp_path / "bin" / "python"))
    assert modem_agent_module._agent_package_digest() == "unknown"


class ModemAgentSafetyTests(unittest.TestCase):
    def _paid_call_control(self, root):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(
            imei="862547055201716", iccid="89852312388530152529",
            private_raw_usb=False)
        with patch("agent.modem_agent.default_data_dir", return_value=Path(root)):
            return ModemControl(args, modem)

    def test_paid_call_lease_is_durable_and_matching_renewal_is_bounded(self):
        with tempfile.TemporaryDirectory() as directory:
            control = self._paid_call_control(directory)
            self.addCleanup(control.stop.set)
            lease = "call-12345678"
            control._arm_paid_call_lease(lease, "out")
            marker = Path(directory) / "state" / "paid-call-862547055201716.json"
            self.assertTrue(marker.exists())
            self.assertTrue(control._renew_paid_call_lease(lease)["ok"])
            self.assertFalse(control._renew_paid_call_lease("call-wrong000")["ok"])
            control._clear_paid_call_lease()
            self.assertFalse(marker.exists())

    def test_paid_call_renewal_is_rejected_after_termination_starts(self):
        with tempfile.TemporaryDirectory() as directory:
            control = self._paid_call_control(directory)
            self.addCleanup(control.stop.set)
            lease = "call-12345678"
            control._arm_paid_call_lease(lease, "out")
            control._paid_call_termination_requested = True
            control._paid_call_deadline = float("inf")

            result = control._renew_paid_call_lease(lease)

            self.assertFalse(result["ok"])
            self.assertEqual(result["status"], "terminating")
            self.assertEqual(control._paid_call_deadline, float("inf"))
            control._clear_paid_call_lease()

    def test_agent_restart_refuses_to_resume_old_paid_call_lease(self):
        with tempfile.TemporaryDirectory() as directory:
            first = self._paid_call_control(directory)
            first._arm_paid_call_lease("call-12345678", "out")
            first.stop.set()
            second = self._paid_call_control(directory)
            self.addCleanup(second.stop.set)
            renewed = second._renew_paid_call_lease("call-12345678")
            self.assertFalse(renewed["ok"])
            self.assertEqual(renewed["status"], "restart_recovery")
            second._clear_paid_call_lease()

    def test_expired_paid_call_lease_uses_one_radio_cutoff_after_unconfirmed_hangup(self):
        with tempfile.TemporaryDirectory() as directory:
            control = self._paid_call_control(directory)
            self.addCleanup(control.stop.set)
            control._verified_call_hangup = Mock(return_value={
                "ok": False, "status": "active", "terminal_confirmed": False})
            control._fresh_call_status = Mock(return_value={
                "ok": True, "status": "idle", "fresh": True, "authoritative": True})
            control._paid_call_radio_cutoff = Mock(return_value={
                "ok": True, "status": "off"})
            control._arm_paid_call_lease("call-12345678", "out")
            control._paid_call_deadline = 0.0
            deadline = __import__("time").monotonic() + 2
            while control._paid_call_lease_id and __import__("time").monotonic() < deadline:
                __import__("time").sleep(0.05)
            self.assertEqual(control._paid_call_lease_id, "")
            control._verified_call_hangup.assert_called_once()
            control._paid_call_radio_cutoff.assert_called_once()

    def test_orderly_shutdown_terminates_paid_call_before_control_and_modem_close(self):
        events = []

        class Control:
            def __init__(self):
                self.stop = __import__("threading").Event()
                original_set = self.stop.set
                self.stop.set = lambda: (events.append("control.stop"), original_set())[1]

            def run(self):
                self.stop.wait()

            def shutdown_paid_call(self):
                events.append("paid_call.shutdown")
                return {"ok": True, "terminal_confirmed": True}

            def begin_shutdown(self):
                events.append("shutdown.gate")

        class Modem:
            connection = None

            def close(self):
                events.append("modem.close")

        stopped = __import__("threading").Event()
        stopped.set()
        args = types.SimpleNamespace(no_pcsc=True, retry=0.01, reset_pin=False)
        run_agent(args, stopped, _allow_private_supervisor=False,
                  modem_override=Modem(), control_override=Control())

        self.assertLess(events.index("paid_call.shutdown"), events.index("control.stop"))
        self.assertLess(events.index("paid_call.shutdown"), events.index("modem.close"))

    def test_orderly_shutdown_unconfirmed_call_keeps_marker_and_single_cutoff(self):
        with tempfile.TemporaryDirectory() as directory:
            control = self._paid_call_control(directory)
            self.addCleanup(control.stop.set)
            control._verified_call_hangup = Mock(return_value={
                "ok": False, "status": "active", "fresh": True,
                "authoritative": True, "terminal_confirmed": False})
            control._fresh_call_status = Mock(return_value={
                "ok": True, "status": "active", "fresh": True,
                "authoritative": True})
            control._paid_call_radio_cutoff = Mock(return_value={
                "ok": True, "status": "off"})
            control._arm_paid_call_lease("call-12345678", "out")

            result = control.shutdown_paid_call()

            self.assertFalse(result["terminal_confirmed"])
            self.assertEqual(control._paid_call_lease_id, "call-12345678")
            self.assertTrue((Path(directory) / "state" /
                             "paid-call-862547055201716.json").exists())
            control._paid_call_radio_cutoff.assert_called_once()
            self.assertTrue(control._paid_call_safety_hold())
            control._clear_paid_call_lease()

    def test_paid_call_fail_safe_is_single_flight_across_watchdog_and_shutdown(self):
        import threading
        with tempfile.TemporaryDirectory() as directory:
            control = self._paid_call_control(directory)
            self.addCleanup(control.stop.set)
            entered = threading.Event()
            release = threading.Event()

            def hangup(**_kwargs):
                entered.set()
                release.wait(2)
                return {"ok": False, "status": "active", "fresh": True,
                        "authoritative": True, "terminal_confirmed": False}

            control._verified_call_hangup = Mock(side_effect=hangup)
            control._paid_call_radio_cutoff = Mock(return_value={
                "ok": True, "status": "off"})
            control._fresh_call_status = Mock(return_value={
                "ok": True, "status": "active", "fresh": True,
                "authoritative": True})
            control._arm_paid_call_lease("call-12345678", "out")
            results = []
            first = threading.Thread(target=lambda: results.append(
                control._terminate_paid_call_fail_safe(radio_cutoff=True)))
            second = threading.Thread(target=lambda: results.append(
                control.shutdown_paid_call()))
            first.start()
            self.assertTrue(entered.wait(1))
            second.start()
            release.set()
            first.join(3)
            second.join(3)

            self.assertEqual(len(results), 2)
            self.assertEqual(control._verified_call_hangup.call_count, 1)
            self.assertEqual(control._paid_call_radio_cutoff.call_count, 1)
            self.assertEqual(control._fresh_call_status.call_count, 2)
            control._clear_paid_call_lease()

    def test_failed_watchdog_result_is_reused_by_later_shutdown(self):
        with tempfile.TemporaryDirectory() as directory:
            control = self._paid_call_control(directory)
            self.addCleanup(control.stop.set)
            control._verified_call_hangup = Mock(return_value={
                "ok": False, "status": "active", "fresh": True,
                "authoritative": True, "terminal_confirmed": False})
            control._paid_call_radio_cutoff = Mock(return_value={
                "ok": True, "status": "off"})
            control._fresh_call_status = Mock(return_value={
                "ok": True, "status": "active", "fresh": True,
                "authoritative": True})
            control._arm_paid_call_lease("call-12345678", "out")

            first = control._terminate_paid_call_fail_safe(radio_cutoff=True)
            second = control.shutdown_paid_call()

            self.assertEqual(second, first)
            self.assertEqual(control._verified_call_hangup.call_count, 1)
            self.assertEqual(control._paid_call_radio_cutoff.call_count, 1)
            control._clear_paid_call_lease()

    def test_provider_timeouts_are_clipped_inside_fail_safe_budget(self):
        with tempfile.TemporaryDirectory() as directory:
            control = self._paid_call_control(directory)
            self.addCleanup(control.stop.set)
            observed = []
            control.modem.platform_provider = types.SimpleNamespace(
                call_hangup=lambda timeout: observed.append(("hangup", timeout)) or {
                    "ok": False, "status": "active", "fresh": True,
                    "authoritative": True, "terminal_confirmed": False},
                call_status=lambda timeout: observed.append(("status", timeout)) or {
                    "ok": True, "status": "idle", "fresh": True,
                    "authoritative": True},
            )
            control._paid_call_radio_cutoff = Mock(return_value={
                "ok": True, "status": "off"})
            control._arm_paid_call_lease("call-12345678", "out")

            result = control._terminate_paid_call_fail_safe(
                radio_cutoff=True, total_timeout=50)

            self.assertTrue(result["terminal_confirmed"])
            self.assertLessEqual(observed[0][1], 20)
            self.assertTrue(all(timeout <= 5 for name, timeout in observed if name == "status"))
            self.assertLessEqual(control._paid_call_radio_cutoff.call_args.args[0], 15)

    def test_fail_safe_lock_wait_is_inside_total_budget_and_preserves_marker(self):
        with tempfile.TemporaryDirectory() as directory:
            control = self._paid_call_control(directory)
            self.addCleanup(control.stop.set)
            control._arm_paid_call_lease("call-12345678", "out")
            self.assertTrue(control.operation_lock.acquire(timeout=1))
            try:
                started = time.monotonic()
                result = control._terminate_paid_call_fail_safe(
                    radio_cutoff=True, total_timeout=0.1)
                elapsed = time.monotonic() - started
            finally:
                control.operation_lock.release()

            self.assertTrue(result["cleanup_blocked"])
            self.assertLess(elapsed, 0.5)
            self.assertEqual(control._paid_call_lease_id, "call-12345678")
            self.assertTrue(control._paid_call_safety_hold())
            control._clear_paid_call_lease()

    def test_watchdog_retries_cleanup_blocked_then_confirms_termination(self):
        with tempfile.TemporaryDirectory() as directory:
            control = self._paid_call_control(directory)
            self.addCleanup(control.stop.set)
            control._paid_call_watchdog_timeout = 0.2
            control._paid_call_retry_delay = 0.05
            control._paid_call_retry_limit = 2
            control._verified_call_hangup = Mock(return_value={
                "ok": False, "status": "active", "fresh": True,
                "authoritative": True, "terminal_confirmed": False})
            control._paid_call_radio_cutoff = Mock(return_value={
                "ok": True, "status": "off"})
            control._fresh_call_status = Mock(return_value={
                "ok": True, "status": "idle", "fresh": True,
                "authoritative": True})
            control._arm_paid_call_lease("call-12345678", "out")
            self.assertTrue(control.operation_lock.acquire(timeout=1))
            control._paid_call_deadline = 0.0
            try:
                deadline = time.monotonic() + 1
                while (control._paid_call_cleanup_retries < 1 and
                       time.monotonic() < deadline):
                    time.sleep(0.01)
                self.assertEqual(control._paid_call_cleanup_retries, 1)
                retry_deadline = control._paid_call_deadline
                renewed = control._renew_paid_call_lease("call-12345678")
                self.assertEqual(renewed["status"], "terminating")
                self.assertEqual(control._paid_call_deadline, retry_deadline)
            finally:
                control.operation_lock.release()

            self.assertTrue(control.paid_call_cleared.wait(2))
            self.assertEqual(control._paid_call_lease_id, "")
            control._paid_call_radio_cutoff.assert_called_once()

    def test_cleanup_blocked_claim_notifies_waiter_to_reclaim(self):
        with tempfile.TemporaryDirectory() as directory:
            control = self._paid_call_control(directory)
            self.addCleanup(control.stop.set)
            control._verified_call_hangup = Mock(return_value={
                "ok": True, "status": "idle", "fresh": True,
                "authoritative": True, "terminal_confirmed": True})
            control._arm_paid_call_lease("call-12345678", "out")
            self.assertTrue(control.operation_lock.acquire(timeout=1))
            results = {}
            first = threading.Thread(target=lambda: results.setdefault(
                "first", control._terminate_paid_call_fail_safe(
                    radio_cutoff=True, total_timeout=0.1)))
            second = threading.Thread(target=lambda: results.setdefault(
                "second", control._terminate_paid_call_fail_safe(
                    radio_cutoff=True, total_timeout=1.0)))
            first.start()
            time.sleep(0.01)
            second.start()
            first.join(1)
            self.assertTrue(results["first"]["cleanup_blocked"])
            control.operation_lock.release()
            second.join(2)

            self.assertTrue(results["second"]["terminal_confirmed"])
            self.assertEqual(control._paid_call_lease_id, "")

    def test_quarantined_shutdown_exits_after_authoritative_call_clear(self):
        events = []

        class Control:
            def __init__(self):
                self.stop = threading.Event()
                self.paid_call_cleared = threading.Event()
                self.hold = True

            def run(self):
                self.stop.wait()

            def begin_shutdown(self):
                events.append("shutdown.gate")

            def shutdown_paid_call(self):
                return {"ok": False, "terminal_confirmed": False}

            def _paid_call_safety_hold(self):
                return self.hold

        class Modem:
            connection = None

            def close(self):
                events.append("modem.close")

        stopped = threading.Event()
        stopped.set()
        control = Control()
        args = types.SimpleNamespace(no_pcsc=True, retry=0.01, reset_pin=False)
        thread = threading.Thread(target=lambda: run_agent(
            args, stopped, _allow_private_supervisor=False,
            modem_override=Modem(), control_override=control))
        thread.start()
        time.sleep(0.05)
        self.assertTrue(thread.is_alive())
        self.assertNotIn("modem.close", events)

        control.hold = False
        control.paid_call_cleared.set()
        thread.join(2)

        self.assertFalse(thread.is_alive())
        self.assertIn("modem.close", events)

    def test_shutdown_gate_rejects_queued_paid_actions_before_at_commands(self):
        with tempfile.TemporaryDirectory() as directory:
            control = self._paid_call_control(directory)
            self.addCleanup(control.stop.set)
            control.modem.capabilities = {"call": True}
            control.modem._at = Mock()
            control.begin_shutdown()

            dial = control.execute("call.dial", {
                "to": "22333322", "lease_id": "call-12345678"})
            answer = control.execute("call.answer", {"lease_id": "call-12345678"})

            self.assertEqual(dial["status"], "shutting_down")
            self.assertEqual(answer["status"], "shutting_down")
            control.modem._at.assert_not_called()
            self.assertFalse(control.paid_call_active.is_set())

    def test_paid_call_commit_rechecks_shutdown_gate_under_lease_lock(self):
        from agent.modem_agent import AgentShuttingDownError
        with tempfile.TemporaryDirectory() as directory:
            control = self._paid_call_control(directory)
            self.addCleanup(control.stop.set)
            control.begin_shutdown()

            with self.assertRaises(AgentShuttingDownError):
                control._arm_paid_call_lease("call-12345678", "out")

            self.assertFalse(control.paid_call_active.is_set())

    def test_install_maintenance_fences_new_paid_call_and_can_cancel(self):
        from agent.modem_agent import AgentShuttingDownError
        with tempfile.TemporaryDirectory() as directory:
            control = self._paid_call_control(directory)
            self.addCleanup(control.stop.set)

            prepared = control.prepare_install_maintenance()

            self.assertTrue(prepared["ready"])
            self.assertTrue(control.shutdown_started.is_set())
            with self.assertRaises(AgentShuttingDownError):
                control._arm_paid_call_lease("call-12345678", "out")
            cancelled = control.cancel_install_maintenance(prepared["nonce"])
            self.assertTrue(cancelled["cancelled"])
            self.assertFalse(control.shutdown_started.is_set())

    def test_install_maintenance_rejects_active_paid_call_without_leaving_gate(self):
        with tempfile.TemporaryDirectory() as directory:
            control = self._paid_call_control(directory)
            self.addCleanup(control.stop.set)
            control._arm_paid_call_lease("call-12345678", "out")

            prepared = control.prepare_install_maintenance()

            self.assertFalse(prepared["ready"])
            self.assertEqual(prepared["status"], "paid_call_active")
            self.assertFalse(control.shutdown_started.is_set())
            control._clear_paid_call_lease()

    def test_multiple_mac_workers_share_one_close_deadline(self):
        import threading
        import time

        class Thread:
            def __init__(self):
                self.alive = True

            def join(self, timeout):
                time.sleep(max(0.0, min(timeout, 0.04)))

            def is_alive(self):
                return self.alive

        class Item:
            def __init__(self, generation):
                self.stop = threading.Event()
                self.thread = Thread()
                self.attachment = types.SimpleNamespace(generation=generation)
                self.control = types.SimpleNamespace(
                    _paid_call_armed=lambda: False, begin_shutdown=lambda: None)
                self.modem = types.SimpleNamespace(close=lambda: setattr(
                    self.thread, "alive", False))

            def alive(self):
                return self.thread.is_alive()

        workers = [Item("one"), Item("two"), Item("three")]
        started = time.monotonic()
        failures = _close_mac_workers(workers, timeout=0.05, force_timeout=0.05)
        elapsed = time.monotonic() - started

        self.assertEqual(failures, [])
        self.assertLess(elapsed, 0.15)
        self.assertTrue(all(item.stop.is_set() for item in workers))

    def test_mac_close_never_force_closes_worker_with_active_paid_lease(self):
        class Thread:
            def join(self, _timeout):
                return None

            def is_alive(self):
                return True

        modem = types.SimpleNamespace(close=Mock())
        item = types.SimpleNamespace(
            stop=threading.Event(), thread=Thread(), modem=modem,
            attachment=types.SimpleNamespace(generation="active-call"),
            control=types.SimpleNamespace(
                _paid_call_armed=lambda: True, begin_shutdown=lambda: None),
            alive=lambda: True,
        )

        failures = _close_mac_workers([item], timeout=0, force_timeout=0)

        modem.close.assert_not_called()
        self.assertEqual(len(failures), 1)
        self.assertIn("paid-call cleanup", failures[0])

    def test_mac_close_shuts_paid_action_gate_before_stop_and_force_close(self):
        events = []

        class Thread:
            def join(self, _timeout):
                return None

            def is_alive(self):
                return True

        class Stop:
            def set(self):
                events.append("stop")

        control = types.SimpleNamespace(
            begin_shutdown=lambda: events.append("gate"),
            _paid_call_armed=lambda: False,
        )
        modem = types.SimpleNamespace(close=lambda: events.append("close"))
        item = types.SimpleNamespace(
            stop=Stop(), thread=Thread(), modem=modem,
            attachment=types.SimpleNamespace(generation="queued-dial"),
            control=control, alive=lambda: True,
        )

        _close_mac_workers([item], timeout=0, force_timeout=0)

        self.assertLess(events.index("gate"), events.index("stop"))
        self.assertLess(events.index("gate"), events.index("close"))

    def test_queued_dial_cannot_commit_after_mac_close_gate(self):
        with tempfile.TemporaryDirectory() as directory:
            control = self._paid_call_control(directory)
            self.addCleanup(control.stop.set)
            entered = threading.Event()
            release = threading.Event()

            def uicc_health_status(**_kwargs):
                entered.set()
                release.wait(2)
                return {"ready": True}

            control.modem.capabilities = {"call": True}
            control.modem.uicc_health_status = uicc_health_status
            control.modem.voice_registration_status = Mock(return_value={"ready": True})
            control.modem.platform_provider = types.SimpleNamespace(call_dial=Mock())
            result = {}
            dial = threading.Thread(target=lambda: result.update(control.execute(
                "call.dial", {"to": "22333322", "lease_id": "call-12345678"})))
            dial.start()
            self.assertTrue(entered.wait(1))

            class WorkerThread:
                def join(self, _timeout):
                    return None

                def is_alive(self):
                    return True

            worker = types.SimpleNamespace(
                stop=threading.Event(), thread=WorkerThread(), modem=types.SimpleNamespace(
                    close=Mock()), control=control,
                attachment=types.SimpleNamespace(generation="queued-dial"), alive=lambda: True,
            )
            _close_mac_workers([worker], timeout=0, force_timeout=0)
            release.set()
            dial.join(2)

            self.assertEqual(result["status"], "shutting_down")
            self.assertFalse(control.paid_call_active.is_set())
            control.modem.platform_provider.call_dial.assert_not_called()

    def test_unconfirmed_paid_call_blocks_radio_and_data_restore(self):
        with tempfile.TemporaryDirectory() as directory:
            control = self._paid_call_control(directory)
            self.addCleanup(control.stop.set)
            control._arm_paid_call_lease("call-12345678", "out")
            control._paid_call_termination_requested = True
            control._cellular_ensure = Mock()
            radio = control.execute("radio.set", {"enabled": True})
            data = control.execute("cellular.ensure", {})
            self.assertEqual(radio["status"], "safety_hold")
            self.assertEqual(data["status"], "safety_hold")
            control._cellular_ensure.assert_not_called()
            control._clear_paid_call_lease()

    def test_dial_transport_failure_retains_paid_call_lease(self):
        with tempfile.TemporaryDirectory() as directory:
            control = self._paid_call_control(directory)
            self.addCleanup(control.stop.set)
            control.modem.capabilities = {"call": True}
            control.modem.uicc_health_status = Mock(return_value={"ready": True})
            control.modem.voice_registration_status = Mock(return_value={"ready": True})
            control.modem.platform_provider = types.SimpleNamespace(
                call_dial=Mock(side_effect=TimeoutError("response lost")))
            with self.assertRaisesRegex(TimeoutError, "response lost"):
                control.execute("call.dial", {
                    "to": "22333322", "lease_id": "call-12345678"})
            self.assertEqual(control._paid_call_lease_id, "call-12345678")
            self.assertTrue((Path(directory) / "state" /
                             "paid-call-862547055201716.json").exists())
            control._clear_paid_call_lease()

    def test_answer_requires_fresh_authoritative_incoming_call_before_ata(self):
        with tempfile.TemporaryDirectory() as directory:
            control = self._paid_call_control(directory)
            self.addCleanup(control.stop.set)
            control.modem.capabilities = {"call": True}
            control.modem.platform_provider = types.SimpleNamespace(call_answer=Mock())
            control._fresh_call_status = Mock(return_value={
                "ok": True, "status": "idle", "fresh": True, "authoritative": True})

            result = control.execute("call.answer", {"lease_id": "call-12345678"})

            self.assertEqual(result["status"], "not_ringing")
            control.modem.platform_provider.call_answer.assert_not_called()
            self.assertFalse(control.paid_call_active.is_set())

    def test_answer_arms_durable_lease_after_fresh_incoming_sample(self):
        with tempfile.TemporaryDirectory() as directory:
            control = self._paid_call_control(directory)
            self.addCleanup(control.stop.set)
            marker = Path(directory) / "state" / "paid-call-862547055201716.json"
            control.modem.capabilities = {"call": True}
            control._fresh_call_status = Mock(return_value={
                "ok": True, "status": "ringing-in", "fresh": True,
                "authoritative": True})

            def answer():
                self.assertTrue(marker.exists())
                return {"ok": True, "status": "active"}

            control.modem.platform_provider = types.SimpleNamespace(call_answer=Mock(
                side_effect=answer))

            result = control.execute("call.answer", {"lease_id": "call-12345678"})

            self.assertEqual(result["status"], "active")
            control.modem.platform_provider.call_answer.assert_called_once()
            control._clear_paid_call_lease()

    def test_corrupt_paid_call_marker_enters_fail_closed_quarantine(self):
        with tempfile.TemporaryDirectory() as directory:
            marker = Path(directory) / "state" / "paid-call-862547055201716.json"
            marker.parent.mkdir(parents=True)
            marker.write_text("{damaged", encoding="utf-8")
            control = self._paid_call_control(directory)
            self.addCleanup(control.stop.set)
            self.assertTrue(control._paid_call_safety_hold())
            with self.assertRaisesRegex(Exception, "quarantined"):
                control._arm_paid_call_lease("call-12345678", "out")
            self.assertEqual(control.execute("radio.set", {"enabled": True})["status"],
                             "safety_hold")
            control._clear_paid_call_lease()

    def test_unreadable_paid_call_marker_is_not_treated_as_absent(self):
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(Path, "read_text", side_effect=PermissionError("denied")):
            marker = Path(directory) / "state" / "paid-call-862547055201716.json"
            marker.parent.mkdir(parents=True)
            marker.touch()
            control = self._paid_call_control(directory)
            self.addCleanup(control.stop.set)
            self.assertTrue(control._paid_call_safety_hold())
            self.assertIn("denied", control._paid_call_marker_error)

    def test_private_usb_expected_apdu_rejection_does_not_open_uicc_circuit(self):
        modem = object.__new__(PrivateUsbModemCard)
        modem._sim_apdu_failures = 1
        modem.capabilities = {"sim_apdu": True}
        modem._at = Mock(side_effect=[RuntimeError("ERROR"), b"+CPIN: READY\r\nOK\r\n"])

        self.assertEqual(modem.transmit(bytes.fromhex("00A4040000")), bytes.fromhex("6F00"))
        self.assertEqual(modem._sim_apdu_failures, 0)
        self.assertTrue(modem.capabilities["sim_apdu"])

    def test_private_usb_uicc_health_failures_still_open_circuit(self):
        modem = object.__new__(PrivateUsbModemCard)
        modem._sim_apdu_failures = 1
        modem.capabilities = {"sim_apdu": True}
        modem._at = Mock(side_effect=RuntimeError("device gone"))

        with self.assertRaisesRegex(Exception, "UICC health failures"):
            modem.transmit(bytes.fromhex("00A40000023F00"))
        self.assertFalse(modem.capabilities["sim_apdu"])

    def test_private_usb_apdu_is_paused_while_data_owns_sim_channel(self):
        modem = object.__new__(PrivateUsbModemCard)
        modem.cellular_active = __import__("threading").Event()
        modem.cellular_active.set()
        modem._at = Mock(side_effect=AssertionError("APDU touched active data channel"))

        self.assertEqual(modem.transmit(bytes.fromhex("00A40000023F00")),
                         bytes.fromhex("6985"))
        modem._at.assert_not_called()

    def test_private_data_vpcd_pause_is_attempt_scoped_and_paid_safe(self):
        modem = types.SimpleNamespace(private_raw_usb=True)
        control = types.SimpleNamespace(
            cellular_active=threading.Event(),
            paid_call_active=threading.Event(),
            _paid_call_safety_hold=Mock(return_value=False),
        )
        attempt = threading.Event()

        self.assertFalse(_expected_private_data_vpcd_pause(
            modem, control, attempt, OSError(errno.EBADF, "Bad file descriptor")))
        attempt.set()
        self.assertFalse(_expected_private_data_vpcd_pause(
            modem, control, attempt, OSError(errno.EBADF, "Bad file descriptor")))
        control.cellular_active.set()
        self.assertTrue(_expected_private_data_vpcd_pause(
            modem, control, attempt, OSError(errno.EBADF, "Bad file descriptor")))
        self.assertTrue(_is_local_vpcd_close_error(RuntimeError("socket is closed")))

        self.assertFalse(_expected_private_data_vpcd_pause(
            modem, control, attempt, RuntimeError("TLS handshake failed")))
        control.paid_call_active.set()
        self.assertFalse(_expected_private_data_vpcd_pause(
            modem, control, attempt, OSError(errno.EBADF, "Bad file descriptor")))
        control.paid_call_active.clear()
        control._paid_call_safety_hold.return_value = True
        self.assertFalse(_expected_private_data_vpcd_pause(
            modem, control, attempt, OSError(errno.EBADF, "Bad file descriptor")))

        control._paid_call_safety_hold.return_value = False
        attempt.clear()
        self.assertFalse(_expected_private_data_vpcd_pause(
            modem, control, attempt, OSError(errno.EBADF, "Bad file descriptor")))

    def test_private_data_pause_closes_vpcd_without_gateway_warning(self):
        stopped = threading.Event()
        events = []

        class Control:
            def __init__(self):
                self.stop = threading.Event()
                self.cellular_active = threading.Event()
                self.data_reconciled = threading.Event()
                self.paid_call_active = threading.Event()
                self.paid_call_cleared = threading.Event()

            def run(self):
                self.stop.wait(1)

            def begin_shutdown(self):
                events.append("shutdown.gate")

            def shutdown_paid_call(self):
                events.append("paid_call.shutdown")
                return {"ok": True, "terminal_confirmed": True}

            def _paid_call_safety_hold(self):
                return False

        class Modem:
            connection = object()
            sim_via_mbn = False
            private_raw_usb = True
            capabilities = {"sim_apdu": True}
            imei = "862547055201716"
            iccid = "89852312388530152529"

            def connect(self):
                raise AssertionError("connected modem should not reconnect")

            def close(self):
                events.append("modem.close")

        class Client:
            def __init__(self, control):
                self.control = control
                self.closed = threading.Event()

            def recv_frame(self):
                events.append("client.recv")
                self.control.cellular_active.set()
                self.assert_closed()
                stopped.set()
                raise OSError(errno.EBADF, "Bad file descriptor")

            def assert_closed(self):
                if not self.closed.wait(1):
                    raise AssertionError("private data pause did not close VPCD client")

            def close(self):
                events.append("client.close")
                self.closed.set()

        control = Control()
        client = Client(control)
        args = types.SimpleNamespace(
            no_pcsc=True, retry=0.01, reset_pin=False, name="",
            host="127.0.0.1", gateway_port=8443, path="/ws", token="", pin="")
        with patch("agent.modem_agent.connect_wss", return_value=client), \
                self.assertLogs("mdd-modem-agent", level="INFO") as logs:
            run_agent(args, stopped, _allow_private_supervisor=False,
                      modem_override=Modem(), control_override=control)

        output = "\n".join(logs.output)
        self.assertIn("vpcd_private_data_pause", output)
        self.assertNotIn("Gateway connection failed", output)
        self.assertIn("paid_call.shutdown", events)
        self.assertEqual(events.count("modem.close"), 1)  # final controlled shutdown only

    def test_private_usb_serial_adapter_preserves_existing_at_and_prompt_flow(self):
        backend = Mock()
        backend.at.return_value = "AT\r\r\nOK\r\n"
        backend.exchange.return_value = b"\r\n+CMGS: 7\r\n\r\nOK\r\n"
        adapter = _PrivateUsbSerialAdapter(backend)

        adapter.write(b"AT\r")
        self.assertEqual(adapter.read(100), b"AT\r\r\nOK\r\n")
        adapter.write(b"hello\x1a")
        self.assertIn(b"+CMGS: 7", adapter.read(100))

        backend.at.assert_called_once_with("AT")
        backend.exchange.assert_called_once_with(b"hello\x1a")

    def test_pending_windows_sms_configuration_is_refreshed_and_rate_limited(self):
        control = self._control()
        control.modem.service_centre = Mock(return_value="+85362101201")

        first = control._refresh_sms_configuration()
        second = control._refresh_sms_configuration()

        self.assertTrue(first["ok"])
        self.assertTrue(second["ok"])
        self.assertTrue(second["cached"])
        self.assertEqual(control.modem.service_centre.call_args_list,
                         [unittest.mock.call(force=True), unittest.mock.call()])

    def test_sms_configuration_pending_hresult_is_not_a_generic_failure(self):
        control = self._control()
        self.assertTrue(control._is_sms_configuration_pending("0x8000000A"))
        self.assertTrue(control._is_sms_configuration_pending("E_PENDING"))
        self.assertFalse(control._is_sms_configuration_pending("0x80070490"))

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

    def test_reverse_websocket_timeout_only_applies_to_handshake(self):
        control = self._control()
        tunnel = Mock()
        with patch("agent.modem_agent.websocket.create_connection", return_value=tunnel,
                   create=True) as create:
            self.assertIs(control._connect_reverse_websocket("wss://gateway/tunnel"), tunnel)
        create.assert_called_once_with(
            "wss://gateway/tunnel", timeout=20, sslopt={"cert_reqs": 0})
        tunnel.settimeout.assert_called_once_with(None)

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

    def test_successful_sms_records_a_fresh_service_centre(self):
        provider = Mock()
        provider.status.return_value = {
            "sms_readiness_authoritative": True, "sms_ready": True,
            "sms_provider": "windows_mbn",
        }
        provider.sms_send.return_value = {"ok": True, "status": "sent"}
        modem = ModemCard("COM16", 115200, platform_provider=provider)
        modem.iccid = "89852312388530152529"
        modem.service_centre = Mock(return_value="+85362101201")
        with patch("agent.modem_agent.sms_history.record") as record:
            modem.sms_send("+85246094054", "test")
        modem.service_centre.assert_called_once_with(force=True)
        record.assert_called_once_with("89852312388530152529", "+85362101201")

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
        modem.voice_registration_status = Mock(return_value={
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

    def test_voice_registration_uses_call_signalling_provider_channel(self):
        modem = ModemCard("auto", 115200)
        modem._at = Mock(side_effect=AssertionError("primary data AT must not be used"))
        modem.platform_provider = Mock()
        modem.platform_provider.voice_command.return_value = (
            b"\r\n+CREG: 0,5\r\nOK\r\n")

        result = modem.voice_registration_status(force=True, allow_repair=False)

        self.assertTrue(result["ready"])
        self.assertEqual(result["bearer"], "cs")
        self.assertEqual(
            modem.platform_provider.voice_command.call_args_list,
            [unittest.mock.call("AT+CREG?"), unittest.mock.call("AT+CEREG?")],
        )

    def test_uicc_health_uses_signalling_provider_channel(self):
        modem = ModemCard("auto", 115200)
        modem._at = Mock(side_effect=AssertionError("primary data AT must not be used"))
        modem.platform_provider = Mock()
        modem.platform_provider.voice_command.side_effect = [
            b"\r\n+CREG: 0,5\r\nOK\r\n",
            b"\r\n+CEREG: 0,5\r\nOK\r\n",
            RuntimeError("CPIN is owned by MBIM"),
        ]

        result = modem.uicc_health_status(force=True, allow_repair=False)

        self.assertTrue(result["ready"])
        self.assertEqual(
            modem.platform_provider.voice_command.call_args_list,
            [unittest.mock.call("AT+CREG?"), unittest.mock.call("AT+CEREG?"),
             unittest.mock.call("AT+CPIN?")],
        )

    def test_uicc_health_falls_back_to_verified_primary_at_on_transport_loss(self):
        modem = ModemCard("auto", 115200)
        modem._at = Mock(return_value=b"\r\nOK\r\n")
        modem.platform_provider = Mock()
        modem.platform_provider.voice_command.side_effect = RuntimeError(
            "auxiliary AT port is closed")

        modem.uicc_health_status(force=True, allow_repair=False)

        self.assertEqual(modem._at.call_count, 3)

    def test_uicc_health_does_not_hide_explicit_modem_failure_with_fallback(self):
        modem = ModemCard("auto", 115200)
        modem._at = Mock(side_effect=AssertionError("must not mask explicit modem failure"))
        modem.platform_provider = Mock()
        modem.platform_provider.voice_command.side_effect = RuntimeError(
            "auxiliary AT command failed: AT+CPIN?: +CME ERROR: SIM failure")

        result = modem.uicc_health_status(force=True, allow_repair=False)

        self.assertEqual(result["state"], "failed")
        modem._at.assert_not_called()

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
        modem = types.SimpleNamespace(
            connection=object(), imei="123456789012345", private_raw_usb=True)
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

    def test_usb_composition_restart_discards_stale_port_snapshot(self):
        modem = ModemCard("auto", 115200)

        def restarting(_candidate):
            modem._reenumeration_pending = True
            return False

        modem._connect_port = Mock(side_effect=restarting)
        ports = [types.SimpleNamespace(device="COM34", description="USB AT Port"),
                 types.SimpleNamespace(device="COM31", description="USB Modem Port"),
                 types.SimpleNamespace(device="COM1", description="Communications Port")]
        with patch("agent.modem_agent.list_ports.comports", return_value=ports):
            self.assertFalse(modem.connect())
        modem._connect_port.assert_called_once_with("COM34")

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

    def test_windows_bootstrap_restores_cfun_zero_before_mbn_discovery(self):
        modem = ModemCard("COM14", 115200)
        modem._at = Mock(side_effect=[
            b"OK", b"OK", b"OK", b"862547055201716\r\nOK",
            b"+CFUN: 0\r\nOK", b"OK",
        ])
        with tempfile.TemporaryDirectory() as directory:
            modem.uicc_health = UiccHealthMaintainer(
                state_path=f"{directory}/uicc.json")
            with patch("agent.modem_agent.os.name", "nt"), \
                    patch("agent.modem_agent.serial.Serial", return_value=Mock()), \
                    patch("agent.modem_agent.WindowsMbnProvider.discover") as discover:
                self.assertFalse(modem._connect_port("COM14"))

        discover.assert_not_called()
        self.assertTrue(modem._reenumeration_pending)
        self.assertEqual(modem._at.call_args_list[-2:], [
            unittest.mock.call("AT+CFUN?"), unittest.mock.call("AT+CFUN=1")])

    def test_paid_reconnect_can_skip_pre_identity_uicc_maintenance(self):
        modem = ModemCard("COM14", 115200)
        commands = []

        def at(command, *args, **kwargs):
            commands.append(command)
            if command == "AT+CGSN":
                return b"862547055201716\r\nOK\r\n"
            if command == "AT+CCID":
                return b"89852312388530152529\r\nOK\r\n"
            if command == "AT+CIMI":
                return b"454070123456789\r\nOK\r\n"
            return b"OK\r\n"

        modem._at = Mock(side_effect=at)
        modem._pre_identity_uicc_maintenance = Mock(
            side_effect=AssertionError("paid reconnect ran Early UICC maintenance"))

        with patch("agent.modem_agent.serial.Serial", return_value=Mock()):
            modem._connect_port("COM14", allow_uicc_maintenance=False)

        modem._pre_identity_uicc_maintenance.assert_not_called()
        self.assertNotIn("AT+CFUN?", commands)
        self.assertNotIn("AT+CFUN=1", commands)

    def test_private_raw_usb_reuses_known_identity_for_bounded_uicc_recovery(self):
        modem = ModemCard("usb:test", 115200)
        modem.private_raw_usb = True
        modem.imei = "862547055201716"
        modem._at = Mock()
        modem.uicc_health = Mock()
        modem.uicc_health.ensure_full_function.return_value = types.SimpleNamespace(
            action="", state="ready", reason="")
        modem.uicc_health.known_iccid.return_value = "89852312388530152529"
        modem.uicc_health.check.return_value = types.SimpleNamespace(
            action="reinitialize_uicc", state="recovered", reason="", ready=True,
            diagnostics={"pin_after_recovery": "ready"})

        modem._pre_identity_uicc_maintenance()

        modem.uicc_health.set_context.assert_called_once_with(
            "862547055201716", "89852312388530152529")
        modem.uicc_health.check.assert_called_once_with(
            modem._at, force=True, allow_repair=True)
        self.assertFalse(modem._reenumeration_pending)

    def test_audio_reprobe_only_refreshes_audio_and_never_prompts(self):
        modem = ModemCard("usb:test", 115200)
        modem.serial = Mock()
        modem.capabilities.update({"call": True, "cellular_data": True})
        modem._at = Mock(return_value=b"OK")
        refreshed = Mock()
        modem.status_refresh_callback = refreshed
        probe = types.SimpleNamespace(
            ready=True, backend="uac", reason="", activation="qpcmv-uac", details={})
        with patch("agent.modem_agent.probe_call_audio", return_value=probe) as call_probe:
            result = modem.reprobe_call_audio()

        self.assertTrue(result["ready"])
        self.assertTrue(modem.capabilities["cellular_data"])
        call_probe.assert_called_once_with(
            "usb:test", modem._at, helper="", allow_permission_prompt=False)
        refreshed.assert_called_once_with()

    def test_private_raw_usb_unknown_sim_never_authorizes_reset(self):
        modem = ModemCard("usb:test", 115200)
        modem.private_raw_usb = True
        modem.imei = "862547055201716"
        modem._at = Mock()
        modem.uicc_health = Mock()
        modem.uicc_health.ensure_full_function.return_value = types.SimpleNamespace(
            action="", state="ready", reason="")
        modem.uicc_health.known_iccid.return_value = ""

        modem._pre_identity_uicc_maintenance()

        modem.uicc_health.check.assert_not_called()

    def test_cnum_pattern_accepts_international_number_and_not_empty_response(self):
        raw = '+CNUM: "","+85361234567",145\r\nOK\r\n'
        self.assertEqual(ModemCard._cnum_number(raw), "+85361234567")
        self.assertEqual(ModemCard._cnum_number("OK\r\n"), "")
        self.assertEqual(ModemCard._cnum_number('+COPS: 0,2,"46011"\r\nOK'), "")

    def test_windows_cached_iccid_migration_requires_one_unambiguous_subscription(self):
        registry = types.SimpleNamespace(
            HKEY_LOCAL_MACHINE=object(),
            OpenKey=Mock(return_value=object()),
            EnumKey=Mock(side_effect=["89852312388530153089", OSError()]),
            CloseKey=Mock())
        with patch("agent.modem_agent.os.name", "nt"), \
                patch.dict(sys.modules, {"winreg": registry}):
            self.assertEqual(ModemCard._windows_single_cached_iccid(),
                             "89852312388530153089")

    def test_windows_cached_iccid_migration_refuses_ambiguous_history(self):
        registry = types.SimpleNamespace(
            HKEY_LOCAL_MACHINE=object(),
            OpenKey=Mock(return_value=object()),
            EnumKey=Mock(side_effect=[
                "89852312388530153089", "8944110069499811522", OSError()]),
            CloseKey=Mock())
        with patch("agent.modem_agent.os.name", "nt"), \
                patch.dict(sys.modules, {"winreg": registry}):
            self.assertEqual(ModemCard._windows_single_cached_iccid(), "")

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
        modem = types.SimpleNamespace(
            connection=object(), imei="123456789012345", private_raw_usb=True)
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
            b'+CGCONTRDP: 1,5,"ctnet","10.0.0.1","255.255.255.0"\r\nOK\r\n',
        ])
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345",
                                      _at=Mock(side_effect=lambda _command: next(responses)))
        control = ModemControl(args, modem)
        candidates = control._modem_profile_candidates()
        self.assertEqual([item["apn"] for item in candidates], ["ctnet"])
        self.assertEqual(candidates[0]["source"], "network")
        self.assertEqual([call.args[0] for call in modem._at.call_args_list],
                         ["AT+CGDCONT?", "AT+CGCONTRDP"])
        control.stop.set()

    def test_cgcontrdp_error_returns_no_candidates(self):
        """When AT+CGCONTRDP returns ERROR, no candidates should be produced."""
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="wwan0", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        responses = iter([
            b'+CGDCONT: 1,"IP","ims"\r\nOK\r\n',
            b'ERROR\r\n',
        ])
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345",
                                      _at=Mock(side_effect=lambda _command: next(responses)))
        control = ModemControl(args, modem)
        self.assertEqual(control._modem_profile_candidates(), [])
        control.stop.set()

    def test_cgcontrdp_empty_apn_is_skipped(self):
        """Network context with empty APN should not appear in candidates."""
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="wwan0", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        responses = iter([
            b'+CGDCONT: 1,"IP","ims"\r\nOK\r\n',
            b'+CGCONTRDP: 1,5,"","10.0.0.1","255.255.255.0"\r\nOK\r\n',
        ])
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345",
                                      _at=Mock(side_effect=lambda _command: next(responses)))
        control = ModemControl(args, modem)
        self.assertEqual(control._modem_profile_candidates(), [])
        control.stop.set()

    def test_cgcontrdp_multiple_contexts(self):
        """Multiple active contexts should all appear if non-reserved."""
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="wwan0", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        responses = iter([
            b'+CGDCONT: 1,"IP","ims"\r\nOK\r\n',
            b'+CGCONTRDP: 1,5,"ims","10.0.0.1","255.255.255.0"\r\n'
            b'+CGCONTRDP: 2,6,"ctnet","10.0.0.2","255.255.255.0"\r\n'
            b'+CGCONTRDP: 3,7,"cmwap","10.0.0.3","255.255.255.0"\r\nOK\r\n',
        ])
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345",
                                      _at=Mock(side_effect=lambda _command: next(responses)))
        control = ModemControl(args, modem)
        candidates = control._modem_profile_candidates()
        apns = [item["apn"] for item in candidates]
        self.assertIn("ctnet", apns)
        self.assertIn("cmwap", apns)
        self.assertNotIn("ims", apns)  # reserved
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

    def test_apdu_runtime_circuit_withdraws_false_positive_capability(self):
        provider = Mock()
        provider.transmit_apdu.side_effect = RuntimeError("SIM channel is now OS-owned")
        modem = ModemCard("unused", 115200, platform_provider=provider)
        modem.capabilities["sim_apdu"] = True

        self.assertEqual(modem.transmit(bytes.fromhex("00A40000023F00")), b"\x6f\x00")
        with self.assertRaisesRegex(RuntimeError, "runtime circuit opened"):
            modem.transmit(bytes.fromhex("00A40000022F00"))

        self.assertFalse(modem.capabilities["sim_apdu"])
        self.assertEqual(provider.transmit_apdu.call_count, 2)

    def test_sms_inbox_circuit_stops_hammering_failed_provider(self):
        control = self._control()
        control.modem.sms_list = Mock(side_effect=RuntimeError("CMGF unavailable"))

        with self.assertRaisesRegex(RuntimeError, "CMGF unavailable"):
            control.execute("sms.list", {})
        with self.assertRaisesRegex(RuntimeError, "CMGF unavailable"):
            control.execute("sms.list", {})
        third = control.execute("sms.list", {})
        fourth = control.execute("sms.list", {})

        self.assertTrue(third["degraded"])
        self.assertEqual(third["retry_after"], 300)
        self.assertTrue(fourth["degraded"])
        self.assertEqual(control.modem.sms_list.call_count, 3)

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

    def test_private_cellular_backend_bypasses_host_interface_and_guard(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345")
        backend = Mock(link_state="down", isolation_ready=True, isolation_error="")
        control = ModemControl(args, modem, dial_backend=backend)
        control._status = Mock(return_value={"registration": "home"})
        control._cellular_interface = Mock(side_effect=AssertionError("host interface used"))
        control.isolation = Mock()

        result = control._cellular_ensure({"allow_roaming": False})

        self.assertTrue(result["proxy"]["ready"])
        self.assertEqual(result["proxy"]["transport"], "private_dial")
        self.assertFalse(result["isolation"]["host_interface"])
        backend.enable.assert_called_once_with()
        backend.qualify.assert_called_once_with()
        control._cellular_interface.assert_not_called()
        control.isolation.ensure.assert_not_called()
        control.stop.set()

    def test_private_status_publishes_generic_at_sms_and_call_readiness(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        responses = {
            "AT+CEREG?": b"\r\n+CEREG: 0,5\r\nOK\r\n",
            "AT+COPS?": b'\r\n+COPS: 0,0,"CHN-CT"\r\nOK\r\n',
            "AT+CSQ": b"\r\n+CSQ: 19,99\r\nOK\r\n",
            "AT+CFUN?": b"\r\n+CFUN: 1\r\nOK\r\n",
        }
        modem = types.SimpleNamespace(
            connection=object(), imei="123456789012345", operator="",
            platform_provider=None, sim_via_mbn=False,
            capabilities={"sms": True, "call": True, "call_audio": True,
                          "cellular_data": True, "sim_apdu": False},
            call_audio_probe=types.SimpleNamespace(ready=True, backend="uac", reason=""),
            _at=Mock(side_effect=lambda command: responses[command]),
            service_centre=Mock(return_value="+85362101201"),
            smsc_changed=Mock(return_value=False),
            uicc_health_status=Mock(return_value={"ready": True, "state": "ready"}),
            sms_submit_readiness=Mock(return_value={"ready": True, "bearer": "cs"}),
            voice_registration_status=Mock(return_value={
                "ready": True, "state": "registered", "bearer": "cs", "reason": "",
            }),
        )
        backend = Mock(link_state="up", isolation_ready=True, isolation_error="")
        control = ModemControl(args, modem, dial_backend=backend)

        result = control._status()

        self.assertTrue(result["sms_ready"])
        self.assertTrue(result["call_ready"])
        self.assertTrue(result["call_audio_ready"])
        self.assertEqual(result["voice_registration"]["bearer"], "cs")
        self.assertEqual(result["operator"], "CHN-CT")
        with patch.dict(os.environ, {"MDD_AGENT_PACKAGE_DIGEST": "c" * 64}):
            capabilities = control._capabilities_snapshot()
        self.assertTrue(capabilities["call_signalling"])
        self.assertTrue(capabilities["call_audio"])
        self.assertEqual(capabilities["paid_call_lease_version"], 1)
        self.assertEqual(capabilities["call_contract"]["package_digest"], "c" * 64)
        control.stop.set()

    def test_private_data_owner_status_pauses_at_sim_sms_and_voice_probes(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(
            connection=object(), imei="123456789012345", operator="cached",
            platform_provider=None, private_raw_usb=True, sim_via_mbn=False,
            capabilities={"sms": True, "call": True, "call_audio": True,
                          "cellular_data": True, "sim_apdu": True},
            call_audio_probe=types.SimpleNamespace(ready=True, backend="uac", reason=""),
            _at=Mock(side_effect=AssertionError("AT probe touched data-owned modem")),
            service_centre=Mock(side_effect=AssertionError("SMSC probe touched modem")),
            smsc_changed=Mock(side_effect=AssertionError("SMSC diff touched modem")),
            uicc_health_status=Mock(side_effect=AssertionError("UICC probe touched modem")),
            sms_submit_readiness=Mock(side_effect=AssertionError("SMS probe touched modem")),
            voice_registration_status=Mock(
                side_effect=AssertionError("voice probe touched modem")),
        )
        backend = Mock(link_state="up", isolation_ready=True, isolation_error="")
        control = ModemControl(args, modem, dial_backend=backend)
        control.cellular_active.set()

        result = control._status()

        self.assertTrue(result["data_active"])
        self.assertTrue(result["proxy"]["ready"])
        self.assertTrue(result["at_probes_paused"])
        self.assertEqual(result["pause_reason"], "cellular_data_active")
        self.assertFalse(result["sms_ready"])
        self.assertFalse(result["call_ready"])
        self.assertFalse(result["sim_apdu_ready"])
        self.assertTrue(result["uicc_health"]["paused"])
        self.assertTrue(result["voice_registration"]["paused"])
        modem._at.assert_not_called()
        modem.service_centre.assert_not_called()
        modem.smsc_changed.assert_not_called()
        modem.uicc_health_status.assert_not_called()
        modem.sms_submit_readiness.assert_not_called()
        modem.voice_registration_status.assert_not_called()
        control.stop.set()

    def test_private_data_guard_requires_explicit_disable_release(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(
            connection=object(), imei="123456789012345", private_raw_usb=True,
            capabilities={"sms": True, "call": True}, platform_provider=None)
        backend = Mock(link_state="down", isolation_ready=True, isolation_error="")
        control = ModemControl(args, modem, dial_backend=backend)

        self.assertTrue(control._private_data_sim_guard_active())

        control.data_reconciled.set()
        self.assertTrue(control._private_data_sim_guard_active())

        control._private_data_release_proven.set()
        self.assertFalse(control._private_data_sim_guard_active())

        backend.link_state = "starting"
        self.assertTrue(control._private_data_sim_guard_active())

        modem.private_raw_usb = False
        self.assertFalse(control._private_data_sim_guard_active())
        control.stop.set()

    def test_private_data_unproven_release_status_does_not_touch_modem(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(
            connection=object(), imei="123456789012345", operator="cached",
            platform_provider=None, private_raw_usb=True, sim_via_mbn=False,
            capabilities={"sms": True, "call": True, "call_audio": True,
                          "cellular_data": True, "sim_apdu": True},
            call_audio_probe=types.SimpleNamespace(ready=True, backend="uac", reason=""),
            _at=Mock(side_effect=AssertionError("AT probe touched unproven data modem")),
            service_centre=Mock(side_effect=AssertionError("SMSC probe touched modem")),
            smsc_changed=Mock(side_effect=AssertionError("SMSC diff touched modem")),
            uicc_health_status=Mock(side_effect=AssertionError("UICC probe touched modem")),
            sms_submit_readiness=Mock(side_effect=AssertionError("SMS probe touched modem")),
            voice_registration_status=Mock(
                side_effect=AssertionError("voice probe touched modem")),
        )
        backend = Mock(link_state="down", isolation_ready=True, isolation_error="")
        control = ModemControl(args, modem, dial_backend=backend)
        control.data_reconciled.set()
        control.cellular_active.clear()

        result = control._status()

        self.assertTrue(result["at_probes_paused"])
        self.assertEqual(result["pause_reason"], "cellular_data_active")
        self.assertFalse(result["sms_ready"])
        self.assertFalse(result["call_ready"])
        self.assertFalse(result["sim_apdu_ready"])
        modem._at.assert_not_called()
        modem.uicc_health_status.assert_not_called()
        modem.sms_submit_readiness.assert_not_called()
        modem.voice_registration_status.assert_not_called()
        control.stop.set()

    def test_private_data_owner_sms_operations_do_not_touch_modem_or_ack_state(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        provider = Mock()
        modem = types.SimpleNamespace(
            connection=object(), imei="123456789012345", platform_provider=provider,
            private_raw_usb=True, capabilities={"sms": True, "call": True},
            sms_list=Mock(side_effect=AssertionError("sms_list touched modem")),
            sms_send=Mock(side_effect=AssertionError("sms_send touched modem")),
            _at=Mock(side_effect=AssertionError("AT touched modem")),
        )
        backend = Mock(link_state="up", isolation_ready=True, isolation_error="")
        control = ModemControl(args, modem, dial_backend=backend)
        control.cellular_active.set()

        listed = control.execute("sms.list", {})
        acked = control.execute("sms.ack", {"id": "7", "fingerprint": "abc"})
        sent = control.execute("sms.send", {"to": "+85360000000", "body": "test"})
        refreshed = control.execute("sms.config.refresh", {})

        self.assertTrue(listed["ok"])
        self.assertEqual(listed["messages"], [])
        self.assertTrue(listed["degraded"])
        self.assertFalse(listed["authoritative"])
        for result in (acked, sent, refreshed):
            self.assertFalse(result["ok"])
            self.assertTrue(result["unavailable"])
            self.assertTrue(result["paused"])
            self.assertEqual(result["status"], "cellular_data_active")
        self.assertEqual(control.acked_sms, set())
        self.assertEqual(control._sms_list_failures, 0)
        self.assertEqual(control._sms_list_blocked_until, 0.0)
        modem.sms_list.assert_not_called()
        modem.sms_send.assert_not_called()
        modem._at.assert_not_called()
        provider.sms_delete.assert_not_called()
        control.stop.set()

    def test_private_data_unproven_release_sms_list_is_paused(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(
            connection=object(), imei="123456789012345", platform_provider=None,
            private_raw_usb=True, capabilities={"sms": True, "call": True},
            sms_list=Mock(side_effect=AssertionError("sms_list touched unproven data modem")),
        )
        backend = Mock(link_state="down", isolation_ready=True, isolation_error="")
        control = ModemControl(args, modem, dial_backend=backend)
        control.data_reconciled.set()
        control.cellular_active.clear()

        result = control.execute("sms.list", {})

        self.assertTrue(result["ok"])
        self.assertTrue(result["paused"])
        self.assertFalse(result["authoritative"])
        modem.sms_list.assert_not_called()
        control.stop.set()

    def test_private_data_owner_blocks_paid_call_starters_and_unleased_status(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        provider = Mock()
        modem = types.SimpleNamespace(
            connection=object(), imei="123456789012345", platform_provider=provider,
            private_raw_usb=True,
            capabilities={"sms": True, "call": True},
            uicc_health_status=Mock(side_effect=AssertionError("UICC touched modem")),
            voice_registration_status=Mock(side_effect=AssertionError("voice touched modem")),
            _at=Mock(side_effect=AssertionError("AT touched modem")),
        )
        backend = Mock(link_state="up", isolation_ready=True, isolation_error="")
        control = ModemControl(args, modem, dial_backend=backend)
        control.cellular_active.set()
        control._fresh_call_status = Mock(return_value={
            "ok": True, "fresh": True, "authoritative": True, "status": "idle"})
        control._verified_call_hangup = Mock(return_value={
            "ok": True, "status": "idle", "terminal_confirmed": True})

        dial = control.execute("call.dial", {"to": "22333322", "lease_id": "call-12345678"})
        answer = control.execute("call.answer", {"lease_id": "call-87654321"})
        dtmf = control.execute("call.dtmf", {"digits": "123#"})

        for result in (dial, answer, dtmf):
            self.assertFalse(result["ok"])
            self.assertTrue(result["unavailable"])
            self.assertTrue(result["paused"])
        self.assertEqual(control._paid_call_lease_id, "")
        control._fresh_call_status.assert_not_called()
        modem.uicc_health_status.assert_not_called()
        modem.voice_registration_status.assert_not_called()
        modem._at.assert_not_called()
        provider.call_dial.assert_not_called()
        provider.call_answer.assert_not_called()
        provider.call_dtmf.assert_not_called()

        status = control.execute("call.status", {})
        self.assertFalse(status["ok"])
        self.assertTrue(status["paused"])
        control._fresh_call_status.assert_not_called()
        control.stop.set()

    def test_private_data_owner_keeps_paid_call_status_and_hangup_terminators(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(
            connection=object(), imei="123456789012345", platform_provider=None,
            private_raw_usb=True,
            capabilities={"sms": True, "call": True},
            _at=Mock(side_effect=AssertionError("unexpected direct AT")),
        )
        backend = Mock(link_state="up", isolation_ready=True, isolation_error="")
        control = ModemControl(args, modem, dial_backend=backend)
        control.cellular_active.set()
        with control._paid_call_lock:
            control._paid_call_lease_id = "call-12345678"
            control._paid_call_termination_requested = True
            control.paid_call_active.set()
        control._fresh_call_status = Mock(return_value={
            "ok": True, "fresh": True, "authoritative": True, "status": "idle"})
        control._verified_call_hangup = Mock(return_value={
            "ok": True, "status": "idle", "terminal_confirmed": True})

        status = control.execute("call.status", {})
        hangup = control.execute("call.hangup", {})

        self.assertTrue(status["ok"])
        self.assertTrue(hangup["ok"])
        control._fresh_call_status.assert_called_once()
        control._verified_call_hangup.assert_called_once()
        control.stop.set()

    def _private_run_args(self):
        return types.SimpleNamespace(
            no_pcsc=True, host="127.0.0.1", gateway_port=8443,
            token="", path="/api/vpcd/ws", pin="", reset_pin=False, retry=0.05,
            name="", port="auto", baud=115200, gammu="", gammu_port="",
            call_audio_helper="", allow_audio_permission_prompt=False)

    class _FakeRunControl:
        def __init__(self):
            self.cellular_active = threading.Event()
            self.paid_call_active = threading.Event()
            self.paid_call_cleared = threading.Event()
            self.stop = threading.Event()
            self.data_reconciled = threading.Event()
            self.data_reconciled.set()
            self._private_data_release_proven = threading.Event()
            self.bootstrap_enabled = False
            self.bootstrap_error = None
            self.bootstrap_disable_calls = 0
            self._armed = threading.Event()

        def run(self):
            self.stop.wait()

        def _paid_call_armed(self):
            return self._armed.is_set()

        def _paid_call_safety_hold(self):
            return self._armed.is_set()

        def _private_data_sim_guard_active(self, *, allow_bootstrap_connect=False):
            if allow_bootstrap_connect and self._private_data_release_proven.is_set():
                return False
            if not self.data_reconciled.is_set():
                return True
            if self.cellular_active.is_set():
                return True
            return not self._private_data_release_proven.is_set()

        def _bootstrap_private_data_release(self):
            if self._paid_call_armed() or self._paid_call_safety_hold():
                return
            if self.bootstrap_error:
                raise self.bootstrap_error
            if self.bootstrap_enabled:
                self.bootstrap_disable_calls += 1
                self.cellular_active.clear()
                self._private_data_release_proven.set()

        def begin_shutdown(self):
            return None

        def shutdown_paid_call(self):
            return {"terminal_confirmed": True}

    class _FakeRunModem:
        def __init__(self):
            self.connection = None
            self.private_raw_usb = True
            self.sim_via_mbn = False
            self.capabilities = {"sim_apdu": True}
            self.imei = "862547055201716"
            self.iccid = "89852312388530152529"
            self.connect = Mock()
            self.close = Mock(side_effect=self._close)
            self.reset = Mock()
            self.transmit = Mock(return_value=b"\x90\x00")

        def _close(self):
            self.connection = None

    def test_run_pauses_private_data_before_reconnect_until_release(self):
        args = self._private_run_args()
        stopped = threading.Event()
        control = self._FakeRunControl()
        control.cellular_active.set()
        modem = self._FakeRunModem()

        def connect():
            modem.connection = object()
            stopped.set()
            return True
        modem.connect.side_effect = connect

        thread = threading.Thread(
            target=run_agent,
            args=(args, stopped, None),
            kwargs={"_allow_private_supervisor": False,
                    "modem_override": modem, "control_override": control},
        )
        thread.start()
        time.sleep(0.2)
        self.assertFalse(modem.connect.called)

        control.cellular_active.clear()
        control._private_data_release_proven.set()
        thread.join(2)

        self.assertFalse(thread.is_alive())
        modem.connect.assert_called_once()

    def test_run_bootstrap_disable_proof_allows_connect_before_desired_reconcile(self):
        args = self._private_run_args()
        stopped = threading.Event()
        control = self._FakeRunControl()
        control.data_reconciled.clear()
        control.bootstrap_enabled = True
        modem = self._FakeRunModem()

        def connect():
            modem.connection = object()
            stopped.set()
            return True
        modem.connect.side_effect = connect

        thread = threading.Thread(
            target=run_agent,
            args=(args, stopped, None),
            kwargs={"_allow_private_supervisor": False,
                    "modem_override": modem, "control_override": control},
        )
        thread.start()
        thread.join(2)

        self.assertFalse(thread.is_alive())
        modem.connect.assert_called_once()

    def test_run_bootstrap_disable_failure_fails_closed_before_connect(self):
        args = self._private_run_args()
        stopped = threading.Event()
        control = self._FakeRunControl()
        control.bootstrap_error = RuntimeError("disable failed")
        modem = self._FakeRunModem()

        with self.assertRaises(RuntimeError):
            run_agent(args, stopped, None, _allow_private_supervisor=False,
                      modem_override=modem, control_override=control)

        modem.connect.assert_not_called()

    def test_run_paid_call_armed_skips_bootstrap_disable_and_vpcd(self):
        args = self._private_run_args()
        stopped = threading.Event()
        control = self._FakeRunControl()
        control.bootstrap_enabled = True
        control._armed.set()
        modem = self._FakeRunModem()
        connect_kwargs = []

        def connect(**kwargs):
            connect_kwargs.append(kwargs)
            modem.connection = object()
            stopped.set()
            return True
        modem.connect.side_effect = connect

        with patch("agent.modem_agent.connect_wss",
                   side_effect=AssertionError("VPCD must stay paused during paid cleanup")) as connect_wss:
            thread = threading.Thread(
                target=run_agent,
                args=(args, stopped, None),
                kwargs={"_allow_private_supervisor": False,
                        "modem_override": modem, "control_override": control},
            )
            thread.start()
            thread.join(2)

        self.assertFalse(thread.is_alive())
        self.assertEqual(control.bootstrap_disable_calls, 0)
        modem.connect.assert_called_once()
        self.assertEqual(connect_kwargs, [{"allow_uicc_maintenance": False}])
        connect_wss.assert_not_called()

    def test_run_keeps_reconnect_paused_after_unconfirmed_private_data_failure(self):
        args = self._private_run_args()
        stopped = threading.Event()
        control = self._FakeRunControl()
        control.cellular_active.clear()
        control._private_data_release_proven.clear()
        modem = self._FakeRunModem()

        thread = threading.Thread(
            target=run_agent,
            args=(args, stopped, None),
            kwargs={"_allow_private_supervisor": False,
                    "modem_override": modem, "control_override": control},
        )
        thread.start()
        time.sleep(0.2)
        self.assertFalse(modem.connect.called)

        stopped.set()
        thread.join(2)

        self.assertFalse(thread.is_alive())
        modem.connect.assert_not_called()

    def test_run_waits_after_vpcd_pause_and_resumes_only_after_data_release(self):
        args = self._private_run_args()
        stopped = threading.Event()
        control = self._FakeRunControl()
        control.cellular_active.set()
        modem = self._FakeRunModem()
        modem.connection = object()

        def connect_gateway(*_args, **_kwargs):
            stopped.set()
            raise RuntimeError("stop after VPCD resume")

        with patch("agent.modem_agent.connect_wss", side_effect=connect_gateway) as connect_wss:
            thread = threading.Thread(
                target=run_agent,
                args=(args, stopped, None),
                kwargs={"_allow_private_supervisor": False,
                        "modem_override": modem, "control_override": control},
            )
            thread.start()
            time.sleep(0.2)
            connect_wss.assert_not_called()

            control.cellular_active.clear()
            control._private_data_release_proven.set()
            thread.join(2)

        self.assertFalse(thread.is_alive())
        connect_wss.assert_called_once()

    def test_run_bootstrap_connect_does_not_allow_vpcd_before_desired_reconcile(self):
        args = self._private_run_args()
        stopped = threading.Event()
        control = self._FakeRunControl()
        control.data_reconciled.clear()
        control._private_data_release_proven.set()
        modem = self._FakeRunModem()
        modem.connection = object()

        with patch("agent.modem_agent.connect_wss",
                   side_effect=AssertionError("VPCD started before desired reconcile")) as connect_wss:
            thread = threading.Thread(
                target=run_agent,
                args=(args, stopped, None),
                kwargs={"_allow_private_supervisor": False,
                        "modem_override": modem, "control_override": control},
            )
            thread.start()
            time.sleep(0.2)
            stopped.set()
            thread.join(2)

        self.assertFalse(thread.is_alive())
        connect_wss.assert_not_called()

    def test_run_keeps_vpcd_paused_after_unconfirmed_private_data_failure(self):
        args = self._private_run_args()
        stopped = threading.Event()
        control = self._FakeRunControl()
        control.cellular_active.clear()
        control._private_data_release_proven.clear()
        modem = self._FakeRunModem()
        modem.connection = object()

        with patch("agent.modem_agent.connect_wss",
                   side_effect=AssertionError("VPCD touched unproven data modem")) as connect_wss:
            thread = threading.Thread(
                target=run_agent,
                args=(args, stopped, None),
                kwargs={"_allow_private_supervisor": False,
                        "modem_override": modem, "control_override": control},
            )
            thread.start()
            time.sleep(0.2)
            stopped.set()
            thread.join(2)

        self.assertFalse(thread.is_alive())
        connect_wss.assert_not_called()

    def test_data_owner_wait_is_interrupted_by_paid_call_armed(self):
        stopped = threading.Event()
        control = self._FakeRunControl()
        control.cellular_active.set()
        modem = self._FakeRunModem()
        result = []

        thread = threading.Thread(
            target=lambda: result.append(
                _wait_data_owner_release(modem, control, stopped, context="test")))
        thread.start()
        time.sleep(0.2)
        self.assertTrue(thread.is_alive())

        control._armed.set()
        thread.join(2)

        self.assertFalse(thread.is_alive())
        self.assertEqual(result, [True])
        self.assertTrue(control.cellular_active.is_set())

    def test_run_paid_call_armed_skips_vpcd_while_data_is_active(self):
        args = self._private_run_args()
        stopped = threading.Event()
        control = self._FakeRunControl()
        control.cellular_active.set()
        control._armed.set()
        modem = self._FakeRunModem()
        modem.connection = object()

        with patch("agent.modem_agent.connect_wss",
                   side_effect=AssertionError("VPCD must stay paused")) as connect_wss:
            thread = threading.Thread(
                target=run_agent,
                args=(args, stopped, None),
                kwargs={"_allow_private_supervisor": False,
                        "modem_override": modem, "control_override": control},
            )
            thread.start()
            time.sleep(0.2)
            stopped.set()
            thread.join(2)

        self.assertFalse(thread.is_alive())
        connect_wss.assert_not_called()

    def test_private_cellular_roaming_policy_disables_before_returning(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(
            connection=object(), imei="123456789012345", private_raw_usb=True)
        backend = Mock(link_state="down")
        backend.isolation_ready = True
        backend.isolation_error = ""
        control = ModemControl(args, modem, dial_backend=backend)
        control.data_reconciled.set()
        control._private_data_release_proven.set()
        control._status = Mock(return_value={"registration": "roaming"})

        result = control._cellular_ensure({"allow_roaming": False})

        self.assertFalse(result["proxy"]["ready"])
        control._status.assert_called_once_with(allow_private_probe=True)
        backend.disable.assert_called_once_with()
        backend.enable.assert_not_called()
        self.assertFalse(control._private_data_owner_probe_authorized)
        self.assertTrue(control._private_data_release_proven.is_set())
        control.stop.set()

    def test_private_bootstrap_release_skips_backend_disable_during_paid_call(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(
            connection=object(), imei="123456789012345", private_raw_usb=True,
            capabilities={"sms": True, "call": True}, platform_provider=None)
        backend = Mock(link_state="down", isolation_ready=True, isolation_error="")
        control = ModemControl(args, modem, dial_backend=backend)
        with control._paid_call_lock:
            control._paid_call_lease_id = "call-12345678"
            control.paid_call_active.set()

        control._bootstrap_private_data_release()

        backend.disable.assert_not_called()
        self.assertFalse(control._private_data_release_proven.is_set())
        control.stop.set()

    def test_private_cellular_ensure_link_up_does_not_probe_status(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(
            connection=object(), imei="123456789012345", private_raw_usb=True)
        backend = Mock(link_state="up", isolation_ready=True, isolation_error="")
        backend.disconnected = __import__("threading").Event()
        control = ModemControl(args, modem, dial_backend=backend)
        control.data_reconciled.set()
        control._private_data_release_proven.set()
        control._status = Mock(side_effect=AssertionError("link-up ensure probed raw AT"))

        result = control._cellular_ensure({})

        self.assertTrue(result["ok"])
        control._status.assert_not_called()
        backend.enable.assert_called_once_with()
        self.assertFalse(control._private_data_owner_probe_authorized)
        control.stop.set()

    def test_private_cellular_ensure_probe_authorization_is_cleared_on_error(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(
            connection=object(), imei="123456789012345", private_raw_usb=True)
        backend = Mock(link_state="down", isolation_ready=True, isolation_error="")
        backend.disconnected = __import__("threading").Event()
        control = ModemControl(args, modem, dial_backend=backend)
        control.data_reconciled.set()
        control._private_data_release_proven.set()
        control._status = Mock(side_effect=RuntimeError("probe failed"))

        with self.assertRaises(RuntimeError):
            control._cellular_ensure({})

        self.assertFalse(control._private_data_owner_probe_authorized)
        backend.enable.assert_not_called()
        control.stop.set()

    def test_private_cellular_disable_failure_keeps_data_ownership(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345")
        backend = Mock(link_state="up")
        backend.disable.side_effect = RuntimeError("hangup failed")
        control = ModemControl(args, modem, dial_backend=backend)
        control.cellular_active.set()

        result = control._cellular_disable()

        self.assertFalse(result["ok"])
        self.assertTrue(result["data_active"])
        self.assertTrue(control.cellular_active.is_set())
        self.assertFalse(control._private_data_release_proven.is_set())
        control.stop.set()

    def test_private_cellular_disable_success_releases_data_ownership(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345")
        backend = Mock(link_state="up")
        control = ModemControl(args, modem, dial_backend=backend)
        control.cellular_active.set()

        result = control._cellular_disable()

        self.assertTrue(result["ok"])
        self.assertFalse(control.cellular_active.is_set())
        self.assertTrue(control._private_data_release_proven.is_set())
        control.stop.set()

    def test_private_enable_timeout_keeps_sim_owned_when_cleanup_is_unconfirmed(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345")
        backend = Mock(link_state="connecting", isolation_ready=True, isolation_error="")
        backend.enable.side_effect = TimeoutError("PPP enable timed out")
        backend.disable.side_effect = TimeoutError("PPP disable timed out")
        backend.disconnected = __import__("threading").Event()
        control = ModemControl(args, modem, dial_backend=backend)
        control._status = Mock(return_value={"registration": "home"})

        result = control._cellular_ensure({})

        self.assertFalse(result["ok"])
        self.assertTrue(result["data_active"])
        self.assertTrue(control.cellular_active.is_set())
        backend.disable.assert_called_once_with()
        control.stop.set()

    def test_private_enable_timeout_does_not_trust_stale_down_link_state(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        modem = types.SimpleNamespace(connection=object(), imei="123456789012345")
        backend = Mock(link_state="down", isolation_ready=True, isolation_error="")
        backend.enable.side_effect = TimeoutError("PPP enable timed out")
        backend.disable.side_effect = TimeoutError("PPP disable timed out")
        backend.disconnected = __import__("threading").Event()
        control = ModemControl(args, modem, dial_backend=backend)
        control._status = Mock(return_value={"registration": "home"})

        result = control._cellular_ensure({})

        self.assertFalse(result["ok"])
        self.assertTrue(result["data_active"])
        self.assertTrue(control.cellular_active.is_set())
        backend.disable.assert_called_once_with()
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

    def test_platform_status_never_enables_uicc_repair(self):
        args = types.SimpleNamespace(
            isolation_helper="", cellular_interface="Cellular 2", advertise_host="",
            socks_port=0, host="127.0.0.1", gateway_port=8443)
        provider = Mock()
        provider.status.return_value = {
            "registration": "unknown", "sms_ready": False,
            "sms_readiness_authoritative": True,
        }
        modem = types.SimpleNamespace(
            connection=object(), imei="123456789012345", platform_provider=provider,
            capabilities={"call": False, "sms": False, "call_audio": False,
                          "sim_apdu": False},
            call_audio_probe=types.SimpleNamespace(ready=False, backend="", reason=""),
            uicc_health_status=Mock(return_value={"ready": None, "state": "unknown"}),
            smsc_changed=Mock(return_value=False), operator="", sim_via_mbn=False,
        )
        control = ModemControl(args, modem)
        control._cellular_interface = Mock(return_value="Cellular 2")
        control._cellular_ip = Mock(return_value="")
        control._windows_restart_target = Mock(return_value={"available": False})

        control._status()

        modem.uicc_health_status.assert_called_once_with(allow_repair=False)
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
        self.assertEqual(modem._at.call_args_list, [
            unittest.mock.call("AT+CFUN=4"), unittest.mock.call("AT+CFUN?")])
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
