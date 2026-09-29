package com.lovitus.mddagent;

import org.json.JSONObject;

/** Pure transition/response rules shared by ordinary and restored call controls. */
final class CallRecovery {
    static boolean rejectedAdmission(CallPlan plan,Exception error){
        if(!(error instanceof GatewayApi.Failure)||!plan.mode.equals("vowifi"))return false;
        GatewayApi.Failure failure=(GatewayApi.Failure)error;
        if(!failure.layer.equals("call"))return false;
        // Provider checks these before reserving/starting this operation. Generic
        // 4xx/5xx and a lost response remain unknown, never evidence of rejection.
        return failure.status==409&&failure.code.equals("call_busy")||
            plan.incoming!=null&&failure.status==404&&failure.code.equals("incoming_call_not_found");
    }
    static boolean terminal(CallPlan plan, String session, JSONObject result) {
        if (plan.mode.equals("cellular")) return session.equals(result.optString("session_id"))
            && result.optBoolean("terminal_confirmed", false);
        return plan.id.equals(result.optString("call_id")) && plan.endOperation.equals(result.optString("operation_id"))
            && result.optBoolean("accepted") && "ended".equals(result.optString("code"));
    }
    static boolean mayDispatch(String phase, boolean cancelled) {
        return !cancelled && "START_MAY_HAVE_RUN".equals(phase);
    }
}
