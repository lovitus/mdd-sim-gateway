"""Ephemeral, call-scoped bridge between Agent PCM/WSS and Asterisk AudioSocket."""

from __future__ import annotations

import asyncio
import hmac
import json
import secrets
import struct
import time
import uuid
from dataclasses import dataclass, field


class MediaUnavailable(RuntimeError):
    pass


@dataclass
class MediaSession:
    call_id: str
    iccid: str
    token: str
    browser_nonce: str
    audio_uuid: uuid.UUID
    server: asyncio.AbstractServer
    port: int
    extension: str = ""
    anchor_iid: str = ""
    anchor_generation: str = ""
    anchor_webrtc_port: int = 0
    instance_iid: str = ""
    direction: str = "out"
    number: str = ""
    agent_ws: object | None = None
    audio_reader: asyncio.StreamReader | None = None
    audio_writer: asyncio.StreamWriter | None = None
    agent_ready: asyncio.Event = field(default_factory=asyncio.Event)
    asterisk_ready: asyncio.Event = field(default_factory=asyncio.Event)
    media_prepared: asyncio.Event = field(default_factory=asyncio.Event)
    closed: asyncio.Event = field(default_factory=asyncio.Event)
    bridge_task: asyncio.Task | None = None
    expiry_task: asyncio.Task | None = None
    orphan_handler: object | None = None
    managed_finalized: bool = False
    release_attempts: int = 0
    release_result: dict | None = None
    release_operation_id: str = ""
    release_unknown: bool = False
    release_requested: bool = False
    release_state: str = ""
    release_deadline: float = 0.0
    release_coordinator_task: asyncio.Task | None = None
    termination_task: asyncio.Task | None = None
    lease_task: asyncio.Task | None = None
    lease_last_healthy_at: float = 0.0
    signalling_in_flight: bool = False
    signalling_method: str = ""
    signalling_operation_id: str = ""
    signalling_params: dict = field(default_factory=dict)
    signalling_recovery_task: asyncio.Task | None = None
    commit_result: dict | None = None
    commit_lock: asyncio.Lock = field(default_factory=asyncio.Lock)
    ring_result: dict | None = None
    ring_lock: asyncio.Lock = field(default_factory=asyncio.Lock)
    _attach_lock: asyncio.Lock = field(default_factory=asyncio.Lock)
    phase: str = "allocated"
    asterisk_to_agent_frames: int = 0
    asterisk_to_agent_bytes: int = 0
    asterisk_to_agent_at: float = 0.0
    agent_to_asterisk_frames: int = 0
    agent_to_asterisk_bytes: int = 0
    agent_to_asterisk_at: float = 0.0
    helper_capture_callbacks: int = 0
    helper_playback_callbacks: int = 0
    helper_capture_bytes: int = 0
    helper_playback_bytes: int = 0
    helper_telemetry_at: float = 0.0
    helper_capture_growth_at: float = 0.0
    helper_playback_growth_at: float = 0.0
    browser_evidence: dict = field(default_factory=dict)
    browser_evidence_at: float = 0.0
    cellular_state: str = ""

    def _refresh_prepared(self) -> None:
        """Derive readiness from call-scoped evidence; connections alone never qualify."""
        if self.closed.is_set():
            self.phase = "closed"
            self.media_prepared.clear()
            return
        now = time.monotonic()
        fresh = lambda stamp: stamp > 0 and now - stamp <= 5.0
        ready = bool(
            self.agent_ws is not None and self.audio_writer is not None and
            self.asterisk_to_agent_frames >= 2 and
            self.agent_to_asterisk_frames >= 2 and
            self.helper_capture_callbacks >= 2 and
            self.helper_playback_callbacks >= 2 and
            self.helper_capture_bytes > 0 and self.helper_playback_bytes > 0 and
            self.browser_evidence.get("ready") is True and
            fresh(self.asterisk_to_agent_at) and fresh(self.agent_to_asterisk_at) and
            fresh(self.helper_capture_growth_at) and
            fresh(self.helper_playback_growth_at) and
            fresh(self.browser_evidence_at))
        if ready:
            if self.signalling_in_flight:
                self.phase = "signalling"
            elif self.commit_result is not None:
                self.phase = ("media_flowing" if self.cellular_state == "active"
                              else "signalling")
            else:
                self.phase = "media_prepared"
            self.media_prepared.set()
        else:
            self.phase = "degraded" if self.commit_result is not None else "preparing"
            self.media_prepared.clear()

    def record_browser_evidence(self, nonce: str, evidence: dict) -> dict:
        if not hmac.compare_digest(self.browser_nonce, str(nonce or "")):
            raise MediaUnavailable("browser media evidence nonce does not match this call")
        required = (
            evidence.get("connection_state") in {"connected", "completed"} and
            evidence.get("local_track_live") is True and
            evidence.get("remote_track_live") is True and
            evidence.get("playback_started") is True and
            int(evidence.get("outbound_packets_delta") or 0) > 0 and
            int(evidence.get("outbound_bytes_delta") or 0) > 0 and
            int(evidence.get("inbound_packets_delta") or 0) > 0 and
            int(evidence.get("inbound_bytes_delta") or 0) > 0)
        if not required:
            raise MediaUnavailable("browser WebRTC did not prove live bidirectional audio")
        self.browser_evidence = {
            "ready": True,
            "connection_state": evidence["connection_state"],
            "local_track_live": True,
            "remote_track_live": True,
            "playback_started": True,
            "outbound_packets_delta": int(evidence["outbound_packets_delta"]),
            "outbound_bytes_delta": int(evidence["outbound_bytes_delta"]),
            "inbound_packets_delta": int(evidence["inbound_packets_delta"]),
            "inbound_bytes_delta": int(evidence["inbound_bytes_delta"]),
        }
        self.browser_evidence_at = time.monotonic()
        self._refresh_prepared()
        return self.media_status()

    def record_helper_telemetry(self, telemetry: dict) -> dict:
        if telemetry.get("type") != "audio.telemetry":
            raise MediaUnavailable("Agent sent unsupported audio telemetry")
        try:
            values = {
                name: int(telemetry.get(name) or 0)
                for name in ("capture_callbacks", "playback_callbacks",
                             "capture_bytes", "playback_bytes")
            }
        except (TypeError, ValueError) as exc:
            raise MediaUnavailable("Agent sent invalid audio telemetry") from exc
        if any(value < 0 for value in values.values()):
            raise MediaUnavailable("Agent sent invalid audio telemetry")
        previous = {
            "capture_callbacks": self.helper_capture_callbacks,
            "playback_callbacks": self.helper_playback_callbacks,
            "capture_bytes": self.helper_capture_bytes,
            "playback_bytes": self.helper_playback_bytes,
        }
        if any(values[name] < previous[name] for name in values):
            raise MediaUnavailable("Agent audio telemetry counters moved backwards")
        now = time.monotonic()
        if (values["capture_callbacks"] > previous["capture_callbacks"] and
                values["capture_bytes"] > previous["capture_bytes"]):
            self.helper_capture_growth_at = now
        if (values["playback_callbacks"] > previous["playback_callbacks"] and
                values["playback_bytes"] > previous["playback_bytes"]):
            self.helper_playback_growth_at = now
        self.helper_capture_callbacks = values["capture_callbacks"]
        self.helper_playback_callbacks = values["playback_callbacks"]
        self.helper_capture_bytes = values["capture_bytes"]
        self.helper_playback_bytes = values["playback_bytes"]
        self.helper_telemetry_at = now
        self._refresh_prepared()
        return self.media_status()

    def media_status(self) -> dict:
        self._refresh_prepared()
        return {
            "phase": self.phase,
            "ready": self.media_prepared.is_set(),
            "evidence": {
                "agent_connected": self.agent_ws is not None,
                "asterisk_connected": self.audio_writer is not None,
                "asterisk_to_agent_frames": self.asterisk_to_agent_frames,
                "agent_to_asterisk_frames": self.agent_to_asterisk_frames,
                "helper_capture_callbacks": self.helper_capture_callbacks,
                "helper_playback_callbacks": self.helper_playback_callbacks,
                "helper_capture_bytes": self.helper_capture_bytes,
                "helper_playback_bytes": self.helper_playback_bytes,
                "browser": dict(self.browser_evidence),
            },
        }

    async def attach_agent(self, websocket, token: str) -> None:
        if not hmac.compare_digest(self.token, str(token or "")):
            raise MediaUnavailable("invalid or expired media token")
        async with self._attach_lock:
            if self.closed.is_set() or self.agent_ws is not None:
                raise MediaUnavailable("media Agent is already attached or the call expired")
            self.agent_ws = websocket
            self.agent_ready.set()
            self.phase = "preparing"
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
                    if len(payload) % 2:
                        raise MediaUnavailable("Asterisk sent an invalid PCM frame")
                    await self.agent_ws.send_bytes(payload)
                    self.asterisk_to_agent_frames += 1
                    self.asterisk_to_agent_bytes += len(payload)
                    self.asterisk_to_agent_at = time.monotonic()
                    self._refresh_prepared()

        async def agent_to_asterisk():
            while True:
                message = await self.agent_ws.receive()
                if message.get("type") == "websocket.disconnect":
                    return
                payload = message.get("bytes")
                text = message.get("text")
                if text:
                    try:
                        telemetry = json.loads(text)
                    except (TypeError, json.JSONDecodeError):
                        raise MediaUnavailable("Agent sent invalid audio telemetry")
                    if telemetry.get("type") != "audio.telemetry":
                        continue
                    self.record_helper_telemetry(telemetry)
                    continue
                if not payload:
                    continue
                if len(payload) > 65535 or len(payload) % 2:
                    raise MediaUnavailable("Agent sent an invalid PCM frame")
                self.audio_writer.write(struct.pack("!BH", 0x10, len(payload)) + payload)
                await self.audio_writer.drain()
                self.agent_to_asterisk_frames += 1
                self.agent_to_asterisk_bytes += len(payload)
                self.agent_to_asterisk_at = time.monotonic()
                self._refresh_prepared()

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
        self.phase = "closed"
        self.media_prepared.clear()
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
        current = asyncio.current_task()
        if self.expiry_task and self.expiry_task is not current:
            self.expiry_task.cancel()
            await asyncio.gather(self.expiry_task, return_exceptions=True)
        # Python 3.14 wait_closed() also waits for active client callbacks. Signal/close both
        # peers first so those callbacks can return; waiting before this point deadlocks.
        await self.server.wait_closed()
        if from_bridge and self.orphan_handler and not self.managed_finalized:
            asyncio.create_task(
                self.orphan_handler(self), name=f"cellular-media-orphan-{self.call_id[:8]}")


class CallMediaManager:
    def __init__(self):
        self._sessions: dict[str, MediaSession] = {}
        self._by_iccid: dict[str, str] = {}
        self._lock = asyncio.Lock()

    async def allocate(self, iccid: str, bind_host: str = "0.0.0.0") -> MediaSession:
        call_id = uuid.uuid4().hex
        token = secrets.token_urlsafe(32)
        browser_nonce = secrets.token_urlsafe(24)
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
        session = MediaSession(
            call_id, str(iccid), token, browser_nonce, audio_uuid, server, port)
        conflict = None
        async with self._lock:
            previous_id = self._by_iccid.get(str(iccid))
            previous = self._sessions.get(previous_id or "")
            if previous:
                conflict = previous
            else:
                self._sessions[call_id] = session
                self._by_iccid[str(iccid)] = call_id
        if conflict:
            await session.close()
            raise MediaUnavailable(
                "another prepared or active call already owns this SIM media session")
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
            session.managed_finalized = True
            current = asyncio.current_task()
            if session.expiry_task and session.expiry_task is not current:
                session.expiry_task.cancel()
                await asyncio.gather(session.expiry_task, return_exceptions=True)
            if (session.termination_task and session.termination_task is not current
                    and session.release_state != "terminated"):
                session.termination_task.cancel()
                await asyncio.gather(session.termination_task, return_exceptions=True)
            if (session.release_coordinator_task
                    and session.release_coordinator_task is not current
                    and session.release_state != "terminated"
                    and not session.release_requested):
                session.release_coordinator_task.cancel()
                await asyncio.gather(session.release_coordinator_task,
                                     return_exceptions=True)
            if session.lease_task and session.lease_task is not current:
                session.lease_task.cancel()
                await asyncio.gather(session.lease_task, return_exceptions=True)
            if (session.signalling_recovery_task and
                    session.signalling_recovery_task is not current):
                session.signalling_recovery_task.cancel()
                await asyncio.gather(session.signalling_recovery_task,
                                     return_exceptions=True)
            await session.close()

    def sessions(self) -> list[MediaSession]:
        return list(self._sessions.values())

    async def close_all(self) -> None:
        async with self._lock:
            sessions = list(self._sessions.values())
            self._sessions.clear()
            self._by_iccid.clear()
        await asyncio.gather(*(session.close() for session in sessions),
                             return_exceptions=True)


manager = CallMediaManager()
