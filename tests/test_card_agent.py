import struct
import threading
import types
from agent.card_agent import is_forbidden_apdu, VPCD_CTRL_ATR, VPCD_CTRL_OFF, VPCD_CTRL_ON, VPCD_CTRL_RESET


def test_apdu_safety_guard():
    # 1. Normal read APDUs must be permitted
    get_response = bytes.fromhex("00C000001A")
    assert is_forbidden_apdu(get_response) is False

    select_applet = bytes.fromhex("00A4040007A0000000871002")
    assert is_forbidden_apdu(select_applet) is False

    authenticate_apdu = bytes.fromhex("80880081101234567890ABCDEF")
    assert is_forbidden_apdu(authenticate_apdu) is False

    # 2. ES10c DeleteProfile (tag 0xBF33) must be strictly blocked
    delete_esim_profile_apdu = bytes.fromhex("80E2900003BF3300")
    assert is_forbidden_apdu(delete_esim_profile_apdu) is True

    # 3. ISO 7816 DELETE FILE (INS=0xE4) must be blocked
    delete_file_apdu = bytes.fromhex("00E40000022F00")
    assert is_forbidden_apdu(delete_file_apdu) is True


def test_vpcd_protocol_constants():
    assert VPCD_CTRL_OFF == 0x01
    assert VPCD_CTRL_ON == 0x02
    assert VPCD_CTRL_RESET == 0x03
    assert VPCD_CTRL_ATR == 0x04

    # 2-byte big endian framing test
    header = struct.pack(">H", 12)
    assert len(header) == 2
    (decoded_len,) = struct.unpack(">H", header)
    assert decoded_len == 12


def test_macos_reader_connect_uses_fresh_explicit_protocol_attempts(monkeypatch):
    from agent import card_agent

    calls = []

    class Connection:
        def __init__(self, succeeds):
            self.succeeds = succeeds

        def connect(self, *, protocol=None):
            calls.append(("connect", protocol))
            if not self.succeeds:
                raise card_agent.CardConnectionException("wrong protocol")

        def disconnect(self):
            calls.append(("disconnect", None))

    class Reader:
        def __init__(self):
            self.created = 0

        def createConnection(self):
            self.created += 1
            return Connection(self.created == 2)

    protocols = types.SimpleNamespace(T0_protocol=1, T1_protocol=2)
    monkeypatch.setattr(card_agent, "CardConnection", protocols)
    monkeypatch.setattr(card_agent.sys, "platform", "darwin")
    reader = Reader()

    result = card_agent.connect_reader(reader)

    assert result.succeeds is True
    assert reader.created == 2
    assert calls == [("connect", 1), ("disconnect", None), ("connect", 2)]


def test_websocket_transport_preserves_bytes_read_with_http_upgrade():
    from agent.card_agent import WebSocketClientTransport

    class Socket:
        def recv(self, _size):
            return b""

    transport = WebSocketClientTransport(Socket(), b"\x82\x02OK")
    assert transport.recv_frame() == b"OK"


def test_secure_wss_pins_tls_before_auth_and_keeps_token_out_of_url(monkeypatch):
    from agent import card_agent

    events = []

    class TlsSocket:
        def __init__(self):
            self.response = b"HTTP/1.1 101 Switching Protocols\r\n\r\n"

        def getpeercert(self, binary_form=False):
            return b"certificate" if binary_form else {}

        def sendall(self, payload):
            events.append(("send", payload.decode("utf-8")))

        def recv(self, _size):
            value, self.response = self.response, b""
            return value

        def settimeout(self, _value):
            pass

        def close(self):
            pass

    tls = TlsSocket()
    context = types.SimpleNamespace(
        check_hostname=True, verify_mode=None,
        wrap_socket=lambda *_args, **_kwargs: tls)
    monkeypatch.setattr(card_agent.socket, "create_connection", lambda *_args, **_kwargs: object())
    monkeypatch.setattr(card_agent.ssl, "create_default_context", lambda: context)
    monkeypatch.setattr(card_agent, "verify_or_pin_fingerprint",
                        lambda *_args, **_kwargs: events.append(("pin", "ok")))

    transport = card_agent.connect_wss(
        "gateway.example", 8443, "/mdd/api/agent/health/ws",
        token="CANARY-SECRET", explicit_pin="AA")
    assert transport.sock is tls
    assert [item[0] for item in events] == ["pin", "send"]
    request = events[1][1]
    assert request.startswith("GET /mdd/api/agent/health/ws HTTP/1.1\r\n")
    assert "CANARY-SECRET" not in request.split(" HTTP/1.1", 1)[0]
    assert "Authorization: Bearer CANARY-SECRET\r\n" in request


def test_card_agent_tofu_fingerprint(tmp_path, monkeypatch):
    import ssl
    import pytest
    from agent.card_agent import format_fingerprint, verify_or_pin_fingerprint

    monkeypatch.setenv("HOME", str(tmp_path))

    cert1 = b"test-cert-bytes-version-1"
    cert2 = b"test-cert-bytes-version-2"
    fp1 = format_fingerprint(cert1)
    assert len(fp1) == 95
    assert ":" in fp1

    # 1. First time connect -> TOFU pins cert1
    verify_or_pin_fingerprint("192.168.1.100", cert1)

    # 2. Subsequent connect with cert1 -> succeeds
    verify_or_pin_fingerprint("192.168.1.100", cert1)

    # 3. Subsequent connect with cert2 -> raises SSLError (Mismatch)
    with pytest.raises(ssl.SSLError, match="MISMATCH"):
        verify_or_pin_fingerprint("192.168.1.100", cert2)

    # 4. Reset pin -> overwrites with cert2 and succeeds
    verify_or_pin_fingerprint("192.168.1.100", cert2, reset_pin=True)

    # 5. Explicit pin matching
    verify_or_pin_fingerprint("192.168.1.100", cert2, explicit_pin=format_fingerprint(cert2))


def test_agent_metadata_is_stable_and_defaults_to_auto_slot(tmp_path, monkeypatch):
    from urllib.parse import parse_qs, urlsplit
    from agent import card_agent
    from agent.card_agent import agent_ws_path, get_agent_id, stable_reader_id

    monkeypatch.setenv("HOME", str(tmp_path))
    monkeypatch.setattr(card_agent, "_agent_identity_cache", "")
    first = get_agent_id()
    assert first == get_agent_id()
    assert stable_reader_id(" USB Reader  00 00 ") == stable_reader_id("usb reader 00 00")

    params = parse_qs(urlsplit(agent_ws_path("/mdd/api/vpcd/ws", "USB Reader")).query)
    assert params["slot"] == ["auto"]
    assert params["agent_id"] == [first]
    assert params["reader_name"] == ["USB Reader"]


def test_pcsc_supervisor_starts_each_hotplugged_reader_once(monkeypatch):
    from agent import card_agent

    snapshots = iter([
        ["Reader A"],
        ["Reader A", "Reader B"],
        ["Reader A", "Reader B"],
    ])
    started = []
    stop = threading.Event()

    def fake_readers():
        try:
            return next(snapshots)
        except StopIteration:
            stop.set()
            return ["Reader A", "Reader B"]

    class Worker:
        def __init__(self, target, args, name, daemon):
            self.args = args
            self.alive = False

        def start(self):
            self.alive = True
            started.append(self.args[2])

        def is_alive(self):
            return self.alive

        def join(self, _timeout=None):
            self.alive = False

    monkeypatch.setattr(card_agent, "readers", fake_readers)
    monkeypatch.setattr(card_agent.threading, "Thread", Worker)
    monkeypatch.setattr(stop, "wait", lambda _seconds: None)

    assert card_agent.run_pcsc_reader_supervisor(
        "gateway", 8443, stop_event=stop, retry_delay=0.5)
    assert started == ["Reader A", "Reader B"]


def test_pcsc_supervisor_restarts_same_reader_after_confirmed_unplug(monkeypatch):
    from agent import card_agent

    stop = threading.Event()
    snapshots = iter([["Reader A"], [], [], ["Reader A"], ["Reader A"]])
    attempts = 0
    started = []

    def fake_readers():
        nonlocal attempts
        attempts += 1
        if attempts >= 5:
            stop.set()
        return next(snapshots)

    class Worker:
        def __init__(self, target, args, name, daemon):
            self.args = args
            self.name = name
            self.worker_stop = args[-1]
            self.started = False

        def start(self):
            self.started = True
            started.append(self.args[2])

        def is_alive(self):
            return self.started and not self.worker_stop.is_set()

        def join(self, _timeout=None):
            self.started = False

    monkeypatch.setattr(card_agent, "readers", fake_readers)
    monkeypatch.setattr(card_agent.threading, "Thread", Worker)
    monkeypatch.setattr(stop, "wait", lambda _seconds: None)

    assert card_agent.run_pcsc_reader_supervisor(
        "gateway", 8443, stop_event=stop, retry_delay=0.5)
    assert started == ["Reader A", "Reader A"]


def test_reader_bridge_reconnects_card_when_atr_is_requested_after_insert(monkeypatch):
    from agent import card_agent

    stop = threading.Event()

    class Card:
        def __init__(self):
            self.connection = object()
            self.reader_name = "Reader A"
            self.atr = b"old"
            self.connects = 0

        def find_and_connect(self):
            self.connects += 1
            self.connection = object()
            self.reader_name = "Reader A"
            self.atr = b"new-atr"
            return True

        def reset(self):
            self.connection = None
            self.reader_name = None
            self.atr = b""

        def transmit(self, _payload):
            return b"response"

        def disconnect(self):
            self.connection = None
            self.atr = b""

    card = Card()

    class Transport:
        def __init__(self):
            self.frames = [bytes([VPCD_CTRL_RESET]), bytes([VPCD_CTRL_ATR]), None]
            self.sent = []

        def recv_frame(self):
            value = self.frames.pop(0)
            if value is None:
                stop.set()
            return value

        def send_frame(self, value):
            self.sent.append(value)

        def close(self):
            pass

    transport = Transport()
    monkeypatch.setattr(card_agent, "PhysicalCardClient", lambda _name: card)
    monkeypatch.setattr(card_agent, "connect_wss", lambda *_args, **_kwargs: transport)

    card_agent.run_reader_bridge(
        "gateway", 8443, "Reader A", use_wss=True, retry_delay=0,
        stop_event=stop,
    )
    assert card.connects == 1
    assert transport.sent == [b"new-atr"]


def test_pcsc_supervisor_logs_repeated_discovery_failure_once(monkeypatch, caplog):
    from agent import card_agent

    stop = threading.Event()
    attempts = 0

    def failing_readers():
        nonlocal attempts
        attempts += 1
        if attempts == 3:
            stop.set()
        raise RuntimeError("resource manager is stopped")

    monkeypatch.setattr(card_agent, "readers", failing_readers)
    monkeypatch.setattr(stop, "wait", lambda _seconds: None)

    assert card_agent.run_pcsc_reader_supervisor(
        "gateway", 8443, stop_event=stop, retry_delay=0.5)
    messages = [record.message for record in caplog.records
                if "PC/SC discovery failed" in record.message]
    assert messages == ["PC/SC discovery failed: resource manager is stopped"]


def test_wss_entrypoint_uses_hotplug_supervisor_without_initial_reader(monkeypatch):
    from agent import card_agent

    calls = []
    monkeypatch.setattr(card_agent, "readers", lambda: [])
    monkeypatch.setattr(card_agent, "acquire_unified_windows_lock", lambda: True)
    monkeypatch.setattr(
        card_agent,
        "run_pcsc_reader_supervisor",
        lambda *args, **kwargs: calls.append((args, kwargs)) or True,
    )
    monkeypatch.setattr(
        card_agent.sys,
        "argv",
        ["card_agent.py", "--gateway", "gateway", "--port", "8443", "--token", "token"],
    )

    assert card_agent.main() == 0
    assert len(calls) == 1
    assert calls[0][0] == ("gateway", 8443)
    assert calls[0][1]["reader_filter"] == ""
