package com.lovitus.mddagent;
import org.junit.Test;
import static org.junit.Assert.*;
import org.json.*;
import java.io.*;
import java.nio.*;
import java.util.*;
public class ProtocolTest {
    @Test public void strictOriginAndPin(){
        Endpoint e=new Endpoint("https://gateway.test:8443/", "aa:".repeat(31)+"aa");assertEquals("https://gateway.test:8443",e.origin);assertEquals(64,e.fingerprint.length());
        for(String bad:new String[]{"http://gateway.test","https://u:p@gateway.test","https://gateway.test/path","https://gateway.test?token=x","https://gateway.test/#x","https://gateway.test:0"})assertThrows(IllegalArgumentException.class,()->new Endpoint(bad,""));
        for(String bad:new String[]{"//evil.test/x","/\\evil.test/x","/x\nHeader: foo","/x#fragment"})assertThrows(IllegalArgumentException.class,()->e.path(bad));
        assertEquals("https://gateway.test:8443/v1/lines/a%2Fb",e.path("/v1/lines/a%2Fb"));
        assertThrows(IllegalArgumentException.class,()->new Endpoint("https://gateway.test","bad"));
    }
    @Test public void qrIsExplicitDataNotCredentialsCommand()throws Exception{
        Setup s=new Setup("{\"version\":1,\"type\":\"mdd-agent-setup\",\"server\":\"https://gateway.test\"}");assertEquals("https://gateway.test",s.endpoint.origin);
        assertThrows(IllegalArgumentException.class,()->new Setup("{\"version\":1,\"type\":\"mdd-agent-setup\",\"server\":\"https://gateway.test\",\"password\":\"secret\"}"));
        assertThrows(IllegalArgumentException.class,()->new Setup("{\"version\":2}"));
    }
    @Test public void reconnectIsBoundedWithJitterAndReset(){Retry r=new Retry(new Random(42));Set<Long> values=new HashSet<>();for(int i=0;i<100;i++){long n=r.next();assertTrue(n>=500&&n<120500);values.add(n);}assertTrue(values.size()>20);r.healthy();assertTrue(r.next()<1500);}
    @Test public void incomingUsesProviderCallIdentity()throws Exception{
        JSONObject line=Json.obj("id","line-a","card_id","89012345678901234567","enabled",true),in=Json.obj("call_id","provider-incoming","caller","+12345678900");
        CallPlan p=new CallPlan(line,"vowifi","",in);assertEquals("provider-incoming",p.id);assertEquals("provider-incoming",p.lease().getString("call_id"));assertEquals("provider-incoming",p.start("lease").getString("call_id"));assertTrue(p.startPath().endsWith("/incoming/answer"));
        CallPlan outgoing=new CallPlan(line,"cellular","0012345678900",null);assertEquals("+12345678900",outgoing.number);assertEquals(line.getString("card_id"),outgoing.start("lease").getString("expected_card_id"));assertEquals("lease",outgoing.end("lease").getString("session_id"));
    }
    @Test public void smsAndIncomingBindExactCardAndOccurrence()throws Exception{
        JSONObject sms=CallPlan.sms("89012345678901234567","+12345678900","hello","one-op");assertEquals(sms.getString("operation_id"),sms.getString("message_id"));assertThrows(IllegalArgumentException.class,()->CallPlan.sms("","+12345678900","hi","op"));
        JSONObject line=Json.obj("id","a/b","card_id","89012345678901234567","enabled",true),in=Json.obj("incoming_event_id","incoming-1","actionable",true,"sim_session_generation","sim-2","native_call_index",3,"occurrence",4L);
        CallPlan p=new CallPlan(line,"cellular","",in);assertEquals("/v1/lines/a%2Fb/cellular/calls/answer",p.startPath());assertEquals(4,p.start("lease").getLong("call_occurrence"));assertEquals("sim-2",p.lease().getString("sim_session_generation"));
        in.put("actionable",false);assertThrows(IllegalArgumentException.class,()->new CallPlan(line,"cellular","",in));
    }
    @Test public void ccidRejectsStaleTruncatedOversizedAndRemoved()throws Exception{
        byte[] reply=Ccid.command(0x80,0,7,new byte[]{1,2});assertArrayEquals(new byte[]{1,2},Ccid.result(reply,0,7,0x80));
        assertThrows(IOException.class,()->Ccid.result(reply,0,8,0x80));assertThrows(IOException.class,()->Ccid.result(Arrays.copyOf(reply,11),0,7,0x80));
        reply[7]=2;assertThrows(IOException.class,()->Ccid.result(reply,0,7,0x80));reply[7]=0;Arrays.fill(reply,1,5,(byte)0xff);assertThrows(IOException.class,()->Ccid.length(reply,reply.length));
    }
    @Test public void ccidRequiresApduDescriptor(){byte[] d=new byte[63];d[0]=9;d[1]=4;d[2]=2;d[9]=54;d[10]=0x21;d[51]=2;assertTrue(Ccid.apduLevel(d,2));assertFalse(Ccid.apduLevel(d,1));d[51]=1;assertFalse(Ccid.apduLevel(d,2));d[9]=0;assertFalse(Ccid.apduLevel(d,2));}
    private static class Card implements SimProtocol.Card {final ArrayDeque<byte[]> replies=new ArrayDeque<>();final List<byte[]> sent=new ArrayList<>();String selected;public byte[] transmit(byte[] c){sent.add(c.clone());return replies.remove();}public void select(String app){selected=app;}public void close(){}}
    @Test public void apduChainingAndAkaAreFixedAndBounded()throws Exception{
        Card c=new Card();c.replies.add(Json.unhex("6102"));c.replies.add(Json.unhex("01029000"));assertArrayEquals(Json.unhex("01029000"),SimProtocol.exchange(c,Json.unhex("00B0000002")));assertArrayEquals(Json.unhex("00C0000002"),c.sent.get(1));
        Card correction=new Card();correction.replies.add(Json.unhex("6C04"));correction.replies.add(Json.unhex("010203049000"));SimProtocol.exchange(correction,Json.unhex("00B0000002"));assertEquals(4,correction.sent.get(1)[4]);
        Card aka=new Card();aka.replies.add(Json.unhex("DB009000"));SimProtocol.aka(aka,"usim",new byte[16],new byte[16]);assertEquals("usim",aka.selected);assertEquals(39,aka.sent.get(0).length);assertEquals(0x88,aka.sent.get(0)[1]&255);assertThrows(IOException.class,()->SimProtocol.aka(aka,"raw",new byte[16],new byte[16]));assertEquals(1,aka.sent.size());
        Card loop=new Card();for(int i=0;i<8;i++)loop.replies.add(Json.unhex("6101"));assertThrows(IOException.class,()->SimProtocol.exchange(loop,Json.unhex("00B0000001")));assertTrue(loop.sent.size()<=5);
    }
    @Test public void identitiesAreDecodedNotGuessed()throws Exception{
        assertEquals("89012345678901234567",SimProtocol.bcd(Json.unhex("98103254769810325476"),false));assertEquals("001010123456789",SimProtocol.bcd(Json.unhex("080910101032547698"),true));
        assertThrows(IOException.class,()->SimProtocol.bcd(Json.unhex("080A"),true));
    }
}
