from control.app.sms_content import is_displayable_sms_text


def test_normal_unicode_sms_is_displayable():
    assert is_displayable_sms_text("Hello\n香港 📱‍💬")


def test_binary_ota_payload_is_not_displayable():
    payload = "\x01\x08\x15RA\x10zÑÃû:§\x1bôb[Ú[h_"
    assert not is_displayable_sms_text(payload)


def test_empty_and_repeated_decode_failures_are_not_displayable():
    assert not is_displayable_sms_text("  \r\n")
    assert not is_displayable_sms_text("bad \ufffd\ufffd bytes")
