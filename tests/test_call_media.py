import asyncio
import struct

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

        await manager.close(session.call_id)
        await asyncio.gather(agent, return_exceptions=True)
        assert ws.closed

    asyncio.run(scenario())
