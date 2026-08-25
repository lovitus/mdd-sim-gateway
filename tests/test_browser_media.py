import asyncio
import inspect
import json
import pathlib
import sys
import unittest
import uuid
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

    async def send_json(self, value):
        self.json.append(value)

    async def send_bytes(self, value):
        self.binary.append(bytes(value))


class BrowserMediaRegistryTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.registry = browser_media.BrowserMediaRegistry(capacity=16)
        self.engine_ws = FakeWebSocket()
        self.engine = browser_media.EngineMediaConnection(
            iid="7", generation="a" * 64, engine_run_id="run-7",
            websocket=self.engine_ws)
        await self.registry.attach_engine(self.engine)

    async def asyncTearDown(self):
        await self.registry.close_all()

    async def allocate(self):
        return await self.registry.allocate(
            iid="7", generation="a" * 64, engine_run_id="run-7",
            subject=browser_media.subject_digest("admin-session"))

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

    async def test_reservation_and_bidirectional_fresh_evidence_are_required(self):
        session = await self.allocate()
        browser_ws = FakeWebSocket()
        ticket = session.ticket
        await self.registry.claim_browser(
            session_id=session.session_id, ticket=ticket,
            subject=browser_media.subject_digest("admin-session"), websocket=browser_ws)

        reserve = asyncio.create_task(self.engine.reserve(session))
        for _ in range(10):
            if self.engine_ws.json:
                break
            await asyncio.sleep(0)
        request = self.engine_ws.json[-1]
        self.assertEqual(request["audio_uuid"], str(session.audio_uuid))
        self.assertTrue(self.engine.acknowledge({
            "audio_uuid": str(session.audio_uuid), "accepted": True}))
        await reserve

        session.started = True
        session.challenge = "fresh-challenge"
        self.registry.start_browser_pump(session)
        for _ in range(2):
            await self.registry.forward_browser_pcm(session, b"\0" * 320)
            await self.registry.handle_engine_pcm(
                self.engine, session.audio_uuid.bytes + b"\0" * 320)
        await asyncio.sleep(0.05)
        status = session.record_browser_evidence({
            "type": "browser.media.evidence", "version": 1,
            "challenge": "fresh-challenge", "capture_callbacks": 2,
            "playback_callbacks": 2, "played_frames": 2,
        })
        self.assertTrue(status["ready"])
        self.assertEqual(len(browser_ws.binary), 2)
        self.assertEqual(len(self.engine_ws.binary), 2)

    async def test_stale_engine_generation_and_seventeenth_session_fail_closed(self):
        with self.assertRaises(browser_media.BrowserMediaUnavailable):
            await self.registry.allocate(
                iid="7", generation="b" * 64, engine_run_id="run-7", subject="x")
        sessions = [await self.allocate() for _ in range(16)]
        with self.assertRaises(browser_media.BrowserMediaUnavailable):
            await self.allocate()
        await self.registry.close(sessions[0], "test")
        replacement = await self.allocate()
        self.assertIsNotNone(replacement)

    async def test_frame_size_counter_rollback_and_unknown_uuid_are_rejected(self):
        session = await self.allocate()
        session.browser_ws = FakeWebSocket()
        session.started = True
        session.challenge = "one"
        with self.assertRaises(browser_media.BrowserMediaUnavailable):
            await self.registry.forward_browser_pcm(session, b"short")
        self.assertFalse(await self.registry.handle_engine_pcm(self.engine, b"x" * 336))
        session.capture_callbacks = 3
        with self.assertRaises(browser_media.BrowserMediaUnavailable):
            session.record_browser_evidence({
                "type": "browser.media.evidence", "version": 1, "challenge": "one",
                "capture_callbacks": 2, "playback_callbacks": 0, "played_frames": 0,
            })

    async def test_engine_disconnect_closes_only_its_sessions(self):
        session = await self.allocate()
        await self.registry.detach_engine(self.engine)
        self.assertTrue(session.closed.is_set())
        self.assertIsNone(self.registry.get(session.session_id))

    async def test_close_is_idempotent_and_releases_exact_uuid_once(self):
        session = await self.allocate()
        await self.registry.close(session, "browser disconnected")
        await self.registry.close(session, "late timeout")
        releases = [item for item in self.engine_ws.json
                    if item.get("type") == "engine.media.release"]
        self.assertEqual(releases, [{
            "type": "engine.media.release", "version": 1,
            "audio_uuid": str(session.audio_uuid),
        }])

    async def test_late_pcm_and_ack_do_not_close_an_unrelated_session(self):
        first = await self.allocate()
        second = await self.allocate()
        second.browser_ws = FakeWebSocket()
        await self.registry.close(first, "first completed")
        self.assertFalse(await self.registry.handle_engine_pcm(
            self.engine, first.audio_uuid.bytes + b"\0" * 320))
        self.assertFalse(self.engine.acknowledge({
            "audio_uuid": str(first.audio_uuid), "accepted": True}))
        self.assertFalse(self.engine.closed.is_set())
        self.assertFalse(second.closed.is_set())

    async def test_malformed_late_ack_still_fails_the_protocol(self):
        with self.assertRaises(browser_media.BrowserMediaUnavailable):
            self.engine.acknowledge({"audio_uuid": "not-a-uuid", "accepted": True})
        with self.assertRaises(browser_media.BrowserMediaUnavailable):
            self.engine.acknowledge({"audio_uuid": str(uuid.uuid4()), "accepted": "yes"})

    async def test_expiry_task_finishes_after_normal_close_without_second_release(self):
        session = await self.allocate()
        with patch.object(main_app.browser_media, "registry", self.registry):
            expiry = asyncio.create_task(main_app._expire_browser_media_session(session))
            await self.registry.close(session, "normal close")
            await asyncio.wait_for(expiry, timeout=0.2)
        releases = [item for item in self.engine_ws.json
                    if item.get("type") == "engine.media.release"]
        self.assertEqual(len(releases), 1)


def test_control_message_parser_is_bounded():
    assert browser_media.parse_text_message('{"version":1}') == {"version": 1}
    with pytest.raises(browser_media.BrowserMediaUnavailable):
        browser_media.parse_text_message(json.dumps({"x": "z" * 5000}))


def test_canary_ami_and_dialplan_have_no_user_dialable_fields_or_carrier_context():
    source = inspect.getsource(browser_media_canary_action)
    assert 'AudioSocket/127.0.0.1:9073/' in source
    assert '"Context": "browser-media-canary"' in source
    assert '"Exten": "echo"' in source
    assert list(inspect.signature(browser_media_canary_action).parameters) == [
            "audio_uuid", "channel_id"]

    dialplan = (ROOT / "engine/templates/extensions.conf.j2").read_text(encoding="utf-8")
    block = dialplan.split("[browser-media-canary]", 1)[1].split("[from-local]", 1)[0]
    assert "TIMEOUT(absolute)=10" in block and "Echo()" in block and "Hangup()" in block
    executable = "\n".join(line for line in block.splitlines()
                           if not line.lstrip().startswith(";"))
    for forbidden in ("include", "volte_ims", "Dial(", "MessageSend", "PJSIPRegister", "APDU"):
        assert forbidden not in executable


class RejectingWebSocket:
    def __init__(self, *, cookie="", origin="https://gateway.test",
                 host="gateway.test", engine_token=""):
        self.cookies = {"mdd_session": cookie} if cookie else {}
        self.headers = {"origin": origin, "host": host,
                        "x-mdd-engine-token": engine_token}
        self.query_params = {}
        self.closed = []

    async def close(self, code=1000, reason=""):
        self.closed.append((code, reason))


class EngineHelloWebSocket(RejectingWebSocket):
    def __init__(self, *, token, iid, run_id):
        super().__init__(engine_token=token)
        self.query_params = {"iid": iid, "engine_run_id": run_id}
        self.accepted = False

    async def accept(self):
        self.accepted = True

    async def receive_text(self):
        return json.dumps({
            "type": "engine.media.hello", "version": 1,
            "iid": self.query_params["iid"],
            "engine_run_id": self.query_params["engine_run_id"],
            "listen_port": 9073, "capacity": 16,
        })


@pytest.mark.asyncio
async def test_browser_websocket_rejects_missing_login_before_accept_or_runtime_work():
    websocket = RejectingWebSocket()
    with patch.object(main_app.auth, "session", return_value=None):
        await main_app.api_browser_media_ws(websocket, "7")
    assert websocket.closed[0][0] == 4401


@pytest.mark.asyncio
async def test_browser_websocket_requires_strict_same_origin():
    websocket = RejectingWebSocket(cookie="session", origin="https://other.test")
    with patch.object(main_app.auth, "session", return_value={"csrf": "x"}), \
            patch.object(main_app.media_ingress, "same_origin", return_value=False):
        await main_app.api_browser_media_ws(websocket, "7")
    assert websocket.closed[0][0] == 4403


@pytest.mark.asyncio
async def test_engine_websocket_rejects_token_before_docker_runtime_lookup():
    websocket = RejectingWebSocket(engine_token="wrong")
    websocket.query_params = {"iid": "7", "engine_run_id": "run-7"}
    with patch.object(main_app.cfg, "internal_event_token", return_value="expected"), \
            patch.object(main_app.hub.runtime, "get", side_effect=AssertionError("runtime lookup")):
        await main_app.api_engine_media_ws(websocket)
    assert websocket.closed[0][0] == 4403


@pytest.mark.asyncio
async def test_late_old_engine_hello_cannot_replace_current_generation_or_close_session():
    registry = browser_media.BrowserMediaRegistry()
    current_ws = FakeWebSocket()
    current = browser_media.EngineMediaConnection(
        iid="7", generation="b" * 64, engine_run_id="run-b", websocket=current_ws)
    await registry.attach_engine(current)
    session = await registry.allocate(
        iid="7", generation="b" * 64, engine_run_id="run-b", subject="subject-b")
    late = EngineHelloWebSocket(token="engine-token", iid="7", run_id="run-a")
    runtime = AsyncMock(side_effect=[
        {"running": True, "container_id": "a" * 64, "engine_run_id": "run-a"},
        {"running": True, "container_id": "b" * 64, "engine_run_id": "run-b"},
    ])
    try:
        with patch.object(main_app.cfg, "internal_event_token", return_value="engine-token"), \
                patch.object(main_app.hub.runtime, "get", runtime), \
                patch.object(main_app.browser_media, "registry", registry):
            await main_app.api_engine_media_ws(late)
        assert late.accepted
        assert late.closed == [(4409, "stale Engine media generation")]
        assert registry.engine("7") is current
        assert not current.closed.is_set()
        assert not session.closed.is_set()
    finally:
        await registry.close_all()


def test_websocket_handler_reserves_before_one_shot_ami_originate():
    source = inspect.getsource(main_app.api_browser_media_ws)
    reserve = source.index("await current_engine.reserve(session)")
    one_shot = source.index("async with OneShotAmiSession(")
    originate = source.index("browser_media_canary_action(")
    started = source.index("session.started = True")
    assert reserve < one_shot < originate < started


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
