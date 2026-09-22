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
    @Test public void tlsTransportFailuresDoNotOverrideCertificateRejection(){
        assertFalse(Link.identityFailure(new javax.net.ssl.SSLException("transport closed")));
        assertFalse(Link.identityFailure(new javax.net.ssl.SSLHandshakeException("EOF before certificate")));
        for(Throwable identity:new Throwable[]{new java.security.cert.CertificateException("changed"),new java.security.cert.CertificateExpiredException(),new javax.net.ssl.SSLPeerUnverifiedException("hostname")}){
            assertTrue(Link.identityFailure(identity));
            assertTrue(Link.identityFailure(new java.io.IOException("wrapper",new javax.net.ssl.SSLException(identity))));
        }
    }
    @Test public void interruptedTlsHandshakeRecoversBothOriginalLinks()throws Exception{
        for(boolean reader:new boolean[]{false,true}){
            ScheduledExecutorService loop=Executors.newSingleThreadScheduledExecutor();
            HeldCertificate cert=new HeldCertificate.Builder().addSubjectAlternativeName("localhost").build();
            try(MockWebServer server=new MockWebServer()){
                server.useHttps(new HandshakeCertificates.Builder().heldCertificate(cert).build().sslSocketFactory(),false);
                server.enqueue(new MockResponse().setSocketPolicy(SocketPolicy.DISCONNECT_AT_START));
                CountDownLatch online=new CountDownLatch(1);LinkedBlockingQueue<JSONObject> hello=new LinkedBlockingQueue<>();
                java.util.concurrent.atomic.AtomicBoolean rejected=new java.util.concurrent.atomic.AtomicBoolean();
                server.enqueue(new MockResponse().withWebSocketUpgrade(new WebSocketListener(){
                    public void onOpen(WebSocket ws,Response response){if(!reader)ws.send(Json.obj("type","mobile.snapshot","schema_version",1,"data",new JSONObject()).toString());}
                    public void onMessage(WebSocket ws,String text){if(reader){try{hello.add(new JSONObject(text));ws.send("{\"kind\":\"hello_ack\"}");}catch(Exception failure){throw new AssertionError(failure);}}}
                }));server.start();
                GatewayApi api=new GatewayApi(new Endpoint(server.url("/").toString(),Json.sha(cert.certificate().getEncoded())),"original-session","original-csrf");
                String path=reader?"/v1/agent/ws":"/v1/mobile/ws";
                Link link=new Link(api,loop,path,reader?"original-agent":"",reader?"original-agent-token":"","original-process",new Link.Events(){
                    public void message(JSONObject o){}
                    public void state(int label,boolean connected){if(label==R.string.link_auth_tls)rejected.set(true);if(connected)online.countDown();}
                });
                try{
                    link.connect();assertTrue("TLS transport closure must reconnect without user action",online.await(10,TimeUnit.SECONDS));assertFalse(rejected.get());
                    RecordedRequest request=null;
                    for(int i=0;i<2;i++){RecordedRequest candidate=server.takeRequest(1,TimeUnit.SECONDS);if(candidate!=null&&"GET".equals(candidate.getMethod()))request=candidate;}
                    assertNotNull(request);assertEquals(path,request.getPath());assertEquals(api.endpoint.origin,request.getHeader("Origin"));
                    if(reader){assertEquals("Bearer original-agent-token",request.getHeader("Authorization"));assertEquals("original-agent",request.getHeader("X-MDD-Agent-ID"));assertNull(request.getHeader("Cookie"));JSONObject frame=hello.poll(1,TimeUnit.SECONDS);assertNotNull(frame);assertEquals("hello",frame.getString("kind"));assertEquals("original-process",frame.getJSONObject("hello").getString("process_generation"));}
                    else assertEquals("mdd_session=original-session",request.getHeader("Cookie"));
                    assertFalse(api.http.retryOnConnectionFailure());
                }finally{link.close();loop.submit(()->{}).get(2,TimeUnit.SECONDS);api.close();}
            }finally{loop.shutdownNow();}
        }
    }
    @Test public void changedCertificateRejectsBothLinksBeforeHttpAndDoesNotReconnect()throws Exception{
        for(boolean reader:new boolean[]{false,true}){
            ScheduledExecutorService loop=Executors.newSingleThreadScheduledExecutor();
            HeldCertificate expected=new HeldCertificate.Builder().addSubjectAlternativeName("localhost").build();
            HeldCertificate changed=new HeldCertificate.Builder().addSubjectAlternativeName("localhost").build();
            try(MockWebServer server=new MockWebServer()){
                server.useHttps(new HandshakeCertificates.Builder().heldCertificate(changed).build().sslSocketFactory(),false);server.start();
                CountDownLatch rejected=new CountDownLatch(1);java.util.concurrent.atomic.AtomicInteger attempts=new java.util.concurrent.atomic.AtomicInteger();
                GatewayApi api=new GatewayApi(new Endpoint(server.url("/").toString(),Json.sha(expected.certificate().getEncoded())),"private-session","csrf");
                Link link=new Link(api,loop,reader?"/v1/agent/ws":"/v1/mobile/ws",reader?"agent":"",reader?"private-agent-token":"","process",new Link.Events(){
                    public void message(JSONObject o){fail("Rejected peer must not publish frames");}
                    public void state(int label,boolean connected){if(label==R.string.link_connecting)attempts.incrementAndGet();if(label==R.string.link_auth_tls)rejected.countDown();assertFalse(connected);}
                });
                try{
                    link.connect();assertTrue(rejected.await(5,TimeUnit.SECONDS));link.networkChanged();loop.submit(()->{}).get(2,TimeUnit.SECONDS);
                    RecordedRequest handshake=server.takeRequest(800,TimeUnit.MILLISECONDS);if(handshake!=null){assertNull(handshake.getMethod());assertNull(handshake.getHeader("Authorization"));assertNull(handshake.getHeader("Cookie"));}
                    assertNull(server.takeRequest(800,TimeUnit.MILLISECONDS));assertEquals(1,attempts.get());
                }finally{link.close();loop.submit(()->{}).get(2,TimeUnit.SECONDS);api.close();}
            }finally{loop.shutdownNow();}
        }
    }
    @Test public void closingAfterExecutorShutdownStillClosesTheSocket()throws Exception{
        ScheduledExecutorService loop=Executors.newSingleThreadScheduledExecutor();
        HeldCertificate cert=new HeldCertificate.Builder().addSubjectAlternativeName("localhost").build();
        CountDownLatch online=new CountDownLatch(1),closed=new CountDownLatch(1);
        try(MockWebServer server=new MockWebServer()){
            server.useHttps(new HandshakeCertificates.Builder().heldCertificate(cert).build().sslSocketFactory(),false);
            server.enqueue(new MockResponse().withWebSocketUpgrade(new WebSocketListener(){
                public void onOpen(WebSocket socket,Response response){socket.send(Json.obj("type","mobile.snapshot","schema_version",1,"data",new JSONObject()).toString());}
                public void onFailure(WebSocket socket,Throwable failure,Response response){closed.countDown();}
                public void onClosed(WebSocket socket,int code,String reason){closed.countDown();}
            }));server.start();
            GatewayApi api=new GatewayApi(new Endpoint(server.url("/").toString(),Json.sha(cert.certificate().getEncoded())),"session","csrf");
            Link link=new Link(api,loop,"/v1/mobile/ws","","","process",new Link.Events(){public void state(int label,boolean connected){if(connected)online.countDown();}public void message(JSONObject o){}});
            try{link.connect();assertTrue(online.await(5,TimeUnit.SECONDS));loop.shutdownNow();link.close();assertTrue("socket close must not depend on a dead executor",closed.await(5,TimeUnit.SECONDS));}
            finally{link.close();api.close();}
        }finally{loop.shutdownNow();}
    }
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
                public void state(int label,boolean online){if(label==R.string.link_auth_required)revoked.countDown();}
                public void message(JSONObject o){if(first.getCount()>0)first.countDown();else second.countDown();}
            });
            try {
                link.connect();assertTrue(first.await(5,TimeUnit.SECONDS));for(int i=0;i<100;i++)link.networkChanged();assertTrue(second.await(5,TimeUnit.SECONDS));
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
            Link link=new Link(api,loop,"/v1/agent/ws","reader-agent","scoped-reader-token","process",new Link.Events(){public void message(JSONObject o){}public void state(int label,boolean online){if(online)admitted.countDown();}});
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
