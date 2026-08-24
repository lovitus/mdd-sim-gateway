"""Low-cost, host-scoped Agent health reporting.

This channel deliberately does not share the per-ICCID modem control socket or any
per-reader VPCD socket.  It reports the health of one Agent installation even when no
hardware is attached, and it never performs hardware I/O or changes runtime state.
"""

from __future__ import annotations

import hashlib
import json
import logging
import os
import platform
import sys
import threading
import time
import urllib.parse
import uuid
from pathlib import Path

try:
    from card_agent import connect_wss
except ModuleNotFoundError:
    from .card_agent import connect_wss


log = logging.getLogger("mdd-agent-health")

HEARTBEAT_INTERVAL = 10.0
MAX_BACKOFF = 60.0


def _agent_version() -> str:
    candidates = []
    bundled = getattr(sys, "_MEIPASS", "")
    if bundled:
        candidates.append(Path(bundled) / "VERSION")
    candidates.append(Path(__file__).resolve().parent.parent / "VERSION")
    for path in candidates:
        try:
            value = path.read_text(encoding="ascii").strip()
        except OSError:
            continue
        if value and len(value) <= 40:
            return value
    return str(os.environ.get("MDD_AGENT_VERSION") or "unknown")[:40]


def _platform_meta() -> dict:
    if os.name == "nt":
        name, support, manager = "windows", "supported", "scm"
    elif sys.platform == "darwin":
        name, support, manager = "macos", "supported", "user-process"
    else:
        # The common transport is useful because it distinguishes a live Linux Agent from
        # an old Agent, but Linux host-health collection is intentionally not claimed yet.
        name, support, manager = "linux", "unsupported", "user-process"
    return {
        "platform": name,
        "arch": str(platform.machine() or "unknown")[:40],
        "agent_version": _agent_version(),
        "manager": manager,
        "collector": "native-v1" if support == "supported" else "unsupported",
        "support": support,
    }


def semantic_fingerprint(meta: dict, snapshot: dict) -> str:
    """Hash only stable health semantics; timestamps and counters live in envelopes."""
    encoded = json.dumps(
        {"meta": meta, "snapshot": snapshot}, ensure_ascii=True,
        sort_keys=True, separators=(",", ":"),
    ).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


class AgentHealthReporter:
    """One single-writer WebSocket reporter owned by ManagedAgentRuntime."""

    def __init__(self, *, config: dict, agent_id: str, snapshot_provider):
        self.config = dict(config)
        self.agent_id = str(agent_id or "")
        self.snapshot_provider = snapshot_provider
        self.run_id = uuid.uuid4().hex
        self.meta = _platform_meta()
        self._stop = threading.Event()
        self._dirty = threading.Event()
        self._transport_lock = threading.Lock()
        self._transport = None
        self._thread: threading.Thread | None = None
        self._revision = 0
        self._sequence = 0

    def start(self) -> None:
        if self._thread and self._thread.is_alive():
            return
        self._stop.clear()
        self._dirty.set()
        self._thread = threading.Thread(
            target=self._run, name="mdd-agent-health", daemon=True)
        self._thread.start()

    def notify_changed(self) -> None:
        # Runtime callbacks only mark the cached snapshot dirty. The single writer samples
        # and emits on its fixed 10-second tick, so callback bursts cannot flood the server.
        self._dirty.set()

    def stop(self, timeout: float = 5.0) -> bool:
        started = time.monotonic()
        self._stop.set()
        if self._thread:
            # Give a healthy session a brief chance to send its final stopped snapshot. A
            # black-holed ACK/write is then aborted and can never retain the hardware lease.
            self._thread.join(min(0.25, max(0.0, timeout)))
            if self._thread.is_alive():
                self._abort_transport()
                remaining = max(0.0, timeout - (time.monotonic() - started))
                self._thread.join(remaining)
        return not bool(self._thread and self._thread.is_alive())

    def _abort_transport(self) -> None:
        with self._transport_lock:
            transport = self._transport
        if transport is None:
            return
        try:
            abort = getattr(transport, "abort", None)
            (abort if abort is not None else transport.close)()
        except Exception:
            pass

    def _snapshot(self) -> dict:
        value = self.snapshot_provider()
        return dict(value) if isinstance(value, dict) else {}

    def _connect(self):
        host, separator, port = str(self.config.get("server") or "").rpartition(":")
        if not separator or not host or not port.isdigit():
            raise ValueError("Agent health server is invalid")
        path = str(self.config.get("health_path") or "/mdd/api/agent/health/ws")
        split = urllib.parse.urlsplit(path if path.startswith("/") else f"/{path}")
        query = dict(urllib.parse.parse_qsl(split.query, keep_blank_values=True))
        query["receipt"] = "1"
        path = urllib.parse.urlunsplit(("", "", split.path,
                                       urllib.parse.urlencode(query), ""))
        # connect_wss completes TLS pinning before it sends an authenticated HTTP Upgrade;
        # the token is an Authorization header and never appears in a URL/access log.
        ws = connect_wss(
            host, int(port), path, token=str(self.config.get("token") or ""),
            explicit_pin=str(self.config.get("pin") or ""), timeout=2.0)
        # Leave scheduling margin inside AgentHealthReporter.stop(timeout=5). A missing
        # receipt is diagnostic and must never retain the hardware installation lease.
        ws.settimeout(3)
        return ws

    def _full(self, kind: str, session_id: str, snapshot: dict) -> dict:
        return {
            "version": 1,
            "type": kind,
            "agent_id": self.agent_id,
            "run_id": self.run_id,
            "session_id": session_id,
            "seq": self._sequence,
            "revision": self._revision,
            "meta": self.meta,
            "snapshot": snapshot,
        }

    @staticmethod
    def _receive_receipt(
            ws, *, session_id: str, sequence: int, revision: int) -> None:
        """Confirm one scheduled frame and service protocol-level Ping frames.

        ``WebSocketClientTransport.recv`` consumes Ping frames and emits Pong before it
        returns the next text frame. Waiting for a small application receipt therefore
        keeps Uvicorn's WebSocket keepalive alive without a second reader/writer thread.
        """
        receipt = json.loads(ws.recv())
        if (set(receipt) != {"version", "type", "session_id", "seq", "revision"} or
                type(receipt.get("version")) is not int or receipt.get("version") != 1 or
                receipt.get("type") != "agent.health.received" or
                str(receipt.get("session_id") or "") != session_id or
                type(receipt.get("seq")) is not int or receipt.get("seq") != sequence or
                type(receipt.get("revision")) is not int or
                receipt.get("revision") != revision):
            raise RuntimeError("Agent health server returned an invalid frame receipt")

    def _serve(self, ws, on_ack=None) -> None:
        snapshot = self._snapshot()
        # stop is the generation fence. A stale reporter whose snapshot provider was blocked
        # must never attach after a replacement reporter has already started.
        if self._stop.is_set():
            return
        fingerprint = semantic_fingerprint(self.meta, snapshot)
        self._revision += 1
        self._sequence += 1
        hello = self._full("agent.health.hello", "", snapshot)
        ws.send(json.dumps(hello, separators=(",", ":")))
        ack = json.loads(ws.recv())
        if (type(ack.get("version")) is not int or ack.get("version") != 1 or
                ack.get("type") != "agent.health.ack"):
            raise RuntimeError("Agent health server did not acknowledge protocol v1")
        session_id = str(ack.get("session_id") or "")
        if not session_id:
            raise RuntimeError("Agent health server returned no session id")
        receipts_required = ack.get("receipt") == "required-v1"
        if on_ack is not None:
            on_ack()
        self._dirty.clear()
        next_tick = time.monotonic() + HEARTBEAT_INTERVAL
        while True:
            # Exactly one scheduled frame per interval. shutdown is the sole immediate frame
            # because it closes lifecycle state deterministically instead of waiting 10s.
            wait_seconds = max(0.0, next_tick - time.monotonic())
            if self._stop.wait(wait_seconds):
                final = self._snapshot()
                final_fingerprint = semantic_fingerprint(self.meta, final)
                if final_fingerprint != fingerprint:
                    self._revision += 1
                self._sequence += 1
                ws.send(json.dumps(
                    self._full("agent.health.shutdown", session_id, final),
                    separators=(",", ":")))
                return
            next_tick += HEARTBEAT_INTERVAL
            current = self._snapshot()
            if self._stop.is_set():
                final_fingerprint = semantic_fingerprint(self.meta, current)
                if final_fingerprint != fingerprint:
                    self._revision += 1
                self._sequence += 1
                ws.send(json.dumps(
                    self._full("agent.health.shutdown", session_id, current),
                    separators=(",", ":")))
                return
            self._dirty.clear()
            current_fingerprint = semantic_fingerprint(self.meta, current)
            if current_fingerprint != fingerprint:
                fingerprint = current_fingerprint
                self._revision += 1
                self._sequence += 1
                ws.send(json.dumps(
                    self._full("agent.health.status", session_id, current),
                    separators=(",", ":")))
            else:
                self._sequence += 1
                ws.send(json.dumps({
                    "version": 1,
                    "type": "agent.health.heartbeat",
                    "agent_id": self.agent_id,
                    "run_id": self.run_id,
                    "session_id": session_id,
                    "seq": self._sequence,
                    "revision": self._revision,
                }, separators=(",", ":")))
            if receipts_required:
                self._receive_receipt(
                    ws, session_id=session_id, sequence=self._sequence,
                    revision=self._revision)
            # A slow receipt must not add its latency forever, but also must not cause a burst
            # of catch-up frames after the connection recovers.
            now = time.monotonic()
            while next_tick <= now:
                next_tick += HEARTBEAT_INTERVAL

    def _run(self) -> None:
        backoff = 1.0
        while not self._stop.is_set():
            ws = None
            try:
                ws = self._connect()
                with self._transport_lock:
                    self._transport = ws
                if self._stop.is_set():
                    self._abort_transport()
                    return
                def mark_established():
                    nonlocal backoff
                    # A single successful hello/ack proves the endpoint recovered. A later
                    # transport loss starts at 1s even if startup failures had reached 60s.
                    backoff = 1.0
                self._serve(ws, mark_established)
            except Exception as exc:
                if not self._stop.is_set():
                    log.warning("Agent health connection failed: %s; retrying in %.0fs",
                                str(exc)[:300], backoff)
            finally:
                with self._transport_lock:
                    if self._transport is ws:
                        self._transport = None
                if ws is not None:
                    try:
                        ws.close()
                    except Exception:
                        pass
            if self._stop.wait(backoff):
                return
            backoff = min(MAX_BACKOFF, max(1.0, backoff * 2.0))
