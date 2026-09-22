package com.lovitus.mddagent;

import org.json.JSONObject;

/** Pure transition/response rules shared by ordinary and restored call controls. */
final class CallRecovery {
    static boolean terminal(CallPlan plan, String session, JSONObject result) {
        if (plan.mode.equals("cellular")) return session.equals(result.optString("session_id"))
            && result.optBoolean("terminal_confirmed", false);
        return plan.id.equals(result.optString("call_id")) && plan.endOperation.equals(result.optString("operation_id"))
            && result.optBoolean("accepted") && "ended".equals(result.optString("code"));
    }
    static boolean mayDispatch(String phase, boolean cancelled) {
        return !cancelled && "START_MAY_HAVE_RUN".equals(phase);
    }
    static String bind(GatewayApi api) {
        return Json.sha((api.endpoint.origin+"\n"+api.endpoint.fingerprint+"\n"+api.token)
            .getBytes(java.nio.charset.StandardCharsets.UTF_8));
    }
}
