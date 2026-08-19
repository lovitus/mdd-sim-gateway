"""Small cross-platform SOCKS5 TCP/UDP server with a pinned outbound source address."""
from __future__ import annotations

import select
import socket
import struct
import threading


def _exact(stream: socket.socket, size: int) -> bytes:
    data = bytearray()
    while len(data) < size:
        chunk = stream.recv(size - len(data))
        if not chunk:
            raise OSError("SOCKS client disconnected")
        data.extend(chunk)
    return bytes(data)


def _address(stream: socket.socket, atyp: int) -> tuple[str, int]:
    if atyp == 1:
        host = socket.inet_ntoa(_exact(stream, 4))
    elif atyp == 3:
        host = _exact(stream, _exact(stream, 1)[0]).decode("idna")
    elif atyp == 4:
        host = socket.inet_ntop(socket.AF_INET6, _exact(stream, 16))
    else:
        raise OSError("unsupported SOCKS address type")
    return host, struct.unpack("!H", _exact(stream, 2))[0]


def _packet_address(packet: bytes, offset: int = 3) -> tuple[str, int, int]:
    atyp = packet[offset]
    if atyp == 1:
        host, offset = socket.inet_ntoa(packet[offset + 1:offset + 5]), offset + 5
    elif atyp == 3:
        length = packet[offset + 1]
        host, offset = packet[offset + 2:offset + 2 + length].decode("idna"), offset + 2 + length
    elif atyp == 4:
        host, offset = socket.inet_ntop(socket.AF_INET6, packet[offset + 1:offset + 17]), offset + 17
    else:
        raise OSError("unsupported SOCKS UDP address type")
    return host, struct.unpack("!H", packet[offset:offset + 2])[0], offset + 2


def _encoded_address(host: str, port: int) -> bytes:
    try:
        value = socket.inet_aton(host)
        return b"\x01" + value + struct.pack("!H", port)
    except OSError:
        encoded = host.encode("idna")
        return b"\x03" + bytes([len(encoded)]) + encoded + struct.pack("!H", port)


class SocksServer:
    def __init__(self, listen_host: str, port: int, source_ip: str):
        self.listen_host, self.port, self.source_ip = listen_host, int(port), source_ip
        self.listener = None
        self.thread = None
        self._connections = set()
        self._connections_lock = threading.Lock()

    @property
    def ready(self) -> bool:
        return bool(self.listener)

    def start(self):
        if self.listener:
            return
        listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        listener.bind((self.listen_host, self.port))
        self.port = listener.getsockname()[1]
        listener.listen(32)
        self.listener = listener
        self.thread = threading.Thread(target=self._serve, name="cellular-socks5", daemon=True)
        self.thread.start()

    def close(self):
        listener, self.listener = self.listener, None
        if listener:
            listener.close()
        with self._connections_lock:
            connections = tuple(self._connections)
            self._connections.clear()
        for connection in connections:
            try:
                connection.close()
            except OSError:
                pass

    def _track(self, connection: socket.socket):
        with self._connections_lock:
            self._connections.add(connection)

    def _forget(self, connection: socket.socket):
        with self._connections_lock:
            self._connections.discard(connection)

    def _serve(self):
        while self.listener:
            try:
                client, _ = self.listener.accept()
                self._track(client)
                threading.Thread(target=self._client, args=(client,), daemon=True).start()
            except OSError:
                break

    def _client(self, client: socket.socket):
        try:
            with client:
                try:
                    version, count = _exact(client, 2)
                    methods = _exact(client, count)
                    if version != 5 or 0 not in methods:
                        client.sendall(b"\x05\xff")
                        return
                    client.sendall(b"\x05\x00")
                    version, command, _, atyp = _exact(client, 4)
                    host, port = _address(client, atyp)
                    if version != 5:
                        return
                    if command == 1:
                        self._tcp(client, host, port)
                    elif command == 3:
                        self._udp(client)
                    else:
                        client.sendall(b"\x05\x07\x00\x01\x00\x00\x00\x00\x00\x00")
                except OSError:
                    return
        finally:
            self._forget(client)

    def _tcp(self, client: socket.socket, host: str, port: int):
        upstream = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self._track(upstream)
        try:
            upstream.bind((self.source_ip, 0))
            upstream.settimeout(15)
            upstream.connect((host, port))
            bound = upstream.getsockname()
            client.sendall(b"\x05\x00\x00" + _encoded_address(bound[0], bound[1]))
            upstream.settimeout(None)
            while True:
                readable, _, _ = select.select([client, upstream], [], [], 60)
                if not readable:
                    continue
                for source in readable:
                    data = source.recv(65536)
                    if not data:
                        return
                    (upstream if source is client else client).sendall(data)
        except OSError:
            try:
                client.sendall(b"\x05\x05\x00\x01\x00\x00\x00\x00\x00\x00")
            except OSError:
                pass
        finally:
            upstream.close()
            self._forget(upstream)

    def _udp(self, control: socket.socket):
        relay = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        outbound = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self._track(relay)
        self._track(outbound)
        try:
            relay.bind((self.listen_host, 0))
            outbound.bind((self.source_ip, 0))
            address = relay.getsockname()
            control.sendall(b"\x05\x00\x00" + _encoded_address(address[0], address[1]))
            client_address = None
            while True:
                readable, _, _ = select.select([control, relay, outbound], [], [], 60)
                if control in readable and not control.recv(1):
                    return
                if relay in readable:
                    packet, client_address = relay.recvfrom(65535)
                    if len(packet) < 10 or packet[:3] != b"\x00\x00\x00":
                        continue
                    host, port, offset = _packet_address(packet)
                    outbound.sendto(packet[offset:], (host, port))
                if outbound in readable and client_address:
                    payload, remote = outbound.recvfrom(65535)
                    relay.sendto(b"\x00\x00\x00" + _encoded_address(remote[0], remote[1]) + payload,
                                 client_address)
        finally:
            relay.close()
            outbound.close()
            self._forget(relay)
            self._forget(outbound)
