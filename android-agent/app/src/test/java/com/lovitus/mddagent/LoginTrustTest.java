package com.lovitus.mddagent;

import okhttp3.mockwebserver.MockWebServer;
import okhttp3.tls.HandshakeCertificates;
import okhttp3.tls.HeldCertificate;
import org.json.JSONObject;
import org.junit.Test;
import java.util.concurrent.TimeUnit;
import static org.junit.Assert.*;

public class LoginTrustTest {
    @Test public void originCanonicalizationKeepsPortsAndIpv6Distinct(){
        assertEquals("https://gateway.test",new Endpoint("https://GATEWAY.test:443/","").origin);
        assertEquals("https://gateway.test:8443",new Endpoint("https://GATEWAY.test:8443/","").origin);
        assertEquals("https://[2001:db8::1]",new Endpoint("https://[2001:DB8::1]:443/","").origin);
    }
    @Test public void rememberedProfileIsSeparateFromActiveSessionAndCanForgetPassword()throws Exception{
        JSONObject state=Json.obj("server","https://active.test","token","active-session","pending_call",Json.obj("operation_id","original"));
        LoginProfile profile=LoginProfile.read(state);profile.address="new.test:8443";profile.username="owner";profile.password="fixture-password";
        state.put("login_profile",profile.persisted());assertEquals("fixture-password",LoginProfile.read(state).password);
        assertEquals("https://new.test:8443",profile.endpoint().origin);assertEquals("https://active.test",state.getString("server"));assertEquals("active-session",state.getString("token"));
        profile.remember=false;state.put("login_profile",profile.persisted());assertEquals("",LoginProfile.read(state).password);assertTrue(state.has("pending_call"));
    }
    @Test public void certificateReplacementRequiresExactPreviousOriginPin()throws Exception{
        JSONObject state=new JSONObject();String first="01".repeat(32),next="02".repeat(32);
        LoginProfile.acceptPin(state,"https://GATEWAY.test:443","",first);
        assertEquals(first,LoginProfile.pin(state,"https://gateway.test"));assertEquals("",LoginProfile.pin(state,"https://gateway.test:8443"));
        assertThrows(IllegalStateException.class,()->LoginProfile.acceptPin(state,"https://gateway.test","",next));
        assertEquals(first,LoginProfile.pin(state,"https://gateway.test"));LoginProfile.acceptPin(state,"https://gateway.test",first,next);assertEquals(next,LoginProfile.pin(state,"https://gateway.test"));
    }
    @Test public void inspectionCannotSendHttpOrCredentials()throws Exception{
        HeldCertificate cert=new HeldCertificate.Builder().addSubjectAlternativeName("localhost").build();
        try(MockWebServer server=new MockWebServer()){
            server.useHttps(new HandshakeCertificates.Builder().heldCertificate(cert).build().sslSocketFactory(),false);server.start();
            CertificateProbe.Presented result=CertificateProbe.inspect(new Endpoint(server.url("/").toString(),""));
            assertEquals(Json.sha(cert.certificate().getEncoded()),result.fingerprint);
            assertNull(server.takeRequest(100,TimeUnit.MILLISECONDS));
        }
    }
    @Test public void expiredCertificateHasNoAcceptableInspectionResult()throws Exception{
        HeldCertificate cert=new HeldCertificate.Builder().addSubjectAlternativeName("localhost").validityInterval(1000,2000).build();
        try(MockWebServer server=new MockWebServer()){
            server.useHttps(new HandshakeCertificates.Builder().heldCertificate(cert).build().sslSocketFactory(),false);server.start();
            assertThrows(java.security.cert.CertificateExpiredException.class,()->CertificateProbe.inspect(new Endpoint(server.url("/").toString(),"")));
            assertNull(server.takeRequest(100,TimeUnit.MILLISECONDS));
        }
    }
}
