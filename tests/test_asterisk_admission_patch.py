import os
import subprocess
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
PATCHER = ROOT / "engine" / "patches" / "asterisk" / "mdd_admission.py"


FIXTURE = r'''#include "asterisk.h"
#include "asterisk/message.h"
static void hex_body(char *bufout, unsigned char *buf, int len)
{
	unsigned char *ptrin;
	char *ptrout = bufout;

	for (ptrin = buf; ptrin < buf + 256; ptrin++)
	{
		*ptrout++ = hexdigit((*ptrin >> 4) & 0xf);
		*ptrout++ = hexdigit((*ptrin) & 0xf);
	}
	*ptrout++ = 0;
}
static void parse_rpdata(pjsip_rx_data *rdata, struct ast_msg *msg, int *ack_ref)
{
	unsigned char buf[300];
	int len = rdata->msg_info.msg->body->print_body(
		rdata->msg_info.msg->body, (char *)buf, sizeof(buf));

	if (len < 3) {
		ast_log(LOG_DEBUG, "MESSAGE RP-DATA is too short or error: %d.\n", len);
		return;
	}
			
	char buf2[MAX_BODY_SIZE * 2 + 1];
	hex_body(buf2, buf, len);
	ast_log(LOG_DEBUG, "SMS RP-DATA '%s'.\n", buf2);
	switch (buf[0]) {
	{
	case 0x01:
		*ack_ref = buf[1] & 0xff;
	}
}
static int msg_send(void *data)
{
	struct msg_data *mdata = data;
	pjsip_tx_data *tdata;
	RAII_VAR(char *, uri, NULL, ast_free);
	RAII_VAR(struct ast_sip_endpoint *, endpoint, NULL, ao2_cleanup);

	endpoint = ast_sip_get_endpoint(mdata->destination, 1, &uri);
	if (!endpoint) {
		return -1;
	}

	ast_debug(3, "Request URI: %s\n", uri);

	if (ast_sip_create_request("MESSAGE", NULL, endpoint, uri, NULL, &tdata)) {
		return -1;
	}
	if (ast_sip_send_request(tdata, NULL, endpoint, NULL, NULL)) {
		return -1;
	}
	return 0;
}
static int sip_msg_send(const struct ast_msg *msg, const char *destination, const char *from)
{
	struct msg_data *mdata;
	int res;

	if (ast_strlen_zero(destination)) {
		ast_log(LOG_ERROR, "SIP MESSAGE - a 'To' URI  must be specified\n");
		return -1;
	}

	mdata = msg_data_create(msg, destination, from);
}
static pj_bool_t module_on_rx_request(pjsip_rx_data *rdata)
{
	struct ast_msg *msg;
	pj_bool_t is_sms;
	int ack_ref = -1;
	/* if not a MESSAGE, don't handle */
	if (pjsip_method_cmp(&rdata->msg_info.msg->line.req.method, &pjsip_message_method)) {
		return PJ_FALSE;
	}

	code = check_content_type(rdata, &is_sms);
	if (code != PJSIP_SC_OK) {
		send_response(rdata, code, NULL, NULL);
		return PJ_TRUE;
	}

	msg = ast_msg_alloc();
	if (!send_response(rdata, PJSIP_SC_ACCEPTED, NULL, NULL)) {
		ast_msg_queue(msg);
	}
	send_rpack(rdata, ack_ref);
}
static int incoming_in_dialog_request(struct ast_sip_session *session, struct pjsip_rx_data *rdata)
{
	int pos = 0;
	int body_pos;

	if (!session->channel) {
		send_response(rdata, PJSIP_SC_NOT_FOUND, dlg, tsx);
		return 0;
	}

	code = check_content_type_in_dialog(rdata);
	if (code != PJSIP_SC_OK) {
		send_response(rdata, code, dlg, tsx);
		return 0;
	}

	caller = ast_channel_caller(session->channel);
	ast_msg_data_queue_frame(session->channel, msg);
	send_response(rdata, PJSIP_SC_ACCEPTED, dlg, tsx);
}
AST_MODULE_INFO(ASTERISK_GPL_KEY, AST_MODFLAG_LOAD_ORDER, "PJSIP Messaging Support",
	.requires = "res_pjsip,res_pjsip_session",
);
'''


def tree(tmp_path, source=FIXTURE):
    root = tmp_path / "asterisk"
    (root / "res").mkdir(parents=True)
    (root / "include" / "asterisk").mkdir(parents=True)
    (root / "res" / "res_pjsip_messaging.c").write_text(source)
    return root


def run_patcher(root):
    env = dict(os.environ, ASTERISK_SOURCE_ROOT=str(root))
    return subprocess.run([sys.executable, str(PATCHER)], env=env,
                          text=True, capture_output=True)


def test_patcher_installs_strict_classifier_and_three_fail_closed_hooks(tmp_path):
    root = tree(tmp_path)
    result = run_patcher(root)
    assert result.returncode == 0, result.stderr
    source = (root / "res" / "res_pjsip_messaging.c").read_text()
    assert source.count("MDD_ADMISSION_PATCH_V1") == 1
    assert source.count('ast_mdd_admission_check("sms_in")') == 2
    assert source.count('ast_mdd_admission_check("sms_out")') == 1
    assert source.count("mdd_classify_message(rdata, is_sms)") == 2
    outbound = source.split("static int msg_send", 1)[1].split(
        "static int sip_msg_send", 1)[0]
    assert outbound.index("ast_sip_get_endpoint(mdata->destination, 1, &uri)") < outbound.index(
        'ast_sorcery_object_get_id(endpoint), "volte_ims"') < outbound.index(
        'ast_mdd_admission_check("sms_out")') < outbound.index("ast_sip_create_request")
    assert outbound.index('ast_mdd_admission_check("sms_out")') < outbound.index(
        "if (ast_sip_send_request")
    assert "const char *carrier" not in source
    assert 'strcmp(carrier, "volte_ims")' not in source
    assert 'ast_begins_with(carrier, "volte_ims/")' not in source
    out = source.split("static pj_bool_t module_on_rx_request", 1)[1].split(
        "static int incoming_in_dialog_request", 1)[0]
    assert out.index("check_content_type(rdata, &is_sms)") < out.index(
        "mdd_classify_message(rdata, is_sms)") < out.index(
        'ast_mdd_admission_check("sms_in")') < out.index("ast_msg_alloc()")
    assert out.index("MDD_MESSAGE_COMPLETION") < out.index(
        'ast_mdd_admission_check("sms_in")')
    assert "parse_rpdata(rdata, NULL, NULL);" in out
    dialog = source.split("static int incoming_in_dialog_request", 1)[1]
    assert dialog.index("check_content_type_in_dialog") < dialog.index(
        "mdd_classify_message(rdata, is_sms)") < dialog.index(
        'ast_mdd_admission_check("sms_in")') < dialog.index("ast_msg_data_queue_frame")
    assert "parse_rpdata(rdata, NULL, NULL);" in dialog
    assert "ptrin < buf + len" in source
    assert "ptrin < buf + 256" not in source
    assert '.requires = "res_mdd_admission,res_pjsip,res_pjsip_session"' in source
    assert (root / "res" / "res_mdd_admission.c").is_file()
    assert (root / "include" / "asterisk" / "mdd_admission.h").is_file()

    second = run_patcher(root)
    assert second.returncode == 0, second.stderr
    assert "already patched" in second.stdout


def test_rpdu_classifier_is_exact_bounded_and_completion_has_no_queue_path(tmp_path):
    root = tree(tmp_path)
    result = run_patcher(root)
    assert result.returncode == 0, result.stderr
    source = (root / "res" / "res_pjsip_messaging.c").read_text()
    classifier = source.split("static enum mdd_message_class mdd_classify_message", 1)[1].split(
        "static int sip_msg_send", 1)[0]
    for required in (
        "unsigned char body[248]", "case 0x01", "case 0x03", "case 0x05",
        "value_len < 2 || value_len > 11", "body[pos++] != 0",
        "value_len < 1 || value_len > 232",
        "pos + value_len != (unsigned int) len", "body[pos++] != 0x41",
    ):
        assert required in classifier
    out = source.split("static pj_bool_t module_on_rx_request", 1)[1].split(
        "static int incoming_in_dialog_request", 1)[0]
    completion = out.split("message_class == MDD_MESSAGE_COMPLETION", 1)[1].split(
        "The sole MT linearization point", 1)[0]
    assert "parse_rpdata(rdata, NULL, NULL);" in completion
    assert "PJSIP_SC_OK" in completion
    assert "ast_msg_alloc" not in completion
    assert "ast_msg_queue" not in completion


def test_patcher_fails_on_upstream_anchor_drift(tmp_path):
    root = tree(tmp_path, FIXTURE.replace('#include "asterisk/message.h"\n', ""))
    result = run_patcher(root)
    assert result.returncode != 0
    assert "expected one anchor" in result.stderr


def test_resource_protocol_is_bounded_exact_and_fail_closed():
    source = (ROOT / "engine" / "patches" / "asterisk" / "mdd_admission" /
              "res_mdd_admission.c").read_text()
    assert "MDD_ADMISSION_TIMEOUT_MS 150" in source
    assert "MDD_ADMISSION_FRAME_MAX 512" in source
    assert 'strcmp(parts[2], "ALLOW")' in source
    assert 'ast_copy_string(buffer, ast_mdd_admission_check(data) ? "ALLOW" : "DENY"' in source
    assert "AST_MODFLAG_GLOBAL_SYMBOLS | AST_MODFLAG_LOAD_ORDER" in source
