"""Install the request-owned outbound REGISTER recovery fence in pinned sysmocom Asterisk."""
from pathlib import Path
import os
import sys

SOURCE = Path(os.environ.get("AST_SRC", "/home/asterisk-build/asterisk")) / \
    "res/res_pjsip_outbound_registration.c"
MARKER = "MDD_REGISTRATION_DISPATCH_FENCE_V1"


def once(source, old, new, label):
    if source.count(old) != 1:
        raise RuntimeError(f"{label}: expected one anchor, found {source.count(old)}")
    return source.replace(old, new, 1)


def patch(source):
    if MARKER in source:
        return source
    source = once(source,
        '#include "asterisk/res_pjsip.h"\n',
        '#include "asterisk/res_pjsip.h"\n#include "asterisk/mdd_admission.h"\n',
        "optional admission API include")
    source = once(source,
        "struct sip_outbound_registration_client_state {\n",
        "/* " + MARKER + " */\nstruct sip_outbound_registration_client_state {\n",
        "client state")
    close = source.index("};", source.index("struct sip_outbound_registration_client_state"))
    source = (source[:close] +
        "\tunsigned int deferred_registration_seconds;\n"
        "\tunsigned int mdd_rearm_generation;\n\tunsigned int mdd_timer_deferred:1;\n" + source[close:])
    request = '''\nstruct mdd_registration_request {\n\tstruct sip_outbound_registration_client_state *client_state;\n\tchar permit_nonce[33]; /* request-owned; timers always leave this empty */\n};\n\nstatic struct mdd_registration_request *mdd_registration_request_create(\n+\t\tstruct sip_outbound_registration_client_state *client_state, const char *permit_nonce)\n{\n\tstruct mdd_registration_request *request = ast_calloc(1, sizeof(*request));\n\tif (!request) {\n\t\tao2_cleanup(client_state);\n\t\treturn NULL;\n\t}\n\trequest->client_state = client_state; /* consumes the caller's existing ao2 reference */\n\tif (!ast_strlen_zero(permit_nonce)) {\n\t\tast_copy_string(request->permit_nonce, permit_nonce, sizeof(request->permit_nonce));\n\t}\n\treturn request;\n}\n\n'''
    request = request.replace("\n+", "\n")
    request = request.replace(
        "\tif (!ast_strlen_zero(permit_nonce)) {\n"
        "\t\tast_copy_string(request->permit_nonce, permit_nonce, sizeof(request->permit_nonce));\n\t}\n",
        "\tif (!ast_strlen_zero(permit_nonce)) {\n"
        "\t\tsize_t index;\n"
        "\t\tif (strlen(permit_nonce) != 32) {\n\t\t\tao2_cleanup(request->client_state);\n"
        "\t\t\tast_free(request);\n\t\t\treturn NULL;\n\t\t}\n"
        "\t\tfor (index = 0; index < 32; ++index) {\n"
        "\t\t\tif (!((permit_nonce[index] >= '0' && permit_nonce[index] <= '9')\n"
        "\t\t\t\t\t|| (permit_nonce[index] >= 'a' && permit_nonce[index] <= 'f'))) {\n"
        "\t\t\t\tao2_cleanup(request->client_state);\n\t\t\t\tast_free(request);\n"
        "\t\t\t\treturn NULL;\n\t\t\t}\n\t\t}\n"
        "\t\tast_copy_string(request->permit_nonce, permit_nonce, sizeof(request->permit_nonce));\n\t}\n")
    request += '''static int handle_client_registration(void *data);\nstatic void schedule_registration_unfenced(\n\t\tstruct sip_outbound_registration_client_state *client_state, unsigned int seconds);\n\nstatic int mdd_push_registration_request(\n\t\tstruct sip_outbound_registration_client_state *client_state, const char *permit_nonce)\n{\n\tstruct mdd_registration_request *request =\n\t\tmdd_registration_request_create(client_state, permit_nonce);\n\tif (!request) { return -1; }\n\tif (ast_sip_push_task(client_state->serializer, handle_client_registration, request)) {\n\t\tao2_cleanup(request->client_state);\n\t\tast_free(request);\n\t\treturn -1;\n\t}\n\treturn 0;\n}\n\n'''
    handler = source.index("static int handle_client_registration(void *data)")
    source = source[:handler] + request + source[handler:]
    source = once(source,
        "\tRAII_VAR(struct sip_outbound_registration_client_state *, client_state, data, ao2_cleanup);\n",
        "\tRAII_VAR(struct mdd_registration_request *, request, data, ast_free);\n"
        "\tRAII_VAR(struct sip_outbound_registration_client_state *, client_state, request->client_state, ao2_cleanup);\n",
        "request-owned handler")
    needle = "\tpjsip_tx_data *tdata;\n"
    needle_at = source.find(needle, handler)
    if needle_at < 0:
        raise RuntimeError("handler tdata declaration is missing")
    source = source[:needle_at] + (needle +
        "\tRAII_VAR(int, mdd_registration_handle, -1, ast_mdd_registration_end);\n"
        "\tunsigned int mdd_deferred_seconds;\n"
        "\n\tif (ast_mdd_registration_fenced()\n"
        "\t\t\t&& (mdd_registration_handle = ast_mdd_registration_begin(request->permit_nonce)) < 0) {\n"
        "\t\tif (ast_strlen_zero(request->permit_nonce)) {\n"
        "\t\t\tmdd_deferred_seconds = client_state->deferred_registration_seconds\n"
        "\t\t\t\t? client_state->deferred_registration_seconds : client_state->retry_interval;\n"
        "\t\t\tclient_state->deferred_registration_seconds =\n"
        "\t\t\t\tmdd_deferred_seconds ? mdd_deferred_seconds : 1;\n"
        "\t\t\tclient_state->mdd_timer_deferred = 1;\n"
        "\t\t\t/* Keep exactly one deferred successor; no send and no queue growth. */\n"
        "\t\t\tschedule_registration_unfenced(client_state,\n"
        "\t\t\t\tclient_state->deferred_registration_seconds);\n"
        "\t\t}\n"
        "\t\treturn 0; /* zero pjsip_regc_register and zero registration_client_send */\n"
        "\t}\n"
        "\tif (ast_strlen_zero(request->permit_nonce)) {\n"
        "\t\tclient_state->mdd_timer_deferred = 0;\n"
        "\t}\n") + \
        source[needle_at + len(needle):]
    source = once(source,
        "ast_sip_push_task(client_state->serializer, handle_client_registration, client_state)",
        "mdd_push_registration_request(client_state, \"\")",
        "timer request")
    schedule_at = source.index("static void schedule_registration(")
    source = source[:schedule_at] + source[schedule_at:].replace(
        "static void schedule_registration", "static void schedule_registration_unfenced", 1)
    schedule_at = source.index("static void schedule_registration_unfenced", schedule_at)
    brace_at = source.index("{", schedule_at)
    depth = 0
    for end in range(brace_at, len(source)):
        depth += source[end] == "{"
        depth -= source[end] == "}"
        if depth == 0:
            schedule_end = end + 1
            break
    else:
        raise RuntimeError("scheduler function is unterminated")
    helpers = '''\n\nstatic void schedule_registration(\n+\t\tstruct sip_outbound_registration_client_state *client_state, unsigned int seconds)\n{\n\tif (ast_mdd_registration_fenced()) {\n\t\tcancel_registration(client_state);\n\t\tclient_state->deferred_registration_seconds = seconds;\n\t\tclient_state->mdd_timer_deferred = 1;\n\t\treturn;\n\t}\n\tschedule_registration_unfenced(client_state, seconds);\n}\n\nstatic int handle_mdd_registration_rearm(void *data)\n{\n\tRAII_VAR(struct mdd_registration_request *, request, data, ast_free);\n\tRAII_VAR(struct sip_outbound_registration_client_state *, client_state, request->client_state, ao2_cleanup);\n\tunsigned int seconds = client_state->mdd_timer_deferred\n\t\t? client_state->deferred_registration_seconds : client_state->retry_interval;\n\tclient_state->mdd_timer_deferred = 0;\n\tschedule_registration_unfenced(client_state, seconds ? seconds : 1);\n\treturn 0; /* timer-only; zero handle_client_registration */\n}\n\nstatic int mdd_queue_registration_request(const char *registration_name,\n\t\tconst char *permit_nonce, int rearm_only)\n{\n\tRAII_VAR(struct ao2_container *, states, ao2_global_obj_ref(current_states), ao2_cleanup);\n\tRAII_VAR(struct sip_outbound_registration_state *, state, NULL, ao2_cleanup);\n\tstruct mdd_registration_request *request;\n\tif (!states || !(state = ao2_find(states, registration_name, OBJ_SEARCH_KEY))) {\n\t\treturn -1;\n\t}\n\tao2_ref(state->client_state, +1);\n\trequest = mdd_registration_request_create(state->client_state, permit_nonce);\n\tif (!request) {\n\t\treturn -1;\n\t}\n\tif (ast_sip_push_task(state->client_state->serializer, rearm_only\n\t\t\t? handle_mdd_registration_rearm : handle_client_registration, request)) {\n\t\tao2_cleanup(request->client_state);\n\t\tast_free(request);\n\t\treturn -1;\n\t}\n\treturn 0;\n}\n'''
    helpers = helpers.replace("\n+", "\n")
    helpers = helpers.replace(
        "\tclient_state->mdd_timer_deferred = 0;\n"
        "\tschedule_registration_unfenced(client_state, seconds ? seconds : 1);\n",
        "\tclient_state->mdd_timer_deferred = 0;\n"
        "\tschedule_registration_unfenced(client_state, seconds ? seconds : 1);\n"
        "\tclient_state->mdd_rearm_generation++;\n")
    helpers = helpers.replace(
        "\tclient_state->mdd_timer_deferred = 0;\n"
        "\tschedule_registration_unfenced(client_state, seconds ? seconds : 1);\n",
        "\tclient_state->mdd_timer_deferred = 0;\n"
        "\tclient_state->deferred_registration_seconds = seconds ? seconds : 1;\n"
        "\tschedule_registration_unfenced(client_state, client_state->deferred_registration_seconds);\n")
    helpers += '''\nstatic int mdd_queue_registration_rearm(const char *registration_name)\n{\n\tRAII_VAR(struct ao2_container *, states, ao2_global_obj_ref(current_states), ao2_cleanup);\n\tRAII_VAR(struct sip_outbound_registration_state *, state, NULL, ao2_cleanup);\n\tstruct mdd_registration_request *request;\n\tint result;\n\tif (!states || !(state = ao2_find(states, registration_name, OBJ_SEARCH_KEY))) {\n\t\treturn -1;\n\t}\n\tao2_ref(state->client_state, +1);\n\trequest = mdd_registration_request_create(state->client_state, "");\n\tif (!request) { return -1; }\n\tresult = ast_sip_push_task_wait_serializer(state->client_state->serializer,\n\t\thandle_mdd_registration_rearm, request);\n\tif (result) {\n\t\tao2_cleanup(request->client_state);\n\t\tast_free(request);\n\t}\n\treturn result;\n}\n'''
    source = source[:schedule_end] + helpers + source[schedule_end:]
    source = once(source,
        "handle_client_registration(state->client_state);",
        "{ struct mdd_registration_request *request = "
        "mdd_registration_request_create(state->client_state, \"\"); "
        "if (request) { handle_client_registration(request); } }",
        "transport request")
    ami = source.index("static int ami_register(")
    brace = source.index("{", ami) + 1
    source = source[:brace] + '''\n\tconst char *mdd_permit_nonce = astman_get_header(m, "MDDPermitNonce");\n\tconst char *mdd_rearm_only = astman_get_header(m, "MDDRearmOnly");\n\tconst char *mdd_action_id = astman_get_header(m, "ActionID");\n\t/* The real action owner resolves the registration state on its serializer.\n\t * MDDPermitNonce is copied into that one request; MDDRearmOnly arms only the\n\t * deferred/fallback timer and never invokes handle_client_registration. */\n''' + source[brace:]
    depth = 1
    for ami_end in range(brace, len(source)):
        depth += source[ami_end] == "{"
        depth -= source[ami_end] == "}"
        if depth == 0:
            break
    else:
        raise RuntimeError("AMI registration action is unterminated")
    registration = ("registration" if "const char *registration =" in source[brace:ami_end]
                    else "registration_name")
    branch = (f'''\n\tif (!ast_strlen_zero(mdd_rearm_only)) {{\n'''
              f'''\t\tif (strcmp(mdd_rearm_only, "true") || !ast_strlen_zero(mdd_permit_nonce)) {{ astman_send_error(s, m, "Invalid exclusive MDD rearm request"); ao2_ref(state, -1); return 0; }}\n'''
              f'''\t\tint queued = mdd_queue_registration_rearm({registration});\n'''
              f'''\t\tif (queued) {{ astman_send_error(s, m, "MDD timer rearm failed"); }} else {{\n'''
              f'''\t\t\tastman_append(s, "Response: Success\\r\\n");\n\t\t\tif (!ast_strlen_zero(mdd_action_id)) {{ astman_append(s, "ActionID: %s\\r\\n", mdd_action_id); }}\n\t\t\tastman_append(s, "Message: MDD timer rearmed\\r\\nMDDTimerId: %s-%u-%u\\r\\nSentRegister: false\\r\\n\\r\\n", {registration}, state->client_state->mdd_rearm_generation, state->client_state->deferred_registration_seconds);\n\t\t}}\n'''
              f'''\t\tao2_ref(state, -1);\n\t\treturn 0;\n\t}}\n'''
              f'''\tif (!ast_strlen_zero(mdd_permit_nonce)) {{\n'''
              f'''\t\tint queued = mdd_queue_registration_request({registration}, mdd_permit_nonce, 0);\n'''
              f'''\t\tif (queued) {{ astman_send_error(s, m, "MDD registration permit rejected"); }} else {{ astman_send_ack(s, m, "MDD registration permit queued"); }}\n'''
              f'''\t\tao2_ref(state, -1);\n\t\treturn 0;\n\t}}\n''')
    candidates = [index for needle in ("queue_register(", "queue_registration(",
                                        "sip_outbound_registration_register(")
                  if (index := source.find(needle, brace, ami_end)) >= 0]
    if not candidates:
        raise RuntimeError("AMI registration action has no registration side-effect anchor")
    side_effect = min(candidates)
    insert_at = source.rfind("\n", brace, side_effect) + 1
    source = source[:insert_at] + branch + source[insert_at:]
    return source


def main():
    original = SOURCE.read_text(encoding="utf-8")
    updated = patch(original)
    if updated != original:
        SOURCE.write_text(updated, encoding="utf-8")
    print("installed request-owned outbound REGISTER dispatch fence")


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print(f"outbound registration fence failed: {exc}", file=sys.stderr)
        raise SystemExit(1) from exc
