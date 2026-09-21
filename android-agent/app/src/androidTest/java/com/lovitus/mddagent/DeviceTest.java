package com.lovitus.mddagent;
import androidx.test.ext.junit.runners.AndroidJUnit4;
import androidx.test.core.app.*;
import androidx.test.platform.app.InstrumentationRegistry;
import android.content.*;
import org.junit.*;
import org.junit.runner.RunWith;
import static org.junit.Assert.*;
import java.util.concurrent.*;
import okhttp3.*;
import okhttp3.mockwebserver.*;
import okhttp3.tls.*;
import okio.ByteString;
import org.json.*;
@RunWith(AndroidJUnit4.class)
public class DeviceTest {
    @Test public void nativeSetupAndKeystoreRoundTrip()throws Exception{
        Context c=ApplicationProvider.getApplicationContext();ConfigStore store=new ConfigStore(c);store.clear();
        try(ActivityScenario<MainActivity> scene=ActivityScenario.launch(MainActivity.class)){
            scene.onActivity(a->{assertNotNull(a.findViewById(R.id.connect_gateway));});
            store.save(Json.obj("server","https://gateway.test","token","opaque-test","csrf","csrf"));assertEquals("opaque-test",new ConfigStore(c).load().getString("token"));
            String raw=c.getSharedPreferences("private_config",0).getString("sealed","");assertFalse(raw.contains("opaque-test"));assertFalse(raw.isEmpty());
        }finally{store.clear();}
    }
    @Test public void realAudioCallbacksGateCanaryAndExactResume()throws Exception{
        Context context=ApplicationProvider.getApplicationContext();new ConfigStore(context).clear();
        InstrumentationRegistry.getInstrumentation().getUiAutomation().executeShellCommand("pm grant "+context.getPackageName()+" android.permission.RECORD_AUDIO").close();
        HeldCertificate cert=new HeldCertificate.Builder().addSubjectAlternativeName("localhost").build();
        ScheduledExecutorService timer=Executors.newSingleThreadScheduledExecutor();CountDownLatch evidence=new CountDownLatch(1),resume=new CountDownLatch(1);java.util.concurrent.atomic.AtomicInteger handshakes=new java.util.concurrent.atomic.AtomicInteger();
        try(ActivityScenario<MainActivity> scene=ActivityScenario.launch(MainActivity.class);MockWebServer server=new MockWebServer()){
            server.useHttps(new HandshakeCertificates.Builder().heldCertificate(cert).build().sslSocketFactory(),false);
            for(int i=0;i<2;i++)server.enqueue(new MockResponse().withWebSocketUpgrade(new WebSocketListener(){
                public void onMessage(WebSocket ws,String text){try{JSONObject q=new JSONObject(text);String t=q.getString("type");if(t.equals("browser.media.hello")||t.equals("browser.media.resume")){int n=handshakes.incrementAndGet();if(n==2){assertEquals("same-ticket",q.getString("resume_ticket"));assertEquals(1,q.getLong("connection_epoch"));resume.countDown();}ws.send(Json.obj("type",n==1?"browser.media.claimed":"browser.media.resumed","challenge","test-challenge","resume_ticket","same-ticket","connection_epoch",n).toString());ws.send(Json.obj("type","browser.media.started","purpose",n==1?"canary":"call").toString());for(int f=0;f<8;f++)ws.send(ByteString.of(new byte[320]));}
                    else if(t.equals("browser.media.evidence")&&q.getLong("capture_callbacks")>0&&q.getLong("played_frames")>0){evidence.countDown();ws.send(Json.obj("type","browser.media.ready","ready",true).toString());}
                }catch(Exception e){throw new AssertionError(e);}}
            }));server.start();
            GatewayApi api=new GatewayApi(new Endpoint(server.url("/").toString(),Json.sha(cert.certificate().getEncoded())),"token","csrf");
            NativeAudio audio=new NativeAudio(context,api,timer,new NativeAudio.Events(){public void state(String s){}public void ended(String r){}});
            try{audio.prepare(Json.obj("session_id","lease","ws_path","/media"),"call").get(15,TimeUnit.SECONDS);assertTrue(evidence.await(1,TimeUnit.SECONDS));audio.markActive();audio.networkChanged();assertTrue(resume.await(5,TimeUnit.SECONDS));assertEquals(2,handshakes.get());audio.mute();assertTrue(audio.muted);assertNotNull(server.takeRequest(1,TimeUnit.SECONDS));assertNotNull(server.takeRequest(1,TimeUnit.SECONDS));assertNull(server.takeRequest(100,TimeUnit.MILLISECONDS));}
            finally{audio.close();api.close();}
        }finally{timer.shutdownNow();}
    }
}
