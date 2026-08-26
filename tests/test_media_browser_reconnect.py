import asyncio
import time
from types import SimpleNamespace
from unittest.mock import AsyncMock, patch

import pytest

from control.app import browser_media, call_media


class Socket:
    def __init__(self):
        self.sent = []

    async def send_json(self, value): self.sent.append(value)
    async def send_bytes(self, value): self.sent.append(value)
    async def close(self, **_kwargs): pass


def make_native_ready(session, now):
    session.started = True
    session.asterisk_ws = Socket()
    session.browser_to_engine_frames = session.engine_to_browser_frames = 2
    session.browser_to_engine_at = session.engine_to_browser_at = now
    session.capture_callbacks = session.playback_callbacks = session.played_frames = 2
    session.evidence_at = session.challenge_ack_at = now
    session.asterisk_status_at = now
    assert session.status()["ready"] is True


@pytest.mark.asyncio
async def test_native_browser_reconnect_rotates_ticket_and_old_handler_cannot_detach_new():
    clock = [100.0]
    registry = browser_media.BrowserMediaRegistry()
    with patch.object(browser_media.time, "monotonic", lambda: clock[0]):
        session = await registry.allocate(
            iid="7", generation="g", engine_run_id="r", subject="subject",
            purpose="outbound", destination="+44123456789", call_token="a" * 32)
        old = Socket()
        await registry.claim_browser(
            session_id=session.session_id, ticket=session.ticket,
            subject="subject", websocket=old)
        old_epoch, resume = session.browser_connection_epoch, session.browser_resume_ticket
        make_native_ready(session, clock[0])
        assert await registry.detach_browser(session, old, old_epoch)
        assert session.browser_ws is None and not session.closed.is_set()
        deadline = session.browser_reconnect_deadline
        clock[0] = 106.0
        new = Socket()
        await registry.resume_browser(
            session_id=session.session_id, resume_ticket=resume,
            subject="subject", websocket=new)
        assert session.browser_ws is new
        assert session.browser_connection_epoch == old_epoch + 1
        assert session.browser_resume_ticket != resume
        assert session.browser_reconnect_deadline == deadline
        assert session.status()["ready"] is False
        assert await registry.acknowledge_browser_resume(
            session, new, session.browser_connection_epoch)
        assert not await registry.detach_browser(session, old, old_epoch)
        assert session.browser_ws is new
        with pytest.raises(browser_media.BrowserMediaUnavailable):
            await registry.resume_browser(
                session_id=session.session_id, resume_ticket=resume,
                subject="subject", websocket=Socket())
        session.browser_to_engine_frames = session.engine_to_browser_frames = 3
        session.browser_to_engine_at = session.engine_to_browser_at = clock[0]
        session.capture_callbacks = session.playback_callbacks = session.played_frames = 3
        session.evidence_at = session.challenge_ack_at = clock[0]
        session.asterisk_status_at = clock[0]
        assert session.status()["ready"] is True
        assert session.browser_reconnect_deadline == 0.0
        await registry.close(session)


@pytest.mark.asyncio
async def test_cellular_browser_reconnect_keeps_owner_and_deadline_but_requires_fresh_growth():
    clock = [200.0]
    session = call_media.MediaSession(
        "c" * 32, "iccid", "agent-token", "subject", "o" * 32,
        instance_iid="6", commit_result={"ok": True})
    session.orphan_handler = AsyncMock()
    agent, old = Socket(), Socket()
    session.agent_ws = agent
    with patch.object(call_media.time, "monotonic", lambda: clock[0]):
        initial = await session.attach_browser_connection(old, "subject", "o" * 32)
        session.browser_to_agent_frames = session.agent_to_browser_frames = 2
        session.browser_to_agent_at = session.agent_to_browser_at = clock[0]
        session.helper_capture_callbacks = session.helper_playback_callbacks = 2
        session.helper_capture_bytes = session.helper_playback_bytes = 640
        session.helper_capture_growth_at = session.helper_playback_growth_at = clock[0]
        session.browser_evidence = {
            "capture_callbacks": 2, "playback_callbacks": 2, "played_frames": 2}
        session.browser_evidence_at = session.browser_capture_growth_at = clock[0]
        session.browser_playback_growth_at = clock[0]
        assert session.media_status()["ready"] is True
        assert await session.detach_browser(old, initial["connection_epoch"])
        deadline = session.browser_reconnect_deadline
        assert not session.closed.is_set() and session.browser_ws is None
        clock[0] = 206.0
        new = Socket()
        resumed = await session.resume_browser_connection(
            new, "subject", "o" * 32, initial["resume_ticket"])
        assert resumed["resume_ticket"] != initial["resume_ticket"]
        assert session.browser_reconnect_deadline == deadline
        assert session.media_status()["ready"] is False
        assert await session.acknowledge_browser_resume(
            new, resumed["connection_epoch"])
        assert not await session.detach_browser(old, initial["connection_epoch"])
        assert session.browser_ws is new
        with pytest.raises(call_media.MediaUnavailable):
            await session.resume_browser_connection(
                Socket(), "subject", "o" * 32, initial["resume_ticket"])
        session.browser_to_agent_frames = session.agent_to_browser_frames = 3
        session.browser_to_agent_at = session.agent_to_browser_at = clock[0]
        session.helper_capture_callbacks = session.helper_playback_callbacks = 3
        session.helper_capture_bytes = session.helper_playback_bytes = 960
        session.helper_capture_growth_at = session.helper_playback_growth_at = clock[0]
        session.browser_evidence = {
            "capture_callbacks": 3, "playback_callbacks": 3, "played_frames": 3}
        session.browser_evidence_at = session.browser_capture_growth_at = clock[0]
        session.browser_playback_growth_at = clock[0]
        assert session.media_status()["ready"] is True
        assert session.browser_reconnect_deadline == 0.0
        await session.close()


@pytest.mark.asyncio
async def test_reconnect_deadline_expiry_has_one_server_owned_cleanup():
    session = call_media.MediaSession(
        "d" * 32, "iccid", "agent-token", "subject", "o" * 32,
        instance_iid="6", commit_result={"ok": True})
    session.agent_ws = Socket()
    old = Socket()
    cleanup = AsyncMock()
    session.orphan_handler = cleanup
    with patch.object(call_media, "CALL_HEARTBEAT_TIMEOUT_SECONDS", 0.02):
        initial = await session.attach_browser_connection(old, "subject", "o" * 32)
        session.last_media_healthy_at = time.monotonic()
        assert await session.detach_browser(old, initial["connection_epoch"])
        async with asyncio.timeout(1):
            while not cleanup.await_count:
                await asyncio.sleep(.005)
    cleanup.assert_awaited_once_with(session)


@pytest.mark.asyncio
async def test_unacknowledged_resume_cannot_suppress_fixed_expiry():
    session = call_media.MediaSession(
        "f" * 32, "iccid", "agent-token", "subject", "o" * 32,
        instance_iid="6", commit_result={"ok": True})
    session.agent_ws = Socket()
    old, replacement = Socket(), Socket()
    cleanup = AsyncMock()
    session.orphan_handler = cleanup
    with patch.object(call_media, "CALL_HEARTBEAT_TIMEOUT_SECONDS", 0.03):
        initial = await session.attach_browser_connection(old, "subject", "o" * 32)
        session.last_media_healthy_at = time.monotonic()
        assert await session.detach_browser(old, initial["connection_epoch"])
        await session.resume_browser_connection(
            replacement, "subject", "o" * 32, initial["resume_ticket"])
        assert session.browser_resume_pending is True
        async with asyncio.timeout(1):
            while not cleanup.await_count:
                await asyncio.sleep(.005)
    cleanup.assert_awaited_once_with(session)


@pytest.mark.asyncio
async def test_acknowledged_resume_without_fresh_media_still_expires_once():
    session = call_media.MediaSession(
        "9" * 32, "iccid", "agent-token", "subject", "o" * 32,
        instance_iid="6", commit_result={"ok": True})
    session.agent_ws = Socket()
    old, replacement = Socket(), Socket()
    cleanup = AsyncMock()
    session.orphan_handler = cleanup
    with patch.object(call_media, "CALL_HEARTBEAT_TIMEOUT_SECONDS", 0.03):
        initial = await session.attach_browser_connection(old, "subject", "o" * 32)
        session.last_media_healthy_at = time.monotonic()
        assert await session.detach_browser(old, initial["connection_epoch"])
        resumed = await session.resume_browser_connection(
            replacement, "subject", "o" * 32, initial["resume_ticket"])
        assert await session.acknowledge_browser_resume(
            replacement, resumed["connection_epoch"])
        async with asyncio.timeout(1):
            while not cleanup.await_count:
                await asyncio.sleep(.005)
    cleanup.assert_awaited_once_with(session)
