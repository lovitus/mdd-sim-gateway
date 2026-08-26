"""Native VoWiFi must tolerate legal PCM frames arriving in one TCP burst."""
import asyncio

import pytest

from control.app import browser_media


class AudioSocket:
    def __init__(self):
        self.frames = []

    async def send_bytes(self, payload):
        self.frames.append(payload)

    async def close(self, **_kwargs):
        pass


@pytest.mark.asyncio
@pytest.mark.parametrize('count', [8, 16])
async def test_native_browser_pcm_burst_drains_without_ending_call(count):
    registry = browser_media.BrowserMediaRegistry()
    session = await registry.allocate(iid='1', generation='generation',
        engine_run_id='run', subject='subject')
    websocket = AudioSocket()
    session.asterisk_ws = websocket
    frames = [bytes([number]) * browser_media.PCM_FRAME_BYTES for number in range(count)]
    registry.start_browser_pump(session)
    try:
        await asyncio.sleep(0)
        for payload in frames:
            await registry.forward_browser_pcm(session, payload)
        async with asyncio.timeout(2):
            while len(websocket.frames) < count:
                await asyncio.sleep(.01)
        assert websocket.frames == frames
        assert session.browser_to_engine_frames == count
        assert not session.closed.is_set()
        assert session.browser_pcm.maxsize == (session.pcm_buffer_ms + 19) // 20
    finally:
        await registry.close_all()


@pytest.mark.asyncio
@pytest.mark.parametrize('budget,expected', [(500, 0), (1000, 1), (1500, 1)])
async def test_native_pcm_age_is_checked_after_send_lock_with_session_budget(budget, expected):
    registry = browser_media.BrowserMediaRegistry()
    session = await registry.allocate(iid='1', generation='g', engine_run_id='r',
                                     subject='s', pcm_buffer_ms=budget)
    websocket = AudioSocket()
    session.asterisk_ws = websocket
    await session.asterisk_send_lock.acquire()
    registry.start_browser_pump(session)
    try:
        await registry.forward_browser_pcm(session, bytes(320))
        await asyncio.sleep(.65)
        session.asterisk_send_lock.release()
        await asyncio.sleep(.04)
        assert len(websocket.frames) == expected
        assert session.browser_to_engine_frames == expected
        assert session.expired_browser_pcm_frames == 1 - expected
        if not expected:
            assert session.browser_to_engine_at == 0
        assert not session.closed.is_set()
    finally:
        await registry.close_all()


@pytest.mark.asyncio
async def test_native_full_queue_keeps_latest_without_blocking_or_faking_progress():
    registry = browser_media.BrowserMediaRegistry()
    session = await registry.allocate(iid='1', generation='g', engine_run_id='r', subject='s')
    session.asterisk_ws = AudioSocket()
    try:
        frames = [bytes([n]) * 320 for n in range(session.browser_pcm.maxsize + 8)]
        async with asyncio.timeout(.1):
            for frame in frames:
                await registry.forward_browser_pcm(session, frame)
        assert session.overflow_browser_pcm_frames == 8
        assert session.expired_browser_pcm_frames == 0
        retained = [session.browser_pcm.get_nowait() for _ in range(session.browser_pcm.maxsize)]
        assert [payload for _, payload in retained] == frames[8:]
        assert all(at > 0 for at, _ in retained)
        assert session.browser_to_engine_frames == 0
        assert session.browser_to_engine_at == 0
        await registry.forward_browser_pcm(session, b'X' * 320)
        await registry.close(session)
        registry.start_browser_pump(session)
        await session.pcm_pump_task
        assert session.browser_to_engine_frames == 0
    finally:
        await registry.close_all()


@pytest.mark.asyncio
async def test_failed_native_downlink_is_not_forwarding_evidence():
    registry = browser_media.BrowserMediaRegistry()
    session = await registry.allocate(iid='1', generation='g', engine_run_id='r', subject='s')
    session.asterisk_ws = AudioSocket()
    class FailedSocket(AudioSocket):
        async def send_bytes(self, payload):
            raise ConnectionError('closed')
    session.browser_ws = FailedSocket()
    try:
        with pytest.raises(ConnectionError):
            await registry.handle_asterisk_pcm(session, bytes(320))
        assert session.engine_to_browser_frames == session.engine_to_browser_at == 0
    finally:
        await registry.close_all()
