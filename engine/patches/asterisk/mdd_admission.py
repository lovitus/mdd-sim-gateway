#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-only
"""Install the MDD admission resource and hook pinned PJSIP MESSAGE paths."""

from pathlib import Path
import os
import shutil
import sys


ROOT = Path(os.environ.get("ASTERISK_SOURCE_ROOT", "/home/asterisk-build/asterisk"))
SOURCE = ROOT / "res" / "res_pjsip_messaging.c"
ASSETS = Path(__file__).with_name("mdd_admission")
MARKER = "MDD_ADMISSION_PATCH_V1"


def replace_once(source: str, old: str, new: str, name: str) -> str:
    count = source.count(old)
    if count != 1:
        raise RuntimeError(f"{name}: expected one anchor, found {count}")
    return source.replace(old, new, 1)


def main() -> int:
    target_header = ROOT / "include" / "asterisk" / "mdd_admission.h"
    target_module = ROOT / "res" / "res_mdd_admission.c"
    shutil.copyfile(ASSETS / "mdd_admission.h", target_header)
    shutil.copyfile(ASSETS / "res_mdd_admission.c", target_module)

    source = SOURCE.read_text(encoding="utf-8")
    if MARKER in source:
        print("MDD admission messaging hooks already patched")
        return 0

    source = replace_once(
        source,
        '#include "asterisk/message.h"\n',
        '#include "asterisk/message.h"\n#include "asterisk/mdd_admission.h" /* MDD_ADMISSION_PATCH_V1 */\n',
        "admission include",
    )
    source = replace_once(
        source,
        '''static int sip_msg_send(const struct ast_msg *msg, const char *destination, const char *from)\n{''',
        '''enum mdd_message_class {\n\tMDD_MESSAGE_NEW,\n\tMDD_MESSAGE_COMPLETION,\n\tMDD_MESSAGE_INVALID,\n};\n\n/* Classify one binary 3GPP RPDU before admission or any SIP success response.\n * TS 24.011 bounds the CP user data carrying an RPDU to 248 octets.  Exact\n * length consumption is intentional: a truncated or concatenated PDU must not\n * impersonate an RP-ACK/RP-ERROR and bypass the new-work gate. */\nstatic enum mdd_message_class mdd_classify_message(pjsip_rx_data *rdata, pj_bool_t is_sms)\n{\n\tunsigned char body[248];\n\tunsigned int pos;\n\tunsigned int value_len;\n\tint len;\n\n\tif (!is_sms) {\n\t\treturn MDD_MESSAGE_NEW;\n\t}\n\tif (!rdata->msg_info.msg->body || !rdata->msg_info.msg->body->len\n\t\t\t|| rdata->msg_info.msg->body->len > sizeof(body)) {\n\t\treturn MDD_MESSAGE_INVALID;\n\t}\n\tlen = rdata->msg_info.msg->body->print_body(\n\t\trdata->msg_info.msg->body, (char *) body, sizeof(body));\n\tif (len <= 0 || len > (int) sizeof(body)) {\n\t\treturn MDD_MESSAGE_INVALID;\n\t}\n\n\tswitch (body[0]) {\n\tcase 0x01: /* RP-DATA, network to mobile station */\n\t\tif (len < 8) {\n\t\t\treturn MDD_MESSAGE_INVALID;\n\t\t}\n\t\tpos = 2; /* message type + message reference */\n\t\tvalue_len = body[pos++];\n\t\tif (value_len < 2 || value_len > 11 || pos + value_len > (unsigned int) len) {\n\t\t\treturn MDD_MESSAGE_INVALID;\n\t\t}\n\t\tpos += value_len;\n\t\tif (pos >= (unsigned int) len || body[pos++] != 0 || pos >= (unsigned int) len) {\n\t\t\treturn MDD_MESSAGE_INVALID;\n\t\t}\n\t\tvalue_len = body[pos++];\n\t\tif (value_len < 1 || value_len > 232 || pos + value_len != (unsigned int) len) {\n\t\t\treturn MDD_MESSAGE_INVALID;\n\t\t}\n\t\treturn MDD_MESSAGE_NEW;\n\tcase 0x03: /* RP-ACK, network to mobile station */\n\t\tpos = 2;\n\t\tbreak;\n\tcase 0x05: /* RP-ERROR, network to mobile station */\n\t\tif (len < 4) {\n\t\t\treturn MDD_MESSAGE_INVALID;\n\t\t}\n\t\tpos = 2;\n\t\tvalue_len = body[pos++];\n\t\tif (value_len < 1 || value_len > 2 || pos + value_len > (unsigned int) len) {\n\t\t\treturn MDD_MESSAGE_INVALID;\n\t\t}\n\t\tpos += value_len;\n\t\tbreak;\n\tdefault:\n\t\treturn MDD_MESSAGE_INVALID;\n\t}\n\n\tif (pos == (unsigned int) len) {\n\t\treturn MDD_MESSAGE_COMPLETION;\n\t}\n\tif (pos + 2 > (unsigned int) len || body[pos++] != 0x41) {\n\t\treturn MDD_MESSAGE_INVALID;\n\t}\n\tvalue_len = body[pos++];\n\tif (value_len < 1 || value_len > 232 || pos + value_len != (unsigned int) len) {\n\t\treturn MDD_MESSAGE_INVALID;\n\t}\n\treturn MDD_MESSAGE_COMPLETION;\n}\n\nstatic int sip_msg_send(const struct ast_msg *msg, const char *destination, const char *from)\n{''',
        "RPDU classifier",
    )
    source = replace_once(
        source,
        '''\tast_debug(3, "Request URI: %s\\n", uri);''',
        '''\t/* AMI MessageSend bypasses msg-from-local. Gate only after PJSIP has\n\t * resolved the actual endpoint, because endpoint@domain syntax discards the\n\t * domain before submit. RP-ACK/RP-ERROR use ast_sip_send_request directly and\n\t * remain admitted transaction completions. */\n\tif (!strcmp(ast_sorcery_object_get_id(endpoint), "volte_ims")\n\t\t\t&& !ast_mdd_admission_check("sms_out")) {\n\t\tast_log(LOG_NOTICE, "MDD admission denied outbound IMS MESSAGE\\n");\n\t\treturn -1;\n\t}\n\n\tast_debug(3, "Request URI: %s\\n", uri);''',
        "AMI MessageSend endpoint submit",
    )
    source = replace_once(
        source,
        '''\tpj_bool_t is_sms;\n\tint ack_ref = -1;''',
        '''\tpj_bool_t is_sms;\n\tenum mdd_message_class message_class;\n\tint ack_ref = -1;''',
        "out-of-dialog class declaration",
    )
    source = replace_once(
        source,
        '''\tcode = check_content_type(rdata, &is_sms);\n\tif (code != PJSIP_SC_OK) {\n\t\tsend_response(rdata, code, NULL, NULL);\n\t\treturn PJ_TRUE;\n\t}\n\n\tmsg = ast_msg_alloc();''',
        '''\tcode = check_content_type(rdata, &is_sms);\n\tif (code != PJSIP_SC_OK) {\n\t\tsend_response(rdata, code, NULL, NULL);\n\t\treturn PJ_TRUE;\n\t}\n\n\tmessage_class = mdd_classify_message(rdata, is_sms);\n\tif (message_class == MDD_MESSAGE_INVALID) {\n\t\tsend_response(rdata, PJSIP_SC_BAD_REQUEST, NULL, NULL);\n\t\treturn PJ_TRUE;\n\t}\n\tif (message_class == MDD_MESSAGE_COMPLETION) {\n\t\t/* Retain the existing RPDU hex evidence exactly once, but do not create or\n\t\t * queue a user message for an RP transaction completion. */\n\t\tparse_rpdata(rdata, NULL, NULL);\n\t\tsend_response(rdata, PJSIP_SC_OK, NULL, NULL);\n\t\treturn PJ_TRUE;\n\t}\n\t/* The sole MT linearization point: before 2xx, queueing and RP-ACK. */\n\tif (!ast_mdd_admission_check("sms_in")) {\n\t\tsend_response(rdata, PJSIP_SC_SERVICE_UNAVAILABLE, NULL, NULL);\n\t\treturn PJ_TRUE;\n\t}\n\n\tmsg = ast_msg_alloc();''',
        "out-of-dialog classified admission",
    )
    source = replace_once(
        source,
        '''\tint pos = 0;\n\tint body_pos;\n\n\tif (!session->channel) {\n\t\tsend_response(rdata, PJSIP_SC_NOT_FOUND, dlg, tsx);\n\t\treturn 0;\n\t}\n\n\tcode = check_content_type_in_dialog(rdata);\n\tif (code != PJSIP_SC_OK) {\n\t\tsend_response(rdata, code, dlg, tsx);\n\t\treturn 0;\n\t}\n\n\tcaller = ast_channel_caller(session->channel);''',
        '''\tint pos = 0;\n\tint body_pos;\n\tpj_bool_t is_sms = PJ_FALSE;\n\tenum mdd_message_class message_class;\n\n\tcode = check_content_type_in_dialog(rdata);\n\tif (code != PJSIP_SC_OK) {\n\t\tsend_response(rdata, code, dlg, tsx);\n\t\treturn 0;\n\t}\n\t/* check_content_type also tells the shared classifier whether this is exactly\n\t * application/vnd.3gpp.sms; its narrower accept result is intentionally ignored. */\n\tcheck_content_type(rdata, &is_sms);\n\tmessage_class = mdd_classify_message(rdata, is_sms);\n\tif (message_class == MDD_MESSAGE_INVALID) {\n\t\tsend_response(rdata, PJSIP_SC_BAD_REQUEST, dlg, tsx);\n\t\treturn 0;\n\t}\n\tif (message_class == MDD_MESSAGE_COMPLETION) {\n\t\tparse_rpdata(rdata, NULL, NULL);\n\t\tsend_response(rdata, PJSIP_SC_OK, dlg, tsx);\n\t\treturn 0;\n\t}\n\tif (!ast_mdd_admission_check("sms_in")) {\n\t\tsend_response(rdata, PJSIP_SC_SERVICE_UNAVAILABLE, dlg, tsx);\n\t\treturn 0;\n\t}\n\n\tif (!session->channel) {\n\t\tsend_response(rdata, PJSIP_SC_NOT_FOUND, dlg, tsx);\n\t\treturn 0;\n\t}\n\n\tcaller = ast_channel_caller(session->channel);''',
        "in-dialog classified admission",
    )
    source = replace_once(
        source,
        '''\tfor (ptrin = buf; ptrin < buf + 256; ptrin++)''',
        '''\tfor (ptrin = buf; ptrin < buf + len; ptrin++)''',
        "RPDU actual-length hex evidence",
    )
    source = replace_once(
        source,
        '''\tif (len < 3) {\n\t\tast_log(LOG_DEBUG, "MESSAGE RP-DATA is too short or error: %d.\\n", len);\n\t\treturn;\n\t}\n\t\t\t\n\tchar buf2[MAX_BODY_SIZE * 2 + 1];\n\thex_body(buf2, buf, len);\n\tast_log(LOG_DEBUG, "SMS RP-DATA '%s'.\\n", buf2);''',
        '''\tif (len <= 0 || len > 248) {\n\t\tast_log(LOG_DEBUG, "MESSAGE RP-DATA has invalid length: %d.\\n", len);\n\t\treturn;\n\t}\n\n\tchar buf2[MAX_BODY_SIZE * 2 + 1];\n\thex_body(buf2, buf, len);\n\tast_log(LOG_DEBUG, "SMS RP-DATA '%s'.\\n", buf2);\n\tif (len < 2) {\n\t\treturn;\n\t}''',
        "RPDU completion evidence",
    )
    source = replace_once(
        source,
        '''\t\t*ack_ref = buf[1] & 0xff;''',
        '''\t\tif (!msg || !ack_ref) {\n\t\t\treturn;\n\t\t}\n\t\t*ack_ref = buf[1] & 0xff;''',
        "RPDU null completion sink",
    )
    source = replace_once(
        source,
        '.requires = "res_pjsip,res_pjsip_session",\n',
        '.requires = "res_mdd_admission,res_pjsip,res_pjsip_session",\n',
        "module dependency",
    )
    SOURCE.write_text(source, encoding="utf-8")
    print("installed MDD admission module and PJSIP MESSAGE hooks")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print(f"MDD admission patch failed: {exc}", file=sys.stderr)
        raise SystemExit(1)
