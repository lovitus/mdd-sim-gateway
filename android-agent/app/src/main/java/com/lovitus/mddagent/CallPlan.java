package com.lovitus.mddagent;
import org.json.*;
import java.net.URLEncoder;
import java.nio.charset.StandardCharsets;
/** All mutations are exact-identity and single-attempt; reconnect is media-only. */
final class CallPlan {
    final String line,card,mode,number,id,operation=Json.id(),endOperation=Json.id();final JSONObject incoming;
    CallPlan(JSONObject line,String mode,String number,JSONObject incoming)throws Exception{
        this.line=line.getString("id");this.card=line.getString("card_id");this.mode=mode;this.incoming=incoming;
        if(!line.optBoolean("enabled")||card.isEmpty()||!(mode.equals("vowifi")||mode.equals("cellular")))throw new IllegalArgumentException("Line/transport unavailable");
        this.number=incoming==null?dialTarget(number):incoming.optString(mode.equals("cellular")?"number":"caller","");
        this.id=incoming==null?Json.id():incoming.getString(mode.equals("cellular")?"incoming_event_id":"call_id");
        if(!id.matches("[A-Za-z0-9_.:-]{1,160}")||incoming!=null&&mode.equals("cellular")&&!incoming.optBoolean("actionable"))throw new IllegalArgumentException("Incoming call no longer actionable");
    }
    static String encode(String v){try{return URLEncoder.encode(v,"UTF-8").replace("+","%20");}catch(java.io.UnsupportedEncodingException e){throw new AssertionError(e);}}
    static String dialTarget(String value){String v=value.trim().replaceAll("[\\s().-]","");if(v.startsWith("00"))v="+"+v.substring(2);if(!v.matches("\\+[1-9][0-9]{5,14}|[0-9]{2,6}"))throw new IllegalArgumentException("Use an international +number or service code");return v;}
    String prefix(){return "/v1/lines/"+encode(line)+(mode.equals("cellular")?"/cellular/calls/":"/vowifi/calls/");}
    String leases(){return mode.equals("cellular")?"/v1/cellular/media/leases":"/v1/media/leases";}
    JSONObject lease(){JSONObject b=Json.obj("line_id",line,"call_id",id);if(mode.equals("cellular")){try{b.put("expected_card_id",card);if(incoming!=null)addIncoming(b);}catch(Exception e){throw new IllegalArgumentException(e);}}return b;}
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
