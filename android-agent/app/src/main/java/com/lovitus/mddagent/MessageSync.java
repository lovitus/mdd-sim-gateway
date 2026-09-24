package com.lovitus.mddagent;

import org.json.*;
import java.util.LinkedHashSet;

/** Cursor and notification outbox advance together; event identity is not a bare event_id. */
final class MessageSync {
    static JSONObject state(JSONObject config,String scope) {
        JSONObject all=config.optJSONObject("message_sync");
        JSONObject state=all==null?null:all.optJSONObject(scope);
        return state==null?Json.obj("cursor","","pending",new JSONArray(),"gap",false):state;
    }
    static void accept(JSONObject config,String scope,String expected,JSONObject page)throws JSONException {
        JSONObject current=state(config,scope);
        if(!expected.equals(current.optString("cursor")))throw new IllegalStateException("Message cursor replaced");
        if(page.optString("cursor").isEmpty())throw new IllegalStateException("Missing message sync cursor");
        JSONArray events=page.getJSONArray("messages");
        MessageJournal.observe(config,scope,events);
        LinkedHashSet<String> pending=new LinkedHashSet<>();
        JSONArray old=Json.array(current,"pending");for(int i=0;i<old.length();i++)pending.add(old.getString(i));
        if(!page.optBoolean("initial"))for(int i=0;i<events.length();i++){
            JSONObject event=events.getJSONObject(i);
            if(event.optBoolean("realtime")&&event.optString("kind").equals("received"))pending.add(identity(event));
        }
        if(pending.size()>200)throw new IllegalStateException("Message notification backlog full");
        JSONObject all=config.optJSONObject("message_sync");if(all==null){all=new JSONObject();config.put("message_sync",all);}
        all.put(scope,Json.obj("cursor",page.getString("cursor"),"gap",current.optBoolean("gap")||page.optBoolean("gap"),"pending",new JSONArray(pending),"notification_status",current.optString("notification_status")));
    }
    static String identity(JSONObject event)throws JSONException {
        return event.getString("line_id")+"/"+event.getString("provider_id")+"/"+event.getString("event_id");
    }
    static void acknowledged(JSONObject config,String scope,String identity)throws JSONException {
        acknowledged(config,scope,new JSONArray().put(identity),"submitted_to_android");
    }
    static void acknowledged(JSONObject config,String scope,JSONArray identities,String status)throws JSONException {
        JSONObject current=state(config,scope);JSONArray remaining=new JSONArray(),pending=Json.array(current,"pending");
        LinkedHashSet<String> done=new LinkedHashSet<>();for(int i=0;i<identities.length();i++)done.add(identities.getString(i));
        for(int i=0;i<pending.length();i++)if(!done.contains(pending.getString(i)))remaining.put(pending.getString(i));
        current.put("pending",remaining).put("notification_status",status);
    }
    static void adoptTrustChange(JSONObject config,String oldScope,String newScope)throws JSONException{
        JSONObject all=config.optJSONObject("message_sync");if(all==null||!all.has(oldScope))return;
        JSONObject old=all.getJSONObject(oldScope),next=all.optJSONObject(newScope);
        if(next==null){all.put(newScope,new JSONObject(old.toString()));all.remove(oldScope);return;}
        LinkedHashSet<String> pending=new LinkedHashSet<>();for(JSONObject state:new JSONObject[]{old,next}){JSONArray rows=Json.array(state,"pending");for(int i=0;i<rows.length();i++)pending.add(rows.getString(i));}
        if(pending.size()>400)throw new IllegalStateException("Message trust-transition backlog requires reconciliation");
        String cursor="";boolean gap=old.optBoolean("gap")||next.optBoolean("gap");
        try{String[] a=old.getString("cursor").split(":",-1),b=next.getString("cursor").split(":",-1);if(a.length==2&&b.length==2&&a[0].equals(b[0]))cursor=a[0]+":"+Math.min(Long.parseLong(a[1]),Long.parseLong(b[1]));else gap=true;}catch(Exception invalid){gap=true;}
        all.put(newScope,Json.obj("cursor",cursor,"gap",gap,"pending",new JSONArray(pending),"notification_status",next.optString("notification_status")));all.remove(oldScope);
    }
}
