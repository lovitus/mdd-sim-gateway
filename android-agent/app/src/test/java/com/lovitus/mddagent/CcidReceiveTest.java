package com.lovitus.mddagent;

import org.junit.Test;
import java.io.IOException;
import java.net.SocketTimeoutException;
import java.util.Arrays;
import static org.junit.Assert.*;

public class CcidReceiveTest {
    private static final class BulkEndpoint implements Ccid.BulkReader {
        final byte[] response;
        final int packetSize,allocationLimit;
        int position,leadingZeros,reads;

        BulkEndpoint(byte[] response,int packetSize,int allocationLimit,int leadingZeros){
            this.response=response;this.packetSize=packetSize;
            this.allocationLimit=allocationLimit;this.leadingZeros=leadingZeros;
        }

        public int read(byte[] buffer,int offset,int length)throws IOException{
            reads++;
            if(length>allocationLimit)throw new IOException("USB read failed (-12)");
            if(leadingZeros>0){leadingZeros--;return 0;}
            int remaining=response.length-position;
            if(remaining==0)throw new SocketTimeoutException("No additional USB packet");
            // A bulk read completes on its requested length or a short USB packet.
            if(length>remaining&&remaining%packetSize==0)
                throw new SocketTimeoutException("Full final packet does not finish oversized read");
            int n=Math.min(length,remaining);
            System.arraycopy(response,position,buffer,offset,n);position+=n;return n;
        }
    }

    @Test public void fullFinalPacketsAndLeadingZlpDoNotDiscardACompleteResponse()throws Exception{
        for(int packet:new int[]{8,16,64,512,1024}){
            for(int frameLength:new int[]{packet*2,packet*3,packet*3+1}){
                byte[] frame=Ccid.command(0x80,0,7,new byte[frameLength-10]);
                for(int zeros:new int[]{0,1,3}){
                    BulkEndpoint input=new BulkEndpoint(frame,packet,Ccid.MAX,zeros);
                    byte[] actual=Ccid.receive(packet,input);
                    assertArrayEquals(frame,actual);
                    assertEquals(frame.length,input.position);
                    assertEquals((frame.length+packet-1)/packet+zeros,input.reads);
                    assertArrayEquals(new byte[frameLength-10],Ccid.result(actual,0,7,0x80));
                    assertThrows(IOException.class,()->Ccid.result(actual,0,8,0x80));
                }
            }
        }
    }

    @Test public void packetReadsAvoidLargeNativeAllocationWithoutRelaxingFrameBounds()throws Exception{
        for(int packet:new int[]{8,16,64,512,1024}){
            for(int bodyLength:new int[]{0,2,6,22,255,65536}){
                byte[] frame=Ccid.command(0x80,0,7,new byte[bodyLength]);
                assertArrayEquals(frame,Ccid.receive(packet,new BulkEndpoint(frame,packet,1024,0)));
            }
        }
        byte[] frame=Ccid.command(0x80,0,7,new byte[]{1,2});
        byte[] oversized=frame.clone();Arrays.fill(oversized,1,5,(byte)0xff);
        for(byte[] invalid:new byte[][]{Arrays.copyOf(frame,11),Arrays.copyOf(frame,13),oversized})
            assertThrows(IOException.class,()->Ccid.receive(16,new BulkEndpoint(invalid,16,1024,0)));
        BulkEndpoint empty=new BulkEndpoint(frame,16,1024,4);
        assertThrows(IOException.class,()->Ccid.receive(16,empty));
        assertEquals(4,empty.reads);
        IOException nativeFailure=new IOException("USB read failed (-110)");
        assertSame(nativeFailure,assertThrows(IOException.class,()->Ccid.receive(16,(b,o,n)->{throw nativeFailure;})));
        for(int invalid:new int[]{0,-1,1025})
            assertThrows(IOException.class,()->Ccid.receive(invalid,(b,o,n)->{fail("Invalid endpoint must not read");return 0;}));
    }
}
