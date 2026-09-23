package com.lovitus.mddagent;

import java.io.IOException;
import java.security.cert.CertificateException;
import java.util.concurrent.CompletionException;
import javax.net.ssl.SSLException;
import javax.net.ssl.SSLPeerUnverifiedException;
import org.json.JSONObject;
import org.junit.Test;
import static org.junit.Assert.*;

public class Pr8ReadRecoveryTest {
    @Test public void transientTlsReadsRetryButIdentityAndBusinessGatesStayTerminal(){
        assertFalse(TransportFailures.terminalRead(new SSLException("transport interrupted")));
        assertFalse(TransportFailures.terminalRead(new CompletionException(new SSLException("wrapped I/O"))));
        assertTrue(TransportFailures.terminalRead(new SSLException("handshake",new CertificateException("rejected"))));
        assertTrue(TransportFailures.terminalRead(new SSLPeerUnverifiedException("pin changed")));
        for(int status:new int[]{401,403,404,409})assertTrue(TransportFailures.terminalRead(new GatewayApi.Failure(status,"fixture")));
        for(int status:new int[]{408,429,500,503})assertFalse(TransportFailures.terminalRead(new GatewayApi.Failure(status,"fixture")));
        assertTrue(TransportFailures.terminalRead(new IllegalStateException("storage unavailable",new IOException("disk"))));
        assertFalse(TransportFailures.identityFailure(new IOException("unknown network error")));
    }
    @Test public void observationIsDeeplyImmutableAndOwnedByExactEpoch()throws Exception{
        Object old=new Object(),next=new Object();JSONObject source=Json.obj("readers",new org.json.JSONArray().put(Json.obj("card_id","old")));
        ReaderObservation observation=new ReaderObservation(old,7,1000,source);
        source.getJSONArray("readers").getJSONObject(0).put("card_id","mutated");
        assertEquals("old",observation.topology().getJSONArray("readers").getJSONObject(0).getString("card_id"));
        observation.topology().getJSONArray("readers").getJSONObject(0).put("card_id","also mutated");
        assertEquals("old",observation.topology().getJSONArray("readers").getJSONObject(0).getString("card_id"));
        assertTrue(observation.current(old,7,1001));assertFalse(observation.current(next,7,1001));
        assertFalse(observation.current(old,8,1001));assertFalse(observation.current(old,7,999));assertFalse(observation.current(old,7,21001));
    }
}
