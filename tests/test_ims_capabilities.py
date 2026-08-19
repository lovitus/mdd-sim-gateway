from control.app.main import _ims_capabilities


def test_registered_line_exposes_voice_and_sms_but_not_rcs():
    result = _ims_capabilities({"smsc": "+447785016005"}, {"state": "OK"}, True)
    assert result["voice"]["actual"] == "on"
    assert result["sms"]["actual"] == "on"
    assert result["rcs"]["actual"] == "unsupported"


def test_registration_and_offline_states_are_not_reported_as_available():
    registering = _ims_capabilities({"smsc": "+1"}, {"state": "REGISTERING"}, True)
    offline = _ims_capabilities({"smsc": "+1"}, {"state": "OK"}, False)
    assert registering["voice"]["actual"] == "starting"
    assert offline["voice"]["actual"] == "off"


def test_missing_smsc_degrades_only_sms():
    result = _ims_capabilities({"id": "1"}, {"state": "OK"}, True)
    assert result["voice"]["actual"] == "on"
    assert result["sms"]["actual"] == "degraded"
