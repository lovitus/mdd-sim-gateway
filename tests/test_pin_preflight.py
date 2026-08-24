from control.app.main import _pin_preflight_detail


def test_pin_required_conflict_has_readable_message_and_attempts():
    detail = _pin_preflight_detail({"ok": False, "code": "pin_required", "tries": 3})
    assert detail == {
        "code": "pin_required",
        "tries": 3,
        "message": "Enter the SIM PIN before enabling VoWiFi. 3 attempts remain.",
    }


def test_no_card_conflict_does_not_invent_attempt_count():
    detail = _pin_preflight_detail({"ok": False, "code": "no_card"})
    assert detail["code"] == "no_card"
    assert detail["tries"] is None
    assert detail["message"] == "The SIM card is not available."
