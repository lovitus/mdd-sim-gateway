package com.lovitus.mddagent;
import org.json.*;
import java.net.URLEncoder;
import java.nio.charset.StandardCharsets;
/** All mutations are exact-identity and single-attempt; reconnect is media-only. */
final class CallPlan {
    final String line,card,mode,number,id,operation,endOperation,recoveryKey;final JSONObject incoming;
    final String lineName,lineNumber;
    CallPlan(JSONObject line,String mode,String number,JSONObject incoming)throws Exception{
        operation=Json.id();endOperation=Json.id();recoveryKey=Json.id()+Json.id();
        this.line=line.getString("id");this.card=line.getString("card_id");this.mode=mode;this.incoming=incoming;
        lineName=line.optString("name",this.line);lineNumber=line.optString("number");
        if(!line.optBoolean("enabled")||card.isEmpty()||!(mode.equals("vowifi")||mode.equals("cellular")))throw new IllegalArgumentException("Line/transport unavailable");
        this.number=incoming==null?dialTarget(number):incoming.optString(mode.equals("cellular")?"number":"caller","");
        this.id=incoming==null?Json.id():incoming.getString(mode.equals("cellular")?"incoming_event_id":"call_id");
        if(!id.matches("[A-Za-z0-9_.:-]{1,160}")||incoming!=null&&mode.equals("cellular")&&!incoming.optBoolean("actionable"))throw new IllegalArgumentException("Incoming call no longer actionable");
    }
    CallPlan(JSONObject saved)throws Exception{
        line=saved.getString("line");card=saved.getString("card");mode=saved.getString("mode");
        lineName=saved.optString("line_name",line);lineNumber=saved.optString("line_number");
        number=saved.getString("number");id=saved.getString("call_id");operation=saved.getString("operation_id");
        endOperation=saved.getString("end_operation_id");incoming=saved.optJSONObject("incoming");recoveryKey=saved.optString("recovery_key");
        if(!(mode.equals("cellular")||mode.equals("vowifi"))||line.isEmpty()||card.isEmpty()||id.isEmpty()||operation.isEmpty()||endOperation.isEmpty())throw new IllegalArgumentException("Invalid saved call identity");
    }
    JSONObject record(){return Json.obj("line",line,"card",card,"mode",mode,"number",number,"call_id",id,
        "operation_id",operation,"end_operation_id",endOperation,"recovery_key",recoveryKey,"incoming",incoming==null?JSONObject.NULL:incoming,
        "line_name",lineName,"line_number",lineNumber);}
    static String encode(String v){try{return URLEncoder.encode(v,"UTF-8").replace("+","%20");}catch(java.io.UnsupportedEncodingException e){throw new AssertionError(e);}}
    static String dialTarget(String value){String v=value.trim().replaceAll("[\\s().-]","");if(v.startsWith("00"))v="+"+v.substring(2);if(!v.matches("\\+[1-9][0-9]{5,14}|[0-9]{2,6}"))throw new IllegalArgumentException("Use an international +number or service code");return v;}
    String prefix(){return "/v1/lines/"+encode(line)+(mode.equals("cellular")?"/cellular/calls/":"/vowifi/calls/");}
    String leases(){return mode.equals("cellular")?"/v1/cellular/media/leases":"/v1/media/leases";}
    JSONObject lease(){JSONObject b=Json.obj("line_id",line,"call_id",id);try{if(!recoveryKey.isEmpty())b.put("operation_id",operation).put("recovery_key",recoveryKey);if(mode.equals("cellular")){b.put("expected_card_id",card);if(incoming!=null)addIncoming(b);}}catch(Exception e){throw new IllegalArgumentException(e);}return b;}
    JSONObject recovery(String action){JSONObject b=Json.obj("call_id",id,"operation_id",operation,"recovery_key",recoveryKey,"action",action);if(action.equals("end"))try{b.put("end_operation_id",endOperation);}catch(Exception e){throw new IllegalArgumentException(e);}return b;}
    void addIncoming(JSONObject b)throws Exception{b.put("operation_id",operation).put("incoming_event_id",incoming.getString("incoming_event_id")).put("sim_session_generation",incoming.getString("sim_session_generation")).put("native_call_index",incoming.getInt("native_call_index")).put("call_occurrence",incoming.getLong("occurrence"));}
    JSONObject start(String session)throws Exception{
        JSONObject b=Json.obj("operation_id",operation);
        if(mode.equals("cellular")){b.put("session_id",session).put("expected_card_id",card);if(incoming!=null)addIncoming(b);else b.put("callee",number);}
        else{b.put("call_id",id).put("media_session_id",session).put("media_buffer_ms",500);if(incoming==null)b.put("callee",number).put("expected_card_id",card);}
        return b;
    }
    String startPath(){return prefix()+(incoming==null?"start":mode.equals("cellular")?"answer":"incoming/answer");}
    JSONObject end(String session){return mode.equals("cellular")?Json.obj("operation_id",endOperation,"session_id",session):Json.obj("operation_id",endOperation,"call_id",id,"reason_code","user_hangup");}
    static JSONObject sms(String card,String to,String body,String id){if(card.isEmpty()||body.trim().isEmpty()||body.length()>4096)throw new IllegalArgumentException("Message empty or too large");return Json.obj("operation_id",id,"message_id",id,"expected_card_id",card,"recipient",dialTarget(to),"body",body);}
}
