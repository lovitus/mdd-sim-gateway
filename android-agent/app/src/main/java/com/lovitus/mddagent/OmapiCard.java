package com.lovitus.mddagent;
import android.se.omapi.*;
import java.io.IOException;
/** Uses only standard access-controlled OMAPI. No privileged/reflection bypass. */
final class OmapiCard implements SimProtocol.Card {
    final Reader reader; final Session session; Channel channel;
    OmapiCard(Reader reader)throws Exception{this.reader=reader;session=reader.openSession();try{select("usim");}catch(Exception e){close();throw e;}}
    public synchronized void select(String application)throws Exception{if(channel!=null)channel.close();channel=session.openLogicalChannel(Json.unhex(application.equals("isim")?"A0000000871004":"A0000000871002"));if(channel==null)throw new IOException("SIM did not grant an OMAPI channel");}
    public synchronized byte[] transmit(byte[] q)throws Exception{if(channel==null||!channel.isOpen())throw new IOException("OMAPI unavailable");return channel.transmit(q);}
    public synchronized void close(){try{session.close();}catch(Exception ignored){}}
}
