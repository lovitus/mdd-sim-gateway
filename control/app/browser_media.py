"""Short-lived browser PCM sessions carried only by the management WS/WSS origin.

This module is deliberately independent from ``call_media``.  The latter owns a paid
cellular call and an Agent audio helper; this registry owns only a non-billable Asterisk Echo
canary.  A browser session is generation-fenced to one persistent Engine relay and never
contains a carrier number, dialplan context or arbitrary AMI fields.
"""

from __future__ import annotations

import asyncio
import hashlib
import hmac
import json
import secrets
import time
import uuid
from dataclasses import dataclass, field


PCM_FRAME_BYTES = 320
MAX_SESSIONS = 16
SESSION_TTL_SECONDS = 30.0
EVIDENCE_FRESH_SECONDS = 5.0


class BrowserMediaUnavailable(RuntimeError):
    pass


def subject_digest(token: str) -> str:
    return hashlib.sha256(str(token or "").encode("utf-8")).hexdigest()


@dataclass
class BrowserMediaSession:
    session_id: str
    ticket: str
    subject: str
    iid: str
    generation: str
    engine_run_id: str
    audio_uuid: uuid.UUID
    channel_id: str
    created_at: float = field(default_factory=time.monotonic)
    browser_ws: object | None = None
    engine: "EngineMediaConnection | None" = None
    closed: asyncio.Event = field(default_factory=asyncio.Event)
    ready: asyncio.Event = field(default_factory=asyncio.Event)
    send_lock: asyncio.Lock = field(default_factory=asyncio.Lock)
    close_lock: asyncio.Lock = field(default_factory=asyncio.Lock)
    browser_to_engine_frames: int = 0
    engine_to_browser_frames: int = 0
    browser_to_engine_at: float = 0.0
    engine_to_browser_at: float = 0.0
    capture_callbacks: int = 0
    playback_callbacks: int = 0
    played_frames: int = 0
    evidence_at: float = 0.0
    challenge: str = ""
    previous_challenge: str = ""
    previous_challenge_at: float = 0.0
    challenge_ack_at: float = 0.0
    started: bool = False
    close_reason: str = ""
    browser_pcm: asyncio.Queue = field(default_factory=lambda: asyncio.Queue(maxsize=12))
    pcm_pump_task: asyncio.Task | None = None

    def expired(self) -> bool:
        return time.monotonic() - self.created_at > SESSION_TTL_SECONDS

    def status(self) -> dict:
        now = time.monotonic()
        fresh = lambda stamp: stamp > 0 and now - stamp <= EVIDENCE_FRESH_SECONDS
        is_ready = bool(
            not self.closed.is_set() and self.started and
            self.browser_to_engine_frames >= 2 and self.engine_to_browser_frames >= 2 and
            self.capture_callbacks >= 2 and self.playback_callbacks >= 2 and
            self.played_frames >= 2 and fresh(self.browser_to_engine_at) and
            fresh(self.engine_to_browser_at) and fresh(self.evidence_at) and
            fresh(self.challenge_ack_at))
        if is_ready:
            self.ready.set()
        else:
            self.ready.clear()
        return {
            "type": "browser.media.status",
            "version": 1,
            "ready": is_ready,
            "phase": "closed" if self.closed.is_set() else (
                "ready" if is_ready else "testing" if self.started else "preparing"),
            "evidence": {
                "browser_to_engine_frames": self.browser_to_engine_frames,
                "engine_to_browser_frames": self.engine_to_browser_frames,
                "capture_callbacks": self.capture_callbacks,
                "playback_callbacks": self.playback_callbacks,
                "played_frames": self.played_frames,
            },
        }

    async def send_json(self, payload: dict) -> None:
        if not self.browser_ws or self.closed.is_set():
            raise BrowserMediaUnavailable("browser media WebSocket is closed")
        async with self.send_lock:
            await self.browser_ws.send_json(payload)

    async def send_pcm(self, payload: bytes) -> None:
        if len(payload) != PCM_FRAME_BYTES:
            raise BrowserMediaUnavailable("Engine sent an invalid PCM frame")
        if not self.browser_ws or self.closed.is_set():
            raise BrowserMediaUnavailable("browser media WebSocket is closed")
        async with self.send_lock:
            await self.browser_ws.send_bytes(payload)

    def record_browser_evidence(self, message: dict) -> dict:
        if message.get("type") != "browser.media.evidence" or message.get("version") != 1:
            raise BrowserMediaUnavailable("invalid browser media evidence")
        try:
            values = {
                key: int(message.get(key) or 0)
                for key in ("capture_callbacks", "playback_callbacks", "played_frames")
            }
        except (TypeError, ValueError) as exc:
            raise BrowserMediaUnavailable("invalid browser media counters") from exc
        previous = {
            "capture_callbacks": self.capture_callbacks,
            "playback_callbacks": self.playback_callbacks,
            "played_frames": self.played_frames,
        }
        if any(value < 0 or value < previous[key] for key, value in values.items()):
            raise BrowserMediaUnavailable("browser media counters moved backwards")
        acknowledged = str(message.get("challenge") or "")
        current = bool(self.challenge and hmac.compare_digest(acknowledged, self.challenge))
        previous = bool(
            self.previous_challenge and
            time.monotonic() - self.previous_challenge_at <= 2.0 and
            hmac.compare_digest(acknowledged, self.previous_challenge))
        if not current and not previous:
            raise BrowserMediaUnavailable("browser media challenge is stale")
        self.capture_callbacks = values["capture_callbacks"]
        self.playback_callbacks = values["playback_callbacks"]
        self.played_frames = values["played_frames"]
        now = time.monotonic()
        self.evidence_at = now
        self.challenge_ack_at = now
        return self.status()


@dataclass
class EngineMediaConnection:
    iid: str
    generation: str
    engine_run_id: str
    websocket: object
    send_lock: asyncio.Lock = field(default_factory=asyncio.Lock)
    reservations: dict[str, asyncio.Future] = field(default_factory=dict)
    closed: asyncio.Event = field(default_factory=asyncio.Event)

    async def send_json(self, payload: dict) -> None:
        if self.closed.is_set():
            raise BrowserMediaUnavailable("Engine media relay is disconnected")
        async with self.send_lock:
            await self.websocket.send_json(payload)

    async def send_pcm(self, audio_uuid: uuid.UUID, payload: bytes) -> None:
        if len(payload) != PCM_FRAME_BYTES:
            raise BrowserMediaUnavailable("browser sent an invalid PCM frame")
        if self.closed.is_set():
            raise BrowserMediaUnavailable("Engine media relay is disconnected")
        async with self.send_lock:
            await self.websocket.send_bytes(audio_uuid.bytes + payload)

    async def reserve(self, session: BrowserMediaSession) -> None:
        key = str(session.audio_uuid)
        if self.closed.is_set() or key in self.reservations:
            raise BrowserMediaUnavailable("Engine media reservation is unavailable")
        future = asyncio.get_running_loop().create_future()
        self.reservations[key] = future
        try:
            await self.send_json({
                "type": "engine.media.reserve", "version": 1,
                "session_id": session.session_id, "audio_uuid": key,
                "ttl_ms": 12000,
            })
            result = await asyncio.wait_for(future, timeout=3.0)
            if not result:
                raise BrowserMediaUnavailable("Engine rejected the media reservation")
        finally:
            self.reservations.pop(key, None)

    def acknowledge(self, message: dict) -> bool:
        try:
            key = str(uuid.UUID(str(message.get("audio_uuid") or "")))
        except ValueError as exc:
            raise BrowserMediaUnavailable("Engine sent an invalid reservation UUID") from exc
        if type(message.get("accepted")) is not bool:
            raise BrowserMediaUnavailable("Engine sent an invalid reservation result")
        future = self.reservations.get(key)
        if not future or future.done():
            # A timed-out reservation may race its already-in-flight ACK.  The frame is valid
            # but no longer authoritative; dropping it must not tear down the shared Engine WSS
            # or unrelated media sessions.
            return False
        future.set_result(message.get("accepted") is True)
        return True

    async def release(self, session: BrowserMediaSession) -> None:
        try:
            await self.send_json({
                "type": "engine.media.release", "version": 1,
                "audio_uuid": str(session.audio_uuid),
            })
        except Exception:
            pass

    def retire(self) -> None:
        if self.closed.is_set():
            return
        self.closed.set()
        for future in self.reservations.values():
            if not future.done():
                future.set_exception(BrowserMediaUnavailable(
                    "Engine media relay disconnected during reservation"))
        self.reservations.clear()


class BrowserMediaRegistry:
    def __init__(self, capacity: int = MAX_SESSIONS):
        self.capacity = int(capacity)
        self._sessions: dict[str, BrowserMediaSession] = {}
        self._by_uuid: dict[uuid.UUID, BrowserMediaSession] = {}
        self._engines: dict[str, EngineMediaConnection] = {}
        self._lock = asyncio.Lock()

    async def allocate(self, *, iid: str, generation: str, engine_run_id: str,
                       subject: str) -> BrowserMediaSession:
        async with self._lock:
            live = [item for item in self._sessions.values() if not item.closed.is_set()]
            if len(live) >= self.capacity:
                raise BrowserMediaUnavailable("browser media capacity is exhausted")
            engine = self._engines.get(str(iid))
            if (not engine or engine.closed.is_set() or
                    engine.generation != str(generation) or
                    engine.engine_run_id != str(engine_run_id)):
                raise BrowserMediaUnavailable("current Engine media relay is unavailable")
            session = BrowserMediaSession(
                session_id=secrets.token_urlsafe(18), ticket=secrets.token_urlsafe(32),
                subject=str(subject), iid=str(iid), generation=str(generation),
                engine_run_id=str(engine_run_id), audio_uuid=uuid.uuid4(),
                channel_id=f"mddcanary-{uuid.uuid4()}", engine=engine)
            self._sessions[session.session_id] = session
            self._by_uuid[session.audio_uuid] = session
            return session

    async def claim_browser(self, *, session_id: str, ticket: str, subject: str,
                            websocket: object) -> BrowserMediaSession:
        async with self._lock:
            session = self._sessions.get(str(session_id))
            if (not session or session.closed.is_set() or session.expired() or
                    session.browser_ws is not None or
                    not hmac.compare_digest(session.ticket, str(ticket or "")) or
                    not hmac.compare_digest(session.subject, str(subject or ""))):
                raise BrowserMediaUnavailable("invalid or expired browser media session")
            session.ticket = ""
            session.browser_ws = websocket
            return session

    async def attach_engine(self, connection: EngineMediaConnection) -> None:
        prior = None
        async with self._lock:
            prior = self._engines.get(connection.iid)
            self._engines[connection.iid] = connection
        if prior and prior is not connection:
            prior.retire()
            await self.close_for_engine(prior, "Engine media relay was replaced")
            try:
                await prior.websocket.close(code=4409, reason="Engine media relay replaced")
            except Exception:
                pass

    async def detach_engine(self, connection: EngineMediaConnection) -> None:
        async with self._lock:
            if self._engines.get(connection.iid) is connection:
                self._engines.pop(connection.iid, None)
        connection.retire()
        await self.close_for_engine(connection, "Engine media relay disconnected")

    async def close_for_engine(self, connection: EngineMediaConnection, reason: str) -> None:
        for session in list(self._sessions.values()):
            if session.engine is connection and not session.closed.is_set():
                await self.close(session, reason)

    async def handle_engine_pcm(self, connection: EngineMediaConnection,
                                payload: bytes) -> bool:
        if len(payload) != 16 + PCM_FRAME_BYTES:
            raise BrowserMediaUnavailable("Engine sent an invalid multiplexed PCM frame")
        audio_uuid = uuid.UUID(bytes=payload[:16])
        session = self._by_uuid.get(audio_uuid)
        if (not session or session.closed.is_set() or session.engine is not connection or
                session.generation != connection.generation or
                session.engine_run_id != connection.engine_run_id):
            # A final AudioSocket frame may already be on the wire when Control releases the
            # exact UUID.  It is stale data, not a protocol failure of this shared Engine WSS.
            return False
        session.engine_to_browser_frames += 1
        session.engine_to_browser_at = time.monotonic()
        await session.send_pcm(payload[16:])
        return True

    async def forward_browser_pcm(self, session: BrowserMediaSession, payload: bytes) -> None:
        if len(payload) != PCM_FRAME_BYTES or session.closed.is_set() or not session.engine:
            raise BrowserMediaUnavailable("invalid browser PCM frame")
        try:
            session.browser_pcm.put_nowait(bytes(payload))
        except asyncio.QueueFull as exc:
            raise BrowserMediaUnavailable("browser PCM jitter queue overflow") from exc

    def start_browser_pump(self, session: BrowserMediaSession) -> None:
        if session.pcm_pump_task is not None:
            raise BrowserMediaUnavailable("browser PCM pump already started")

        async def pump() -> None:
            deadline = time.monotonic()
            try:
                while not session.closed.is_set():
                    payload = await session.browser_pcm.get()
                    deadline = max(deadline + 0.02, time.monotonic())
                    delay = deadline - time.monotonic()
                    if delay > 0:
                        await asyncio.sleep(delay)
                    if session.closed.is_set() or not session.engine:
                        return
                    await session.engine.send_pcm(session.audio_uuid, payload)
                    session.browser_to_engine_frames += 1
                    session.browser_to_engine_at = time.monotonic()
            except asyncio.CancelledError:
                raise
            except Exception:
                await self.close(session, "browser PCM pump failed")

        session.pcm_pump_task = asyncio.create_task(
            pump(), name=f"browser-pcm-{session.session_id[:8]}")

    async def close(self, session: BrowserMediaSession, reason: str = "closed") -> None:
        async with session.close_lock:
            if session.closed.is_set():
                return
            session.close_reason = str(reason)[:160]
            session.closed.set()
            pump = session.pcm_pump_task
            if pump and pump is not asyncio.current_task():
                pump.cancel()
                await asyncio.gather(pump, return_exceptions=True)
            if session.engine:
                await session.engine.release(session)
            async with self._lock:
                self._sessions.pop(session.session_id, None)
                self._by_uuid.pop(session.audio_uuid, None)

    async def close_all(self) -> None:
        for session in list(self._sessions.values()):
            await self.close(session, "Control is shutting down")
        for connection in list(self._engines.values()):
            connection.retire()
        self._engines.clear()

    def get(self, session_id: str) -> BrowserMediaSession | None:
        return self._sessions.get(str(session_id))

    def engine(self, iid: str) -> EngineMediaConnection | None:
        value = self._engines.get(str(iid))
        return value if value and not value.closed.is_set() else None


registry = BrowserMediaRegistry()


def parse_text_message(raw: str, max_bytes: int = 4096) -> dict:
    if len(str(raw).encode("utf-8")) > max_bytes:
        raise BrowserMediaUnavailable("media control message is too large")
    try:
        value = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise BrowserMediaUnavailable("invalid media control JSON") from exc
    if not isinstance(value, dict):
        raise BrowserMediaUnavailable("media control message must be an object")
    return value
