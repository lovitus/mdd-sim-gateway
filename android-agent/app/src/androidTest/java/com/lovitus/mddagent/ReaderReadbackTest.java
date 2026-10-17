package com.lovitus.mddagent;

import androidx.test.ext.junit.runners.AndroidJUnit4;
import androidx.test.platform.app.InstrumentationRegistry;
import android.os.SystemClock;
import okhttp3.*;
import okhttp3.mockwebserver.*;
import okhttp3.tls.*;
import org.json.*;
import org.junit.Test;
import org.junit.runner.RunWith;
import java.io.*;
import java.lang.reflect.*;
import java.nio.charset.StandardCharsets;
import java.util.*;
import java.util.concurrent.*;
import java.util.concurrent.atomic.*;
import static org.junit.Assert.*;

@RunWith(AndroidJUnit4.class)
public class ReaderReadbackTest {
    static final String READER="Synthetic USIM reader", CARD="89012345678901234567", PROCESS="android-test-process", SESSION="test-insertion-1";
    static JSONObject request(){return Json.obj("scope","sim_identity","operation_id","readback-operation-1","process_generation",PROCESS,"reader_name",READER,"card_id",CARD,"sim_session_generation",SESSION);}
    static Field field(Class<?> type,String name)throws Exception{Field f=type.getDeclaredField(name);f.setAccessible(true);return f;}
    static void attach(ReaderHub hub,IdentityCard card)throws Exception{
        Class<?> type=Class.forName(ReaderHub.class.getName()+"$Entry");Constructor<?> constructor=type.getDeclaredConstructor(String.class,SimProtocol.Card.class);constructor.setAccessible(true);
        Object entry=constructor.newInstance(READER,card);field(type,"id").set(entry,CARD);field(type,"generation").set(entry,SESSION);
        field(type,"sim").set(entry,Json.obj("identity_state","ready","imsi","001010123456789","mcc","001","mnc","01"));
        synchronized(hub){((Map<String,Object>)field(ReaderHub.class,"entries").get(hub)).put(READER,entry);}
    }
    static ReaderHub hub(){return new ReaderHub(InstrumentationRegistry.getInstrumentation().getTargetContext());}
    static Object normalized(Object value)throws Exception{
        if(value instanceof JSONObject){Map<String,Object> out=new TreeMap<>();JSONObject object=(JSONObject)value;for(Iterator<String> i=object.keys();i.hasNext();){String key=i.next();out.put(key,normalized(object.get(key)));}return out;}
        if(value instanceof JSONArray){List<Object> out=new ArrayList<>();JSONArray array=(JSONArray)value;for(int i=0;i<array.length();i++)out.add(normalized(array.get(i)));return out;}
        return value;
    }
    static final class Fixture implements AutoCloseable {
        final MockWebServer server=new MockWebServer();final AgentService service=new AgentService();final ReaderHub hub=hub();
        final IdentityCard card=new IdentityCard();final LinkedBlockingQueue<JSONObject> frames=new LinkedBlockingQueue<>();
        final AtomicReference<WebSocket> peer=new AtomicReference<>();final GatewayApi api;final ExecutorService readerIO;
        Fixture()throws Exception{
            HeldCertificate cert=new HeldCertificate.Builder().addSubjectAlternativeName("localhost").build();
            server.useHttps(new HandshakeCertificates.Builder().heldCertificate(cert).build().sslSocketFactory(),false);
            server.enqueue(new MockResponse().setHeader("X-MDD-Agent-Features","reader-readback-v1,reader-sim-identity-v1").withWebSocketUpgrade(new WebSocketListener(){
                public void onMessage(WebSocket ws,String text){try{JSONObject value=new JSONObject(text);frames.add(value);peer.set(ws);if(value.optString("kind").equals("hello"))ws.send("{\"kind\":\"hello_ack\"}");}catch(Exception failure){throw new AssertionError(failure);}}
            }));server.start();api=new GatewayApi(new Endpoint(server.url("/").toString(),Json.sha(cert.certificate().getEncoded())),"admin-session","csrf");
            for(Object[] pair:new Object[][]{{"api",api},{"hub",hub},{"config",Json.obj("agent_id","test-reader","agent_token","reader-token")},{"available",true},{"sharing",true},{"process",PROCESS}})field(AgentService.class,(String)pair[0]).set(service,pair[1]);
            readerIO=(ExecutorService)field(AgentService.class,"readerIO").get(service);
            Method start=AgentService.class.getDeclaredMethod("startReaderLink");start.setAccessible(true);start.invoke(service);
            next("hello");next("health");drain();attach(hub,card);frames.clear();
        }
        void drain()throws Exception{service.loop.submit(()->{}).get(5,TimeUnit.SECONDS);readerIO.submit(()->{}).get(5,TimeUnit.SECONDS);InstrumentationRegistry.getInstrumentation().waitForIdleSync();service.loop.submit(()->{}).get(5,TimeUnit.SECONDS);}
        JSONObject next(String kind)throws Exception{
            long until=System.nanoTime()+TimeUnit.SECONDS.toNanos(5);
            while(System.nanoTime()<until){JSONObject frame=frames.poll(until-System.nanoTime(),TimeUnit.NANOSECONDS);if(frame==null)break;if(kind.equals(frame.optString("kind")))return frame;}
            throw new AssertionError("ANDROID_READBACK_WIRE: no "+kind+" response");
        }
        void send(JSONObject request){peer.get().send(Json.obj("kind","reader_readback_request","request_id","readback-wire-1","reader_readback_request",request).toString());}
        public void close()throws Exception{field(AgentService.class,"destroyed").set(service,true);Link link=(Link)field(AgentService.class,"agent").get(service);if(link!=null)link.close();readerIO.shutdownNow();readerIO.awaitTermination(5,TimeUnit.SECONDS);hub.close();service.loop.shutdownNow();service.io.shutdownNow();service.controlIO.shutdownNow();api.close();server.close();}
    }
    @Test public void serviceReadbackUsesTypedWireAndRefreshesExistingTopology()throws Exception{
        try(Fixture f=new Fixture()){
            RecordedRequest upgrade=f.server.takeRequest(1,TimeUnit.SECONDS);
            assertEquals("reader-readback-v1,reader-sim-identity-v1",upgrade.getHeader("X-MDD-Agent-Capabilities"));assertNull(upgrade.getHeader("Cookie"));
            f.send(request());JSONObject response=f.next("reader_readback_response");
            try(InputStream in=InstrumentationRegistry.getInstrumentation().getContext().getAssets().open("sim-identity-readback.json")){
                ByteArrayOutputStream bytes=new ByteArrayOutputStream();byte[] chunk=new byte[1024];for(int n;(n=in.read(chunk))!=-1;)bytes.write(chunk,0,n);
                assertEquals(normalized(new JSONObject(bytes.toString("UTF-8"))),normalized(response));
            }
            f.drain();JSONObject reader=f.service.readers().getJSONArray("readers").getJSONObject(0);
            assertEquals(SESSION,reader.getString("session_generation"));assertEquals("+12345",reader.getJSONObject("sim").getString("smsc"));assertTrue(f.service.lastReaderAt>0);
            assertEquals(3,f.card.iccidReads);assertEquals(0,f.card.closeCount);
            JSONObject aka=Json.obj("operation_id","readback-operation-1","session_generation",SESSION,"card_id",CARD,"device_kind","reader","application","usim","rand","AAAAAAAAAAAAAAAAAAAAAA==","autn","AAAAAAAAAAAAAAAAAAAAAA==");
            String wire=Json.obj("kind","aka_request","request_id","aka-wire","aka_request",aka).toString();
            f.peer.get().send(wire);JSONObject first=f.next("aka_response");assertEquals("2wA=",first.getJSONObject("aka_response").getString("body"));int commands=f.card.commands.size();
            f.peer.get().send(wire);assertEquals(normalized(first),normalized(f.next("aka_response")));assertEquals(commands,f.card.commands.size());
        }
    }
    @Test public void rejectsNonSIMScopeAndStaleIdentitiesBeforeAnyCardIO()throws Exception{
        try(ReaderHub hub=hub()){
            IdentityCard card=new IdentityCard();attach(hub,card);
            for(String change:new String[]{"scope","absent-scope","operation_id","process_generation","reader_name","card_id","sim_session_generation","unexpected"}){
                JSONObject q=request();if(change.equals("absent-scope"))q.remove("scope");else q.put(change,change.equals("scope")?"":change.equals("operation_id")?"invalid id":"different");
                JSONObject result=hub.readback(q,PROCESS,SystemClock.elapsedRealtime()+15000,()->true);
                assertEquals(change,"failed",result.getString("state"));assertFalse(result.has("reader"));
            }
            assertTrue(card.commands.isEmpty());
        }
    }
    @Test public void interruptedAndReplacedReadbacksNeverCommitCachedFacts()throws Exception{
        for(String fault:new String[]{"cancelled","deadline","connection-lost","card-changed","transport"})try(ReaderHub hub=hub()){
            IdentityCard card=new IdentityCard();attach(hub,card);AtomicBoolean current=new AtomicBoolean(!fault.equals("cancelled"));
            long deadline=SystemClock.elapsedRealtime()+(fault.equals("deadline")?-1:15000);
            if(fault.equals("card-changed"))card.changeAt=3;
            card.beforeTransfer=()->{if(card.commands.size()==7){if(fault.equals("connection-lost"))current.set(false);if(fault.equals("transport"))throw new IllegalStateException("native transport failed");}};
            JSONObject result=hub.readback(request(),PROCESS,deadline,current::get);
            assertEquals(fault,"unknown",result.getString("state"));assertFalse(result.has("reader"));
            assertFalse(hub.topology().getJSONArray("readers").getJSONObject(0).getJSONObject("sim").has("smsc"));assertEquals(0,card.closeCount);
            if(fault.equals("cancelled")||fault.equals("deadline"))assertTrue(card.commands.isEmpty());
        }
    }
    @Test public void queuedServiceWorkAfterSharingStopsDoesNoIOOrPublication()throws Exception{
        try(Fixture f=new Fixture()){
            CountDownLatch occupied=new CountDownLatch(1),release=new CountDownLatch(1);
            f.readerIO.execute(()->{occupied.countDown();try{release.await(5,TimeUnit.SECONDS);}catch(InterruptedException e){Thread.currentThread().interrupt();}});assertTrue(occupied.await(2,TimeUnit.SECONDS));
            Link link=(Link)field(AgentService.class,"agent").get(f.service);
            Link.Events listener=(Link.Events)field(Link.class,"events").get(link);
            // Deliver at the actual Service admission boundary while reader I/O is
            // occupied; the separate WSS test covers decoding and wire delivery.
            f.service.loop.submit(()->listener.message(Json.obj("kind","reader_readback_request","request_id","readback-wire-1","reader_readback_request",request()))).get(2,TimeUnit.SECONDS);
            field(AgentService.class,"sharing").set(f.service,false);release.countDown();f.drain();
            assertTrue(f.card.commands.isEmpty());assertFalse(f.service.readers().toString().contains("+12345"));
        }
    }
}
