package com.lovitus.mddagent;
import java.io.*;
import java.util.*;
import org.json.*;
/** Port of the existing agentsim fixed read/AKA protocol; no remote APDU tunnel. */
final class SimProtocol {
    interface Card extends AutoCloseable {
        byte[] transmit(byte[] command)throws Exception;
        void select(String application)throws Exception;
        void close();
    }
    static byte[] exchange(Card card,byte[] command)throws Exception{
        byte[] r=card.transmit(command);check(r);
        if((r[r.length-2]&255)==0x6c){byte[] corrected=command.clone();int lc=corrected[4]&255;
            if(corrected.length==5)corrected[4]=r[r.length-1];else if(corrected.length==5+lc){corrected=Arrays.copyOf(command,command.length+1);corrected[corrected.length-1]=r[r.length-1];}else if(corrected.length==6+lc)corrected[corrected.length-1]=r[r.length-1];else return r;
            r=card.transmit(corrected);check(r);
        }
        ByteArrayOutputStream out=new ByteArrayOutputStream();
        for(int n=0;;n++){
            check(r);out.write(r,0,r.length-2);int sw=r[r.length-2]&255;
            if(out.size()>4096)throw new IOException("SIM response too large");
            if(sw!=0x61&&sw!=0x9f){out.write(r,r.length-2,2);return out.toByteArray();}
            if(n>=4)throw new IOException("SIM response chaining exceeded");
            r=card.transmit(new byte[]{0,(byte)0xc0,0,0,r[r.length-1]});
        }
    }
    static void check(byte[] r)throws IOException{if(r==null||r.length<2)throw new IOException("Short SIM response");}
    static byte[] success(byte[] r)throws IOException{check(r);if((r[r.length-2]&255)!=0x90||r[r.length-1]!=0)throw new IOException("SIM access denied or unavailable");return Arrays.copyOf(r,r.length-2);}
    static void file(Card c,int id)throws Exception{success(exchange(c,new byte[]{0,(byte)0xa4,0,4,2,(byte)(id>>8),(byte)id,0}));}
    static String iccid(Card c)throws Exception{file(c,0x3f00);file(c,0x2fe2);byte[] b=success(exchange(c,new byte[]{0,(byte)0xb0,0,0,10}));if(b.length!=10)throw new IOException("Invalid ICCID size");return bcd(b,false);}
    static String bcd(byte[] data,boolean imsi)throws IOException{
        int start=0,end=data.length;StringBuilder s=new StringBuilder();boolean odd=true;
        if(imsi){if(data.length<2||(data[0]&255)<1||(data[0]&255)>data.length-1||(data[1]&7)!=1)throw new IOException("Invalid IMSI");end=(data[0]&255)+1;odd=(data[1]&8)!=0;appendDigit(s,(data[1]&255)>>4);start=2;}
        for(int i=start;i<end;i++){int low=data[i]&15,high=(data[i]&255)>>4;appendDigit(s,low);
            if(high==15&&i==end-1&&(!imsi||!odd))continue;appendDigit(s,high);}
        if(imsi&&(s.length()<5||s.length()>15||(s.length()%2==1)!=odd))throw new IOException("Invalid IMSI parity");
        return s.toString();
    }
    private static void appendDigit(StringBuilder s,int d)throws IOException{if(d>9)throw new IOException("Invalid numeric identity");s.append((char)('0'+d));}
    static byte[] ef(Card c,int id,int len)throws Exception{file(c,id);byte[] b=success(exchange(c,new byte[]{0,(byte)0xb0,0,0,(byte)len}));if(b.length<len)throw new IOException("Short identity file");return b;}
    static JSONObject identity(Card c)throws Exception{
        String id=iccid(c);JSONObject sim;
        try{c.select("usim");String imsi=bcd(ef(c,0x6f07,9),true);sim=Json.obj("identity_state","partial","imsi",imsi,"mcc",imsi.substring(0,3),"error_code","reader_sim_mnc_length_unavailable");
            try{byte[] ad=ef(c,0x6fad,4);int len=ad[3]&15;if((len==2||len==3)&&imsi.length()>=3+len){sim.put("mnc",imsi.substring(3,3+len));sim.put("identity_state","ready");sim.remove("error_code");}}catch(Exception ignored){}
        }catch(Exception unavailable){sim=Json.obj("identity_state","unavailable","error_code","reader_sim_identity_unavailable");}
        return Json.obj("card_id",id,"sim",sim);
    }
    static byte[] aka(Card c,String application,byte[] rand,byte[] autn)throws Exception{
        if(!Arrays.asList("usim","isim").contains(application)||rand.length!=16||autn.length!=16)throw new IOException("Invalid AKA challenge");
        c.select(application);byte[] q=new byte[39];q[1]=(byte)0x88;q[3]=(byte)0x81;q[4]=34;q[5]=16;System.arraycopy(rand,0,q,6,16);q[22]=16;System.arraycopy(autn,0,q,23,16);return exchange(c,q);
    }
}
