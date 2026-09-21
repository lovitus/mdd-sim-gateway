package com.lovitus.mddagent;
import android.app.*;
import android.content.*;
import android.content.pm.ServiceInfo;
import android.hardware.usb.*;
import android.net.*;
import android.os.*;
import org.json.*;
import java.util.*;
import java.util.concurrent.*;
import java.util.concurrent.atomic.AtomicBoolean;

/** One process owner for foreground availability, reader I/O and the current call. */
public final class AgentService extends Service {
    public static final String START="com.lovitus.mddagent.START", PAUSE="com.lovitus.mddagent.PAUSE";
    public final class LocalBinder extends Binder {AgentService service(){return AgentService.this;}}
    final ScheduledExecutorService loop=Executors.newSingleThreadScheduledExecutor();
    private final ExecutorService readerIO=Executors.newSingleThreadExecutor();
    final ExecutorService io=Executors.newFixedThreadPool(4);
    private final Handler main=new Handler(Looper.getMainLooper());
    private final CopyOnWriteArrayList<Runnable> listeners=new CopyOnWriteArrayList<>();
    private final LinkedHashSet<String> announced=new LinkedHashSet<>();
    private final AtomicBoolean scanPending=new AtomicBoolean();
    private final String process=Json.id();
    private ConnectivityManager connectivity;private ConnectivityManager.NetworkCallback networkCallback;
    private ConfigStore store;private volatile JSONObject config;private volatile GatewayApi api;private volatile Link observer,agent;private volatile ReaderHub hub;
    private volatile boolean destroyed,available,sharing,online;private boolean foreground;
    volatile JSONObject snapshot=Json.obj("lines",new JSONArray(),"messages",new JSONArray(),"cellular_calls",new JSONArray());
    volatile String connection="Paused",readerStatus="Reader sharing off",notice="";volatile RemoteCall call;
    private final AtomicBoolean smsPending=new AtomicBoolean();
    private final BroadcastReceiver usbReceiver=new BroadcastReceiver(){public void onReceive(Context c,Intent i){refreshReaders();}};
    public IBinder onBind(Intent intent){return new LocalBinder();}
    @Override public void onCreate(){super.onCreate();store=new ConfigStore(this);config=store.load();
        NotificationManager n=getSystemService(NotificationManager.class);
        n.createNotificationChannel(new NotificationChannel("availability","Gateway availability",NotificationManager.IMPORTANCE_LOW));
        NotificationChannel calls=new NotificationChannel("incoming","Gateway calls and messages",NotificationManager.IMPORTANCE_HIGH);calls.setLockscreenVisibility(Notification.VISIBILITY_PRIVATE);n.createNotificationChannel(calls);
        connectivity=getSystemService(ConnectivityManager.class);
        networkCallback=new ConnectivityManager.NetworkCallback(){@Override public void onAvailable(Network n){networkChanged();}@Override public void onLost(Network n){networkChanged();}};
        connectivity.registerDefaultNetworkCallback(networkCallback);
        IntentFilter f=new IntentFilter(getPackageName()+".USB_PERMISSION");f.addAction(UsbManager.ACTION_USB_DEVICE_ATTACHED);f.addAction(UsbManager.ACTION_USB_DEVICE_DETACHED);
        if(Build.VERSION.SDK_INT>=33)registerReceiver(usbReceiver,f,Context.RECEIVER_NOT_EXPORTED);else registerReceiver(usbReceiver,f);
        loop.scheduleWithFixedDelay(()->{if(observer!=null)observer.checkFreshness();if(available&&sharing){Link link=agent;if(link!=null)link.health(android.os.SystemClock.elapsedRealtime()-lastReaderAt>20000?Json.obj("reader_condition","recovering","reader_detail","Reader observation in progress","readers",new JSONArray(),"modem_condition","disabled"):lastReaders);refreshReaders();}},1,10,TimeUnit.SECONDS);
    }
    @Override public int onStartCommand(Intent intent,int flags,int id){
        if(intent!=null&&PAUSE.equals(intent.getAction())){pause();return START_NOT_STICKY;}
        if(intent!=null&&START.equals(intent.getAction())||intent==null&&store.load().optBoolean("available")){
            try{if(call!=null)return START_STICKY;config=store.load();available=true;sharing=config.optBoolean("share");promote(false);configure();}catch(Exception failure){connection="Unable to start availability";available=false;stopForeground(STOP_FOREGROUND_REMOVE);foreground=false;stopSelf();changed();}
        }
        return available?START_STICKY:START_NOT_STICKY;
    }
    private void configure()throws Exception{
        closeLinks();snapshot=Json.obj("lines",new JSONArray(),"messages",new JSONArray(),"cellular_calls",new JSONArray());if(api!=null){api.close();api=null;}
        if(config.optString("token").isEmpty()){connection="Sign in required";changed();return;}
        api=new GatewayApi(new Endpoint(config.getString("server"),config.optString("pin")),config.getString("token"),config.getString("csrf"));
        observer=new Link(api,loop,"/v1/mobile/ws","","",process,new Link.Events(){
            public void state(String text,boolean yes){connection=text;online=yes;changed();}
            public void message(JSONObject message){if(message.optString("type").equals("mobile.snapshot")){snapshot=Json.object(message,"data");notifyEvents();changed();}}
        });observer.connect();
        if(sharing&&!config.optString("agent_token").isEmpty())startReaderLink();
        else if(sharing){sharing=false;readerStatus="Enroll reader access in Readers";}
        changed();
    }
    private void startReaderLink(){
        if(hub==null||hub.closed)hub=new ReaderHub(this);
        agent=new Link(api,loop,"/v1/agent/ws",config.optString("agent_id"),config.optString("agent_token"),process,new Link.Events(){
            public void state(String text,boolean connected){readerStatus=text;changed();if(connected){Link link=agent;if(link!=null)link.health(lastReaders);refreshReaders();}}
            public void message(JSONObject message){if(message.optString("kind").equals("aka_request")){
                final Link owner=agent;final ReaderHub device=hub;if(owner==null||device==null)return;final long generation=owner.generation();
                readerIO.execute(()->{JSONObject answer=device.authenticate(Json.object(message,"aka_request"));owner.respond(generation,message.optString("request_id"),answer);});
            }}
        });agent.connect();refreshReaders();
    }
    void refreshReaders(){if(destroyed||!available||!sharing||hub==null||!scanPending.compareAndSet(false,true))return;ReaderHub owner=hub;
        readerIO.execute(()->{try{owner.scan();if(owner==hub&&available&&sharing){Link link=agent;if(link!=null)link.health(owner.topology());lastReaders=owner.topology();lastReaderAt=android.os.SystemClock.elapsedRealtime();readerStatus=owner.diagnostic;changed();}}finally{scanPending.set(false);}});
    }
    JSONObject readers(){ReaderHub h=hub;return h==null?Json.obj("readers",new JSONArray()):lastReaders;}
    volatile long lastReaderAt;
    volatile JSONObject lastReaders=Json.obj("reader_condition","ready","readers",new JSONArray(),"modem_condition","disabled");
    void shareReaders(boolean enabled){if(call!=null){notice="Finish the call before changing reader sharing";changed();return;}if(!available||api==null){notice="Connect first";changed();return;}
        if(!enabled){sharing=false;save("share",false);if(agent!=null)agent.close();agent=null;ReaderHub old=hub;hub=null;if(old!=null)readerIO.execute(old::close);readerStatus="Reader sharing off";promote(false);changed();return;}
        notice="Enrolling reader access…";changed();final GatewayApi owner=api;
        io.execute(()->{try{String token=config.optString("agent_token"),id=config.optString("agent_id");if(id.isEmpty())id="android-"+Json.id();if(token.isEmpty()){JSONObject response=owner.json("POST","/api/auth/agent-credentials",Json.obj("action","issue","agent_id",id));token=response.getString("agent_token");}
            final String credential=token,identity=id;main.post(()->{if(!available||api!=owner)return;save("agent_id",identity);save("agent_token",credential);save("share",true);sharing=true;promote(false);startReaderLink();notice="Reader sharing enabled. Grant USB access if prompted.";changed();});
        }catch(Exception e){notice="Reader enrollment failed: "+RemoteCall.safe(e);changed();}});
    }
    synchronized void save(String key,Object value){try{config.put(key,value);store.save(config);}catch(Exception failure){notice="Could not save private configuration";}}
    JSONObject config(){return store.load();}
    boolean online(){return available&&online;}
    boolean available(){return available;}
    boolean sharing(){return sharing;}
    void addListener(Runnable l){listeners.addIfAbsent(l);}
    void removeListener(Runnable l){listeners.remove(l);}
    void changed(){main.post(()->{if(destroyed)return;for(Runnable l:listeners)l.run();if(foreground&&(Build.VERSION.SDK_INT<33||checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS)==android.content.pm.PackageManager.PERMISSION_GRANTED))getSystemService(NotificationManager.class).notify(1,notification());});}
    private PendingIntent openIntent(int id){return PendingIntent.getActivity(this,id,new Intent(this,MainActivity.class).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),PendingIntent.FLAG_UPDATE_CURRENT|PendingIntent.FLAG_IMMUTABLE);}
    private Notification notification(){return new Notification.Builder(this,"availability").setSmallIcon(com.lovitus.mddagent.R.drawable.ic_agent).setContentTitle(getString(R.string.app_name)).setContentText(call!=null?call.state:connection+(sharing?" · readers shared":"")).setContentIntent(openIntent(1)).setOngoing(true).setCategory(Notification.CATEGORY_SERVICE).setVisibility(Notification.VISIBILITY_PRIVATE).addAction(new Notification.Action.Builder(null,getString(R.string.pause),PendingIntent.getService(this,1,new Intent(this,AgentService.class).setAction(PAUSE),PendingIntent.FLAG_UPDATE_CURRENT|PendingIntent.FLAG_IMMUTABLE)).build()).build();}
    private void promote(boolean microphone){int type=0;if(Build.VERSION.SDK_INT>=34)type=ServiceInfo.FOREGROUND_SERVICE_TYPE_REMOTE_MESSAGING|(sharing?ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE:0);if(microphone&&Build.VERSION.SDK_INT>=30)type|=ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE;
        if(Build.VERSION.SDK_INT>=29)startForeground(1,notification(),type);else startForeground(1,notification());foreground=true;}
    void begin(CallPlan plan)throws Exception{if(!online()||api==null)throw new IllegalStateException("Gateway is offline");if(call!=null)throw new IllegalStateException("Resolve the existing call first");
        boolean current=false;JSONArray live=Json.array(snapshot,"lines");for(int i=0;i<live.length();i++){JSONObject l=live.optJSONObject(i);if(l!=null&&l.optString("id").equals(plan.line)&&l.optString("card_id").equals(plan.card)&&l.optBoolean("enabled"))current=true;}if(!current)throw new IllegalStateException("Line identity changed; select it again");
        promote(true);call=new RemoteCall(this,api,plan,io);call.wake=getSystemService(PowerManager.class).newWakeLock(PowerManager.PARTIAL_WAKE_LOCK,"mdd:active-call");call.wake.acquire(2*60*60*1000L);call.start();changed();}
    void microphoneFinished(RemoteCall owner){main.post(()->{releaseWake(owner);if(available&&(call==owner||call==null))promote(false);});}
    private void releaseWake(RemoteCall owner){if(owner.wake!=null&&owner.wake.isHeld())owner.wake.release();owner.wake=null;}
    void callAudioEnded(RemoteCall owner){microphoneFinished(owner);changed();}
    void clearCall(RemoteCall owner){main.post(()->{if(call==owner){releaseWake(owner);call=null;changed();}});}
    void hangup(){RemoteCall c=call;if(c!=null)c.hangup();}
    void reject(JSONObject line,String mode,JSONObject incoming){if(!online()||api==null)return;final GatewayApi owner=api;io.execute(()->{try{
        JSONObject b=Json.obj("operation_id",Json.id());if(mode.equals("cellular"))b.put("incoming_event_id",incoming.getString("incoming_event_id")).put("expected_card_id",incoming.getString("card_id")).put("sim_session_generation",incoming.getString("sim_session_generation")).put("native_call_index",incoming.getInt("native_call_index")).put("call_occurrence",incoming.getLong("occurrence"));else b.put("call_id",incoming.getString("call_id")).put("reason_code","user_rejected");
        owner.json("POST","/v1/lines/"+CallPlan.encode(line.getString("id"))+"/"+mode+"/calls/"+(mode.equals("cellular")?"reject":"incoming/reject"),b);notice="Declined";
    }catch(Exception e){notice="Decline not confirmed; refresh call state";}changed();});}
    void sendSMS(JSONObject line,String mode,String to,String body)throws Exception{
        if(!online()||api==null)throw new IllegalStateException("Gateway is offline");if(!smsPending.compareAndSet(false,true))throw new IllegalStateException("Message submission already in progress");
        final String id=Json.id();final JSONObject payload;try{payload=CallPlan.sms(line.getString("card_id"),to,body,id);}catch(Exception e){smsPending.set(false);throw e;}
        final String path="/v1/lines/"+CallPlan.encode(line.getString("id"))+"/"+mode+(mode.equals("cellular")?"/messages":"/messages/send");final GatewayApi owner=api;
        // Durable uncertainty marker before side effects, never an automatic resend queue.
        synchronized(this){config.put("last_sms",Json.obj("operation_id",id,"line_id",line.getString("id"),"transport",mode,"state","unknown"));try{store.save(config);}catch(Exception e){smsPending.set(false);throw e;}}
        notice="Submitting once · "+id;changed();io.execute(()->{try{JSONObject result=owner.json("POST",path,payload);save("last_sms",Json.obj("operation_id",id,"state",result.optString("code","submitted")));notice="Submission returned: "+result.optString("code","submitted")+". Check delivery history.";}catch(Exception e){notice=getString(R.string.unknown_result)+" · "+id;}finally{smsPending.set(false);changed();}});
    }
    private void notifyEvents(){JSONArray lines=Json.array(snapshot,"lines");for(int i=0;i<lines.length();i++){JSONObject in=Json.object(lines.optJSONObject(i),"incoming");announce("call:"+in.optString("call_id"),!in.optString("call_id").isEmpty(),true);}
        JSONArray cell=Json.array(snapshot,"cellular_calls");for(int i=0;i<cell.length();i++){JSONObject c=cell.optJSONObject(i);if(c!=null)announce("cell:"+c.optString("incoming_event_id"),c.optBoolean("actionable"),true);}
        JSONArray messages=Json.array(snapshot,"messages");boolean first=!seenMessageSnapshot;for(int i=messages.length()-1;i>=0;i--){JSONObject m=messages.optJSONObject(i);if(m==null)continue;String id=m.optString("event_id",m.optString("id"));if(id.isEmpty())continue;if(first)announced.add("sms:"+id);else announce("sms:"+id,m.optString("kind").equals("received"),false);}seenMessageSnapshot=true;
        while(announced.size()>512)announced.remove(announced.iterator().next());
    }
    private boolean seenMessageSnapshot;
    private void announce(String id,boolean actionable,boolean isCall){if(Build.VERSION.SDK_INT>=33&&checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS)!=android.content.pm.PackageManager.PERMISSION_GRANTED)return;if(!actionable||!announced.add(id))return;Notification n=new Notification.Builder(this,"incoming").setSmallIcon(R.drawable.ic_agent).setContentTitle(getString(isCall?R.string.incoming_call:R.string.incoming_sms)).setContentText(getString(R.string.tap_open)).setContentIntent(openIntent(2)).setAutoCancel(true).setTimeoutAfter(isCall?60000:600000).setCategory(isCall?Notification.CATEGORY_CALL:Notification.CATEGORY_MESSAGE).setVisibility(Notification.VISIBILITY_PRIVATE).build();getSystemService(NotificationManager.class).notify(100+(id.hashCode()&0xffff),n);}
    private void networkChanged(){if(!available)return;Link o=observer,a=agent;if(o!=null)o.networkChanged();if(a!=null)a.networkChanged();RemoteCall c=call;if(c!=null&&c.audio!=null)c.audio.networkChanged();}
    private void closeLinks(){if(observer!=null)observer.close();if(agent!=null)agent.close();observer=agent=null;online=false;}
    void pause(){if(call!=null){notice="Hang up or resolve the current call before pausing";changed();return;}available=false;save("available",false);closeLinks();ReaderHub old=hub;hub=null;if(old!=null)readerIO.execute(old::close);if(api!=null){api.close();api=null;}connection="Paused";stopForeground(STOP_FOREGROUND_REMOVE);foreground=false;stopSelf();changed();}
    void logout(){if(call!=null){notice="Resolve the current call before signing out";changed();return;}pause();store.clear();config=new JSONObject();snapshot=Json.obj("lines",new JSONArray(),"messages",new JSONArray(),"cellular_calls",new JSONArray());announced.clear();seenMessageSnapshot=false;notice="Signed out. Reader credential can be revoked in gateway settings.";getSystemService(NotificationManager.class).cancelAll();changed();}
    @Override public void onDestroy(){destroyed=true;closeLinks();if(call!=null&&call.audio!=null)call.audio.close();if(call!=null)releaseWake(call);if(api!=null)api.close();try{connectivity.unregisterNetworkCallback(networkCallback);unregisterReceiver(usbReceiver);}catch(Exception ignored){}ReaderHub old=hub;if(old!=null)readerIO.execute(old::close);loop.shutdownNow();io.shutdownNow();readerIO.shutdown();super.onDestroy();}
}
