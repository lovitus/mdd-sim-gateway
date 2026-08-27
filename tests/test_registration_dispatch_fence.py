import importlib.util
import hashlib
import fcntl
import json
import os
from pathlib import Path
import threading
from concurrent.futures import ThreadPoolExecutor

import pytest

from engine import admission_gate


ROOT = Path(__file__).parents[1]
PATCHER = ROOT / "engine/patches/asterisk/mdd_registration_fence.py"
FIXTURE = ROOT / "tests/fixtures/outbound_registration_dispatch.c"


def load_patcher():
    assert PATCHER.is_file(), "outbound-registration fence patcher is missing"
    spec = importlib.util.spec_from_file_location("registration_dispatch_fence", PATCHER)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def permit(nonce="a" * 32):
    return {
        "version": 2, "phase": "permit_issued", "permit_nonce": nonce,
        "campaign_epoch": "b" * 64, "engine_run_id": "run-current",
        "auth_seq_baseline": 7, "issued_at": 1000.0, "deadline": 1060.0,
    }


def write_permit(root, value=None):
    path = root / "usim-registration-permit.json"
    path.write_text(json.dumps(value or permit()), encoding="utf-8")
    return path


def test_patcher_gates_scheduler_handler_timer_transport_and_request_owned_ami_nonce():
    source = load_patcher().patch(FIXTURE.read_text(encoding="utf-8"))
    assert "\n+" not in source
    assert source.count('#include "asterisk/mdd_admission.h"') == 1
    assert source.count("MDD_REGISTRATION_DISPATCH_FENCE_V1") == 1
    handler = source.split("static int handle_client_registration", 1)[1].split(
        "static void sip_outbound_registration_timer_cb", 1)[0]
    schedule = source.split("static void schedule_registration", 1)[1].split(
        "static int registration_transport_shutdown_cb", 1)[0]
    assert "ast_mdd_registration_begin" in handler and "ast_mdd_registration_end" in handler
    assert handler.index("ast_mdd_registration_begin") < handler.index("pjsip_regc_register")
    assert handler.index("ast_mdd_registration_begin") < handler.index("registration_client_send")
    assert "ast_mdd_registration_fenced" in schedule and "deferred_registration_seconds" in schedule
    assert "MDDPermitNonce" in source and "MDDRearmOnly" in source
    assert "mdd_action_id" in source and 'ActionID: %s\\r\\n' in source
    ami = source.split("static int ami_register(", 1)[1]
    assert ami.index("mdd_queue_registration_request") < ami.index("return queue_registration")
    assert "permit_nonce" in source and "request" in source
    timer = source.split("sip_outbound_registration_timer_cb", 1)[1].split(
        "static void schedule_registration", 1)[0]
    transport = source.split("registration_transport_shutdown_cb", 1)[1].split(
        "static int ami_register_outbound", 1)[0]
    assert 'mdd_push_registration_request(client_state, "")' in timer
    assert 'mdd_registration_request_create(state->client_state, "")' in transport


def test_rearm_timer_firing_inside_fence_preserves_one_bounded_followup():
    source = load_patcher().patch(FIXTURE.read_text(encoding="utf-8"))
    handler = source.split("static int handle_client_registration(void *data)", 2)[2].split(
        "static void sip_outbound_registration_timer_cb", 1)[0]
    rearm = source.split("static int handle_mdd_registration_rearm", 1)[1].split(
        "static int mdd_queue_registration_request", 1)[0]

    # Rearm first creates exactly one real timer and advances the generation reported in ACK.
    assert rearm.count("schedule_registration_unfenced") == 1
    assert rearm.index("schedule_registration_unfenced") < rearm.index(
        "mdd_rearm_generation++")
    # If that timer fires before Control's recovered record/fence unlink is durable, the empty
    # ordinary request sends no REGISTER, records one deferred intent and replaces the consumed
    # timer with exactly one successor. Repeated collisions never grow the timer/task count.
    fenced = handler.split("if (ast_mdd_registration_fenced()", 1)[1].split(
        "return 0; /* zero pjsip_regc_register", 1)[0]
    assert "ast_strlen_zero(request->permit_nonce)" in fenced
    assert fenced.index("mdd_timer_deferred = 1") < fenced.index(
        "schedule_registration_unfenced")
    assert fenced.count("schedule_registration_unfenced") == 1
    after_fence = handler.split(
        "return 0; /* zero pjsip_regc_register and zero registration_client_send */", 1)[1]
    assert after_fence.index("mdd_timer_deferred = 0") < after_fence.index(
        "pjsip_regc_register")


def test_patcher_applies_to_exact_pinned_sysmocom_source_when_supplied():
    path = os.environ.get("MDD_PINNED_ASTERISK_REGISTRATION_SOURCE")
    if not path:
        pytest.skip("exact pinned sysmocom source is supplied by the Engine build/review gate")
    raw = Path(path).read_bytes()
    assert hashlib.sha256(raw).hexdigest() == \
        "d7f49063e49356abc694b343642757f6c497f7144ddcaeb00516ea45ed1e9b9b"
    source = load_patcher().patch(raw.decode())
    assert source.count('#include "asterisk/mdd_admission.h"') == 1
    assert source.index('#include "asterisk/mdd_admission.h"') < source.index(
        "ast_mdd_registration_fenced")
    assert source.count("MDD_REGISTRATION_DISPATCH_FENCE_V1") == 1
    ami = source.split("static int ami_register(", 1)[1].split("static int ami_authresponse", 1)[0]
    assert ami.index("MDDPermitNonce") < ami.index("queue_register(state)")
    assert "astman_send_ack" in ami and "MDDTimerId:" in ami and "SentRegister: false" in ami
    assert "ast_sip_push_task_wait_serializer" in source


def test_admission_header_exposes_registration_fence_and_exact_nonce_consumer():
    header = (ROOT / "engine/patches/asterisk/mdd_admission/mdd_admission.h").read_text()
    assert "ast_mdd_registration_fenced" in header
    assert "ast_mdd_registration_begin" in header and "ast_mdd_registration_end" in header
    assert "permit_nonce" in header


def test_registration_fence_lstat_errors_are_fail_closed_except_enoent():
    source = (ROOT / "engine/patches/asterisk/mdd_admission/res_mdd_admission.c").read_text()
    function = source.split("ast_mdd_registration_fenced", 1)[1].split(
        "ast_mdd_registration_begin", 1)[0]
    assert "errno = 0" in function
    assert "!lstat(path, &metadata) || errno != ENOENT" in function


def test_docker_patch_order_installs_optional_header_before_registration_patch():
    dockerfile = (ROOT / "engine/Dockerfile").read_text()
    installer = (ROOT / "engine/patches/asterisk/mdd_admission.py").read_text()
    assert "for p in /home/asterisk-build/patches/asterisk/*.py" in dockerfile
    names = sorted(path.name for path in (ROOT / "engine/patches/asterisk").glob("*.py"))
    assert names.index("mdd_admission.py") < names.index("mdd_registration_fence.py")
    assert 'include" / "asterisk" / "mdd_admission.h"' in installer


def test_exact_permit_is_consumed_once_and_receipt_is_durable_before_allow(tmp_path):
    write_permit(tmp_path)
    result = admission_gate.consume_registration_permit(
        tmp_path, "a" * 32, engine_run_id="run-current", now=1001.0)
    assert result["allowed"] is True and result["status"] == "dispatch_recorded"
    receipt_path = tmp_path / "usim-registration-dispatch.json"
    receipt = json.loads(receipt_path.read_text())
    assert receipt["permit_nonce"] == "a" * 32 and receipt["dispatch_count"] == 1
    assert receipt["result_class"] == "dispatch_recorded_send_unknown"
    assert "sent" not in receipt and not (tmp_path / "usim-registration-permit.json").exists()
    replay = admission_gate.consume_registration_permit(
        tmp_path, "a" * 32, engine_run_id="run-current", now=1002.0)
    assert replay["allowed"] is False and replay["status"] == "already_consumed"


@pytest.mark.parametrize("nonce,run,now", [
    ("c" * 32, "run-current", 1001.0), ("a" * 32, "run-old", 1001.0),
    ("a" * 32, "run-current", 1061.0), ("short", "run-current", 1001.0),
])
def test_wrong_stale_or_malformed_permit_is_never_consumed(tmp_path, nonce, run, now):
    original = write_permit(tmp_path).read_bytes()
    result = admission_gate.consume_registration_permit(
        tmp_path, nonce, engine_run_id=run, now=now)
    assert result["allowed"] is False
    assert (tmp_path / "usim-registration-permit.json").read_bytes() == original
    assert not (tmp_path / "usim-registration-dispatch.json").exists()


def test_two_concurrent_consumers_produce_one_allow_and_one_receipt(tmp_path):
    write_permit(tmp_path)
    barrier = threading.Barrier(2)
    def consume():
        barrier.wait()
        return admission_gate.consume_registration_permit(
            tmp_path, "a" * 32, engine_run_id="run-current", now=1001.0)
    with ThreadPoolExecutor(max_workers=2) as pool:
        results = [future.result() for future in (pool.submit(consume), pool.submit(consume))]
    assert sum(result["allowed"] is True for result in results) == 1
    receipt = json.loads((tmp_path / "usim-registration-dispatch.json").read_text())
    assert receipt["dispatch_count"] == 1


def test_gate_requires_asterisk_peer_to_hold_dispatch_lock(tmp_path):
    write_permit(tmp_path)
    denied = admission_gate.consume_registration_permit(
        tmp_path, "a" * 32, engine_run_id="run-current", now=1001.0,
        peer_holds_dispatch=True)
    assert denied["status"] == "dispatch_lock_not_held"
    lock_path = tmp_path / admission_gate.REGISTRATION_DISPATCH_LOCK
    with lock_path.open("a+") as lock:
        fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        allowed = admission_gate.consume_registration_permit(
            tmp_path, "a" * 32, engine_run_id="run-current", now=1001.0,
            peer_holds_dispatch=True)
    assert allowed["allowed"] is True


def test_receipt_debris_denies_even_if_permit_reappears(tmp_path):
    write_permit(tmp_path)
    (tmp_path / "usim-registration-dispatch.json").write_text(json.dumps({
        **permit(), "phase": "submitted_unknown", "dispatch_count": 1,
        "result_class": "dispatch_recorded_send_unknown"}), encoding="utf-8")
    result = admission_gate.consume_registration_permit(
        tmp_path, "a" * 32, engine_run_id="run-current", now=1001.0)
    assert result["allowed"] is False and result["status"] == "already_consumed"


def test_registration_begin_holds_dispatch_until_pcscf_and_handler_holds_pcscf_through_send():
    source = (ROOT / "engine/patches/asterisk/mdd_admission/res_mdd_admission.c").read_text()
    begin = source.split("ast_mdd_registration_begin", 1)[1].split(
        "ast_mdd_registration_end", 1)[0]
    assert begin.index(".usim-registration-dispatch.lock") < begin.index("ast_mdd_admission_check")
    assert begin.index("ast_mdd_admission_check") < begin.index(".pcscf-rebind.lock")
    assert begin.index(".pcscf-rebind.lock") < begin.index("LOCK_UN")
