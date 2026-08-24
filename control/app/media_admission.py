"""Short-lived, one-shot admission for browser-originated VoWiFi calls.

The browser first calls the local Asterisk echo extension.  A carrier INVITE is admitted only
after both sides of that exact canary have reported evidence: Asterisk saw the token on the
current Engine generation and the browser measured fresh bidirectional RTP.  This state is
deliberately in-memory and never survives an Engine or Control restart.
"""
from __future__ import annotations

from dataclasses import dataclass
import secrets
import threading
import time


TOKEN_TTL_SECONDS = 30.0
PROOF_TTL_SECONDS = 10.0
MAX_ENTRIES = 512
TERMINAL_TOMBSTONE_SECONDS = 60.0


@dataclass
class _Admission:
    iid: str
    generation: str
    media_route_id: str
    issued_at: float
    websocket_id: str = ""
    engine_at: float = 0.0
    browser_at: float = 0.0
    transaction_id: str = ""
    target: str = ""
    consumed_at: float = 0.0
    initial_cseq: int = 0
    initial_branch: str = ""
    initial_authorized: bool = False
    auth_cseq: int = 0
    auth_branch: str = ""
    challenge_seen: bool = False
    invite_closed: bool = False
    source_call_id: str = ""


@dataclass(frozen=True)
class _TerminalCall:
    iid: str
    source_call_id: str
    recorded_at: float


class MediaAdmissionRegistry:
    def __init__(self, *, clock=time.monotonic):
        self._clock = clock
        self._lock = threading.Lock()
        self._entries: dict[str, _Admission] = {}
        # Asterisk invokes call_out and call_result through independent background hooks.  A very
        # short call can therefore end before its start hook arrives.  Keep a small, in-memory
        # terminal fence so that the late start cannot resurrect an admission or a lease.
        self._terminal: dict[str, _TerminalCall] = {}

    def _prune_locked(self, now: float) -> None:
        # Every call token is short-lived even after it is claimed. A failed canary or an
        # authenticated SIP retry must not accumulate for the lifetime of a browser WebSocket.
        expired = [token for token, entry in self._entries.items()
                   if (not entry.source_call_id
                       and ((entry.consumed_at and now - entry.consumed_at > TOKEN_TTL_SECONDS)
                       or (not entry.consumed_at
                           and now - entry.issued_at > TOKEN_TTL_SECONDS)))]
        for token in expired:
            self._entries.pop(token, None)
        expired_terminal = [token for token, terminal in self._terminal.items()
                            if now - terminal.recorded_at > TERMINAL_TOMBSTONE_SECONDS]
        for token in expired_terminal:
            self._terminal.pop(token, None)
        if len(self._terminal) > MAX_ENTRIES:
            oldest_terminal = sorted(
                self._terminal, key=lambda token: self._terminal[token].recorded_at)
            for token in oldest_terminal[:len(self._terminal) - MAX_ENTRIES]:
                self._terminal.pop(token, None)
        if len(self._entries) > MAX_ENTRIES:
            oldest = sorted((token for token, entry in self._entries.items()
                             if not entry.transaction_id),
                            key=lambda token: self._entries[token].issued_at)
            for token in oldest[:len(self._entries) - MAX_ENTRIES]:
                self._entries.pop(token, None)

    def issue(self, iid: str, generation: str, media_route_id: str = "") -> str:
        if not iid or not generation or not media_route_id:
            return ""
        now = self._clock()
        token = secrets.token_urlsafe(32)
        with self._lock:
            self._prune_locked(now)
            if len(self._entries) >= MAX_ENTRIES:
                return ""
            self._entries[token] = _Admission(
                str(iid), str(generation), str(media_route_id), now)
        return token

    def matches_route(self, token: str, iid: str, generation: str,
                      media_route_id: str) -> bool:
        with self._lock:
            entry = self._entries.get(str(token))
            return bool(entry and entry.iid == str(iid)
                        and entry.generation == str(generation)
                        and entry.media_route_id == str(media_route_id))

    def claim_canary(self, token: str, iid: str, generation: str,
                     websocket_id: str, media_route_id: str = "") -> bool:
        now = self._clock()
        with self._lock:
            self._prune_locked(now)
            entry = self._entries.get(str(token))
            if (not entry or entry.iid != str(iid)
                    or entry.generation != str(generation)
                    or entry.media_route_id != str(media_route_id)):
                return False
            if entry.websocket_id and entry.websocket_id != str(websocket_id):
                return False
            if entry.transaction_id:
                return False
            entry.websocket_id = str(websocket_id)
            entry.engine_at = 0.0
            entry.browser_at = 0.0
            return bool(entry.websocket_id)

    @staticmethod
    def valid_browser_evidence(evidence: dict) -> bool:
        if not isinstance(evidence, dict):
            return False
        if evidence.get("connection_state") not in {"connected", "completed"}:
            return False
        for key in ("local_track_live", "remote_track_live", "playback_started"):
            if type(evidence.get(key)) is not bool or not evidence[key]:
                return False
        for key in ("outbound_packets_delta", "outbound_bytes_delta",
                    "inbound_packets_delta", "inbound_bytes_delta"):
            value = evidence.get(key)
            if type(value) not in {int, float} or not 0 < value < 1_000_000_000:
                return False
        return True

    def mark_browser(self, token: str, iid: str, generation: str,
                     evidence: dict) -> bool:
        if not self.valid_browser_evidence(evidence):
            return False
        now = self._clock()
        with self._lock:
            self._prune_locked(now)
            entry = self._entries.get(str(token))
            if (not entry or not entry.websocket_id or entry.iid != str(iid)
                    or entry.generation != str(generation) or entry.transaction_id):
                return False
            entry.browser_at = now
            return True

    def mark_engine(self, token: str, iid: str, generation: str) -> bool:
        now = self._clock()
        with self._lock:
            self._prune_locked(now)
            entry = self._entries.get(str(token))
            if (not entry or not entry.websocket_id or entry.iid != str(iid)
                    or entry.generation != str(generation) or entry.transaction_id):
                return False
            entry.engine_at = now
            return True

    def status(self, token: str, iid: str, generation: str) -> dict:
        now = self._clock()
        with self._lock:
            self._prune_locked(now)
            entry = self._entries.get(str(token))
            valid = bool(entry and entry.iid == str(iid)
                         and entry.generation == str(generation))
            ready = bool(valid and entry.websocket_id and entry.engine_at and entry.browser_at
                         and now - entry.engine_at <= PROOF_TTL_SECONDS
                         and now - entry.browser_at <= PROOF_TTL_SECONDS)
            return {"ready": ready,
                    "engine_proven": bool(valid and entry.engine_at),
                    "browser_proven": bool(valid and entry.browser_at)}

    def authorize_invite(self, token: str, iid: str, generation: str,
                         websocket_id: str, transaction_id: str, target: str,
                         cseq: int = 1, branch: str = "test-branch",
                         has_authorization: bool = False) -> bool:
        with self._lock:
            now = self._clock()
            self._prune_locked(now)
            entry = self._entries.get(str(token))
            identity_matches = bool(entry and entry.iid == str(iid)
                                    and entry.generation == str(generation)
                                    and entry.websocket_id == str(websocket_id))
            if (identity_matches and entry.transaction_id and not entry.invite_closed
                    and entry.transaction_id == str(transaction_id)
                    and entry.target == str(target)
                    and now - entry.consumed_at <= TOKEN_TTL_SECONDS):
                if (cseq == entry.initial_cseq and branch == entry.initial_branch
                        and bool(has_authorization) == entry.initial_authorized):
                    return True  # exact retransmission of the first transaction
                if (entry.auth_cseq and cseq == entry.auth_cseq
                        and branch == entry.auth_branch and has_authorization):
                    return True  # exact retransmission of the one challenged retry
                if (entry.challenge_seen and not entry.initial_authorized
                        and not entry.auth_cseq and has_authorization
                        and cseq == entry.initial_cseq + 1
                        and branch and branch != entry.initial_branch):
                    entry.auth_cseq = cseq
                    entry.auth_branch = str(branch)
                    return True
                return False
            ready = bool(entry and entry.iid == str(iid)
                         and entry.generation == str(generation)
                         and entry.websocket_id == str(websocket_id)
                         and not entry.transaction_id
                         and transaction_id and target and type(cseq) is int and cseq > 0
                         and branch and len(str(branch)) <= 160
                         and entry.engine_at and entry.browser_at
                         and now - entry.engine_at <= PROOF_TTL_SECONDS
                         and now - entry.browser_at <= PROOF_TTL_SECONDS)
            if ready:
                entry.transaction_id = str(transaction_id)
                entry.target = str(target)
                entry.consumed_at = now
                entry.initial_cseq = cseq
                entry.initial_branch = str(branch)
                entry.initial_authorized = bool(has_authorization)
                entry.engine_at = 0.0
                entry.browser_at = 0.0
            return ready

    def observe_invite_response(self, websocket_id: str, transaction_id: str,
                                cseq: int, status_code: int) -> bool:
        """Fence digest retry and close the one-shot transaction on a final response."""
        if (not websocket_id or not transaction_id or type(cseq) is not int
                or type(status_code) is not int):
            return False
        with self._lock:
            now = self._clock()
            self._prune_locked(now)
            for entry in self._entries.values():
                if (entry.websocket_id != str(websocket_id)
                        or entry.transaction_id != str(transaction_id)):
                    continue
                expected = {entry.initial_cseq}
                if entry.auth_cseq:
                    expected.add(entry.auth_cseq)
                if cseq not in expected:
                    return False
                if status_code in {401, 407} and cseq == entry.initial_cseq:
                    entry.challenge_seen = True
                    return True
                if status_code >= 200:
                    entry.invite_closed = True
                    return True
                return True
        return False

    def bind_channel(self, token: str, iid: str, generation: str,
                     source_call_id: str) -> bool:
        if not source_call_id or len(str(source_call_id)) > 160:
            return False
        with self._lock:
            now = self._clock()
            self._prune_locked(now)
            terminal = self._terminal.get(str(token))
            if terminal:
                # A token authorizes one call only.  A terminal record for this token blocks both
                # the exact delayed start and any inconsistent attempt to bind it to another ID.
                return False
            entry = self._entries.get(str(token))
            if (not entry or entry.iid != str(iid)
                    or entry.generation != str(generation) or not entry.transaction_id):
                return False
            if entry.source_call_id and entry.source_call_id != str(source_call_id):
                return False
            entry.source_call_id = str(source_call_id)
            return True

    def authorization_active(self, token: str, iid: str, generation: str,
                             source_call_id: str) -> bool:
        with self._lock:
            self._prune_locked(self._clock())
            entry = self._entries.get(str(token))
            return bool(entry and entry.iid == str(iid)
                        and entry.generation == str(generation)
                        and entry.transaction_id
                        and entry.source_call_id == str(source_call_id))

    def close_call(self, token: str, iid: str, source_call_id: str) -> bool:
        if not source_call_id or len(str(source_call_id)) > 160:
            return False
        with self._lock:
            now = self._clock()
            self._prune_locked(now)
            terminal = self._terminal.get(str(token))
            if terminal:
                return bool(terminal.iid == str(iid)
                            and terminal.source_call_id == str(source_call_id))
            entry = self._entries.get(str(token))
            if (not entry or entry.iid != str(iid)
                    or (entry.source_call_id
                        and entry.source_call_id != str(source_call_id))):
                return False
            self._entries.pop(str(token), None)
            self._terminal[str(token)] = _TerminalCall(
                str(iid), str(source_call_id), now)
            return True

    def release_websocket(self, websocket_id: str) -> list[dict]:
        with self._lock:
            owned = [token for token, entry in self._entries.items()
                     if entry.websocket_id == str(websocket_id)]
            authorized = [{"token": token, "iid": self._entries[token].iid,
                           "generation": self._entries[token].generation,
                           "transaction_id": self._entries[token].transaction_id,
                           "target": self._entries[token].target,
                           "source_call_id": self._entries[token].source_call_id}
                          for token in owned if self._entries[token].transaction_id]
            for token in owned:
                self._entries.pop(token, None)
            return authorized


registry = MediaAdmissionRegistry()
