package com.lovitus.mddagent;
import java.io.IOException;
import java.nio.*;
import java.util.Arrays;
/** USB CCID framing only; never interpret peer-supplied bytes as arbitrary APDUs. */
final class Ccid {
    static final int MAX=65546;
    static byte[] command(int type,int slot,int sequence,byte[] body){
        if(slot<0||slot>255||sequence<0||sequence>255||body.length>65536)throw new IllegalArgumentException("CCID bounds");
        ByteBuffer b=ByteBuffer.allocate(10+body.length).order(ByteOrder.LITTLE_ENDIAN);
        b.put((byte)type).putInt(body.length).put((byte)slot).put((byte)sequence).put(new byte[3]).put(body);return b.array();
    }
    static int length(byte[] b,int count)throws IOException{
        if(count<10)return -1;long n=Integer.toUnsignedLong(ByteBuffer.wrap(b,1,4).order(ByteOrder.LITTLE_ENDIAN).getInt());
        if(n>65536)throw new IOException("CCID response too large");return (int)n+10;
    }
    interface BulkReader {
        int read(byte[] buffer,int offset,int length)throws IOException;
    }
    static byte[] receive(int packetSize,BulkReader input)throws IOException{
        if(packetSize<=0||packetSize>1024)throw new IOException("Invalid USB bulk packet size");
        byte[] frame=new byte[MAX];
        int count=0,expected=-1,empty=0;
        // OpenEUICC reads endpoint-sized packets and assembles by CCID dwLength.
        // Large USB reads can wait for a short packet after a complete CCID frame.
        while(expected<0||count<expected){
            int requested=Math.min(packetSize,frame.length-count);
            int read=input.read(frame,count,requested);
            // An exact-packet response may leave a terminating ZLP for this read.
            if(read==0&&count==0&&empty++<3)continue;
            if(read<=0||read>requested)throw new IOException("Incomplete CCID response");
            count+=read;
            expected=length(frame,count);
            if(expected>=0&&count>expected)throw new IOException("CCID trailing data");
        }
        return Arrays.copyOf(frame,count);
    }
    static byte[] result(byte[] b,int slot,int sequence,int type)throws IOException{
        if(b.length<10||length(b,b.length)!=b.length||(b[0]&255)!=type||(b[5]&255)!=slot||(b[6]&255)!=sequence)throw new IOException("CCID response identity mismatch");
        if((b[7]&0xc0)!=0)throw new IOException("CCID command failed");
        if((b[7]&3)==2)throw new IOException("Card removed");
        return Arrays.copyOfRange(b,10,b.length);
    }
    static boolean apduLevel(byte[] descriptors,int interfaceID){
        int current=-1;
        for(int offset=0;offset+2<=descriptors.length;){int n=descriptors[offset]&255;if(n<2||offset+n>descriptors.length)return false;
            int type=descriptors[offset+1]&255;if(type==4&&n>=9)current=descriptors[offset+2]&255;
            if(current==interfaceID&&type==0x21&&n>=54){int features=ByteBuffer.wrap(descriptors,offset+40,4).order(ByteOrder.LITTLE_ENDIAN).getInt();return (features&0x60000)!=0;}
            offset+=n;
        }return false;
    }
}
