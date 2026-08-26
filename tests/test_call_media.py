import asyncio
import json
from unittest.mock import AsyncMock, patch

import pytest

from control.app.call_media import CallMediaManager, MediaUnavailable


class FakeWebSocket:
    def __init__(self):
        self.incoming = asyncio.Queue()
        self.sent = []
        self.messages = []
        self.closed = False

    async def receive(self):
        return await self.incoming.get()

    async def send_bytes(self, value):
        self.sent.append(value)

    async def send_json(self, value):
        self.messages.append(value)

    async def close(self):
        self.closed = True


async def allocate(manager, **overrides):
    values = dict(owner_subject="subject", owner_token="a" * 32,
                  instance_iid="5", direction="out", number="+44123456789",
                  agent_session_id="agent-session")
    values.update(overrides)
    return await manager.allocate("8985000000000000000", **values)


async def wait_until(predicate, timeout=1):
    async with asyncio.timeout(timeout):
        while not predicate():
            await asyncio.sleep(0.005)


async def connect(session):
    agent, browser = FakeWebSocket(), FakeWebSocket()
    tasks = [asyncio.create_task(session.attach_agent(agent, session.token)),
             asyncio.create_task(session.attach_browser(browser, "subject", "a" * 32))]
    await wait_until(lambda: session.bridge_task is not None)
    return agent, browser, tasks


def browser_evidence(session, **overrides):
    values = dict(type="cellular.media.evidence", version=1, challenge=session.challenge,
                  capture_callbacks=4, playback_callbacks=4, played_frames=4)
    values.update(overrides)
    return values


def helper_evidence(**overrides):
    values = dict(type="audio.telemetry", capture_callbacks=4, playback_callbacks=4,
                  capture_bytes=1280, playback_bytes=1280)
    values.update(overrides)
    return values


@pytest.mark.asyncio
async def test_native_bridge_reframes_agent_callbacks_and_requires_actual_duplex_evidence():
    manager = CallMediaManager()
    with patch("asyncio.start_server", side_effect=AssertionError("no TCP media listener")):
        session = await allocate(manager)
    agent, browser, tasks = await connect(session)
    try:
        assert browser.messages[0]["type"] == "cellular.media.started"
        frame = b"\x01\x00" * 160
        # Common Agent callback: 640B/40ms. Partial callbacks retain their tail.
        await agent.incoming.put({"bytes": frame + frame[:80]})
        await agent.incoming.put({"bytes": frame[80:]})
        for _ in range(2):
            await browser.incoming.put({"bytes": frame})
        await wait_until(lambda: len(browser.sent) == 2 and len(agent.sent) == 2)
        assert browser.sent == [frame, frame]
        assert agent.sent == [frame, frame]
        assert not session.media_status()["ready"]
        await agent.incoming.put({"text": json.dumps(helper_evidence())})
        await browser.incoming.put({"text": json.dumps(browser_evidence(session))})
        await wait_until(lambda: session.media_status()["ready"])
        assert session.media_status()["evidence"]["browser_to_agent_frames"] == 2
        assert session.media_status()["evidence"]["agent_to_browser_frames"] == 2
    finally:
        await manager.close(session.call_id)
        await asyncio.gather(*tasks)
    assert agent.closed and browser.closed


@pytest.mark.asyncio
async def test_same_cookie_different_page_cannot_claim_or_replace_existing_browser():
    manager = CallMediaManager()
    session = await allocate(manager)
    agent, browser, tasks = await connect(session)
    try:
        for subject, token in (("subject", "b" * 32), ("another-cookie", "a" * 32)):
            with pytest.raises(MediaUnavailable, match="another browser"):
                await session.attach_browser(FakeWebSocket(), subject, token)
        with pytest.raises(MediaUnavailable, match="already attached"):
            await session.attach_browser(FakeWebSocket(), "subject", "a" * 32)
        assert session.browser_ws is browser
        assert not browser.closed and not agent.closed
    finally:
        await manager.close(session.call_id)
        await asyncio.gather(*tasks)


@pytest.mark.asyncio
async def test_frozen_counter_replays_and_one_way_audio_never_keep_media_ready():
    manager = CallMediaManager()
    session = await allocate(manager)
    session.agent_ws = FakeWebSocket()
    session.browser_ws = FakeWebSocket()
    session.challenge = "challenge"
    try:
        with patch("control.app.call_media.time.monotonic", return_value=100.0):
            session.browser_to_agent_frames = session.agent_to_browser_frames = 2
            session.browser_to_agent_at = session.agent_to_browser_at = 100.0
            session.record_helper_telemetry(helper_evidence())
            assert session.record_browser_evidence(browser_evidence(session))["ready"]
        with patch("control.app.call_media.time.monotonic", return_value=106.0):
            session.browser_to_agent_at = session.agent_to_browser_at = 106.0
            session.record_helper_telemetry(helper_evidence())
            assert not session.record_browser_evidence(browser_evidence(session))["ready"]
            assert session.browser_capture_growth_at == 100.0
            assert session.helper_capture_growth_at == 100.0
            with pytest.raises(MediaUnavailable, match="stale"):
                session.record_browser_evidence(browser_evidence(session, challenge="old"))
            with pytest.raises(MediaUnavailable, match="backwards"):
                session.record_browser_evidence(browser_evidence(session, capture_callbacks=3))
            session.record_helper_telemetry(helper_evidence(
                capture_callbacks=8, playback_callbacks=8, capture_bytes=2560, playback_bytes=2560))
            session.agent_to_browser_frames = 0
            assert not session.record_browser_evidence(browser_evidence(
                session, capture_callbacks=8, playback_callbacks=8, played_frames=8))["ready"]
    finally:
        await manager.close(session.call_id)


@pytest.mark.asyncio
@pytest.mark.parametrize("side,payload", [("browser", b"bad"), ("agent", b"bad"),
                                        ("agent", b"x" * 65536)])
async def test_invalid_or_backlogged_audio_closes_media_but_keeps_manager_occupancy(side, payload):
    manager = CallMediaManager()
    session = await allocate(manager)
    finalized = AsyncMock()
    session.orphan_handler = finalized
    agent, browser, tasks = await connect(session)
    await (browser if side == "browser" else agent).incoming.put({"bytes": payload})
    await asyncio.wait_for(session.closed.wait(), 1)
    await asyncio.gather(*tasks)
    await wait_until(lambda: finalized.await_count == 1)
    assert manager.for_iccid(session.iccid) is session
    await manager.close(session.call_id)


@pytest.mark.asyncio
@pytest.mark.parametrize("phase", ["signalling", "paid"])
@pytest.mark.parametrize("source", ["agent_block", "agent_callbacks", "browser"])
@pytest.mark.parametrize("frame_count", [8, 16])
async def test_paid_pcm_bursts_use_bounded_backpressure_without_losing_order(phase, source, frame_count):
    manager = CallMediaManager()
    session = await allocate(manager)
    if phase == "signalling":
        session.signalling_method = "call.dial"
        session.signalling_in_flight = True
    else:
        session.commit_result = {"ok": True}
    agent, browser, tasks = await connect(session)
    frames = [index.to_bytes(2, "little") * 160 for index in range(1, frame_count + 1)]
    try:
        if source == "agent_block":
            agent.incoming.put_nowait({"bytes": b"".join(frames)})
        elif source == "agent_callbacks":
            for index in range(0, frame_count, 2):
                agent.incoming.put_nowait({"bytes": b"".join(frames[index:index + 2])})
        else:
            for frame in frames:
                browser.incoming.put_nowait({"bytes": frame})
        output = agent.sent if source == "browser" else browser.sent
        await wait_until(lambda: len(output) == len(frames) or session.closed.is_set())
        assert not session.closed.is_set()
        assert output == frames
        assert session.expired_pcm_frames == {"uplink": 0, "downlink": 0}
    finally:
        await manager.close(session.call_id)
        await asyncio.gather(*tasks)


@pytest.mark.asyncio
async def test_paid_pcm_block_keeps_original_age_across_enqueue_waits():
    manager = CallMediaManager()
    session = await allocate(manager)
    session.commit_result = {"ok": True}
    finalized = AsyncMock()
    session.orphan_handler = finalized
    agent, browser, tasks = await connect(session)
    try:
        agent.incoming.put_nowait({"bytes": b"\x01\x00" * (160 * 40)})
        await wait_until(lambda: session.closed.is_set() or
                         getattr(session, "expired_pcm_frames", {}).get("downlink", 0) > 0)
        assert not session.closed.is_set()
        assert len(browser.sent) < 40
        assert not any(message.get("type") == "cellular.media.error" for message in browser.messages)
        fresh = b"\x03\x00" * 160
        agent.incoming.put_nowait({"bytes": fresh})
        await wait_until(lambda: fresh in browser.sent)
        assert browser.sent[-1] == fresh
        assert session.agent_to_browser_frames == len(browser.sent)
        finalized.assert_not_awaited()
        assert manager.for_iccid(session.iccid) is session
    finally:
        await manager.close(session.call_id)
        await asyncio.gather(*tasks)


@pytest.mark.asyncio
async def test_partial_agent_frame_retains_oldest_fragment_age():
    manager = CallMediaManager()
    session = await allocate(manager)
    session.commit_result = {"ok": True}
    agent, browser, tasks = await connect(session)
    try:
        agent.incoming.put_nowait({"bytes": b"\x01\x00" * 80})
        await asyncio.sleep(.55)
        agent.incoming.put_nowait({"bytes": b"\x02\x00" * 80})
        await wait_until(lambda: bool(browser.sent) or session.closed.is_set() or
                         getattr(session, "expired_pcm_frames", {}).get("downlink", 0) > 0)
        assert not session.closed.is_set() and not browser.sent
        assert session.expired_pcm_frames["downlink"] == 1
        assert session.agent_to_browser_frames == 0
        assert session.agent_to_browser_at == 0
    finally:
        await manager.close(session.call_id)
        await asyncio.gather(*tasks)


@pytest.mark.asyncio
async def test_browser_send_lock_cannot_turn_an_expired_frame_into_fresh_evidence(caplog):
    manager = CallMediaManager()
    session = await allocate(manager)
    session.commit_result = {"ok": True}
    agent, browser, tasks = await connect(session)
    old = b"\x01\x00" * 160
    fresh = b"\x04\x00" * 160
    try:
        async with session._browser_send_lock:
            agent.incoming.put_nowait({"bytes": old})
            await asyncio.sleep(.55)
        await wait_until(lambda: session.expired_pcm_frames["downlink"] == 1)
        assert not session.closed.is_set() and not browser.sent
        assert session.agent_to_browser_frames == 0 and session.agent_to_browser_at == 0
        assert "stage=browser_send_lock" in caplog.text
        assert session.token not in caplog.text and session.owner_token not in caplog.text
        agent.incoming.put_nowait({"bytes": fresh})
        await wait_until(lambda: browser.sent == [fresh])
        assert session.agent_to_browser_frames == 1
    finally:
        await manager.close(session.call_id)
        await asyncio.gather(*tasks)


@pytest.mark.asyncio
async def test_configured_1500ms_budget_allows_1200ms_block_without_discard():
    manager = CallMediaManager()
    session = await allocate(manager, pcm_buffer_ms=1500)
    session.commit_result = {"ok": True}
    agent, browser, tasks = await connect(session)
    frames = [index.to_bytes(2, "little") * 160 for index in range(1, 61)]
    try:
        agent.incoming.put_nowait({"bytes": b"".join(frames)})
        await wait_until(lambda: len(browser.sent) == 60 or session.closed.is_set(), timeout=2)
        assert not session.closed.is_set()
        assert browser.sent == frames
        assert session.expired_pcm_frames["downlink"] == 0
    finally:
        await manager.close(session.call_id)
        await asyncio.gather(*tasks)


@pytest.mark.asyncio
async def test_configured_queue_wait_is_not_cut_short_by_the_send_io_timeout():
    manager = CallMediaManager()
    session = await allocate(manager, pcm_buffer_ms=1500)
    session.commit_result = {"ok": True}
    agent, browser, tasks = await connect(session)
    frames = [index.to_bytes(2, "little") * 160 for index in range(1, 9)]
    try:
        async with session._browser_send_lock:
            agent.incoming.put_nowait({"bytes": b"".join(frames)})
            await asyncio.sleep(.7)
        await wait_until(lambda: len(browser.sent) == 8 or session.closed.is_set())
        assert not session.closed.is_set()
        assert browser.sent == frames
        assert session.expired_pcm_frames["downlink"] == 0
    finally:
        await manager.close(session.call_id)
        await asyncio.gather(*tasks)


@pytest.mark.asyncio
@pytest.mark.parametrize("stale_direction", ["uplink", "downlink", "both"])
async def test_expired_forwarding_is_not_replaced_by_fresh_telemetry(stale_direction):
    manager = CallMediaManager()
    session = await allocate(manager)
    session.agent_ws, session.browser_ws = FakeWebSocket(), FakeWebSocket()
    session.challenge = "challenge"
    try:
        with patch("control.app.call_media.time.monotonic", return_value=100.0):
            session.browser_to_agent_frames = session.agent_to_browser_frames = 2
            session.browser_to_agent_at = session.agent_to_browser_at = 100.0
            session.record_helper_telemetry(helper_evidence())
            assert session.record_browser_evidence(browser_evidence(session))["ready"]
        with patch("control.app.call_media.time.monotonic", return_value=106.0):
            if stale_direction == "uplink":
                session.agent_to_browser_at = 106.0
            elif stale_direction == "downlink":
                session.browser_to_agent_at = 106.0
            session.expired_pcm_frames["uplink"] = 200
            session.expired_pcm_frames["downlink"] = 200
            session.record_helper_telemetry(helper_evidence(
                capture_callbacks=8, playback_callbacks=8, capture_bytes=2560, playback_bytes=2560))
            media = session.record_browser_evidence(browser_evidence(
                session, capture_callbacks=8, playback_callbacks=8, played_frames=8))
            assert not media["ready"]
            assert media["evidence"]["browser_to_agent_frames"] == 2
            assert media["evidence"]["agent_to_browser_frames"] == 2
    finally:
        await manager.close(session.call_id)


@pytest.mark.asyncio
async def test_cancelling_paid_backpressure_closes_all_waiting_bridge_tasks():
    manager = CallMediaManager()
    session = await allocate(manager)
    session.commit_result = {"ok": True}
    agent, browser, tasks = await connect(session)
    entered, stalled = asyncio.Event(), asyncio.Event()

    async def blocked_send(_value):
        entered.set()
        await stalled.wait()

    browser.send_bytes = blocked_send
    agent.incoming.put_nowait({"bytes": b"\x01\x00" * (160 * 8)})
    await asyncio.wait_for(entered.wait(), 1)
    await asyncio.wait_for(manager.close(session.call_id), 1)
    await asyncio.wait_for(asyncio.gather(*tasks), 1)
    assert session.bridge_task.done() and agent.closed and browser.closed
    assert manager.for_iccid(session.iccid) is None


@pytest.mark.asyncio
@pytest.mark.parametrize("milliseconds", [200, 400, 500])
async def test_normal_agent_backlog_before_browser_upgrade_keeps_latest_bounded_warmup(milliseconds):
    manager = CallMediaManager()
    session = await allocate(manager)
    agent, browser = FakeWebSocket(), FakeWebSocket()
    agent_task = asyncio.create_task(session.attach_agent(agent, session.token))
    await session.agent_ready.wait()
    for index in range(milliseconds // 20):
        await agent.incoming.put({"bytes": index.to_bytes(2, "little") * 160})
    browser_task = asyncio.create_task(session.attach_browser(browser, "subject", "a" * 32))
    try:
        await wait_until(lambda: len(browser.sent) >= 2 or session.closed.is_set())
        assert not session.closed.is_set()
        await browser.incoming.put({"bytes": b"\x01\x00" * 160})
        await browser.incoming.put({"bytes": b"\x01\x00" * 160})
        await agent.incoming.put({"text": json.dumps(helper_evidence())})
        await browser.incoming.put({"text": json.dumps(browser_evidence(session))})
        await wait_until(lambda: session.media_status()["ready"])
        assert len(browser.sent) <= 6
        assert int.from_bytes(browser.sent[0][:2], "little") >= milliseconds // 20 - 6
        assert session.commit_result is None and not session.signalling_method
    finally:
        await manager.close(session.call_id)
        await asyncio.gather(agent_task, browser_task)


@pytest.mark.asyncio
async def test_browser_send_stall_is_bounded_and_requests_server_owned_release():
    manager = CallMediaManager()
    session = await allocate(manager)
    finalized = AsyncMock()
    session.orphan_handler = finalized
    agent, browser, tasks = await connect(session)
    stalled = asyncio.Event()

    async def blocked_send(_value):
        await stalled.wait()

    browser.send_bytes = blocked_send
    with patch("control.app.call_media.MEDIA_IO_TIMEOUT_SECONDS", 0.03):
        await agent.incoming.put({"bytes": b"\x01\x00" * 160})
        await asyncio.wait_for(session.closed.wait(), 1)
        await asyncio.gather(*tasks)
        await wait_until(lambda: finalized.await_count == 1)
    assert manager.for_iccid(session.iccid) is session
    await manager.close(session.call_id)


@pytest.mark.asyncio
async def test_manager_rejects_second_sim_owner_without_closing_first():
    manager = CallMediaManager()
    first = await allocate(manager)
    try:
        with pytest.raises(MediaUnavailable, match="already owns"):
            await allocate(manager, owner_token="b" * 32)
        assert manager.for_iccid(first.iccid) is first
        assert not first.closed.is_set()
    finally:
        await manager.close(first.call_id)


@pytest.mark.asyncio
async def test_sustained_pcm_renews_real_evidence_and_disconnect_has_one_release_owner():
    manager = CallMediaManager()
    session = await allocate(manager)
    finalized = AsyncMock()
    session.orphan_handler = finalized
    agent, browser, tasks = await connect(session)
    frame = b"\x01\x00" * 160
    deadline = asyncio.get_running_loop().time()
    try:
        for tick in range(120):
            await browser.incoming.put({"bytes": frame})
            if tick % 2 == 0:
                await agent.incoming.put({"bytes": frame * 2})
            if tick and tick % 10 == 0:
                await agent.incoming.put({"text": json.dumps(helper_evidence(
                    capture_callbacks=tick // 2, playback_callbacks=tick,
                    capture_bytes=tick * 320, playback_bytes=len(agent.sent) * 320))})
                challenge = next(message["challenge"] for message in reversed(browser.messages)
                                 if message.get("challenge"))
                await browser.incoming.put({"text": json.dumps(browser_evidence(
                    session, challenge=challenge, capture_callbacks=tick,
                    playback_callbacks=len(browser.sent), played_frames=len(browser.sent)))})
            deadline += 0.02
            await asyncio.sleep(max(0, deadline - asyncio.get_running_loop().time()))
        assert not session.closed.is_set()
        assert session.media_status()["ready"]
        assert len(browser.sent) >= 115 and len(agent.sent) >= 115
        assert any(message["type"] == "cellular.media.ready" for message in browser.messages)
        assert sum(message["type"] == "cellular.media.challenge" for message in browser.messages) >= 2
        await browser.incoming.put({"type": "websocket.disconnect"})
        await asyncio.wait_for(session.closed.wait(), 1)
        await asyncio.gather(*tasks)
        await wait_until(lambda: finalized.await_count == 1)
        assert manager.get(session.call_id) is session
    finally:
        await manager.close(session.call_id)
