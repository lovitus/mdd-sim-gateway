import socket
import struct
import threading
import os

from agent import cellular_backend as protocol
from agent.cellular_backend import PrivateCellularBackend


def _serve(transport, requests, stop):
    next_handle = 20
    while not stop.is_set():
        header = transport.recv(protocol.HEADER.size)
        if not header:
            return
        version, message_type, _flags, request_id, length = protocol.HEADER.unpack(header)
        payload = b""
        while len(payload) < length:
            payload += transport.recv(length - len(payload))
        requests.append((message_type, payload))
        if message_type in {protocol.RESOLVE, protocol.DNS_SERVER}:
            value = b"\xcb\x00\x71\x09"
        elif message_type in {protocol.TCP_OPEN, protocol.UDP_OPEN}:
            next_handle += 1
            value = struct.pack("!I", next_handle)
        elif message_type == protocol.SHUTDOWN:
            value = b""
            stop.set()
        else:
            value = b""
        transport.sendall(protocol.HEADER.pack(
            version, protocol.RESPONSE, 0, request_id, len(value) + 4) +
            struct.pack("!i", 0) + value)


def test_private_backend_dns_tcp_and_udp_use_only_inherited_transport(monkeypatch):
    parent, child = socket.socketpair()
    requests = []
    stop = threading.Event()
    thread = threading.Thread(target=_serve, args=(child, requests, stop), daemon=True)
    thread.start()
    # A host DNS or Internet socket fallback would fail this test immediately.
    monkeypatch.setattr(socket, "getaddrinfo", lambda *_a, **_k: (_ for _ in ()).throw(
        AssertionError("host DNS used")))
    monkeypatch.setattr(socket, "create_connection", lambda *_a, **_k: (_ for _ in ()).throw(
        AssertionError("host TCP used")))
    backend = PrivateCellularBackend(parent)

    backend.enable()
    assert backend.resolve("example.com") == "203.0.113.9"
    assert backend.dns_server() == "203.0.113.9"
    tcp = backend.open_tcp("example.com", 443)
    tcp.sendall(b"hello")
    udp = backend.open_udp()
    udp.sendto(b"one datagram", ("dns.example", 53))
    tcp.close()
    udp.close()
    backend.disable()
    backend.close()
    thread.join(1)

    assert [item[0] for item in requests] == [
        protocol.DATA_ENABLE, protocol.RESOLVE, protocol.DNS_SERVER,
        protocol.TCP_OPEN, protocol.TCP_WRITE,
        protocol.UDP_OPEN, protocol.UDP_SEND, protocol.TCP_CLOSE,
        protocol.UDP_CLOSE, protocol.DATA_DISABLE, protocol.SHUTDOWN,
    ]
    assert sum(item[0] == protocol.UDP_SEND for item in requests) == 1


def test_at_command_carries_a_helper_deadline_inside_the_serialized_request():
    parent, child = socket.socketpair()
    requests = []
    stop = threading.Event()
    thread = threading.Thread(target=_serve, args=(child, requests, stop), daemon=True)
    thread.start()
    backend = PrivateCellularBackend(parent)

    backend.at("AT+CLCC", timeout=5)
    backend.close()
    thread.join(1)

    message_type, payload = requests[0]
    assert message_type == protocol.AT_COMMAND_V2
    timeout_ms = struct.unpack("!I", payload[:4])[0]
    assert 4500 <= timeout_ms < 5000
    assert payload[4:] == b"AT+CLCC"


def test_private_tcp_and_udp_events_are_multiplexed_by_handle():
    parent, child = socket.socketpair()
    requests = []
    stop = threading.Event()
    thread = threading.Thread(target=_serve, args=(child, requests, stop), daemon=True)
    thread.start()
    backend = PrivateCellularBackend(parent)
    tcp = backend.open_tcp("example.com", 80)
    udp = backend.open_udp()

    tcp_payload = struct.pack("!I", tcp.handle) + b"reply"
    child.sendall(protocol.HEADER.pack(1, protocol.TCP_DATA, 0, 0, len(tcp_payload)) + tcp_payload)
    udp_payload = struct.pack("!IH", udp.handle, 53) + b"\x08\x08\x08\x08" + b"dns"
    child.sendall(protocol.HEADER.pack(1, protocol.UDP_DATA, 0, 0, len(udp_payload)) + udp_payload)

    assert tcp.recv(16) == b"reply"
    assert udp.recvfrom() == (b"dns", ("8.8.8.8", 53))
    backend.close()


def test_disconnect_notifies_owner_and_close_still_releases_watchdog():
    parent, child = socket.socketpair()
    watch_read, watch_write = os.pipe()

    class ExitedProcess:
        def __init__(self):
            self.waited = False

        def wait(self, timeout=None):
            self.waited = True
            return 7

    process = ExitedProcess()
    backend = PrivateCellularBackend(parent, process=process, watchdog=watch_write)
    child.close()

    assert backend.disconnected.wait(1)
    backend.close()
    assert process.waited
    try:
        os.fstat(watch_write)
        assert False, "watchdog descriptor leaked after a prior IPC disconnect"
    except OSError:
        pass
    os.close(watch_read)


def test_isolation_revocation_closes_all_private_sessions_fail_closed():
    parent, child = socket.socketpair()
    requests = []
    stop = threading.Event()
    thread = threading.Thread(target=_serve, args=(child, requests, stop), daemon=True)
    thread.start()
    backend = PrivateCellularBackend(parent)
    backend.isolation_ready = True
    tcp = backend.open_tcp("example.com", 443)
    udp = backend.open_udp()

    backend.revoke("isolation_not_proven: routes changed")

    assert backend.disconnected.is_set()
    assert not backend.isolation_ready
    assert backend.link_state == "down"
    assert tcp.recv(1) == b""
    assert udp.recvfrom() == (b"", ("0.0.0.0", 0))
