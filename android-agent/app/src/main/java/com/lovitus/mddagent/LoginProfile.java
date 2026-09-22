package com.lovitus.mddagent;

import org.json.JSONObject;

/** A local encrypted form draft, separate from the active gateway/session owner. */
final class LoginProfile {
    String address="",username="",password="",manualPin="";
    String enrollmentOrigin="",agentID="",agentToken="";
    boolean remember=true;
    static LoginProfile read(JSONObject state){
        LoginProfile p=new LoginProfile();JSONObject saved=state.optJSONObject("login_profile");
        if(saved!=null){p.address=saved.optString("server");p.username=saved.optString("username");p.manualPin=saved.optString("manual_pin");p.enrollmentOrigin=saved.optString("enrollment_origin");p.agentID=saved.optString("agent_id");p.agentToken=saved.optString("agent_token");p.remember=saved.optBoolean("remember",true);if(p.remember)p.password=saved.optString("password");}
        else{p.address=state.optString("server");p.username=state.optString("username");}
        return p;
    }
    JSONObject persisted(){return Json.obj("server",address,"username",username,"manual_pin",manualPin,"enrollment_origin",enrollmentOrigin,"agent_id",agentID,"agent_token",agentToken,"remember",remember,"password",remember?password:"");}
    Endpoint endpoint(){String value=address.trim();return new Endpoint(value.contains("://")?value:"https://"+value,manualPin);}
    static String pin(JSONObject state,String origin){
        JSONObject pins=state.optJSONObject("server_pins");if(pins!=null&&pins.has(origin))return pins.optString(origin);
        try{if(new Endpoint(state.optString("server"),state.optString("pin")).origin.equals(origin))return state.optString("pin");}catch(IllegalArgumentException ignored){}
        return "";
    }
    static void acceptPin(JSONObject state,String origin,String expected,String fingerprint)throws Exception{
        Endpoint endpoint=new Endpoint(origin,fingerprint);
        if(endpoint.fingerprint.isEmpty()||!pin(state,endpoint.origin).equals(expected))throw new IllegalStateException("Saved certificate changed; review again");
        JSONObject pins=state.optJSONObject("server_pins");if(pins==null)pins=new JSONObject();pins.put(endpoint.origin,endpoint.fingerprint);state.put("server_pins",pins);
        if(!expected.equals(endpoint.fingerprint)){
            JSONObject transitions=state.optJSONObject("pin_transitions");if(transitions==null)transitions=new JSONObject();
            org.json.JSONArray edges=transitions.optJSONArray(endpoint.origin);if(edges==null){edges=new org.json.JSONArray();JSONObject prior=transitions.optJSONObject(endpoint.origin);if(prior!=null)edges.put(prior);}
            boolean exists=false;for(int i=0;i<edges.length();i++){JSONObject edge=edges.getJSONObject(i);exists|=expected.equals(edge.optString("from"))&&endpoint.fingerprint.equals(edge.optString("to"));}
            if(!exists)edges.put(Json.obj("from",expected,"to",endpoint.fingerprint));transitions.put(endpoint.origin,edges);state.put("pin_transitions",transitions);
        }
    }
}
