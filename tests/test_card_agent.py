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
