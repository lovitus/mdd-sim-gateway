package com.lovitus.mddagent;
import okhttp3.mockwebserver.*;
import okhttp3.tls.*;
import org.junit.Test;
import static org.junit.Assert.*;
import java.io.*;
import java.util.concurrent.TimeUnit;
public class GatewayApiTest {
    @Test public void explicitPinAllowsOnlyExactCertificateAndNoRedirectOrPaidRetry()throws Exception{
        HeldCertificate cert=new HeldCertificate.Builder().addSubjectAlternativeName("localhost").build();
        try(MockWebServer server=new MockWebServer()){
            server.useHttps(new HandshakeCertificates.Builder().heldCertificate(cert).build().sslSocketFactory(),false);server.start();
            String origin=server.url("/").toString();GatewayApi api=new GatewayApi(new Endpoint(origin,Json.sha(cert.certificate().getEncoded())),"session-test","csrf-test");
            try{
                server.enqueue(new MockResponse().setBody("{\"ok\":true}"));assertTrue(api.json("POST","/mutation",Json.obj("operation_id","once")).getBoolean("ok"));RecordedRequest r=server.takeRequest();assertEquals("mdd_session=session-test",r.getHeader("Cookie"));assertEquals("csrf-test",r.getHeader("X-MDD-CSRF-Token"));assertEquals("Bearer session-test",r.getHeader("Authorization"));
                server.enqueue(new MockResponse().setResponseCode(307).setHeader("Location","https://other.invalid/").setBody("{}"));assertThrows(GatewayApi.Failure.class,()->api.json("POST","/paid",Json.obj("operation_id","never-repeat")));assertNotNull(server.takeRequest(1,TimeUnit.SECONDS));assertNull(server.takeRequest(100,TimeUnit.MILLISECONDS));
            Object stopped=new Object();api.cancel(stopped);assertThrows(IOException.class,()->api.json("POST","/cancelled-paid",Json.obj("operation_id","stop-before-dispatch"),stopped));assertNull(server.takeRequest(100,TimeUnit.MILLISECONDS));
            }finally{api.close();}
            GatewayApi wrong=new GatewayApi(new Endpoint(origin,"00".repeat(32)),"secret","csrf");try{assertThrows(IOException.class,()->wrong.json("GET","/never",null));}finally{wrong.close();}
        }
    }
}
