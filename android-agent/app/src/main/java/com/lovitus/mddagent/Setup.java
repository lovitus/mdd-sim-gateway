package com.lovitus.mddagent;
import org.json.*;
/** QR configuration is data, never a command or automatic trust decision. */
final class Setup {
    final Endpoint endpoint; final String agentID,agentToken;
    Setup(String text)throws Exception{
        if(text.length()>8192)throw new IllegalArgumentException("Setup QR is too large");
        JSONObject q=new JSONObject(text);
        if(q.optInt("version")!=1||!q.optString("type").equals("mdd-agent-setup"))throw new IllegalArgumentException("Not an MDD setup QR");
        endpoint=new Endpoint(q.getString("server"),q.optString("certificate_sha256"));
        agentID=q.optString("agent_id");agentToken=q.optString("agent_token");
        if(!agentID.isEmpty()&&!agentID.matches("[A-Za-z0-9_.:-]{1,128}"))throw new IllegalArgumentException("Invalid agent ID");
        if(agentID.isEmpty()!=agentToken.isEmpty()||agentToken.length()>512||agentToken.matches(".*[\\r\\n ].*"))throw new IllegalArgumentException("Invalid reader enrollment");
        if(q.has("password"))throw new IllegalArgumentException("Setup QR must not contain an account password");
    }
}
