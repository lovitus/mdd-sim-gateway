import asyncio
import base64
import inspect
import json
import pathlib
import sys
import time
import unittest
import warnings
from types import SimpleNamespace
from unittest.mock import AsyncMock, patch

import pytest

ROOT = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "control"))

from app import browser_media  # noqa: E402
from app.ami import (OneShotAmiSession, browser_media_canary_action,
                     browser_media_outbound_warmup_action,
                     browser_media_inbound_warmup_action)  # noqa: E402
from app import main as main_app  # noqa: E402


class FakeWebSocket:
    def __init__(self):
        self.json = []
        self.binary = []
        self.closed = []

    async def send_json(self, value):
        self.json.append(value)

    async def send_bytes(self, value):
        self.binary.append(bytes(value))

    async def close(self, code=1000, reason=""):
        self.closed.append((code, reason))


class HangingWebSocket(FakeWebSocket):
    async def send_json(self, _value):
        await asyncio.Event().wait()

    async def close(self, code=1000, reason=""):
        await asyncio.Event().wait()


def media_start(session):
    return {
        "event": "MEDIA_START", "channel": "WebSocket/mdd_control_media/test",
        "channel_id": session.channel_id, "format": "slin",
        "optimal_frame_size": 320, "ptime": 20,
    }


class BrowserMediaRegistryTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.registry = browser_media.BrowserMediaRegistry(capacity=16)

    async def asyncTearDown(self):
        await self.registry.close_all()

    async def allocate(self, generation=None):
        return await self.registry.allocate(
            iid="7", generation=generation or "a" * 64, engine_run_id="run-7",
            subject=browser_media.subject_digest("admin-session"))

    async def attach_asterisk(self, session, websocket=None):
        websocket = websocket or FakeWebSocket()
        await self.registry.claim_asterisk(
            engine_sid=session.engine_sid, iid="7", generation=session.generation,
            engine_run_id="run-7", websocket=websocket,
            media_start=media_start(session))
        return websocket

    async def test_ticket_is_one_shot_and_bound_to_subject(self):
        session = await self.allocate()
        with self.assertRaises(browser_media.BrowserMediaUnavailable):
            await self.registry.claim_browser(
                session_id=session.session_id, ticket=session.ticket,
                subject=browser_media.subject_digest("other"), websocket=FakeWebSocket())
        ticket = session.ticket
        await self.registry.claim_browser(
            session_id=session.session_id, ticket=ticket,
            subject=browser_media.subject_digest("admin-session"), websocket=FakeWebSocket())
        with self.assertRaises(browser_media.BrowserMediaUnavailable):
            await self.registry.claim_browser(
                session_id=session.session_id, ticket=ticket,
                subject=browser_media.subject_digest("admin-session"), websocket=FakeWebSocket())

    async def test_per_call_asterisk_wss_and_fresh_bidirectional_evidence_are_required(self):
        session = await self.allocate()
        browser_ws = FakeWebSocket()
        await self.registry.claim_browser(
            session_id=session.session_id, ticket=session.ticket,
            subject=browser_media.subject_digest("admin-session"), websocket=browser_ws)
        asterisk_ws = await self.attach_asterisk(session)

        session.started = True
        session.challenge = "fresh-challenge"
        self.registry.start_browser_pump(session)
        for _ in range(2):
            await self.registry.forward_browser_pcm(session, b"\0" * 320)
            await self.registry.handle_asterisk_pcm(session, b"\0" * 320)
        await asyncio.sleep(0.05)
        self.registry.handle_asterisk_control(session, {
            "event": "STATUS", "channel_id": session.channel_id, "queue_length": 0})
        status = session.record_browser_evidence({
            "type": "browser.media.evidence", "version": 1,
            "challenge": "fresh-challenge", "capture_callbacks": 2,
            "playback_callbacks": 2, "played_frames": 2,
        })
        self.assertTrue(status["ready"])
        self.assertEqual(len(browser_ws.binary), 2)
        self.assertEqual(len(asterisk_ws.binary), 2)

    async def test_generation_identity_and_seventeenth_session_fail_closed(self):
        session = await self.allocate()
        with self.assertRaises(browser_media.BrowserMediaUnavailable):
            await self.registry.claim_asterisk(
                engine_sid=session.engine_sid, iid="7", generation="b" * 64,
                engine_run_id="run-7", websocket=FakeWebSocket(),
                media_start=media_start(session))
        sessions = [session] + [await self.allocate() for _ in range(15)]
        with self.assertRaises(browser_media.BrowserMediaUnavailable):
            await self.allocate()
        await self.registry.close(sessions[0], "test")
        self.assertIsNotNone(await self.allocate())

    async def test_frame_size_counter_rollback_and_media_start_format_are_rejected(self):
        session = await self.allocate()
        session.browser_ws = FakeWebSocket()
        session.started = True
        session.challenge = "one"
        with self.assertRaises(browser_media.BrowserMediaUnavailable):
            await self.registry.forward_browser_pcm(session, b"short")
        bad_start = {**media_start(session), "optimal_frame_size": 640}
        with self.assertRaises(browser_media.BrowserMediaUnavailable):
            await self.registry.claim_asterisk(
                engine_sid=session.engine_sid, iid="7", generation=session.generation,
                engine_run_id="run-7", websocket=FakeWebSocket(), media_start=bad_start)
        session.capture_callbacks = 3
        with self.assertRaises(browser_media.BrowserMediaUnavailable):
            session.record_browser_evidence({
                "type": "browser.media.evidence", "version": 1, "challenge": "one",
                "capture_callbacks": 2, "playback_callbacks": 0, "played_frames": 0,
            })

    async def test_close_is_idempotent_and_hangup_is_sent_once(self):
        session = await self.allocate()
        asterisk_ws = await self.attach_asterisk(session)
        await self.registry.close(session, "browser disconnected")
        await self.registry.close(session, "late timeout")
        self.assertEqual(asterisk_ws.json, [{"command": "HANGUP"}])
        self.assertIsNone(self.registry.get(session.session_id))

    async def test_one_per_call_transport_close_does_not_close_unrelated_session(self):
        first = await self.allocate()
        second = await self.allocate()
        second.browser_ws = FakeWebSocket()
        await self.attach_asterisk(first)
        await self.attach_asterisk(second)
        await self.registry.close(first, "first completed")
        self.assertFalse(await self.registry.handle_asterisk_pcm(first, b"\0" * 320))
        self.assertFalse(second.closed.is_set())

    async def test_outbound_identity_is_exclusive_per_line_and_removed_on_close(self):
        token = "A" * 43
        first = await self.registry.allocate(
            iid="7", generation="a" * 64, engine_run_id="run-7", subject="subject",
            purpose="outbound", destination="+447700900123", call_token=token)
        self.assertIs(self.registry.outbound("7"), first)
        self.assertIs(self.registry.get_by_call_token(token), first)
        with self.assertRaisesRegex(browser_media.BrowserMediaUnavailable, "active call owner"):
            await self.registry.allocate(
                iid="7", generation="a" * 64, engine_run_id="run-7", subject="subject",
                purpose="outbound", destination="555", call_token="B" * 43)
        await self.registry.close(first, "done")
        self.assertIsNone(self.registry.outbound("7"))
        self.assertIsNone(self.registry.get_by_call_token(token))

    async def test_outbound_rejects_destination_or_token_outside_exact_contract(self):
        for destination, token in (("07700900123", "A" * 43),
                                   ("+01234567", "A" * 43),
                                   ("+447700900123", "short")):
            with self.subTest(destination=destination, token=token):
                with self.assertRaises(browser_media.BrowserMediaUnavailable):
                    await self.registry.allocate(
                        iid="7", generation="a" * 64, engine_run_id="run-7",
                        subject="subject", purpose="outbound", destination=destination,
                        call_token=token)

    async def test_inbound_allows_three_exact_claimants_and_close_releases_capacity(self):
        identity = {
            "iid": "7", "generation": "a" * 64, "engine_run_id": "run-7",
            "subject": "subject", "purpose": "inbound", "backend_call_id": 91,
            "backend_revision": 0,
            "source_call_id": "run-7:171.7",
        }
        claimants = [await self.registry.allocate(**identity) for _ in range(3)]
        self.assertEqual(
            self.registry.inbound_claimants("7", "run-7", "run-7:171.7", 91),
            claimants)
        with self.assertRaisesRegex(
                browser_media.BrowserMediaUnavailable, "maximum media claimants"):
            await self.registry.allocate(**identity)
        await self.registry.close(claimants[0], "claimant left")
        replacement = await self.registry.allocate(**identity)
        self.assertEqual(len(self.registry.inbound_claimants(
            "7", "run-7", "run-7:171.7", 91)), 3)
        self.assertIsNotNone(replacement)

    async def test_inbound_claimant_identity_is_exact(self):
        cases = (
        {"backend_call_id": 0, "backend_revision": 0,
         "source_call_id": "run-7:171.7"},
        {"backend_call_id": True, "backend_revision": 0,
         "source_call_id": "run-7:171.7"},
        {"backend_call_id": 91, "backend_revision": -1,
         "source_call_id": "run-7:171.7"},
        {"backend_call_id": 91, "backend_revision": 0,
         "source_call_id": "missing-linkedid"},
        )
        for values in cases:
            with self.subTest(values=values), self.assertRaises(
                    browser_media.BrowserMediaUnavailable):
                await self.registry.allocate(
                    iid="7", generation="a" * 64, engine_run_id="run-7",
                    subject="subject", purpose="inbound", **values)

    async def test_inbound_commit_wins_or_loses_expiry_atomically_and_gets_full_ttl(self):
        identity = {
            "iid": "7", "generation": "a" * 64, "engine_run_id": "run-7",
            "subject": "subject", "purpose": "inbound", "backend_call_id": 91,
            "backend_revision": 0, "source_call_id": "run-7:171.7",
        }
        live = await self.registry.allocate(**identity)
        live.expires_at = time.monotonic() + 0.01
        committed_at = time.monotonic()
        self.assertTrue(await self.registry.commit_inbound(live))
        self.assertGreaterEqual(
            live.expires_at - committed_at, browser_media.SESSION_TTL_SECONDS - 0.01)
        self.assertFalse(await self.registry.commit_inbound(live))

        expired = await self.registry.allocate(
            **{**identity, "backend_call_id": 92,
               "source_call_id": "run-7:171.8"})
        expired.expires_at = time.monotonic() - 1
        self.assertFalse(await self.registry.commit_inbound(expired))
        self.assertTrue(await self.registry.close_if_expired(expired))
        self.assertIsNone(self.registry.get(expired.session_id))

    async def test_expiry_task_observes_committed_deadline_instead_of_old_reservation(self):
        registry = browser_media.BrowserMediaRegistry()
        with patch.object(browser_media, "SESSION_TTL_SECONDS", 0.12), \
                patch.object(main_app.browser_media, "registry", registry):
            session = await registry.allocate(
                iid="7", generation="a" * 64, engine_run_id="run-7",
                subject="subject", purpose="inbound", backend_call_id=91,
                backend_revision=0, source_call_id="run-7:171.7")
            expiry = asyncio.create_task(main_app._expire_browser_media_session(session))
            await asyncio.sleep(0.04)
            self.assertTrue(await registry.commit_inbound(session))
            await asyncio.sleep(0.09)
            self.assertFalse(session.closed.is_set())
            await asyncio.wait_for(expiry, timeout=0.08)
            self.assertTrue(session.closed.is_set())

    @staticmethod
    def mark_inbound_ready(session):
        now = time.monotonic()
        session.phase = "inbound_ready"
        session.started = True
        session.browser_ws = FakeWebSocket()
        session.asterisk_ws = FakeWebSocket()
        session.browser_to_engine_frames = session.engine_to_browser_frames = 2
        session.browser_to_engine_at = session.engine_to_browser_at = now
        session.capture_callbacks = session.playback_callbacks = session.played_frames = 2
        session.evidence_at = session.challenge_ack_at = now
        session.asterisk_status_at = now

    async def test_inbound_owner_task_is_registered_before_it_can_run_and_pauses_ttl(self):
        session = await self.registry.allocate(
            iid="7", generation="a" * 64, engine_run_id="run-7",
            subject="subject", purpose="inbound", backend_call_id=91,
            backend_revision=0, source_call_id="run-7:171.7")
        self.assertTrue(await self.registry.commit_inbound(session))
        self.mark_inbound_ready(session)
        started = asyncio.Event()
        release = asyncio.Event()

        async def owner(current):
            self.assertIs(self.registry.inbound_owner(current.session_id), current)
            self.assertIs(current.answer_task, asyncio.current_task())
            started.set()
            await release.wait()

        task = await self.registry.start_inbound_owner(session, owner)
        self.assertIsNotNone(task)
        self.assertIs(self.registry.inbound_owner(session.session_id), session)
        self.assertIs(session.answer_task, task)
        self.assertTrue(session.answer_owned)
        self.assertEqual(session.phase, "claiming")
        self.assertEqual(session.expires_at, float("inf"))
        await asyncio.wait_for(started.wait(), timeout=0.1)
        session.abort_requested.set()
        release.set()
        await asyncio.wait_for(task, timeout=0.1)
        self.assertIsNone(self.registry.inbound_owner(session.session_id))

    async def test_inbound_owner_factory_failure_leaves_ready_claimant_unchanged(self):
        session = await self.registry.allocate(
            iid="7", generation="a" * 64, engine_run_id="run-7",
            subject="subject", purpose="inbound", backend_call_id=91,
            backend_revision=0, source_call_id="run-7:171.7")
        self.assertTrue(await self.registry.commit_inbound(session))
        self.mark_inbound_ready(session)
        original_expiry = session.expires_at

        def failed_factory(_session):
            raise RuntimeError("injected owner factory failure")

        with self.assertRaisesRegex(RuntimeError, "injected"):
            await self.registry.start_inbound_owner(session, failed_factory)
        self.assertEqual(session.phase, "inbound_ready")
        self.assertFalse(session.answer_owned)
        self.assertIsNone(session.answer_task)
        self.assertEqual(session.expires_at, original_expiry)
        self.assertIsNone(self.registry.inbound_owner(session.session_id))

    async def test_closing_owned_session_requests_abort_without_losing_task_identity(self):
        session = await self.registry.allocate(
            iid="7", generation="a" * 64, engine_run_id="run-7",
            subject="subject", purpose="inbound", backend_call_id=91,
            backend_revision=0, source_call_id="run-7:171.7")
        self.assertTrue(await self.registry.commit_inbound(session))
        self.mark_inbound_ready(session)

        async def owner(current):
            await current.abort_requested.wait()
            self.assertIs(self.registry.inbound_owner(current.session_id), current)

        task = await self.registry.start_inbound_owner(session, owner)
        await self.registry.close(session, "browser disconnected")
        self.assertTrue(session.abort_requested.is_set())
        self.assertIs(self.registry.inbound_owner(session.session_id), session)
        await asyncio.wait_for(task, timeout=0.1)
        self.assertIsNone(self.registry.inbound_owner(session.session_id))

    async def test_inbound_owner_exception_closes_session_and_releases_capacity(self):
        session = await self.registry.allocate(
            iid="7", generation="a" * 64, engine_run_id="run-7",
            subject="subject", purpose="inbound", backend_call_id=91,
            backend_revision=0, source_call_id="run-7:171.7")
        self.assertTrue(await self.registry.commit_inbound(session))
        self.mark_inbound_ready(session)

        async def failed(_current):
            raise RuntimeError("injected owner operation failure")

        with self.assertLogs(browser_media.log, level="ERROR") as captured:
            task = await self.registry.start_inbound_owner(session, failed)
            with self.assertRaisesRegex(RuntimeError, "injected"):
                await task
        self.assertTrue(any("RuntimeError" in item for item in captured.output))
        self.assertTrue(session.abort_requested.is_set())
        self.assertIsNone(self.registry.get(session.session_id))
        self.assertIsNone(self.registry.inbound_owner(session.session_id))
        replacement = await self.registry.allocate(
            iid="7", generation="a" * 64, engine_run_id="run-7",
            subject="subject", purpose="inbound", backend_call_id=91,
            backend_revision=0, source_call_id="run-7:171.7")
        self.assertIsNotNone(replacement)

    async def test_inbound_owner_cancellation_closes_session_and_owner_map(self):
        session = await self.registry.allocate(
            iid="7", generation="a" * 64, engine_run_id="run-7",
            subject="subject", purpose="inbound", backend_call_id=91,
            backend_revision=0, source_call_id="run-7:171.7")
        self.assertTrue(await self.registry.commit_inbound(session))
        self.mark_inbound_ready(session)
        parked = asyncio.Event()

        async def owner(_current):
            await parked.wait()

        with self.assertLogs(browser_media.log, level="WARNING") as captured:
            task = await self.registry.start_inbound_owner(session, owner)
            await asyncio.sleep(0)
            task.cancel()
            with self.assertRaises(asyncio.CancelledError):
                await task
        self.assertTrue(any("cancelled" in item for item in captured.output))
        self.assertTrue(session.abort_requested.is_set())
        self.assertIsNone(self.registry.get(session.session_id))
        self.assertIsNone(self.registry.inbound_owner(session.session_id))

    async def test_create_task_failure_closes_both_wrapper_and_operation_coroutines(self):
        session = await self.registry.allocate(
            iid="7", generation="a" * 64, engine_run_id="run-7",
            subject="subject", purpose="inbound", backend_call_id=91,
            backend_revision=0, source_call_id="run-7:171.7")
        self.assertTrue(await self.registry.commit_inbound(session))
        self.mark_inbound_ready(session)

        async def owner(_current):
            return None

        with warnings.catch_warnings():
            warnings.simplefilter("error", RuntimeWarning)
            with patch.object(browser_media.asyncio, "create_task",
                              side_effect=RuntimeError("injected create_task failure")), \
                    self.assertRaisesRegex(RuntimeError, "injected"):
                await self.registry.start_inbound_owner(session, owner)
        self.assertFalse(session.answer_owned)
        self.assertIsNone(session.answer_task)
        self.assertEqual(session.phase, "inbound_ready")
        self.assertIs(self.registry.get(session.session_id), session)
        self.assertIsNone(self.registry.inbound_owner(session.session_id))

    async def test_asterisk_status_is_exact_and_fails_before_20s_upstream_queue(self):
        session = await self.allocate()
        await self.attach_asterisk(session)
        self.registry.handle_asterisk_control(session, {
            "event": "STATUS", "channel_id": session.channel_id, "queue_length": 10})
        self.assertEqual(session.asterisk_queue_length, 10)
        with self.assertRaises(browser_media.BrowserMediaUnavailable, msg="identity"):
            self.registry.handle_asterisk_control(session, {
                "event": "MEDIA_XON", "channel_id": "other", "queue_length": 0})
        with self.assertRaises(browser_media.BrowserMediaUnavailable, msg="200ms"):
            self.registry.handle_asterisk_control(session, {
                "event": "STATUS", "channel_id": session.channel_id, "queue_length": 11})
        with self.assertRaises(browser_media.BrowserMediaUnavailable, msg="backpressure"):
            self.registry.handle_asterisk_control(session, {
                "event": "MEDIA_XOFF", "channel_id": session.channel_id})

    async def test_ready_requires_a_fresh_exact_asterisk_status(self):
        session = await self.allocate()
        session.browser_ws = FakeWebSocket()
        await self.attach_asterisk(session)
        session.started = True
        session.challenge = "fresh"
        now = time.monotonic()
        session.browser_to_engine_frames = session.engine_to_browser_frames = 2
        session.browser_to_engine_at = session.engine_to_browser_at = now
        status = session.record_browser_evidence({
            "type": "browser.media.evidence", "version": 1, "challenge": "fresh",
            "capture_callbacks": 2, "playback_callbacks": 2, "played_frames": 2})
        self.assertFalse(status["ready"])

    async def test_expiry_task_finishes_after_normal_close_without_second_hangup(self):
        session = await self.allocate()
        asterisk_ws = await self.attach_asterisk(session)
        with patch.object(main_app.browser_media, "registry", self.registry):
            expiry = asyncio.create_task(main_app._expire_browser_media_session(session))
            await self.registry.close(session, "normal close")
            await asyncio.wait_for(expiry, timeout=0.2)
        self.assertEqual(asterisk_ws.json, [{"command": "HANGUP"}])

    async def test_close_and_shutdown_are_bounded_when_both_websockets_hang(self):
        session = await self.allocate()
        session.browser_ws = HangingWebSocket()
        await self.attach_asterisk(session, HangingWebSocket())
        with patch.object(browser_media, "CLOSE_IO_TIMEOUT_SECONDS", 0.01):
            await asyncio.wait_for(
                self.registry.close(session, "hung endpoints"), timeout=0.1)
        self.assertTrue(session.closed.is_set())
        self.assertIsNone(self.registry.get(session.session_id))

    async def test_missing_status_response_closes_only_that_session(self):
        session = await self.allocate()
        await self.attach_asterisk(session)
        with patch.object(main_app.browser_media, "registry", self.registry), \
                patch.object(browser_media,
                             "ASTERISK_STATUS_RESPONSE_TIMEOUT_SECONDS", 0.01):
            await asyncio.wait_for(
                main_app._browser_media_asterisk_status(session), timeout=0.1)
        self.assertTrue(session.closed.is_set())


def test_control_message_parser_is_bounded():
    assert browser_media.parse_text_message('{"version":1}') == {"version": 1}
    with pytest.raises(browser_media.BrowserMediaUnavailable):
        browser_media.parse_text_message(json.dumps({"x": "z" * 5000}))


def test_canary_ami_and_dialplan_have_no_user_dialable_fields_or_carrier_context():
    source = inspect.getsource(browser_media_canary_action)
    assert 'WebSocket/mdd_control_media/c(slin)nf(json)v(sid=' in source
    assert 'len(channel) > 160' in source
    assert 'AudioSocket/' not in source
    assert '"Context": "browser-media-canary"' in source
    assert '"Exten": "echo"' in source
    assert '"Timeout": "5000"' in source
    assert list(inspect.signature(browser_media_canary_action).parameters) == [
        "engine_sid", "channel_id"]

    dialplan = (ROOT / "engine/templates/extensions.conf.j2").read_text(encoding="utf-8")
    block = dialplan.split("[browser-media-canary]", 1)[1].split("[from-local]", 1)[0]
    assert "TIMEOUT(absolute)=10" in block and "Echo()" in block and "Hangup()" in block
    assert 'MDD_ADMISSION(media_check)' in block
    assert block.index('MDD_ADMISSION(media_check)') < block.index("Echo()")
    assert "n(rebind-blocked),Hangup(41)" in block
    executable = "\n".join(line for line in block.splitlines()
                           if not line.lstrip().startswith(";"))
    assert "STAT(e," not in executable
    for forbidden in ("include", "volte_ims", "Dial(", "MessageSend", "PJSIPRegister", "APDU"):
        assert forbidden not in executable


def test_native_outbound_warmup_and_dialplan_are_fixed_fail_closed_paths():
    action = browser_media_outbound_warmup_action(
        "A" * 24, "mddcanary-00000000-0000-4000-8000-000000000000")
    assert action["Context"] == "browser-media-outbound-warmup"
    assert action["Exten"] == "echo" and action["Priority"] == "1"
    assert not any(value == "+447700900123" for value in action.values())

    dialplan = (ROOT / "engine/templates/extensions.conf.j2").read_text(encoding="utf-8")
    warmup = dialplan.split("[browser-media-outbound-warmup]", 1)[1].split(
        "[browser-media-outbound]", 1)[0]
    native = dialplan.split("[browser-media-outbound]", 1)[1].split("[from-local]", 1)[0]
    local = dialplan.split("[from-local]", 1)[1].split("[mdd-call-active]", 1)[0]
    incoming = dialplan.split("[volte_ims]", 1)[1].split("[volte_ims_msg]", 1)[0]
    assert "echo,1,Set(GROUP(mdd_line_call)=active)" in warmup
    assert "GROUP_COUNT(active@mdd_line_call)" in warmup
    assert warmup.index("GROUP(mdd_line_call)") < warmup.index("Echo()")
    assert "exten => call,1" in native
    for variable in ("MDD_NATIVE_CALL", "MDD_MEDIA_TOKEN", "MDD_MEDIA_EPOCH",
                     "MDD_OPERATION_ID", "MDD_DESTINATION"):
        assert variable in native
    assert "Goto(from-local,${MDD_DESTINATION},1)" in native
    assert 'GotoIf($["${MDD_NATIVE_CALL}" != "1"]?native-required)' in local
    assert "PJSIPHangup(Decline)" in local and '"${MDD_NATIVE_CALL}" != "1"' in local
    assert "GROUP(mdd_line_call)" in incoming
    assert "GROUP_COUNT(active@mdd_line_call)" in incoming


def test_inbound_claimant_warmup_is_fixed_and_never_joins_the_paid_line_group():
    action = browser_media_inbound_warmup_action(
        "A" * 24, "mddcanary-00000000-0000-4000-8000-000000000000")
    assert action["Context"] == "browser-media-inbound-warmup"
    assert action["Exten"] == "echo" and action["Priority"] == "1"
    dialplan = (ROOT / "engine/templates/extensions.conf.j2").read_text(encoding="utf-8")
    warmup = dialplan.split("[browser-media-inbound-warmup]", 1)[1].split(
        "[browser-media-outbound]", 1)[0]
    assert "MDD_ADMISSION(media_check)" in warmup
    assert "TIMEOUT(absolute)=10" in warmup and "Echo()" in warmup
    assert "GROUP(" not in warmup and "GROUP_COUNT(" not in warmup
    for forbidden in ("Dial(", "MddAnswerBridged", "PJSIPAnswer", "Answer("):
        assert forbidden not in warmup


def test_incoming_owner_classification_requires_one_pristine_unanswered_leg():
    linkedid = "171.7"
    channel = {
        "Linkedid": linkedid, "Context": "volte_ims",
        "ChannelStateDesc": "Ringing", "BridgeId": "",
    }
    variables = {
        "MDD_INBOUND_ATTACH": "0", "MDD_INBOUND_ARMED": "0",
        "MDD_INBOUND_SOURCE_ID": "", "MDD_INBOUND_OPERATION": "",
        "MDD_MEDIA_EPOCH": "", "MDD_INBOUND_WINNER_ID": "",
        "MDD_INBOUND_WINNER_CHANNEL": "",
        "MDD_INBOUND_ANSWER_RESULT": "waiting",
    }
    pristine = {"ok": True, "channel": channel, "variables": variables}
    assert main_app._incoming_owner_classification(pristine, linkedid) == "pristine"
    mutations = (
        {"ok": False},
        {"channel": {**channel, "ChannelStateDesc": "Up"}},
        {"channel": {**channel, "BridgeId": "bridge-1"}},
        {"variables": {**variables, "MDD_INBOUND_ARMED": "1"}},
        {"variables": {**variables, "MDD_INBOUND_OPERATION": "a" * 32}},
        {"variables": {**variables, "MDD_INBOUND_ANSWER_RESULT": ""}},
    )
    for index, mutation in enumerate(mutations):
        candidate = {**pristine, **mutation}
        expected = "unknown" if index == 0 else "unsafe"
        assert main_app._incoming_owner_classification(candidate, linkedid) == expected
    incomplete = {**pristine, "variables": {
        key: value for key, value in variables.items() if key != "MDD_MEDIA_EPOCH"}}
    assert main_app._incoming_owner_classification(incomplete, linkedid) == "unknown"


@pytest.mark.asyncio
async def test_one_shot_redirect_requires_the_only_exact_warmup_and_readback():
    channel_id = "mddcanary-00000000-0000-4000-8000-000000000000"
    channel = "WebSocket/mdd_control_media-00000001"
    session = OneShotAmiSession("7", "127.0.0.1", 5038, "u", "s", AsyncMock())
    session._snapshot = AsyncMock(return_value={
        "ok": True, "count": 1, "channels": [{
            "Event": "CoreShowChannel", "Uniqueid": channel_id, "Channel": channel,
            "Context": "browser-media-outbound-warmup", "Application": "Echo",
        }],
    })
    variables = {
        "MDD_NATIVE_CALL": "1", "MDD_MEDIA_TOKEN": "A" * 43,
        "MDD_MEDIA_EPOCH": "B" * 24, "MDD_OPERATION_ID": "c" * 32,
        "MDD_DESTINATION": "+447700900123",
    }
    replies = []
    for value in variables.values():
        replies.extend(([{"Response": "Success"}], [{"Response": "Success", "Value": value}]))
    session.action = AsyncMock(side_effect=replies)
    assert await session.prepare_browser_media_redirect(
        channel=channel, channel_id=channel_id, variables=variables) == {"ok": True}
    assert session.action.await_count == 10

    session._snapshot = AsyncMock(return_value={"ok": True, "count": 2, "channels": []})
    session.action.reset_mock()
    rejected = await session.prepare_browser_media_redirect(
        channel=channel, channel_id=channel_id, variables=variables)
    assert rejected["ok"] is False
    session.action.assert_not_awaited()


@pytest.mark.asyncio
async def test_redirect_reproves_fresh_media_after_set_get_before_authorize_or_submit():
    registry = browser_media.BrowserMediaRegistry()
    session = await registry.allocate(
        iid="7", generation="a" * 64, engine_run_id="run-7", subject="subject",
        purpose="outbound", destination="+447700900123", call_token="A" * 43)
    session.phase = "warmup"
    session.asterisk_channel = "WebSocket/mdd_control_media-00000001"
    session.asterisk_channel_id = session.channel_id

    transaction = SimpleNamespace(
        prepare_browser_media_redirect=AsyncMock(return_value={"ok": True}),
        redirect_browser_media_outbound=AsyncMock(return_value={"ok": True}))
    transaction.__aenter__ = AsyncMock(return_value=transaction)
    transaction.__aexit__ = AsyncMock(return_value=None)

    class TransactionContext:
        async def __aenter__(self):
            return transaction
        async def __aexit__(self, *_args):
            return None

    authorize = patch.object(main_app.media_admission, "authorize_native")
    with patch.object(main_app, "_native_browser_media_ready",
                      AsyncMock(side_effect=[True, False])), \
            patch.object(main_app.hub.runtime, "get", AsyncMock(return_value={
                "running": True, "ip": "172.18.0.7", "container_id": "a" * 64,
                "engine_run_id": "run-7", "media_websocket": True,
                "browser_outbound": True})), \
            patch.object(main_app.cfg, "get_instance", return_value={
                "ami_user": "vowifi", "ami_secret": "secret"}), \
            patch.object(main_app, "OneShotAmiSession", return_value=TransactionContext()), \
            authorize as authorized, \
            pytest.raises(browser_media.BrowserMediaUnavailable, match="became stale"):
        await main_app._redirect_native_browser_outbound(session)
    authorized.assert_not_called()
    transaction.redirect_browser_media_outbound.assert_not_awaited()
    await registry.close_all()


@pytest.mark.asyncio
async def test_outbound_phase_transition_is_monotonic_under_delayed_hooks():
    registry = browser_media.BrowserMediaRegistry()
    session = await registry.allocate(
        iid="7", generation="a" * 64, engine_run_id="run-7", subject="subject",
        purpose="outbound", destination="+447700900123", call_token="A" * 43)
    assert await session.transition_phase("warmup") == 1
    assert await session.transition_phase("active") == 2
    assert await session.transition_phase("calling") is None
    assert session.phase == "active"
    assert await session.transition_phase("terminal") == 3
    assert await session.transition_phase("ending") is None
    assert session.phase == "terminal"
    await registry.close_all()


@pytest.mark.asyncio
async def test_inbound_phase_transition_is_monotonic_under_owner_and_disconnect_races():
    registry = browser_media.BrowserMediaRegistry()
    session = await registry.allocate(
        iid="7", generation="a" * 64, engine_run_id="run-7", subject="subject",
        purpose="inbound", backend_call_id=91, backend_revision=0,
        source_call_id="run-7:171.7")
    assert await session.transition_phase("inbound_warmup") == 1
    assert await session.transition_phase("inbound_ready") == 2
    assert await session.transition_phase("claiming") == 3
    assert await session.transition_phase("ending") == 4
    assert await session.transition_phase("attach_submitted_unknown") is None
    assert await session.transition_phase("active") is None
    assert session.phase == "ending"
    assert await session.transition_phase("terminal") == 5
    assert await session.transition_phase("ending") is None
    await registry.close_all()


class RejectingWebSocket:
    def __init__(self, *, cookie="", origin="https://gateway.test", host="gateway.test",
                 authorization="", protocol=""):
        self.cookies = {"mdd_session": cookie} if cookie else {}
        self.headers = {"origin": origin, "host": host,
                        "authorization": authorization,
                        "sec-websocket-protocol": protocol}
        self.query_params = {}
        self.closed = []
        self.accepted = []

    async def close(self, code=1000, reason=""):
        self.closed.append((code, reason))

    async def accept(self, subprotocol=None):
        self.accepted.append(subprotocol)


class BrowserSessionWebSocket(RejectingWebSocket):
    def __init__(self, session, cookie="session"):
        super().__init__(cookie=cookie)
        self.session = session
        self.sent_json = []

    async def receive_text(self):
        return json.dumps({
            "type": "browser.media.hello", "version": 1,
            "session_id": self.session.session_id, "ticket": self.session.ticket,
        })

    async def send_json(self, value):
        self.sent_json.append(value)


class AsteriskMediaWebSocket(RejectingWebSocket):
    def __init__(self, session, token):
        scoped = browser_media.engine_media_token(
            token, session.iid, session.engine_run_id)
        encoded = base64.b64encode(f"mdd-engine:{scoped}".encode()).decode()
        super().__init__(authorization=f"Basic {encoded}", protocol="media")
        self.query_params = {"sid": session.engine_sid}
        self.session = session
        self.sent_json = []

    async def receive_text(self):
        return json.dumps(media_start(self.session))

    async def receive(self):
        return {"type": "websocket.disconnect"}

    async def send_json(self, value):
        self.sent_json.append(value)

    async def send_bytes(self, _value):
        pass


@pytest.mark.asyncio
async def test_browser_websocket_rejects_missing_login_before_accept_or_runtime_work():
    websocket = RejectingWebSocket()
    with patch.object(main_app.auth, "session", return_value=None):
        await main_app.api_browser_media_ws(websocket, "7")
    assert websocket.closed[0][0] == 4401


@pytest.mark.asyncio
async def test_handler_level_websocket_close_is_bounded():
    websocket = HangingWebSocket()
    with patch.object(main_app, "BROWSER_MEDIA_WS_CLOSE_TIMEOUT_SECONDS", 0.01):
        await asyncio.wait_for(
            main_app._bounded_browser_media_websocket_close(websocket), timeout=0.1)


@pytest.mark.asyncio
async def test_native_hangup_phase_send_is_bounded_when_browser_stops_reading():
    registry = browser_media.BrowserMediaRegistry()
    session = await registry.allocate(
        iid="7", generation="a" * 64, engine_run_id="run-a", subject="subject",
        purpose="outbound", destination="+447700900123", call_token="A" * 43)
    session.browser_ws = HangingWebSocket()
    started = time.monotonic()
    sent = await main_app._bounded_native_call_phase_send(
        session, {"type": "browser.call.phase", "phase": "ending"}, timeout=0.01)
    assert sent is False
    assert time.monotonic() - started < 0.1
    await registry.close_all()


@pytest.mark.asyncio
async def test_rejected_browser_websocket_close_is_bounded():
    websocket = RejectingWebSocket()

    async def hang_close(*_args, **_kwargs):
        await asyncio.Event().wait()

    websocket.close = hang_close
    with patch.object(main_app.auth, "session", return_value=None), \
            patch.object(main_app, "BROWSER_MEDIA_WS_CLOSE_TIMEOUT_SECONDS", 0.01):
        await asyncio.wait_for(main_app.api_browser_media_ws(websocket, "7"), timeout=0.1)


@pytest.mark.asyncio
async def test_browser_websocket_requires_strict_same_origin():
    websocket = RejectingWebSocket(cookie="session", origin="https://other.test")
    with patch.object(main_app.auth, "session", return_value={"csrf": "x"}), \
            patch.object(main_app.media_ingress, "same_origin", return_value=False):
        await main_app.api_browser_media_ws(websocket, "7")
    assert websocket.closed[0][0] == 4403


@pytest.mark.asyncio
async def test_asterisk_websocket_rejects_basic_auth_before_runtime_lookup():
    websocket = RejectingWebSocket(authorization="Basic invalid", protocol="media")
    websocket.query_params = {"sid": "x" * 24}
    with patch.object(main_app.cfg, "internal_event_token", return_value="expected"), \
            patch.object(main_app.hub.runtime, "get", side_effect=AssertionError("runtime lookup")):
        await main_app.api_engine_media_call_ws(websocket)
    assert websocket.closed[0][0] == 4403


@pytest.mark.asyncio
async def test_global_or_other_run_token_cannot_claim_an_exact_media_session():
    registry = browser_media.BrowserMediaRegistry()
    session = await registry.allocate(
        iid="7", generation="a" * 64, engine_run_id="run-b", subject="subject")
    wrong_scope = SimpleNamespace(
        iid="7", engine_run_id="run-a", engine_sid=session.engine_sid)
    websocket = AsteriskMediaWebSocket(wrong_scope, "engine-token")
    websocket.query_params = {"sid": session.engine_sid}
    with patch.object(main_app.cfg, "internal_event_token", return_value="engine-token"), \
            patch.object(main_app.browser_media, "registry", registry), \
            patch.object(main_app.hub.runtime, "get",
                         side_effect=AssertionError("runtime lookup")):
        await main_app.api_engine_media_call_ws(websocket)
    assert websocket.closed == [(4403, "invalid Asterisk media identity")]
    await registry.close_all()


@pytest.mark.asyncio
async def test_slow_stale_asterisk_wss_cannot_attach_or_close_current_session():
    registry = browser_media.BrowserMediaRegistry()
    stale = await registry.allocate(
        iid="7", generation="a" * 64, engine_run_id="run-a", subject="subject-a")
    current = await registry.allocate(
        iid="7", generation="b" * 64, engine_run_id="run-b", subject="subject-b")
    websocket = AsteriskMediaWebSocket(stale, "engine-token")
    runtime = AsyncMock(side_effect=[
        {"running": True, "container_id": "a" * 64, "engine_run_id": "run-a",
         "media_websocket": True},
        {"running": True, "container_id": "b" * 64, "engine_run_id": "run-b",
         "media_websocket": True},
    ])
    try:
        with patch.object(main_app.cfg, "internal_event_token", return_value="engine-token"), \
                patch.object(main_app.hub.runtime, "get", runtime), \
                patch.object(main_app.browser_media, "registry", registry):
            await main_app.api_engine_media_call_ws(websocket)
        assert websocket.accepted == ["media"]
        assert stale.asterisk_ws is None
        assert not current.closed.is_set()
    finally:
        await registry.close_all()


@pytest.mark.asyncio
async def test_asterisk_media_ready_is_published_only_after_local_answer_command():
    registry = browser_media.BrowserMediaRegistry()
    session = await registry.allocate(
        iid="7", generation="a" * 64, engine_run_id="run-a", subject="subject-a")
    websocket = AsteriskMediaWebSocket(session, "engine-token")
    runtime = {"running": True, "container_id": "a" * 64,
               "engine_run_id": "run-a", "media_websocket": True}
    with patch.object(main_app.cfg, "internal_event_token", return_value="engine-token"), \
            patch.object(main_app.hub.runtime, "get", AsyncMock(return_value=runtime)), \
            patch.object(main_app.browser_media, "registry", registry):
        await main_app.api_engine_media_call_ws(websocket)
    assert session.asterisk_ready.is_set()
    assert websocket.sent_json[:1] == [{"command": "ANSWER"}]
    assert websocket.sent_json[-1:] == [{"command": "HANGUP"}]


def test_browser_handler_originate_waits_for_exact_asterisk_wss_before_pcm():
    source = inspect.getsource(main_app.api_browser_media_ws)
    one_shot = source.index("async with OneShotAmiSession(")
    maintenance = source.index("async with _pcscf_admission_boundary(")
    originate = source.index("browser_media_canary_action(")
    ready = source.index("session.asterisk_ready.wait()")
    started = source.index("session.started = True")
    assert one_shot < maintenance < originate < ready < started
    assert "await _line_admission_blocked(str(iid))" in source


@pytest.mark.asyncio
async def test_browser_media_prepare_rejects_maintenance_before_runtime_or_allocation():
    request = SimpleNamespace(cookies={}, headers={})
    with patch.object(main_app, "_browser_media_cookie_subject", return_value="subject"), \
            patch.object(main_app.cfg, "get_instance", return_value={"id": "7"}), \
            patch.object(main_app, "_line_admission_blocked",
                         AsyncMock(return_value=True)), \
            patch.object(main_app.hub.runtime, "get",
                         side_effect=AssertionError("runtime must not be read")), \
            pytest.raises(main_app.HTTPException) as exc:
        await main_app.api_browser_media_prepare("7", request)
    assert exc.value.status_code == 409


@pytest.mark.asyncio
async def test_outbound_prepare_requires_capability_exact_idle_snapshot_and_server_token():
    request = SimpleNamespace(cookies={}, headers={})
    runtime = {"running": True, "container_id": "a" * 64, "engine_run_id": "run-7",
               "media_websocket": True, "browser_outbound": True}
    ami = SimpleNamespace(complete_channel_snapshot=AsyncMock(return_value={
        "ok": True, "count": 0, "channels": [],
    }))
    registry = browser_media.BrowserMediaRegistry()
    try:
        with patch.object(main_app, "_browser_media_cookie_subject", return_value="subject"), \
                patch.object(main_app.cfg, "get_instance", return_value={"id": "7"}), \
                patch.object(main_app, "_line_admission_blocked",
                             AsyncMock(return_value=False)), \
                patch.object(main_app.hub.runtime, "get", AsyncMock(return_value=runtime)), \
                patch.object(main_app.hub, "ami_for", AsyncMock(return_value=ami)), \
                patch.object(main_app.browser_media, "registry", registry), \
                patch.object(main_app.media_admission, "issue", return_value="A" * 43):
            result = await main_app.api_browser_media_outbound_prepare(
                "7", {"to": "+447700900123"}, request)
        assert result["purpose"] == "outbound"
        session = registry.get(result["session_id"])
        assert session is not None and session.destination == "+447700900123"
        assert "A" * 43 not in result.values()
    finally:
        await registry.close_all()

    with patch.object(main_app, "_browser_media_cookie_subject", return_value="subject"), \
            patch.object(main_app.cfg, "get_instance", return_value={"id": "7"}), \
            patch.object(main_app, "_line_admission_blocked", AsyncMock(return_value=False)), \
            patch.object(main_app.hub.runtime, "get", AsyncMock(return_value=runtime)), \
            patch.object(main_app.hub, "ami_for", AsyncMock(return_value=SimpleNamespace(
                complete_channel_snapshot=AsyncMock(return_value={"ok": True, "count": 1})))), \
            pytest.raises(main_app.HTTPException) as busy:
        await main_app.api_browser_media_outbound_prepare(
            "7", {"to": "+447700900123"}, request)
    assert busy.value.status_code == 409


def incoming_record(*, state="ringing", revision=0):
    return {
        "id": 91, "instance": "7", "direction": "in", "transport": "vowifi",
        "status": "ringing", "source_call_id": "run-7:171.7",
        "engine_run_id": "run-7", "browser_state": state,
        "browser_revision": revision,
    }


def pristine_incoming_snapshot():
    return {
        "ok": True,
        "channel": {
            "Channel": "PJSIP/volte_ims-00000001", "Linkedid": "171.7",
            "Context": "volte_ims", "ChannelStateDesc": "Ringing", "BridgeId": "",
        },
        "variables": {
            "MDD_INBOUND_ATTACH": "0", "MDD_INBOUND_ARMED": "0",
            "MDD_INBOUND_SOURCE_ID": "", "MDD_INBOUND_OPERATION": "",
            "MDD_MEDIA_EPOCH": "", "MDD_INBOUND_WINNER_ID": "",
            "MDD_INBOUND_WINNER_CHANNEL": "",
            "MDD_INBOUND_ANSWER_RESULT": "waiting",
        },
    }


@pytest.mark.asyncio
async def test_incoming_prepare_allocates_at_most_three_non_billable_claimants():
    request = SimpleNamespace(cookies={}, headers={})
    record = incoming_record()
    runtime = {
        "running": True, "container_id": "a" * 64, "engine_run_id": "run-7",
        "media_websocket": True, "browser_inbound": True,
    }
    ami = SimpleNamespace(
        incoming_browser_owner_snapshot=AsyncMock(return_value=pristine_incoming_snapshot()))
    registry = browser_media.BrowserMediaRegistry()
    try:
        with patch.object(main_app, "_browser_media_cookie_subject",
                          return_value="subject"), \
                patch.object(main_app.cfg, "get_instance", return_value={"id": "7"}), \
                patch.object(main_app, "_line_admission_blocked",
                             AsyncMock(return_value=False)), \
                patch.object(main_app.hub.runtime, "get", AsyncMock(return_value=runtime)), \
                patch.object(main_app.hub, "ami_for", AsyncMock(return_value=ami)), \
                patch.object(main_app.store, "get_browser_call_exact",
                             return_value=record), \
                patch.object(main_app.browser_media, "registry", registry):
            results = [await main_app.api_browser_incoming_media_prepare(
                "7", "91", {"source_call_id": "run-7:171.7",
                              "engine_run_id": "run-7"}, request) for _ in range(3)]
            with pytest.raises(main_app.HTTPException) as fourth:
                await main_app.api_browser_incoming_media_prepare(
                    "7", "91", {"source_call_id": "run-7:171.7",
                                  "engine_run_id": "run-7"}, request)
        assert [item["claimants"] for item in results] == [1, 2, 3]
        assert fourth.value.status_code == 503
        assert "maximum media claimants" in str(fourth.value.detail)
        assert ami.incoming_browser_owner_snapshot.await_count == 3
        assert len(registry.inbound_claimants("7", "run-7", "run-7:171.7", 91)) == 3
    finally:
        await registry.close_all()
        await asyncio.sleep(0)


@pytest.mark.asyncio
async def test_incoming_prepare_partial_owner_transitions_to_ending_and_hangs_up_exact_call():
    request = SimpleNamespace(cookies={}, headers={})
    record = incoming_record()
    ending = {**record, "browser_state": "ending", "browser_revision": 1}
    runtime = {
        "running": True, "container_id": "a" * 64, "engine_run_id": "run-7",
        "media_websocket": True, "browser_inbound": True,
    }
    snapshot = pristine_incoming_snapshot()
    snapshot["variables"]["MDD_INBOUND_ARMED"] = "1"
    ami = SimpleNamespace(incoming_browser_owner_snapshot=AsyncMock(return_value=snapshot))
    registry = browser_media.BrowserMediaRegistry()
    try:
        with patch.object(main_app, "_browser_media_cookie_subject", return_value="subject"), \
                patch.object(main_app.cfg, "get_instance", return_value={"id": "7"}), \
                patch.object(main_app, "_line_admission_blocked",
                             AsyncMock(return_value=False)), \
                patch.object(main_app.hub.runtime, "get", AsyncMock(return_value=runtime)), \
                patch.object(main_app.hub, "ami_for", AsyncMock(return_value=ami)), \
                patch.object(main_app.store, "get_browser_call_exact", return_value=record), \
                patch.object(main_app.store, "transition_browser_call",
                             return_value=ending) as transition, \
                patch.object(main_app, "hangup_incoming_vowifi_call",
                             AsyncMock()) as hangup, \
                patch.object(main_app.browser_media, "registry", registry), \
                pytest.raises(main_app.HTTPException) as rejected:
            await main_app.api_browser_incoming_media_prepare(
                "7", "91", {"source_call_id": "run-7:171.7",
                              "engine_run_id": "run-7"}, request)
        assert rejected.value.status_code == 409
        transition.assert_called_once_with(
            "7", "91", "run-7:171.7", "run-7", expected_state="ringing",
            expected_revision=0, new_state="ending", status="ending")
        hangup.assert_awaited_once_with("7", "91", "run-7:171.7", "run-7")
        assert registry.inbound_claimants("7", "run-7", "run-7:171.7", 91) == []
    finally:
        await registry.close_all()
        await asyncio.sleep(0)


@pytest.mark.asyncio
async def test_incoming_prepare_unreadable_owner_is_retryable_and_never_hangs_up():
    request = SimpleNamespace(cookies={}, headers={})
    record = incoming_record()
    runtime = {
        "running": True, "container_id": "a" * 64, "engine_run_id": "run-7",
        "media_websocket": True, "browser_inbound": True,
    }
    ami = SimpleNamespace(incoming_browser_owner_snapshot=AsyncMock(return_value={
        "ok": False, "reason": "variable_unreadable"}))
    registry = browser_media.BrowserMediaRegistry()
    try:
        with patch.object(main_app, "_browser_media_cookie_subject", return_value="subject"), \
                patch.object(main_app.cfg, "get_instance", return_value={"id": "7"}), \
                patch.object(main_app, "_line_admission_blocked",
                             AsyncMock(return_value=False)), \
                patch.object(main_app.hub.runtime, "get", AsyncMock(return_value=runtime)), \
                patch.object(main_app.hub, "ami_for", AsyncMock(return_value=ami)), \
                patch.object(main_app.store, "get_browser_call_exact", return_value=record), \
                patch.object(main_app.store, "transition_browser_call") as transition, \
                patch.object(main_app, "hangup_incoming_vowifi_call",
                             AsyncMock()) as hangup, \
                patch.object(main_app.browser_media, "registry", registry), \
                pytest.raises(main_app.HTTPException) as rejected:
            await main_app.api_browser_incoming_media_prepare(
                "7", "91", {"source_call_id": "run-7:171.7",
                              "engine_run_id": "run-7"}, request)
        assert rejected.value.status_code == 409
        assert rejected.value.detail["code"] == "incoming_owner_unavailable"
        transition.assert_not_called()
        hangup.assert_not_awaited()
        assert registry.inbound_claimants("7", "run-7", "run-7:171.7", 91) == []
    finally:
        await registry.close_all()
        await asyncio.sleep(0)


@pytest.mark.asyncio
async def test_incoming_prepare_commits_a_full_ttl_after_slow_owner_precheck():
    request = SimpleNamespace(cookies={}, headers={})
    record = incoming_record()
    runtime = {
        "running": True, "container_id": "a" * 64, "engine_run_id": "run-7",
        "media_websocket": True, "browser_inbound": True,
    }
    registry = browser_media.BrowserMediaRegistry()

    async def slow_snapshot(_linkedid):
        session = registry.inbound_claimants("7", "run-7", "run-7:171.7", 91)[0]
        session.expires_at = time.monotonic() + 0.03
        await asyncio.sleep(0.015)
        return pristine_incoming_snapshot()

    ami = SimpleNamespace(incoming_browser_owner_snapshot=AsyncMock(
        side_effect=slow_snapshot))
    try:
        with patch.object(main_app, "_browser_media_cookie_subject", return_value="subject"), \
                patch.object(main_app.cfg, "get_instance", return_value={"id": "7"}), \
                patch.object(main_app, "_line_admission_blocked",
                             AsyncMock(return_value=False)), \
                patch.object(main_app.hub.runtime, "get", AsyncMock(return_value=runtime)), \
                patch.object(main_app.hub, "ami_for", AsyncMock(return_value=ami)), \
                patch.object(main_app.store, "get_browser_call_exact", return_value=record), \
                patch.object(main_app.browser_media, "registry", registry):
            result = await main_app.api_browser_incoming_media_prepare(
                "7", "91", {"source_call_id": "run-7:171.7",
                              "engine_run_id": "run-7"}, request)
        session = registry.get(result["session_id"])
        assert session is not None and session.committed_at > 0
        assert session.expires_at - time.monotonic() > 29.9
    finally:
        await registry.close_all()
        await asyncio.sleep(0)


@pytest.mark.asyncio
async def test_incoming_prepare_never_returns_a_reservation_that_expired_during_precheck():
    request = SimpleNamespace(cookies={}, headers={})
    record = incoming_record()
    runtime = {
        "running": True, "container_id": "a" * 64, "engine_run_id": "run-7",
        "media_websocket": True, "browser_inbound": True,
    }
    registry = browser_media.BrowserMediaRegistry()

    async def expire_snapshot(_linkedid):
        session = registry.inbound_claimants("7", "run-7", "run-7:171.7", 91)[0]
        session.expires_at = time.monotonic() - 1
        return pristine_incoming_snapshot()

    ami = SimpleNamespace(incoming_browser_owner_snapshot=AsyncMock(
        side_effect=expire_snapshot))
    try:
        with patch.object(main_app, "_browser_media_cookie_subject", return_value="subject"), \
                patch.object(main_app.cfg, "get_instance", return_value={"id": "7"}), \
                patch.object(main_app, "_line_admission_blocked",
                             AsyncMock(return_value=False)), \
                patch.object(main_app.hub.runtime, "get", AsyncMock(return_value=runtime)), \
                patch.object(main_app.hub, "ami_for", AsyncMock(return_value=ami)), \
                patch.object(main_app.store, "get_browser_call_exact", return_value=record), \
                patch.object(main_app.browser_media, "registry", registry), \
                pytest.raises(main_app.HTTPException) as expired:
            await main_app.api_browser_incoming_media_prepare(
                "7", "91", {"source_call_id": "run-7:171.7",
                              "engine_run_id": "run-7"}, request)
        assert expired.value.status_code == 409
        assert expired.value.detail["code"] == "incoming_claimant_expired"
        assert registry.inbound_claimants("7", "run-7", "run-7:171.7", 91) == []
    finally:
        await registry.close_all()
        await asyncio.sleep(0)


@pytest.mark.asyncio
async def test_stale_incoming_ticket_is_closed_before_runtime_or_ami_originate():
    registry = browser_media.BrowserMediaRegistry()
    session = await registry.allocate(
        iid="7", generation="a" * 64, engine_run_id="run-7",
        subject=browser_media.subject_digest("session"), purpose="inbound",
        backend_call_id=91, backend_revision=0, source_call_id="run-7:171.7")
    websocket = BrowserSessionWebSocket(session)
    stale = incoming_record(state="claiming", revision=1)
    with patch.object(main_app.auth, "session", return_value={"csrf": "x"}), \
            patch.object(main_app.media_ingress, "same_origin", return_value=True), \
            patch.object(main_app.browser_media, "registry", registry), \
            patch.object(main_app.store, "get_browser_call_exact", return_value=stale), \
            patch.object(main_app.hub.runtime, "get",
                         side_effect=AssertionError("runtime must not be read")), \
            patch.object(main_app, "OneShotAmiSession") as one_shot:
        await main_app.api_browser_media_ws(websocket, "7")
    one_shot.assert_not_called()
    assert websocket.accepted == [None]
    assert any(item.get("type") == "browser.media.error" for item in websocket.sent_json)
    assert registry.get(session.session_id) is None


@pytest.mark.asyncio
async def test_incoming_prepare_closes_claimant_if_engine_changes_during_allocation():
    request = SimpleNamespace(cookies={}, headers={})
    record = incoming_record()
    old_runtime = {
        "running": True, "container_id": "a" * 64, "engine_run_id": "run-7",
        "media_websocket": True, "browser_inbound": True,
    }
    new_runtime = {
        **old_runtime, "container_id": "b" * 64, "engine_run_id": "run-8",
    }
    ami = SimpleNamespace(
        incoming_browser_owner_snapshot=AsyncMock(return_value=pristine_incoming_snapshot()))
    registry = browser_media.BrowserMediaRegistry()
    try:
        with patch.object(main_app, "_browser_media_cookie_subject",
                          return_value="subject"), \
                patch.object(main_app.cfg, "get_instance", return_value={"id": "7"}), \
                patch.object(main_app, "_line_admission_blocked",
                             AsyncMock(return_value=False)), \
                patch.object(main_app.hub.runtime, "get", AsyncMock(
                    side_effect=[old_runtime, old_runtime, new_runtime])), \
                patch.object(main_app.hub, "ami_for", AsyncMock(return_value=ami)), \
                patch.object(main_app.store, "get_browser_call_exact",
                             return_value=record), \
                patch.object(main_app.browser_media, "registry", registry), \
                pytest.raises(main_app.HTTPException) as changed:
            await main_app.api_browser_incoming_media_prepare(
                "7", "91", {"source_call_id": "run-7:171.7",
                              "engine_run_id": "run-7"}, request)
        assert changed.value.status_code == 409
        assert registry.inbound_claimants("7", "run-8", "run-7:171.7", 91) == []
    finally:
        await registry.close_all()
        await asyncio.sleep(0)


@pytest.mark.asyncio
@pytest.mark.parametrize("state,status", (("claiming", 409), ("terminal", 404)))
async def test_incoming_prepare_non_ringing_state_never_reads_runtime_or_allocates(
        state, status):
    request = SimpleNamespace(cookies={}, headers={})
    with patch.object(main_app, "_browser_media_cookie_subject", return_value="subject"), \
            patch.object(main_app.store, "get_browser_call_exact",
                         return_value=incoming_record(state=state, revision=1)), \
            patch.object(main_app.hub.runtime, "get",
                         side_effect=AssertionError("runtime must not be read")), \
            patch.object(main_app.browser_media.registry, "allocate",
                         side_effect=AssertionError("claimant must not allocate")), \
            pytest.raises(main_app.HTTPException) as rejected:
        await main_app.api_browser_incoming_media_prepare(
            "7", "91", {"source_call_id": "run-7:171.7",
                          "engine_run_id": "run-7"}, request)
    assert rejected.value.status_code == status


def test_browser_media_prepare_subject_uses_valid_cookie_not_auth_headers():
    request = SimpleNamespace(
        cookies={main_app.auth.SESSION_COOKIE: "cookie-session"},
        headers={"x-mdd-session": "different-header-session",
                 "authorization": "Bearer different-bearer-session"},
    )
    with patch.object(main_app.auth, "session", return_value={"csrf": "ok"}) as lookup:
        subject = main_app._browser_media_cookie_subject(request)
    lookup.assert_called_once_with("cookie-session")
    assert subject == browser_media.subject_digest("cookie-session")


def test_browser_media_prepare_without_valid_cookie_fails_closed():
    request = SimpleNamespace(
        cookies={},
        headers={"x-mdd-session": "header-only-session",
                 "authorization": "Bearer bearer-only-session"},
    )
    with patch.object(main_app.auth, "session", return_value=None) as lookup, \
            pytest.raises(main_app.HTTPException) as exc:
        main_app._browser_media_cookie_subject(request)
    lookup.assert_called_once_with("")
    assert exc.value.status_code == 401
