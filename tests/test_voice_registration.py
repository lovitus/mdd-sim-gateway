from unittest.mock import Mock
import json
import threading

from agent.voice_registration import VoiceRegistrationMaintainer


def responder(values):
    def command(value):
        response = values[value]
        if isinstance(response, Exception):
            raise response
        return response.encode() if isinstance(response, str) else response
    return Mock(side_effect=command)


def base(**changes):
    values = {
        "AT+CREG?": "+CREG: 0,0\r\nOK\r\n",
        "AT+CEREG?": "+CEREG: 0,0\r\nOK\r\n",
        'AT+QCFG="ims"': '+QCFG: "ims",0,0\r\nOK\r\n',
        "AT+CFUN?": "+CFUN: 1\r\nOK\r\n",
        "AT+COPS?": "+COPS: 0\r\nOK\r\n",
        'AT+QENG="servingcell"':
            '+QENG: "servingcell","LIMSRV","LTE","FDD",460,11\r\nOK\r\n',
        'AT+QMBNCFG="List"':
            '+QMBNCFG: "List",2,1,1,"VoLTE_OPNMKT_CT",0x050113FC,202402281\r\nOK\r\n',
        'AT+QMBNCFG="AutoSel"': '+QMBNCFG: "AutoSel",1\r\nOK\r\n',
        'AT+QCFG="ims",1': "OK\r\n",
        'AT+QMBNCFG="Deactivate"': "OK\r\n",
        "AT+CFUN=1,1": OSError("port disappeared"),
    }
    values.update(changes)
    return values


def test_registered_cs_is_read_only():
    at = responder(base(**{"AT+CREG?": "+CREG: 0,5\r\nOK\r\n"}))
    result = VoiceRegistrationMaintainer().check(at, force=True)
    assert result.ready is True
    assert result.bearer == "cs"
    assert [call.args[0] for call in at.call_args_list] == ["AT+CREG?", "AT+CEREG?"]


def test_explicit_deregistration_restores_standard_automatic_mode_first():
    values = base(**{"AT+COPS?": "+COPS: 2\r\nOK\r\n", "AT+COPS=0": "OK\r\n"})
    replies = iter([values["AT+CREG?"], values["AT+CEREG?"], values['AT+QCFG="ims"'],
                    values["AT+CFUN?"], "+COPS: 2\r\nOK\r\n",
                    values['AT+QENG="servingcell"'], values["AT+COPS=0"],
                    "+COPS: 0\r\nOK\r\n"])
    at = Mock(side_effect=lambda _command: next(replies).encode())
    result = VoiceRegistrationMaintainer().check(at, force=True)
    assert result.action == "automatic_registration"
    assert result.restart_required is False
    assert 'AT+QCFG="ims",1' not in [call.args[0] for call in at.call_args_list]


def test_selected_active_volte_profile_enables_ims_once_and_restarts():
    values = base()
    # The readback after the write must prove config 1 before any reset is requested.
    calls = []
    def command(value):
        calls.append(value)
        if value == 'AT+QCFG="ims"' and calls.count(value) == 2:
            return b'+QCFG: "ims",1,0\r\nOK\r\n'
        response = values[value]
        if isinstance(response, Exception):
            raise response
        return response.encode()
    result = VoiceRegistrationMaintainer().check(command, force=True)
    assert result.action == "enable_selected_volte_profile"
    assert result.restart_required is True
    assert calls[-3:] == ['AT+QCFG="ims",1', 'AT+QCFG="ims"', "AT+CFUN=1,1"]


def test_unknown_or_inactive_profile_never_changes_persistent_ims():
    at = responder(base(**{
        'AT+QMBNCFG="List"':
            '+QMBNCFG: "List",0,1,1,"ROW_Generic_3GPP",0x1,1\r\nOK\r\n'}))
    result = VoiceRegistrationMaintainer().check(at, force=True)
    assert result.action == ""
    assert result.state == "pending"
    assert 'AT+QCFG="ims",1' not in [call.args[0] for call in at.call_args_list]


def test_explicit_ims_disable_is_never_overridden():
    at = responder(base(**{'AT+QCFG="ims"': '+QCFG: "ims",2,0\r\nOK\r\n'}))
    result = VoiceRegistrationMaintainer().check(at, force=True)
    assert result.action == ""
    assert 'AT+QMBNCFG="List"' not in [call.args[0] for call in at.call_args_list]
    assert 'AT+QCFG="ims",1' not in [call.args[0] for call in at.call_args_list]


def test_enabled_ims_reapplies_exact_active_volte_profile_only_once(tmp_path):
    values = base(**{'AT+QCFG="ims"': '+QCFG: "ims",1,0\r\nOK\r\n'})
    state = tmp_path / "voice.json"
    first_at = responder(values)
    first = VoiceRegistrationMaintainer(state_path=state)
    first.set_context("imei", "iccid", "firmware")

    result = first.check(first_at, force=True)

    assert result.action == "reapply_active_volte_profile"
    assert result.restart_required is True
    assert 'AT+QMBNCFG="Deactivate"' in [
        call.args[0] for call in first_at.call_args_list]

    second_at = responder(values)
    second = VoiceRegistrationMaintainer(state_path=state)
    second.set_context("imei", "iccid", "firmware")
    repeated = second.check(second_at, force=True)

    assert repeated.action == ""
    assert repeated.diagnostics["profile_reapply_attempted"] is True
    assert 'AT+QMBNCFG="Deactivate"' not in [
        call.args[0] for call in second_at.call_args_list]


def test_two_voice_maintainers_merge_concurrent_action_guards(tmp_path):
    state = tmp_path / "voice.json"
    first = VoiceRegistrationMaintainer(state_path=state)
    second = VoiceRegistrationMaintainer(state_path=state)
    first.set_context("imei-a", "iccid-a")
    second.set_context("imei-b", "iccid-b")
    barrier = threading.Barrier(3)

    def record(maintainer, profile):
        barrier.wait()
        maintainer._record_action("reapply_active_volte_profile", profile)

    threads = [threading.Thread(target=record, args=(first, "profile-a")),
               threading.Thread(target=record, args=(second, "profile-b"))]
    for thread in threads:
        thread.start()
    barrier.wait()
    for thread in threads:
        thread.join()

    saved = json.loads(state.read_text(encoding="utf-8"))
    assert first._action_key("reapply_active_volte_profile", "profile-a") in saved
    assert second._action_key("reapply_active_volte_profile", "profile-b") in saved


def test_reapplied_profile_gets_one_delayed_plain_restart(tmp_path):
    values = base(**{'AT+QCFG="ims"': '+QCFG: "ims",1,0\r\nOK\r\n'})
    state = tmp_path / "voice.json"
    maintainer = VoiceRegistrationMaintainer(
        state_path=state, action_cooldown=120)
    maintainer.set_context("imei", "iccid", "firmware")
    maintainer._record_action("reapply_active_volte_profile", "VoLTE_OPNMKT_CT")
    stored = maintainer._load_actions()
    key = maintainer._action_key("reapply_active_volte_profile", "VoLTE_OPNMKT_CT")
    stored[key] -= 121
    state.write_text(__import__("json").dumps(stored), encoding="utf-8")
    at = responder(values)

    result = maintainer.check(at, force=True)

    assert result.action == "stabilize_after_profile_reapply"
    assert result.restart_required is True
    assert 'AT+QMBNCFG="Deactivate"' not in [call.args[0] for call in at.call_args_list]
    assert "AT+CFUN=1,1" in [call.args[0] for call in at.call_args_list]
