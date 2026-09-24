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
    static boolean recoveryTerminal(JSONObject result) {
        String outcome=result.optString("terminal_outcome");
        return result.optBoolean("terminal_confirmed")
            && hasTerminalIdentity(result)
            && (outcome.isEmpty()||outcome.equals("ended")||outcome.equals("rejected"));
    }
    private static boolean hasTerminalIdentity(JSONObject result) {
        return !result.optString("call_id").isEmpty()
            && !result.optString("operation_id").isEmpty()
            && !result.optString("session_id").isEmpty();
    }
    static boolean mayDispatch(String phase, boolean cancelled) {
        return !cancelled && "START_MAY_HAVE_RUN".equals(phase);
    }
    static int terminalMessage(JSONObject result) {
        switch(result.optString("terminal_outcome")){
            case "rejected":return R.string.call_rejected;
            case "":
            case "ended":return R.string.call_original_ended;
            default:return R.string.call_end_unconfirmed;
        }
    }
    static String bind(GatewayApi api) {
        return Json.sha((api.endpoint.origin+"\n"+api.endpoint.fingerprint+"\n"+api.token)
            .getBytes(java.nio.charset.StandardCharsets.UTF_8));
    }
}
