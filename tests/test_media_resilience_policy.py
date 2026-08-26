"""Execute media recovery policy with real sessions and a bounded virtual clock.

No HTTP, Docker, Agent RPC or paid signalling is performed. The inbound test extracts
only the active lease loop, avoiding mocks for its unrelated claim/Answer workflow.
"""
import ast
import asyncio
import inspect
import textwrap
from types import SimpleNamespace
from unittest.mock import Mock

import pytest

from control.app import browser_media, main


class StopFixture(BaseException):
    pass


class Clock:
    def __init__(self):
        self.now = 100.0
        self.after_advance = None

    def monotonic(self):
        return self.now

    async def advance(self, seconds):
        self.now = round(self.now + seconds, 6)
        if self.now > 150:
            raise StopFixture("policy did not finish within the fixture bound")
        if self.after_advance:
            result = self.after_advance()
            if inspect.isawaitable(result):
                await result


class VirtualAsyncio:
    def __init__(self, clock, *, block_status_send=False):
        self.clock = clock
        self.block_status_send = block_status_send
        self.send_timeouts = []

    def __getattr__(self, name):
        return getattr(asyncio, name)

    async def sleep(self, seconds):
        await self.clock.advance(seconds)

    async def wait_for(self, awaitable, timeout):
        frame = getattr(awaitable, "cr_frame", None)
        local = frame.f_locals if frame else {}
        event = local.get("self")
        if isinstance(event, asyncio.Event) and not event.is_set():
            awaitable.close()
            await self.clock.advance(timeout)
            raise asyncio.TimeoutError
        if (self.block_status_send and
                (local.get("payload") or {}).get("command") == "GET_STATUS"):
            awaitable.close()
            self.send_timeouts.append(timeout)
            await self.clock.advance(timeout)
            raise asyncio.TimeoutError
        return await awaitable


class Socket:
    def __init__(self, on_json=None):
        self.messages, self.frames = [], []
        self.on_json = on_json

    async def send_json(self, value):
        self.messages.append(value)
        if self.on_json:
            self.on_json(value)

    async def send_bytes(self, value):
        self.frames.append(value)

    async def close(self, **_kwargs):
        pass


def fresh_media(session, clock):
    session.started = True
    session.browser_to_engine_frames += 2
    session.engine_to_browser_frames += 2
    session.browser_to_engine_at = session.engine_to_browser_at = clock.now
    session.asterisk_status_at = clock.now
    session.asterisk_xoff = False
    session.issue_challenge()
    session.record_browser_evidence({
        "type": "browser.media.evidence", "version": 1, "challenge": session.challenge,
        "capture_callbacks": session.capture_callbacks + 2,
        "playback_callbacks": session.playback_callbacks + 2,
        "played_frames": session.played_frames + 2})


async def environment(monkeypatch, purpose="outbound", **kwargs):
    clock = Clock()
    monkeypatch.setattr(main, "time", clock)
    monkeypatch.setattr(browser_media, "time", clock)
    virtual = VirtualAsyncio(clock, **kwargs)
    monkeypatch.setattr(main, "asyncio", virtual)
    registry = browser_media.BrowserMediaRegistry()
    monkeypatch.setattr(browser_media, "registry", registry)
    session = await registry.allocate(
        iid="7", generation="g", engine_run_id="run", subject="subject",
        purpose=purpose, destination="+441234567890", call_token="t" * 32,
        backend_call_id=1, backend_revision=0, source_call_id="run:incoming")
    session.phase = "active"
    session.browser_ws, session.asterisk_ws = Socket(), Socket()
    session.asterisk_channel_id = session.channel_id
    fresh_media(session, clock)
    return SimpleNamespace(clock=clock, virtual=virtual, registry=registry, session=session)


def inbound_active_loop():
    tree = ast.parse(textwrap.dedent(inspect.getsource(main._run_browser_inbound_owner_locked)))
    loops = [node for node in ast.walk(tree) if isinstance(node, ast.While)
             and isinstance(node.body[0], ast.If)
             and "_browser_inbound_owner_record(session, {'active'})" in ast.unparse(node.body[0].test)]
    assert len(loops) == 1
    wrapper = ast.parse(
        "async def active_loop(session, lease, pair, bridge_id, last_media_renewal):\n    pass\n").body[0]
    wrapper.body = loops
    scope = dict(vars(main))
    exec(compile(ast.fix_missing_locations(ast.Module(body=[wrapper], type_ignores=[])),
                 "<production-inbound-active-loop>", "exec"), scope)
    return scope["active_loop"]


async def drive_lease(env, monkeypatch, purpose, *, recover_at=None, fault=None):
    session, clock = env.session, env.clock
    renewals, snapshots, revoked = [], [], []
    admission = {"active": True}
    identity = {"session": session, "session_id": session.session_id,
                "operation_id": session.operation_id, "media_epoch": session.media_epoch}
    runtime = {"running": True, "container_id": "g", "engine_run_id": "run",
               "browser_outbound": True}
    if fault == "closed":
        session.closed.set()
    elif fault == "generation":
        runtime["container_id"] = "replacement"
    elif fault == "identity":
        if purpose == "outbound":
            session.operation_id = "other-owner"
    elif fault == "phase":
        session.phase = "ending"

    async def renew(*_args):
        renewals.append(clock.now)
        if len(renewals) == 1:
            session.asterisk_xoff = True
        else:
            admission["active"] = False
            session.abort_requested.set()
        return {"ok": True} if purpose == "inbound" else True

    def advance():
        if recover_at is not None and clock.now >= recover_at and len(renewals) == 1:
            fresh_media(session, clock)
    clock.after_advance = advance

    if purpose == "outbound":
        async def ami_for(*_args):
            return SimpleNamespace(renew_channel_absolute_timeout=renew)

        async def runtime_get(*_args, **_kwargs):
            return runtime

        monkeypatch.setattr(main, "hub", SimpleNamespace(
            ami_for=ami_for, runtime=SimpleNamespace(get=runtime_get)))
        monkeypatch.setattr(main, "media_admission", SimpleNamespace(
            authorization_active=lambda *_args: admission["active"],
            close_call=lambda *args: revoked.append((clock.now, args))))
        hangup = Mock()
        monkeypatch.setattr(main, "_schedule_native_browser_hangup", hangup)
        await main._renew_softphone_call_lease("t" * 32, "7", "g", session.channel_id, identity)
    else:
        async def current(_session):
            return runtime["container_id"] == session.generation

        async def snapshot(*_args):
            snapshots.append(clock.now)
            return {"ok": True, "owner_matches": fault != "bridge", "bridge_id": "bridge",
                    "ims_up": True, "winner_up": True, "variables": {"ims": {
                        "MDD_INBOUND_ARMED": "0", "MDD_INBOUND_ANSWER_RESULT": "answered"}}}

        monkeypatch.setattr(main, "_browser_media_generation_current", current)
        monkeypatch.setattr(main, "_browser_inbound_owner_record", lambda *_args:
                            None if fault in {"identity", "phase"} else {"browser_state": "active"})
        lease = SimpleNamespace(begin_round=lambda *_args: None,
                                browser_inbound_pair_snapshot=snapshot,
                                renew_browser_inbound_timeouts=renew)
        await inbound_active_loop()(session, lease, {}, "bridge", clock.now)
        hangup = None
    return SimpleNamespace(renewals=renewals, snapshots=snapshots, revoked=revoked, hangup=hangup)


@pytest.mark.asyncio
@pytest.mark.parametrize("purpose", ["outbound", "inbound"])
async def test_native_recovers_six_seconds_after_last_renewal_without_early_cleanup(monkeypatch, purpose):
    env = await environment(monkeypatch, purpose)
    try:
        result = await drive_lease(env, monkeypatch, purpose, recover_at=106.0)
        assert result.renewals == [100.0, 106.0]
        assert not env.session.closed.is_set()
        assert not result.revoked
        if result.hangup:
            result.hangup.assert_not_called()
        else:
            assert result.snapshots == [100.0, 106.0], "recovery proves the pair before renewal"
    finally:
        await env.registry.close_all()


@pytest.mark.asyncio
@pytest.mark.parametrize("purpose", ["outbound", "inbound"])
async def test_native_missing_media_uses_one_fixed_ten_second_renewal_anchor(monkeypatch, purpose):
    env = await environment(monkeypatch, purpose)
    try:
        result = await drive_lease(env, monkeypatch, purpose)
        assert result.renewals == [100.0]
        assert env.clock.now == 110.0
        if purpose == "outbound":
            assert len(result.revoked) == 1
            result.hangup.assert_called_once_with(env.session)
            assert env.session.closed.is_set()
        # Inbound returns from this exact active loop to the existing owner finally/cleanup.
    finally:
        await env.registry.close_all()


@pytest.mark.asyncio
@pytest.mark.parametrize("purpose", ["outbound", "inbound"])
@pytest.mark.parametrize("fault", ["identity", "phase", "generation", "closed"])
async def test_native_identity_or_closed_never_uses_media_grace(monkeypatch, purpose, fault):
    env = await environment(monkeypatch, purpose)
    try:
        result = await drive_lease(env, monkeypatch, purpose, fault=fault)
        assert result.renewals == []
        assert env.clock.now == 100.0
        if purpose == "outbound":
            assert len(result.revoked) == 1
    finally:
        await env.registry.close_all()


@pytest.mark.asyncio
async def test_inbound_wrong_bridge_is_rejected_before_any_timeout_renewal(monkeypatch):
    env = await environment(monkeypatch, "inbound")
    try:
        result = await drive_lease(env, monkeypatch, "inbound", fault="bridge")
        assert result.snapshots == [100.0] and not result.renewals
        assert env.clock.now == 100.0
    finally:
        await env.registry.close_all()


def status_message(session, queue=0, **flags):
    return {"event": "STATUS", "channel_id": session.channel_id, "queue_length": queue,
            "queue_full": False, "media_paused": False, **flags}


@pytest.mark.asyncio
async def test_status_six_second_loss_recovers_without_closing(monkeypatch):
    env = await environment(monkeypatch)
    gets = []
    def respond(command):
        if command["command"] == "GET_STATUS":
            gets.append(env.clock.now)
            if env.clock.now >= 106:
                env.registry.handle_asterisk_control(env.session, status_message(env.session))
    env.session.asterisk_ws = Socket(respond)
    def finish():
        if env.session.asterisk_status_at >= 106:
            raise StopFixture
    env.clock.after_advance = finish
    try:
        with pytest.raises(StopFixture):
            await main._browser_media_asterisk_status(env.session)
        assert gets == [100.0, 102.0, 104.0, 106.0]
        assert env.session.asterisk_status_at == 106.0
        assert not env.session.closed.is_set()
    finally:
        await env.registry.close_all()


@pytest.mark.asyncio
@pytest.mark.parametrize("previous_status,expected", [(0.0, 110.0), (97.0, 107.0)])
async def test_status_loss_never_slides_its_last_valid_status_deadline(monkeypatch, previous_status, expected):
    env = await environment(monkeypatch)
    env.session.asterisk_status_at = previous_status
    await main._browser_media_asterisk_status(env.session)
    assert env.clock.now == expected
    assert env.session.closed.is_set()
    assert env.session.close_reason == "Asterisk media status failed"


@pytest.mark.asyncio
async def test_status_write_wait_is_inside_the_same_recovery_deadline(monkeypatch):
    env = await environment(monkeypatch, block_status_send=True)
    await main._browser_media_asterisk_status(env.session)
    assert env.virtual.send_timeouts == [10.0]
    assert env.clock.now == 110.0 and env.session.closed.is_set()


@pytest.mark.asyncio
async def test_flush_is_one_shot_and_does_not_invent_queue_or_media_progress(monkeypatch):
    env = await environment(monkeypatch)
    session, clock, registry = env.session, env.clock, env.registry
    original = (session.browser_to_engine_frames, session.engine_to_browser_frames,
                session.browser_to_engine_at, session.engine_to_browser_at)
    commands = []
    gets = [0]
    high = session.browser_pcm.maxsize + 1
    def respond(command):
        commands.append(command["command"])
        if command["command"] == "GET_STATUS":
            gets[0] += 1
            registry.handle_asterisk_control(session, status_message(session, high))
        elif command["command"] == "FLUSH_MEDIA":
            assert session.asterisk_flush_pending and session.asterisk_queue_length == high
    session.asterisk_ws = Socket(respond)
    def finish():
        if gets[0] == 2:
            raise StopFixture
    clock.after_advance = finish
    try:
        with pytest.raises(StopFixture):
            await main._browser_media_asterisk_status(session)
        assert commands.count("FLUSH_MEDIA") == 1
        assert session.asterisk_queue_length == high and session.asterisk_flush_pending
        assert not await session.send_asterisk_pcm(bytes(320), received_at=clock.now)
        registry.handle_asterisk_control(session, {"event": "MEDIA_XOFF", "channel_id": session.channel_id})
        registry.handle_asterisk_control(session, status_message(session, 0, queue_full=True))
        assert session.asterisk_xoff and session.asterisk_flush_pending
        registry.handle_asterisk_control(session, {"event": "MEDIA_XON", "channel_id": session.channel_id})
        assert session.asterisk_flush_pending, "XON alone is not proof of the configured queue bound"
        registry.handle_asterisk_control(session, status_message(session, media_paused=True))
        assert not session.asterisk_flush_pending and not session.asterisk_xoff
        assert session.asterisk_media_paused and not session.status()["ready"]
        registry.handle_asterisk_control(session, status_message(session))
        assert session.status()["ready"]
        assert (session.browser_to_engine_frames, session.engine_to_browser_frames,
                session.browser_to_engine_at, session.engine_to_browser_at) == original
    finally:
        await registry.close_all()
