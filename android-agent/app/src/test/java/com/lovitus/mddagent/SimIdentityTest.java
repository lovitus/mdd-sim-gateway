package com.lovitus.mddagent;

import org.json.JSONObject;
import org.junit.Test;
import java.util.Arrays;
import static org.junit.Assert.*;

public class SimIdentityTest {
    @Test public void identityReadsDefaultSMSPWithoutPINOrWrites()throws Exception{
        for(int alpha:new int[]{0,12})for(boolean international:new boolean[]{true,false}){
            IdentityCard card=new IdentityCard();
            byte[] record=new byte[28+alpha];Arrays.fill(record,(byte)0xff);System.arraycopy(card.smsp,0,record,alpha,28);
            record[alpha+14]=(byte)(international?0x91:0x81);card.smsp=record;
            card.fcp=new byte[]{0x62,7,(byte)0x82,5,0x42,0x21,0,(byte)record.length,1};
            JSONObject observed=SimProtocol.identity(card).getJSONObject("sim");
            assertEquals("ready",observed.getString("identity_state"));
            assertEquals("SMSP_READBACK",(international?"+":"")+"12345",observed.optString("smsc"));
            assertEquals("001010123456789",observed.getString("imsi"));assertEquals("01",observed.getString("mnc"));
            for(byte[] command:card.commands){int ins=command[1]&255;assertTrue(ins==0xa4||ins==0xb0||ins==0xb2||ins==0xc0);}
        }
    }
    @Test public void missingOrMalformedSMSPDoesNotInventSMSCorInvalidateIdentity()throws Exception{
        for(String fault:new String[]{"absent","missing","short","filler","length","fcp","oversize"}){
            IdentityCard card=new IdentityCard();
            switch(fault){
                case "absent":card.smsp[0]=(byte)0xff;break;
                case "missing":card.smspStatus="6a82";break;
                case "short":card.smsp=Arrays.copyOf(card.smsp,27);break;
                case "filler":card.smsp[15]=(byte)0xf1;break;
                case "length":card.smsp[13]=12;break;
                case "fcp":card.fcp=Json.unhex("6282ffff");break;
                case "oversize":card.fcp=Json.unhex("620782054221010001");break;
            }
            JSONObject sim=SimProtocol.identity(card).getJSONObject("sim");
            assertEquals(fault,"ready",sim.getString("identity_state"));assertFalse(fault,sim.has("smsc"));
        }
    }
}
