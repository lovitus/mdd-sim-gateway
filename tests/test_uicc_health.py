from unittest.mock import Mock
import threading

from agent.uicc_health import UiccHealthMaintainer


def responder(values):
    def command(value):
        response = values[value]
        if isinstance(response, Exception):
            raise response
        return response.encode() if isinstance(response, str) else response
    return Mock(side_effect=command)


def test_registered_domain_with_hidden_pin_does_not_touch_radio():
    at = responder({
        "AT+CREG?": "+CREG: 0,5\r\nOK\r\n",
        "AT+CEREG?": "+CEREG: 0,5\r\nOK\r\n",
        "AT+CPIN?": RuntimeError("CPIN is owned by MBIM"),
    })

    result = UiccHealthMaintainer().check(at, force=True)

    assert result.ready is True
    assert [call.args[0] for call in at.call_args_list] == [
        "AT+CREG?", "AT+CEREG?", "AT+CPIN?"
    ]


def test_stale_registration_cannot_mask_corroborated_sim_failure(tmp_path):
    pin_replies = iter([
        RuntimeError("+CME ERROR: SIM failure"),
        "+CPIN: READY\r\nOK\r\n",
    ])
    values = {
        "AT+CREG?": ["+CREG: 0,5\r\nOK\r\n", "+CREG: 0,5\r\nOK\r\n"],
        "AT+CEREG?": ["+CEREG: 0,5\r\nOK\r\n", "+CEREG: 0,5\r\nOK\r\n"],
        "AT+QSIMSTAT?": "+QSIMSTAT: 0,0\r\nOK\r\n",
        "AT+CFUN?": "+CFUN: 1\r\nOK\r\n",
        "AT+CFUN=0": "OK\r\n",
        "AT+CFUN=1": "OK\r\n",
    }

    def command(value):
        response = (next(pin_replies) if value == "AT+CPIN?" else
                    values[value].pop(0) if value in {"AT+CREG?", "AT+CEREG?"} else
                    values[value])
        if isinstance(response, Exception):
            raise response
        return response.encode()

    maintainer = UiccHealthMaintainer(
        state_path=tmp_path / "uicc.json", settle_timeout=1, poll_interval=.1,
        sleeper=lambda _seconds: None)
    maintainer.set_context("imei", "iccid", "firmware")

    result = maintainer.check(Mock(side_effect=command), force=True)

    assert result.state == "recovered"


def test_bootstrap_only_completes_interrupted_full_function_transition(tmp_path):
    at = responder({"AT+CFUN?": "+CFUN: 0\r\nOK\r\n", "AT+CFUN=1": "OK\r\n"})
    maintainer = UiccHealthMaintainer(state_path=tmp_path / "uicc.json")

    result = maintainer.ensure_full_function(at, "imei")

    assert result.state == "restarting"
    assert result.action == "restore_full_function"
    assert [call.args[0] for call in at.call_args_list] == ["AT+CFUN?", "AT+CFUN=1"]


def test_bootstrap_never_overrides_intentional_radio_off(tmp_path):
    at = responder({"AT+CFUN?": "+CFUN: 4\r\nOK\r\n"})
    result = UiccHealthMaintainer(
        state_path=tmp_path / "uicc.json").ensure_full_function(at, "imei")

    assert result.state == "unchanged"
    assert [call.args[0] for call in at.call_args_list] == ["AT+CFUN?"]


def test_last_successful_identity_is_available_for_early_recovery(tmp_path):
    state = tmp_path / "uicc.json"
    first = UiccHealthMaintainer(state_path=state)
    first.remember_identity("864819055504383", "89852312388530153089")

    restarted = UiccHealthMaintainer(state_path=state)

    assert restarted.known_iccid("864819055504383") == "89852312388530153089"


def test_two_uicc_maintainers_merge_concurrent_identities(tmp_path):
    state = tmp_path / "uicc.json"
    first = UiccHealthMaintainer(state_path=state)
    second = UiccHealthMaintainer(state_path=state)
    barrier = threading.Barrier(3)

    def remember(maintainer, imei, iccid):
        barrier.wait()
        maintainer.remember_identity(imei, iccid)

    threads = [
        threading.Thread(target=remember, args=(
            first, "864819055504383", "89852312388530153089")),
        threading.Thread(target=remember, args=(
            second, "862547055201716", "89852312388530152529")),
    ]
    for thread in threads:
        thread.start()
    barrier.wait()
    for thread in threads:
        thread.join()

    result = UiccHealthMaintainer(state_path=state)
    assert result.known_iccid("864819055504383") == "89852312388530153089"
    assert result.known_iccid("862547055201716") == "89852312388530152529"


def test_invalid_identity_is_never_persisted_as_recovery_authority(tmp_path):
    maintainer = UiccHealthMaintainer(state_path=tmp_path / "uicc.json")
    maintainer.remember_identity("not-an-imei", "89852312388530153089")
    maintainer.remember_identity("864819055504383", "short")

    assert maintainer.known_iccid("864819055504383") == ""


def test_pin_lock_is_reported_without_reset():
    at = responder({
        "AT+CREG?": "+CREG: 0,0\r\nOK\r\n",
        "AT+CEREG?": "+CEREG: 0,0\r\nOK\r\n",
        "AT+CPIN?": "+CPIN: SIM PIN\r\nOK\r\n",
    })
    maintainer = UiccHealthMaintainer()
    maintainer.set_context("imei", "iccid", "firmware")

    result = maintainer.check(at, force=True)

    assert result.ready is False
    assert result.state == "locked"
    assert "AT+CFUN=0" not in [call.args[0] for call in at.call_args_list]


def test_explicit_sim_failure_runs_one_standard_cfun_cycle_and_recovers(tmp_path):
    pin_replies = iter([
        RuntimeError("+CME ERROR: SIM failure"),
        "+CPIN: READY\r\nOK\r\n",
    ])
    values = {
        "AT+CREG?": ["+CREG: 0,0\r\nOK\r\n", "+CREG: 0,5\r\nOK\r\n"],
        "AT+CEREG?": ["+CEREG: 0,0\r\nOK\r\n", "+CEREG: 0,5\r\nOK\r\n"],
        "AT+QSIMSTAT?": "+QSIMSTAT: 0,0\r\nOK\r\n",
        "AT+CFUN?": "+CFUN: 1\r\nOK\r\n",
        "AT+CFUN=0": "OK\r\n",
        "AT+CFUN=1": "OK\r\n",
    }

    def command(value):
        if value == "AT+CPIN?":
            response = next(pin_replies)
        elif value in {"AT+CREG?", "AT+CEREG?"}:
            response = values[value].pop(0)
        else:
            response = values[value]
        if isinstance(response, Exception):
            raise response
        return response.encode()

    state = tmp_path / "uicc.json"
    maintainer = UiccHealthMaintainer(
        state_path=state, settle_timeout=1, poll_interval=0.1,
        sleeper=lambda _seconds: None)
    maintainer.set_context("imei", "iccid", "firmware")

    result = maintainer.check(Mock(side_effect=command), force=True)

    assert result.ready is True
    assert result.state == "recovered"
    assert result.action == "reinitialize_uicc"
    assert not maintainer._attempted()


def test_persistent_guard_prevents_reset_loop_after_failed_recovery(tmp_path):
    values = {
        "AT+CREG?": "+CREG: 0,0\r\nOK\r\n",
        "AT+CEREG?": "+CEREG: 0,0\r\nOK\r\n",
        "AT+CPIN?": RuntimeError("+CME ERROR: SIM failure"),
        "AT+QSIMSTAT?": "+QSIMSTAT: 0,0\r\nOK\r\n",
        "AT+CFUN?": "+CFUN: 1\r\nOK\r\n",
    }
    state = tmp_path / "uicc.json"
    first = UiccHealthMaintainer(state_path=state)
    first.set_context("imei", "iccid", "firmware")
    first._record_attempt()
    at = responder(values)

    result = UiccHealthMaintainer(state_path=state)
    result.set_context("imei", "iccid", "firmware")
    checked = result.check(at, force=True)

    assert checked.ready is False
    assert "already attempted" in checked.reason
    assert "AT+CFUN=0" not in [call.args[0] for call in at.call_args_list]


def test_interrupted_recovery_only_restores_full_function_mode(tmp_path):
    pin_replies = iter([
        RuntimeError("+CME ERROR: SIM failure"),
        "+CPIN: READY\r\nOK\r\n",
    ])
    values = {
        "AT+CREG?": ["+CREG: 0,0\r\nOK\r\n", "+CREG: 0,5\r\nOK\r\n"],
        "AT+CEREG?": ["+CEREG: 0,0\r\nOK\r\n", "+CEREG: 0,5\r\nOK\r\n"],
        "AT+QSIMSTAT?": "+QSIMSTAT: 0,0\r\nOK\r\n",
        "AT+CFUN?": "+CFUN: 0\r\nOK\r\n",
        "AT+CFUN=1": "OK\r\n",
    }

    def command(value):
        if value == "AT+CPIN?":
            response = next(pin_replies)
        elif value in {"AT+CREG?", "AT+CEREG?"}:
            response = values[value].pop(0)
        else:
            response = values[value]
        if isinstance(response, Exception):
            raise response
        return response.encode()

    state = tmp_path / "uicc.json"
    maintainer = UiccHealthMaintainer(
        state_path=state, settle_timeout=1, poll_interval=0.1,
        sleeper=lambda _seconds: None)
    maintainer.set_context("imei", "iccid", "firmware")
    maintainer._record_attempt()
    at = Mock(side_effect=command)

    checked = maintainer.check(at, force=True)

    assert checked.ready is True
    assert "AT+CFUN=0" not in [call.args[0] for call in at.call_args_list]
    assert "AT+CFUN=1" in [call.args[0] for call in at.call_args_list]


def test_unknown_state_or_missing_prior_identity_never_resets():
    at = responder({
        "AT+CREG?": "+CREG: 0,0\r\nOK\r\n",
        "AT+CEREG?": "+CEREG: 0,0\r\nOK\r\n",
        "AT+CPIN?": RuntimeError("+CME ERROR: SIM failure"),
        "AT+QSIMSTAT?": "+QSIMSTAT: 0,0\r\nOK\r\n",
    })
    maintainer = UiccHealthMaintainer()
    maintainer.set_context("imei", "", "firmware")

    result = maintainer.check(at, force=True)

    assert result.ready is False
    assert "prior SIM identity" in result.reason
    assert "AT+CFUN=0" not in [call.args[0] for call in at.call_args_list]


def test_missing_durable_state_refuses_reset_loop():
    at = responder({
        "AT+CREG?": "+CREG: 0,0\r\nOK\r\n",
        "AT+CEREG?": "+CEREG: 0,0\r\nOK\r\n",
        "AT+CPIN?": RuntimeError("+CME ERROR: SIM failure"),
        "AT+QSIMSTAT?": "+QSIMSTAT: 0,0\r\nOK\r\n",
    })
    maintainer = UiccHealthMaintainer()
    maintainer.set_context("imei", "iccid", "firmware")

    result = maintainer.check(at, force=True)

    assert result.ready is False
    assert "durable recovery state" in result.reason
    assert "AT+CFUN=0" not in [call.args[0] for call in at.call_args_list]
