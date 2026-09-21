package com.lovitus.mddagent;

import androidx.test.ext.junit.runners.AndroidJUnit4;
import okhttp3.*;
import okhttp3.mockwebserver.*;
import okhttp3.tls.*;
import org.json.JSONObject;
import org.junit.Test;
import org.junit.runner.RunWith;
import java.util.concurrent.*;
import java.util.concurrent.atomic.AtomicReference;
import static org.junit.Assert.*;

@RunWith(AndroidJUnit4.class)
public class LinkTest {
    @Test public void observerReconnectRetainsSessionAndRevocationIsTerminal() throws Exception {
        ScheduledExecutorService loop=Executors.newSingleThreadScheduledExecutor();
        HeldCertificate cert=new HeldCertificate.Builder().addSubjectAlternativeName("localhost").build();
        try(MockWebServer server=new MockWebServer()) {
            server.useHttps(new HandshakeCertificates.Builder().heldCertificate(cert).build().sslSocketFactory(),false);
            CountDownLatch first=new CountDownLatch(1),second=new CountDownLatch(1),revoked=new CountDownLatch(1);
            AtomicReference<WebSocket> peer=new AtomicReference<>();
            for(int i=0;i<2;i++) server.enqueue(new MockResponse().withWebSocketUpgrade(new WebSocketListener(){
                @Override public void onOpen(WebSocket ws,Response r){peer.set(ws);ws.send(Json.obj("type","mobile.snapshot","schema_version",1,"sequence",1,"data",Json.obj("lines",new org.json.JSONArray())).toString());}
            }));
            server.start();
            GatewayApi api=new GatewayApi(new Endpoint(server.url("/").toString(),Json.sha(cert.certificate().getEncoded())),"same-session","same-csrf");
            Link link=new Link(api,loop,"/v1/mobile/ws","","","process",new Link.Events(){
                public void state(String s,boolean online){if(s.contains("required"))revoked.countDown();}
                public void message(JSONObject o){if(first.getCount()>0)first.countDown();else second.countDown();}
            });
            try {
                link.connect();assertTrue(first.await(5,TimeUnit.SECONDS));link.networkChanged();assertTrue(second.await(5,TimeUnit.SECONDS));
                for(int i=0;i<2;i++){RecordedRequest q=server.takeRequest(1,TimeUnit.SECONDS);assertNotNull(q);assertEquals("GET",q.getMethod());assertEquals("/v1/mobile/ws",q.getPath());assertEquals("mdd_session=same-session",q.getHeader("Cookie"));}
                peer.get().close(4401,"revoked");assertTrue(revoked.await(5,TimeUnit.SECONDS));link.networkChanged();
                assertNull("revocation must not trigger a new login or reconnect",server.takeRequest(800,TimeUnit.MILLISECONDS));
            } finally {link.close();loop.submit(()->{}).get(2,TimeUnit.SECONDS);api.close();}
        } finally {loop.shutdownNow();}
    }

    @Test public void readerPublishesOneTopologyThenSequenceOnlyHealth() throws Exception {
        ScheduledExecutorService loop=Executors.newSingleThreadScheduledExecutor();
        HeldCertificate cert=new HeldCertificate.Builder().addSubjectAlternativeName("localhost").build();
        try(MockWebServer server=new MockWebServer()) {
            server.useHttps(new HandshakeCertificates.Builder().heldCertificate(cert).build().sslSocketFactory(),false);
            LinkedBlockingQueue<JSONObject> frames=new LinkedBlockingQueue<>();CountDownLatch admitted=new CountDownLatch(1);
            server.enqueue(new MockResponse().withWebSocketUpgrade(new WebSocketListener(){
                @Override public void onMessage(WebSocket ws,String text){try{JSONObject q=new JSONObject(text);frames.add(q);if(q.optString("kind").equals("hello"))ws.send("{\"kind\":\"hello_ack\"}");}catch(Exception e){throw new AssertionError(e);}}
            }));server.start();
            GatewayApi api=new GatewayApi(new Endpoint(server.url("/").toString(),Json.sha(cert.certificate().getEncoded())),"admin-session","csrf");
            Link link=new Link(api,loop,"/v1/agent/ws","reader-agent","scoped-reader-token","process",new Link.Events(){public void message(JSONObject o){}public void state(String s,boolean online){if(online)admitted.countDown();}});
            try {
                link.connect();assertTrue(admitted.await(5,TimeUnit.SECONDS));assertEquals("hello",frames.poll(1,TimeUnit.SECONDS).getString("kind"));
                JSONObject facts=Json.obj("reader_condition","ready","readers",new org.json.JSONArray(),"modem_condition","disabled");
                link.health(facts);link.health(facts);
                JSONObject first=frames.poll(2,TimeUnit.SECONDS).getJSONObject("health"),second=frames.poll(2,TimeUnit.SECONDS).getJSONObject("health");
                assertEquals(1,first.getLong("sequence"));assertEquals(2,second.getLong("sequence"));assertTrue(first.has("topology"));assertFalse(second.has("topology"));assertEquals(first.getString("topology_revision"),second.getString("topology_revision"));
                RecordedRequest request=server.takeRequest(1,TimeUnit.SECONDS);assertEquals("Bearer scoped-reader-token",request.getHeader("Authorization"));assertNull(request.getHeader("Cookie"));assertNull(request.getHeader("X-MDD-Session"));
                link.close();link.networkChanged();assertNull(server.takeRequest(800,TimeUnit.MILLISECONDS));
            } finally {link.close();loop.submit(()->{}).get(2,TimeUnit.SECONDS);api.close();}
        } finally {loop.shutdownNow();}
    }
}
