#!/usr/bin/env python3
"""Engine-local AudioSocket <-> authenticated Control WSS relay.

The listener is loopback-only and accepts only UUIDs explicitly reserved over the current
Engine incarnation's persistent WSS.  Public clients never reach this port; their PCM stays on
the existing management WS/WSS origin.
"""

from __future__ import annotations

import base64
import hashlib
import json
import logging
import os
import queue
import secrets
import socket
import ssl
import struct
import threading
import time
import urllib.parse
import uuid
from dataclasses import dataclass, field

from cryptography import x509
from cryptography.hazmat.primitives import serialization


LISTEN_HOST = "127.0.0.1"
LISTEN_PORT = 9073
PCM_FRAME_BYTES = 320
MAX_RESERVATIONS = 16
MAX_WS_MESSAGE = 65536
MAX_QUEUE_FRAMES = 12
WS_GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

logging.basicConfig(level=logging.INFO, format="%(asctime)s [media-relay] %(message)s")
log = logging.getLogger("mdd.engine.media_relay")


class RelayError(RuntimeError):
    pass


def _read_engine_env(path: str = "/run/mdd-sim-gateway/engine.env") -> dict[str, str]:
    values: dict[str, str] = {}
    try:
        with open(path, encoding="utf-8") as handle:
            for raw in handle:
                key, separator, value = raw.strip().partition("=")
                if not separator or not key:
                    continue
                if len(value) >= 2 and value[0] == value[-1] == "'":
                    value = value[1:-1].replace("'\"'\"'", "'")
                values[key] = value
    except OSError:
        pass
    return values


def _spki_digest(certificate: bytes) -> bytes:
    cert = x509.load_der_x509_certificate(certificate)
    spki = cert.public_key().public_bytes(
        serialization.Encoding.DER,
        serialization.PublicFormat.SubjectPublicKeyInfo,
    )
    return hashlib.sha256(spki).digest()


def _expected_spki(path: str) -> bytes:
    with open(path, "rb") as handle:
        value = handle.read(1024 * 1024)
    cert = x509.load_pem_x509_certificate(value)
    spki = cert.public_key().public_bytes(
        serialization.Encoding.DER,
        serialization.PublicFormat.SubjectPublicKeyInfo,
    )
    return hashlib.sha256(spki).digest()


class WebSocketTransport:
    def __init__(self, sock: socket.socket, initial: bytes = b""):
        self.sock = sock
        self.initial = bytearray(initial)
        self.send_lock = threading.Lock()
        self.closed = False

    def _read_exact(self, length: int) -> bytes:
        value = bytearray()
        if self.initial:
            take = min(length, len(self.initial))
            value.extend(self.initial[:take])
            del self.initial[:take]
        while len(value) < length:
            chunk = self.sock.recv(length - len(value))
            if not chunk:
                raise ConnectionError("WebSocket closed")
            value.extend(chunk)
        return bytes(value)

    def send(self, opcode: int, payload: bytes) -> None:
        payload = bytes(payload)
        if self.closed or len(payload) > MAX_WS_MESSAGE:
            raise RelayError("invalid WebSocket send")
        header = bytearray([0x80 | opcode])
        if len(payload) < 126:
            header.append(0x80 | len(payload))
        elif len(payload) <= 65535:
            header.extend((0x80 | 126,))
            header.extend(struct.pack("!H", len(payload)))
        else:
            header.extend((0x80 | 127,))
            header.extend(struct.pack("!Q", len(payload)))
        mask = secrets.token_bytes(4)
        header.extend(mask)
        masked = bytes(byte ^ mask[index & 3] for index, byte in enumerate(payload))
        with self.send_lock:
            self.sock.sendall(bytes(header) + masked)

    def send_json(self, payload: dict) -> None:
        self.send(0x01, json.dumps(payload, separators=(",", ":")).encode("utf-8"))

    def receive(self) -> tuple[int, bytes]:
        while True:
            first, second = self._read_exact(2)
            if first & 0x70 or not first & 0x80:
                raise RelayError("fragmented or reserved WebSocket frame")
            opcode = first & 0x0F
            if second & 0x80:
                raise RelayError("server WebSocket frame must not be masked")
            length = second & 0x7F
            if length == 126:
                length = struct.unpack("!H", self._read_exact(2))[0]
            elif length == 127:
                length = struct.unpack("!Q", self._read_exact(8))[0]
            if length > MAX_WS_MESSAGE or (opcode >= 0x08 and length > 125):
                raise RelayError("oversized WebSocket frame")
            payload = self._read_exact(length)
            if opcode == 0x08:
                raise ConnectionError("WebSocket close received")
            if opcode == 0x09:
                self.send(0x0A, payload)
                continue
            if opcode == 0x0A:
                continue
            if opcode not in (0x01, 0x02):
                raise RelayError("unsupported WebSocket opcode")
            return opcode, payload

    def close(self) -> None:
        if self.closed:
            return
        try:
            self.send(0x08, struct.pack("!H", 1000))
        except Exception:
            pass
        self.closed = True
        try:
            self.sock.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
        try:
            self.sock.close()
        except OSError:
            pass


def connect_wss(manager_url: str, iid: str, run_id: str, token: str,
                cert_path: str) -> WebSocketTransport:
    parsed = urllib.parse.urlsplit(manager_url)
    if parsed.scheme != "https" or not parsed.hostname:
        raise RelayError("Engine media relay requires an HTTPS manager URL")
    port = parsed.port or 443
    base_path = parsed.path.rstrip("/")
    query = urllib.parse.urlencode({"iid": iid, "engine_run_id": run_id})
    path = f"{base_path}/api/engine/media/ws?{query}"
    if "\r" in token or "\n" in token:
        raise RelayError("invalid Engine media token")
    raw = socket.create_connection((parsed.hostname, port), timeout=6.0)
    sock = None
    context = ssl.create_default_context()
    context.check_hostname = False
    context.verify_mode = ssl.CERT_REQUIRED
    context.load_verify_locations(cafile=cert_path)
    if hasattr(ssl, "VERIFY_X509_PARTIAL_CHAIN"):
        context.verify_flags |= ssl.VERIFY_X509_PARTIAL_CHAIN
    try:
        sock = context.wrap_socket(raw, server_hostname=parsed.hostname)
        peer = sock.getpeercert(binary_form=True)
        if not peer or not secrets.compare_digest(_spki_digest(peer), _expected_spki(cert_path)):
            raise ssl.SSLError("manager certificate SPKI pin mismatch")
        key = base64.b64encode(secrets.token_bytes(16)).decode("ascii")
        headers = [
            f"GET {path} HTTP/1.1", f"Host: {parsed.hostname}:{port}",
            "Upgrade: websocket", "Connection: Upgrade", f"Sec-WebSocket-Key: {key}",
            "Sec-WebSocket-Version: 13", f"X-MDD-Engine-Token: {token}", "", "",
        ]
        sock.sendall("\r\n".join(headers).encode("ascii"))
        response = bytearray()
        while b"\r\n\r\n" not in response and len(response) <= 16384:
            chunk = sock.recv(2048)
            if not chunk:
                break
            response.extend(chunk)
        marker = response.find(b"\r\n\r\n")
        if marker < 0:
            raise RelayError("incomplete WebSocket upgrade response")
        lines = response[:marker].decode("iso-8859-1").split("\r\n")
        if not lines or not lines[0].startswith("HTTP/1.1 101 "):
            raise RelayError("manager rejected Engine media WebSocket")
        fields = {}
        for line in lines[1:]:
            name, separator, value = line.partition(":")
            if separator:
                fields[name.strip().casefold()] = value.strip()
        expected = base64.b64encode(hashlib.sha1(
            (key + WS_GUID).encode("ascii")).digest()).decode("ascii")
        if not secrets.compare_digest(fields.get("sec-websocket-accept", ""), expected):
            raise RelayError("invalid WebSocket upgrade accept")
        sock.settimeout(None)
        return WebSocketTransport(sock, bytes(response[marker + 4:]))
    except BaseException:
        try:
            (sock or raw).close()
        except OSError:
            pass
        raise


@dataclass
class Reservation:
    audio_uuid: uuid.UUID
    expires_at: float
    pcm: queue.Queue = field(default_factory=lambda: queue.Queue(MAX_QUEUE_FRAMES))
    conn: socket.socket | None = None
    closed: threading.Event = field(default_factory=threading.Event)

    def close(self) -> None:
        if self.closed.is_set():
            return
        self.closed.set()
        if self.conn:
            try:
                self.conn.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            try:
                self.conn.close()
            except OSError:
                pass


class MediaRelay:
    def __init__(self, iid: str, run_id: str):
        self.iid = iid
        self.run_id = run_id
        self.ws: WebSocketTransport | None = None
        self.lock = threading.Lock()
        self.reservations: dict[uuid.UUID, Reservation] = {}
        self.listener: socket.socket | None = None

    def _remove(self, audio_uuid: uuid.UUID) -> None:
        with self.lock:
            reservation = self.reservations.pop(audio_uuid, None)
        if reservation:
            reservation.close()

    def _close_all(self) -> None:
        with self.lock:
            values = list(self.reservations.values())
            self.reservations.clear()
        for reservation in values:
            reservation.close()

    def reserve(self, message: dict) -> bool:
        try:
            audio_uuid = uuid.UUID(str(message.get("audio_uuid") or ""))
            ttl_ms = int(message.get("ttl_ms") or 0)
        except (ValueError, TypeError):
            return False
        if not 1000 <= ttl_ms <= 12000:
            return False
        with self.lock:
            expired = [key for key, value in self.reservations.items()
                       if value.expires_at <= time.monotonic()]
            for key in expired:
                self.reservations.pop(key).close()
            if audio_uuid in self.reservations or len(self.reservations) >= MAX_RESERVATIONS:
                return False
            self.reservations[audio_uuid] = Reservation(
                audio_uuid, time.monotonic() + ttl_ms / 1000.0)
        return True

    def send_pcm(self, audio_uuid: uuid.UUID, payload: bytes) -> None:
        ws = self.ws
        if not ws or len(payload) != PCM_FRAME_BYTES:
            raise RelayError("media WSS is unavailable")
        ws.send(0x02, audio_uuid.bytes + payload)

    def receive_pcm(self, payload: bytes) -> bool:
        if len(payload) != 16 + PCM_FRAME_BYTES:
            raise RelayError("invalid multiplexed PCM frame")
        audio_uuid = uuid.UUID(bytes=payload[:16])
        with self.lock:
            reservation = self.reservations.get(audio_uuid)
        if not reservation or reservation.closed.is_set():
            # Control may have released this UUID while its final valid binary frame was already
            # in flight.  Do not sacrifice the persistent multiplexed WSS or other reservations.
            return False
        try:
            reservation.pcm.put_nowait(payload[16:])
        except queue.Full:
            reservation.close()
            self._remove(audio_uuid)
            return False
        return True

    def _audio_reader(self, reservation: Reservation) -> None:
        conn = reservation.conn
        try:
            while not reservation.closed.is_set():
                header = _recv_exact(conn, 3)
                kind, length = struct.unpack("!BH", header)
                if kind == 0x00 and length == 0:
                    return
                if kind != 0x10 or length != PCM_FRAME_BYTES:
                    raise RelayError("unsupported AudioSocket frame")
                self.send_pcm(reservation.audio_uuid, _recv_exact(conn, length))
        finally:
            self._remove(reservation.audio_uuid)

    def _audio_writer(self, reservation: Reservation) -> None:
        conn = reservation.conn
        deadline = time.monotonic()
        try:
            while not reservation.closed.is_set():
                try:
                    payload = reservation.pcm.get(timeout=0.5)
                except queue.Empty:
                    continue
                deadline = max(deadline + 0.02, time.monotonic())
                delay = deadline - time.monotonic()
                if delay > 0:
                    time.sleep(delay)
                conn.sendall(struct.pack("!BH", 0x10, len(payload)) + payload)
        finally:
            self._remove(reservation.audio_uuid)

    def _accept_audio(self, conn: socket.socket) -> None:
        reservation = None
        try:
            conn.settimeout(3.0)
            kind, length = struct.unpack("!BH", _recv_exact(conn, 3))
            if kind != 0x01 or length != 16:
                raise RelayError("AudioSocket UUID handshake required")
            audio_uuid = uuid.UUID(bytes=_recv_exact(conn, 16))
            with self.lock:
                reservation = self.reservations.get(audio_uuid)
                if (not reservation or reservation.conn is not None or
                        reservation.expires_at <= time.monotonic()):
                    raise RelayError("unknown, duplicate or expired AudioSocket UUID")
                reservation.conn = conn
            conn.settimeout(None)
            threading.Thread(target=self._audio_reader, args=(reservation,), daemon=True).start()
            threading.Thread(target=self._audio_writer, args=(reservation,), daemon=True).start()
        except Exception:
            if reservation:
                self._remove(reservation.audio_uuid)
            else:
                try:
                    conn.close()
                except OSError:
                    pass

    def start_listener(self) -> None:
        listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        listener.bind((LISTEN_HOST, LISTEN_PORT))
        listener.listen(MAX_RESERVATIONS)
        self.listener = listener
        log.info("AudioSocket relay ready on loopback port %d", LISTEN_PORT)

        def accept_loop():
            while True:
                conn, _address = listener.accept()
                threading.Thread(target=self._accept_audio, args=(conn,), daemon=True).start()

        threading.Thread(target=accept_loop, name="media-relay-listener", daemon=True).start()

    def serve_wss(self, manager_url: str, token: str, cert_path: str,
                  on_ready=None) -> None:
        ws = connect_wss(manager_url, self.iid, self.run_id, token, cert_path)
        self.ws = ws
        ready = False
        try:
            ws.send_json({
                "type": "engine.media.hello", "version": 1,
                "iid": self.iid, "engine_run_id": self.run_id,
                "listen_port": LISTEN_PORT, "capacity": MAX_RESERVATIONS,
            })
            while True:
                opcode, payload = ws.receive()
                if opcode == 0x02:
                    self.receive_pcm(payload)
                    continue
                if len(payload) > 4096:
                    raise RelayError("Engine media control message is too large")
                message = json.loads(payload.decode("utf-8"))
                kind = message.get("type")
                if message.get("version") != 1:
                    raise RelayError("unsupported Engine media protocol")
                if kind == "engine.media.hello.ack":
                    if not ready:
                        ready = True
                        if on_ready:
                            on_ready()
                    continue
                if kind == "engine.media.reserve":
                    accepted = self.reserve(message)
                    ws.send_json({
                        "type": "engine.media.reserve.ack", "version": 1,
                        "audio_uuid": str(message.get("audio_uuid") or ""),
                        "accepted": accepted,
                    })
                elif kind == "engine.media.release":
                    try:
                        self._remove(uuid.UUID(str(message.get("audio_uuid") or "")))
                    except ValueError:
                        pass
                else:
                    raise RelayError("unsupported Engine media control message")
        finally:
            self.ws = None
            ws.close()
            self._close_all()


def _recv_exact(conn: socket.socket, length: int) -> bytes:
    value = bytearray()
    while len(value) < length:
        chunk = conn.recv(length - len(value))
        if not chunk:
            raise ConnectionError("AudioSocket closed")
        value.extend(chunk)
    return bytes(value)


class RetryBackoff:
    """Capped reconnect delay reset only by an acknowledged application handshake."""

    def __init__(self, initial: float = 1.0, maximum: float = 30.0):
        self.initial = float(initial)
        self.maximum = float(maximum)
        self.delay = self.initial

    def ready(self) -> None:
        self.delay = self.initial

    def next_delay(self) -> float:
        value = self.delay
        self.delay = min(self.maximum, self.delay * 2.0)
        return value


def main() -> None:
    env = _read_engine_env()
    iid = str(os.environ.get("MDD_ID") or env.get("MDD_ID") or "").strip()
    run_id = str(os.environ.get("MDD_ENGINE_RUN_ID") or "").strip()
    manager_url = str(env.get("MANAGER_URL") or "").strip()
    token = str(env.get("MANAGER_EVENT_TOKEN") or "")
    cert_path = "/etc/asterisk/certificate.crt"
    if not iid or not run_id or not manager_url or not token:
        raise SystemExit("media relay identity is incomplete")
    relay = MediaRelay(iid, run_id)
    relay.start_listener()
    backoff = RetryBackoff()
    while True:
        try:
            relay.serve_wss(manager_url, token, cert_path, on_ready=backoff.ready)
        except Exception as exc:
            delay = backoff.next_delay()
            log.warning("media WSS unavailable (%s); retrying in %.0fs", type(exc).__name__, delay)
        else:
            delay = backoff.next_delay()
        time.sleep(delay)


if __name__ == "__main__":
    main()
