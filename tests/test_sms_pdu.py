from agent.sms_pdu import decode_deliver, encode_submit


def test_encode_submit_uses_ucs2_and_excludes_smsc_octet_from_tpdu_size():
    pdu, size = encode_submit("+85246094054", "MDD 测试")
    assert pdu.startswith("0001000B91")
    assert "004D00440044" in pdu
    assert size == len(bytes.fromhex(pdu)) - 1


def test_decode_ucs2_sms_deliver():
    # SMSC omitted; sender +1234; SCTS 7 bytes; UCS-2 body "OK".
    value = decode_deliver("00000491214300080000000000000004004F004B", 7, "NEW")
    assert value["id"] == "7"
    assert value["peer"] == "+1234"
    assert value["body"] == "OK"
    assert value["displayable"] is True
    assert value["alphabet"] == "ucs2"


def test_decode_class2_8bit_data_is_not_rendered_as_latin1_text():
    # DCS F6 is 8-bit, class 2 (U/SIM-specific). The transport must acknowledge it without
    # exposing arbitrary OTA bytes as a user-visible message.
    value = decode_deliver("00000481023400F600000000000004010203FF", 8, "NEW")
    assert value["peer"] == "2043"
    assert value["dcs"] == 0xF6
    assert value["alphabet"] == "binary"
    assert value["displayable"] is False
    assert value["body"] == ""
