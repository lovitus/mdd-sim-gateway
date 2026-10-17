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
    private Map<String,UiText> usbFailures=Collections.emptyMap();
    private final Map<String,UsbRecovery> usbRecovery=new HashMap<>();
    synchronized Map<String,UiText> usbFailures(){return new HashMap<>(usbFailures);}
    private final LinkedHashMap<String,Receipt> receipts=new LinkedHashMap<>();
    private static final class Entry{String name,generation=Json.id(),id="";JSONObject sim;SimProtocol.Card card;long insertion,metadataNext;int metadataAttempts;Entry(String name,SimProtocol.Card card){this.name=name;this.card=card;}}
    private static final class Receipt{String fingerprint;JSONObject response;Receipt(String f,JSONObject r){fingerprint=f;response=r;}}
    ReaderHub(Context context){usb=context.getSystemService(UsbManager.class);try{se=new SEService(context,Runnable::run,()->{});}catch(Exception ignored){}}
    synchronized void scan(boolean explicitMetadata,boolean recheck,java.util.function.BooleanSupplier canRecover){if(closed)return;
        Set<String> seen=new HashSet<>();Map<String,UiText> failures=new LinkedHashMap<>();boolean omapiFailed=false;
        for(UsbDevice d:usb.getDeviceList().values()){
            if(!usb.hasPermission(d))continue;boolean ccid=false;for(int i=0;i<d.getInterfaceCount();i++)ccid|=d.getInterface(i).getInterfaceClass()==11;if(!ccid)continue;
            String name="USB-"+d.getVendorId()+"-"+d.getProductId()+"-"+d.getDeviceId()+"-slot0";seen.add(name);
            UsbRecovery recovery=usbRecovery.computeIfAbsent(name,ignored->new UsbRecovery());
            if(recheck)recovery.recheck();
            try{Entry e=entries.get(name);if(e!=null){UsbCard c=(UsbCard)e.card;c.status();if(c.insertion.get()!=e.insertion){remove(name);e=null;}else repairMetadata(e,explicitMetadata);}
                if(e==null&&!canRecover.getAsBoolean()){failures.put(name,UiText.of(R.string.reader_usb_recovery_busy));continue;}
                if(e==null&&entries.size()<8){UsbCard card=new UsbCard(usb,d);e=new Entry(name,card);entries.put(name,e);discover(e);e.insertion=card.insertion.get();}
                if(e!=null)recovery.healthy(android.os.SystemClock.elapsedRealtime());
            }catch(Exception failure){remove(name);
                UiText detail=failure instanceof UsbCard.WriteFailure?UiText.of(R.string.reader_usb_write_failed,((UsbCard.WriteFailure)failure).transferred):UiText.of(R.string.reader_usb_unavailable);
                if(failure instanceof UsbCard.TransportFailure){
                    UsbCard.TransportFailure transport=(UsbCard.TransportFailure)failure;
                    if(!(failure instanceof UsbCard.WriteFailure))detail=UiText.of(transport.phase.equals("read")?R.string.reader_usb_read_failed:R.string.reader_usb_native_failed,transport.transferred);
                }
                if(!recovery.failureReported){
                    recovery.failureReported=true;
                    String diagnostic="USB first scan failure class="+failure.getClass().getSimpleName();
                    if(failure instanceof UsbCard.TransportFailure){
                        UsbCard.TransportFailure transport=(UsbCard.TransportFailure)failure;
                        diagnostic+=" phase="+transport.phase+" command="+transport.command+" result="+transport.transferred+" elapsed_ms="+transport.elapsedMillis;
                    }
                    android.util.Log.i("MDDUSB",diagnostic);
                }
                boolean transportFailure=failure instanceof UsbCard.TransportFailure&&!"native_library".equals(((UsbCard.TransportFailure)failure).phase);
                if(canRecover.getAsBoolean()&&recovery.failed(transportFailure,android.os.SystemClock.elapsedRealtime())){
                    try{
                        UsbCard card=UsbRecovery.reset(usb,d);Entry restored=new Entry(name,card);entries.put(name,restored);
                        discover(restored);restored.insertion=card.insertion.get();
                        recovery.resetResult=0;recovery.resetStage="";recovery.healthy(android.os.SystemClock.elapsedRealtime());
                        android.util.Log.i("MDDUSB","USB port recovery result=0 stage=identified");
                        continue;
                    }catch(Exception recoveryFailure){
                        remove(name);UsbRecovery.Failure reason=UsbRecovery.Failure.at("identity",recoveryFailure);
                        recovery.resetResult=reason.code;recovery.resetStage=reason.stage;
                        Throwable cause=reason.getCause()==null?reason:reason.getCause();
                        android.util.Log.i("MDDUSB","USB port recovery result="+reason.code+" stage="+reason.stage+" failure="+cause.getClass().getSimpleName()+" retry_pending="+recovery.retryPending());
                    }
                }
                if(recovery.resetResult!=0){
                    boolean unsupported="device_shape".equals(recovery.resetStage)||"native_library".equals(recovery.resetStage);
                    int message=unsupported?R.string.reader_usb_recovery_unsupported:recovery.retryPending()?R.string.reader_usb_recovery_retrying:R.string.reader_usb_recovery_failed;
                    detail=UiText.of(R.string.reader_scan_details,detail,UiText.of(message,recovery.resetResult,recovery.resetStage));
                }else if(recovery.retryPending()||recovery.exhausted()){
                    int message=recovery.retryPending()?R.string.reader_usb_recovery_unstable_retrying:R.string.reader_usb_recovery_unstable_stopped;
                    detail=UiText.of(R.string.reader_scan_details,detail,UiText.of(message));
                }
                failures.put(name,detail);
            }
        }
        usbRecovery.keySet().retainAll(seen);
        if(se!=null&&se.isConnected())for(Reader reader:se.getReaders()){
            String name="OMAPI-"+reader.getName();seen.add(name);
            try{Entry e=entries.get(name);if(!reader.isSecureElementPresent()){remove(name);continue;}
                if(e==null&&entries.size()<8){e=new Entry(name,new OmapiCard(reader));entries.put(name,e);discover(e);}else if(e!=null)repairMetadata(e,explicitMetadata);
            }catch(Exception failure){remove(name);omapiFailed=true;}
        }
        for(String name:new ArrayList<>(entries.keySet()))if(!seen.contains(name))remove(name);
        usbFailures=failures;
        diagnostic=entries.isEmpty()?UiText.EMPTY:UiText.of(R.string.reader_count,entries.size());
        for(UiText failure:failures.values())diagnostic=diagnostic.empty()?failure:UiText.of(R.string.reader_scan_details,diagnostic,failure);
        if(omapiFailed){UiText failure=UiText.of(R.string.reader_omapi_blocked);diagnostic=diagnostic.empty()?failure:UiText.of(R.string.reader_scan_details,diagnostic,failure);}
        if(diagnostic.empty())diagnostic=UiText.of(R.string.reader_permission_missing);
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
    private static final class ReadbackFailure extends java.io.IOException {
        final String code;
        ReadbackFailure(String code){super(code);this.code=code;}
    }
    synchronized JSONObject readback(JSONObject q,String process,long deadline,java.util.function.BooleanSupplier current){
        JSONObject result=Json.obj("scope",q.optString("scope"),"operation_id",q.optString("operation_id"),
            "process_generation",q.optString("process_generation"),"reader_name",q.optString("reader_name"),
            "card_id",q.optString("card_id"),"sim_session_generation",q.optString("sim_session_generation"),
            "state","failed","error_code","invalid_reader_readback_request");
        try{
            Set<String> fields=new HashSet<>(Arrays.asList("scope","operation_id","process_generation","reader_name","card_id","sim_session_generation"));
            for(Iterator<String> keys=q.keys();keys.hasNext();){String key=keys.next();if(!fields.remove(key)||!(q.opt(key) instanceof String))return result;}
            fields.remove("scope"); // Omitted scope is the old full-read request, not SIM-only.
            if(!fields.isEmpty()||!q.getString("operation_id").matches("[A-Za-z0-9_.:-]{1,128}")||
                !q.getString("process_generation").matches("[A-Za-z0-9_.:-]{1,128}")||
                !q.getString("sim_session_generation").matches("[A-Za-z0-9_.:-]{1,128}")||
                !q.getString("card_id").matches("[0-9]{1,64}"))return result;
            if(!q.optString("scope").equals("sim_identity"))return result.put("error_code","reader_readback_scope_unsupported");
            if(!q.getString("process_generation").equals(process))return result.put("error_code","reader_readback_process_changed");
            Entry entry=entries.get(q.getString("reader_name"));
            if(closed||entry==null||!entry.id.equals(q.getString("card_id"))||!entry.generation.equals(q.getString("sim_session_generation")))
                return result.put("error_code","reader_readback_identity_stale");
            // Guard every fixed APDU and the final publication, not only the socket reply.
            // An in-flight native transfer may finish after cancellation; its facts cannot commit.
            final ReadbackFailure[] transport={null};
            RunnableCheck check=()->{
                if(transport[0]!=null)throw transport[0];
                if(!current.getAsBoolean()||Thread.currentThread().isInterrupted()||android.os.SystemClock.elapsedRealtime()>=deadline)
                    throw new ReadbackFailure("reader_readback_interrupted");
                if(closed||entries.get(entry.name)!=entry||!entry.generation.equals(q.optString("sim_session_generation"))||
                    entry.card instanceof UsbCard&&((UsbCard)entry.card).insertion.get()!=entry.insertion)
                    throw new ReadbackFailure("reader_readback_identity_stale");
            };
            SimProtocol.Card guarded=new SimProtocol.Card(){
                public byte[] transmit(byte[] command)throws Exception{
                    check.run();byte[] response;
                    try{response=entry.card.transmit(command);}catch(Exception failure){transport[0]=new ReadbackFailure("reader_readback_transport_failed");throw transport[0];}
                    check.run();return response;
                }
                public void select(String application)throws Exception{
                    check.run();
                    if(entry.card instanceof OmapiCard)entry.card.select(application);
                    else SimProtocol.selectApplication(this,application);
                    check.run();
                }
                public void close(){}
            };
            check.run();
            if(!SimProtocol.iccid(guarded).equals(entry.id))throw new ReadbackFailure("reader_readback_identity_changed");
            JSONObject observed=SimProtocol.identity(guarded);
            if(!observed.getString("card_id").equals(entry.id)||!SimProtocol.iccid(guarded).equals(entry.id))
                throw new ReadbackFailure("reader_readback_identity_changed");
            check.run();
            JSONObject sim=observed.getJSONObject("sim");
            entry.sim=sim;
            result.put("reader",Json.obj("reader_name",entry.name,"card_present",true,"session_generation",entry.generation,
                "card_id",entry.id,"identity_state","identified","sim",sim));
            result.put("state","applied");result.remove("error_code");return result;
        }catch(ReadbackFailure failure){return Json.obj("scope",q.optString("scope"),"operation_id",q.optString("operation_id"),
            "process_generation",q.optString("process_generation"),"reader_name",q.optString("reader_name"),
            "card_id",q.optString("card_id"),"sim_session_generation",q.optString("sim_session_generation"),"state","unknown","error_code",failure.code);
        }catch(Exception failure){try{result.put("state","unknown").put("error_code","reader_readback_failed");}catch(JSONException ignored){}return result;}
    }
    private interface RunnableCheck {void run()throws ReadbackFailure;}
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
