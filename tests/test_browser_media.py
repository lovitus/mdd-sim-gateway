import asyncio
import base64
import inspect
import json
import pathlib
import sys
import time
import unittest
from types import SimpleNamespace
from unittest.mock import AsyncMock, patch

import pytest

ROOT = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "control"))

from app import browser_media  # noqa: E402
from app.ami import (OneShotAmiSession, browser_media_canary_action,
                     browser_media_outbound_warmup_action)  # noqa: E402
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
