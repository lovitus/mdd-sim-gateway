"""Answer a 401 re-challenge that arrives without a Security-Server header.

T-Mobile US IMS re-challenges a mid-session re-REGISTER with a 401 that carries no
Security-Server header: the network keeps the ESTABLISHED sec-agree IPsec SAs and expects the
authenticated REGISTER on them, with no renegotiation (TS 33.203 requires a Security-Server
offer only when SAs are being (re)established).  The sysmocom VoLTE code treats every 401 as
an SA negotiation, so the missing header failed the exchange and Asterisk logged "'401' fatal
response received ... retrying in '30' seconds" — a registration blip on every such re-auth
(line 1, 2026-08-09 02:30 and 10:10).

Insert a fallback in handle_volte_unauthorized(): when the header is genuinely ABSENT and the
volte transport still holds negotiated SAs (volte.registered), keep the SAs and go straight to
VOLTE_STATE_RESPONSE so ast_sip_create_request_with_auth() answers the challenge over the
existing transport — old_request already carries the current Security-Verify and
P-Access-Network-Info headers.  A present-but-unparsable header stays fatal, and the initial
registration (no SAs yet) is unchanged.
"""

import os
import sys
from pathlib import Path


SOURCE = Path(os.environ.get("AST_SRC", "/home/asterisk-build/asterisk")) \
    / "res/res_pjsip_outbound_registration.c"

MARKER = "PATCH reauth_missing_security_server"

ANCHOR = (
    '\t/* Get and select best candidate from "Security-Server" header. */\n'
    "\tif (volte_get_security_server(transport_state, response->rdata, &sec)) {\n"
)

FALLBACK = (
    "\t/* " + MARKER + ": a 401 re-challenge without Security-Server is a re-authentication\n"
    "\t * over the ESTABLISHED SAs (T-Mobile US does this on re-REGISTER), not a failed SA\n"
    "\t * negotiation.  Keep the SAs and answer the challenge over them; old_request already\n"
    "\t * carries the current Security-Verify / P-Access-Network-Info headers.  An absent\n"
    "\t * header with no established SAs (initial registration) still falls through to the\n"
    "\t * fatal path below, as does a present-but-unparsable header. */\n"
    "\t{\n"
    '\t\tstatic const pj_str_t str_security_server = { "Security-Server", 15 };\n'
    "\n"
    "\t\tif (transport_state->volte.registered\n"
    "\t\t    && !pjsip_msg_find_hdr_by_name(response->rdata->msg_info.msg, &str_security_server, NULL)) {\n"
    '\t\t\tast_log(LOG_NOTICE, "401 re-challenge without \'Security-Server\'; keeping the "\n'
    '\t\t\t\t"established security associations and answering over them.\\n");\n'
    "\t\t\tvolte_set_state(response->client_state, VOLTE_STATE_RESPONSE);\n"
    "\t\t\tret = 0;\n"
    "\t\t\tgoto out;\n"
    "\t\t}\n"
    "\t}\n"
    "\n"
)


def patch(source: str) -> str:
    if MARKER in source:
        return source

    fn_start = source.find("static int handle_volte_unauthorized(")
    if fn_start < 0:
        raise ValueError("handle_volte_unauthorized not found")

    anchor_at = source.find(ANCHOR, fn_start)
    if anchor_at < 0:
        raise ValueError("Security-Server anchor not found in handle_volte_unauthorized")
    if source.find(ANCHOR, anchor_at + 1) >= 0:
        raise ValueError("Security-Server anchor is not unique")

    return source[:anchor_at] + FALLBACK + source[anchor_at:]


try:
    original = SOURCE.read_text()
    updated = patch(original)
except (OSError, ValueError) as exc:
    print(f"reauth missing-Security-Server patch failed: {exc}", file=sys.stderr)
    raise SystemExit(1) from exc

if updated == original:
    print("re-auth without Security-Server already patched")
else:
    SOURCE.write_text(updated)
    print("patched handle_volte_unauthorized to re-auth over established SAs "
          "when a 401 has no Security-Server header")
