"""Short-lived browser PCM sessions carried only by the management WS/WSS origin.

This module is deliberately independent from ``call_media``.  The latter owns a paid
cellular call and an Agent audio helper; this registry owns native Asterisk media WSS sessions.
Every outbound call first runs a non-billable Echo warmup on the same channel; only Control can
redirect it into the fixed carrier context after complete fresh media evidence.
"""

from __future__ import annotations

import asyncio
import hashlib
import hmac
import json
import logging
import re
import secrets
import time
import uuid
from dataclasses import dataclass, field
from typing import Awaitable, Callable


log = logging.getLogger(__name__)


PCM_FRAME_BYTES = 320
MAX_SESSIONS = 16
MAX_INBOUND_CLAIMANTS = 3
SESSION_TTL_SECONDS = 30.0
EVIDENCE_FRESH_SECONDS = 5.0
ASTERISK_STATUS_FRESH_SECONDS = 2.5
ASTERISK_STATUS_RESPONSE_TIMEOUT_SECONDS = 1.5
CLOSE_IO_TIMEOUT_SECONDS = 0.5


class BrowserMediaUnavailable(RuntimeError):
    pass


def subject_digest(token: str) -> str:
    return hashlib.sha256(str(token or "").encode("utf-8")).hexdigest()


def engine_media_token(global_token: str, iid: str, engine_run_id: str) -> str:
    """Derive one Engine-run credential without changing the shared event-token contract."""
    key = str(global_token or "").encode("utf-8")
    message = (b"mdd-media-v1\0" + str(iid).encode("utf-8") + b"\0" +
               str(engine_run_id).encode("utf-8"))
    return hmac.new(key, message, hashlib.sha256).hexdigest() if key else ""


@dataclass
class BrowserMediaSession:
    session_id: str
    ticket: str
    engine_sid: str
    subject: str
    iid: str
    generation: str
    engine_run_id: str
    channel_id: str
    purpose: str = "canary"
    pcm_buffer_ms: int = 500
    destination: str = ""
    operation_id: str = ""
    media_epoch: str = ""
    call_token: str = ""
    backend_call_id: int = 0
    backend_revision: int = -1
    source_call_id: str = ""
    inbound_ims_channel: str = ""
    inbound_ims_uniqueid: str = ""
    phase: str = "allocated"
    phase_revision: int = 0
    created_at: float = field(default_factory=time.monotonic)
    expires_at: float = field(
        default_factory=lambda: time.monotonic() + SESSION_TTL_SECONDS)
    committed_at: float = 0.0
    expiry_claimed: bool = False
    answer_owned: bool = False
    abort_requested: asyncio.Event = field(default_factory=asyncio.Event)
    answer_task: asyncio.Task | None = None
    browser_ws: object | None = None
    asterisk_ws: object | None = None
    asterisk_channel: str = ""
    asterisk_channel_id: str = ""
    closed: asyncio.Event = field(default_factory=asyncio.Event)
    ready: asyncio.Event = field(default_factory=asyncio.Event)
    asterisk_ready: asyncio.Event = field(default_factory=asyncio.Event)
    asterisk_status_event: asyncio.Event = field(default_factory=asyncio.Event)
    send_lock: asyncio.Lock = field(default_factory=asyncio.Lock)
    asterisk_send_lock: asyncio.Lock = field(default_factory=asyncio.Lock)
    close_lock: asyncio.Lock = field(default_factory=asyncio.Lock)
    phase_lock: asyncio.Lock = field(default_factory=asyncio.Lock)
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
    challenge_history: list[tuple[str, float]] = field(default_factory=list)
    challenge_ack_at: float = 0.0
    started: bool = False
    close_reason: str = ""
    browser_pcm: asyncio.Queue = field(init=False)
    pcm_pump_task: asyncio.Task | None = None
    asterisk_queue_length: int = 0
    asterisk_xoff: bool = False
    asterisk_status_at: float = 0.0
    expired_browser_pcm_frames: int = 0
    overflow_browser_pcm_frames: int = 0
    backpressure_dropped_frames: int = 0
    asterisk_flush_pending: bool = False
    asterisk_media_paused: bool = False

    def __post_init__(self):
        if type(self.pcm_buffer_ms) is not int or not 100 <= self.pcm_buffer_ms <= 2000:
            raise BrowserMediaUnavailable("audio buffer must be an integer from 100 to 2000 ms")
        self.browser_pcm = asyncio.Queue(maxsize=(self.pcm_buffer_ms + 19) // 20)

    async def transition_phase(self, phase: str) -> int | None:
        """Move a native call monotonically; delayed tasks can never regress state."""
        ranks = ({
            "allocated": 0, "warmup": 1, "redirect_submitted_unknown": 2,
            "calling": 3, "active": 4, "ending": 5, "terminal": 6,
        } if self.purpose == "outbound" else {
            "allocated": 0, "inbound_warmup": 1, "inbound_ready": 2,
            "claiming": 3, "attach_submitted_unknown": 4,
            "answer_submitted_unknown": 5, "active": 6,
            "ending": 7, "terminal": 8,
        } if self.purpose == "inbound" else None)
        if ranks is None:
            async with self.phase_lock:
                self.phase = str(phase)
                self.phase_revision += 1
                return self.phase_revision
        if phase not in ranks:
            return None
        async with self.phase_lock:
            if ranks.get(self.phase, -1) > ranks[phase]:
                return None
            if self.phase == phase:
                return self.phase_revision
            self.phase = phase
            self.phase_revision += 1
            return self.phase_revision

    def expired(self) -> bool:
        return time.monotonic() >= self.expires_at

    def status(self) -> dict:
        now = time.monotonic()
        fresh = lambda stamp: stamp > 0 and now - stamp <= EVIDENCE_FRESH_SECONDS
        is_ready = bool(
            not self.closed.is_set() and self.started and
            self.browser_to_engine_frames >= 2 and self.engine_to_browser_frames >= 2 and
            self.capture_callbacks >= 2 and self.playback_callbacks >= 2 and
            self.played_frames >= 2 and fresh(self.browser_to_engine_at) and
            fresh(self.engine_to_browser_at) and fresh(self.evidence_at) and
            fresh(self.challenge_ack_at) and self.asterisk_status_at > 0 and
            now - self.asterisk_status_at <= ASTERISK_STATUS_FRESH_SECONDS and
            self.asterisk_queue_length <= self.browser_pcm.maxsize
            and not self.asterisk_xoff and not self.asterisk_flush_pending
            and not self.asterisk_media_paused)
        if is_ready:
            self.ready.set()
        else:
            self.ready.clear()
        return {
            "type": "browser.media.status",
            "version": 1,
            "ready": is_ready,
            "buffer_limit_ms": self.pcm_buffer_ms,
            "purpose": self.purpose,
            "operation_id": self.operation_id,
            "media_epoch": self.media_epoch,
            "call_phase": self.phase,
            "call_revision": self.phase_revision,
            "phase": "closed" if self.closed.is_set() else (
                "ready" if is_ready else "testing" if self.started else "preparing"),
            "evidence": {
                "browser_to_engine_frames": self.browser_to_engine_frames,
                "engine_to_browser_frames": self.engine_to_browser_frames,
                "capture_callbacks": self.capture_callbacks,
                "playback_callbacks": self.playback_callbacks,
                "played_frames": self.played_frames,
                "expired_pcm_frames": self.expired_browser_pcm_frames,
                "overflow_pcm_frames": self.overflow_browser_pcm_frames,
                "backpressure_dropped_frames": self.backpressure_dropped_frames,
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

    async def send_asterisk_json(self, payload: dict) -> None:
        if not self.asterisk_ws or self.closed.is_set():
            raise BrowserMediaUnavailable("Asterisk media WebSocket is closed")
        async with self.asterisk_send_lock:
            await self.asterisk_ws.send_json(payload)

    async def send_asterisk_pcm(self, payload: bytes, *, received_at: float | None = None) -> bool:
        if len(payload) != PCM_FRAME_BYTES:
            raise BrowserMediaUnavailable("browser sent an invalid PCM frame")
        if not self.asterisk_ws or self.closed.is_set():
            raise BrowserMediaUnavailable("Asterisk media WebSocket is unavailable")
        async with self.asterisk_send_lock:
            if received_at is not None and time.monotonic() - received_at > self.pcm_buffer_ms / 1000:
                self.expired_browser_pcm_frames += 1
                return False
            if (self.asterisk_xoff or self.asterisk_flush_pending or self.asterisk_media_paused
                    or self.asterisk_queue_length >= self.browser_pcm.maxsize):
                self.backpressure_dropped_frames += 1
                return False
            await self.asterisk_ws.send_bytes(payload)
            return True

    def issue_challenge(self) -> str:
        now = time.monotonic()
        self.previous_challenge, self.previous_challenge_at = self.challenge, now
        self.challenge = secrets.token_urlsafe(18)
        self.challenge_history = [(token, at) for token, at in self.challenge_history
                                  if 0 <= now - at <= EVIDENCE_FRESH_SECONDS][-7:]
        self.challenge_history.append((self.challenge, now))
        return self.challenge

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
        now = time.monotonic()
        if not any(0 <= now - issued <= EVIDENCE_FRESH_SECONDS
                   and hmac.compare_digest(acknowledged, token)
                   for token, issued in self.challenge_history):
            raise BrowserMediaUnavailable("browser media challenge is stale")
        self.capture_callbacks = values["capture_callbacks"]
        self.playback_callbacks = values["playback_callbacks"]
        self.played_frames = values["played_frames"]
        now = time.monotonic()
        self.evidence_at = now
        self.challenge_ack_at = now
        return self.status()


class BrowserMediaRegistry:
    def __init__(self, capacity: int = MAX_SESSIONS):
        self.capacity = int(capacity)
        self._sessions: dict[str, BrowserMediaSession] = {}
        self._by_engine_sid: dict[str, BrowserMediaSession] = {}
        self._outbound_by_iid: dict[str, BrowserMediaSession] = {}
        self._by_call_token: dict[str, BrowserMediaSession] = {}
        self._inbound_owners: dict[str, BrowserMediaSession] = {}
        self._lock = asyncio.Lock()

    async def allocate(self, *, iid: str, generation: str, engine_run_id: str,
                       subject: str, purpose: str = "canary", destination: str = "",
                       call_token: str = "", backend_call_id: int = 0,
                       backend_revision: int = -1,
                       source_call_id: str = "", pcm_buffer_ms: int = 500) -> BrowserMediaSession:
        async with self._lock:
            live = [item for item in self._sessions.values() if not item.closed.is_set()]
            if len(live) >= self.capacity:
                raise BrowserMediaUnavailable("browser media capacity is exhausted")
            if purpose not in {"canary", "outbound", "inbound"}:
                raise BrowserMediaUnavailable("invalid browser media purpose")
            if purpose == "outbound":
                current = self._outbound_by_iid.get(str(iid))
                if current and not current.closed.is_set():
                    raise BrowserMediaUnavailable("this line already has an active call owner")
                if (not re.fullmatch(r"(?:\d{2,6}|\+[1-9]\d{6,14})", destination)
                        or not re.fullmatch(r"[A-Za-z0-9_-]{32,128}", call_token)):
                    raise BrowserMediaUnavailable("invalid outbound media identity")
            if purpose == "inbound":
                if (type(backend_call_id) is not int or backend_call_id <= 0
                        or type(backend_revision) is not int or backend_revision < 0
                        or not re.fullmatch(
                            r"[A-Za-z0-9_.:-]{1,128}:[A-Za-z0-9_.-]{1,160}",
                            source_call_id)):
                    raise BrowserMediaUnavailable("invalid inbound media identity")
                inbound_key = (str(iid), str(engine_run_id), str(source_call_id),
                               int(backend_call_id))
                claimants = [item for item in live if item.purpose == "inbound" and
                             (item.iid, item.engine_run_id, item.source_call_id,
                              item.backend_call_id) == inbound_key]
                if len(claimants) >= MAX_INBOUND_CLAIMANTS:
                    raise BrowserMediaUnavailable(
                        "incoming call already has the maximum media claimants")
            session = BrowserMediaSession(
                session_id=secrets.token_urlsafe(18), ticket=secrets.token_urlsafe(32),
                engine_sid=secrets.token_urlsafe(18),
                subject=str(subject), iid=str(iid), generation=str(generation),
                engine_run_id=str(engine_run_id),
                channel_id=f"mddcanary-{uuid.uuid4()}", purpose=purpose,
                destination=str(destination), operation_id=uuid.uuid4().hex,
                media_epoch=secrets.token_urlsafe(18), call_token=str(call_token),
                backend_call_id=int(backend_call_id or 0),
                backend_revision=int(backend_revision),
                source_call_id=str(source_call_id or ""), pcm_buffer_ms=pcm_buffer_ms)
            self._sessions[session.session_id] = session
            self._by_engine_sid[session.engine_sid] = session
            if purpose == "outbound":
                self._outbound_by_iid[session.iid] = session
                self._by_call_token[session.call_token] = session
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

    async def claim_asterisk(self, *, engine_sid: str, iid: str, generation: str,
                             engine_run_id: str, websocket: object,
                             media_start: dict) -> BrowserMediaSession:
        channel = str(media_start.get("channel") or "")
        channel_id = str(media_start.get("channel_id") or "")
        if (media_start.get("event") != "MEDIA_START" or
                media_start.get("format") != "slin" or
                media_start.get("optimal_frame_size") != PCM_FRAME_BYTES or
                media_start.get("ptime") != 20 or
                not channel or len(channel) > 240 or "\r" in channel or "\n" in channel or
                not re.fullmatch(r"[A-Za-z0-9_.:-]{1,160}", channel_id)):
            raise BrowserMediaUnavailable("invalid Asterisk media start identity")
        async with self._lock:
            session = self._by_engine_sid.get(str(engine_sid))
            if (not session or session.closed.is_set() or session.expired() or
                    session.asterisk_ws is not None or session.iid != str(iid) or
                    session.generation != str(generation) or
                    session.engine_run_id != str(engine_run_id) or
                    channel_id != session.channel_id):
                raise BrowserMediaUnavailable("invalid or stale Asterisk media session")
            session.asterisk_ws = websocket
            session.asterisk_channel = channel
            session.asterisk_channel_id = channel_id
            return session

    async def handle_asterisk_pcm(self, session: BrowserMediaSession,
                                  payload: bytes) -> bool:
        if len(payload) != PCM_FRAME_BYTES:
            raise BrowserMediaUnavailable("Asterisk sent an invalid PCM frame")
        if session.closed.is_set() or not session.asterisk_ws:
            return False
        await session.send_pcm(payload)
        session.engine_to_browser_frames += 1
        session.engine_to_browser_at = time.monotonic()
        return True

    def handle_asterisk_control(self, session: BrowserMediaSession, message: dict) -> None:
        event = str(message.get("event") or "")
        if str(message.get("channel_id") or "") != session.asterisk_channel_id:
            raise BrowserMediaUnavailable("Asterisk media control identity changed")
        if event == "STATUS":
            queue_length = message.get("queue_length")
            if type(queue_length) is not int or not 0 <= queue_length <= 1000:
                raise BrowserMediaUnavailable("invalid Asterisk media queue status")
            for name in ("media_paused", "queue_full"):
                if name in message and type(message[name]) is not bool:
                    raise BrowserMediaUnavailable("invalid Asterisk queue flags")
            session.asterisk_queue_length = queue_length
            session.asterisk_status_at = time.monotonic()
            if "media_paused" in message:
                session.asterisk_media_paused = message["media_paused"]
            if queue_length <= session.browser_pcm.maxsize and message.get("queue_full") is False:
                session.asterisk_flush_pending = False
                session.asterisk_xoff = False
            session.asterisk_status_event.set()
            return
        if event == "MEDIA_XOFF":
            session.asterisk_xoff = True
            return
        if event == "MEDIA_XON":
            session.asterisk_xoff = False
            return
        if event in {"DTMF_END", "QUEUE_DRAINED", "MEDIA_MARK_PROCESSED"}:
            return
        raise BrowserMediaUnavailable("unexpected Asterisk media control event")

    async def forward_browser_pcm(self, session: BrowserMediaSession, payload: bytes) -> None:
        if len(payload) != PCM_FRAME_BYTES or session.closed.is_set() \
                or not session.asterisk_ws:
            raise BrowserMediaUnavailable("invalid browser PCM frame")
        received_at = time.monotonic()
        # Never park the WS reader behind audio: the next message may be a heartbeat.
        # No await between eviction/insertion; drops are not forwarding evidence.
        if session.browser_pcm.full():
            session.browser_pcm.get_nowait()
            session.overflow_browser_pcm_frames += 1
        session.browser_pcm.put_nowait((received_at, bytes(payload)))

    def start_browser_pump(self, session: BrowserMediaSession) -> None:
        if session.pcm_pump_task is not None:
            raise BrowserMediaUnavailable("browser PCM pump already started")

        async def pump() -> None:
            deadline = time.monotonic()
            try:
                while not session.closed.is_set():
                    received_at, payload = await session.browser_pcm.get()
                    if time.monotonic() - received_at > session.pcm_buffer_ms / 1000:
                        session.expired_browser_pcm_frames += 1
                        continue
                    deadline = max(deadline + 0.02, time.monotonic())
                    delay = deadline - time.monotonic()
                    if delay > 0:
                        await asyncio.sleep(delay)
                    if session.closed.is_set() or not session.asterisk_ws:
                        return
                    if not await session.send_asterisk_pcm(payload, received_at=received_at):
                        continue
                    session.browser_to_engine_frames += 1
                    session.browser_to_engine_at = time.monotonic()
            except asyncio.CancelledError:
                raise
            except Exception:
                await self.close(session, "browser PCM pump failed")

        session.pcm_pump_task = asyncio.create_task(
            pump(), name=f"browser-pcm-{session.session_id[:8]}")

    async def close(self, session: BrowserMediaSession, reason: str = "closed") -> None:
        if session.answer_owned:
            session.abort_requested.set()
        async with session.close_lock:
            if session.closed.is_set():
                return
            session.close_reason = str(reason)[:160]
            known_reasons = {"Asterisk media status failed", "browser PCM pump failed",
                             "native browser media lease lost", "browser media peer ended",
                             "expired", "closed", "incoming call answered elsewhere"}
            cause = session.close_reason if session.close_reason in known_reasons else "other"
            log.info("native_media_closed iid=%s session=%s phase=%s cause=%s tx=%d rx=%d expired=%d",
                     session.iid, session.session_id[:8], session.phase, cause,
                     session.browser_to_engine_frames, session.engine_to_browser_frames,
                     session.expired_browser_pcm_frames)
            session.closed.set()
            pump = session.pcm_pump_task
            asterisk_ws = session.asterisk_ws
            browser_ws = session.browser_ws
            async with self._lock:
                self._sessions.pop(session.session_id, None)
                self._by_engine_sid.pop(session.engine_sid, None)
                if self._outbound_by_iid.get(session.iid) is session:
                    self._outbound_by_iid.pop(session.iid, None)
                if self._by_call_token.get(session.call_token) is session:
                    self._by_call_token.pop(session.call_token, None)
            if pump and pump is not asyncio.current_task():
                pump.cancel()
                try:
                    await asyncio.wait_for(
                        asyncio.gather(pump, return_exceptions=True),
                        timeout=CLOSE_IO_TIMEOUT_SECONDS)
                except asyncio.TimeoutError:
                    pass
            if asterisk_ws:
                async def hangup() -> None:
                    async with session.asterisk_send_lock:
                        await asterisk_ws.send_json({"command": "HANGUP"})
                try:
                    await asyncio.wait_for(
                        hangup(), timeout=CLOSE_IO_TIMEOUT_SECONDS)
                except Exception:
                    pass
            for endpoint in (asterisk_ws, browser_ws):
                if not endpoint:
                    continue
                try:
                    await asyncio.wait_for(
                        endpoint.close(code=1000), timeout=CLOSE_IO_TIMEOUT_SECONDS)
                except Exception:
                    pass

    async def close_all(self) -> None:
        sessions = list(self._sessions.values())
        if sessions:
            await asyncio.gather(
                *(self.close(session, "Control is shutting down") for session in sessions),
                return_exceptions=True)

    async def commit_inbound(self, session: BrowserMediaSession) -> bool:
        """Atomically turn a live reservation into a full-TTL claimant ticket."""
        async with self._lock:
            current = self._sessions.get(session.session_id)
            if (current is not session or session.purpose != "inbound"
                    or session.closed.is_set() or session.expiry_claimed
                    or session.committed_at > 0 or session.expired()):
                return False
            now = time.monotonic()
            session.committed_at = now
            session.expires_at = now + SESSION_TTL_SECONDS
            return True

    async def start_inbound_owner(
            self, session: BrowserMediaSession,
            coroutine_factory: Callable[[BrowserMediaSession], Awaitable[None]]) \
            -> asyncio.Task | None:
        """Atomically freeze claimant TTL and publish its one server-owned lifecycle task."""
        async with session.phase_lock:
            async with self._lock:
                current = self._sessions.get(session.session_id)
                if (current is not session or session.purpose != "inbound"
                        or session.closed.is_set() or session.expiry_claimed
                        or session.answer_owned or session.answer_task is not None
                        or session.committed_at <= 0 or session.expired()
                        or session.phase != "inbound_ready"
                        or session.status().get("ready") is not True):
                    return None
                try:
                    operation = coroutine_factory(session)
                    if not asyncio.iscoroutine(operation):
                        raise TypeError("inbound owner factory did not return a coroutine")

                    async def run_owner() -> None:
                        try:
                            await operation
                        except asyncio.CancelledError:
                            log.warning(
                                "browser inbound owner cancelled iid=%s session=%s",
                                session.iid, session.session_id[:8])
                            raise
                        except BaseException as exc:
                            log.error(
                                "browser inbound owner failed iid=%s session=%s error=%s",
                                session.iid, session.session_id[:8], type(exc).__name__)
                            raise
                        finally:
                            session.abort_requested.set()
                            try:
                                await self.close(session, "browser inbound owner ended")
                            finally:
                                await self.finish_inbound_owner(session)

                    runner = run_owner()
                    task = asyncio.create_task(
                        runner, name=f"browser-inbound-owner-{session.session_id[:8]}")
                except BaseException:
                    if 'runner' in locals() and asyncio.iscoroutine(runner):
                        runner.close()
                    if 'operation' in locals() and asyncio.iscoroutine(operation):
                        operation.close()
                    raise
                session.answer_owned = True
                session.expires_at = float("inf")
                session.phase = "claiming"
                session.phase_revision += 1
                session.answer_task = task
                self._inbound_owners[session.session_id] = session

                def consume_result(value: asyncio.Task) -> None:
                    if not value.cancelled():
                        try:
                            value.exception()
                        except Exception:
                            pass

                task.add_done_callback(consume_result)
                return task

    async def finish_inbound_owner(self, session: BrowserMediaSession) -> None:
        async with self._lock:
            if self._inbound_owners.get(session.session_id) is session:
                self._inbound_owners.pop(session.session_id, None)

    def inbound_owner(self, session_id: str) -> BrowserMediaSession | None:
        return self._inbound_owners.get(str(session_id))

    def inbound_owner_sessions(self) -> list[BrowserMediaSession]:
        return list(self._inbound_owners.values())

    def inbound_call_sessions(self, iid: str, engine_run_id: str,
                              source_call_id: str, backend_call_id: int) \
            -> list[BrowserMediaSession]:
        values = self.inbound_claimants(
            iid, engine_run_id, source_call_id, backend_call_id)
        for session in self._inbound_owners.values():
            if (session not in values and session.iid == str(iid)
                    and session.engine_run_id == str(engine_run_id)
                    and session.source_call_id == str(source_call_id)
                    and session.backend_call_id == int(backend_call_id)):
                values.append(session)
        return values

    def abort_inbound_owner(self, session_id: str) -> BrowserMediaSession | None:
        session = self._inbound_owners.get(str(session_id))
        if session:
            session.abort_requested.set()
        return session

    async def close_if_expired(
            self, session: BrowserMediaSession,
            reason: str = "browser media session expired") -> bool:
        """Claim an expiry under the same lock as commit, then close outside it."""
        async with self._lock:
            current = self._sessions.get(session.session_id)
            if (current is not session or session.closed.is_set()
                    or session.expiry_claimed or not session.expired()):
                return False
            session.expiry_claimed = True
        await self.close(session, reason)
        return True

    def get(self, session_id: str) -> BrowserMediaSession | None:
        return self._sessions.get(str(session_id))

    def get_by_engine_sid(self, engine_sid: str) -> BrowserMediaSession | None:
        return self._by_engine_sid.get(str(engine_sid))

    def outbound(self, iid: str) -> BrowserMediaSession | None:
        value = self._outbound_by_iid.get(str(iid))
        return value if value and not value.closed.is_set() else None

    def line_reserved(self, iid: str) -> bool:
        """Existing paid-capable reservations, including an inbound owner still cleaning up."""
        return (any(session.iid == str(iid) and session.purpose in {"outbound", "inbound"}
                    and not session.closed.is_set() for session in self._sessions.values())
                or any(session.iid == str(iid) for session in self._inbound_owners.values()))

    def get_by_call_token(self, token: str) -> BrowserMediaSession | None:
        value = self._by_call_token.get(str(token))
        return value if value and not value.closed.is_set() else None

    def inbound_claimants(self, iid: str, engine_run_id: str,
                          source_call_id: str, backend_call_id: int) -> list[BrowserMediaSession]:
        return [session for session in self._sessions.values()
                if not session.closed.is_set() and session.purpose == "inbound"
                and session.iid == str(iid)
                and session.engine_run_id == str(engine_run_id)
                and session.source_call_id == str(source_call_id)
                and session.backend_call_id == int(backend_call_id)]


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
