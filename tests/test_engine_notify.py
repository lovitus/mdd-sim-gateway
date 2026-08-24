import base64
import importlib.util
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("mdd_engine_notify", ROOT / "engine" / "notify.py")
notify = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(notify)


def test_dialplan_protocol_decodes_untrusted_values_without_shell_syntax():
    peer = '"; touch /tmp/not-a-command; #'
    encoded = base64.b64encode(peer.encode()).decode()

    assert notify.parse_args([
        "call_in", "--call-id", "asterisk-linked.123", "--arg64", encoded,
    ]) == ("call_in", [peer], "asterisk-linked.123")


def test_dialplan_protocol_rejects_invalid_base64_and_correlation_ids():
    assert notify.parse_args(["call_in", "--arg64", "not base64!"]) is None
    assert notify.parse_args(["call_in", "--call-id", "bad id", "--arg64", "QQ=="]) is None


def test_rendered_hooks_use_python_and_structured_arguments():
    template = (ROOT / "engine" / "templates" / "extensions.conf.j2").read_text()
    hook_lines = [line for line in template.splitlines() if "notify.py" in line]

    assert hook_lines
    assert all("TrySystem(python3 /usr/local/bin/notify.py" in line for line in hook_lines)
    assert all("--arg64" in line for line in hook_lines if "call_" in line or "sms_" in line)
    assert all("--call-id" in line for line in hook_lines if "call_" in line)
    assert 'media_check --call-id "${CHANNEL(uniqueid)}" --arg64' in template
    assert "Set(TIMEOUT(absolute)=10)" in template
    assert 'call_out --call-id "${MDD_SOURCE_CALL_ID}"' in template
    assert 'BASE64_ENCODE(${MDD_MEDIA_TOKEN})' in template


def test_media_admission_token_is_redacted_from_persistent_event_log():
    source = (ROOT / "engine" / "notify.py").read_text()
    assert 'if event == "media_check"' in source
    assert 'event == "call_out"' in source
    assert 'event == "call_result"' in source
    assert '"<redacted>"' in source


def test_real_asterisk_call_legs_fail_closed_on_durable_pcscf_marker():
    template = (ROOT / "engine" / "templates" / "extensions.conf.j2").read_text()
    guard = "STAT(e,/run/mdd-sim-gateway/pcscf-rebind.json)"
    assert template.count(guard) >= 4
    inbound = template.split("[volte_ims]", 1)[1].split("[volte_ims_msg]", 1)[0]
    outbound = template.split("[from-local]", 1)[1].split("[ims-outbound-headers]", 1)[0]
    assert inbound.index(guard) < inbound.index("notify.py call_in")
    assert outbound.index(guard) < outbound.index("Dial(PJSIP/${EXTEN}@volte_ims")
    assert "exten => h,1" in inbound and "exten => h,1" in outbound


def test_local_sip_message_is_fenced_before_log_notify_or_carrier_send():
    template = (ROOT / "engine" / "templates" / "extensions.conf.j2").read_text()
    guard = "STAT(e,/run/mdd-sim-gateway/pcscf-rebind.json)"
    local_sms = template.split("[msg-from-local]", 1)[1]
    blocked = local_sms.index(guard)
    assert blocked < local_sms.index("FILE(/logs/messages.txt")
    assert blocked < local_sms.index("notify.py sms_out")
    assert blocked < local_sms.index("MessageSend(pjsip:volte_ims/")
    assert "n(rebind-blocked),Hangup(41)" in local_sms
