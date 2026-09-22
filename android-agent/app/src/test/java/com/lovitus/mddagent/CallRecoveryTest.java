package com.lovitus.mddagent;

import org.json.JSONObject;
import org.junit.Test;
import static org.junit.Assert.*;

public class CallRecoveryTest {
    private CallPlan plan(String mode)throws Exception {
        return new CallPlan(Json.obj("id","line-1","card_id","8944100000000000001","enabled",true),mode,"+441234567890",null);
    }
    @Test public void restoredPlanKeepsEveryOperationIdentity()throws Exception {
        CallPlan original=plan("cellular"),restored=new CallPlan(new JSONObject(original.record().toString()));
        assertEquals(original.id,restored.id);
        assertEquals(original.operation,restored.operation);
        assertEquals(original.endOperation,restored.endOperation);
        assertEquals(original.start("session").toString(),restored.start("session").toString());
    }
    @Test public void cellularHTTPResponseIsNotTerminalWithoutExactConfirmation()throws Exception {
        CallPlan plan=plan("cellular");
        assertFalse(CallRecovery.terminal(plan,"session",Json.obj("code","cellular_call_ended","session_id","session")));
        assertFalse(CallRecovery.terminal(plan,"session",Json.obj("session_id","session","terminal_confirmed",false)));
        assertFalse(CallRecovery.terminal(plan,"session",Json.obj("session_id","other","terminal_confirmed",true)));
        assertTrue(CallRecovery.terminal(plan,"session",Json.obj("session_id","session","terminal_confirmed",true)));
    }
    @Test public void vowifiRequiresOriginalEndReceipt()throws Exception {
        CallPlan plan=plan("vowifi");JSONObject result=Json.obj("accepted",true,"code","ended","call_id",plan.id,"operation_id",plan.endOperation);
        assertTrue(CallRecovery.terminal(plan,"session",result));
        result.put("operation_id","another-operation");assertFalse(CallRecovery.terminal(plan,"session",result));
        result.put("operation_id",plan.endOperation).put("code","active");assertFalse(CallRecovery.terminal(plan,"session",result));
    }
    @Test public void cancellationAlwaysWinsBeforeAdmission() {
        assertTrue(CallRecovery.mayDispatch("START_MAY_HAVE_RUN",false));
        assertFalse(CallRecovery.mayDispatch("START_MAY_HAVE_RUN",true));
        assertFalse(CallRecovery.mayDispatch("ENDING",false));
        assertFalse(CallRecovery.mayDispatch("TERMINAL",false));
    }
}
