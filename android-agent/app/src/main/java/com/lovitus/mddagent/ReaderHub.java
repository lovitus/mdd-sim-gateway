package com.lovitus.mddagent;
import android.content.Context;
import android.hardware.usb.*;
import android.se.omapi.*;
import android.util.Base64;
import org.json.*;
import java.util.*;
import java.util.concurrent.*;
import java.nio.charset.StandardCharsets;
/** One serialized I/O owner per Hub. Readiness is never inferred from USB presence alone. */
final class ReaderHub implements AutoCloseable {
    private final UsbManager usb;private SEService se;private final TreeMap<String,Entry> entries=new TreeMap<>();
    volatile boolean closed;volatile UiText diagnostic=UiText.of(R.string.reader_permission_missing);
    private final LinkedHashMap<String,Receipt> receipts=new LinkedHashMap<>();
    private static final class Entry{String name,generation=Json.id(),id="";JSONObject sim;SimProtocol.Card card;long insertion,metadataNext;int metadataAttempts;Entry(String name,SimProtocol.Card card){this.name=name;this.card=card;}}
    private static final class Receipt{String fingerprint;JSONObject response;Receipt(String f,JSONObject r){fingerprint=f;response=r;}}
    ReaderHub(Context context){usb=context.getSystemService(UsbManager.class);try{se=new SEService(context,Runnable::run,()->{});}catch(Exception ignored){}}
    synchronized void scan(){scan(false);}
    synchronized void scan(boolean explicitMetadata){if(closed)return;
        Set<String> seen=new HashSet<>();
        for(UsbDevice d:usb.getDeviceList().values()){
            if(!usb.hasPermission(d))continue;boolean ccid=false;for(int i=0;i<d.getInterfaceCount();i++)ccid|=d.getInterface(i).getInterfaceClass()==11;if(!ccid)continue;
            String name="USB-"+d.getVendorId()+"-"+d.getProductId()+"-"+d.getDeviceId()+"-slot0";seen.add(name);
            try{Entry e=entries.get(name);if(e!=null){UsbCard c=(UsbCard)e.card;c.status();if(c.insertion.get()!=e.insertion){remove(name);e=null;}else repairMetadata(e,explicitMetadata);}
                if(e==null&&entries.size()<8){UsbCard card=new UsbCard(usb,d);e=new Entry(name,card);entries.put(name,e);discover(e);e.insertion=card.insertion.get();}
            }catch(Exception failure){remove(name);diagnostic=UiText.of(R.string.reader_usb_unavailable);}
        }
        if(se!=null&&se.isConnected())for(Reader reader:se.getReaders()){
            String name="OMAPI-"+reader.getName();seen.add(name);
            try{Entry e=entries.get(name);if(!reader.isSecureElementPresent()){remove(name);continue;}
                if(e==null&&entries.size()<8){e=new Entry(name,new OmapiCard(reader));entries.put(name,e);discover(e);}else if(e!=null)repairMetadata(e,explicitMetadata);
            }catch(Exception failure){remove(name);diagnostic=UiText.of(R.string.reader_omapi_blocked);}
        }
        for(String name:new ArrayList<>(entries.keySet()))if(!seen.contains(name))remove(name);
        if(!entries.isEmpty())diagnostic=UiText.of(R.string.reader_count,entries.size());
    }
    private void discover(Entry e)throws Exception{JSONObject identity=SimProtocol.identity(e.card);e.id=identity.getString("card_id");e.sim=identity.getJSONObject("sim");e.metadataNext=android.os.SystemClock.elapsedRealtime()+60000;}
    private void repairMetadata(Entry e,boolean explicit)throws Exception{
        String state=e.sim==null?"unavailable":e.sim.optString("identity_state");
        if(state.equals("ready")||state.equals("pin_required")&&!explicit)return;
        long now=android.os.SystemClock.elapsedRealtime();
        if(explicit){e.metadataAttempts=0;e.metadataNext=0;}
        if(e.metadataAttempts>=3||now<e.metadataNext)return;
        e.metadataAttempts++;e.metadataNext=now+(1L<<e.metadataAttempts)*30000;
        JSONObject identity=SimProtocol.identity(e.card);
        if(!e.id.equals(identity.getString("card_id")))throw new java.io.IOException("Card identity changed during metadata read");
        e.sim=identity.getJSONObject("sim");
    }
    private void remove(String n){Entry e=entries.remove(n);if(e!=null)e.card.close();}
    synchronized JSONObject topology(){JSONArray readers=new JSONArray();for(Entry e:entries.values()){
        if(e.card instanceof UsbCard&&((UsbCard)e.card).insertion.get()!=e.insertion)continue;
        readers.put(Json.obj("reader_name",e.name,"card_present",true,"session_generation",e.generation,"card_id",e.id,"identity_state","identified","sim",e.sim));}
        return Json.obj("reader_condition","ready","readers",readers,"modem_condition","disabled");
    }
    synchronized JSONObject authenticate(JSONObject q){String op=q.optString("operation_id"),gen=q.optString("session_generation");JSONObject fail=Json.obj("operation_id",op,"session_generation",gen,"failure",Json.obj("kind","not_ready","code","reader_unavailable","retryable",false));
        if(closed)return fail;
        String fp=Json.sha(q.toString().getBytes(StandardCharsets.UTF_8));Receipt old=receipts.get(op);if(old!=null)return old.fingerprint.equals(fp)?old.response:Json.obj("operation_id",op,"session_generation",gen,"failure",Json.obj("kind","conflict","code","operation_identity_conflict","retryable",false));
        try{
            if(!op.matches("[A-Za-z0-9_.:-]{1,160}")||(!q.optString("device_kind").isEmpty()&&!q.optString("device_kind").equals("reader")))return fail;
            byte[] rand=Base64.decode(q.getString("rand"),Base64.NO_WRAP),autn=Base64.decode(q.getString("autn"),Base64.NO_WRAP);
            String app=q.getString("application");if(rand.length!=16||autn.length!=16||!(app.equals("usim")||app.equals("isim")))return fail;
            Entry match=null;for(Entry e:entries.values())if(e.generation.equals(gen)&&e.id.equals(q.optString("card_id"))){if(match!=null)return fail;match=e;}
            if(match==null)return fail;Entry e=match;
            if(e.card instanceof UsbCard&&((UsbCard)e.card).insertion.get()!=e.insertion)return fail;
            // Fresh identity under the serialized owner, before authentication.
            if(!SimProtocol.iccid(e.card).equals(e.id)){remove(e.name);return fail;}
            receipts.put(op,new Receipt(fp,fail));
            byte[] r=SimProtocol.aka(e.card,app,rand,autn);
            if(e.card instanceof UsbCard&&((UsbCard)e.card).insertion.get()!=e.insertion)return fail;
            JSONObject response=Json.obj("operation_id",op,"session_generation",gen,"body",Base64.encodeToString(Arrays.copyOf(r,r.length-2),Base64.NO_WRAP),"sw1",r[r.length-2]&255,"sw2",r[r.length-1]&255);
            receipts.put(op,new Receipt(fp,response));while(receipts.size()>128)receipts.remove(receipts.keySet().iterator().next());return response;
        }catch(Exception failure){receipts.put(op,new Receipt(fp,fail));while(receipts.size()>128)receipts.remove(receipts.keySet().iterator().next());return fail;}
    }
    public synchronized void close(){closed=true;for(Entry e:entries.values())e.card.close();entries.clear();receipts.clear();if(se!=null)se.shutdown();}
}
