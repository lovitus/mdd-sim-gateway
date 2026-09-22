package com.lovitus.mddagent;

import org.json.*;
import org.junit.Test;
import static org.junit.Assert.*;

public class MessageSyncTest {
    private JSONObject event(String line,boolean realtime){return Json.obj("line_id",line,"provider_id","p","event_id","same","kind","received","realtime",realtime);}
    @Test public void cursorAndOutboxSurviveRestartWithoutCrossLineCollision()throws Exception {
        JSONObject config=new JSONObject();JSONArray messages=new JSONArray().put(event("a",true)).put(event("b",true)).put(event("c",false));
        MessageSync.accept(config,"account","",Json.obj("cursor","db:3","initial",false,"messages",messages));
        JSONObject restarted=new JSONObject(config.toString());
        assertEquals("db:3",MessageSync.state(restarted,"account").getString("cursor"));
        assertEquals(2,MessageSync.state(restarted,"account").getJSONArray("pending").length());
        assertEquals("",MessageSync.state(restarted,"other-account").getString("cursor"));
        MessageSync.acknowledged(restarted,"account","a/p/same");
        assertEquals("b/p/same",MessageSync.state(restarted,"account").getJSONArray("pending").getString(0));
        try{MessageSync.accept(restarted,"account","",Json.obj("cursor","db:3","messages",messages));fail();}catch(IllegalStateException expected){}
    }
    @Test public void initialSeedDoesNotNotifyHistoricalMessages()throws Exception {
        JSONObject config=new JSONObject();MessageSync.accept(config,"s","",Json.obj("cursor","db:1","initial",true,"messages",new JSONArray().put(event("a",true))));
        assertEquals(0,MessageSync.state(config,"s").getJSONArray("pending").length());
    }
    @Test public void suppressionIsNotRecordedAsNotificationDelivery()throws Exception{
        JSONObject config=new JSONObject();MessageSync.accept(config,"s","",Json.obj("cursor","a".repeat(32)+":1","initial",false,"messages",new JSONArray().put(event("a",true))));
        JSONArray batch=new JSONArray(MessageSync.state(config,"s").getJSONArray("pending").toString());MessageSync.acknowledged(config,"s",batch,"suppressed_by_permission");
        assertEquals("suppressed_by_permission",MessageSync.state(config,"s").getString("notification_status"));assertEquals(0,MessageSync.state(config,"s").getJSONArray("pending").length());
        MessageSync.accept(config,"s","a".repeat(32)+":1",Json.obj("cursor","a".repeat(32)+":1","initial",false,"messages",new JSONArray()));assertEquals("suppressed_by_permission",MessageSync.state(config,"s").getString("notification_status"));
    }
}
