"""Call-scoped, bounded PCM bridge between a browser and its cellular Agent."""

from __future__ import annotations

import asyncio
import hmac
import json
import logging
import secrets
import time
import uuid
from dataclasses import dataclass, field

log = logging.getLogger("vowifi.call_media")


class MediaUnavailable(RuntimeError):
    pass


PCM_FRAME_BYTES = 320
MEDIA_IO_TIMEOUT_SECONDS = 0.5
DEFAULT_PCM_BUFFER_MS = 500
MIN_PCM_BUFFER_MS = 100
MAX_PCM_BUFFER_MS = 2000
EVIDENCE_FRESH_SECONDS = 5.0
# Normal writes may recover from transient backpressure. Terminal notification and
# close retain their separate short budget, not another full liveness window.
CALL_HEARTBEAT_TIMEOUT_SECONDS = 10.0
MEDIA_SEND_TIMEOUT_SECONDS = CALL_HEARTBEAT_TIMEOUT_SECONDS


def validate_pcm_buffer_ms(value) -> int:
    if type(value) is not int or not MIN_PCM_BUFFER_MS <= value <= MAX_PCM_BUFFER_MS:
        raise ValueError("Cellular audio buffer must be an integer from 100 to 2000 ms")
    return value


@dataclass
class MediaSession:
    call_id: str
    iccid: str
    token: str
    owner_subject: str
    owner_token: str
    instance_iid: str = ""
    direction: str = "out"
    number: str = ""
    source_call_id: str = ""
    agent_session_id: str = ""
    agent_id: str = ""
    modem_id: str = ""
    pcm_buffer_ms: int = DEFAULT_PCM_BUFFER_MS
    agent_ws: object | None = None
    browser_ws: object | None = None
    agent_ready: asyncio.Event = field(default_factory=asyncio.Event)
    prepare_done: asyncio.Event = field(default_factory=asyncio.Event)
    prepare_error: str = ""
    media_prepared: asyncio.Event = field(default_factory=asyncio.Event)
    closed: asyncio.Event = field(default_factory=asyncio.Event)
    bridge_task: asyncio.Task | None = None
    expiry_task: asyncio.Task | None = None
    orphan_handler: object | None = None
    orphan_task: asyncio.Task | None = None
    managed_finalized: bool = False
    release_attempts: int = 0
    release_result: dict | None = None
    release_operation_id: str = ""
    release_unknown: bool = False
    release_requested: bool = False
    ringing_hangup_requested: bool = False
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
    _attach_lock: asyncio.Lock = field(default_factory=asyncio.Lock)
    _browser_send_lock: asyncio.Lock = field(default_factory=asyncio.Lock)
    phase: str = "allocated"
    browser_to_agent_frames: int = 0
    browser_to_agent_at: float = 0.0
    agent_to_browser_frames: int = 0
    agent_to_browser_at: float = 0.0
    expired_pcm_frames: dict = field(default_factory=lambda: {"uplink": 0, "downlink": 0})
    overflow_pcm_frames: dict = field(default_factory=lambda: {"uplink": 0, "downlink": 0})
    helper_capture_callbacks: int = 0
    helper_playback_callbacks: int = 0
    helper_capture_bytes: int = 0
    helper_playback_bytes: int = 0
    helper_telemetry_at: float = 0.0
    helper_capture_growth_at: float = 0.0
    helper_playback_growth_at: float = 0.0
    browser_evidence: dict = field(default_factory=dict)
    browser_evidence_at: float = 0.0
    browser_capture_growth_at: float = 0.0
    browser_playback_growth_at: float = 0.0
    challenge: str = ""
    previous_challenge: str = ""
    previous_challenge_at: float = 0.0
    challenge_history: list[tuple[str, float]] = field(default_factory=list)
    cellular_state: str = ""

    def owns(self, subject: str, owner_token: str) -> bool:
        return bool(subject and owner_token and
                    hmac.compare_digest(self.owner_subject, str(subject)) and
                    hmac.compare_digest(self.owner_token, str(owner_token)))

    def _refresh_prepared(self) -> None:
        """Derive readiness from call-scoped evidence; connections alone never qualify."""
        if self.closed.is_set():
            self.phase = "closed"
            self.media_prepared.clear()
            return
        now = time.monotonic()
        fresh = lambda stamp: stamp > 0 and now - stamp <= EVIDENCE_FRESH_SECONDS
        ready = bool(
            self.agent_ws is not None and self.browser_ws is not None and
            self.browser_to_agent_frames >= 2 and self.agent_to_browser_frames >= 2 and
            self.helper_capture_callbacks >= 2 and
            self.helper_playback_callbacks >= 2 and
            self.helper_capture_bytes > 0 and self.helper_playback_bytes > 0 and
            self.browser_evidence.get("capture_callbacks", 0) >= 2 and
            self.browser_evidence.get("playback_callbacks", 0) >= 2 and
            self.browser_evidence.get("played_frames", 0) >= 2 and
            fresh(self.browser_to_agent_at) and fresh(self.agent_to_browser_at) and
            fresh(self.helper_capture_growth_at) and
            fresh(self.helper_playback_growth_at) and
            fresh(self.browser_evidence_at) and fresh(self.browser_capture_growth_at) and
            fresh(self.browser_playback_growth_at))
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

    def issue_challenge(self) -> str:
        now = time.monotonic()
        self.previous_challenge, self.previous_challenge_at = self.challenge, now
        self.challenge = secrets.token_urlsafe(18)
        self.challenge_history = [(token, at) for token, at in self.challenge_history
                                  if 0 <= now - at <= EVIDENCE_FRESH_SECONDS][-7:]
        self.challenge_history.append((self.challenge, now))
        return self.challenge

    def record_browser_evidence(self, evidence: dict) -> dict:
        if (evidence.get("type") != "cellular.media.evidence" or
                evidence.get("version") != 1):
            raise MediaUnavailable("native cellular media evidence is required")
        now = time.monotonic()
        acknowledged = str(evidence.get("challenge") or "")
        if not any(0 <= now - issued <= EVIDENCE_FRESH_SECONDS
                   and hmac.compare_digest(acknowledged, token)
                   for token, issued in self.challenge_history):
            raise MediaUnavailable("browser media challenge is stale")
        values = {key: evidence.get(key) for key in (
            "capture_callbacks", "playback_callbacks", "played_frames")}
        if any(type(value) is not int or value < self.browser_evidence.get(key, 0)
               for key, value in values.items()):
            raise MediaUnavailable("browser audio counters are invalid or moved backwards")
        if values["capture_callbacks"] > self.browser_evidence.get("capture_callbacks", 0):
            self.browser_capture_growth_at = now
        if (values["playback_callbacks"] > self.browser_evidence.get("playback_callbacks", 0)
                and values["played_frames"] > self.browser_evidence.get("played_frames", 0)):
            self.browser_playback_growth_at = now
        self.browser_evidence = values
        self.browser_evidence_at = now
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
            "buffer_limit_ms": self.pcm_buffer_ms,
            "evidence": {
                "agent_connected": self.agent_ws is not None,
                "browser_connected": self.browser_ws is not None,
                "browser_to_agent_frames": self.browser_to_agent_frames,
                "agent_to_browser_frames": self.agent_to_browser_frames,
                "expired_pcm_frames": dict(self.expired_pcm_frames),
                "overflow_pcm_frames": dict(self.overflow_pcm_frames),
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

    async def attach_browser(self, websocket, subject: str, owner_token: str) -> None:
        if not self.owns(subject, owner_token):
            raise MediaUnavailable("another browser owns this cellular call")
        async with self._attach_lock:
            if self.closed.is_set() or self.release_requested or self.browser_ws is not None:
                raise MediaUnavailable("browser is already attached or the call expired")
            self.browser_ws = websocket
            self.issue_challenge()
            await self.send_browser_json({"type": "cellular.media.started", "version": 1,
                                          "call_id": self.call_id,
                                          "challenge": self.challenge,
                                          "frame_bytes": PCM_FRAME_BYTES})
            self._maybe_start()
        await self.closed.wait()

    async def send_browser_json(self, value: dict) -> None:
        async with self._browser_send_lock:
            await asyncio.wait_for(self.browser_ws.send_json(value), MEDIA_SEND_TIMEOUT_SECONDS)

    def _maybe_start(self) -> None:
        if self.agent_ws is not None and self.browser_ws is not None and self.bridge_task is None:
            self.bridge_task = asyncio.create_task(self._bridge(),
                                                   name=f"cellular-media-{self.call_id[:8]}")

    async def _bridge(self) -> None:
        queue_frames = (self.pcm_buffer_ms + 19) // 20
        uplink = asyncio.Queue(maxsize=queue_frames)
        downlink = asyncio.Queue(maxsize=queue_frames)
        # Snapshot local queue age for this session. This is not an RTT or call-loss timer.
        max_age_seconds = self.pcm_buffer_ms / 1000

        def discard_expired(queue, received, stage):
            direction = "downlink" if queue is downlink else "uplink"
            self.expired_pcm_frames[direction] += 1
            if self.expired_pcm_frames[direction] == 1:
                log.warning(
                    "cellular_pcm_expired iid=%s call=%s direction=%s stage=%s age_ms=%.1f limit_ms=%d queued=%d phase=%s",
                    self.instance_iid, self.call_id[:12], direction, stage,
                    (time.monotonic() - received) * 1000, self.pcm_buffer_ms, queue.qsize(), self.phase)
            # Discarding a late audio frame is not successful media. In particular it must
            # never refresh forwarding timestamps, readiness or the paid-call lease.

        async def enqueue(queue, payload, received):
            # Keep fresh audio and read the following heartbeat without waiting for a
            # blocked writer. Eviction/insertion is atomic within this event loop.
            if queue.full():
                queue.get_nowait()
                direction = "downlink" if queue is downlink else "uplink"
                self.overflow_pcm_frames[direction] += 1
            queue.put_nowait((received, payload))

        async def browser_receive():
            while True:
                message = await self.browser_ws.receive()
                received = time.monotonic()
                if message.get("type") == "websocket.disconnect":
                    return
                payload = message.get("bytes")
                if payload is not None:
                    if len(payload) != PCM_FRAME_BYTES:
                        raise MediaUnavailable("browser sent an invalid PCM frame")
                    await enqueue(uplink, payload, received)
                    continue
                raw = message.get("text") or ""
                if len(raw) > 4096:
                    raise MediaUnavailable("browser media control is too large")
                try:
                    self.record_browser_evidence(json.loads(raw))
                except MediaUnavailable as exc:
                    if str(exc) != "browser media challenge is stale":
                        raise
                    # An old echo is not new liveness evidence, nor proof of a dead peer.
                    # Keep receiving; the unchanged paid-call heartbeat window decides.

        async def agent_receive():
            pending = bytearray()
            pending_received = None
            while True:
                message = await self.agent_ws.receive()
                received = time.monotonic()
                if message.get("type") == "websocket.disconnect":
                    return
                payload = message.get("bytes")
                text = message.get("text")
                if text:
                    try:
                        telemetry = json.loads(text)
                    except (TypeError, json.JSONDecodeError):
                        raise MediaUnavailable("Agent sent invalid audio telemetry")
                    self.record_helper_telemetry(telemetry)
                    continue
                if not payload:
                    continue
                if len(payload) > 65535 or len(payload) % 2:
                    raise MediaUnavailable("Agent sent an invalid PCM frame")
                frame_received = pending_received if pending else received
                pending.extend(payload)
                while len(pending) >= PCM_FRAME_BYTES:
                    await enqueue(downlink, bytes(pending[:PCM_FRAME_BYTES]), frame_received)
                    del pending[:PCM_FRAME_BYTES]
                    frame_received = received
                pending_received = frame_received if pending else None

        async def pump(queue, *, browser):
            deadline = time.monotonic()
            while True:
                received, payload = await queue.get()
                if time.monotonic() - received > max_age_seconds:
                    discard_expired(queue, received, "pump_age")
                    continue
                deadline = max(deadline + 0.02, time.monotonic())
                await asyncio.sleep(max(0.0, deadline - time.monotonic()))
                if time.monotonic() - received > max_age_seconds:
                    discard_expired(queue, received, "pump_pacing")
                    continue
                if browser:
                    async with self._browser_send_lock:
                        if time.monotonic() - received > max_age_seconds:
                            discard_expired(queue, received, "browser_send_lock")
                            continue
                        await asyncio.wait_for(self.browser_ws.send_bytes(payload),
                                               MEDIA_SEND_TIMEOUT_SECONDS)
                    self.agent_to_browser_frames += 1
                    self.agent_to_browser_at = time.monotonic()
                else:
                    await asyncio.wait_for(self.agent_ws.send_bytes(payload),
                                           MEDIA_SEND_TIMEOUT_SECONDS)
                    self.browser_to_agent_frames += 1
                    self.browser_to_agent_at = time.monotonic()
                self._refresh_prepared()

        async def monitor():
            was_ready = False
            while True:
                await asyncio.sleep(1)
                self.issue_challenge()
                await self.send_browser_json({"type": "cellular.media.challenge", "version": 1,
                                              "call_id": self.call_id,
                                              "challenge": self.challenge})
                status = self.media_status()
                await self.send_browser_json({
                    "type": "cellular.media.ready" if status["ready"] and not was_ready
                            else "cellular.media.status", "version": 1,
                    "call_id": self.call_id, "media": status})
                was_ready = status["ready"]

        tasks = [asyncio.create_task(browser_receive()), asyncio.create_task(agent_receive()),
                 asyncio.create_task(pump(uplink, browser=False)),
                 asyncio.create_task(pump(downlink, browser=True)),
                 asyncio.create_task(monitor())]
        try:
            done, _ = await asyncio.wait(tasks, return_when=asyncio.FIRST_COMPLETED)
            for task in done:
                task.result()
        except Exception as exc:
            cause = str(exc) if isinstance(exc, MediaUnavailable) else type(exc).__name__
            log.warning("cellular_media_error iid=%s call=%s cause=%s tx=%d rx=%d",
                        self.instance_iid, self.call_id[:12], cause,
                        self.browser_to_agent_frames, self.agent_to_browser_frames)
            try:
                await asyncio.wait_for(self.send_browser_json({
                    "type": "cellular.media.error", "version": 1,
                    "call_id": self.call_id, "error": str(exc)}), MEDIA_IO_TIMEOUT_SECONDS)
            except Exception:
                pass
        finally:
            for task in tasks:
                if not task.done():
                    task.cancel()
            await asyncio.gather(*tasks, return_exceptions=True)
            if any(self.expired_pcm_frames.values()):
                log.info("cellular_pcm_expired_total iid=%s call=%s uplink=%d downlink=%d",
                         self.instance_iid, self.call_id[:12],
                         self.expired_pcm_frames["uplink"], self.expired_pcm_frames["downlink"])
            if any(self.overflow_pcm_frames.values()):
                log.info("cellular_pcm_overflow_total iid=%s call=%s uplink=%d downlink=%d",
                         self.instance_iid, self.call_id[:12],
                         self.overflow_pcm_frames["uplink"], self.overflow_pcm_frames["downlink"])
            await self.close(from_bridge=True)

    async def close(self, *, from_bridge: bool = False) -> None:
        if self.closed.is_set():
            return
        # Publish the existing cleanup owner before closed or any await. Restart recovery
        # must not mistake an in-progress, uncommitted browser cancel for an orphaned call.
        if from_bridge and self.orphan_handler and not self.managed_finalized:
            self.orphan_task = asyncio.create_task(
                self.orphan_handler(self), name=f"cellular-media-orphan-{self.call_id[:8]}")
        self.closed.set()
        self.phase = "closed"
        self.media_prepared.clear()
        for socket in (self.browser_ws, self.agent_ws):
            if socket:
                try:
                    await asyncio.wait_for(socket.close(), MEDIA_IO_TIMEOUT_SECONDS)
                except Exception:
                    pass
        if self.bridge_task and not from_bridge:
            self.bridge_task.cancel()
            await asyncio.gather(self.bridge_task, return_exceptions=True)
        current = asyncio.current_task()
        if self.expiry_task and self.expiry_task is not current:
            self.expiry_task.cancel()
            await asyncio.gather(self.expiry_task, return_exceptions=True)


class CallMediaManager:
    def __init__(self):
        self._sessions: dict[str, MediaSession] = {}
        self._by_iccid: dict[str, str] = {}
        self._lock = asyncio.Lock()

    async def allocate(self, iccid: str, *, owner_subject: str, owner_token: str,
                       instance_iid: str, direction: str, number: str,
                       source_call_id: str = "", agent_session_id: str = "",
                       agent_id: str = "", modem_id: str = "",
                       pcm_buffer_ms: int = DEFAULT_PCM_BUFFER_MS) -> MediaSession:
        pcm_buffer_ms = validate_pcm_buffer_ms(pcm_buffer_ms)
        call_id = uuid.uuid4().hex
        token = secrets.token_urlsafe(32)
        session = MediaSession(
            call_id, str(iccid), token, owner_subject, owner_token,
            instance_iid=instance_iid, direction=direction, number=number,
            source_call_id=source_call_id, agent_session_id=agent_session_id,
            agent_id=agent_id, modem_id=modem_id,
            pcm_buffer_ms=pcm_buffer_ms)
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
