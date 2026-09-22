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
    static final class StatusError extends IOException {
        final int sw1,sw2;
        StatusError(byte[] response){super(String.format(Locale.ROOT,"SIM status %02X%02X",response[response.length-2]&255,response[response.length-1]&255));sw1=response[response.length-2]&255;sw2=response[response.length-1]&255;}
    }
    static byte[] success(byte[] r)throws IOException{check(r);if((r[r.length-2]&255)!=0x90||r[r.length-1]!=0)throw new StatusError(r);return Arrays.copyOf(r,r.length-2);}
    // Port of agentsim/apdu.go: full EF.DIR AIDs first, then partial-name fallback,
    // both SELECT response modes, and at most 32 read-only directory records.
    static void selectApplication(Card card,String application)throws Exception{
        String prefix=application.equals("isim")?"A0000000871004":"A0000000871002";
        LinkedHashSet<String> aids=new LinkedHashSet<>();
        try{
            file(card,0x3f00);file(card,0x2f00);
            for(int record=1;record<=32;record++){
                byte[] response=exchange(card,new byte[]{0,(byte)0xb2,(byte)record,4,0});
                if((response[response.length-2]&255)!=0x90||response[response.length-1]!=0)break;
                findAIDs(Arrays.copyOf(response,response.length-2),0,aids);
            }
        }catch(StatusError unavailable){/* A missing directory permits standards-based partial selection. */}
        ArrayList<String> candidates=new ArrayList<>();for(String aid:aids)if(aid.startsWith(prefix))candidates.add(aid);candidates.add(prefix);
        StatusError last=null;
        for(String aid:candidates)for(int p2:new int[]{4,0}){
            byte[] bytes=Json.unhex(aid),command=new byte[5+bytes.length];command[1]=(byte)0xa4;command[2]=4;command[3]=(byte)p2;command[4]=(byte)bytes.length;System.arraycopy(bytes,0,command,5,bytes.length);
            byte[] response=exchange(card,command);int sw=response[response.length-2]&255;
            if(sw==0x90&&response[response.length-1]==0||sw==0x62||sw==0x63)return;
            last=new StatusError(response);
        }
        if(last!=null)throw last;throw new IOException("SIM application unavailable");
    }
    private static void findAIDs(byte[] bytes,int depth,Set<String> aids)throws IOException{
        if(depth>8||bytes.length>4096)throw new IOException("SIM directory bounds exceeded");
        for(int offset=0;offset<bytes.length;){
            int tag=bytes[offset++]&255;if(tag==0||tag==255)continue;
            if((tag&31)==31)return;
            if(offset>=bytes.length)return;int length=bytes[offset++]&255;
            if((length&128)!=0){int octets=length&127;if(octets<1||octets>2||offset+octets>bytes.length)return;length=0;for(int i=0;i<octets;i++)length=(length<<8)|(bytes[offset++]&255);}
            if(length>bytes.length-offset)return;
            byte[] value=Arrays.copyOfRange(bytes,offset,offset+length);offset+=length;
            if(tag==0x4f&&value.length>0&&value.length<=16)aids.add(Json.hex(value).toUpperCase(Locale.ROOT));
            if((tag&32)!=0)findAIDs(value,depth+1,aids);
        }
    }
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
        }catch(Exception unavailable){
            boolean pin=unavailable instanceof StatusError&&((StatusError)unavailable).sw1==0x69&&((StatusError)unavailable).sw2==0x82;
            String code=pin?"reader_sim_pin_required":unavailable instanceof StatusError?String.format(Locale.ROOT,"reader_sim_status_%02x%02x",((StatusError)unavailable).sw1,((StatusError)unavailable).sw2):"reader_sim_identity_unavailable";
            sim=Json.obj("identity_state",pin?"pin_required":"unavailable","error_code",code);
        }
        return Json.obj("card_id",id,"sim",sim);
    }
    static byte[] aka(Card c,String application,byte[] rand,byte[] autn)throws Exception{
        if(!Arrays.asList("usim","isim").contains(application)||rand.length!=16||autn.length!=16)throw new IOException("Invalid AKA challenge");
        c.select(application);byte[] q=new byte[39];q[1]=(byte)0x88;q[3]=(byte)0x81;q[4]=34;q[5]=16;System.arraycopy(rand,0,q,6,16);q[22]=16;System.arraycopy(autn,0,q,23,16);return exchange(c,q);
    }
}
