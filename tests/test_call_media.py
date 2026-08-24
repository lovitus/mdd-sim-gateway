import asyncio
import struct
from unittest.mock import patch

from control.app.call_media import CallMediaManager


class FakeWebSocket:
    def __init__(self):
        self.incoming = asyncio.Queue()
        self.sent = []
        self.closed = False

    async def receive(self):
        return await self.incoming.get()

    async def send_bytes(self, value):
        self.sent.append(value)

    async def close(self):
        self.closed = True


def test_media_bridge_forwards_pcm_in_both_directions_and_rejects_wrong_uuid():
    async def scenario():
        manager = CallMediaManager()
        session = await manager.allocate("8985000000000000000", "127.0.0.1")
        ws = FakeWebSocket()
        agent = asyncio.create_task(session.attach_agent(ws, session.token))
        await asyncio.wait_for(session.agent_ready.wait(), 1)

        wrong_reader, wrong_writer = await asyncio.open_connection("127.0.0.1", session.port)
        wrong_writer.write(struct.pack("!BH", 1, 16) + b"x" * 16)
        await wrong_writer.drain()
        assert await asyncio.wait_for(wrong_reader.read(), 1) == b""

        reader, writer = await asyncio.open_connection("127.0.0.1", session.port)
        writer.write(struct.pack("!BH", 1, 16) + session.audio_uuid.bytes)
        await writer.drain()
        await asyncio.wait_for(session.asterisk_ready.wait(), 1)

        downlink = b"\x01\x00" * 160
        writer.write(struct.pack("!BH", 0x10, len(downlink)) + downlink)
        await writer.drain()
        for _ in range(20):
            if ws.sent:
                break
            await asyncio.sleep(0.01)
        assert ws.sent == [downlink]

        uplink = b"\x02\x00" * 160
        await ws.incoming.put({"type": "websocket.receive", "bytes": uplink})
        assert await asyncio.wait_for(reader.readexactly(3), 1) == struct.pack(
            "!BH", 0x10, len(uplink))
        assert await asyncio.wait_for(reader.readexactly(len(uplink)), 1) == uplink

        # Socket attachment and PCM forwarding alone are deliberately insufficient.  The
        # helper must prove callbacks/consumption and the authenticated browser must prove
        # fresh RTP growth in both directions for this exact call.
        assert not session.media_prepared.is_set()
        await ws.incoming.put({
            "type": "websocket.receive",
            "text": '{"type":"audio.telemetry","capture_callbacks":4,'
                    '"playback_callbacks":4,"capture_bytes":1280,'
                    '"playback_bytes":1280}',
        })
        writer.write(struct.pack("!BH", 0x10, len(downlink)) + downlink)
        writer.write(struct.pack("!BH", 0x10, len(downlink)) + downlink)
        await writer.drain()
        await ws.incoming.put({"type": "websocket.receive", "bytes": uplink})
        await ws.incoming.put({"type": "websocket.receive", "bytes": uplink})
        for _ in range(20):
            if session.agent_to_asterisk_frames >= 2:
                break
            await asyncio.sleep(0.01)
        session.record_browser_evidence(session.browser_nonce, {
            "connection_state": "connected",
            "local_track_live": True,
            "remote_track_live": True,
            "playback_started": True,
            "outbound_packets_delta": 20,
            "outbound_bytes_delta": 6400,
            "inbound_packets_delta": 20,
            "inbound_bytes_delta": 6400,
        })
        await asyncio.wait_for(session.media_prepared.wait(), 1)
        assert session.media_status()["phase"] == "media_prepared"

        await manager.close(session.call_id)
        await asyncio.gather(agent, return_exceptions=True)
        assert ws.closed

    asyncio.run(scenario())


def test_browser_evidence_is_call_scoped_and_fail_closed():
    async def scenario():
        manager = CallMediaManager()
        session = await manager.allocate("8985000000000000001", "127.0.0.1")
        try:
            evidence = {
                "connection_state": "connected",
                "local_track_live": True,
                "remote_track_live": True,
                "playback_started": True,
                "outbound_packets_delta": 1,
                "outbound_bytes_delta": 320,
                "inbound_packets_delta": 1,
                "inbound_bytes_delta": 320,
            }
            try:
                session.record_browser_evidence("wrong", evidence)
                assert False, "wrong nonce unexpectedly accepted"
            except Exception as exc:
                assert "nonce" in str(exc)
            try:
                session.record_browser_evidence(
                    session.browser_nonce, {**evidence, "inbound_bytes_delta": 0})
                assert False, "one-way browser evidence unexpectedly accepted"
            except Exception as exc:
                assert "bidirectional" in str(exc)
            assert not session.media_prepared.is_set()
        finally:
            session.audio_writer = None
            session.agent_ws = None
            await manager.close(session.call_id)

    asyncio.run(scenario())


def test_helper_readiness_requires_monotonic_counter_growth_not_repeated_telemetry():
    async def scenario():
        manager = CallMediaManager()
        session = await manager.allocate("8985000000000000002", "127.0.0.1")
        try:
            session.agent_ws = object()
            session.audio_writer = object()
            session.asterisk_to_agent_frames = 2
            session.agent_to_asterisk_frames = 2
            session.asterisk_to_agent_at = 100.0
            session.agent_to_asterisk_at = 100.0
            session.browser_evidence = {"ready": True}
            session.browser_evidence_at = 100.0
            session.helper_capture_callbacks = 4
            session.helper_playback_callbacks = 4
            session.helper_capture_bytes = 1280
            session.helper_playback_bytes = 1280
            session.helper_capture_growth_at = 100.0
            session.helper_playback_growth_at = 100.0
            with patch("control.app.call_media.time.monotonic", return_value=104.0):
                assert session.media_status()["ready"] is True
            frozen = {
                "type": "audio.telemetry", "capture_callbacks": 4,
                "playback_callbacks": 4, "capture_bytes": 1280,
                "playback_bytes": 1280,
            }
            with patch("control.app.call_media.time.monotonic", return_value=106.0):
                assert session.record_helper_telemetry(frozen)["ready"] is False
            assert session.helper_telemetry_at == 106.0
            assert session.helper_capture_growth_at == 100.0
            assert session.helper_playback_growth_at == 100.0
            try:
                session.record_helper_telemetry({**frozen, "capture_callbacks": 3})
                assert False, "counter rollback unexpectedly accepted"
            except Exception as exc:
                assert "backwards" in str(exc)
        finally:
            session.audio_writer = None
            session.agent_ws = None
            await manager.close(session.call_id)

    asyncio.run(scenario())


def test_manager_rejects_duplicate_sim_media_without_closing_existing_session():
    async def scenario():
        manager = CallMediaManager()
        first = await manager.allocate("8985000000000000000", "127.0.0.1")
        try:
            try:
                await manager.allocate("8985000000000000000", "127.0.0.1")
                assert False, "duplicate allocation unexpectedly succeeded"
            except Exception as exc:
                assert "already owns" in str(exc)
            assert manager.for_iccid(first.iccid) is first
            assert not first.closed.is_set()
        finally:
            await manager.close(first.call_id)

    asyncio.run(scenario())
