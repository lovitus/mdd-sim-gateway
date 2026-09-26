package com.lovitus.mddagent;

import org.json.*;

/** Bounded encrypted submission receipts and unresolved work, never an automatic send queue. */
final class MessageJournal {
    static final int CAPACITY = 128;
    static final int BODY_BUDGET = 256 * 1024;
    private static JSONArray rows(JSONObject config) throws JSONException {
        JSONArray records = config.optJSONArray("message_operations");
        if(records==null){records=new JSONArray();config.put("message_operations",records);}
        JSONObject legacy=config.optJSONObject("last_sms");
        if(legacy!=null&&!legacy.optString("operation_id").isEmpty()){
            boolean found=false;
            for(int i=0;i<records.length();i++)found|=legacy.optString("operation_id").equals(records.getJSONObject(i).optString("operation_id"));
            if(!found){JSONObject imported=new JSONObject(legacy.toString());imported.put("scope","legacy-unverified");imported.put("state","unknown");records.put(imported);}
            config.remove("last_sms");
        }
        return records;
    }
    static void begin(JSONObject config,String scope,String id,JSONObject line,String transport,String... recipient)throws JSONException {
        JSONArray records=rows(config),kept=new JSONArray();
        int toDrop=Math.max(0,records.length()+1-CAPACITY);
        for(int i=0;i<records.length();i++){
            JSONObject row=records.getJSONObject(i);
            if(id.equals(row.optString("operation_id")))throw new IllegalStateException("Message operation already exists");
            if(toDrop>0&&resolved(row)){toDrop--;continue;}
            kept.put(row);
        }
        if(kept.length()>=CAPACITY)throw new IllegalStateException("未决短信记录已满，请先核对原提交");
        String body=recipient.length>1?recipient[1]:"";int bytes=body.getBytes(java.nio.charset.StandardCharsets.UTF_8).length;
        for(int i=0;i<kept.length();i++)bytes+=kept.getJSONObject(i).optString("body").getBytes(java.nio.charset.StandardCharsets.UTF_8).length;
        // Evict only resolved local previews; never discard an unresolved payload.
        for(int i=0;i<kept.length()&&bytes>BODY_BUDGET;i++){
            JSONObject old=kept.getJSONObject(i);if(resolved(old)){bytes-=old.optString("body").getBytes(java.nio.charset.StandardCharsets.UTF_8).length;old.remove("body");}
        }
        if(bytes>BODY_BUDGET)throw new IllegalStateException("未决短信内容已达安全容量，请先核对原提交");
        JSONObject record=Json.obj("scope",scope,"operation_id",id,"message_id",id,"line_id",line.getString("id"),
            "card_id",line.getString("card_id"),"line_name",line.optString("name"),"line_number",line.optString("number"),"gateway_origin",config.optString("server"),"gateway_pin",config.optString("pin"),
            "recipient",recipient.length==0?"":recipient[0],"transport",transport,"state","unknown","created_at",System.currentTimeMillis());
        if(!body.isEmpty())record.put("body",body);kept.put(record);
        config.put("message_operations",kept);
    }
    static void response(JSONObject config,String scope,String id,JSONObject result)throws JSONException {
        JSONArray records=rows(config);
        for(int i=0;i<records.length();i++){
            JSONObject row=records.getJSONObject(i);
            if(!id.equals(row.optString("operation_id"))||!scope.equals(row.optString("scope")))continue;
            boolean accepted=confirmed(row,result);
            if(accepted&&!row.optString("state").equals("failure_observed"))row.put("state","submitted");
            return;
        }
        throw new IllegalStateException("Message record owner changed");
    }
    static void notDispatched(JSONObject config,String scope,String id)throws JSONException {
        JSONArray records=rows(config);
        for(int i=0;i<records.length();i++){JSONObject row=records.getJSONObject(i);if(id.equals(row.optString("operation_id"))&&scope.equals(row.optString("scope")))row.put("state","not_dispatched");}
    }
    static void failure(JSONObject config,String scope,String id,Exception failure)throws JSONException {
        JSONArray records=rows(config);
        for(int i=0;i<records.length();i++){
            JSONObject row=records.getJSONObject(i);
            if(id.equals(row.optString("operation_id"))&&scope.equals(row.optString("scope"))){
                // A failed response does not prove that no SMS part was delivered.
                row.put("failure_detail",RemoteCall.safe(failure));
                return;
            }
        }
    }
    static boolean observe(JSONObject config,String scope,JSONArray events)throws JSONException {
        String before=config.toString();
        JSONArray records=rows(config);
        for(int i=0;i<records.length();i++){
            JSONObject row=records.getJSONObject(i);
            if(!scope.equals(row.optString("scope"))||row.optString("state").equals("not_dispatched")||row.optString("message_id").isEmpty())continue;
            for(int j=0;j<events.length();j++){
                JSONObject event=events.getJSONObject(j);
                if(row.optString("message_id").equals(event.optString("message_id"))&&row.optString("line_id").equals(event.optString("line_id"))
                    &&row.optString("transport").equals(event.optString("transport"))
                    &&(event.optString("kind").equals("submitted")||event.optString("kind").equals("delivery"))){
                    if(event.optString("state").equals("failed")){
                        row.put("state","failure_observed");
                        String detail=event.optString("error",event.optString("error_code"));
                        if(!detail.isEmpty())row.put("failure_detail",detail);
                    }else if(event.optString("kind").equals("submitted")&&!resolved(row)&&!row.optString("state").equals("failure_observed"))row.put("state","submission_observed");
                }
            }
        }
        return !before.equals(config.toString());
    }

    /** Adapted from this repository's webui/src/mdd/historyAdapter.js messageRows. */
    static JSONArray history(JSONArray events) {
        java.util.Map<String,JSONObject> reports=new java.util.HashMap<>();
        java.util.Set<String> submitted=new java.util.HashSet<>();
        for(int i=0;i<events.length();i++){
            JSONObject event=events.optJSONObject(i);if(event==null)continue;
            String key=reportKey(event);if(key.isEmpty())continue;
            if(event.optString("kind").equals("submitted"))submitted.add(key);
            if(event.optString("kind").equals("delivery")){
                JSONObject prior=reports.get(key);
                if(prior==null||eventTime(event,"received_at").compareTo(eventTime(prior,"received_at"))>=0)reports.put(key,event);
            }
        }
        java.util.ArrayList<JSONObject> rows=new java.util.ArrayList<>();
        try{
            for(int i=0;i<events.length();i++){
                JSONObject event=events.optJSONObject(i);if(event==null)continue;
                String key=reportKey(event),kind=event.optString("kind");
                // Keep orphan receipts visible until their earlier submission page is loaded.
                if(kind.equals("delivery")&&submitted.contains(key))continue;
                JSONObject row=new JSONObject(event.toString());
                JSONObject report=kind.equals("submitted")?reports.get(key):null;
                if(report!=null){
                    row.put("delivery_state",report.optString("state"));
                    for(String field:new String[]{"state","error","sip_code","rp_cause"})if(report.has(field))row.put(field,report.get(field));
                }
                rows.add(row);
            }
        }catch(JSONException failure){throw new IllegalArgumentException("Invalid message history",failure);}
        rows.sort((left,right)->eventTime(left,"observed_at").compareTo(eventTime(right,"observed_at")));
        return new JSONArray(rows);
    }

    private static String reportKey(JSONObject event){
        if(event.optString("line_id").isEmpty()||event.optString("transport").isEmpty()||event.optString("message_id").isEmpty())return "";
        return new JSONArray().put(event.optString("line_id")).put(event.optString("transport")).put(event.optString("message_id")).put(event.optInt("part")).toString();
    }

    private static java.time.Instant eventTime(JSONObject event,String preferred){
        for(String field:new String[]{preferred,"received_at","observed_at"}){
            try{return java.time.Instant.parse(event.optString(field));}catch(java.time.format.DateTimeParseException ignored){}
        }
        return java.time.Instant.EPOCH;
    }
    static boolean resolved(JSONObject row){return row.optString("state").equals("submitted")||row.optString("state").equals("not_dispatched");}
    static boolean confirmed(JSONObject row,JSONObject result){return row.optString("message_id").equals(result.optString("message_id"))&&(row.optString("transport").equals("cellular")?result.optString("code").equals("cellular_sms_submitted"):row.optString("operation_id").equals(result.optString("operation_id"))&&result.optBoolean("accepted")&&result.optString("code").equals("sent"));}
    static JSONObject find(JSONObject config,String scope,String id)throws JSONException{JSONArray records=rows(config);for(int i=0;i<records.length();i++){JSONObject row=records.getJSONObject(i);if(scope.equals(row.optString("scope"))&&id.equals(row.optString("operation_id")))return new JSONObject(row.toString());}throw new IllegalStateException("原短信记录不属于当前登录");}
    static void adoptConfirmedTrustChange(JSONObject config,String origin,String pin,String authenticatedUser)throws JSONException{
        JSONObject transitions=config.optJSONObject("pin_transitions");if(transitions==null||!pin.equals(LoginProfile.pin(config,origin)))return;
        JSONArray edges=transitions.optJSONArray(origin);if(edges==null){JSONObject legacy=transitions.optJSONObject(origin);if(legacy==null)return;edges=new JSONArray().put(legacy);}
        java.util.Map<String,java.util.Set<String>> predecessors=new java.util.HashMap<>();
        for(int i=0;i<edges.length();i++){JSONObject edge=edges.getJSONObject(i);predecessors.computeIfAbsent(edge.optString("to"),key->new java.util.LinkedHashSet<>()).add(edge.optString("from"));}
        java.util.LinkedHashSet<String> trusted=new java.util.LinkedHashSet<>();java.util.ArrayDeque<String> pending=new java.util.ArrayDeque<>();trusted.add(pin);pending.add(pin);
        while(!pending.isEmpty())for(String prior:predecessors.getOrDefault(pending.remove(),java.util.Collections.emptySet()))if(trusted.add(prior))pending.add(prior);
        String newScope=GatewayApi.scope(origin,pin,authenticatedUser);JSONArray records=rows(config);
        for(String priorPin:trusted){if(priorPin.equals(pin))continue;String oldScope=GatewayApi.scope(origin,priorPin,authenticatedUser);
            for(int i=0;i<records.length();i++){JSONObject row=records.getJSONObject(i);if(oldScope.equals(row.optString("scope"))&&origin.equals(row.optString("gateway_origin"))&&priorPin.equals(row.optString("gateway_pin")))row.put("scope",newScope).put("gateway_pin",pin);}
        }
    }
}
