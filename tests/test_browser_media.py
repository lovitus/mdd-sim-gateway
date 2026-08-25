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
from app.ami import browser_media_canary_action  # noqa: E402
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
    assert "/run/mdd-sim-gateway/engine-maintenance.json" in block
    assert "/run/mdd-sim-gateway/pcscf-rebind.json" in block
    assert 'MDD_ADMISSION(media_check)' in block
    assert block.index('MDD_ADMISSION(media_check)') < block.index("Echo()")
    assert "n(rebind-blocked),Hangup(41)" in block
    executable = "\n".join(line for line in block.splitlines()
                           if not line.lstrip().startswith(";"))
    for forbidden in ("include", "volte_ims", "Dial(", "MessageSend", "PJSIPRegister", "APDU"):
        assert forbidden not in executable


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
