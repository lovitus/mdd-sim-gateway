package com.lovitus.mddagent;

import java.io.IOException;
import java.util.*;

/** Fixed synthetic USIM files, used by the real protocol/Hub/Link in tests. */
final class IdentityCard implements SimProtocol.Card {
    final List<byte[]> commands=Collections.synchronizedList(new ArrayList<>());
    byte[] fcp=Json.unhex("620782054221001c01");
    byte[] smsp=new byte[28];
    String imsiStatus="", smspStatus="";
    int selected, iccidReads, changeAt=Integer.MAX_VALUE, mncLength=2, closeCount;
    Runnable beforeTransfer=()->{};
    IdentityCard(){Arrays.fill(smsp,(byte)0xff);smsp[0]=(byte)0xfd;smsp[13]=4;smsp[14]=(byte)0x91;smsp[15]=0x21;smsp[16]=0x43;smsp[17]=(byte)0xf5;}
    public byte[] transmit(byte[] command)throws Exception{
        commands.add(command.clone());beforeTransfer.run();
        int ins=command[1]&255;
        if(ins==0xa4){
            if(command[2]==4)return Json.unhex("9000");
            selected=((command[5]&255)<<8)|(command[6]&255);
            if(selected==0x2f00)return Json.unhex("6a82");
            if(selected==0x6f42)return smspStatus.isEmpty()?status(fcp):Json.unhex(smspStatus);
            return Json.unhex("9000");
        }
        if(ins==0xb0){
            if(selected==0x2fe2)return status(Json.unhex(++iccidReads>=changeAt?"98103254769810325477":"98103254769810325476"));
            if(selected==0x6f07)return imsiStatus.isEmpty()?status(Json.unhex("080910101032547698")):Json.unhex(imsiStatus);
            if(selected==0x6fad)return status(new byte[]{0,0,0,(byte)mncLength});
        }
        if(ins==0xb2 && selected==0x6f42)return status(smsp);
        if(ins==0x88)return Json.unhex("db009000");
        throw new IOException("Unexpected test APDU "+Json.hex(command));
    }
    public void select(String application)throws Exception{SimProtocol.selectApplication(this,application);}
    public void close(){closeCount++;}
    private static byte[] status(byte[] bytes){byte[] result=Arrays.copyOf(bytes,bytes.length+2);result[bytes.length]=(byte)0x90;return result;}
}
