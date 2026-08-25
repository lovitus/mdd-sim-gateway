import os
from pathlib import Path
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[1]
PATCHER = ROOT / "engine/patches/asterisk/mdd_answer_bridged.py"
SOURCE = (ROOT / "engine/patches/asterisk/mdd_answer_bridged" /
          "app_mdd_answer_bridged.c")


def test_patcher_installs_the_isolated_module_idempotently(tmp_path):
    apps = tmp_path / "apps"
    apps.mkdir()
    (apps / "Makefile").write_text("# fixture\n", encoding="utf-8")
    env = {**os.environ, "ASTERISK_SOURCE_ROOT": str(tmp_path)}
    for _ in range(2):
        subprocess.run([sys.executable, str(PATCHER)], env=env, check=True,
                       capture_output=True, text=True)
    assert (apps / "app_mdd_answer_bridged.c").read_bytes() == SOURCE.read_bytes()


def test_callback_lifetime_lock_order_and_unique_answer_are_frozen():
    source = SOURCE.read_text(encoding="utf-8")
    assert "ast_bridge_channel_queue_callback" in source
    assert "ast_module_ref(AST_MODULE_SELF)" in source
    assert source.count("ast_module_unref(AST_MODULE_SELF)") >= 2
    assert "static int unload_module(void)" in source
    unload = source.split("static int unload_module(void)", 1)[1].split(
        "static int load_module", 1)[0]
    assert "return -1;" in unload
    assert "ast_manager_unregister" not in unload

    callback = source.split("static void answer_callback", 1)[1].split(
        "static int manager_answer_bridged", 1)[0]
    admission = callback.index('ast_mdd_admission_check("call_in")')
    bridge_lock = callback.index("ast_bridge_channel_lock_bridge")
    peer = callback.index("ast_bridge_channel_peer")
    channels = callback.index("ast_channel_lock_both")
    consume = callback.index('pbx_builtin_setvar_helper(ims, "MDD_INBOUND_ARMED", "0")')
    bridge_unlock = callback.index("ast_bridge_unlock")
    answer = callback.index("ast_raw_answer")
    assert admission < bridge_lock < peer < channels < consume < bridge_unlock < answer
    assert "bridge->num_channels == 2" in callback
    assert "bridge->dissolved" in callback
    assert "ast_channel_state(winner) == AST_STATE_UP" in callback
    assert "answer_result == 0 && ast_channel_state(ims) == AST_STATE_UP" in callback
    assert callback.index('"MDD_INBOUND_ANSWER_RESULT", "pending"') < answer
    assert callback.index('"MDD_INBOUND_ANSWER_RESULT", "answered"') > answer
    assert '"MDD_INBOUND_ANSWER_RESULT", "denied"' in callback
    assert '"MDD_INBOUND_ANSWER_RESULT", "failed"' in callback
    for variable in ("MDD_INBOUND_ATTACH", "MDD_INBOUND_SOURCE_ID",
                     "MDD_INBOUND_WINNER_CHANNEL"):
        assert variable in callback


def test_manager_action_is_strict_and_releases_channel_before_queue():
    source = SOURCE.read_text(encoding="utf-8")
    action = source.split("static int manager_answer_bridged", 1)[1].split(
        "static int unload_module", 1)[0]
    assert "validate_headers(message)" in action
    for field in ("Channel", "IMSUniqueid", "WinnerChannel", "WinnerUniqueid",
                  "BridgeUniqueid", "OperationID", "MediaEpoch"):
        assert f'astman_get_header(message, "{field}")' in action
    assert action.index("ast_channel_unlock(ims)") < action.index(
        "ast_bridge_channel_queue_callback")
    assert action.index('ast_mdd_admission_check("call_in")') < action.index(
        "ast_bridge_channel_queue_callback")
    assert 'EVENT_FLAG_CALL, manager_answer_bridged' in source
    assert "EVENT_FLAG_SYSTEM" not in source
    assert "EVENT_FLAG_COMMAND" not in source
    assert '.requires = "res_mdd_admission"' in source
    assert "lower_hex_operation(operation_id)" in action
    assert "opaque_epoch(media_epoch)" in action
    assert "media_websocket_channel(winner_channel)" in action


def test_docker_build_and_capability_label_require_the_module():
    dockerfile = (ROOT / "engine/Dockerfile").read_text(encoding="utf-8")
    assert "--enable app_mdd_answer_bridged" in dockerfile
    assert 'io.mdd-sim-gateway.browser-inbound="mdd-browser-inbound-v1"' in dockerfile


def test_isolated_attach_uses_fixed_redirect_target_and_never_ami_bridge():
    dialplan = (ROOT / "tests/fixtures/browser_inbound_extensions.conf").read_text()
    attach = dialplan.split("[e3-attach]", 1)[1]
    assert "Bridge(${MDD_INBOUND_WINNER_CHANNEL},n)" in attach
    harness = (ROOT / "tests/fixtures/browser_inbound_answer_asterisk_e2e.py").read_text()
    assert '"Action": "Redirect"' in harness
    assert '"Action": "Bridge"' not in harness


def test_product_inbound_wait_attach_timeout_and_single_result_handler_are_fixed():
    dialplan = (ROOT / "engine/templates/extensions.conf.j2").read_text()
    incoming = dialplan.split("[volte_ims]", 1)[1].split("[mdd-inbound-result]", 1)[0]
    result = dialplan.split("[mdd-inbound-result]", 1)[1].split(
        "[volte_ims_msg]", 1)[0]
    attach = dialplan.split("[browser-media-inbound-attach]", 1)[1].split(
        "[from-local]", 1)[0]
    assert incoming.index("hangup_handler_push") < incoming.index("notify.py call_in")
    assert "Set(DIALSTATUS=NOANSWER)" in incoming
    assert "TIMEOUT(absolute)=65" in incoming and "Wait(60)" in incoming
    assert "Dial(PJSIP/webrtc" not in incoming
    assert "notify.py call_result" not in incoming
    assert "notify.py call_result" in result
    assert "MDD_ADMISSION(media_check)" in attach
    assert "MDD_INBOUND_SOURCE_ID" in attach and "CHANNEL(uniqueid)" in attach
    assert attach.index("MDD_ADMISSION(media_check)") < attach.index(
        "TIMEOUT(absolute)=10") < attach.index(
            "Bridge(${MDD_INBOUND_WINNER_CHANNEL},n)")
    assert "^WebSocket/mdd_control_media/0x[0-9A-Fa-f]+$" in attach
