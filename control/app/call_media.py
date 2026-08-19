"""Ephemeral, call-scoped bridge between Agent PCM/WSS and Asterisk AudioSocket."""

from __future__ import annotations

import asyncio
import hmac
import secrets
import struct
import uuid
from dataclasses import dataclass, field


class MediaUnavailable(RuntimeError):
    pass


@dataclass
class MediaSession:
    call_id: str
    iccid: str
    token: str
    audio_uuid: uuid.UUID
    server: asyncio.AbstractServer
    port: int
    extension: str = ""
    anchor_iid: str = ""
    instance_iid: str = ""
    direction: str = "out"
    number: str = ""
    agent_ws: object | None = None
    audio_reader: asyncio.StreamReader | None = None
    audio_writer: asyncio.StreamWriter | None = None
    agent_ready: asyncio.Event = field(default_factory=asyncio.Event)
    asterisk_ready: asyncio.Event = field(default_factory=asyncio.Event)
    closed: asyncio.Event = field(default_factory=asyncio.Event)
    bridge_task: asyncio.Task | None = None
    commit_result: dict | None = None
    commit_lock: asyncio.Lock = field(default_factory=asyncio.Lock)
    ring_result: dict | None = None
    ring_lock: asyncio.Lock = field(default_factory=asyncio.Lock)
    _attach_lock: asyncio.Lock = field(default_factory=asyncio.Lock)

    async def attach_agent(self, websocket, token: str) -> None:
        if not hmac.compare_digest(self.token, str(token or "")):
            raise MediaUnavailable("invalid or expired media token")
        async with self._attach_lock:
            if self.closed.is_set() or self.agent_ws is not None:
                raise MediaUnavailable("media Agent is already attached or the call expired")
            self.agent_ws = websocket
            self.agent_ready.set()
            self._maybe_start()
        await self.closed.wait()

    async def attach_audiosocket(self, reader: asyncio.StreamReader,
                                 writer: asyncio.StreamWriter) -> None:
        try:
            kind, length = struct.unpack("!BH", await asyncio.wait_for(
                reader.readexactly(3), timeout=8))
            payload = await asyncio.wait_for(reader.readexactly(length), timeout=8)
            if kind != 0x01 or payload != self.audio_uuid.bytes:
                raise MediaUnavailable("AudioSocket UUID handshake does not match this call")
            async with self._attach_lock:
                if self.closed.is_set() or self.audio_writer is not None:
                    raise MediaUnavailable("Asterisk media is already attached or the call expired")
                self.audio_reader, self.audio_writer = reader, writer
                self.asterisk_ready.set()
                self._maybe_start()
            await self.closed.wait()
        finally:
            if self.audio_writer is writer and not self.closed.is_set():
                await self.close()

    def _maybe_start(self) -> None:
        if self.agent_ws is not None and self.audio_writer is not None and self.bridge_task is None:
            self.bridge_task = asyncio.create_task(self._bridge(),
                                                   name=f"cellular-media-{self.call_id[:8]}")

    async def _bridge(self) -> None:
        async def asterisk_to_agent():
            while True:
                header = await self.audio_reader.readexactly(3)
                kind, length = struct.unpack("!BH", header)
                payload = await self.audio_reader.readexactly(length) if length else b""
                if kind == 0x00:
                    return
                if kind == 0x10 and payload:
                    await self.agent_ws.send_bytes(payload)

        async def agent_to_asterisk():
            while True:
                message = await self.agent_ws.receive()
                if message.get("type") == "websocket.disconnect":
                    return
                payload = message.get("bytes")
                if not payload:
                    continue
                if len(payload) > 65535 or len(payload) % 2:
                    raise MediaUnavailable("Agent sent an invalid PCM frame")
                self.audio_writer.write(struct.pack("!BH", 0x10, len(payload)) + payload)
                await self.audio_writer.drain()

        tasks = [asyncio.create_task(asterisk_to_agent()),
                 asyncio.create_task(agent_to_asterisk())]
        try:
            await asyncio.wait(tasks, return_when=asyncio.FIRST_COMPLETED)
        finally:
            for task in tasks:
                if not task.done():
                    task.cancel()
            await asyncio.gather(*tasks, return_exceptions=True)
            await self.close(from_bridge=True)

    async def close(self, *, from_bridge: bool = False) -> None:
        if self.closed.is_set():
            return
        self.closed.set()
        self.server.close()
        if self.audio_writer:
            try:
                self.audio_writer.write(b"\x00\x00\x00")
                await self.audio_writer.drain()
            except Exception:
                pass
            self.audio_writer.close()
            try:
                await self.audio_writer.wait_closed()
            except Exception:
                pass
        if self.agent_ws:
            try:
                await self.agent_ws.close()
            except Exception:
                pass
        if self.bridge_task and not from_bridge:
            self.bridge_task.cancel()
            await asyncio.gather(self.bridge_task, return_exceptions=True)
        # Python 3.14 wait_closed() also waits for active client callbacks. Signal/close both
        # peers first so those callbacks can return; waiting before this point deadlocks.
        await self.server.wait_closed()


class CallMediaManager:
    def __init__(self):
        self._sessions: dict[str, MediaSession] = {}
        self._by_iccid: dict[str, str] = {}
        self._lock = asyncio.Lock()

    async def allocate(self, iccid: str, bind_host: str = "0.0.0.0") -> MediaSession:
        call_id = uuid.uuid4().hex
        token = secrets.token_urlsafe(32)
        audio_uuid = uuid.uuid4()

        async def accept(reader: asyncio.StreamReader, writer: asyncio.StreamWriter):
            session = self._sessions.get(call_id)
            if not session:
                writer.close()
                await writer.wait_closed()
                return
            try:
                await session.attach_audiosocket(reader, writer)
            except Exception:
                writer.close()
                try:
                    await writer.wait_closed()
                except Exception:
                    pass

        server = await asyncio.start_server(accept, bind_host, 0)
        port = int(server.sockets[0].getsockname()[1])
        session = MediaSession(call_id, str(iccid), token, audio_uuid, server, port)
        async with self._lock:
            previous_id = self._by_iccid.get(str(iccid))
            previous = self._sessions.get(previous_id or "")
            if previous:
                await previous.close()
                self._sessions.pop(previous.call_id, None)
            self._sessions[call_id] = session
            self._by_iccid[str(iccid)] = call_id
        return session

    def get(self, call_id: str) -> MediaSession | None:
        return self._sessions.get(str(call_id or ""))

    def for_iccid(self, iccid: str) -> MediaSession | None:
        return self.get(self._by_iccid.get(str(iccid or ""), ""))

    async def close(self, call_id: str) -> None:
        async with self._lock:
            session = self._sessions.pop(str(call_id or ""), None)
            if session and self._by_iccid.get(session.iccid) == session.call_id:
                self._by_iccid.pop(session.iccid, None)
        if session:
            await session.close()

    async def close_all(self) -> None:
        async with self._lock:
            sessions = list(self._sessions.values())
            self._sessions.clear()
            self._by_iccid.clear()
        await asyncio.gather(*(session.close() for session in sessions),
                             return_exceptions=True)


manager = CallMediaManager()
