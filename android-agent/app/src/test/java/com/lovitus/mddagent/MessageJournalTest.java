package com.lovitus.mddagent;

import org.json.*;
import org.junit.Test;
import static org.junit.Assert.*;

public class MessageJournalTest {
    private JSONObject line(){return Json.obj("id","line-1","card_id","8944100000000000001");}
    @Test public void unknownAIsNotOverwrittenBySuccessfulB()throws Exception {
        JSONObject config=new JSONObject();
        MessageJournal.begin(config,"scope","a",line(),"cellular");MessageJournal.begin(config,"scope","b",line(),"cellular");
        MessageJournal.response(config,"scope","b",Json.obj("message_id","b","code","cellular_sms_submitted"));
        JSONObject reloaded=new JSONObject(config.toString());
        assertEquals("unknown",reloaded.getJSONArray("message_operations").getJSONObject(0).getString("state"));
        assertEquals("submitted",reloaded.getJSONArray("message_operations").getJSONObject(1).getString("state"));
    }
    @Test public void fullUnresolvedJournalRefusesNewOperation()throws Exception {
        JSONObject config=new JSONObject();for(int i=0;i<MessageJournal.CAPACITY;i++)MessageJournal.begin(config,"s","m-"+i,line(),"vowifi");
        String before=config.toString();
        try{MessageJournal.begin(config,"s","overflow",line(),"vowifi");fail();}catch(IllegalStateException expected){}
        assertEquals(before,config.toString());
    }
    @Test public void otherScopeCannotCompleteAnUnresolvedOperation()throws Exception {
        JSONObject config=new JSONObject();MessageJournal.begin(config,"original","a",line(),"cellular");
        try{MessageJournal.response(config,"other","a",Json.obj("message_id","a","code","cellular_sms_submitted"));fail();}catch(IllegalStateException expected){}
        assertEquals("unknown",config.getJSONArray("message_operations").getJSONObject(0).getString("state"));
    }
    @Test public void legacyUnknownIsRetainedWithoutInventedIdentity()throws Exception {
        JSONObject config=Json.obj("last_sms",Json.obj("operation_id","legacy","state","unknown"));
        MessageJournal.begin(config,"current","b",line(),"cellular");
        assertEquals("legacy-unverified",config.getJSONArray("message_operations").getJSONObject(0).getString("scope"));
        assertFalse(config.has("last_sms"));
    }
    @Test public void oneSubmittedPartCannotRetireAWholeUnknownMessage()throws Exception{
        JSONObject config=new JSONObject();MessageJournal.begin(config,"s","a",line(),"vowifi","+15550100123","two-part original");
        MessageJournal.observe(config,"s",new JSONArray().put(Json.obj("message_id","a","line_id","line-1","transport","vowifi","kind","submitted","part",1)));
        JSONObject row=MessageJournal.find(config,"s","a");assertEquals("submission_observed",row.getString("state"));assertFalse(MessageJournal.resolved(row));assertEquals("two-part original",row.getString("body"));
        MessageJournal.response(config,"s","a",Json.obj("message_id","a","operation_id","a","accepted",true,"code","sent"));assertFalse(MessageJournal.find(config,"s","a").has("body"));
    }
    @Test public void receiptUsesOriginalPayloadAndCannotBecomeASend()throws Exception{
        JSONObject config=new JSONObject();MessageJournal.begin(config,"s","a",line(),"cellular","+15550100123","original A");MessageJournal.begin(config,"s","b",line(),"cellular","+15550100999","later B");
        JSONObject receipt=MessageJournal.receipt(MessageJournal.find(config,"s","a"));assertTrue(receipt.getBoolean("reconcile_only"));assertEquals("a",receipt.getString("operation_id"));assertEquals("original A",receipt.getString("body"));assertEquals("+15550100123",receipt.getString("recipient"));
        MessageJournal.notDispatched(config,"s","b");assertFalse(MessageJournal.find(config,"s","b").has("body"));
        assertThrows(IllegalStateException.class,()->MessageJournal.receipt(Json.obj("state","unknown","operation_id","old")));
    }
    @Test public void originalBodiesHaveABoundedBudgetWithoutEvictingUnknown()throws Exception{
        JSONObject config=new JSONObject();MessageJournal.begin(config,"s","a",line(),"vowifi","+15550100123","keep");String before=config.toString();
        assertThrows(IllegalStateException.class,()->MessageJournal.begin(config,"s","overflow",line(),"vowifi","+15550100123","x".repeat(MessageJournal.BODY_BUDGET)));
        assertEquals(before,config.toString());
    }
    @Test public void approvedCertificateAndSameAuthenticatedUserCanRetainUnknownOwnership()throws Exception{
        String origin="https://gateway.test",old="01".repeat(32),next="02".repeat(32),a=GatewayApi.scope(origin,old,"owner"),b=GatewayApi.scope(origin,next,"owner");
        JSONObject config=Json.obj("server",origin,"pin",old);MessageJournal.begin(config,a,"a",line(),"cellular","+15550100123","original");
        MessageSync.accept(config,a,"",Json.obj("cursor","a".repeat(32)+":7","initial",true,"messages",new JSONArray()));
        LoginProfile.acceptPin(config,origin,old,next);
        MessageJournal.adoptConfirmedTrustChange(config,origin,next,"other-user");assertEquals("unknown",MessageJournal.find(config,a,"a").getString("state"));
        MessageJournal.adoptConfirmedTrustChange(config,origin,next,"owner");assertEquals("original",MessageJournal.find(config,b,"a").getString("body"));assertEquals(next,MessageJournal.find(config,b,"a").getString("gateway_pin"));assertEquals("a".repeat(32)+":7",MessageSync.state(config,b).getString("cursor"));
    }
    @Test public void acceptedIntermediateCertificateWithoutLoginDoesNotLoseEarlierBinding()throws Exception{
        String origin="https://gateway.test",a="01".repeat(32),b="02".repeat(32),c="03".repeat(32),old=GatewayApi.scope(origin,a,"owner"),current=GatewayApi.scope(origin,c,"owner");
        JSONObject config=Json.obj("server",origin,"pin",a);MessageJournal.begin(config,old,"a",line(),"cellular","+15550100123","retained");
        MessageSync.accept(config,old,"",Json.obj("cursor","b".repeat(32)+":9","initial",true,"messages",new JSONArray()));
        LoginProfile.acceptPin(config,origin,a,b);LoginProfile.acceptPin(config,origin,b,c);
        MessageJournal.adoptConfirmedTrustChange(config,origin,c,"other-user");assertEquals("retained",MessageJournal.find(config,old,"a").getString("body"));
        MessageJournal.adoptConfirmedTrustChange(config,origin,c,"owner");assertEquals("retained",MessageJournal.find(config,current,"a").getString("body"));assertEquals("b".repeat(32)+":9",MessageSync.state(config,current).getString("cursor"));
    }
}
