package com.lovitus.mddagent;

import android.content.Context;
import okhttp3.*;
import okhttp3.mockwebserver.*;
import okhttp3.tls.*;
import org.json.*;
import java.util.*;
import java.util.concurrent.*;
import java.util.concurrent.atomic.*;
import static org.junit.Assert.*;

/** Existing Core message/history shapes; receipt routes never increment sends. */
final class SmsGatewayFixture implements AutoCloseable {
    final MockWebServer gateway=new MockWebServer();final ConfigStore store;final String mode,scope,otherScope;
    private final ScheduledExecutorService heartbeat=Executors.newSingleThreadScheduledExecutor();
    final String lineID="fixture-sms-line",card="8944100000000000001",stream="a".repeat(32);
    final JSONObject line;final List<JSONObject> events=new CopyOnWriteArrayList<>();
    final Map<String,JSONObject> original=new ConcurrentHashMap<>();
    final AtomicInteger sends=new AtomicInteger(),receipts=new AtomicInteger(),syncs=new AtomicInteger();
    final AtomicInteger syncError=new AtomicInteger(),connections=new AtomicInteger();
    private final boolean missingMobileEndpoint;
    final AtomicBoolean failNextSync=new AtomicBoolean();final AtomicReference<Throwable> failure=new AtomicReference<>();
    final CountDownLatch connected=new CountDownLatch(1),lateHistory=new CountDownLatch(1);
    final CountDownLatch oldSyncBlocked=new CountDownLatch(1);final AtomicBoolean blockOldSync=new AtomicBoolean();volatile CountDownLatch oldSyncGate;
    final CountDownLatch oldSyncReturned=new CountDownLatch(1);
    volatile CountDownLatch historyGate;volatile WebSocket observer;volatile String firstID="",secondID="";long sequence;
    SmsGatewayFixture(Context context,String mode)throws Exception{this(context,mode,false);}
    SmsGatewayFixture(Context context,String mode,boolean missingMobileEndpoint)throws Exception{
        assertTrue(context.getPackageName().endsWith(".qa"));this.mode=mode;this.missingMobileEndpoint=missingMobileEndpoint;store=new ConfigStore(context);
        line=Json.obj("id",lineID,"name","Fixture SMS line","number","+15550100000","card_id",card,"enabled",true,"ims","ready","operations",Json.obj(mode+"_sms",Json.obj("ready",true)));
        HeldCertificate certificate=new HeldCertificate.Builder().addSubjectAlternativeName("localhost").build();gateway.useHttps(new HandshakeCertificates.Builder().heldCertificate(certificate).build().sslSocketFactory(),false);
        gateway.setDispatcher(new okhttp3.mockwebserver.Dispatcher(){public MockResponse dispatch(RecordedRequest request){try{return handle(request);}catch(Throwable error){failure.compareAndSet(null,error);return new MockResponse().setResponseCode(500).setBody("{\"code\":\"fixture_contract_error\"}");}}});gateway.start();
        String origin=new Endpoint(gateway.url("/").toString(),"").origin,pin=Json.sha(certificate.certificate().getEncoded());scope=GatewayApi.scope(origin,pin,"fixture-owner");otherScope=GatewayApi.scope(origin,pin,"fixture-other");
        store.clear();store.save(Json.obj("server",origin,"pin",pin,"token","fixture-session","csrf","fixture-csrf","available",true,"account_scope",scope));
        heartbeat.scheduleWithFixedDelay(this::heartbeat,20,20,TimeUnit.SECONDS);
    }
    private MockResponse handle(RecordedRequest request)throws Exception{
        HttpUrl url=request.getRequestUrl();String path=url.encodedPath();boolean other="Bearer fixture-other-session".equals(request.getHeader("Authorization"));assertEquals(other?"Bearer fixture-other-session":"Bearer fixture-session",request.getHeader("Authorization"));
        if(request.getMethod().equals("POST"))assertEquals(other?"fixture-other-csrf":"fixture-csrf",request.getHeader("X-MDD-CSRF-Token"));
        if(path.equals("/api/auth/status"))return json(Json.obj("authenticated",true,"username",other?"fixture-other":"fixture-owner","token",other?"fixture-other-session":"fixture-session"));
        if(path.equals("/v1/mobile/ws")){if(missingMobileEndpoint)return new MockResponse().setResponseCode(404);return new MockResponse().withWebSocketUpgrade(new WebSocketListener(){public void onOpen(WebSocket socket,Response response){synchronized(SmsGatewayFixture.this){observer=socket;connections.incrementAndGet();publish();connected.countDown();}}public void onClosing(WebSocket socket,int code,String reason){socket.close(code,null);}});}
        if(path.equals("/v1/mobile/lines/"+lineID))return json(line);
        if(path.equals("/v1/messages")&&"true".equals(url.queryParameter("sync"))){
            boolean held=!other&&blockOldSync.getAndSet(false);if(held){oldSyncBlocked.countDown();assertNotNull(oldSyncGate);assertTrue(oldSyncGate.await(15,TimeUnit.SECONDS));}
            syncs.incrementAndGet();int error=syncError.get();if(error!=0||failNextSync.getAndSet(false))return new MockResponse().setResponseCode(error==0?503:error).setBody("{\"code\":\"message_read_failed\"}");
            String cursor=url.queryParameter("after");boolean initial=cursor==null||cursor.isEmpty();int after=initial?Math.max(0,events.size()-100):Integer.parseInt(cursor.substring(cursor.indexOf(':')+1));int through=Math.min(events.size(),after+100);JSONArray rows=new JSONArray();for(int i=after;i<through;i++)rows.put(events.get(i));
            MockResponse response=json(Json.obj("messages",rows,"cursor",stream+":"+through,"more",through<events.size(),"initial",initial,"gap",false));if(held)oldSyncReturned.countDown();return response;
        }
        if(path.equals("/v1/messages/conversations")){assertEquals("true",url.queryParameter("all"));return json(Json.obj("conversations",new JSONArray().put(Json.obj("line_id","archived-line","transport",mode,"peer","+15550100888","count",65,"last",received(65,"archived-line","+15550100888")))));}
        if(path.equals("/v1/messages")&&"true".equals(url.queryParameter("page"))){
            assertEquals("archived-line",url.queryParameter("line_id"));assertEquals(mode,url.queryParameter("transport"));assertEquals("+15550100888",url.queryParameter("peer"));String before=url.queryParameter("before");int last=before==null||before.isEmpty()?65:Integer.parseInt(before)-1;JSONArray rows=new JSONArray();for(int i=Math.max(1,last-49);i<=last;i++)rows.put(received(i,"archived-line","+15550100888"));
            if(last<65&&historyGate!=null){lateHistory.countDown();assertTrue(historyGate.await(10,TimeUnit.SECONDS));}
            return json(Json.obj("messages",rows,"next_before",last>50?String.valueOf(last-49):""));
        }
        String sendPath="/v1/lines/"+lineID+(mode.equals("cellular")?"/cellular/messages":"/vowifi/messages/send");
        String receiptPath="/v1/lines/"+lineID+"/vowifi/messages/receipt";
        if(path.equals(sendPath)||path.equals(receiptPath)){
            JSONObject body=new JSONObject(request.getBody().readUtf8());String id=body.getString("operation_id");assertEquals(id,body.getString("message_id"));assertEquals(card,body.getString("expected_card_id"));
            boolean query=path.equals(receiptPath)||body.optBoolean("reconcile_only");
            if(query){JSONObject prior=original.get(id);assertNotNull("Query cannot create a missing send",prior);assertEquals(prior.getString("recipient"),body.getString("recipient"));assertEquals(prior.getString("body"),body.getString("body"));if(mode.equals("cellular"))assertTrue(body.getBoolean("reconcile_only"));receipts.incrementAndGet();return submitted(id);}
            int count=sends.incrementAndGet();original.put(id,new JSONObject(body.toString()));
            if(count==1){firstID=id;return new MockResponse().setResponseCode(502).setBody("{\"code\":\"submission_uncertain\"}");}
            secondID=id;return submitted(id);
        }
        throw new AssertionError("Unexpected SMS route: "+path);
    }
    private MockResponse submitted(String id){return json(mode.equals("cellular")?Json.obj("code","cellular_sms_submitted","message_id",id,"references",new JSONArray().put(7)):Json.obj("accepted",true,"code","sent","operation_id",id,"message_id",id));}
    JSONObject received(int i,String line,String peer){return Json.obj("schema_version",1,"event_id","fixture-received-"+i,"line_id",line,"provider_id","fixture-provider","process_generation","fixture-process","transport",mode,"kind","received","realtime",true,"sender",peer,"body","fixture message "+i,"observed_at","2026-09-22T00:00:00Z","received_at","2026-09-22T00:00:00Z");}
    void appendReceived(int count){for(int i=0;i<count;i++)events.add(received(events.size()+1,lineID,"+15550100123"));publish();}
    void appendPartial()throws Exception{JSONObject first=original.get(firstID);events.add(Json.obj("schema_version",1,"event_id","fixture-submitted-part","line_id",lineID,"provider_id","fixture-provider","process_generation","fixture-process","transport",mode,"kind","submitted","message_id",firstID,"part",1,"recipient",first.getString("recipient"),"call_id","fixture-sip-message","state","submitted","observed_at","2026-09-22T00:00:00Z","received_at","2026-09-22T00:00:00Z"));publish();}
    synchronized void publish(){if(observer==null)return;JSONArray recent=new JSONArray();for(int i=Math.max(0,events.size()-50);i<events.size();i++)recent.put(events.get(i));observer.send(Json.obj("type","mobile.snapshot","schema_version",1,"sequence",++sequence,"data",Json.obj("lines",new JSONArray().put(line),"incoming_lines",new JSONArray(),"cellular_calls",new JSONArray(),"messages",recent)).toString());}
    private synchronized void heartbeat(){if(observer!=null)observer.send(Json.obj("type","mobile.heartbeat","schema_version",1,"sequence",++sequence).toString());}
    void check(){if(failure.get()!=null)throw new AssertionError("SMS fixture protocol failed",failure.get());}
    private static MockResponse json(JSONObject body){return new MockResponse().setHeader("Content-Type","application/json").setBody(body.toString());}
    public void close()throws Exception{heartbeat.shutdownNow();if(oldSyncGate!=null)oldSyncGate.countDown();if(historyGate!=null)historyGate.countDown();if(observer!=null)observer.close(1000,"fixture complete");gateway.close();}
}
