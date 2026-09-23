package com.lovitus.mddagent;

import android.content.Context;
import okhttp3.*;
import okhttp3.mockwebserver.*;
import okhttp3.tls.*;
import okio.ByteString;
import org.json.*;
import java.util.concurrent.*;
import java.util.concurrent.atomic.*;
import static org.junit.Assert.*;

/** Test-only peer using the Core lease, media and exact recovery wire contracts. */
final class CallGatewayFixture implements AutoCloseable {
    final String mode,lineID="fixture-line-129",card="8944100000000000129",session="fixture-media-session";
    final MockWebServer gateway=new MockWebServer();
    final JSONObject line;volatile JSONObject incoming;
    final ConfigStore store;
    final AtomicInteger leases=new AtomicInteger(),starts=new AtomicInteger(),ends=new AtomicInteger(),statuses=new AtomicInteger(),deletes=new AtomicInteger(),dtmf=new AtomicInteger(),rejects=new AtomicInteger(),pcm=new AtomicInteger();
    final AtomicReference<Throwable> failure=new AtomicReference<>();
    final CountDownLatch connected=new CountDownLatch(1),started=new CountDownLatch(1),ended=new CountDownLatch(1),toneReceived=new CountDownLatch(1),rejected=new CountDownLatch(1),queried=new CountDownLatch(1),canaryObserved=new CountDownLatch(1),mediaClosed=new CountDownLatch(1);
    final ScheduledExecutorService clock=Executors.newSingleThreadScheduledExecutor();
    volatile JSONObject lease;
    volatile WebSocket observer,media;
    volatile boolean rejectStart;volatile String terminalOutcome="ended";
    volatile boolean incomingVisible,terminal,confirmEnd=true,wrongIdentity,holdReady;
    volatile CountDownLatch deleteGate;
    final CountDownLatch deleteRequested=new CountDownLatch(1);
    private final AtomicLong sequence=new AtomicLong();
    private final boolean inbound;
    final AtomicInteger mediaConnections=new AtomicInteger(),mediaRejected=new AtomicInteger(),resumes=new AtomicInteger(),resumedPCM=new AtomicInteger(),guardAttempts=new AtomicInteger();
    final CountDownLatch resumedAudio=new CountDownLatch(1),guardUnknown=new CountDownLatch(1);
    volatile long rejectMediaUntil,mediaHeaderDelayMS;
    final AtomicBoolean rejectNextMedia=new AtomicBoolean();
    volatile boolean requireMutedResume;
    private long mediaEpoch;
    private String resumeTicket="",mediaChallenge="";

    CallGatewayFixture(Context context,String mode,boolean inbound)throws Exception{
        assertTrue("Fixtures must never touch a user app",context.getPackageName().endsWith(".qa"));
        this.mode=mode;this.inbound=inbound;incomingVisible=inbound;store=new ConfigStore(context);
        incoming=mode.equals("cellular")?Json.obj("incoming_event_id","fixture-cell-incoming","line_id",lineID,"card_id",card,"sim_session_generation","fixture-card-generation","native_call_index",2,"occurrence",7,"number","+15550100129","actionable",true,"state","ringing_in"):
            Json.obj("call_id","fixture-vowifi-incoming","caller","+15550100129","callee","+15550100000","received_at","2026-09-22T00:00:00Z");
        line=Json.obj("id",lineID,"name","Fixture remote line 129","number","+15550100129","card_id",card,"enabled",true,"ims","ready","operations",Json.obj(mode+"_call",Json.obj("ready",true)));
        HeldCertificate certificate=new HeldCertificate.Builder().addSubjectAlternativeName("localhost").build();
        gateway.useHttps(new HandshakeCertificates.Builder().heldCertificate(certificate).build().sslSocketFactory(),false);
        gateway.setDispatcher(new okhttp3.mockwebserver.Dispatcher(){public MockResponse dispatch(RecordedRequest request){try{return handle(request);}catch(Throwable error){failure.compareAndSet(null,error);return new MockResponse().setResponseCode(500).setBody("{\"code\":\"fixture_contract_mismatch\"}");}}});
        gateway.start();store.clear();store.save(Json.obj("server",gateway.url("/").toString().replaceAll("/$",""),"pin",Json.sha(certificate.certificate().getEncoded()),"token","fixture-session","csrf","fixture-csrf","available",true));
    }
    private MockResponse handle(RecordedRequest request)throws Exception{
        String path=request.getRequestUrl().encodedPath(),method=request.getMethod();
        assertEquals("Bearer fixture-session",request.getHeader("Authorization"));
        if(!method.equals("GET"))assertEquals("fixture-csrf",request.getHeader("X-MDD-CSRF-Token"));
        if(path.equals("/api/auth/status"))return json(Json.obj("authenticated",true,"username","fixture-owner","token","fixture-session","csrf","fixture-csrf"));
        if(path.equals("/v1/messages"))return json(Json.obj("messages",new JSONArray(),"cursor","fixture-stream:0","initial",true,"more",false));
        if(path.equals("/v1/mobile/lines/"+lineID))return json(line);
        if(path.equals("/v1/mobile/ws"))return new MockResponse().withWebSocketUpgrade(new WebSocketListener(){public void onOpen(WebSocket socket,Response response){observer=socket;publish();connected.countDown();}});
        String leasePath=mode.equals("cellular")?"/v1/cellular/media/leases":"/v1/media/leases";
        if(path.equals(leasePath)){
            JSONObject body=new JSONObject(request.getBody().readUtf8());
            if(method.equals("DELETE")){assertEquals(session,body.getString("session_id"));deletes.incrementAndGet();deleteRequested.countDown();CountDownLatch gate=deleteGate;if(gate!=null)assertTrue("Test did not release DELETE",gate.await(15,TimeUnit.SECONDS));return new MockResponse().setResponseCode(204);}
            assertEquals("POST",method);assertEquals(lineID,body.getString("line_id"));assertFalse(body.getString("recovery_key").isEmpty());
            if(mode.equals("cellular")){assertEquals(card,body.getString("expected_card_id"));if(inbound)assertIncoming(body);}
            lease=body;assertEquals(1,leases.incrementAndGet());
            return new MockResponse().setResponseCode(201).setBody(Json.obj("session_id",session,"ws_path","/api/"+(mode.equals("cellular")?"cellular-browser-media/":"browser-media/")+session+"/ws","expires_at","2099-01-01T00:00:00Z").toString());
        }
        if(path.startsWith("/api/browser-media/")||path.startsWith("/api/cellular-browser-media/")){
            if(rejectNextMedia.getAndSet(false)||android.os.SystemClock.elapsedRealtime()<rejectMediaUntil){mediaRejected.incrementAndGet();return new MockResponse().setResponseCode(503).setBody("{\"code\":\"fixture_transport_unavailable\"}");}
            return mediaSocket().setHeadersDelay(mediaHeaderDelayMS,TimeUnit.MILLISECONDS);
        }
        String prefix="/v1/lines/"+lineID+"/"+mode+"/calls/";
        if(path.equals(prefix+(inbound?(mode.equals("cellular")?"answer":"incoming/answer"):"start"))){
            JSONObject body=new JSONObject(request.getBody().readUtf8()),saved=store.load().getJSONObject("pending_call");
            assertEquals(lease.getString("operation_id"),body.getString("operation_id"));assertEquals(lease.getString("call_id"),saved.getString("call_id"));
            assertEquals("START_MAY_HAVE_RUN",saved.getString("phase"));assertEquals(session,saved.getString("session_id"));assertEquals(lease.getString("recovery_key"),saved.getString("recovery_key"));
            if(mode.equals("cellular")){assertEquals(session,body.getString("session_id"));assertEquals(card,body.getString("expected_card_id"));if(inbound)assertIncoming(body);}
            else{assertEquals(lease.getString("call_id"),body.getString("call_id"));assertEquals(session,body.getString("media_session_id"));if(!inbound)assertEquals(card,body.getString("expected_card_id"));}
            if(!inbound)assertEquals("+15550100999",body.getString("callee"));else assertEquals(incoming.getString(mode.equals("cellular")?"incoming_event_id":"call_id"),lease.getString("call_id"));
            starts.incrementAndGet();incomingVisible=false;publish();started.countDown();
            if(rejectStart){terminalOutcome="rejected";terminal=true;return new MockResponse().setResponseCode(422).setBody("{\"code\":\"call_rejected\"}");}
            if(mode.equals("cellular"))return json(inbound?Json.obj("code","cellular_incoming_answered","session_id",session,"incoming_event_id",incoming.getString("incoming_event_id")):Json.obj("code","cellular_call_started","session_id",session,"call_id",lease.getString("call_id"),"state","dialing"));
            return json(Json.obj("operation_id",body.getString("operation_id"),"accepted",true,"code","active","call_id",lease.getString("call_id")));
        }
        if(path.equals(prefix+"recovery")){
            JSONObject body=new JSONObject(request.getBody().readUtf8());assertEquals(lease.getString("call_id"),body.getString("call_id"));assertEquals(lease.getString("operation_id"),body.getString("operation_id"));assertEquals(lease.getString("recovery_key"),body.getString("recovery_key"));
            if(body.getString("action").equals("end")){assertEquals(store.load().getJSONObject("pending_call").getString("end_operation_id"),body.getString("end_operation_id"));ends.incrementAndGet();if(confirmEnd)terminal=true;ended.countDown();}
            else{assertEquals("status",body.getString("action"));statuses.incrementAndGet();queried.countDown();}
            if(terminal)return json(Json.obj("state","terminal","call_id",wrongIdentity?"another-call":lease.getString("call_id"),"operation_id",lease.getString("operation_id"),"session_id",session,"terminal_confirmed",true,"outcome",terminalOutcome,"terminal_at","2026-09-22T00:01:00Z"));
            return json(Json.obj("state","ending_unconfirmed","terminal_confirmed",false));
        }
        if(path.equals(prefix+"dtmf")){
            JSONObject body=new JSONObject(request.getBody().readUtf8());assertEquals("5",body.getString("signal"));assertFalse(body.getString("operation_id").isEmpty());assertEquals(mode.equals("cellular")?session:lease.getString("call_id"),body.getString(mode.equals("cellular")?"session_id":"call_id"));dtmf.incrementAndGet();toneReceived.countDown();return json(mode.equals("cellular")?Json.obj("code","cellular_dtmf_sent","session_id",session,"signal","5","state","active"):Json.obj("accepted",true,"code","dtmf_rtp","operation_id",body.getString("operation_id"),"call_id",lease.getString("call_id")));
        }
        if(path.equals(prefix+(mode.equals("cellular")?"reject":"incoming/reject"))){
            JSONObject body=new JSONObject(request.getBody().readUtf8());if(mode.equals("cellular")){assertIncoming(body);assertEquals(card,body.getString("expected_card_id"));}else assertEquals(incoming.getString("call_id"),body.getString("call_id"));
            rejects.incrementAndGet();incomingVisible=false;publish();rejected.countDown();return json(mode.equals("cellular")?Json.obj("code","cellular_incoming_rejected","incoming_event_id",incoming.getString("incoming_event_id")):Json.obj("accepted",true,"code","rejected","call_id",incoming.getString("call_id"),"operation_id",body.getString("operation_id")));
        }
        throw new AssertionError("Unexpected native call route: "+method+" "+path);
    }
    private void assertIncoming(JSONObject body)throws Exception{assertEquals(incoming.getString("incoming_event_id"),body.getString("incoming_event_id"));assertEquals(incoming.getString("sim_session_generation"),body.getString("sim_session_generation"));assertEquals(incoming.getInt("native_call_index"),body.getInt("native_call_index"));assertEquals(incoming.getLong("occurrence"),body.getLong("call_occurrence"));}
    // Same rotating resume identity and per-connection challenge as Core's
    // cellularmedia.claimBrowser and Provider browsermedia.handler.
    private synchronized JSONObject claim(JSONObject hello)throws Exception{
        assertEquals(session,hello.getString("session_id"));assertEquals(1,hello.getInt("version"));
        boolean resumed=hello.getString("type").equals("browser.media.resume");
        if(resumed){assertEquals(resumeTicket,hello.getString("resume_ticket"));assertEquals(mediaEpoch,hello.getLong("connection_epoch"));assertTrue(mediaEpoch>0);resumes.incrementAndGet();}
        else{assertEquals("browser.media.hello",hello.getString("type"));assertEquals(0,mediaEpoch);assertEquals(lease.getString("call_id"),hello.getString("ticket"));}
        mediaEpoch++;resumeTicket=Json.id();mediaChallenge=Json.id();
        return Json.obj("type",resumed?"browser.media.resumed":"browser.media.claimed","version",1,"challenge",mediaChallenge,"resume_ticket",resumeTicket,"connection_epoch",mediaEpoch);
    }
    private MockResponse mediaSocket(){return new MockResponse().withWebSocketUpgrade(new WebSocketListener(){
        private volatile ScheduledFuture<?> pump;private JSONObject grant;private long captureBase=-1,playedBase=-1,callbackBase=-1;private int frames;
        public void onOpen(WebSocket socket,Response response){media=socket;mediaConnections.incrementAndGet();}
        public void onMessage(WebSocket socket,ByteString frame){try{
            if(socket!=media)return;assertNotNull("PCM before claim",grant);assertEquals(320,frame.size());frames++;pcm.incrementAndGet();
            if(grant.optLong("connection_epoch")>1){resumedPCM.incrementAndGet();if(requireMutedResume)for(byte value:frame.toByteArray())assertEquals("Mute must survive media resume",0,value);}
        }catch(Throwable error){failure.compareAndSet(null,error);socket.close(1008,"fixture PCM mismatch");}}
        public void onMessage(WebSocket socket,String text){try{
            if(socket!=media)return;JSONObject message=new JSONObject(text);
            if(grant==null){
                grant=claim(message);socket.send(grant.toString());socket.send(Json.obj("type","browser.media.started","version",1,"purpose",grant.getLong("connection_epoch")==1?"canary":"call").toString());
                pump=clock.scheduleAtFixedRate(()->socket.send(ByteString.of(tone())),0,20,TimeUnit.MILLISECONDS);return;
            }
            assertEquals("browser.media.evidence",message.getString("type"));assertEquals(grant.getString("challenge"),message.getString("challenge"));
            long captured=message.getLong("capture_callbacks"),played=message.getLong("played_frames"),callbacks=message.getLong("playback_callbacks");
            if(grant.getLong("connection_epoch")==1){if(captured>0&&played>1&&frames>1){canaryObserved.countDown();if(!holdReady)ready();}}
            else{
                if(captureBase<0){captureBase=captured;playedBase=played;callbackBase=callbacks;}
                if(captured>captureBase&&played>playedBase+1&&callbacks>callbackBase&&frames>1)resumedAudio.countDown();
            }
        }catch(Throwable error){failure.compareAndSet(null,error);socket.close(1008,"fixture protocol mismatch");}}
        private void stopped(){if(pump!=null)pump.cancel(false);mediaClosed.countDown();}
        public void onClosing(WebSocket socket,int code,String reason){stopped();socket.close(code,null);}
        public void onClosed(WebSocket socket,int code,String reason){stopped();}
        public void onFailure(WebSocket socket,Throwable error,Response response){stopped();}
    });}
    void interruptMedia(boolean prolonged){
        assertNotNull(media);mediaHeaderDelayMS=650;rejectNextMedia.set(!prolonged);rejectMediaUntil=prolonged?android.os.SystemClock.elapsedRealtime()+20000:0;
        if(prolonged)clock.schedule(()->{guardAttempts.incrementAndGet();guardUnknown.countDown();},10,TimeUnit.SECONDS);
        media.close(1001,"fixture temporary transport loss");
    }
    void ready(){media.send(Json.obj("type","browser.media.ready","version",1,"ready",true).toString());}
    void nextIncoming()throws Exception{JSONObject next=new JSONObject(incoming.toString());next.put(mode.equals("cellular")?"incoming_event_id":"call_id","fixture-next-incoming");if(mode.equals("cellular"))next.put("occurrence",8);incoming=next;incomingVisible=true;publish();}
    void publish(){WebSocket socket=observer;if(socket==null)return;try{
        JSONArray lines=new JSONArray(),incomingLines=new JSONArray(),cells=new JSONArray();
        if(inbound){for(int i=0;i<128;i++)lines.put(Json.obj("id","other-"+i,"name","Other line "+i,"card_id","890000"+i,"enabled",true,"ims","unknown","operations",new JSONObject()));}
        else lines.put(line);
        if(incomingVisible){JSONObject row=new JSONObject(line.toString());row.put(mode.equals("cellular")?"cellular_incoming":"incoming",incoming);incomingLines.put(row);if(mode.equals("cellular"))cells.put(incoming);}
        socket.send(Json.obj("type","mobile.snapshot","schema_version",1,"sequence",sequence.incrementAndGet(),"data",Json.obj("lines",lines,"incoming_lines",incomingLines,"cellular_calls",cells,"messages",new JSONArray(),"incomplete",inbound)).toString());
    }catch(Exception e){failure.compareAndSet(null,e);}}
    void remoteEnd(boolean confirmed){terminal=confirmed;WebSocket socket=media;if(socket!=null)socket.close(1000,"fixture remote media ended");}
    void check(){Throwable error=failure.get();if(error!=null)throw new AssertionError("Native wire contract failed",error);}
    static byte[] tone(){byte[] pcm=new byte[320];for(int i=0;i<160;i++){short value=(short)(200*Math.sin(2*Math.PI*440*i/8000));pcm[2*i]=(byte)value;pcm[2*i+1]=(byte)(value>>8);}return pcm;}
    private static MockResponse json(JSONObject value){return new MockResponse().setHeader("Content-Type","application/json").setBody(value.toString());}
    public void close()throws Exception{clock.shutdownNow();if(media!=null)media.close(1000,"fixture complete");if(observer!=null)observer.close(1000,"fixture complete");gateway.close();}
}
