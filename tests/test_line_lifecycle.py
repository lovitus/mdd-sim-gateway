import asyncio
import signal
import sys
import tempfile
import threading
import unittest
from contextlib import contextmanager
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import AsyncMock, patch

from control import run as control_run
from control.app import config, control_lifecycle, engine, main
from control.app.media_admission import MediaAdmissionRegistry


HEALTH_STARTED_AT = "2026-08-26T06:56:00.000000000Z"


@contextmanager
def known_health_rebuild_inputs(iid):
    """Supply actual restart eligibility, not a mock of the production eligibility guard."""
    card = {"present": True, "matched": iid, "remote": True,
            "connection_online": True, "identity_current": True,
            "session_generation": "known-session", "identity_session_generation": "known-session"}
    with tempfile.TemporaryDirectory() as temp, \
            patch.object(main.engine, "DATA_DIR", temp), \
            patch.object(main.hub, "cards_list", return_value=[card]), \
            patch.object(main, "_device_for_card", return_value=("reader-" + iid, "reader")), \
            patch.object(main.device_state, "desired", return_value={
                "defaults": {"vowifi_enabled": True}}):
        yield


class DeletedCardSuppressionTests(unittest.TestCase):
    def test_deleted_inserted_card_stays_suppressed_until_physical_removal(self):
        with tempfile.TemporaryDirectory() as temp:
            config_path = str(Path(temp) / "config.yaml")
            with patch.multiple(config, DATA_DIR=temp, CONFIG_PATH=config_path):
                config.suppress_card_until_removal("test-iccid")
                self.assertTrue(config.card_auto_create_suppressed("test-iccid"))
                self.assertNotIn("test-iccid", Path(config_path).read_text())
                config.unsuppress_card("test-iccid")
                self.assertFalse(config.card_auto_create_suppressed("test-iccid"))

    def test_engine_instance_data_delete_is_scoped_to_one_line(self):
        with tempfile.TemporaryDirectory() as temp:
            target = Path(temp) / "instances" / "line-1" / "run"
            other = Path(temp) / "instances" / "line-2" / "run"
            target.mkdir(parents=True)
            other.mkdir(parents=True)
            (target / "instance.json").write_text("secret")
            (other / "keep").write_text("keep")
            with patch.object(engine, "DATA_DIR", temp):
                self.assertTrue(engine.delete_instance_data("line-1"))
            self.assertFalse(target.parent.exists())
            self.assertTrue((other / "keep").exists())


class PcscfRebindLifecycleTests(unittest.IsolatedAsyncioTestCase):
    def tearDown(self):
        main.hub.pcscf_rebinding.discard("9")
        main.hub.pcscf_rebind_result.pop("9", None)
        main.hub.engine_recovering.discard("9")

    async def test_control_restart_recovers_marker_with_exact_three_part_owner(self):
        marker = {"engine_run_id": "run-9", "phase": "pending"}
        runtime = {"running": True, "container_id": "container-9",
                   "started_at": "2026-08-22T12:00:00Z", "engine_run_id": "run-9"}
        main.hub.pcscf_rebinding.discard("9")
        with patch.object(main.engine, "pcscf_rebind_pending", return_value=True), \
                patch.object(main.engine, "read_pcscf_rebind", return_value=marker), \
                patch.object(main.cfg, "get_instance", return_value={
                    "id": "9", "enabled": True}), \
                patch.object(main.hub.runtime, "get",
                             new=AsyncMock(return_value=runtime)), \
                patch.object(main.engine, "request_pcscf_rebind",
                             return_value={"status": "submitted"}) as submit:
            result = await main._reconcile_pcscf_rebind("9")

        self.assertEqual(result["status"], "submitted")
        self.assertIn("9", main.hub.pcscf_rebinding)
        submit.assert_called_once_with(
            "9", "container-9", "2026-08-22T12:00:00Z", "run-9")

    async def test_previous_run_marker_fences_but_cannot_mutate_new_engine(self):
        marker = {"engine_run_id": "old-run", "phase": "submitted"}
        runtime = {"running": True, "container_id": "same-container",
                   "started_at": "2026-08-22T12:01:00Z", "engine_run_id": "new-run"}
        with patch.object(main.engine, "pcscf_rebind_pending", return_value=True), \
                patch.object(main.engine, "read_pcscf_rebind", return_value=marker), \
                patch.object(main.cfg, "get_instance", return_value={
                    "id": "9", "enabled": True}), \
                patch.object(main.hub.runtime, "get",
                             new=AsyncMock(return_value=runtime)), \
                patch.object(main.engine, "request_pcscf_rebind") as submit:
            result = await main._reconcile_pcscf_rebind("9")
        self.assertEqual(result["status"], "awaiting_new_generation")
        submit.assert_not_called()
        self.assertIn("9", main.hub.pcscf_rebinding)

    async def test_new_call_and_vowifi_sms_are_blocked_before_paid_submission(self):
        ami = AsyncMock()
        with patch.object(main, "_pcscf_rebind_pending",
                          new=AsyncMock(return_value=True)), \
                patch.object(main.hub, "ami_for", new=AsyncMock(return_value=ami)), \
                patch.object(main.store, "add_message") as add_message, \
                patch.object(main.store, "add_call") as add_call:
            sms = await main._send_sms_vowifi("9", "+44123", "hello")
            call = await main.place_call_on_line("9", "+44123")
        self.assertTrue(sms["unavailable"])
        self.assertTrue(call["unavailable"])
        ami.send_sms.assert_not_awaited()
        ami.originate.assert_not_awaited()
        add_message.assert_not_called()
        add_call.assert_not_called()

    async def test_marker_winning_after_optimistic_check_still_blocks_actual_submit(self):
        @main.asynccontextmanager
        async def denied(_iid):
            yield False

        ami = AsyncMock()
        with patch.object(main, "_line_admission_blocked",
                          new=AsyncMock(return_value=False)), \
                patch.object(main, "_pcscf_admission_boundary", new=denied), \
                patch.object(main.hub, "ami_for", new=AsyncMock(return_value=ami)), \
                patch.object(main.store, "add_message") as add_message:
            result = await main._send_sms_vowifi("9", "+44123", "hello")
        self.assertTrue(result["unavailable"])
        ami.send_sms.assert_not_awaited()
        add_message.assert_not_called()

        class _Browser:
            closed = None
            used = False

            async def receive(self):
                if self.used:
                    raise RuntimeError("done")
                self.used = True
                return {"type": "websocket.receive",
                        "text": "INVITE sip:alice@example SIP/2.0\r\n"}

            async def close(self, **kwargs):
                self.closed = kwargs

        browser, upstream = _Browser(), AsyncMock()
        with patch.object(main, "_line_admission_blocked",
                          new=AsyncMock(return_value=False)), \
                patch.object(main, "_pcscf_admission_boundary", new=denied), \
                patch.object(main, "_sip_initial_invite_admission", return_value=None):
            await main._forward_softphone_client(browser, upstream, "9")
        self.assertEqual(browser.closed["code"], 4412)
        upstream.send.assert_not_awaited()

        with patch.object(main, "_line_admission_blocked",
                          new=AsyncMock(return_value=False)), \
                patch.object(main.engine, "exec_cli_with_pcscf_admission",
                             return_value={"admitted": False, "output": ""}) as register:
            with self.assertRaises(main.HTTPException) as rejected:
                await main.api_instance_register("9")
        self.assertEqual(rejected.exception.status_code, 409)
        register.assert_called_once_with("9", "pjsip send register volte_ims")

    async def test_double_cancel_cannot_release_register_worker_admission_early(self):
        with tempfile.TemporaryDirectory() as temp, \
                patch.object(engine, "DATA_DIR", temp), \
                patch.object(main, "_line_admission_blocked",
                             new=AsyncMock(return_value=False)):
            worker_entered = threading.Event()
            worker_release = threading.Event()
            worker_returned = threading.Event()
            marker_published = threading.Event()
            run = Path(temp) / "instances" / "9" / "run"

            def blocked_cli(_iid, _command):
                worker_entered.set()
                worker_release.wait(timeout=1.0)
                worker_returned.set()
                return "submitted"

            def publish_marker():
                with engine._pcscf_rebind_locked("9"):
                    (run / "pcscf-rebind.json").write_text("pending")
                marker_published.set()

            with patch.object(engine, "exec_cli", side_effect=blocked_cli):
                request = asyncio.create_task(main.api_instance_register("9"))
                self.assertTrue(await asyncio.to_thread(worker_entered.wait, 0.3))
                publisher = threading.Thread(target=publish_marker)
                publisher.start()
                await asyncio.sleep(0.01)
                request.cancel()
                await asyncio.sleep(0)
                request.cancel()
                with self.assertRaises(asyncio.CancelledError):
                    await request
                self.assertFalse(worker_returned.is_set())
                self.assertFalse(marker_published.is_set())
                worker_release.set()
                self.assertTrue(await asyncio.to_thread(worker_returned.wait, 0.3))
                publisher.join(timeout=0.3)
                self.assertTrue(marker_published.is_set())

    async def test_durable_fence_blocks_initial_invite_but_allows_bye(self):
        class _Browser:
            def __init__(self, frame):
                self.frame = frame
                self.used = False
                self.closed = None

            async def receive(self):
                if self.used:
                    raise RuntimeError("done")
                self.used = True
                return {"type": "websocket.receive", "text": self.frame}

            async def close(self, **kwargs):
                self.closed = kwargs

        upstream = AsyncMock()
        with patch.object(main, "_pcscf_rebind_pending",
                          new=AsyncMock(return_value=True)):
            invite = _Browser("INVITE sip:123@example SIP/2.0\r\n")
            await main._forward_softphone_client(invite, upstream, "9")
            bye = _Browser("BYE sip:123@example SIP/2.0\r\n")
            await main._forward_softphone_client(bye, upstream, "9")
        self.assertEqual(invite.closed["code"], 4412)
        self.assertEqual(upstream.send.await_count, 1)

    async def test_durable_fence_blocks_browser_sip_message_submission(self):
        class _Browser:
            used = False
            closed = None

            async def receive(self):
                if self.used:
                    raise RuntimeError("done")
                self.used = True
                return {"type": "websocket.receive", "text":
                        "MESSAGE sip:+44123@example SIP/2.0\r\n"
                        "To: <sip:+44123@example>\r\n\r\nhello"}

            async def close(self, **kwargs):
                self.closed = kwargs

        browser, upstream = _Browser(), AsyncMock()
        with patch.object(main, "_pcscf_rebind_pending",
                          new=AsyncMock(return_value=True)):
            await main._forward_softphone_client(browser, upstream, "9")
        self.assertEqual(browser.closed["code"], 4412)
        upstream.send.assert_not_awaited()

    async def test_contended_admission_fails_closed_without_orphaned_waiter(self):
        with tempfile.TemporaryDirectory() as temp, \
                patch.object(engine, "DATA_DIR", temp):
            with engine._pcscf_rebind_locked("9"):
                async def attempt():
                    async with main._pcscf_admission_boundary("9") as admitted:
                        return admitted

                task = asyncio.create_task(attempt())
                self.assertFalse(await asyncio.wait_for(task, timeout=0.2))

    async def test_hung_upstream_submit_releases_flock_for_marker_publisher(self):
        class _Browser:
            used = False
            closed = None

            async def receive(self):
                if self.used:
                    raise RuntimeError("done")
                self.used = True
                return {"type": "websocket.receive", "text":
                        "INVITE sip:+44123@example SIP/2.0\r\n"
                        "To: <sip:+44123@example>\r\n\r\n"}

            async def close(self, **kwargs):
                self.closed = kwargs

        class _HungUpstream:
            def __init__(self):
                self.entered = asyncio.Event()
                self.never = asyncio.Event()
                self.aborted = asyncio.Event()
                self.closed = False
                owner = self

                class _Transport:
                    def abort(self):
                        owner.aborted.set()

                self.transport = _Transport()

            async def send(self, _message):
                self.entered.set()
                try:
                    await self.never.wait()
                except asyncio.CancelledError:
                    # Model a send implementation that does not finish until its underlying
                    # transport is aborted; cancellation alone is not the hard deadline.
                    await self.aborted.wait()

            async def close(self):
                self.closed = True

        with tempfile.TemporaryDirectory() as temp, \
                patch.object(engine, "DATA_DIR", temp), \
                patch.object(main, "SOFTPHONE_UPSTREAM_SUBMIT_TIMEOUT_SECONDS", 0.05), \
                patch.object(main, "_line_admission_blocked",
                             new=AsyncMock(return_value=False)), \
                patch.object(main, "_sip_initial_invite_admission", return_value=None):
            run = Path(temp) / "instances" / "9" / "run"
            browser, upstream = _Browser(), _HungUpstream()
            forwarding = asyncio.create_task(
                main._forward_softphone_client(browser, upstream, "9"))
            await asyncio.wait_for(upstream.entered.wait(), timeout=0.2)
            published = threading.Event()

            def publish_marker():
                with engine._pcscf_rebind_locked("9"):
                    (run / "pcscf-rebind.json").write_text("pending")
                published.set()

            publisher = threading.Thread(target=publish_marker)
            publisher.start()
            await asyncio.sleep(0.01)
            self.assertFalse(published.is_set())
            await asyncio.wait_for(forwarding, timeout=0.3)
            publisher.join(timeout=0.3)
            self.assertTrue(published.is_set())
            self.assertEqual(browser.closed["code"], 4415)
            self.assertTrue(upstream.aborted.is_set())
            self.assertTrue(upstream.closed)

    def test_rebind_status_is_observation_overlay_not_health_recovery_input(self):
        main.hub.pcscf_rebinding.add("9")
        healthy = {"state": "OK", "label": "OK", "reason_code": "registered",
                   "reason": "", "detail": {"registration": "Registered"}}
        observed = main._with_pcscf_rebind_observation("9", healthy)
        self.assertEqual(observed["state"], "REGISTERING")
        self.assertEqual(observed["reason_code"], "pcscf_rebind")
        self.assertTrue(observed["detail"]["pcscf_rebind_pending"])

        main.hub.pcscf_rebind_result["9"] = {
            "status": "submit_retry_exhausted", "rejections": 3,
            "manual_required": True}
        manual = main._with_pcscf_rebind_observation("9", healthy)
        self.assertEqual(manual["state"], "ERROR")
        self.assertEqual(manual["reason_code"], "pcscf_rebind_manual")
        self.assertEqual(manual["detail"]["pcscf_rebind_rejections"], 3)

    async def test_engine_rebind_event_requires_run_id(self):
        missing = await main.api_engine_event({
            "instance": "9", "event": "pcscf_rebind", "args": ["fd00::2"]})
        self.assertFalse(missing["accepted"])
        with patch.object(main, "_reconcile_pcscf_rebind",
                          new=AsyncMock(return_value={"status": "stale_event"})), \
                patch.object(main.hub, "broadcast", new=AsyncMock()):
            stale = await main.api_engine_event({
                "instance": "9", "event": "pcscf_rebind", "args": ["fd00::2"],
                "engine_run_id": "old-run"})
        self.assertFalse(stale["accepted"])


class UsimAuthRecoveryLifecycleTests(unittest.IsolatedAsyncioTestCase):
    async def _run_recovery_snapshot(self, registration: str) -> tuple[bool, object]:
        iid = "51"
        runtime = {"running": True, "container_id": "a" * 64,
                   "started_at": "2026-08-24T15:27:07.939955772Z",
                   "engine_run_id": "run-51"}
        failure = {"engine_run_id": "run-51", "auth_seq": 2,
                   "cause_class": "pcsc_service_unavailable", "ts": 1000.0}
        protocol = object()
        ami = SimpleNamespace(
            connected=True,
            _mgr=SimpleNamespace(protocol=protocol),
            zero_usim_recovery_call_channels_complete=AsyncMock(return_value=True),
            zero_channels_complete=AsyncMock(
                side_effect=AssertionError("global zero-channel semantics must remain unused")),
            registration_state=AsyncMock(return_value=registration),
        )
        observed = {}

        def submit(_iid, **kwargs):
            observed["zero_channels"] = kwargs["zero_channels"]()
            observed["before_exec"] = kwargs["before_exec"]()
            return {"status": "channel_state_unknown", "submitted": False}

        main.hub.engine_recovery_locks.pop(iid, None)
        try:
            with patch.object(main, "_durable_maintenance_pending", return_value=False), \
                    patch.object(main.engine, "usim_recovery_fence_pending",
                                 return_value=True), \
                    patch.object(main.hub.runtime, "get",
                                 new=AsyncMock(return_value=runtime)), \
                    patch.object(main.engine, "usim_status", return_value={}), \
                    patch.object(main.status_mod, "current_local_usim_unavailable",
                                 return_value=failure), \
                    patch.object(main, "_remote_usim_recovery_topology",
                                 return_value=("b" * 64, {"reader_id": "reader"})), \
                    patch.object(main, "_same_remote_usim_recovery_topology",
                                 return_value=True), \
                    patch.object(main, "_line_auto_start_allowed",
                                 return_value=(True, "")), \
                    patch.object(main.engine, "reserve_usim_recovery_attempt",
                                 return_value={"status": "reserved", "attempt": 1}), \
                    patch.object(main, "_pcscf_rebind_pending",
                                 new=AsyncMock(return_value=False)), \
                    patch.object(main.hub, "ami_for", new=AsyncMock(return_value=ami)), \
                    patch.object(main.engine, "usim_recovery_transport_ready",
                                 return_value=True), \
                    patch.object(main.engine, "submit_usim_recovery_register",
                                 side_effect=submit):
                await main._reconcile_usim_auth_recovery({"id": iid})
        finally:
            main.hub.engine_recovery_locks.pop(iid, None)
        return bool(observed["zero_channels"]), ami

    async def test_recovery_uses_dedicated_call_snapshot_for_retryable_registration(self):
        for registration in ("Rejected", "Unregistered"):
            with self.subTest(registration=registration):
                zero_channels, ami = await self._run_recovery_snapshot(registration)

                self.assertTrue(zero_channels)
                ami.zero_usim_recovery_call_channels_complete.assert_awaited_once_with(
                    timeout=2.0)
                ami.zero_channels_complete.assert_not_awaited()

    async def test_recovery_still_rejects_registered_state(self):
        zero_channels, ami = await self._run_recovery_snapshot("Registered")

        self.assertFalse(zero_channels)
        ami.zero_usim_recovery_call_channels_complete.assert_awaited_once_with(timeout=2.0)
        ami.registration_state.assert_awaited_once()


class LineDeleteApiTests(unittest.IsolatedAsyncioTestCase):
    async def test_delete_line_can_delete_history_and_pause_inserted_card(self):
        inst = {"id": "1", "iccid": "test-iccid"}
        card = {"present": True, "matched": "1", "iccid": "test-iccid"}
        with patch.object(main.cfg, "get_instance", return_value=inst), \
                patch.object(main.hub, "cards_list", return_value=[card]), \
                patch.object(main.cfg, "suppress_card_until_removal") as suppress, \
                patch.object(main.engine, "stop"), \
                patch.object(main.hub, "drop_ami", new=AsyncMock()), \
                patch.object(main.cfg, "delete_instance") as delete_config, \
                patch.object(main.engine, "delete_instance_data") as delete_data, \
                patch.object(main.store, "begin_instance_call_fence"), \
                patch.object(main.store, "end_instance_call_fence"), \
                patch.object(main.store, "clear_messages", return_value=12) as clear_messages, \
                patch.object(main.store, "clear_calls", return_value=3) as clear_calls, \
                patch.object(main.store, "clear_line_states", return_value=7) as clear_states, \
                patch.object(main, "_refresh_card_matches"), \
                patch.object(main.hub, "broadcast", new=AsyncMock()):
            result = await main.api_instance_delete("1", delete_history=True, confirm_id="1")

        suppress.assert_called_once_with("test-iccid")
        delete_config.assert_called_once_with("1")
        delete_data.assert_called_once()
        self.assertEqual(delete_data.call_args.args, ("1",))
        self.assertIn("_permit", delete_data.call_args.kwargs)
        clear_messages.assert_called_once_with("1")
        clear_calls.assert_called_once_with("1")
        clear_states.assert_called_once_with("1")
        self.assertTrue(result["history_deleted"])

    async def test_delete_line_can_preserve_history(self):
        inst = {"id": "1", "iccid": ""}
        with patch.object(main.cfg, "get_instance", return_value=inst), \
                patch.object(main.hub, "cards_list", return_value=[]), \
                patch.object(main.engine, "stop"), \
                patch.object(main.hub, "drop_ami", new=AsyncMock()), \
                patch.object(main.cfg, "delete_instance"), \
                patch.object(main.engine, "delete_instance_data"), \
                patch.object(main.store, "begin_instance_call_fence"), \
                patch.object(main.store, "end_instance_call_fence"), \
                patch.object(main.store, "clear_messages") as clear_messages, \
                patch.object(main.store, "clear_calls") as clear_calls, \
                patch.object(main.store, "clear_line_states") as clear_states, \
                patch.object(main, "_refresh_card_matches"), \
                patch.object(main.hub, "broadcast", new=AsyncMock()):
            result = await main.api_instance_delete("1", delete_history=False, confirm_id="1")

        clear_messages.assert_not_called()
        clear_calls.assert_not_called()
        self.assertFalse(result["history_deleted"])

    async def test_delete_requires_exact_line_id_confirmation(self):
        with self.assertRaises(main.HTTPException) as raised:
            await main.api_instance_delete("line-2", confirm_id="line-1")
        self.assertEqual(raised.exception.status_code, 400)

    async def test_delete_duplicate_line_starts_surviving_owner(self):
        inst = {"id": "old", "iccid": "same-card"}
        replacement = {"id": "2", "iccid": "same-card", "enabled": True}
        card = {"present": True, "matched": "old", "iccid": "same-card"}
        with patch.object(main.cfg, "get_instance", return_value=inst), \
                patch.object(main.cfg, "list_instances", return_value=[inst, replacement]), \
                patch.object(main.hub, "cards_list", return_value=[card]), \
                patch.object(main.cfg, "suppress_card_until_removal") as suppress, \
                patch.object(main.engine, "stop"), \
                patch.object(main.hub, "drop_ami", new=AsyncMock()), \
                patch.object(main.cfg, "delete_instance"), \
                patch.object(main.engine, "delete_instance_data"), \
                patch.object(main.store, "begin_instance_call_fence"), \
                patch.object(main.store, "end_instance_call_fence"), \
                patch.object(main.store, "clear_line_states"), \
                patch.object(main, "_refresh_card_matches"), \
                patch.object(main, "_auto_start_hotplugged_line", new=AsyncMock()) as start, \
                patch.object(main.hub, "broadcast", new=AsyncMock()):
            await main.api_instance_delete("old", delete_history=False, confirm_id="old")
            await __import__('asyncio').sleep(0)

        suppress.assert_not_called()
        start.assert_awaited_once_with("2")


class ControlShutdownFenceTests(unittest.IsolatedAsyncioTestCase):
    def tearDown(self):
        control_lifecycle.reset_for_startup()
        main.hub.cards.pop("shutdown-reader", None)
        main.hub.cards.pop("real-reader", None)
        main.hub.engine_recovery_locks.pop("shutdown-line", None)
        main.hub.engine_recovery_locks.pop("real-line", None)

    def test_signal_fences_card_loss_before_requesting_both_servers_exit(self):
        servers = [SimpleNamespace(should_exit=False, force_exit=False),
                   SimpleNamespace(should_exit=False, force_exit=False)]
        observed = []

        def begin_shutdown():
            observed.append(tuple(server.should_exit for server in servers))

        with patch.object(control_run.control_lifecycle, "begin_shutdown",
                          side_effect=begin_shutdown):
            control_run._coordinated_exit(servers, signal.SIGTERM, None)

        self.assertEqual(observed, [(False, False)])
        self.assertTrue(all(server.should_exit for server in servers))
        self.assertFalse(any(server.force_exit for server in servers))

        control_run._coordinated_exit(servers, signal.SIGINT, None)
        self.assertTrue(all(server.force_exit for server in servers))

    async def test_http_and_https_configs_bound_connection_drain(self):
        configs = []

        class FakeConfig:
            def __init__(self, *args, **kwargs):
                del args
                self.__dict__.update(kwargs)
                configs.append(self)

        class FakeServer:
            def __init__(self, config):
                self.config = config
                self.should_exit = False
                self.force_exit = False

            async def serve(self):
                return

        fake_uvicorn = SimpleNamespace(Config=FakeConfig, Server=FakeServer)
        with patch.dict(sys.modules, {"uvicorn": fake_uvicorn}):
            await control_run._run_dual(
                "127.0.0.1", 8443, 8000, "cert", "key")

        self.assertEqual(len(configs), 2)
        self.assertTrue(all(config.timeout_graceful_shutdown == 2.0
                            for config in configs))

    async def test_shutdown_transport_loss_does_not_mutate_card_or_engine(self):
        entry = {"name": "shutdown-reader", "index": 2,
                 "matched": "shutdown-line", "iccid": "saved-card"}
        original = {**entry, "present": True}
        main.hub.cards["shutdown-reader"] = dict(original)
        control_lifecycle.begin_shutdown()
        with patch.object(main.cfg, "unsuppress_card") as unsuppress, \
                patch.object(main.cfg, "get_instance") as get_instance, \
                patch.object(main.engine, "stop_for_card_loss") as stop:
            stopped = await main._on_card_remove(entry, reader_unplugged=True)

        self.assertFalse(stopped)
        self.assertEqual(main.hub.cards["shutdown-reader"], original)
        unsuppress.assert_not_called()
        get_instance.assert_not_called()
        stop.assert_not_called()

    async def test_shutdown_winning_while_waiting_for_recovery_lock_does_not_stop(self):
        entry = {"name": "shutdown-reader", "index": 2,
                 "matched": "shutdown-line", "iccid": "saved-card"}
        inst = {"id": "shutdown-line", "iccid": "saved-card"}
        reached_target_lookup = asyncio.Event()
        lock = main.hub.recovery_lock("shutdown-line")
        await lock.acquire()

        def get_instance(_iid):
            reached_target_lookup.set()
            return inst

        control_lifecycle.reset_for_startup()
        try:
            with patch.object(main.cfg, "unsuppress_card"), \
                    patch.object(main.cfg, "get_instance", side_effect=get_instance), \
                    patch.object(main.hub, "reset_health") as reset_health, \
                    patch.object(main.engine, "stop_for_card_loss") as stop:
                task = asyncio.create_task(main._on_card_remove(entry))
                await reached_target_lookup.wait()
                await asyncio.sleep(0)
                control_lifecycle.begin_shutdown()
                lock.release()
                stopped = await task
        finally:
            if lock.locked():
                lock.release()

        self.assertFalse(stopped)
        reset_health.assert_not_called()
        stop.assert_not_called()

    async def test_real_card_removal_still_performs_exact_containment(self):
        entry = {"name": "real-reader", "index": 4,
                 "matched": "real-line", "iccid": "real-card"}
        inst = {"id": "real-line", "iccid": "real-card"}
        control_lifecycle.reset_for_startup()
        with patch.object(main.cfg, "unsuppress_card") as unsuppress, \
                patch.object(main.cfg, "get_instance", return_value=inst), \
                patch.object(main.engine, "is_running", return_value=True), \
                patch.object(main.engine, "stop_for_card_loss",
                             return_value={"stopped": True}) as stop, \
                patch.object(main.hub, "drop_ami", new=AsyncMock()) as drop_ami, \
                patch.object(main.hub, "broadcast", new=AsyncMock()):
            stopped = await main._on_card_remove(entry)

        self.assertTrue(stopped)
        unsuppress.assert_called_once_with("real-card")
        stop.assert_called_once()
        drop_ami.assert_awaited_once_with("real-line")

    async def test_lifespan_exit_fences_then_runs_existing_shutdown_cleanup(self):
        previous_users = main._lifespan_users
        main._lifespan_users = 1
        control_lifecycle.reset_for_startup()
        cleanup = AsyncMock()
        try:
            with patch.object(main, "_shutdown_background_tasks", new=cleanup):
                context = main.lifespan(main.app)
                await context.__aenter__()
                # Simulate the peer listener having already completed its lifespan.
                main._lifespan_users = 1
                await context.__aexit__(None, None, None)
        finally:
            main._lifespan_users = previous_users

        self.assertTrue(control_lifecycle.shutdown_started())
        cleanup.assert_awaited_once()


class RemoteVpcdLossContainmentTests(unittest.IsolatedAsyncioTestCase):
    def tearDown(self):
        for name in ("Virtual PCD 00 01", "Virtual PCD 00 0A", "USB Reader"):
            main.hub.cards.pop(name, None)
        main.hub.remote_loss_candidates.clear()
        main.hub.remote_loss_inflight.clear()
        main.hub.remote_loss_completed.clear()
        main.hub.remote_reader_seen.clear()

    async def test_stale_remote_probe_cannot_update_config_or_publish_identity(self):
        name = "Virtual PCD 00 01"
        previous = {"name": name, "index": 1, "present": False,
                    "card_presence": "unknown", "iccid": "old-card", "matched": "7"}
        main.hub.cards[name] = previous
        card = SimpleNamespace(
            iccid="new-card", pin_enabled=False, pin_tries=3, imsi="234100000000001",
            mcc="234", mnc="10", mnc_len=2, smsc="+447700900000", spn="Carrier",
            carrier_identity={"spn": "Carrier"}, gid1=None, gid2=None)
        with patch.object(main.usbreader, "port_for_index", return_value="usb-port"), \
                patch.object(main, "_find_running_by_reader", return_value=None), \
                patch.object(main.vpcd_registry, "begin_observation",
                             return_value="old-generation"), \
                patch.object(main.sim, "read_card", return_value=card), \
                patch.object(main.vpcd_registry, "observe_card",
                             return_value=False) as observe, \
                patch.object(main, "_match_instance_by_iccid") as match, \
                patch.object(main, "_ensure_card_draft") as draft, \
                patch.object(main.cfg, "upsert_instance") as upsert:
            await main._on_card_insert(name, 1)

        observe.assert_called_once()
        match.assert_not_called()
        draft.assert_not_called()
        upsert.assert_not_called()
        self.assertEqual(main.hub.cards[name]["iccid"], "old-card")
        self.assertEqual(main.hub.cards[name]["card_presence"], "unknown")

    async def test_remote_probe_exception_publishes_unrecognized_without_side_effects(self):
        name = "Virtual PCD 00 01"
        main.hub.cards.pop(name, None)
        with patch.object(main.usbreader, "port_for_index", return_value="usb-port"), \
                patch.object(main, "_find_running_by_reader", return_value=None), \
                patch.object(main.vpcd_registry, "begin_observation",
                             return_value="generation-a"), \
                patch.object(main.sim, "read_card", side_effect=RuntimeError("PCSC failed")), \
                patch.object(main.vpcd_registry, "observe_card") as observe, \
                patch.object(main, "_match_instance_by_iccid") as match, \
                patch.object(main, "_ensure_card_draft") as draft, \
                patch.object(main.cfg, "upsert_instance") as upsert:
            await main._on_card_insert(name, 1)

        observe.assert_not_called()
        match.assert_not_called()
        draft.assert_not_called()
        upsert.assert_not_called()
        self.assertTrue(main.hub.cards[name]["present"])
        self.assertIsNone(main.hub.cards[name]["iccid"])
        self.assertEqual(main.hub.cards[name]["card_presence"], "present")

    async def test_blank_euicc_is_sanitized_before_first_cas_and_generation_change(self):
        name = "Virtual PCD 00 01"
        previous = {"name": name, "index": 1, "present": False,
                    "card_presence": "unknown", "iccid": "old-card", "matched": "7"}
        main.hub.cards[name] = previous
        card = SimpleNamespace(
            iccid="891111111111", pin_enabled=False, pin_tries=3,
            imsi="111111111111111", mcc="111", mnc="11", mnc_len=2,
            smsc="", spn="", carrier_identity={}, gid1=None, gid2=None)
        with tempfile.TemporaryDirectory() as temp:
            registry = main.vpcd_slots.VpcdSlotRegistry(
                str(Path(temp) / "vpcd-slots.json"))
            first_claim = registry.claim(
                agent_id="agent-a", reader_id="reader-a", requested_slot=1,
                agent_run_id="run-a")
            original_observe = registry.observe_card
            calls = 0

            def observe_then_reconnect(*args, **kwargs):
                nonlocal calls
                calls += 1
                result = original_observe(*args, **kwargs)
                if calls == 1:
                    self.assertTrue(result)
                    registry.release(first_claim)
                    registry.claim(
                        agent_id="agent-a", reader_id="reader-a", requested_slot=1,
                        agent_run_id="run-a")
                return result

            with patch.object(main, "vpcd_registry", registry), \
                    patch.object(registry, "observe_card",
                                 side_effect=observe_then_reconnect), \
                    patch.object(main.usbreader, "port_for_index",
                                 return_value="usb-port"), \
                    patch.object(main, "_find_running_by_reader", return_value=None), \
                    patch.object(main.sim, "read_card", return_value=card), \
                    patch.object(main, "_match_instance_by_iccid") as match, \
                    patch.object(main, "_ensure_card_draft") as draft:
                await main._on_card_insert(name, 1)

            record = registry.snapshot()[0]
        self.assertEqual(calls, 2)
        match.assert_not_called()
        draft.assert_not_called()
        self.assertFalse(record["identity_current"])
        self.assertNotIn("iccid", record)
        self.assertNotIn("imsi", record)

    async def test_remote_present_false_becomes_unknown_without_card_loss_actions(self):
        entry = {"name": "Virtual PCD 00 01", "index": 1, "present": True,
                 "iccid": "saved-card", "matched": "7", "imsi": "saved-imsi"}
        main.hub.cards[entry["name"]] = entry
        with patch.object(main, "_on_card_remove", new=AsyncMock()) as remove, \
                patch.object(main.cfg, "unsuppress_card") as unsuppress, \
                patch.object(main.hub, "reset_health") as reset_health, \
                patch.object(main.engine, "stop_for_card_loss") as stop:
            stopped = await main._handle_card_absence(entry)

        self.assertFalse(stopped)
        self.assertFalse(entry["present"])
        self.assertTrue(entry["enumerated"])
        self.assertEqual(entry["card_presence"], "unknown")
        self.assertEqual(entry["iccid"], "saved-card")
        self.assertEqual(entry["matched"], "7")
        remove.assert_not_awaited()
        unsuppress.assert_not_called()
        reset_health.assert_not_called()
        stop.assert_not_called()

    async def test_remote_reader_disappearance_is_retained_as_unknown(self):
        name = "Virtual PCD 00 0A"
        entry = {"name": name, "index": 10, "present": True,
                 "iccid": "saved-card", "matched": "1"}
        main.hub.cards[name] = entry
        with patch.object(main, "_on_card_remove", new=AsyncMock()) as remove:
            stopped, remote_unknown = await main._handle_reader_disappearance(name, entry)

        self.assertFalse(stopped)
        self.assertTrue(remote_unknown)
        self.assertIs(main.hub.cards[name], entry)
        self.assertFalse(entry["enumerated"])
        self.assertEqual(entry["card_presence"], "unknown")
        remove.assert_not_awaited()

    async def test_native_absence_keeps_existing_exact_removal_paths(self):
        entry = {"name": "USB Reader", "index": 2, "present": True,
                 "iccid": "native-card", "matched": "2"}
        main.hub.cards[entry["name"]] = entry
        with patch.object(main, "_on_card_remove", new=AsyncMock(
                side_effect=[True, True])) as remove:
            self.assertTrue(await main._handle_card_absence(entry))
            stopped, remote_unknown = await main._handle_reader_disappearance(
                entry["name"], entry)

        self.assertTrue(stopped)
        self.assertFalse(remote_unknown)
        self.assertNotIn(entry["name"], main.hub.cards)
        self.assertEqual(remove.await_count, 2)
        self.assertEqual(remove.await_args_list[1].kwargs, {"reader_unplugged": True})

    async def test_same_run_health_v2_and_live_generation_confirm_card_loss_once(self):
        name = "Virtual PCD 00 01"
        entry = {"name": name, "index": 1, "present": False, "enumerated": True,
                 "iccid": "saved-card", "matched": "7"}
        main.hub.cards[name] = entry
        record = {
            "slot": 1, "online": True, "identity_current": True,
            "identity_session_generation": "vpcd-generation",
            "session_generation": "vpcd-generation", "agent_id": "agent-a",
            "agent_run_id": "run-a", "reader_id": "reader-a",
        }
        authority = {
            "agent_id": "agent-a", "run_id": "run-a", "session_id": "health-session",
            "revision": 2, "pcsc": {"version": 2, "discovery": "ok", "generation": 5,
                                      "readers": [{"reader_id": "reader-a",
                                                   "name": "Reader A",
                                                   "card_present": False}]},
        }
        with patch.object(main.vpcd_registry, "snapshot", return_value=[record]), \
                patch.object(main.agent_health_registry, "reader_authority",
                             new=AsyncMock(return_value=authority)), \
                patch.object(main, "REMOTE_CARD_LOSS_STABLE_SECONDS", 0), \
                patch.object(main, "_on_card_remove", new=AsyncMock(
                    return_value=True)) as remove, \
                patch.object(main.vpcd_registry, "confirm_card_absent") as absent:
            self.assertFalse(await main._reconcile_remote_card_evidence(name))
            self.assertTrue(await main._reconcile_remote_card_evidence(name))
            self.assertFalse(await main._reconcile_remote_card_evidence(name))

        remove.assert_awaited_once_with(
            entry, reader_unplugged=False,
            remote_evidence_key=(
                "card", name, "agent-a", "run-a", "health-session", "reader-a",
                "vpcd-generation", 5))
        absent.assert_called_once_with(name, "vpcd-generation")

    async def test_failed_remote_stop_is_not_completed_or_confirmed(self):
        name = "Virtual PCD 00 01"
        entry = {"name": name, "index": 1, "present": False, "enumerated": True,
                 "iccid": "saved-card", "matched": "7", "card_presence": "unknown"}
        main.hub.cards[name] = entry
        record = {
            "slot": 1, "online": True, "identity_current": True,
            "identity_session_generation": "vpcd-generation",
            "session_generation": "vpcd-generation", "agent_id": "agent-a",
            "agent_run_id": "run-a", "reader_id": "reader-a",
        }
        authority = {
            "session_id": "health-session",
            "pcsc": {"version": 2, "discovery": "ok", "generation": 5,
                     "readers": [{"reader_id": "reader-a", "name": "Reader A",
                                  "card_present": False}]},
        }
        with patch.object(main.vpcd_registry, "snapshot", return_value=[record]), \
                patch.object(main.agent_health_registry, "reader_authority",
                             new=AsyncMock(return_value=authority)), \
                patch.object(main, "REMOTE_CARD_LOSS_STABLE_SECONDS", 0), \
                patch.object(main, "_on_card_remove",
                             new=AsyncMock(return_value=False)) as remove, \
                patch.object(main.vpcd_registry, "confirm_card_absent") as absent:
            self.assertFalse(await main._reconcile_remote_card_evidence(name))
            self.assertFalse(await main._reconcile_remote_card_evidence(name))

        remove.assert_awaited_once()
        absent.assert_not_called()
        self.assertFalse(main.hub.remote_loss_inflight)
        self.assertFalse(main.hub.remote_loss_completed)
        self.assertEqual(entry["card_presence"], "unknown")

    async def test_health_session_change_restarts_stability_window(self):
        name = "Virtual PCD 00 01"
        entry = {"name": name, "index": 1, "present": False, "enumerated": True,
                 "iccid": "saved-card", "matched": "7"}
        main.hub.cards[name] = entry
        record = {
            "slot": 1, "online": True, "identity_current": True,
            "identity_session_generation": "vpcd-generation",
            "session_generation": "vpcd-generation", "agent_id": "agent-a",
            "agent_run_id": "run-a", "reader_id": "reader-a",
        }
        authority = {
            "session_id": "health-session-1",
            "pcsc": {"version": 2, "discovery": "ok", "generation": 5,
                     "readers": [{"reader_id": "reader-a", "name": "Reader A",
                                  "card_present": False}]},
        }
        with patch.object(main.vpcd_registry, "snapshot", return_value=[record]), \
                patch.object(main.agent_health_registry, "reader_authority",
                             new=AsyncMock(return_value=authority)), \
                patch.object(main, "REMOTE_CARD_LOSS_STABLE_SECONDS", 0), \
                patch.object(main, "_on_card_remove",
                             new=AsyncMock(return_value=True)) as remove:
            self.assertFalse(await main._reconcile_remote_card_evidence(name))
            authority["session_id"] = "health-session-2"
            self.assertFalse(await main._reconcile_remote_card_evidence(name))
            self.assertTrue(await main._reconcile_remote_card_evidence(name))

        remove.assert_awaited_once()
        self.assertEqual(remove.await_args.kwargs["remote_evidence_key"][4],
                         "health-session-2")

    async def test_reader_unplug_requires_prior_seen_in_same_live_health_session(self):
        name = "Virtual PCD 00 0A"
        entry = {"name": name, "index": 10, "present": True, "enumerated": True,
                 "iccid": "saved-card", "matched": "1"}
        main.hub.cards[name] = entry
        record = {
            "slot": 10, "online": True, "identity_current": True,
            "identity_session_generation": "generation-a",
            "session_generation": "generation-a", "agent_id": "agent-a",
            "agent_run_id": "run-a", "reader_id": "reader-a",
        }
        authority = {
            "session_id": "health-session",
            "pcsc": {"version": 2, "discovery": "ok", "generation": 9,
                     "readers": [
                         {"reader_id": "reader-a", "name": "Reader A",
                          "card_present": True},
                         {"reader_id": "reader-b", "name": "Reader B",
                          "card_present": True},
                     ]},
        }
        with patch.object(main.vpcd_registry, "snapshot", return_value=[record]), \
                patch.object(main.agent_health_registry, "reader_authority",
                             new=AsyncMock(return_value=authority)), \
                patch.object(main, "REMOTE_CARD_LOSS_STABLE_SECONDS", 0), \
                patch.object(main, "_on_card_remove",
                             new=AsyncMock(return_value=True)) as remove:
            # Healthy inventory establishes that this exact reader existed in this session.
            self.assertFalse(await main._reconcile_remote_card_evidence(name))
            entry.update(present=False, enumerated=False, card_presence="unknown")
            record.update(online=False, identity_current=False)
            authority["pcsc"]["readers"] = [
                {"reader_id": "reader-b", "name": "Reader B", "card_present": True}]
            self.assertFalse(await main._reconcile_remote_card_evidence(name))
            self.assertTrue(await main._reconcile_remote_card_evidence(name))

        remove.assert_awaited_once()
        self.assertTrue(remove.await_args.kwargs["reader_unplugged"])

    async def test_remote_removal_rechecks_generation_inside_recovery_lock(self):
        name = "Virtual PCD 00 01"
        entry = {"name": name, "index": 1, "present": False, "enumerated": True,
                 "card_presence": "unknown", "iccid": "saved-card", "matched": "7"}
        main.hub.cards[name] = dict(entry)
        inst = {"id": "7", "iccid": "saved-card"}
        evidence_key = ("card", name, "agent-a", "run-a", "health-session",
                        "reader-a", "generation-a", 1)
        with patch.object(main, "_remote_loss_key_current",
                          new=AsyncMock(side_effect=[True, False])) as current, \
                patch.object(main.cfg, "get_instance", return_value=inst), \
                patch.object(main.cfg, "unsuppress_card") as unsuppress, \
                patch.object(main.engine, "is_running", return_value=True), \
                patch.object(main.engine, "stop_for_card_loss") as stop, \
                patch.object(main.hub, "reset_health") as reset_health:
            stopped = await main._on_card_remove(
                entry, remote_evidence_key=evidence_key)

        self.assertFalse(stopped)
        self.assertEqual(current.await_count, 2)
        stop.assert_not_called()
        reset_health.assert_not_called()
        unsuppress.assert_not_called()
        self.assertEqual(main.hub.cards[name]["iccid"], "saved-card")

    async def test_remote_stop_failure_preserves_unknown_identity(self):
        name = "Virtual PCD 00 01"
        entry = {"name": name, "index": 1, "present": False, "enumerated": True,
                 "card_presence": "unknown", "iccid": "saved-card", "matched": "7"}
        main.hub.cards[name] = dict(entry)
        inst = {"id": "7", "iccid": "saved-card"}
        evidence_key = ("card", name, "agent-a", "run-a", "health-session",
                        "reader-a", "generation-a", 1)
        with patch.object(main, "_remote_loss_key_current",
                          new=AsyncMock(return_value=True)), \
                patch.object(main.cfg, "get_instance", return_value=inst), \
                patch.object(main.cfg, "unsuppress_card") as unsuppress, \
                patch.object(main.engine, "is_running", return_value=True), \
                patch.object(main.engine, "stop_for_card_loss",
                             return_value={"status": "forced_manual", "stopped": False}), \
                patch.object(main.hub, "drop_ami", new=AsyncMock()) as drop_ami:
            stopped = await main._on_card_remove(
                entry, remote_evidence_key=evidence_key)

        self.assertFalse(stopped)
        unsuppress.assert_not_called()
        drop_ami.assert_not_awaited()
        self.assertEqual(main.hub.cards[name]["iccid"], "saved-card")
        self.assertEqual(main.hub.cards[name]["card_presence"], "unknown")

    async def test_remote_missing_engine_is_safe_terminal_absence(self):
        name = "Virtual PCD 00 01"
        entry = {"name": name, "index": 1, "present": False, "enumerated": True,
                 "card_presence": "unknown", "iccid": "saved-card", "matched": "7"}
        main.hub.cards[name] = dict(entry)
        inst = {"id": "7", "iccid": "saved-card"}
        evidence_key = ("card", name, "agent-a", "run-a", "health-session",
                        "reader-a", "generation-a", 1)
        with patch.object(main, "_remote_loss_key_current",
                          new=AsyncMock(return_value=True)), \
                patch.object(main.cfg, "get_instance", return_value=inst), \
                patch.object(main.cfg, "unsuppress_card") as unsuppress, \
                patch.object(main.engine, "is_running", return_value=False), \
                patch.object(main.engine, "stop_for_card_loss",
                             return_value={"status": "missing", "stopped": False}), \
                patch.object(main.hub, "broadcast", new=AsyncMock()):
            contained = await main._on_card_remove(
                entry, remote_evidence_key=evidence_key)

        self.assertTrue(contained)
        unsuppress.assert_called_once_with("saved-card")
        self.assertEqual(main.hub.cards[name]["card_presence"], "absent")
        self.assertIsNone(main.hub.cards[name]["iccid"])

    def test_confirmed_absence_is_scoped_to_same_vpcd_generation(self):
        name = "Virtual PCD 00 01"
        key = ("card", name, "agent-a", "run-a", "health-session", "reader-a",
               "generation-a", 5)
        main.hub.remote_loss_completed.add(key)
        with patch.object(main.vpcd_registry, "snapshot", return_value=[{
                "slot": 1, "session_generation": "generation-a"}]):
            self.assertTrue(main._remote_card_absence_confirmed(name))
        with patch.object(main.vpcd_registry, "snapshot", return_value=[{
                "slot": 1, "session_generation": "generation-b"}]):
            self.assertFalse(main._remote_card_absence_confirmed(name))

    async def test_conflicting_or_new_generation_remote_evidence_never_stops(self):
        name = "Virtual PCD 00 01"
        entry = {"name": name, "index": 1, "present": False, "enumerated": True,
                 "iccid": "saved-card", "matched": "7"}
        main.hub.cards[name] = entry
        base = {
            "slot": 1, "online": True, "identity_current": True,
            "identity_session_generation": "old-generation",
            "session_generation": "new-generation", "agent_id": "agent-a",
            "agent_run_id": "run-a", "reader_id": "reader-a",
        }
        authority = {
            "pcsc": {"version": 2, "discovery": "ok", "generation": 8,
                     "readers": [{"reader_id": "reader-a", "name": "Reader A",
                                  "card_present": False}]},
        }
        with patch.object(main.vpcd_registry, "snapshot", return_value=[base]), \
                patch.object(main.agent_health_registry, "reader_authority",
                             new=AsyncMock(return_value=authority)), \
                patch.object(main, "_on_card_remove", new=AsyncMock()) as remove:
            self.assertFalse(await main._reconcile_remote_card_evidence(name))
        remove.assert_not_awaited()

        # Health says the reader/card is still present while VPCD is offline: transport
        # ambiguity, not a physical removal.
        base.update(online=False, identity_session_generation="new-generation")
        authority["pcsc"]["readers"][0]["card_present"] = True
        with patch.object(main.vpcd_registry, "snapshot", return_value=[base]), \
                patch.object(main.agent_health_registry, "reader_authority",
                             new=AsyncMock(return_value=authority)), \
                patch.object(main, "_on_card_remove", new=AsyncMock()) as remove:
            self.assertFalse(await main._reconcile_remote_card_evidence(name))
        remove.assert_not_awaited()

    async def test_single_reader_total_absence_remains_unknown(self):
        name = "Virtual PCD 00 0A"
        entry = {"name": name, "index": 10, "present": False, "enumerated": False,
                 "iccid": "saved-card", "matched": "1"}
        main.hub.cards[name] = entry
        main.hub.remote_reader_seen.add(
            ("agent-a", "run-a", "health-session", "reader-a"))
        record = {
            "slot": 10, "online": False, "identity_current": False,
            "identity_session_generation": "generation-a",
            "session_generation": "generation-a", "agent_id": "agent-a",
            "agent_run_id": "run-a", "reader_id": "reader-a",
        }
        authority = {"session_id": "health-session",
                     "pcsc": {"version": 2, "discovery": "ok", "generation": 9,
                              "readers": []}}
        with patch.object(main.vpcd_registry, "snapshot", return_value=[record]), \
                patch.object(main.agent_health_registry, "reader_authority",
                             new=AsyncMock(return_value=authority)), \
                patch.object(main, "_on_card_remove", new=AsyncMock()) as remove:
            self.assertFalse(await main._reconcile_remote_card_evidence(name))
        remove.assert_not_awaited()


class BackgroundStartGuardTests(unittest.IsolatedAsyncioTestCase):
    def tearDown(self):
        for iid in ("1", "2", "offline", "removed"):
            main.hub.reset_health(iid)

    def test_saved_line_without_a_live_card_is_not_eligible(self):
        inst = {"id": "offline", "iccid": "saved-card", "enabled": True}
        with patch.object(main.hub, "cards_list", return_value=[]):
            self.assertEqual(main._line_auto_start_allowed(inst), (False, "no_card"))

    def test_remote_cached_identity_is_not_eligible_for_background_start(self):
        inst = {"id": "offline", "iccid": "saved-card", "enabled": True}
        card = {"present": True, "iccid": "saved-card", "remote": True,
                "connection_online": True, "identity_current": False,
                "identity_session_generation": "old", "session_generation": "new"}
        with patch.object(main.hub, "cards_list", return_value=[card]):
            self.assertEqual(main._line_auto_start_allowed(inst),
                             (False, "card_identity_unknown"))

    def test_device_vowifi_switch_blocks_background_start(self):
        inst = {"id": "offline", "iccid": "saved-card", "enabled": True}
        card = {"present": True, "iccid": "saved-card", "hardware_id": "device-1",
                "hardware_kind": "modem"}
        desired = {"defaults": {"vowifi_enabled": True},
                   "devices": {"device-1": {"vowifi_enabled": False}}}
        with patch.object(main.hub, "cards_list", return_value=[card]), \
                patch.object(main.device_state, "desired", return_value=desired):
            self.assertEqual(
                main._line_auto_start_allowed(inst), (False, "vowifi_disabled"))

    async def test_auto_recovery_does_not_recreate_an_absent_line(self):
        inst = {"id": "offline", "iccid": "saved-card", "enabled": True}
        main.hub.health_for("offline").update({
            "frozen_code": "tunnel_sim_auth", "frozen_reason": "failed",
            "auto_retrying": True,
        })
        with patch.object(main, "_line_auto_start_allowed",
                          return_value=(False, "no_card")), \
                patch.object(main, "_start_engine_checked") as start, \
                patch.object(main.hub, "broadcast", new=AsyncMock()):
            await main._auto_recover_instance("offline", inst, 60)

        start.assert_not_called()
        self.assertEqual(main.hub.status_cache["offline"]["state"], "NO_CARD")

    async def test_auto_recovery_uses_absent_only_start(self):
        iid = "auto-absent"
        inst = {"id": iid, "iccid": "saved-card", "enabled": True}
        main.hub.health_for(iid).update({
            "frozen_code": "tunnel_network", "frozen_reason": "failed",
            "auto_retrying": True,
        })
        with patch.object(main, "_line_auto_start_allowed", return_value=(True, "")), \
                patch.object(main.cfg, "get_instance", return_value=inst), \
                patch.object(main.cfg, "get_settings", return_value={}), \
                patch.object(main.hub.runtime, "get", new=AsyncMock(return_value={
                    "running": False, "container_id": None})), \
                patch.object(main, "_start_engine_checked", return_value="new") as start, \
                patch.object(main.hub, "broadcast", new=AsyncMock()):
            await main._auto_recover_instance(
                iid, inst, 60, main.hub.lifecycle_epoch(iid))

        self.assertFalse(start.call_args.kwargs["replace_existing"])

    async def test_auto_recovery_stands_down_when_lifecycle_epoch_changes(self):
        iid = "auto-stale"
        inst = {"id": iid, "iccid": "saved-card", "enabled": True}
        main.hub.health_for(iid).update({
            "frozen_code": "tunnel_network", "frozen_reason": "failed",
            "auto_retrying": True,
        })
        scheduled = main.hub.lifecycle_epoch(iid)

        async def invalidate(_message):
            main.hub.bump_lifecycle_epoch(iid)

        with patch.object(main, "_line_auto_start_allowed", return_value=(True, "")), \
                patch.object(main.cfg, "get_instance", return_value=inst), \
                patch.object(main.hub.runtime, "get", new=AsyncMock(return_value={
                    "running": False, "container_id": None})), \
                patch.object(main, "_start_engine_checked") as start, \
                patch.object(main.hub, "broadcast", new=AsyncMock(side_effect=invalidate)):
            await main._auto_recover_instance(iid, inst, 60, scheduled)

        start.assert_not_called()
        self.assertFalse(main.hub.health_for(iid)["auto_retrying"])

    async def test_card_removal_cancels_a_pending_recovery_without_a_container(self):
        inst = {"id": "removed", "iccid": "saved-card"}
        main.hub.health_for("removed").update({
            "frozen_code": "tunnel_sim_auth", "next_retry_at": 12345,
        })
        entry = {"name": "reader", "index": 2, "matched": "removed",
                 "iccid": "saved-card"}
        with patch.object(main.cfg, "unsuppress_card"), \
                patch.object(main.cfg, "get_instance", return_value=inst), \
                patch.object(main.engine, "is_running", return_value=False):
            stopped = await main._on_card_remove(entry)

        self.assertFalse(stopped)
        health = main.hub.health["removed"]
        self.assertIsNone(health["frozen_code"])
        self.assertIsNone(health["next_retry_at"])

    async def test_maintenance_restart_only_recreates_the_running_snapshot(self):
        running = {"id": "1", "enabled": True}
        offline = {"id": "2", "enabled": True}

        def is_running(iid):
            return str(iid) == "1"

        with patch.object(main.cfg, "list_instances", return_value=[running, offline]), \
                patch.object(main.cfg, "get_settings", return_value={}), \
                patch.object(main.engine, "is_running", side_effect=is_running), \
                patch.object(main.engine, "stop") as stop, \
                patch.object(main, "_start_engine_checked") as start, \
                patch.object(main.hub, "drop_ami", new=AsyncMock()):
            result = await main.api_system_maintenance({"action": "restart_lines"})

        self.assertEqual(result["restarted"], ["1"])
        stop.assert_called_once_with("1")
        start.assert_called_once_with(running, {}, dev_mounts=False)


class StatusActivityTests(unittest.TestCase):
    def test_frozen_status_explains_countdown_and_next_action(self):
        main.hub.health["activity-test"] = {
            "auto_retrying": False, "fail_start": None, "retry_count": 3,
            "frozen_code": "registering", "frozen_reason": "IMS unavailable",
            "next_retry_at": None, "last_state": None,
        }
        status = main._with_status_activity("activity-test", {
            "state": "ERROR", "label": "Failed", "reason": "IMS unavailable",
            "detail": {}, "frozen": True, "automatic_retry_in": 18,
            "retry": {"count": 3, "max": 3},
        })
        self.assertEqual(status["activity"]["seconds"], 18)
        self.assertIn("rebuilt", status["activity"]["next"])
        main.hub.health.pop("activity-test", None)

    def test_permanent_pin_freeze_explains_required_manual_action(self):
        main.hub.health["pin-test"] = {
            "auto_retrying": False, "fail_start": None, "retry_count": 3,
            "frozen_code": "pin_wrong", "frozen_reason": "SIM PIN is incorrect.",
            "next_retry_at": None, "last_state": None,
        }
        status = main._with_status_activity("pin-test", {
            "state": "ERROR", "label": "Failed", "reason_code": "pin_wrong",
            "reason": "SIM PIN is incorrect.", "detail": {}, "frozen": True,
            "automatic_retry_in": None, "retry": {"count": 3, "max": 3},
        })
        self.assertFalse(status["activity"]["automatic"])
        self.assertIn("PIN", status["activity"]["next"])
        main.hub.health.pop("pin-test", None)


class OfflineDeviceStatusTests(unittest.IsolatedAsyncioTestCase):
    def test_quiet_stopped_lines_do_not_hold_gateway_on_fast_polling(self):
        instances = [{"id": "ok", "enabled": True}, {"id": "away", "enabled": True}]
        main.hub.status_cache["ok"] = {"state": "OK"}
        main.hub.status_cache["away"] = {"state": "STOPPED"}
        self.assertEqual(main._status_poll_delay(instances), main.STATUS_POLL_HEALTHY_SECONDS)
        main.hub.status_cache.pop("ok", None)
        main.hub.status_cache.pop("away", None)

    def test_registering_line_keeps_fast_polling(self):
        instances = [{"id": "starting", "enabled": True}]
        main.hub.status_cache["starting"] = {"state": "REGISTERING"}
        self.assertEqual(main._status_poll_delay(instances), main.STATUS_POLL_FAST_SECONDS)
        main.hub.status_cache.pop("starting", None)

    def test_fresh_cached_ok_is_returned_without_live_probe(self):
        iid = "cached-ok"
        main.hub.status_cache[iid] = {"state": "OK", "label": "Working",
                                      "reason_code": "ok", "reason": "Working.",
                                      "detail": {"registration": "Registered"}}
        main.hub.status_sampled_at[iid] = main.time.monotonic()
        result = main._cached_line_status({"id": iid, "enabled": True})
        self.assertEqual(result["state"], "OK")
        main.hub.reset_health(iid)

    def test_stale_cached_ok_is_not_reported_as_working(self):
        iid = "stale-ok"
        main.hub.status_cache[iid] = {"state": "OK", "label": "Working",
                                      "reason_code": "ok", "reason": "Working.",
                                      "detail": {"registration": "Registered"}}
        main.hub.status_sampled_at[iid] = (
            main.time.monotonic() - main.STATUS_CACHE_MAX_AGE_SECONDS - 1)
        result = main._cached_line_status({"id": iid, "enabled": True})
        self.assertEqual(result["state"], "REGISTERING")
        self.assertEqual(result["reason_code"], "status_stale")
        self.assertEqual(result["detail"]["stale_previous_state"], "OK")
        self.assertGreater(result["detail"]["stale_sample_age_seconds"],
                           main.STATUS_CACHE_MAX_AGE_SECONDS)
        main.hub.reset_health(iid)

    def test_cached_ok_without_valid_sample_time_is_stale(self):
        for sampled_at in (None, float("nan"), float("inf"),
                           main.time.monotonic() + 10):
            iid = f"bad-sample-{sampled_at!r}"
            main.hub.status_cache[iid] = {"state": "OK", "label": "Working",
                                          "reason_code": "ok", "reason": "Working.",
                                          "detail": {}}
            if sampled_at is not None:
                main.hub.status_sampled_at[iid] = sampled_at
            result = main._cached_line_status({"id": iid, "enabled": True})
            self.assertEqual(result["state"], "REGISTERING")
            self.assertEqual(result["reason_code"], "status_stale")
            main.hub.reset_health(iid)

    def test_disabled_line_ignores_even_fresh_cached_ok(self):
        iid = "disabled-cached-ok"
        main.hub.status_cache[iid] = {"state": "OK", "label": "Working",
                                      "reason_code": "ok", "reason": "Working.",
                                      "detail": {}}
        main.hub.status_sampled_at[iid] = main.time.monotonic()
        result = main._cached_line_status({"id": iid, "enabled": False})
        self.assertEqual(result["state"], "STOPPED")
        main.hub.reset_health(iid)

    async def test_generic_registering_is_observational_past_old_failure_budget(self):
        iid = "plain-registering"
        main.hub.reset_health(iid)
        inst = {"id": iid, "enabled": True, "retry": {"max": 2, "interval": 5}}
        status = {
            "state": "REGISTERING", "label": "Registering to IMS",
            "reason_code": "registering", "reason": "Registration is in progress.",
            "detail": {"registration": "Unregistered", "active_channels": 0},
        }
        with patch.object(main.engine, "capture_and_stop_if_idle") as capture, \
                patch.object(main, "_judge_exit_failure") as failover_judge, \
                patch.object(main.hub, "drop_ami", new=AsyncMock()) as drop_ami:
            for _ in range(8):
                main.hub.health_for(iid)["fail_start"] = main.time.monotonic() - 100_000
                result = await main._apply_health_with_recovery(
                    iid, inst, status, "same-generation")
                self.assertEqual(result["retry"], {"count": 0, "max": 2})
                self.assertNotIn("_engine_recovery", result)

        self.assertIsNone(main.hub.health_for(iid)["fail_start"])
        capture.assert_not_called()
        failover_judge.assert_not_called()
        drop_ami.assert_not_awaited()
        main.hub.reset_health(iid)

    def test_registration_activity_describes_in_place_retry_not_rebuild(self):
        for reason_code in ("registering", "reg_temporary", "reg_rejected"):
            with self.subTest(reason_code=reason_code):
                status = main._with_status_activity("activity", {
                    "state": "REGISTERING", "label": "Registering to IMS",
                    "reason_code": reason_code, "reason": "Carrier response.",
                    "detail": {"pcscf": "pcscf.example.invalid"},
                })
                self.assertNotIn("rebuilt", status["activity"]["next"].casefold())
                self.assertNotIn("重建", status["activity"]["next"])
        for state, reason_code in (("TUNNEL_DOWN", "tunnel_network"),
                                   ("REGISTERING", "reg_unanswered")):
            with self.subTest(state=state, reason_code=reason_code):
                status = main._with_status_activity("activity", {
                    "state": state, "label": "Recovering",
                    "reason_code": reason_code, "reason": "Carrier silence.",
                    "detail": {"pcscf": "pcscf.example.invalid"},
                })
                self.assertNotIn("will be rebuilt", status["activity"]["next"].casefold())
                self.assertIn("current engine", status["activity"]["next"].casefold())
        main.hub.reset_health("activity")

    async def test_bounded_local_registration_hold_keeps_same_generation(self):
        iid = "local-registration-stalled"
        inst = {"id": iid, "enabled": True, "retry": {"max": 2, "interval": 5}}
        state = {
            "state": "REGISTERING", "label": "Registering to IMS",
            "reason_code": "local_registration_stalled",
            "reason": "The local registration did not start.",
            "detail": {"registration": "Unregistered", "active_channels": 0},
        }
        main.hub.reset_health(iid)
        main.apply_health(iid, inst, state, "generation-local")
        main.hub.health_for(iid)["fail_start"] = main.time.monotonic() - 11
        plan = {"action": main.failover.HOLD, "ledger": {}, "country": "us",
                "node": "node-a", "candidates": ["node-a"], "pinned": False,
                "peer_registered": False, "swu": "DOWN", "retransmits": 0,
                "verdict": main.failover.BLAMES_EXIT, "was_backing_off": False}
        with known_health_rebuild_inputs(iid), \
                patch.object(main.engine, "capture_and_stop_if_idle") as capture, \
                patch.object(main, "_plan_exit_failure", return_value=plan), \
                patch.object(main, "_commit_exit_failure_plan",
                             return_value=main.failover.HOLD) as commit, \
                patch.object(main.cfg, "get_instance", return_value=inst), \
                patch.object(main.hub.runtime, "get", new=AsyncMock(return_value={
                    "running": True, "container_id": "generation-local",
                    "started_at": HEALTH_STARTED_AT})), \
                patch.object(main.hub, "drop_ami", new=AsyncMock()) as drop_ami:
            result = await main._apply_health_with_recovery(
                iid, inst, state, "generation-local", sampled_started_at=HEALTH_STARTED_AT)
        self.assertNotIn("frozen", result)
        self.assertEqual(result["detail"]["recovery_mode"], "in_place")
        capture.assert_not_called()
        commit.assert_called_once()
        drop_ami.assert_not_awaited()
        main.hub.reset_health(iid)

    def test_tunnel_failure_budget_is_not_carried_into_registration_progress(self):
        iid = "tunnel-then-registering"
        main.hub.reset_health(iid)
        inst = {"id": iid, "enabled": True, "retry": {"max": 3, "interval": 30}}
        tunnel = {
            "state": "TUNNEL_DOWN", "label": "Tunnel down",
            "reason_code": "tunnel_network", "reason": "ePDG did not answer.",
            "detail": {},
        }
        main.apply_health(iid, inst, tunnel, "same-generation")
        self.assertIsNotNone(main.hub.health_for(iid)["fail_start"])

        registering = {
            "state": "REGISTERING", "label": "Registering to IMS",
            "reason_code": "registering", "reason": "Registration is in progress.",
            "detail": {"registration": "Unregistered"},
        }
        result = main.apply_health(iid, inst, registering, "same-generation")
        self.assertEqual(result["retry"], {"count": 0, "max": 3})
        self.assertIsNone(main.hub.health_for(iid)["fail_start"])
        main.hub.reset_health(iid)

    async def test_ims_rejection_is_observed_without_rebuilding_the_engine(self):
        main.hub.health.pop("3", None)
        main.hub.status_cache.pop("3", None)
        rejected = {"state": "REGISTERING", "label": "Registering to IMS",
                    "reason_code": "reg_rejected", "reason": "Carrier rejected IMS.",
                    "detail": {"registration": "Rejected", "active_channels": 0}}
        with patch.object(main.engine, "capture_and_stop_if_idle") as capture, \
                patch.object(main, "_judge_exit_failure") as failover_judge, \
                patch.object(main.hub, "drop_ami", new=AsyncMock()) as drop_ami:
            inst = {"id": "3", "enabled": True,
                    "retry": {"max": 3, "interval": 30}}
            first = main.apply_health("3", inst, rejected, "generation-3")
            for _ in range(5):
                main.hub.health["3"]["fail_start"] = main.time.monotonic() - 10_000
                observed = await main._apply_health_with_recovery(
                    "3", inst, rejected, "generation-3")

        self.assertNotIn("frozen", first)
        self.assertEqual(first["retry"], {"count": 0, "max": 3})
        self.assertEqual(observed["retry"], {"count": 0, "max": 3})
        self.assertIsNone(main.hub.health["3"]["fail_start"])
        capture.assert_not_called()
        failover_judge.assert_not_called()
        drop_ami.assert_not_awaited()
        main.hub.exit_ledgers.pop("3", None)
        main.hub.health.pop("3", None)

    async def test_temporal_ims_rejection_keeps_engine_and_asterisk_retry_owner(self):
        iid = "temporary-register"
        main.hub.reset_health(iid)
        h = main.hub.health_for(iid)
        h.update({
            "fail_start": main.time.monotonic() - 10_000,
            "retry_count": 3,
            "recovery_blocked_generation": "generation-temporary",
            "recovery_blocked_until": main.time.monotonic() + 3600,
            "recovery_blocked_reason": "quiesce_restart_race",
        })
        main.hub.exit_ledgers[iid] = {"failures": 7, "reported": True}
        inst = {"id": iid, "enabled": True, "retry": {"max": 3, "interval": 30}}
        with patch.object(main.engine, "capture_and_stop_if_idle") as capture, \
                patch.object(main, "_judge_exit_failure") as failover_judge, \
                patch.object(main.hub, "drop_ami", new=AsyncMock()) as drop_ami:
            for channels in (0, 1, None, 0, None):
                temporary = {
                    "state": "REGISTERING", "label": "Registering to IMS",
                    "reason_code": "reg_temporary",
                    "reason": "Carrier retry is scheduled.",
                    "detail": {"registration": "Rejected", "sip_status": 503,
                               "retry_after_seconds": 300,
                               "active_channels": channels},
                }
                result = await main._apply_health_with_recovery(
                    iid, inst, temporary, "generation-temporary")
                self.assertEqual(result["retry"], {"count": 0, "max": 3})
                self.assertNotIn("_engine_recovery", result)

        self.assertIsNone(h["fail_start"])
        self.assertEqual(h["retry_count"], 0)
        self.assertIsNone(h["recovery_blocked_generation"])
        self.assertEqual(main.hub.exit_ledgers[iid], {"failures": 7, "reported": True})
        capture.assert_not_called()
        failover_judge.assert_not_called()
        drop_ami.assert_not_awaited()

        fatal = {
            "state": "REGISTERING", "label": "Registering to IMS",
            "reason_code": "reg_rejected", "reason": "Fatal response.",
            "detail": {"registration": "Rejected", "sip_status": 403},
        }
        fatal_result = main.apply_health(iid, inst, fatal, "generation-temporary")
        self.assertEqual(fatal_result["retry"], {"count": 0, "max": 3})
        self.assertIsNone(h["fail_start"])

        healthy = {"state": "OK", "label": "Working", "reason_code": "ok",
                   "reason": "Working.", "detail": {"registration": "Registered"}}
        self.assertEqual(main.apply_health(
            iid, inst, healthy, "generation-temporary")["retry"],
            {"count": 0, "max": 3})
        self.assertIsNone(main.hub.health_for(iid)["fail_start"])
        main.hub.exit_ledgers.pop(iid, None)
        main.hub.reset_health(iid)

    def test_temporal_log_never_unfreezes_an_existing_manual_or_pin_gate(self):
        iid = "temporary-frozen"
        main.hub.reset_health(iid)
        h = main.hub.health_for(iid)
        h.update({"frozen_code": "pin_wrong", "frozen_reason": "PIN required",
                  "next_retry_at": None})
        result = main.apply_health(iid, {"id": iid, "enabled": True}, {
            "state": "REGISTERING", "label": "Registering to IMS",
            "reason_code": "reg_temporary", "reason": "Carrier retry.",
            "detail": {"registration": "Rejected", "sip_status": 503}},
            "generation-temporary")
        self.assertTrue(result["frozen"])
        self.assertEqual(result["reason_code"], "pin_wrong")
        main.hub.reset_health(iid)

    async def test_unanswered_ims_with_no_call_uses_generation_safe_fast_recovery(self):
        iid = "fast-unanswered"
        main.hub.reset_health(iid)
        main.hub.reg_unanswered_recovery_at.pop(iid, None)
        unanswered = {
            "state": "REGISTERING", "label": "Registering to IMS",
            "reason_code": "reg_unanswered", "reason": "Carrier IMS did not answer.",
            "detail": {"registration": "Rejected", "active_channels": 0,
                       "registration_event_at": main.time.time() - 300,
                       "registration_event_key": "a" * 64, "retry_after_seconds": 30},
        }
        inst = {"id": iid, "enabled": True, "retry": {"max": 3, "interval": 30}}
        started = main.time.monotonic()
        with known_health_rebuild_inputs(iid), \
                patch.object(main.engine, "capture_and_stop_if_idle",
                          return_value={"status": "stopped", "stopped": True}) as capture_and_stop, \
                patch.object(main, "_plan_exit_failure", return_value={
                    "action": main.failover.HOLD, "ledger": {}, "country": "us",
                    "node": "node-a", "candidates": [], "pinned": False,
                    "peer_registered": False, "swu": "CONNECTED", "retransmits": 0,
                    "verdict": main.failover.BLAMES_ELSEWHERE,
                    "was_backing_off": False}), \
                patch.object(main, "_commit_exit_failure_plan",
                             return_value=main.failover.HOLD), \
                patch.object(main.cfg, "get_instance", return_value=inst), \
                patch.object(main.hub.runtime, "get", new=AsyncMock(return_value={
                    "running": True, "container_id": "generation-1",
                    "started_at": HEALTH_STARTED_AT})), \
                patch.object(main.hub, "drop_ami", new=AsyncMock()) as drop_ami:
            result = await main._apply_health_with_recovery(
                iid, inst, unanswered, "generation-1", sampled_started_at=HEALTH_STARTED_AT)
            await __import__("asyncio").sleep(0)

        self.assertTrue(result["frozen"])
        self.assertEqual(result["reason_code"], "reg_unanswered")
        self.assertGreaterEqual(result["automatic_retry_in"], 9)
        self.assertLessEqual(result["automatic_retry_in"], 10)
        capture_and_stop.assert_called_once()
        self.assertEqual(capture_and_stop.call_args.args,
                         (iid, inst, "health-freeze:reg_unanswered", "generation-1"))
        self.assertEqual(capture_and_stop.call_args.kwargs["expected_started_at"], HEALTH_STARTED_AT)
        self.assertTrue(callable(capture_and_stop.call_args.kwargs["before_quiesce"]))
        drop_ami.assert_awaited_once_with(iid)
        self.assertGreaterEqual(main.hub.reg_unanswered_recovery_at[iid], started)
        main.hub.reset_health(iid)
        main.hub.reg_unanswered_recovery_at.pop(iid, None)

    async def test_unanswered_ims_with_active_or_unknown_calls_keeps_slow_path(self):
        inst = {"id": "guarded", "enabled": True,
                "retry": {"max": 3, "interval": 30}}
        for channels in (1, None):
            with self.subTest(active_channels=channels):
                main.hub.reset_health("guarded")
                main.hub.reg_unanswered_recovery_at.pop("guarded", None)
                st = {
                    "state": "REGISTERING", "label": "Registering to IMS",
                    "reason_code": "reg_unanswered", "reason": "No response.",
                    "detail": {"registration": "Rejected", "active_channels": channels},
                }
                with patch.object(main.engine, "capture_and_stop") as capture:
                    result = main.apply_health("guarded", inst, st, "generation-1")
                self.assertNotIn("frozen", result)
                self.assertEqual(result["retry"], {"count": 1, "max": 3})
                capture.assert_not_called()
        main.hub.reset_health("guarded")

    async def test_unanswered_fast_recovery_is_rate_limited_per_line(self):
        iid = "rate-limited"
        main.hub.reset_health(iid)
        main.hub.reg_unanswered_recovery_at[iid] = main.time.monotonic()
        st = {
            "state": "REGISTERING", "label": "Registering to IMS",
            "reason_code": "reg_unanswered", "reason": "No response.",
            "detail": {"registration": "Rejected", "active_channels": 0},
        }
        inst = {"id": iid, "enabled": True, "retry": {"max": 3, "interval": 30}}
        with patch.object(main.engine, "capture_and_stop_if_idle") as capture:
            result = main.apply_health(iid, inst, st, "generation-2")
        self.assertNotIn("frozen", result)
        capture.assert_not_called()
        main.hub.reset_health(iid)
        main.hub.reg_unanswered_recovery_at.pop(iid, None)

    async def test_due_unanswered_recovery_still_preserves_engine_while_rate_limited(self):
        iid = "rate-limited-due"
        main.hub.reset_health(iid)
        main.hub.reg_unanswered_recovery_at[iid] = main.time.monotonic()
        h = main.hub.health_for(iid)
        h["fail_start"] = main.time.monotonic() - 10_000
        st = {
            "state": "REGISTERING", "label": "Registering to IMS",
            "reason_code": "reg_unanswered", "reason": "No response.",
            "detail": {"registration": "Rejected", "active_channels": 0,
                       "registration_event_at": main.time.time() - 300,
                       "registration_event_key": "a" * 64, "retry_after_seconds": 30},
        }
        inst = {"id": iid, "enabled": True, "retry": {"max": 3, "interval": 30}}
        with known_health_rebuild_inputs(iid), \
                patch.object(main.cfg, "get_instance", return_value=inst), \
                patch.object(main.hub.runtime, "get", new=AsyncMock(return_value={
                    "running": True, "container_id": "generation-rate",
                    "started_at": HEALTH_STARTED_AT})), \
                patch.object(main.engine, "capture_and_stop_if_idle") as capture:
            result = await main._apply_health_with_recovery(
                iid, inst, st, "generation-rate", sampled_started_at=HEALTH_STARTED_AT)
        self.assertEqual(
            result["detail"]["recovery_action"], "reg_unanswered_rate_limited")
        capture.assert_not_called()
        main.hub.reset_health(iid)
        main.hub.reg_unanswered_recovery_at.pop(iid, None)

    async def test_repeated_vowifi_on_request_restarts_stopped_modem_line(self):
        line = {"id": "3", "name": "Giff", "enabled": False}
        device = {"id": "modem-a", "device_type": "modem", "instance_id": "3"}
        desired = {"devices": {"modem-a": {
            "cellular_enabled": True, "vowifi_enabled": True, "flight_mode": False}}}
        observed = {"devices": {"modem-a": {"present": True}}}
        with patch.object(main, "_unified_devices", new=AsyncMock(return_value=[device])), \
                patch.object(main, "_device_sources", return_value=(desired, observed, {})), \
                patch.object(main, "_device_identities", return_value={}), \
                patch.object(main.hub, "cards_list", return_value=[]), \
                patch.object(main, "_instance_for_device", return_value=line), \
                patch.object(main.engine, "is_running", return_value=False), \
                patch.object(main.cfg, "upsert_instance", return_value={**line, "enabled": True}) as save, \
                patch.object(main.device_state, "set_desired") as set_desired, \
                patch.object(main.egress, "publish"), \
                patch.object(main, "_wait_for_device_request", new=AsyncMock()), \
                patch.object(main, "_resume_instances", new=AsyncMock(return_value={})) as resume, \
                patch.object(main.hub, "broadcast", new=AsyncMock()):
            result = await main.api_device_capabilities(
                "modem-a", {"vowifi_enabled": True})

        self.assertEqual(result["id"], "modem-a")
        save.assert_called_once_with({"id": "3", "enabled": True})
        set_desired.assert_called_once()
        resume.assert_awaited_once_with({"3"}, set())

    async def test_disabled_line_stops_stale_container_without_auto_recovery(self):
        inst = {"id": "3", "enabled": False}
        with patch.object(main.cfg, "get_instance", return_value=inst), \
                patch.object(main.hub.runtime, "get", new=AsyncMock(return_value={
                    "running": True, "ip": "172.17.0.2", "container_id": "c3"})), \
                patch.object(main.engine, "stop") as stop, \
                patch.object(main.hub, "drop_ami", new=AsyncMock()) as drop_ami, \
                patch.object(main.hub, "broadcast", new=AsyncMock()) as broadcast, \
                patch.object(main.status_mod, "compute", new=AsyncMock()) as compute:
            await main._poll_instance_status(inst)

        stop.assert_called_once_with("3", expected_container_id="c3")
        drop_ami.assert_awaited_once_with("3")
        compute.assert_not_awaited()
        self.assertEqual(main.hub.status_cache["3"]["state"], "STOPPED")
        broadcast.assert_awaited_once()
        main.hub.status_cache.pop("3", None)
        main.hub.status_sampled_at.pop("3", None)
        main.hub.health.pop("3", None)

    async def test_stale_disabled_snapshot_does_not_stop_newly_enabled_line(self):
        stale = {"id": "race", "enabled": False}
        current = {"id": "race", "enabled": True}
        sampled = {"state": "REGISTERING", "label": "Registering",
                   "reason_code": "registering", "reason": "Registering.",
                   "detail": {"registration": "unknown"}}
        with patch.object(main.cfg, "get_instance", return_value=current), \
                patch.object(main.hub.runtime, "get", new=AsyncMock(return_value={
                    "running": True, "ip": "172.17.0.2", "container_id": "race"})), \
                patch.object(main.engine, "stop") as stop, \
                patch.object(main.hub, "ami_for", new=AsyncMock(return_value=object())), \
                patch.object(main.status_mod, "compute", new=AsyncMock(return_value=sampled)), \
                patch.object(main.hub, "broadcast", new=AsyncMock()):
            await main._poll_instance_status(stale)

        stop.assert_not_called()
        self.assertEqual(main.hub.status_cache["race"]["state"], "REGISTERING")
        main.hub.reset_health("race")

    async def test_reader_enable_is_persisted_before_engine_start(self):
        device = {"id": "reader-a", "device_type": "reader", "instance_id": "7"}
        line = {"id": "7", "enabled": False}
        order = []

        def save(update):
            order.append("save")
            return {**line, **update}

        async def start(_iid):
            order.append("start")
            return {"ok": True}

        with patch.object(main, "_unified_devices", new=AsyncMock(return_value=[device])), \
                patch.object(main.cfg, "get_instance", return_value=line), \
                patch.object(main.engine, "is_running", return_value=False), \
                patch.object(main.cfg, "upsert_instance", side_effect=save) as persisted, \
                patch.object(main, "api_instance_start", new=AsyncMock(side_effect=start)):
            await main.api_device_capabilities("reader-a", {"vowifi_enabled": True})

        self.assertEqual(order, ["start"])
        persisted.assert_not_called()

    async def test_manual_start_persists_enabled_inside_lifecycle_lock(self):
        iid = "manual-enable"
        line = {"id": iid, "enabled": False, "iccid": "card"}
        order = []

        def save(update):
            order.append(("save", dict(update)))
            return {**line, **update}

        def start(inst, _settings, dev_mounts=False, permit=None):
            order.append(("start", inst["enabled"], dev_mounts))
            self.assertIsNotNone(permit)
            return "generation-manual"

        with patch.object(main.cfg, "get_instance", return_value=line), \
                patch.object(main, "_card_identity_mismatch", return_value=None), \
                patch.object(main, "_preflight_pin", new=AsyncMock(return_value={"ok": True})), \
                patch.object(main, "_reader_port_for_instance", return_value=""), \
                patch.object(main, "_reader_index_for_instance", return_value=None), \
                patch.object(main.cfg, "get_settings", return_value={}), \
                patch.object(main.cfg, "upsert_instance", side_effect=save), \
                patch.object(main, "_start_engine_checked", side_effect=start), \
                patch.object(main.hub, "drop_ami", new=AsyncMock()), \
                patch.object(main, "_clear_manual_recovery_history"):
            before = main.hub.lifecycle_epoch(iid)
            result = await main.api_instance_start(iid)

        self.assertEqual(result["container"], "generation-manual")
        self.assertEqual(order, [
            ("save", {"id": iid, "enabled": True}),
            ("start", True, False),
        ])
        self.assertEqual(main.hub.lifecycle_epoch(iid), before + 1)

    async def test_manual_start_does_not_recreate_line_deleted_while_waiting(self):
        iid = "deleted-before-start-lock"
        line = {"id": iid, "enabled": False, "iccid": "card"}
        with patch.object(main.cfg, "get_instance", side_effect=[line, None]), \
                patch.object(main, "_card_identity_mismatch", return_value=None), \
                patch.object(main, "_preflight_pin",
                             new=AsyncMock(return_value={"ok": True})), \
                patch.object(main, "_reader_port_for_instance", return_value=""), \
                patch.object(main, "_reader_index_for_instance", return_value=None), \
                patch.object(main.cfg, "get_settings", return_value={}), \
                patch.object(main.cfg, "upsert_instance") as save, \
                patch.object(main, "_start_engine_checked") as start:
            with self.assertRaises(main.HTTPException) as raised:
                await main.api_instance_start(iid)
        self.assertEqual(raised.exception.status_code, 404)
        save.assert_not_called()
        start.assert_not_called()

    async def test_reprovision_does_not_recreate_line_deleted_while_waiting(self):
        iid = "deleted-before-reprovision-lock"
        line = {"id": iid, "enabled": True, "iccid": "card"}
        with patch.object(main.cfg, "get_instance", side_effect=[line, None]), \
                patch.object(main.cfg, "upsert_instance") as save, \
                patch.object(main, "_start_engine_checked") as start:
            with self.assertRaises(main.HTTPException) as raised:
                await main.api_reprovision(iid, {"name": "stale update"})
        self.assertEqual(raised.exception.status_code, 404)
        save.assert_not_called()
        start.assert_not_called()

    async def test_manual_stop_clears_pending_automatic_recovery(self):
        main.hub.health["stop-test"] = {
            "auto_retrying": False, "fail_start": 1, "retry_count": 3,
            "frozen_code": "registering", "frozen_reason": "IMS unavailable",
            "next_retry_at": main.time.monotonic() + 1, "last_state": "REGISTERING",
        }
        inst = {"id": "stop-test", "enabled": True}
        with patch.object(main.cfg, "get_instance", return_value=inst), \
                patch.object(main.cfg, "upsert_instance", return_value={
                    **inst, "enabled": False}) as save, \
                patch.object(main.hub.runtime, "get", new=AsyncMock(return_value={
                    "running": True, "container_id": "generation-stop"})), \
                patch.object(main.engine, "stop") as stop, \
                patch.object(main.hub, "drop_ami", new=AsyncMock()) as drop_ami:
            await main.api_instance_stop("stop-test")

        self.assertIsNone(main.hub.health["stop-test"]["frozen_code"])
        self.assertIsNone(main.hub.health["stop-test"]["next_retry_at"])
        self.assertEqual(main.hub.status_cache["stop-test"]["state"], "STOPPED")
        save.assert_called_once_with({"id": "stop-test", "enabled": False})
        stop.assert_called_once_with(
            "stop-test", expected_container_id="generation-stop")
        drop_ami.assert_awaited_once_with("stop-test")
        main.hub.reset_health("stop-test")

    async def test_manual_stop_does_not_recreate_line_deleted_while_waiting(self):
        iid = "deleted-before-stop-lock"
        line = {"id": iid, "enabled": True}
        with patch.object(main.cfg, "get_instance", side_effect=[line, None]), \
                patch.object(main.cfg, "upsert_instance") as save, \
                patch.object(main.engine, "stop") as stop:
            with self.assertRaises(main.HTTPException) as raised:
                await main.api_instance_stop(iid)
        self.assertEqual(raised.exception.status_code, 404)
        save.assert_not_called()
        stop.assert_not_called()

    async def test_unknown_registration_only_holds_ok_for_bounded_grace(self):
        iid = "status-grace"
        previous = {"state": "OK", "label": "Working", "reason_code": "ok",
                    "reason": "Registered.", "detail": {"registration": "Registered"}}
        unknown = {"state": "REGISTERING", "label": "Registering",
                   "reason_code": "registering", "reason": "Checking.",
                   "detail": {"registration": "unknown"}}
        sampled_at = main.time.monotonic()
        main.hub.status_cache[iid] = previous
        main.hub.status_sampled_at[iid] = sampled_at
        inst = {"id": iid, "enabled": True}
        with patch.object(main.hub, "ami_for", new=AsyncMock(return_value=object())), \
                patch.object(main.status_mod, "compute", new=AsyncMock(return_value=unknown)), \
                patch.object(main.hub.runtime, "get", new=AsyncMock(return_value={
                    "running": True, "ip": "172.17.0.2", "container_id": "grace"})), \
                patch.object(main.hub, "broadcast", new=AsyncMock()):
            await main._poll_instance_status(inst)
            self.assertEqual(main.hub.status_cache[iid]["state"], "OK")
            self.assertEqual(main.hub.status_sampled_at[iid], sampled_at)

            main.hub.status_sampled_at[iid] = (
                main.time.monotonic() - main.STATUS_OK_GRACE_SECONDS - 1)
            await main._poll_instance_status(inst)

        self.assertEqual(main.hub.status_cache[iid]["state"], "REGISTERING")
        main.hub.reset_health(iid)

    async def test_live_modem_sim_is_present_without_vowifi_bridge_card(self):
        desired = {"devices": {"modem-a": {
            "cellular_enabled": True, "vowifi_enabled": False, "flight_mode": False}}}
        observed = {"devices": {"modem-a": {
            "present": True,
            "actual": {"cellular_radio_enabled": True, "vowifi_bridge_active": False},
            "cellular": {"available": True, "sim_iccid": "live-card",
                         "registration": "roaming", "operator": "Visited Network",
                         "radio_enabled": True, "data_active": True}}}}
        line = {"id": "3", "name": "Home SIM", "iccid": "live-card",
                "mcc": "234", "mnc": "10", "enabled": False}
        with patch.object(main, "_device_sources", return_value=(desired, observed, {})), \
                patch.object(main, "_device_identities", return_value={}), \
                patch.object(main.hub, "cards_list", return_value=[]), \
                patch.object(main.cfg, "list_instances", return_value=[line]), \
                patch.object(main.device_state, "native_reader_devices", return_value={}), \
                patch.object(main.device_state, "hardware", return_value={}), \
                patch.object(main.cfg, "get_settings", return_value={
                    "proxy": {"exits": {}}, "rekey": {"minutes": 30}}), \
                patch.object(main, "_cached_line_status", return_value=None), \
                patch.object(main.egress, "status", return_value={"lines": {}}), \
                patch.object(main.egress, "line_country", return_value="GB"), \
                patch.object(main.egress, "country_for_mcc", return_value="GB"):
            devices = await main._unified_devices()

        self.assertEqual(len(devices), 1)
        device = devices[0]
        self.assertTrue(device["sim"]["present"])
        self.assertEqual(device["sim"]["name"], "Home SIM")
        self.assertEqual(device["sim"]["carrier"]["name"], "O2")
        self.assertEqual(device["sim"]["carrier"]["plmn"], "234-10")
        self.assertEqual(device["instance_id"], "3")
        self.assertEqual(device["capabilities"]["cellular"]["actual"], "on")

    async def test_saved_unplugged_modem_never_looks_like_it_is_transitioning(self):
        desired = {"devices": {"modem-a": {
            "cellular_enabled": False, "vowifi_enabled": True, "flight_mode": False}}}
        observed = {"devices": {"modem-a": {
            "present": False, "transitioning": True,
            "actual": {"cellular_radio_enabled": False, "vowifi_bridge_active": False}}}}
        assignments = {"modem-a": {"name": "Saved modem"}}
        with patch.object(main, "_device_sources", return_value=(desired, observed, assignments)), \
                patch.object(main, "_device_identities", return_value={}), \
                patch.object(main.hub, "cards_list", return_value=[]), \
                patch.object(main.device_state, "native_reader_devices", return_value={}), \
                patch.object(main.device_state, "hardware", return_value={
                    "modem-a": {"device_type": "modem", "name": "Saved modem"}}), \
                patch.object(main.cfg, "get_settings", return_value={
                    "proxy": {"exits": {}}, "rekey": {"minutes": 30}}), \
                patch.object(main.egress, "status", return_value={"lines": {}}), \
                patch.object(main.egress, "line_country", return_value=""), \
                patch.object(main.egress, "country_for_mcc", return_value=""):
            devices = await main._unified_devices()

        self.assertEqual(len(devices), 1)
        device = devices[0]
        self.assertFalse(device["present"])
        self.assertEqual(device["capabilities"]["cellular"]["actual"], "off")
        self.assertEqual(device["capabilities"]["flight"]["actual"], "off")
        self.assertEqual(device["capabilities"]["vowifi"]["actual"], "off")
        self.assertFalse(device["capabilities"]["vowifi"]["available"])


if __name__ == "__main__":
    unittest.main()


class ConnectivityTimelineEvidenceTests(unittest.TestCase):
    """The timeline must chart what was observed, not what could not be read.

    compute() only reaches REGISTERING once the tunnel is installed, so a registration of
    "unknown" there means the read itself failed — the management timeout this codebase
    already refuses to treat as a carrier failure. Charting it as a disconnect made a line
    that never dropped its tunnel show 16 outages in a day.
    """

    def test_a_registered_line_is_up(self):
        self.assertEqual(main._line_state_kind(
            {"state": "OK", "detail": {"registration": "Registered"}}), "up")

    def test_a_stopped_line_is_off(self):
        self.assertEqual(main._line_state_kind({"state": "STOPPED", "detail": {}}), "off")

    def test_an_unreadable_registration_is_not_evidence_of_a_disconnect(self):
        for detail in ({"registration": "unknown"}, {"registration": ""}, {}):
            self.assertIsNone(main._line_state_kind({"state": "REGISTERING", "detail": detail}))

    def test_a_carrier_answer_is_still_recorded_as_down(self):
        # These are real observations of a line that is not registered.
        for registration in ("Unregistered", "Rejected"):
            self.assertEqual(main._line_state_kind(
                {"state": "REGISTERING", "detail": {"registration": registration}}), "down")

    def test_failures_before_registration_are_recorded_as_down(self):
        for state in ("TUNNEL_DOWN", "EPDG_UNRESOLVED", "NO_CARD", "PIN_PROBLEM", "ERROR"):
            self.assertEqual(main._line_state_kind({"state": state, "detail": {}}), "down")


class HostAlertSuppressionTests(unittest.TestCase):
    """Suppression is measured in hours, so it has to outlive a manager restart: an appliance
    is restarted for upgrades far more often than a brown-out or a full disk changes."""

    def setUp(self):
        self._temp = tempfile.TemporaryDirectory()
        self._patch = patch.object(main.cfg, "DATA_DIR", self._temp.name)
        self._patch.start()

    def tearDown(self):
        self._patch.stop()
        self._temp.cleanup()

    def test_state_survives_a_restart(self):
        main._save_host_alert_state({"undervoltage_seen": {"at": 1000.0}})
        self.assertEqual(main._load_host_alert_state(),
                         {"undervoltage_seen": {"at": 1000.0}})

    def test_a_missing_state_file_is_not_an_error(self):
        self.assertEqual(main._load_host_alert_state(), {})

    def test_a_corrupt_state_file_does_not_break_the_poller(self):
        with open(main._host_alert_state_path(), "w", encoding="utf-8") as handle:
            handle.write("{not json")
        self.assertEqual(main._load_host_alert_state(), {})

    def test_acknowledged_host_alert_stays_hidden_while_condition_persists(self):
        alerts = [{"code": "undervoltage_seen", "severity": "warning"}]
        self.assertEqual(main._visible_host_alerts(alerts, {}), alerts)
        self.assertEqual(main._visible_host_alerts(
            alerts, {"undervoltage_seen": {"acknowledged": True}}), [])

    def test_clear_host_alerts_persists_acknowledgement(self):
        old_alerts, old_state = main.hub.host_alerts, main.hub.host_alert_state
        try:
            main.hub.host_alerts = [{"code": "undervoltage_seen", "severity": "warning"}]
            main.hub.host_alert_state = {}

            result = main.api_host_alerts_clear()

            self.assertEqual(result["cleared"], ["undervoltage_seen"])
            self.assertEqual(main.hub.host_alerts, [])
            saved = main._load_host_alert_state()
            self.assertTrue(saved["undervoltage_seen"]["acknowledged"])
        finally:
            main.hub.host_alerts, main.hub.host_alert_state = old_alerts, old_state

    def test_the_summary_explains_each_condition_rather_than_naming_it(self):
        text = main._host_alert_summary([
            {"code": "undervoltage_now", "severity": "critical", "detail": {"events": 96}}])
        self.assertIn("供电", text)
        self.assertIn("critical", text)
        self.assertIn("96", text)
        self.assertNotIn("undervoltage_now", text)


class SustainedAlertTests(unittest.TestCase):
    """Starting a container on a memory-tight box pages a batch back in. That burst is real
    but is the cost of the operation, not something an operator can act on — and reporting it
    is how an indicator earns the reputation that makes people ignore a genuine outage."""

    def test_a_one_sample_spike_is_not_reported(self):
        streaks = {}
        spike = [{"code": "swap_pressure", "severity": "warning", "detail": {}}]
        self.assertEqual(main._sustained_alerts(spike, streaks), [])

    def test_a_rate_that_holds_is_reported(self):
        streaks = {}
        spike = [{"code": "swap_pressure", "severity": "warning", "detail": {}}]
        for _ in range(main.SUSTAINED_ALERT_SAMPLES - 1):
            self.assertEqual(main._sustained_alerts(spike, streaks), [])
        kept = main._sustained_alerts(spike, streaks)
        self.assertEqual([x["code"] for x in kept], ["swap_pressure"])
        self.assertEqual(kept[0]["detail"]["samples"], main.SUSTAINED_ALERT_SAMPLES)

    def test_a_gap_restarts_the_count(self):
        streaks = {}
        spike = [{"code": "swap_pressure", "severity": "warning", "detail": {}}]
        main._sustained_alerts(spike, streaks)
        main._sustained_alerts([], streaks)          # subsided
        self.assertEqual(main._sustained_alerts(spike, streaks), [])

    def test_conditions_that_are_instantaneous_are_reported_at_once(self):
        # A brown-out lasts seconds; waiting three minutes would simply miss it.
        streaks = {}
        alerts = [{"code": "undervoltage_now", "severity": "critical", "detail": {}}]
        self.assertEqual(main._sustained_alerts(alerts, streaks), alerts)


class ImeiSourceFollowsReaderTests(unittest.TestCase):
    """A reader that moves to a new USB port derives a new id; lines naming the old one as
    their IMEI source are left pointing at a device that no longer exists."""

    def _run(self, instances):
        saved = []
        with patch.object(main.cfg, "list_instances", return_value=instances), \
                patch.object(main.cfg, "upsert_instance", side_effect=saved.append):
            followed = main._follow_imei_source("reader-old", "reader-new")
        return followed, saved

    def test_the_line_naming_the_retired_id_is_repointed(self):
        followed, saved = self._run([{"id": "5", "imei_source_device_id": "reader-old"}])
        self.assertEqual(followed, ["5"])
        self.assertEqual(saved, [{"id": "5", "imei_source_device_id": "reader-new"}])

    def test_lines_naming_another_device_are_untouched(self):
        followed, saved = self._run([
            {"id": "1", "imei_source_device_id": "2c7c-0125-1-1.4.4"},
            {"id": "2", "imei_source_device_id": ""},
            {"id": "3"},
        ])
        self.assertEqual(followed, [])
        self.assertEqual(saved, [])

    def test_every_line_sharing_the_reader_follows_it(self):
        followed, _ = self._run([{"id": "3", "imei_source_device_id": "reader-old"},
                                 {"id": "4", "imei_source_device_id": "reader-old"}])
        self.assertEqual(followed, ["3", "4"])

    def test_the_marker_is_never_cleared(self):
        # An empty marker would let the one-time legacy migration run a second time and
        # overwrite the reader record from whichever SIM happens to be inserted.
        _, saved = self._run([{"id": "5", "imei_source_device_id": "reader-old"}])
        self.assertTrue(all(item["imei_source_device_id"] for item in saved))


class OutageDetailTests(unittest.TestCase):
    """The outage record must name the evidence, not just the verdict."""

    def test_a_dns_failure_names_the_domain_and_the_resolvers(self):
        st = {"reason_code": "epdg_unresolved",
              "detail": {"epdg_fqdn": "epdg.epc.mnc260.mcc310.pub.3gppnetwork.org",
                         "nameservers": ["223.5.5.5", "119.29.29.29"]}}
        evidence = __import__("json").loads(main._outage_detail(st))
        self.assertEqual(evidence, {
            "code": "client_dns_unresolved",
            "peer": "epdg.epc.mnc260.mcc310.pub.3gppnetwork.org",
            "servers": ["223.5.5.5", "119.29.29.29"],
        })

    def test_a_tunnel_failure_names_the_epdg(self):
        st = {"reason_code": "tunnel_network",
              "detail": {"epdg_fqdn": "epdg.epc.mnc010.mcc234.pub.3gppnetwork.org"}}
        evidence = __import__("json").loads(main._outage_detail(st))
        self.assertEqual(evidence["code"], "server_epdg_ike_unanswered")
        self.assertEqual(evidence["peer"], "epdg.epc.mnc010.mcc234.pub.3gppnetwork.org")

    def test_a_registration_failure_names_the_pcscf(self):
        st = {"reason_code": "reg_rejected",
              "detail": {"pcscf": "fd00:976a:2:153::5", "registration": "Rejected",
                         "sip_status": 403}}
        evidence = __import__("json").loads(main._outage_detail(st))
        self.assertEqual(evidence, {"code": "server_pcscf_sip_rejected",
                                    "peer": "fd00:976a:2:153::5", "status": 403})

    def test_a_temporary_registration_failure_keeps_the_retry_schedule(self):
        st = {"reason_code": "reg_temporary",
              "detail": {"pcscf": "fd00:976a:2:153::5", "registration": "Rejected",
                         "sip_status": 503, "retry_after_seconds": 300}}
        evidence = __import__("json").loads(main._outage_detail(st))
        self.assertEqual(evidence, {"code": "server_pcscf_sip_temporary",
                                    "peer": "fd00:976a:2:153::5", "status": 503,
                                    "retry_after": 300})

    def test_child_rekey_timeout_names_server_request_and_peer(self):
        st = {"reason_code": "tunnel_child_rekey_timeout",
              "detail": {"epdg_fqdn": "epdg.example"}}
        evidence = __import__("json").loads(main._outage_detail(st))
        self.assertEqual(evidence, {"code": "server_epdg_child_rekey_unanswered",
                                    "peer": "epdg.example"})

    def test_tunnel_setup_does_not_call_recovery_the_outage_cause(self):
        st = {"reason_code": "tunnel_setup",
              "detail": {"epdg_fqdn": "epdg.example"}}
        evidence = __import__("json").loads(main._outage_detail(st))
        self.assertEqual(evidence, {"code": "tunnel_cause_not_captured",
                                    "peer": "epdg.example"})

    def test_codes_without_useful_evidence_stay_quiet(self):
        self.assertEqual(main._outage_detail({"reason_code": "no_card", "detail": {}}), "")


class ExitFailoverWiringTests(unittest.IsolatedAsyncioTestCase):
    """The policy is only worth its tests if it is actually consulted on the freeze path,
    and if giving up really stops the rebuild instead of merely saying so."""

    EXITS = {"exits": {"us": {"node": "node-a", "candidates": ["node-a", "node-b"],
                              "selection": "auto"}}}
    INST = {"id": "9", "enabled": True, "mcc": "310", "mnc": "240", "name": "test"}

    def setUp(self):
        main.hub.exit_ledgers.pop("9", None)
        main.hub.reset_health("9")
        self.enterContext(known_health_rebuild_inputs("9"))

    def tearDown(self):
        main.hub.exit_ledgers.pop("9", None)
        main.hub.reset_health("9")

    def _runtime(self, generation="generation-9"):
        return patch.object(main.hub.runtime, "get", new=AsyncMock(return_value={
            "running": True, "container_id": generation, "started_at": HEALTH_STARTED_AT,
        }))

    def _plan(self, action):
        return patch.object(main, "_plan_exit_failure", return_value={
            "action": action, "ledger": {}, "country": "us", "node": "node-a",
            "candidates": ["node-a", "node-b"], "pinned": False,
            "peer_registered": False, "swu": "DOWN", "retransmits": 0,
            "verdict": main.failover.BLAMES_EXIT, "was_backing_off": False,
        })

    def _judge(self, swu, retransmits, exits=None, stable_for=0.0, peers=()):
        st = {"reason_code": "tunnel_network", "reason": "x"}
        with patch.object(main.egress, "line_country", return_value="us"), \
                patch.object(main.egress, "status", return_value=exits or self.EXITS), \
                patch.object(main.cfg, "list_instances", return_value=list(peers)), \
                patch.object(main.engine, "read_run_json", return_value={"state": swu}), \
                patch.object(main.engine, "ike_evidence",
                             return_value={"retransmits": retransmits}), \
                patch.object(main, "_save_exit_ledgers"), \
                patch.object(main.egress, "request_reselect") as reselect, \
                patch.object(main.asyncio, "to_thread", new=AsyncMock()) as to_thread:
            action = main._judge_exit_failure("9", self.INST, st, stable_for)
        # dispatch is handed to to_thread(), which is called synchronously to build the
        # awaitable — so its arguments are visible without waiting for the task to run.
        return action, reselect, to_thread

    async def test_a_healthy_tunnel_neither_moves_the_exit_nor_notifies(self):
        action, reselect, to_thread = self._judge("CONNECTED", 0)
        self.assertEqual(action, main.failover.HOLD)
        reselect.assert_not_called()
        to_thread.assert_not_called()

    async def test_a_fresh_engine_never_reports_established_tunnel_samples(self):
        for _ in range(main.failover.FAILURES_BEFORE_REPORT + 2):
            action, reselect, to_thread = self._judge(
                "CONNECTED", 2, stable_for=0.0)
            self.assertEqual(action, main.failover.HOLD)
            reselect.assert_not_called()
            to_thread.assert_not_called()
        self.assertEqual(main.hub.exit_ledgers["9"]["failures"], 0)
        self.assertFalse(main.hub.exit_ledgers["9"]["reported"])

    async def test_unreadable_tunnel_evidence_is_read_only_and_never_invents_zero(self):
        main.hub.exit_ledgers["9"] = {
            **main.failover.blank_ledger(), "node": "old-node",
            "failures": main.failover.FAILURES_BEFORE_REPORT - 1,
        }
        peer = {**self.INST, "id": "10"}
        exits = {"exits": {"us": {"node": "current-node",
                                    "candidates": ["current-node"],
                                    "selection": "auto"}}}
        with patch.object(main.egress, "line_country", return_value="us"), \
                patch.object(main.egress, "status", return_value=exits), \
                patch.object(main.cfg, "list_instances", return_value=[peer]), \
                patch.object(main.hub, "status_cache", {"10": {"state": "OK"}}), \
                patch.object(main.engine, "read_run_json",
                             side_effect=RuntimeError("unreadable")), \
                patch.object(main, "_save_exit_ledgers"), \
                patch.object(main.asyncio, "to_thread", new=AsyncMock()) as to_thread:
            action = main._judge_exit_failure(
                "9", self.INST, {"reason_code": "tunnel_network"}, 30.0)

        self.assertEqual(action, main.failover.HOLD)
        self.assertEqual(main.hub.exit_ledgers["9"]["failures"],
                         main.failover.FAILURES_BEFORE_REPORT - 1)
        to_thread.assert_not_called()

    async def test_the_exit_moves_once_the_node_has_had_its_chances(self):
        for _ in range(main.failover.STRIKES_PER_NODE - 1):
            action, reselect, _ = self._judge("CONNECTING", 14)
            self.assertEqual(action, main.failover.HOLD)
            reselect.assert_not_called()
        action, reselect, _ = self._judge("CONNECTING", 14)
        self.assertEqual(action, main.failover.SWITCH)
        reselect.assert_called_once()

    async def test_an_exhausted_pool_notifies_once_and_backs_off(self):
        seen = []
        for node in ("node-a", "node-b"):
            exits = {"exits": {"us": {"node": node, "candidates": ["node-a", "node-b"],
                                      "selection": "auto"}}}
            for _ in range(main.failover.STRIKES_PER_NODE):
                action, _reselect, to_thread = self._judge("DOWN", 0, exits)
                seen.append((action, to_thread))
        action, to_thread = seen[-1]
        self.assertEqual(action, main.failover.BACK_OFF)
        self.assertEqual(to_thread.call_args[0][0], main.notify_push.dispatch)
        self.assertEqual(to_thread.call_args[0][2], main.notify_push.EV_LINE_UNRECOVERABLE)
        self.assertTrue(main.hub.exit_ledgers["9"]["exhausted"])
        # The slow retries that follow keep backing off without announcing again.
        action, _reselect, to_thread = self._judge("DOWN", 0)
        self.assertEqual(action, main.failover.BACK_OFF)
        to_thread.assert_not_called()

    async def test_a_registered_sibling_keeps_the_exit_where_it_is(self):
        peer = {"id": "7", "enabled": True, "mcc": "310", "mnc": "260", "name": "peer"}
        main.hub.status_cache["7"] = {"state": "OK"}
        actions = []
        try:
            for _ in range(main.failover.FAILURES_BEFORE_REPORT):
                action, reselect, _ = self._judge("DOWN", 0, peers=[peer])
                actions.append(action)
                reselect.assert_not_called()
        finally:
            main.hub.status_cache.pop("7", None)
        # Never a switch — but the operator is still told, once, that the line is stuck.
        self.assertNotIn(main.failover.SWITCH, actions)
        self.assertEqual(actions[-1], main.failover.REPORT)

    async def test_giving_up_stops_the_automatic_rebuild(self):
        h = main.hub.health_for("9")
        h["fail_start"] = main.time.monotonic() - 10_000    # past the retry budget
        st = {"state": "TUNNEL_DOWN", "label": "x", "reason_code": "tunnel_network",
              "reason": "x", "detail": {"active_channels": 0}}
        with self._runtime(), self._plan(main.failover.GIVE_UP), \
                patch.object(main.cfg, "get_instance", return_value=self.INST), \
                patch.object(main, "_commit_exit_failure_plan",
                             return_value=main.failover.GIVE_UP), \
                patch.object(main.engine, "capture_and_stop_if_idle",
                             return_value={"status": "stopped", "stopped": True}), \
                patch.object(main.cfg, "get_settings", return_value={}):
            await main._apply_health_with_recovery(
                "9", self.INST, st, "generation-9", sampled_started_at=HEALTH_STARTED_AT)
        # None reads as "never" in the recovery check, the same mechanism a blocked PIN uses.
        self.assertIsNone(h["next_retry_at"])
        self.assertEqual(h["frozen_code"], "tunnel_network")

    async def test_a_failed_manual_retry_stays_stopped_while_pin_is_still_locked(self):
        h = main.hub.health_for("9")
        h["fail_start"] = main.time.monotonic() - 10_000
        main.hub.exit_ledgers["9"] = {
            "node": "node-a", "strikes": 3, "tried": ["node-a"],
            "failures": 3, "given_up": True, "reported": True,
        }
        st = {"state": "TUNNEL_DOWN", "label": "x", "reason_code": "tunnel_network",
              "reason": "x", "detail": {"active_channels": 0}}
        with self._runtime(), self._plan(main.failover.HOLD), \
                patch.object(main.cfg, "get_instance", return_value=self.INST), \
                patch.object(main, "_commit_exit_failure_plan",
                             return_value=main.failover.HOLD), \
                patch.object(main.engine, "capture_and_stop_if_idle") as capture, \
                patch.object(main.cfg, "get_settings", return_value={}):
            result = await main._apply_health_with_recovery(
                "9", self.INST, st, "generation-9", sampled_started_at=HEALTH_STARTED_AT)
        self.assertIsNone(h["next_retry_at"])
        self.assertEqual(result["detail"]["recovery_mode"], "in_place")
        capture.assert_not_called()
        main.hub.exit_ledgers.pop("9", None)

    async def test_backing_off_slows_the_rebuild_instead_of_stopping_it(self):
        h = main.hub.health_for("9")
        h["fail_start"] = main.time.monotonic() - 10_000
        st = {"state": "TUNNEL_DOWN", "label": "x", "reason_code": "tunnel_network",
              "reason": "x", "detail": {"active_channels": 0}}
        with self._runtime(), self._plan(main.failover.BACK_OFF), \
                patch.object(main.cfg, "get_instance", return_value=self.INST), \
                patch.object(main, "_commit_exit_failure_plan",
                             return_value=main.failover.BACK_OFF), \
                patch.object(main.engine, "capture_and_stop_if_idle",
                             return_value={"status": "stopped", "stopped": True}), \
                patch.object(main.cfg, "get_settings", return_value={}):
            await main._apply_health_with_recovery(
                "9", self.INST, st, "generation-9", sampled_started_at=HEALTH_STARTED_AT)
        # An hour, not the ordinary cooldown: the line still retries by itself, but at a
        # pace that stops the churn while whatever broke every exit at once passes.
        remaining = h["next_retry_at"] - main.time.monotonic()
        self.assertGreater(remaining, main.failover.EXHAUSTED_RETRY_SECONDS * 0.9)

    async def test_hold_keeps_generation_and_restarts_observation_budget(self):
        h = main.hub.health_for("9")
        h["fail_start"] = main.time.monotonic() - 10_000
        st = {"state": "TUNNEL_DOWN", "label": "x", "reason_code": "tunnel_network",
              "reason": "x", "detail": {"active_channels": 0}}
        with self._runtime(), self._plan(main.failover.HOLD), \
                patch.object(main.cfg, "get_instance", return_value=self.INST), \
                patch.object(main, "_commit_exit_failure_plan",
                             return_value=main.failover.HOLD), \
                patch.object(main.engine, "capture_and_stop_if_idle") as capture, \
                patch.object(main.cfg, "get_settings", return_value={}):
            result = await main._apply_health_with_recovery(
                "9", self.INST, st, "generation-9", sampled_started_at=HEALTH_STARTED_AT)
        self.assertIsNone(h["next_retry_at"])
        self.assertIsNone(h["fail_start"])
        self.assertEqual(result["detail"]["recovery_action"], main.failover.HOLD)
        capture.assert_not_called()

    def test_hold_commit_log_says_engine_is_preserved_not_frozen(self):
        plan = {
            "action": main.failover.HOLD, "ledger": {}, "country": "gb",
            "node": "node-a", "candidates": ["node-a"], "pinned": False,
            "peer_registered": False, "swu": "CONNECTED", "retransmits": 0,
            "verdict": "ambiguous", "was_backing_off": False,
        }
        with patch.object(main, "_save_exit_ledgers"), \
                self.assertLogs(main.log, level="INFO") as captured:
            action = main._commit_exit_failure_plan(
                "9", self.INST, {"reason_code": "tunnel_network"}, 30, plan,
                engine_retained=True)
        message = "\n".join(captured.output).casefold()
        self.assertEqual(action, main.failover.HOLD)
        self.assertIn("kept the current engine", message)
        self.assertNotIn("froze", message)
        main.hub.exit_ledgers.pop("9", None)

    async def test_exit_judgement_exception_keeps_generation_and_ledger(self):
        h = main.hub.health_for("9")
        h["fail_start"] = main.time.monotonic() - 10_000
        main.hub.exit_ledgers["9"] = {"failures": 2, "node": "node-a"}
        original = dict(main.hub.exit_ledgers["9"])
        st = {"state": "TUNNEL_DOWN", "label": "x", "reason_code": "tunnel_network",
              "reason": "x", "detail": {"active_channels": 0}}
        with self._runtime(), \
                patch.object(main.cfg, "get_instance", return_value=self.INST), \
                patch.object(main, "_plan_exit_failure", side_effect=RuntimeError("bad")), \
                patch.object(main, "_commit_exit_failure_plan") as commit, \
                patch.object(main.engine, "capture_and_stop_if_idle") as capture:
            result = await main._apply_health_with_recovery(
                "9", self.INST, st, "generation-9", sampled_started_at=HEALTH_STARTED_AT)
        self.assertEqual(result["detail"]["recovery_action"], "exit_judgement_failed")
        self.assertEqual(main.hub.exit_ledgers["9"], original)
        capture.assert_not_called()
        commit.assert_not_called()

    async def test_report_and_pace_both_keep_the_same_engine(self):
        for action in (main.failover.REPORT, main.failover.PACE):
            with self.subTest(action=action):
                main.hub.reset_health("9")
                h = main.hub.health_for("9")
                h["fail_start"] = main.time.monotonic() - 10_000
                st = {"state": "TUNNEL_DOWN", "label": "x",
                      "reason_code": "tunnel_network", "reason": "x",
                      "detail": {"active_channels": 0}}
                with self._runtime(), self._plan(action), \
                        patch.object(main.cfg, "get_instance", return_value=self.INST), \
                        patch.object(main, "_commit_exit_failure_plan",
                                     return_value=action), \
                        patch.object(main.engine, "capture_and_stop_if_idle") as capture, \
                        patch.object(main.cfg, "get_settings", return_value={}):
                    result = await main._apply_health_with_recovery(
                        "9", self.INST, st, "generation-9", sampled_started_at=HEALTH_STARTED_AT)
                self.assertNotIn("frozen", result)
                self.assertIsNone(h["next_retry_at"])
                self.assertEqual(result["detail"]["recovery_mode"], "in_place")
                capture.assert_not_called()

    async def test_active_or_unknown_call_state_never_removes_the_engine(self):
        for channels, reason in ((1, "active_call"), (None, "call_state_unknown")):
            with self.subTest(active_channels=channels):
                main.hub.reset_health("9")
                h = main.hub.health_for("9")
                h["fail_start"] = main.time.monotonic() - 10_000
                st = {"state": "TUNNEL_DOWN", "label": "x",
                      "reason_code": "tunnel_network", "reason": "x",
                      "detail": {"active_channels": channels}}
                with patch.object(main.engine, "capture_and_stop_if_idle") as capture, \
                        patch.object(main, "_judge_exit_failure") as judge, \
                        patch.object(main.cfg, "get_settings", return_value={}):
                    result = main.apply_health("9", self.INST, st, "generation-1")
                self.assertNotIn("frozen", result)
                self.assertEqual(result["detail"]["recovery_blocked"], reason)
                capture.assert_not_called()
                judge.assert_not_called()

    async def test_generation_change_during_capture_never_freezes_or_rebuilds(self):
        main.hub.reset_health("9")
        h = main.hub.health_for("9")
        h["fail_start"] = main.time.monotonic() - 10_000
        st = {"state": "TUNNEL_DOWN", "label": "x", "reason_code": "tunnel_network",
              "reason": "x", "detail": {"active_channels": 0}}
        with self._runtime("old-generation"), \
                patch.object(main.cfg, "get_instance", return_value=self.INST), \
                self._plan(main.failover.SWITCH), \
                patch.object(main.engine, "capture_and_stop_if_idle", return_value={
                "status": "generation_changed", "stopped": False}), \
                patch.object(main, "_commit_exit_failure_plan") as commit:
            result = await main._apply_health_with_recovery(
                "9", self.INST, st, "old-generation", sampled_started_at=HEALTH_STARTED_AT)
        self.assertNotIn("frozen", result)
        self.assertEqual(result["detail"]["recovery_blocked"], "generation_changed")
        self.assertIsNone(h["next_retry_at"])
        commit.assert_not_called()

    async def test_failed_recovery_is_rate_limited_across_status_polls(self):
        main.hub.reset_health("9")
        h = main.hub.health_for("9")
        h["fail_start"] = main.time.monotonic() - 10_000
        st = {"state": "TUNNEL_DOWN", "label": "x", "reason_code": "tunnel_network",
              "reason": "x", "detail": {"active_channels": 0}}
        with self._runtime(), self._plan(main.failover.SWITCH), \
                patch.object(main.cfg, "get_instance", return_value=self.INST), \
                patch.object(main.engine, "capture_and_stop_if_idle", return_value={
                "status": "quiesce_restart_race", "stopped": False}) as capture, \
                patch.object(main.cfg, "get_settings", return_value={}):
            first = await main._apply_health_with_recovery(
                "9", self.INST, st, "generation-9", sampled_started_at=HEALTH_STARTED_AT)
            second = await main._apply_health_with_recovery(
                "9", self.INST, st, "generation-9", sampled_started_at=HEALTH_STARTED_AT)
            self.assertEqual(capture.call_count, 1)
            self.assertEqual(first["detail"]["recovery_blocked"],
                             "quiesce_restart_race")
            self.assertEqual(second["detail"]["recovery_blocked"],
                             "quiesce_restart_race")

            h["recovery_blocked_until"] = main.time.monotonic() - 1
            await main._apply_health_with_recovery(
                "9", self.INST, st, "generation-9", sampled_started_at=HEALTH_STARTED_AT)
            self.assertEqual(capture.call_count, 2)

    async def test_new_generation_clears_failed_recovery_gate(self):
        main.hub.reset_health("9")
        h = main.hub.health_for("9")
        h.update({
            "fail_start": main.time.monotonic() - 10_000,
            "recovery_blocked_generation": "old-generation",
            "recovery_blocked_until": main.time.monotonic() + 3600,
            "recovery_blocked_reason": "quiesce_restart_race",
        })
        st = {"state": "TUNNEL_DOWN", "label": "x", "reason_code": "tunnel_network",
              "reason": "x", "detail": {"active_channels": 0}}
        with self._runtime("new-generation"), self._plan(main.failover.SWITCH), \
                patch.object(main.cfg, "get_instance", return_value=self.INST), \
                patch.object(main.engine, "capture_and_stop_if_idle", return_value={
                "status": "generation_changed", "stopped": False}) as capture, \
                patch.object(main.cfg, "get_settings", return_value={}):
            await main._apply_health_with_recovery(
                "9", self.INST, st, "new-generation", sampled_started_at=HEALTH_STARTED_AT)
        capture.assert_called_once()
        self.assertEqual(h["recovery_blocked_generation"], "new-generation")

    async def test_healthy_sample_clears_failed_recovery_gate(self):
        main.hub.reset_health("9")
        h = main.hub.health_for("9")
        h.update({
            "recovery_blocked_generation": "generation-9",
            "recovery_blocked_until": main.time.monotonic() + 3600,
            "recovery_blocked_reason": "quiesce_restart_race",
        })
        result = main.apply_health("9", self.INST, {
            "state": "OK", "label": "ok", "reason_code": "registered",
            "reason": "", "detail": {}}, "generation-9")
        self.assertEqual(result["state"], "OK")
        self.assertIsNone(main.hub.health_for("9")["recovery_blocked_generation"])

    async def test_failed_hourly_probe_keeps_hourly_cadence(self):
        main.hub.reset_health("9")
        h = main.hub.health_for("9")
        h.update({"frozen_code": "registering", "frozen_reason": "stuck",
                  "retry_delay": main.failover.EXHAUSTED_RETRY_SECONDS,
                  "auto_retrying": True})
        with patch.object(main, "_line_auto_start_allowed", return_value=(True, "")), \
                patch.object(main.cfg, "get_instance", return_value=self.INST), \
                patch.object(main.hub.runtime, "get", new=AsyncMock(return_value={
                    "running": False, "container_id": None})), \
                patch.object(main, "_start_engine_checked", side_effect=RuntimeError("failed")), \
                patch.object(main.hub, "broadcast", new=AsyncMock()):
            await main._auto_recover_instance(
                "9", self.INST, int(main.failover.EXHAUSTED_RETRY_SECONDS))
        remaining = h["next_retry_at"] - main.time.monotonic()
        self.assertGreater(remaining, main.failover.EXHAUSTED_RETRY_SECONDS * 0.9)

    async def test_call_admission_waits_for_the_recovery_gate(self):
        ami = AsyncMock()
        ami.originate.return_value = {"ok": True}
        lock = main.hub.recovery_lock("9")
        await lock.acquire()
        try:
            with patch.object(main, "_browser_call_recovery_global_pending", False), \
                    patch.object(main.hub, "ami_for", new=AsyncMock(return_value=ami)), \
                    patch.object(main.store, "add_call"):
                task = __import__("asyncio").create_task(
                    main.place_call_on_line("9", "12345"))
                await __import__("asyncio").sleep(0)
                ami.originate.assert_not_awaited()
                lock.release()
                result = await task
        finally:
            if lock.locked():
                lock.release()
        self.assertTrue(result["ok"])
        ami.originate.assert_awaited_once()

    async def test_legacy_rest_originate_is_fail_closed_without_media_admission(self):
        with patch.object(main, "place_call_on_line", new=AsyncMock()) as originate:
            with self.assertRaises(main.HTTPException) as raised:
                await main.api_call("9", {"to": "+44123"})
        self.assertEqual(raised.exception.status_code, 409)
        originate.assert_not_awaited()

    def test_softphone_upstream_uses_exact_engine_bridge_address(self):
        self.assertEqual(
            main._softphone_upstream_url({"ip": "172.17.0.9"}),
            "wss://172.17.0.9:8089/ws")
        self.assertEqual(
            main._softphone_upstream_url({"ip": "fd00::9"}),
            "wss://[fd00::9]:8089/ws")
        with self.assertRaisesRegex(ValueError, "exact Engine bridge address"):
            main._softphone_upstream_url({"ip": "browser-controlled.invalid"})

    async def test_softphone_invite_is_fenced_but_bye_can_drain(self):
        class _Browser:
            def __init__(self, frames):
                self.frames = iter(frames)
                self.closed = None

            async def receive(self):
                try:
                    return {"type": "websocket.receive", "text": next(self.frames)}
                except StopIteration as exc:
                    raise RuntimeError("done") from exc

            async def close(self, **kwargs):
                self.closed = kwargs

        upstream = AsyncMock()
        main.hub.engine_recovering.add("9")
        try:
            blocked = _Browser(["INVITE sip:123@example SIP/2.0\r\n"])
            await main._forward_softphone_client(blocked, upstream, "9")
            upstream.send.assert_not_awaited()
            self.assertEqual(blocked.closed["code"], 4412)

            draining = _Browser(["BYE sip:123@example SIP/2.0\r\n"])
            await main._forward_softphone_client(draining, upstream, "9")
            upstream.send.assert_awaited_once()

            reinvite = _Browser([
                "INVITE sip:123@example SIP/2.0\r\n"
                "To: <sip:123@example>;tag=existing-dialog\r\n"
                "From: <sip:web@example>;tag=browser\r\n\r\n"])
            await main._forward_softphone_client(reinvite, upstream, "9")
            self.assertEqual(upstream.send.await_count, 2)
            self.assertIsNone(reinvite.closed)
        finally:
            main.hub.engine_recovering.discard("9")

    async def test_numeric_softphone_invite_requires_fresh_one_shot_media_proof(self):
        class _Browser:
            def __init__(self, frame):
                self.frame = frame
                self.sent = False
                self.closed = None

            async def receive(self):
                if self.sent:
                    raise RuntimeError("done")
                self.sent = True
                return {"type": "websocket.receive", "text": self.frame}

            async def close(self, **kwargs):
                self.closed = kwargs

        evidence = {
            "connection_state": "connected", "local_track_live": True,
            "remote_track_live": True, "playback_started": True,
            "outbound_packets_delta": 1, "outbound_bytes_delta": 160,
            "inbound_packets_delta": 1, "inbound_bytes_delta": 160,
        }
        registry = MediaAdmissionRegistry()
        token = registry.issue("9", "generation", "route-a")
        header = f"X-MDD-Media-Token: {token}\r\n"
        upstream = AsyncMock()
        with patch.object(main, "_browser_call_recovery_global_pending", False), \
                patch.object(main, "media_admission", registry), \
                patch.object(main.media_ingress, "binding_id", return_value="route-a"):
            native_blocked = _Browser("INVITE sip:+4412345@example SIP/2.0\r\n\r\n")
            await main._forward_softphone_client(
                native_blocked, upstream, "9", "generation", "ws-a", "route-a",
                native_outbound=True)
            self.assertEqual(native_blocked.closed["code"], 4416)
            upstream.send.assert_not_awaited()

            direct = _Browser("INVITE sip:123@example SIP/2.0\r\n\r\n")
            await main._forward_softphone_client(
                direct, upstream, "9", "generation", "ws-a", "route-a")
            self.assertEqual(direct.closed["code"], 4413)
            upstream.send.assert_not_awaited()

            canary = _Browser(
                f"INVITE sip:mdd-media-check@example SIP/2.0\r\n{header}\r\n")
            await main._forward_softphone_client(
                canary, upstream, "9", "generation", "ws-a", "route-a")
            self.assertEqual(upstream.send.await_count, 1)
            self.assertTrue(registry.mark_engine(token, "9", "generation"))
            self.assertTrue(registry.mark_browser(token, "9", "generation", evidence))

            carrier = _Browser(
                f"INVITE sip:+44123@example SIP/2.0\r\nCall-ID: call-a\r\n"
                f"From: <sip:web@example>;tag=from-a\r\n{header}\r\n")
            carrier.frame = carrier.frame.replace(
                "From:", "Via: SIP/2.0/WSS host;branch=z9hG4bK-a\r\n"
                "CSeq: 1 INVITE\r\nFrom:")
            await main._forward_softphone_client(
                carrier, upstream, "9", "generation", "ws-a", "route-a")
            self.assertEqual(upstream.send.await_count, 2)
            transaction = main._sip_initial_invite_admission(carrier.frame)[2]
            self.assertTrue(registry.observe_invite_response(
                "ws-a", transaction, 1, 401))

            digest_retry = _Browser(
                f"INVITE sip:+44123@example SIP/2.0\r\nCall-ID: call-a\r\n"
                f"From: <sip:web@example>;tag=from-a\r\n"
                f"Authorization: Digest response=retry\r\n{header}\r\n")
            digest_retry.frame = digest_retry.frame.replace(
                "From:", "Via: SIP/2.0/WSS host;branch=z9hG4bK-b\r\n"
                "CSeq: 2 INVITE\r\nFrom:")
            await main._forward_softphone_client(
                digest_retry, upstream, "9", "generation", "ws-a", "route-a")
            self.assertEqual(upstream.send.await_count, 3)

            replay = _Browser(
                f"INVITE sip:+44123@example SIP/2.0\r\nCall-ID: call-a\r\n"
                f"From: <sip:web@example>;tag=from-b\r\n{header}\r\n")
            await main._forward_softphone_client(
                replay, upstream, "9", "generation", "ws-a", "route-a")
            self.assertEqual(replay.closed["code"], 4413)
            self.assertEqual(upstream.send.await_count, 3)

    async def test_engine_media_proof_requires_exact_channel_bidirectional_rtp(self):
        registry = MediaAdmissionRegistry()
        token = registry.issue("9", "generation", "route-a")
        self.assertTrue(registry.claim_canary(
            token, "9", "generation", "ws-a", "route-a"))
        ami = SimpleNamespace(channel_rtp_counts=AsyncMock(side_effect=[
            {"tx_packets": 3, "rx_packets": 0},
            {"tx_packets": 5, "rx_packets": 4},
        ]))
        with patch.object(main, "media_admission", registry), \
                patch.object(main.hub, "ami_for", new=AsyncMock(return_value=ami)), \
                patch.object(main.hub.runtime, "get", new=AsyncMock(return_value={
                    "running": True, "container_id": "generation"})), \
                patch.object(main.asyncio, "sleep", new=AsyncMock()):
            await main._prove_engine_media_canary(
                "9", token, "generation", "run:171.9")
        self.assertTrue(registry.status(token, "9", "generation")["engine_proven"])
        self.assertEqual(ami.channel_rtp_counts.await_count, 2)

    async def test_dead_softphone_ws_targets_only_its_authorized_call_after_grace(self):
        token = "token-abcdefghijklmnopqrstuvwxyz123456"
        ami = SimpleNamespace(hangup_channel=AsyncMock(return_value=True))
        with patch.object(main.hub, "ami_for", new=AsyncMock(return_value=ami)), \
                patch.object(main.hub.runtime, "get", new=AsyncMock(return_value={
                    "running": True, "container_id": "generation"})), \
                patch.object(main.asyncio, "sleep", new=AsyncMock()) as sleep:
            await main._terminate_disconnected_softphone_calls("9", "generation", [{
                "token": token, "generation": "generation",
                "source_call_id": "171234.1"}])
        sleep.assert_awaited_once_with(10.0)
        ami.hangup_channel.assert_awaited_once_with("171234.1")

    async def test_softphone_call_lease_renews_only_exact_live_generation(self):
        registry = MediaAdmissionRegistry()
        token = registry.issue("9", "generation", "route")
        evidence = {
            "connection_state": "connected", "local_track_live": True,
            "remote_track_live": True, "playback_started": True,
            "outbound_packets_delta": 1, "outbound_bytes_delta": 160,
            "inbound_packets_delta": 1, "inbound_bytes_delta": 160,
        }
        self.assertTrue(registry.claim_canary(
            token, "9", "generation", "ws", "route"))
        self.assertTrue(registry.mark_engine(token, "9", "generation"))
        self.assertTrue(registry.mark_browser(token, "9", "generation", evidence))
        self.assertTrue(registry.authorize_invite(
            token, "9", "generation", "ws", "dialog", "+44123"))
        self.assertTrue(registry.bind_channel(token, "9", "generation", "171.9"))
        ami = SimpleNamespace(renew_channel_absolute_timeout=AsyncMock(return_value=True))
        async def end_after_one_tick(_seconds):
            registry.close_call(token, "9", "171.9")
        with patch.object(main, "media_admission", registry), \
                patch.object(main.hub, "ami_for", new=AsyncMock(return_value=ami)), \
                patch.object(main.hub.runtime, "get", new=AsyncMock(return_value={
                    "running": True, "container_id": "generation"})), \
                patch.object(main.asyncio, "sleep", new=AsyncMock(
                    side_effect=end_after_one_tick)):
            await main._renew_softphone_call_lease(
                token, "9", "generation", "171.9")
        ami.renew_channel_absolute_timeout.assert_awaited_once_with("171.9", 10)

    async def test_softphone_call_lease_stops_after_bounded_missing_channel_samples(self):
        registry = MediaAdmissionRegistry()
        token = registry.issue("9", "generation", "route")
        evidence = {
            "connection_state": "connected", "local_track_live": True,
            "remote_track_live": True, "playback_started": True,
            "outbound_packets_delta": 1, "outbound_bytes_delta": 160,
            "inbound_packets_delta": 1, "inbound_bytes_delta": 160,
        }
        self.assertTrue(registry.claim_canary(
            token, "9", "generation", "ws", "route"))
        self.assertTrue(registry.mark_engine(token, "9", "generation"))
        self.assertTrue(registry.mark_browser(token, "9", "generation", evidence))
        self.assertTrue(registry.authorize_invite(
            token, "9", "generation", "ws", "dialog", "+44123"))
        self.assertTrue(registry.bind_channel(token, "9", "generation", "171.9"))
        ami = SimpleNamespace(renew_channel_absolute_timeout=AsyncMock(return_value=False))
        with patch.object(main, "media_admission", registry), \
                patch.object(main.hub, "ami_for", new=AsyncMock(return_value=ami)), \
                patch.object(main.hub.runtime, "get", new=AsyncMock(return_value={
                    "running": True, "container_id": "generation"})), \
                patch.object(main.asyncio, "sleep", new=AsyncMock()):
            await main._renew_softphone_call_lease(
                token, "9", "generation", "171.9")
        self.assertEqual(ami.renew_channel_absolute_timeout.await_count, 3)
        self.assertFalse(registry.authorization_active(
            token, "9", "generation", "171.9"))

    async def test_native_call_lease_stops_and_hangs_up_on_stale_pcm_evidence(self):
        registry = MediaAdmissionRegistry()
        token = registry.issue("9", "generation", "native-wss-v1")
        self.assertTrue(registry.authorize_native(
            token, "9", "generation", "owner", "+44123", "171.9"))
        native_session = SimpleNamespace(
            iid="9", generation="generation", channel_id="171.9",
            session_id="session-9", operation_id="operation-9", media_epoch="epoch-9",
            phase="active",
            engine_run_id="run-9", browser_ws=object(), asterisk_ws=object(),
            status=lambda: {"ready": False})
        identity = {"session": native_session, "session_id": "session-9",
                    "operation_id": "operation-9", "media_epoch": "epoch-9"}
        browser_registry = SimpleNamespace(
            get_by_call_token=lambda value: native_session if value == token else None,
            close=AsyncMock())
        ami = SimpleNamespace(
            renew_channel_absolute_timeout=AsyncMock(return_value=True),
            hangup_channel=AsyncMock(return_value=True))
        with patch.object(main, "media_admission", registry), \
                patch.object(main.browser_media, "registry", browser_registry), \
                patch.object(main, "_schedule_native_browser_hangup") as schedule, \
                patch.object(main.hub, "ami_for", new=AsyncMock(return_value=ami)), \
                patch.object(main.hub.runtime, "get", new=AsyncMock(return_value={
                    "running": True, "container_id": "generation",
                    "engine_run_id": "run-9", "browser_outbound": True})):
            await main._renew_softphone_call_lease(
                token, "9", "generation", "171.9", identity)
        browser_registry.close.assert_awaited_once_with(
            native_session, "native browser media lease lost")
        schedule.assert_called_once_with(native_session)
        ami.hangup_channel.assert_not_awaited()
        ami.renew_channel_absolute_timeout.assert_not_awaited()
        self.assertFalse(registry.authorization_active(
            token, "9", "generation", "171.9"))

    async def test_native_call_lease_never_falls_back_when_registry_entry_is_missing(self):
        registry = MediaAdmissionRegistry()
        token = registry.issue("9", "generation", "native-wss-v1")
        self.assertTrue(registry.authorize_native(
            token, "9", "generation", "owner", "+44123", "171.9"))
        frozen = SimpleNamespace(
            iid="9", generation="generation", channel_id="171.9",
            session_id="session-9", operation_id="operation-9", media_epoch="epoch-9",
            phase="active",
            engine_run_id="run-9", browser_ws=object(), asterisk_ws=object(),
            status=lambda: {"ready": True})
        identity = {"session": frozen, "session_id": frozen.session_id,
                    "operation_id": frozen.operation_id, "media_epoch": frozen.media_epoch}
        browser_registry = SimpleNamespace(
            get_by_call_token=lambda _value: None, close=AsyncMock())
        ami = SimpleNamespace(renew_channel_absolute_timeout=AsyncMock(return_value=True))
        with patch.object(main, "media_admission", registry), \
                patch.object(main.browser_media, "registry", browser_registry), \
                patch.object(main, "_schedule_native_browser_hangup") as schedule, \
                patch.object(main.hub, "ami_for", new=AsyncMock(return_value=ami)), \
                patch.object(main.hub.runtime, "get", new=AsyncMock(return_value={
                    "running": True, "container_id": "generation",
                    "engine_run_id": "run-9", "browser_outbound": True})):
            await main._renew_softphone_call_lease(
                token, "9", "generation", "171.9", identity)
        ami.renew_channel_absolute_timeout.assert_not_awaited()
        browser_registry.close.assert_awaited_once_with(
            frozen, "native browser media lease lost")
        schedule.assert_called_once_with(frozen)

    async def test_native_call_lease_never_renews_after_hangup_phase(self):
        registry = MediaAdmissionRegistry()
        token = registry.issue("9", "generation", "native-wss-v1")
        self.assertTrue(registry.authorize_native(
            token, "9", "generation", "owner", "+44123", "171.9"))
        ending = SimpleNamespace(
            iid="9", generation="generation", channel_id="171.9",
            session_id="session-9", operation_id="operation-9", media_epoch="epoch-9",
            phase="ending", engine_run_id="run-9", browser_ws=object(),
            asterisk_ws=object(), status=lambda: {"ready": True})
        identity = {"session": ending, "session_id": ending.session_id,
                    "operation_id": ending.operation_id, "media_epoch": ending.media_epoch}
        browser_registry = SimpleNamespace(
            get_by_call_token=lambda _value: ending, close=AsyncMock())
        ami = SimpleNamespace(renew_channel_absolute_timeout=AsyncMock(return_value=True))
        with patch.object(main, "media_admission", registry), \
                patch.object(main.browser_media, "registry", browser_registry), \
                patch.object(main, "_schedule_native_browser_hangup") as schedule, \
                patch.object(main.hub, "ami_for", new=AsyncMock(return_value=ami)), \
                patch.object(main.hub.runtime, "get", new=AsyncMock(return_value={
                    "running": True, "container_id": "generation",
                    "engine_run_id": "run-9", "browser_outbound": True})):
            await main._renew_softphone_call_lease(
                token, "9", "generation", "171.9", identity)
        ami.renew_channel_absolute_timeout.assert_not_awaited()
        browser_registry.close.assert_awaited_once_with(
            ending, "native browser media lease lost")
        schedule.assert_called_once_with(ending)

    async def test_softphone_shutdown_hangs_up_only_exact_generation_then_cancels_tasks(self):
        import asyncio

        current_task = asyncio.create_task(asyncio.Event().wait())
        stale_task = asyncio.create_task(asyncio.Event().wait())
        leases = {
            "current": {"iid": "9", "generation": "current",
                        "source_call_id": "171.9", "task": current_task},
            "stale": {"iid": "8", "generation": "old",
                      "source_call_id": "171.8", "task": stale_task},
        }
        ami = SimpleNamespace(hangup_channel=AsyncMock(return_value=True))
        async def runtime(iid, force=False):
            del force
            return {"running": True,
                    "container_id": "current" if iid == "9" else "replacement"}
        with patch.object(main, "_softphone_call_leases", leases), \
                patch.object(main.hub.runtime, "get", new=AsyncMock(side_effect=runtime)), \
                patch.object(main.hub, "ami_for", new=AsyncMock(return_value=ami)):
            await main._shutdown_softphone_call_leases()
        ami.hangup_channel.assert_awaited_once_with("171.9")
        self.assertTrue(current_task.cancelled())
        self.assertTrue(stale_task.cancelled())
        self.assertEqual(leases, {})

    def test_sip_initial_invite_distinguishes_uri_and_folded_dialog_tags(self):
        self.assertTrue(main._sip_initial_invite(
            "INVITE sip:b@example SIP/2.0\r\n"
            "To: <sip:b@example;tag=uri-parameter>\r\n\r\n"))
        self.assertFalse(main._sip_initial_invite(
            "INVITE sip:b@example SIP/2.0\r\n"
            "To: <sip:b@example>\r\n ;tag=folded-dialog\r\n\r\n"))
        self.assertFalse(main._sip_initial_invite(
            "INVITE sip:b@example SIP/2.0\r\n"
            "t: <sip:b@example>;tag=compact-dialog\r\n\r\n"))

    async def test_manual_stop_waits_for_recovery_transaction(self):
        lock = main.hub.recovery_lock("manual-stop")
        await lock.acquire()
        try:
            inst = {"id": "manual-stop", "enabled": True}
            with patch.object(main.cfg, "get_instance", return_value=inst), \
                    patch.object(main.cfg, "upsert_instance", return_value={
                        **inst, "enabled": False}), \
                    patch.object(main.hub.runtime, "get", new=AsyncMock(return_value={
                        "running": True, "container_id": "manual-generation"})), \
                    patch.object(main.engine, "stop") as stop, \
                    patch.object(main.hub, "drop_ami", new=AsyncMock()), \
                    patch.object(main, "_clear_manual_recovery_history"):
                task = __import__("asyncio").create_task(
                    main.api_instance_stop("manual-stop"))
                await __import__("asyncio").sleep(0)
                stop.assert_not_called()
                lock.release()
                await task
        finally:
            if lock.locked():
                lock.release()
        stop.assert_called_once_with(
            "manual-stop", expected_container_id="manual-generation")

    async def test_registering_clears_what_the_ledger_held_against_the_exit(self):
        main.hub.exit_ledgers["9"] = {"node": "node-a", "strikes": 2, "tried": ["node-a"],
                                      "failures": 2, "given_up": True, "reported": True}
        st = {"state": "OK", "label": "Working", "reason_code": "ok", "reason": "",
              "detail": {}}
        with patch.object(main, "_save_exit_ledgers"), \
                patch.object(main.cfg, "get_settings", return_value={}):
            main.apply_health("9", self.INST, st)
        self.assertNotIn("9", main.hub.exit_ledgers)
