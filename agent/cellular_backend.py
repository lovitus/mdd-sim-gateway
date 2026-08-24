"""Private cellular dial API; no host IP sockets or host DNS are permitted here."""

from __future__ import annotations

import collections
import ipaddress
import os
import socket
import struct
import subprocess
import threading
import logging
from pathlib import Path


PROTOCOL_VERSION = 1
MAX_FRAME = 1024 * 1024
HEADER = struct.Struct("!BBHII")

HELLO = 1
RESOLVE = 2
TCP_OPEN = 3
TCP_WRITE = 4
TCP_CLOSE = 5
UDP_OPEN = 6
UDP_SEND = 7
UDP_CLOSE = 8
AT_COMMAND = 9
SHUTDOWN = 10
DATA_ENABLE = 11
DATA_DISABLE = 12
DNS_SERVER = 13
RAW_EXCHANGE = 14
ISOLATION_CHECK = 15
AT_COMMAND_V2 = 16
RESPONSE = 0x80
TCP_DATA = 0x90
TCP_EOF = 0x91
UDP_DATA = 0x92
LINK_STATE = 0x93
log = logging.getLogger("mdd-cellular-backend")


class CellularBackendError(RuntimeError):
    pass


def _target(host: str, port: int) -> bytes:
    encoded = str(host).encode("idna")
    if not encoded or len(encoded) > 253 or not 1 <= int(port) <= 65535:
        raise ValueError("invalid private cellular target")
    return struct.pack("!HH", len(encoded), int(port)) + encoded


class _PrivateTcpStream:
    def __init__(self, backend, handle: int):
        self.backend = backend
        self.handle = int(handle)
        self._buffer = bytearray()
        self._closed = False
        self._condition = threading.Condition()

    def sendall(self, data: bytes) -> None:
        payload = bytes(data)
        if self._closed:
            raise CellularBackendError("private TCP stream is closed")
        self.backend._request(TCP_WRITE, struct.pack("!I", self.handle) + payload)

    def recv(self, size: int) -> bytes:
        if size <= 0:
            return b""
        with self._condition:
            while not self._buffer and not self._closed:
                self._condition.wait()
            if not self._buffer:
                return b""
            value = bytes(self._buffer[:size])
            del self._buffer[:size]
            return value

    def close(self) -> None:
        if self._closed:
            return
        try:
            self.backend._request(TCP_CLOSE, struct.pack("!I", self.handle))
        finally:
            self._mark_closed()
            self.backend._remove_tcp(self.handle)

    def _feed(self, value: bytes) -> None:
        with self._condition:
            self._buffer.extend(value)
            self._condition.notify_all()

    def _mark_closed(self) -> None:
        with self._condition:
            self._closed = True
            self._condition.notify_all()


class _PrivateUdpEndpoint:
    def __init__(self, backend, handle: int):
        self.backend = backend
        self.handle = int(handle)
        self._packets = collections.deque()
        self._closed = False
        self._condition = threading.Condition()

    def sendto(self, data: bytes, target: tuple[str, int]) -> None:
        if self._closed:
            raise CellularBackendError("private UDP endpoint is closed")
        host, port = target
        # A UDP datagram is submitted exactly once.  The backend never transparently retries.
        payload = struct.pack("!I", self.handle) + _target(host, port) + bytes(data)
        self.backend._request(UDP_SEND, payload)

    def recvfrom(self) -> tuple[bytes, tuple[str, int]]:
        with self._condition:
            while not self._packets and not self._closed:
                self._condition.wait()
            if not self._packets:
                return b"", ("0.0.0.0", 0)
            return self._packets.popleft()

    def close(self) -> None:
        if self._closed:
            return
        try:
            self.backend._request(UDP_CLOSE, struct.pack("!I", self.handle))
        finally:
            with self._condition:
                self._closed = True
                self._condition.notify_all()
            self.backend._remove_udp(self.handle)

    def _feed(self, address: bytes, port: int, data: bytes) -> None:
        peer = (str(ipaddress.IPv4Address(address)), int(port))
        with self._condition:
            self._packets.append((bytes(data), peer))
            self._condition.notify_all()


class PrivateCellularBackend:
    """Multiplex one modem companion without exposing a local proxy listener."""

    def __init__(self, transport, *, process=None, watchdog=None):
        self.transport = transport
        self.process = process
        self.watchdog = watchdog
        self._send_lock = threading.Lock()
        # The companion executes control requests serially.  Matching that
        # contract here prevents a short isolation probe queued behind a
        # legitimate long PPP/SMS operation from timing out and revoking an
        # otherwise exclusively owned device.
        self._request_lock = threading.RLock()
        self._condition = threading.Condition()
        self._pending = {}
        self._tcp = {}
        self._udp = {}
        self._next_id = 1
        self._closed = False
        self._resources_closed = False
        self._close_lock = threading.Lock()
        self.disconnected = threading.Event()
        self.link_state = "starting"
        self.isolation_ready = False
        self.isolation_error = "isolation_not_proven: qualification has not completed"
        self.identity = {}
        self._stderr_thread = None
        self._reader = threading.Thread(
            target=self._read_loop, name="mdd-cellular-io", daemon=True)
        self._reader.start()

    @classmethod
    def launch(cls, executable: Path | str, device: dict):
        """Launch one child with anonymous IPC and a parent-death EOF capability."""
        parent, child = socket.socketpair(socket.AF_UNIX, socket.SOCK_STREAM)
        watch_read, watch_write = os.pipe()
        os.set_inheritable(child.fileno(), True)
        os.set_inheritable(watch_read, True)
        command = [
            str(executable), "--ipc-fd", str(child.fileno()),
            "--watch-fd", str(watch_read),
            "--vid", str(int(device["vid"])), "--pid", str(int(device["pid"])),
            "--bus", str(int(device["bus"])), "--address", str(int(device["address"])),
        ]
        process = None
        try:
            process = subprocess.Popen(
                command, pass_fds=(child.fileno(), watch_read), close_fds=True,
                stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                stderr=subprocess.PIPE, start_new_session=True)
        except Exception:
            parent.close()
            os.close(watch_write)
            raise
        finally:
            child.close()
            os.close(watch_read)
        backend = cls(parent, process=process, watchdog=watch_write)
        if process.stderr is not None:
            def drain_stderr():
                for raw in iter(process.stderr.readline, b""):
                    line = raw.decode("utf-8", errors="replace").rstrip()
                    if line:
                        log.warning("cellular companion: %s", line[:1000])
            backend._stderr_thread = threading.Thread(
                target=drain_stderr, name="mdd-cellular-io-stderr", daemon=True)
            backend._stderr_thread.start()
        try:
            hello = backend._request(HELLO, timeout=20).decode("utf-8", errors="replace")
            backend.identity = dict(
                item.split("=", 1) for item in hello.split(";") if "=" in item)
            if backend.identity.get("at_transactions") != "2":
                raise CellularBackendError(
                    "cellular companion is too old; transaction-safe AT parser v2 is required")
            return backend
        except Exception:
            backend.close()
            raise

    def enable(self) -> None:
        self._request(DATA_ENABLE, timeout=75)

    def disable(self, timeout: float = 15) -> None:
        self._request(DATA_DISABLE, timeout=max(0.1, min(15.0, float(timeout))))

    def resolve(self, hostname: str) -> str:
        value = self._request(RESOLVE, str(hostname).encode("idna"))
        if len(value) != 4:
            raise CellularBackendError("companion returned an invalid DNS result")
        return str(ipaddress.IPv4Address(value))

    def dns_server(self) -> str:
        value = self._request(DNS_SERVER)
        if len(value) != 4:
            raise CellularBackendError("companion returned an invalid negotiated DNS server")
        return str(ipaddress.IPv4Address(value))

    def open_tcp(self, host: str, port: int):
        value = self._request(TCP_OPEN, _target(host, port))
        if len(value) != 4:
            raise CellularBackendError("companion returned an invalid TCP handle")
        handle = struct.unpack("!I", value)[0]
        stream = _PrivateTcpStream(self, handle)
        self._tcp[handle] = stream
        return stream

    def open_udp(self):
        value = self._request(UDP_OPEN)
        if len(value) != 4:
            raise CellularBackendError("companion returned an invalid UDP handle")
        handle = struct.unpack("!I", value)[0]
        endpoint = _PrivateUdpEndpoint(self, handle)
        self._udp[handle] = endpoint
        return endpoint

    def at(self, command: str, timeout: float = 30) -> str:
        value = str(command).encode("ascii")
        if not value.startswith(b"AT") or b"\r" in value or b"\n" in value:
            raise ValueError("invalid AT command")
        request_timeout = max(0.3, min(30.0, float(timeout)))
        # Leave bounded IPC/drain scheduling margin so the helper terminates its USB
        # transaction before the Python caller's deadline.  A timed-out request must not keep
        # the single companion transaction lane occupied behind the caller's back.
        helper_timeout_ms = max(100, int((request_timeout - 0.2) * 1000))
        return self._request(
            AT_COMMAND_V2, struct.pack("!I", helper_timeout_ms) + value,
            timeout=request_timeout).decode(
                "utf-8", errors="replace")

    def exchange(self, payload: bytes) -> bytes:
        value = bytes(payload)
        if not value or len(value) > 65535:
            raise ValueError("invalid raw modem exchange")
        # SMS submission waits for one explicit network acknowledgement and must not be
        # retried.  Keep the existing modem-domain deadline while allowing that response.
        return self._request(RAW_EXCHANGE, value, timeout=195)

    def qualify(self) -> None:
        self._request(ISOLATION_CHECK, timeout=10)

    def revoke(self, reason: str) -> None:
        self.isolation_ready = False
        self.isolation_error = str(reason or "isolation_not_proven")[:500]
        self.link_state = "down"
        self._fail(CellularBackendError(self.isolation_error))
        self.close()

    def close(self) -> None:
        with self._close_lock:
            if self._resources_closed:
                return
            if not self._closed:
                try:
                    self._request(SHUTDOWN, timeout=5)
                except Exception:
                    pass
            self._fail(CellularBackendError("private cellular backend closed"))
            if self.watchdog is not None:
                os.close(self.watchdog)
                self.watchdog = None
            try:
                self.transport.close()
            except OSError:
                pass
            if self.process is not None:
                try:
                    self.process.wait(timeout=8)
                except subprocess.TimeoutExpired:
                    self.process.terminate()
                    self.process.wait(timeout=3)
            if self._stderr_thread is not None:
                self._stderr_thread.join(timeout=1)
            self._resources_closed = True

    def _request(self, message_type: int, payload: bytes = b"", timeout: float = 30) -> bytes:
        with self._request_lock:
            return self._request_serial(message_type, payload, timeout)

    def _request_serial(
            self, message_type: int, payload: bytes = b"", timeout: float = 30) -> bytes:
        with self._condition:
            if self._closed:
                raise CellularBackendError("private cellular companion is unavailable")
            request_id = self._next_id
            self._next_id += 1
            self._pending[request_id] = None
        frame = HEADER.pack(PROTOCOL_VERSION, message_type, 0, request_id, len(payload)) + payload
        try:
            with self._send_lock:
                self.transport.sendall(frame)
            with self._condition:
                if not self._condition.wait_for(
                        lambda: self._pending.get(request_id) is not None or self._closed,
                        timeout=timeout):
                    raise TimeoutError("private cellular companion request timed out")
                result = self._pending.pop(request_id, None)
            if self._closed and result is None:
                raise CellularBackendError("private cellular companion disconnected")
            status, value = result
            if status:
                detail = value.decode("utf-8", errors="replace") or f"error {status}"
                raise CellularBackendError(detail)
            return value
        except Exception:
            with self._condition:
                self._pending.pop(request_id, None)
            raise

    def _read_loop(self) -> None:
        try:
            while not self._closed:
                header = self._recv_exact(HEADER.size)
                version, message_type, _flags, request_id, length = HEADER.unpack(header)
                if version != PROTOCOL_VERSION or length > MAX_FRAME:
                    raise CellularBackendError("invalid private cellular frame")
                payload = self._recv_exact(length)
                if message_type == RESPONSE:
                    if len(payload) < 4:
                        raise CellularBackendError("short private cellular response")
                    status = struct.unpack("!i", payload[:4])[0]
                    with self._condition:
                        if request_id in self._pending:
                            self._pending[request_id] = (status, payload[4:])
                            self._condition.notify_all()
                elif message_type == TCP_DATA and len(payload) >= 4:
                    handle = struct.unpack("!I", payload[:4])[0]
                    if handle in self._tcp:
                        self._tcp[handle]._feed(payload[4:])
                elif message_type == TCP_EOF and len(payload) >= 4:
                    handle = struct.unpack("!I", payload[:4])[0]
                    if handle in self._tcp:
                        self._tcp[handle]._mark_closed()
                elif message_type == UDP_DATA and len(payload) >= 10:
                    handle, port = struct.unpack("!IH", payload[:6])
                    if handle in self._udp:
                        self._udp[handle]._feed(payload[6:10], port, payload[10:])
                elif message_type == LINK_STATE:
                    self.link_state = payload.decode("ascii", errors="replace")
        except Exception as exc:
            self._fail(exc)

    def _recv_exact(self, length: int) -> bytes:
        result = bytearray()
        while len(result) < length:
            chunk = self.transport.recv(length - len(result))
            if not chunk:
                raise CellularBackendError("private cellular companion closed its IPC channel")
            result.extend(chunk)
        return bytes(result)

    def _fail(self, error: Exception) -> None:
        with self._condition:
            if self._closed:
                return
            self._closed = True
            for stream in self._tcp.values():
                stream._mark_closed()
            for endpoint in self._udp.values():
                with endpoint._condition:
                    endpoint._closed = True
                    endpoint._condition.notify_all()
            for request_id, result in list(self._pending.items()):
                if result is None:
                    self._pending[request_id] = (-1, str(error).encode("utf-8"))
            self._condition.notify_all()
            self.disconnected.set()

    def _remove_tcp(self, handle: int) -> None:
        self._tcp.pop(handle, None)

    def _remove_udp(self, handle: int) -> None:
        self._udp.pop(handle, None)
