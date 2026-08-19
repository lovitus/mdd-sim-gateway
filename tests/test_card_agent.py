import struct
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


def test_websocket_transport_preserves_bytes_read_with_http_upgrade():
    from agent.card_agent import WebSocketClientTransport

    class Socket:
        def recv(self, _size):
            return b""

    transport = WebSocketClientTransport(Socket(), b"\x82\x02OK")
    assert transport.recv_frame() == b"OK"


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
