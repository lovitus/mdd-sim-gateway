package com.lovitus.mddagent;
import android.content.*;
import android.os.*;
import androidx.test.core.app.*;
import androidx.test.platform.app.InstrumentationRegistry;
import androidx.test.ext.junit.runners.AndroidJUnit4;
import java.lang.reflect.Field;
import java.util.concurrent.*;
import java.util.concurrent.atomic.AtomicReference;
import okhttp3.*;
import okhttp3.mockwebserver.*;
import okhttp3.tls.*;
import org.json.*;
import org.junit.Test;
import org.junit.runner.RunWith;
import static org.junit.Assert.*;

@RunWith(AndroidJUnit4.class)
public class Pr8ReaderPublicationTest {
    static void field(AgentService owner,String name,Object value)throws Exception{Field f=AgentService.class.getDeclaredField(name);f.setAccessible(true);f.set(owner,value);}
    @Test public void queuedOldDiscoveryCannotPublishOnNewSharingOwner()throws Exception{
        Context context=ApplicationProvider.getApplicationContext();assertTrue(context.getPackageName().endsWith(".qa"));ConfigStore store=new ConfigStore(context);store.clear();
        CountDownLatch bound=new CountDownLatch(1),online=new CountDownLatch(1);AtomicReference<AgentService> current=new AtomicReference<>();
        ServiceConnection binding=new ServiceConnection(){public void onServiceConnected(ComponentName n,IBinder b){current.set(((AgentService.LocalBinder)b).service());bound.countDown();}public void onServiceDisconnected(ComponentName n){}};
        assertTrue(context.bindService(new Intent(context,AgentService.class),binding,Context.BIND_AUTO_CREATE));assertTrue(bound.await(5,TimeUnit.SECONDS));
        AgentService service=current.get();ReaderHub old=new ReaderHub(context),replacement=new ReaderHub(context);
        try(MockWebServer server=new MockWebServer()){
            HeldCertificate cert=new HeldCertificate.Builder().addSubjectAlternativeName("localhost").build();server.useHttps(new HandshakeCertificates.Builder().heldCertificate(cert).build().sslSocketFactory(),false);
            LinkedBlockingQueue<JSONObject> received=new LinkedBlockingQueue<>();
            server.enqueue(new MockResponse().withWebSocketUpgrade(new WebSocketListener(){public void onMessage(WebSocket ws,String text){JSONObject q;try{q=new JSONObject(text);if(q.optString("kind").equals("hello"))ws.send("{\"kind\":\"hello_ack\"}");else received.add(q);}catch(Exception e){throw new AssertionError(e);}}}));server.start();
            GatewayApi api=new GatewayApi(new Endpoint(server.url("/").toString(),Json.sha(cert.certificate().getEncoded())),"","");
            Link link=new Link(api,service.loop,"/v1/agent/ws","reader","credential","generation",new Link.Events(){public void message(JSONObject o){}public void state(int label,boolean yes){if(yes)online.countDown();}});
            try{
                link.connect();assertTrue(online.await(5,TimeUnit.SECONDS));ConfigStore.intent(()->{}).get(5,TimeUnit.SECONDS);InstrumentationRegistry.getInstrumentation().waitForIdleSync();
                JSONObject oldFacts=Json.obj("reader_condition","ready","readers",new JSONArray().put(Json.obj("card_id","old-card","session_generation","old-generation")),"modem_condition","disabled");
                JSONObject newFacts=Json.obj("reader_condition","ready","readers",new JSONArray().put(Json.obj("card_id","new-card","session_generation","new-generation")),"modem_condition","disabled");
                ReaderObservation late=new ReaderObservation(old,1,SystemClock.elapsedRealtime(),oldFacts);
                // Hardware work is already finished. Publication is still queued;
                // switch sharing owners on the same looper before releasing it.
                InstrumentationRegistry.getInstrumentation().runOnMainSync(()->{try{
                    field(service,"available",true);field(service,"sharing",true);field(service,"hub",replacement);field(service,"readerEpoch",2L);field(service,"agent",link);
                    service.publishReaderObservation(late,UiText.EMPTY);
                    assertEquals("recovering",service.readers().getString("reader_condition"));
                    service.publishReaderObservation(new ReaderObservation(replacement,2,SystemClock.elapsedRealtime(),newFacts),UiText.EMPTY);
                    assertEquals("new-card",service.readers().getJSONArray("readers").getJSONObject(0).getString("card_id"));
                }catch(Exception e){throw new AssertionError(e);}});
                JSONObject health=received.poll(5,TimeUnit.SECONDS);assertNotNull(health);assertFalse(health.toString().contains("old-card"));assertTrue(health.toString().contains("new-card"));
                service.loop.submit(()->{}).get(5,TimeUnit.SECONDS);assertNull(received.poll(200,TimeUnit.MILLISECONDS));
            }finally{link.close();service.loop.submit(()->{}).get(5,TimeUnit.SECONDS);api.close();}
        }finally{old.close();context.unbindService(binding);context.stopService(new Intent(context,AgentService.class));InstrumentationRegistry.getInstrumentation().waitForIdleSync();ConfigStore.intent(()->{}).get(5,TimeUnit.SECONDS);store.clear();}
    }
}
