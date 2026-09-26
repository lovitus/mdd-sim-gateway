package com.lovitus.mddagent;
import android.hardware.usb.*;
import android.os.SystemClock;
import android.system.OsConstants;
import java.io.*;
import java.nio.ByteBuffer;
import java.util.Arrays;
import java.util.concurrent.atomic.AtomicLong;
final class UsbCard implements SimProtocol.Card {
    final UsbDevice device; final UsbDeviceConnection connection; final UsbInterface intf;
    final UsbEndpoint input,output,interrupt; final AtomicLong insertion=new AtomicLong(1);
    volatile boolean closed; int sequence; byte[] atr;
    private final Object eventLock=new Object();
    private UsbRequest eventRequest;
    private Thread eventThread;
    private boolean claimed;
    static final class WriteFailure extends IOException {
        final int transferred;
        WriteFailure(int transferred){super("USB write failed ("+transferred+"); outcome unknown");this.transferred=transferred;}
    }
    UsbCard(UsbManager manager,UsbDevice d)throws Exception{
        this(manager,d,false);
    }
    UsbCard(UsbManager manager,UsbDevice d,boolean reset)throws Exception{
        device=d;UsbInterface found=null;UsbEndpoint in=null,out=null,intr=null;
        for(int i=0;i<d.getInterfaceCount();i++){UsbInterface f=d.getInterface(i);if(f.getInterfaceClass()!=11)continue;in=null;out=null;intr=null;
            for(int e=0;e<f.getEndpointCount();e++){UsbEndpoint p=f.getEndpoint(e);if(p.getType()==UsbConstants.USB_ENDPOINT_XFER_BULK){if(p.getDirection()==UsbConstants.USB_DIR_IN)in=p;else out=p;}else if(p.getType()==UsbConstants.USB_ENDPOINT_XFER_INT&&p.getDirection()==UsbConstants.USB_DIR_IN)intr=p;}
            if(in!=null&&out!=null){found=f;break;}}
        if(found==null)throw new IOException("USB device has no CCID bulk interface");
        intf=found;input=in;output=out;interrupt=intr;connection=manager.openDevice(d);
        if(connection==null){if(reset)throw new UsbRecovery.Failure("open",-OsConstants.EACCES);throw new IOException("USB permission required");}
        String stage="capability";
        try{
            if(!Ccid.apduLevel(connection.getRawDescriptors(),intf.getId()))throw new IOException("Reader must support APDU-level CCID (TPDU readers are not advertised)");
            stage="claim";
            if(!connection.claimInterface(intf,!reset))throw new IOException("USB interface is unavailable");
            claimed=true;
            if(reset){
                stage="release";
                if(!connection.releaseInterface(intf))throw new UsbRecovery.Failure(stage,-OsConstants.EIO);
                claimed=false;
                stage="reset";
                int result=UsbPortReset.reset(connection.getFileDescriptor());
                if(result!=0)throw new UsbRecovery.Failure(stage,result);
                stage="reclaim";
                if(!connection.claimInterface(intf,false))throw new UsbRecovery.Failure(stage,-OsConstants.EBUSY);
                claimed=true;
                // Keep the recovered descriptor alive through CCID handshake and power-on.
                stage="slot_status";
                for(int attempt=0;;attempt++){
                    try{status();break;}
                    catch(WriteFailure notReady){
                        // Only GetSlotStatus is safe to repeat while firmware resumes.
                        if(attempt>=2||notReady.transferred>0)throw notReady;
                        Thread.sleep(250L<<attempt);
                    }
                }
            }
            stage="power_on";
            atr=exchange(0x62,new byte[0],0x80);
        }catch(Exception|LinkageError e){close();if(e instanceof InterruptedException)Thread.currentThread().interrupt();if(reset)throw UsbRecovery.Failure.at(stage,e);throw e;}
        if(interrupt!=null){eventThread=new Thread(this::watch,"mdd-ccid-events");eventThread.setDaemon(true);eventThread.start();}
    }
    synchronized byte[] exchange(int type,byte[] data,int expected)throws Exception{
        if(closed)throw new IOException("Reader closed");int seq=sequence++&255;byte[] q=Ccid.command(type,0,seq,data);
        int written=connection.bulkTransfer(output,q,q.length,2000);
        if(written!=q.length)throw new WriteFailure(written);
        long deadline=SystemClock.elapsedRealtime()+5000;
        for(int extensions=0;extensions<8;extensions++){
            byte[] b=new byte[Ccid.MAX];int count=0,length=-1;
            while(length<0||count<length){int wait=(int)(deadline-SystemClock.elapsedRealtime());if(wait<=0||closed)throw new IOException("USB response timeout");
                int n=connection.bulkTransfer(input,b,count,b.length-count,Math.min(wait,2000));if(n<=0)throw new IOException("USB reader disconnected or timed out");count+=n;length=Ccid.length(b,count);if(length>=0&&count>length)throw new IOException("CCID trailing data");}
            byte[] r=Arrays.copyOf(b,count);
            if((r[5]&255)!=0||(r[6]&255)!=seq||(r[0]&255)!=expected)throw new IOException("CCID stale response");
            if((r[7]&0xc0)==0x80)continue;
            if(type==0x65 && (r[7]&3)==2){insertion.incrementAndGet();throw new IOException("SIM removed");}
            return Ccid.result(r,0,seq,expected);
        }throw new IOException("CCID extension budget exhausted");
    }
    public byte[] transmit(byte[] q)throws Exception{return exchange(0x6f,q,0x80);}
    public void select(String application)throws Exception{SimProtocol.selectApplication(this,application);}
    void status()throws Exception{exchange(0x65,new byte[0],0x81);}
    private void watch(){
        UsbRequest request=new UsbRequest();
        try{
            synchronized(eventLock){if(closed||!request.initialize(connection,interrupt))return;eventRequest=request;}
            while(!closed){ByteBuffer b=ByteBuffer.allocate(8);synchronized(eventLock){if(closed||!request.queue(b))break;}
                // requestWait uses a native wait; this is not APDU or network polling.
                UsbRequest completed=connection.requestWait();if(completed==null)break;
                int size=b.position();if(size>=2&&(b.get(0)&255)==0x50&&(b.get(1)&2)!=0)insertion.incrementAndGet();
            }
        }catch(Exception ignored){if(!closed)insertion.incrementAndGet();}
        finally{synchronized(eventLock){eventRequest=null;request.close();}}
    }
    public void close(){
        synchronized(eventLock){if(closed)return;closed=true;insertion.incrementAndGet();if(eventRequest!=null)eventRequest.cancel();}
        if(eventThread!=null&&eventThread!=Thread.currentThread())try{eventThread.join(2000);}catch(InterruptedException interrupted){Thread.currentThread().interrupt();}
        if(claimed){connection.releaseInterface(intf);claimed=false;}connection.close();
    }
}
