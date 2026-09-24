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
    public static final String START="com.lovitus.mddagent.START", RESTORE="com.lovitus.mddagent.RESTORE", PAUSE="com.lovitus.mddagent.PAUSE";
    public final class LocalBinder extends Binder {AgentService service(){return AgentService.this;}}
    final ScheduledExecutorService loop=Executors.newSingleThreadScheduledExecutor();
    private final ExecutorService readerIO=Executors.newSingleThreadExecutor();
    final ExecutorService io=Executors.newFixedThreadPool(4);
    final ExecutorService controlIO=Executors.newSingleThreadExecutor();
    private final Handler main=new Handler(Looper.getMainLooper());
    private final CopyOnWriteArrayList<Runnable> listeners=new CopyOnWriteArrayList<>();
    final AgentActivityLog activityLog=new AgentActivityLog();
    private boolean activityObserved,activityAvailable,activityOnline,activitySharing,activityReaderOnline,activityCallPresent;
    private int activityConnection=-1,activityNotice=-1;
    private String activityCallPhase="";
    private final LinkedHashSet<String> announced=new LinkedHashSet<>();
    private static final class ReaderScan {final ReaderHub hub;final Link link;final long epoch;ReaderScan(ReaderHub hub,Link link,long epoch){this.hub=hub;this.link=link;this.epoch=epoch;}}
    private final java.util.concurrent.atomic.AtomicReference<ReaderScan> scanOwner=new java.util.concurrent.atomic.AtomicReference<>();
    private volatile long readerEpoch;
    private final String process=Json.id();
    private ConnectivityManager connectivity;private ConnectivityManager.NetworkCallback networkCallback;
    private ConfigStore store;private volatile JSONObject config;private volatile GatewayApi api;private volatile Link observer,agent;private volatile ReaderHub hub;
    private volatile boolean destroyed,available,sharing,online;volatile boolean readerLinkOnline;private boolean foreground;
    volatile JSONObject snapshot=Json.obj("lines",new JSONArray(),"messages",new JSONArray(),"cellular_calls",new JSONArray());
    private JSONArray catalogNumbers=new JSONArray();
    private GatewayApi numbersRequest;
    private long numbersAttempt;
    volatile UiText connection=UiText.of(R.string.paused),readerStatus=UiText.of(R.string.sharing_off),notice=UiText.EMPTY;volatile RemoteCall call;
    private final AtomicBoolean smsPending=new AtomicBoolean(),enrollmentPending=new AtomicBoolean();
    private final AtomicBoolean loginPending=new AtomicBoolean();
    private final Retry loginRetry=new Retry();
    private ScheduledFuture<?> loginTimer;
    private final Set<String> messageChecks=ConcurrentHashMap.newKeySet();
    private ScheduledFuture<?> healthTask;
    private volatile long configurationEpoch;
    private volatile long sharingIntentEpoch;
    private volatile boolean intentSaving;
    private volatile boolean configurationLoaded;
    private final BroadcastReceiver usbReceiver=new BroadcastReceiver(){public void onReceive(Context c,Intent i){refreshReaders();changed();}};
    public IBinder onBind(Intent intent){return new LocalBinder();}
    @Override public void onCreate(){super.onCreate();store=new ConfigStore(this);config=new JSONObject();
        ConfigStore.intent(()->{try{JSONObject saved=store.load();main.post(()->{if(destroyed||configurationEpoch!=0)return;config=saved;sharing=saved.optBoolean("share");readerStatus=UiText.of(sharing?R.string.sharing_paused:R.string.sharing_off);changed();});}catch(Exception failure){main.post(()->{if(destroyed||configurationEpoch!=0)return;notice=UiText.of(R.string.settings_unavailable);changed();});}});
        NotificationManager n=getSystemService(NotificationManager.class);
        n.createNotificationChannel(new NotificationChannel("availability",getString(R.string.notification_availability),NotificationManager.IMPORTANCE_LOW));
        NotificationChannel calls=new NotificationChannel("incoming",getString(R.string.notification_events),NotificationManager.IMPORTANCE_HIGH);calls.setLockscreenVisibility(Notification.VISIBILITY_PRIVATE);n.createNotificationChannel(calls);
        connectivity=getSystemService(ConnectivityManager.class);
        networkCallback=new ConnectivityManager.NetworkCallback(){@Override public void onAvailable(Network n){networkChanged();}@Override public void onLost(Network n){networkChanged();}};
        connectivity.registerDefaultNetworkCallback(networkCallback);
        IntentFilter f=new IntentFilter(getPackageName()+".USB_PERMISSION");f.addAction(UsbManager.ACTION_USB_DEVICE_ATTACHED);f.addAction(UsbManager.ACTION_USB_DEVICE_DETACHED);
        if(Build.VERSION.SDK_INT>=33)registerReceiver(usbReceiver,f,Context.RECEIVER_NOT_EXPORTED);else registerReceiver(usbReceiver,f);
    }
    @Override public int onStartCommand(Intent intent,int flags,int id){
        if(intent!=null&&PAUSE.equals(intent.getAction())){pause();return START_NOT_STICKY;}
        if(intent==null||START.equals(intent.getAction())||RESTORE.equals(intent.getAction())){
            try{
                if(call!=null&&call.busy())return START_STICKY;
                invalidateCall();
                final long expected=++configurationEpoch;final boolean explicit=intent!=null&&START.equals(intent.getAction());available=true;intentSaving=true;notice=UiText.EMPTY;promote(false);
                ConfigStore.intent(()->{try{JSONObject loaded=explicit?store.update(current->current.put("available",true)):store.load();main.post(()->{
                    if(destroyed||expected!=configurationEpoch)return;
                    configurationLoaded=true;intentSaving=false;config=loaded;sharing=loaded.optBoolean("share");
                    if(!loaded.optBoolean("available")){available=false;connection=UiText.of(R.string.paused);readerStatus=UiText.of(sharing?R.string.sharing_paused:R.string.sharing_off);stopForeground(STOP_FOREGROUND_REMOVE);foreground=false;stopSelf();changed();return;}
                    try{configure();startHealth();}
                    catch(Exception e){connection=UiText.of(R.string.settings_unavailable);changed();}
                });}catch(Exception e){main.post(()->{if(destroyed||expected!=configurationEpoch)return;intentSaving=false;available=false;connection=UiText.of(R.string.settings_unavailable);stopForeground(STOP_FOREGROUND_REMOVE);foreground=false;stopSelf();changed();});}});
            }catch(Exception failure){intentSaving=false;connection=UiText.of(R.string.availability_failed);available=false;stopForeground(STOP_FOREGROUND_REMOVE);foreground=false;stopSelf();changed();}
        }
        return available?START_STICKY:START_NOT_STICKY;
    }
    private void startHealth(){
        if(healthTask!=null)healthTask.cancel(false);
        healthTask=loop.scheduleWithFixedDelay(()->{
            if(!available||destroyed)return;
            if(observer!=null)observer.checkFreshness();
            if(sharing){Link link=agent;if(link!=null)link.health(android.os.SystemClock.elapsedRealtime()-lastReaderAt>20000?recoveringReaders():lastReaders);refreshReaders();}
        },1,10,TimeUnit.SECONDS);
    }
    void prepareLogin(){
        if(accountBusy())throw new IllegalStateException(getString(R.string.account_busy));
        invalidateCall();
        configurationEpoch++;closeLinks();if(api!=null){api.close();api=null;}
        if(healthTask!=null)healthTask.cancel(false);
        snapshot=Json.obj("lines",new JSONArray(),"messages",new JSONArray(),"incoming_lines",new JSONArray(),"cellular_calls",new JSONArray());notice=UiText.EMPTY;
        connection=UiText.of(R.string.signin_original);changed();
    }
    private void configure()throws Exception{
        closeLinks();snapshot=Json.obj("lines",new JSONArray(),"messages",new JSONArray(),"cellular_calls",new JSONArray());if(api!=null){api.close();api=null;}
        catalogNumbers=new JSONArray();numbersRequest=null;numbersAttempt=0;
        call=null;
        if(config.optString("token").isEmpty()){connection=UiText.of(R.string.login_required);changed();return;}
        api=new GatewayApi(new Endpoint(config.getString("server"),config.optString("pin")),config.getString("token"),config.getString("csrf"));
        final GatewayApi connectionOwner=api;
        observer=new Link(api,loop,"/v1/mobile/ws","","",process,new Link.Events(){
            public void authenticationRequired(){main.post(()->renewSession(connectionOwner));}
            public void state(int label,boolean yes){if(api!=connectionOwner)return;boolean recovered=yes&&!online;connection=UiText.of(label);online=yes;if(recovered){main.post(()->{if(api!=connectionOwner)return;requestMessageSync(false);refreshLineNumbers(true);});RemoteCall c=call;if(c!=null&&(c.audio==null||c.audio.closed))c.reconcile();}changed();}
            public void message(JSONObject message){if(message.optString("type").equals("mobile.snapshot"))main.post(()->{
                if(api!=connectionOwner)return;snapshot=normalizeSnapshot(Json.object(message,"data"));notifyEvents();changed();
                JSONArray rows=Json.array(snapshot,"lines");
                for(int i=0;i<rows.length();i++){JSONObject row=rows.optJSONObject(i);if(row!=null&&!row.has("number")){refreshLineNumbers(false);break;}}
            });}
        });observer.connect();
        if(sharing&&!config.optString("agent_token").isEmpty())startReaderLink();
        else if(sharing){sharing=false;readerStatus=UiText.of(R.string.enroll_readers);}
        changed();
    }
    private JSONObject normalizeSnapshot(JSONObject data){
        JSONArray lines=Json.array(data,"lines"),calls=Json.array(data,"cellular_calls");
        for(int i=0;i<lines.length();i++){
            JSONObject line=lines.optJSONObject(i);if(line==null)continue;
            line.remove("number");
            for(int j=0;j<catalogNumbers.length();j++){
                JSONObject saved=catalogNumbers.optJSONObject(j);
                if(saved!=null&&!saved.optBoolean("deleted")&&line.optString("id").equals(saved.optString("id"))&&line.optString("card_id").equals(saved.optString("card_id"))){
                    try{line.put("number",Json.object(saved,"sim").optString("msisdn"));}catch(JSONException ignored){}break;
                }
            }
            for(int j=0;j<calls.length();j++){
                JSONObject event=calls.optJSONObject(j);
                if(event!=null&&event.optBoolean("actionable")&&line.optString("id").equals(event.optString("line_id")))
                    try{line.put("cellular_incoming",event);}catch(JSONException ignored){}
            }
        }
        return data;
    }
    void refreshLineNumbers(boolean explicit){
        final GatewayApi owner=api;long now=SystemClock.elapsedRealtime();
        if(owner==null||!online()||numbersRequest==owner||!explicit&&numbersAttempt!=0&&now-numbersAttempt<30000)return;
        numbersRequest=owner;numbersAttempt=now;
        io.execute(()->{try{
            JSONArray rows=Json.array(owner.json("GET","/v1/catalog/lines",null),"lines");
            main.post(()->{
                if(api!=owner||numbersRequest!=owner)return;numbersRequest=null;catalogNumbers=rows;
                try{snapshot=normalizeSnapshot(new JSONObject(snapshot.toString()));}catch(JSONException ignored){}
                changed();
            });
        }catch(Exception failure){main.post(()->{if(api==owner&&numbersRequest==owner)numbersRequest=null;});}});
    }
    private void renewSession(GatewayApi owner){
        if(!available||destroyed||api!=owner||owner==null)return;
        if(!loginPending.compareAndSet(false,true))return;
        final long epoch=configurationEpoch;final Link link=observer;
        io.execute(()->{
            GatewayApi candidate=null;
            try{
                JSONObject saved=store.load();LoginProfile login=LoginProfile.read(saved);
                if(!login.remember||login.password.isEmpty()||!login.endpoint().origin.equals(owner.endpoint.origin)||!login.username.equals(saved.optString("username")))
                    throw new IllegalStateException("Sign in required");
                candidate=new GatewayApi(owner.endpoint,"","");candidate.login(login.username,login.password);
                final String token=candidate.token,csrf=candidate.csrf;
                ConfigStore.intent(()->{try{
                    JSONObject updated=store.update(current->{
                        if(destroyed||!available||epoch!=configurationEpoch||api!=owner||observer!=link)throw new IllegalStateException("Connection replaced");
                        if(!owner.token.equals(current.optString("token")))throw new IllegalStateException("Account changed");
                        current.put("token",token).put("csrf",csrf);
                    });
                    main.post(()->{try{
                        if(destroyed||!available||epoch!=configurationEpoch||api!=owner||observer!=link)return;
                        config=updated;owner.token=token;owner.csrf=csrf;loginRetry.healthy();link.sessionRenewed();
                    }finally{loginPending.set(false);}});
                }catch(Exception failure){main.post(()->{loginPending.set(false);if(api==owner){connection=UiText.of(R.string.settings_save_failed);changed();}});}});
            }catch(Exception failure){
                boolean retry=failure instanceof java.io.IOException&&!Link.identityFailure(failure)&&
                    (!(failure instanceof GatewayApi.Failure)||((GatewayApi.Failure)failure).status>=500||((GatewayApi.Failure)failure).status==429||((GatewayApi.Failure)failure).status==408);
                main.post(()->{
                    loginPending.set(false);if(destroyed||!available||epoch!=configurationEpoch||api!=owner)return;
                    connection=UiText.of(retry?R.string.link_wait_network:R.string.link_auth_required);changed();
                    if(retry)loginTimer=loop.schedule(()->main.post(()->renewSession(owner)),loginRetry.next(),TimeUnit.MILLISECONDS);
                });
            }finally{if(candidate!=null)candidate.close();}
        });
    }
    private void startReaderLink(){
        if(agent!=null){agent.close();agent=null;}
        if(hub==null||hub.closed)hub=new ReaderHub(this);
        lastReaders=recoveringReaders();lastReaderAt=0;
        readerUSBFailures=Collections.emptyMap();
        final long epoch=++readerEpoch;scanOwner.set(null);readerLinkOnline=false;
        agent=new Link(api,loop,"/v1/agent/ws",config.optString("agent_id"),config.optString("agent_token"),process,new Link.Events(){
            public void state(int label,boolean connected){if(epoch!=readerEpoch||!sharing)return;readerLinkOnline=connected;readerStatus=UiText.of(label);changed();if(connected){Link link=agent;if(link!=null)link.health(android.os.SystemClock.elapsedRealtime()-lastReaderAt>20000?recoveringReaders():lastReaders);refreshReaders();}}
            public void message(JSONObject message){if(epoch!=readerEpoch||!sharing)return;if(message.optString("kind").equals("aka_request")){
                final Link owner=agent;final ReaderHub device=hub;if(owner==null||device==null)return;final long generation=owner.generation();
                readerIO.execute(()->{JSONObject answer=device.authenticate(Json.object(message,"aka_request"));owner.respond(generation,message.optString("request_id"),answer);});
            }}
        });agent.connect();refreshReaders();
    }
    void refreshReaders(){refreshReaders(false);}
    void refreshReaderMetadata(){refreshReaders(true);}
    private void refreshReaders(boolean explicitMetadata){
        if(destroyed||!available||!sharing)return;
        ReaderHub owner=hub;Link link=agent;long epoch=readerEpoch;
        if(owner==null)return;
        ReaderScan ticket=new ReaderScan(owner,link,epoch);
        if(!scanOwner.compareAndSet(null,ticket))return;
        try{readerIO.execute(()->{
            try{
                owner.scan(explicitMetadata);JSONObject fresh=owner.topology();Map<String,UiText> failures=owner.usbFailures();
                if(!main.post(()->{
                    if(scanOwner.get()!=ticket)return;
                    try{
                        if(ticket.epoch==readerEpoch&&ticket.hub==hub&&ticket.link==agent&&available&&sharing&&!owner.closed){
                            if(link!=null)link.health(fresh);lastReaders=fresh;readerUSBFailures=failures;lastReaderAt=android.os.SystemClock.elapsedRealtime();readerStatus=owner.diagnostic;changed();
                        }
                    }finally{scanOwner.compareAndSet(ticket,null);}
                }))scanOwner.compareAndSet(ticket,null);
            }catch(RuntimeException failure){
                owner.diagnostic=UiText.of(R.string.reader_usb_unavailable);
                if(!main.post(()->{
                    if(scanOwner.get()!=ticket)return;
                    try{
                        if(ticket.epoch==readerEpoch&&ticket.hub==hub&&ticket.link==agent&&available&&sharing&&!owner.closed){
                            lastReaders=recoveringReaders();lastReaderAt=0;readerStatus=owner.diagnostic;changed();
                        }
                    }finally{scanOwner.compareAndSet(ticket,null);}
                }))scanOwner.compareAndSet(ticket,null);
            }
        });}catch(RejectedExecutionException rejected){scanOwner.compareAndSet(ticket,null);}
    }
    JSONObject readers(){ReaderHub h=hub;return h==null?Json.obj("readers",new JSONArray()):lastReaders;}
    volatile long lastReaderAt;
    private static JSONObject recoveringReaders(){return Json.obj("reader_condition","recovering","reader_detail","Fresh reader observation required","readers",new JSONArray(),"modem_condition","disabled");}
    volatile JSONObject lastReaders=recoveringReaders();
    volatile Map<String,UiText> readerUSBFailures=Collections.emptyMap();
    void shareReaders(boolean enabled){if(call!=null){notice=UiText.of(R.string.sharing_call_busy);changed();return;}
        if(!enabled){
            readerUSBFailures=Collections.emptyMap();
            final long expected=++sharingIntentEpoch;readerEpoch++;scanOwner.set(null);sharing=false;readerLinkOnline=false;lastReaders=recoveringReaders();lastReaderAt=0;
            if(agent!=null)agent.close();agent=null;ReaderHub old=hub;hub=null;if(old!=null)readerIO.execute(old::close);
            readerStatus=UiText.of(R.string.sharing_off);notice=UiText.EMPTY;if(available)promote(false);changed();
            ConfigStore.intent(()->{try{JSONObject saved=store.update(current->current.put("share",false));main.post(()->{if(destroyed||expected!=sharingIntentEpoch)return;config=saved;changed();});}
                catch(Exception failure){main.post(()->{if(destroyed||expected!=sharingIntentEpoch)return;notice=UiText.of(R.string.sharing_save_failed);changed();});}});return;
        }
        if(!available||api==null){notice=UiText.of(R.string.connect_first);changed();return;}
        if(!enrollmentPending.compareAndSet(false,true)){notice=UiText.of(R.string.enrollment_busy);changed();return;}
        final long expected=++sharingIntentEpoch,configuration=configurationEpoch;
        notice=UiText.of(R.string.enrolling_readers);changed();final GatewayApi owner=api;
        io.execute(()->{try{String token=config.optString("agent_token"),id=config.optString("agent_id");if(id.isEmpty())id="android-"+Json.id();if(token.isEmpty()){JSONObject response=owner.json("POST","/api/auth/agent-credentials",Json.obj("action","issue","agent_id",id));token=response.getString("agent_token");}
            final String credential=token,identity=id;
            ConfigStore.intent(()->{try{
                if(destroyed||expected!=sharingIntentEpoch||configuration!=configurationEpoch||!available||api!=owner)return;
                JSONObject enrolled=store.update(current->{if(!owner.token.equals(current.optString("token")))throw new IllegalStateException("Account changed");current.put("agent_id",identity);current.put("agent_token",credential);current.put("share",true);});
                main.post(()->{if(destroyed||expected!=sharingIntentEpoch||configuration!=configurationEpoch||!available||api!=owner)return;config=enrolled;sharing=true;promote(false);startReaderLink();notice=UiText.EMPTY;changed();});
            }catch(Exception e){main.post(()->{if(destroyed||expected!=sharingIntentEpoch)return;notice=UiText.of(R.string.enrollment_failed,RemoteCall.safe(e));changed();});}
            finally{enrollmentPending.set(false);changed();}});
        }catch(Exception e){main.post(()->{if(expected==sharingIntentEpoch){notice=UiText.of(R.string.enrollment_failed,RemoteCall.safe(e));changed();}enrollmentPending.set(false);});}});
    }
    JSONObject config(){try{return new JSONObject(config.toString());}catch(Exception e){return new JSONObject();}}
    boolean online(){return available&&online;}
    boolean available(){return available;}
    boolean hasLoadedConfiguration(){return configurationLoaded;}
    boolean sharing(){return sharing;}
    boolean intentSaving(){return intentSaving||enrollmentPending.get();}
    boolean messageBusy(){return smsPending.get();}
    boolean accountBusy(){return call!=null&&call.busy()||smsPending.get()||enrollmentPending.get();}
    void addListener(Runnable l){listeners.addIfAbsent(l);}
    void removeListener(Runnable l){listeners.remove(l);}
    private synchronized void captureActivity(){
        String phase=call==null?"":call.phase;
        if(!activityObserved){activityObserved=true;activityAvailable=available;activityOnline=online;activitySharing=sharing;activityReaderOnline=readerLinkOnline;activityConnection=connection.resource;activityNotice=notice.resource;activityCallPresent=call!=null;activityCallPhase=phase;return;}
        if(activityAvailable!=available)activityLog.add(available?R.string.activity_agent_started:R.string.activity_agent_paused);
        if(activityOnline!=online)activityLog.add(online?R.string.activity_gateway_connected:R.string.activity_gateway_disconnected);
        if(activityConnection!=connection.resource&&connection.resource==R.string.link_upgrade)activityLog.add(R.string.activity_core_mobile_404);
        if(activityNotice!=notice.resource){int event=activityForNotice(notice.resource);if(event!=0)activityLog.add(event);}
        if(activitySharing!=sharing)activityLog.add(sharing?R.string.activity_reader_sharing_started:R.string.activity_reader_sharing_stopped);
        if(activityReaderOnline!=readerLinkOnline)activityLog.add(readerLinkOnline?R.string.activity_reader_connected:R.string.activity_reader_disconnected);
        if(activityCallPresent!= (call!=null))activityLog.add(call==null?R.string.activity_call_cleared:R.string.activity_call_recovery);
        else if(call!=null&&!phase.equals(activityCallPhase)){
            if(phase.equals("ACTIVE"))activityLog.add(R.string.activity_call_active);
            else if(phase.equals("ENDING"))activityLog.add(R.string.activity_call_ending);
            else if(phase.equals("TERMINAL"))activityLog.add(R.string.activity_call_terminal);
        }
        activityAvailable=available;activityOnline=online;activitySharing=sharing;activityReaderOnline=readerLinkOnline;activityConnection=connection.resource;activityNotice=notice.resource;activityCallPresent=call!=null;activityCallPhase=phase;
    }
    private static int activityForNotice(int resource){
        if(resource==R.string.message_saving)return R.string.activity_sms_started;
        if(resource==R.string.message_result_returned)return R.string.activity_sms_result_returned;
        if(resource==R.string.message_not_dispatched)return R.string.activity_sms_not_dispatched;
        if(resource==R.string.message_unknown_id)return R.string.activity_sms_unconfirmed;
        if(resource==R.string.message_sync_failed)return R.string.activity_sms_sync_failed;
        if(resource==R.string.message_sync_gap)return R.string.activity_sms_sync_gap;
        return 0;
    }
    void changed(){captureActivity();main.post(()->{if(destroyed)return;for(Runnable l:listeners)l.run();if(foreground&&(Build.VERSION.SDK_INT<33||checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS)==android.content.pm.PackageManager.PERMISSION_GRANTED))getSystemService(NotificationManager.class).notify(1,notification());});}
    private PendingIntent openIntent(int id){return PendingIntent.getActivity(this,id,new Intent(this,MainActivity.class).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),PendingIntent.FLAG_UPDATE_CURRENT|PendingIntent.FLAG_IMMUTABLE);}
    private Notification notification(){return new Notification.Builder(this,"availability").setSmallIcon(com.lovitus.mddagent.R.drawable.ic_agent).setContentTitle(getString(R.string.app_name)).setContentText(call!=null?call.state.render(this):connection.render(this)+(sharing?" · "+getString(R.string.readers_shared_suffix):"")).setContentIntent(openIntent(1)).setOngoing(true).setCategory(Notification.CATEGORY_SERVICE).setVisibility(Notification.VISIBILITY_PRIVATE).addAction(new Notification.Action.Builder(null,getString(R.string.pause),PendingIntent.getService(this,1,new Intent(this,AgentService.class).setAction(PAUSE),PendingIntent.FLAG_UPDATE_CURRENT|PendingIntent.FLAG_IMMUTABLE)).build()).build();}
    private void promote(boolean microphone){int type=0;if(Build.VERSION.SDK_INT>=34)type=ServiceInfo.FOREGROUND_SERVICE_TYPE_REMOTE_MESSAGING|(sharing?ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE:0);if(microphone&&Build.VERSION.SDK_INT>=30)type|=ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE;
        if(Build.VERSION.SDK_INT>=29)startForeground(1,notification(),type);else startForeground(1,notification());foreground=true;}
    void begin(CallPlan plan)throws Exception{if(!online()||api==null)throw new IllegalStateException(getString(R.string.connect_first));if(call!=null)throw new IllegalStateException(getString(R.string.account_busy));
        // The IO owner performs authoritative exact-line validation, including paged lines.
        promote(true);RemoteCall next=new RemoteCall(this,api,plan,io);call=next;
        try{next.wake=getSystemService(PowerManager.class).newWakeLock(PowerManager.PARTIAL_WAKE_LOCK,"mdd:active-call");next.wake.acquire(2*60*60*1000L);next.start();}
        catch(RuntimeException failure){next.invalidate();releaseWake(next);if(call==next)call=null;if(!destroyed&&available)promote(false);changed();throw failure;}
        changed();}
    boolean ownsCall(RemoteCall owner){return !destroyed&&call==owner;}
    private void invalidateCall(){RemoteCall old=call;if(old!=null)old.invalidate();}
    void microphoneFinished(RemoteCall owner){main.post(()->{releaseWake(owner);if(!destroyed&&available&&(call==owner||call==null))promote(false);});}
    private void releaseWake(RemoteCall owner){if(owner.wake!=null&&owner.wake.isHeld())owner.wake.release();owner.wake=null;}
    void callAudioEnded(RemoteCall owner){microphoneFinished(owner);if(owner.submitted)owner.reconcile();changed();}
    void reconcileSoon(RemoteCall owner){
        if(destroyed)return;
        try{loop.schedule(()->{if(available&&call==owner&&!owner.phase.equals("TERMINAL"))owner.reconcile();},1,TimeUnit.SECONDS);}catch(RejectedExecutionException ignored){}
    }
    void clearCall(RemoteCall owner,UiText message){main.post(()->{if(ownsCall(owner)){releaseWake(owner);notice=message;call=null;changed();}});}
    void hangup(){RemoteCall c=call;if(c!=null)c.hangup();}
    void reject(JSONObject line,String mode,JSONObject incoming,Runnable confirmed){if(!online()||api==null)return;final GatewayApi owner=api;io.execute(()->{try{
        JSONObject b=Json.obj("operation_id",Json.id());if(mode.equals("cellular"))b.put("incoming_event_id",incoming.getString("incoming_event_id")).put("expected_card_id",incoming.getString("card_id")).put("sim_session_generation",incoming.getString("sim_session_generation")).put("native_call_index",incoming.getInt("native_call_index")).put("call_occurrence",incoming.getLong("occurrence"));else b.put("call_id",incoming.getString("call_id")).put("reason_code","user_rejected");
        JSONObject result=owner.json("POST","/v1/lines/"+CallPlan.encode(line.getString("id"))+"/"+mode+"/calls/"+(mode.equals("cellular")?"reject":"incoming/reject"),b);
        boolean exact=mode.equals("cellular")?"cellular_incoming_rejected".equals(result.optString("code"))&&incoming.getString("incoming_event_id").equals(result.optString("incoming_event_id")):
            result.optBoolean("accepted")&&"rejected".equals(result.optString("code"))&&incoming.getString("call_id").equals(result.optString("call_id"))&&b.getString("operation_id").equals(result.optString("operation_id"));
        if(!exact)throw new IllegalStateException("Incoming rejection not confirmed");notice=UiText.of(R.string.declined);main.post(()->{if(api==owner)confirmed.run();});
    }catch(Exception e){notice=UiText.of(R.string.decline_unknown);}changed();});}
    void sendSMS(JSONObject line,String mode,String to,String body)throws Exception{
        if(!online()||api==null)throw new IllegalStateException(getString(R.string.connect_first));if(!smsPending.compareAndSet(false,true))throw new IllegalStateException(getString(R.string.message_busy));
        final String id=Json.id();final JSONObject payload;try{payload=CallPlan.sms(line.getString("card_id"),to,body,id);}catch(Exception e){smsPending.set(false);throw e;}
        final String path="/v1/lines/"+CallPlan.encode(line.getString("id"))+"/"+mode+(mode.equals("cellular")?"/messages":"/messages/send");final GatewayApi owner=api;
        // Durable uncertainty marker before side effects, never an automatic resend queue.
        notice=UiText.of(R.string.message_saving);changed();io.execute(()->{
            String scope="";boolean recorded=false,dispatch=false;
            try{
                scope=owner.authenticatedScope();GatewayApi.requireReady(owner.exactLine(line.getString("id"),line.getString("card_id")),mode+"_sms");final String account=scope;
                store.update(current->{if(!owner.token.equals(current.optString("token")))throw new IllegalStateException("Account changed");MessageJournal.begin(current,account,id,line,mode,payload.getString("recipient"),payload.getString("body"));current.put("account_scope",account);});
                recorded=true;
                refreshPrivateState(owner);
                if(api!=owner||!available)throw new IllegalStateException("Connection owner changed");
                owner.exactLine(line.getString("id"),line.getString("card_id"));
                dispatch=true;
                JSONObject result=owner.json("POST",path,payload);
                store.update(current->MessageJournal.response(current,account,id,result));
                if(api==owner)notice=UiText.of(R.string.message_result_returned);
            }catch(Exception e){
                if(recorded&&!dispatch){final String account=scope;try{store.update(current->MessageJournal.notDispatched(current,account,id));}catch(Exception ignored){}}
                if(api==owner)notice=dispatch?UiText.of(R.string.message_unknown_id,id):UiText.of(R.string.message_not_dispatched);
            }finally{smsPending.set(false);refreshPrivateState(owner);}
        });
    }

    private void refreshPrivateState(GatewayApi owner){ConfigStore.intent(()->{try{JSONObject saved=store.load();main.post(()->{if(destroyed||api!=owner)return;config=saved;changed();});}catch(Exception failure){main.post(()->{if(api==owner){notice=UiText.of(R.string.message_state_unreadable);changed();}});}});}
    long accountEpoch(){return configurationEpoch;}
    boolean canQueryMessages(){return available&&!destroyed&&api!=null;}
    boolean messageChecking(String scope,String operation){return messageChecks.contains(scope+"\n"+operation);}
    void reconcileMessage(String scope,String operation){
        final GatewayApi owner=api;if(!canQueryMessages()||owner==null)return;
        final String key=scope+"\n"+operation;if(!messageChecks.add(key))return;
        io.execute(()->{try{
            if(!scope.equals(owner.authenticatedScope()))throw new IllegalStateException("Account changed");
            JSONObject original=MessageJournal.find(store.load(),scope,operation);
            JSONObject page=owner.json("GET","/v1/messages?line_id="+CallPlan.encode(original.getString("line_id"))+"&transport="+original.getString("transport")+"&limit=100",null);
            store.update(current->{requireMessageOwner(current,owner);MessageJournal.observe(current,scope,Json.array(page,"messages"));});
            notice=UiText.of(R.string.message_retained_unknown);
        }catch(Exception failure){notice=UiText.of(R.string.history_failed);}
        finally{messageChecks.remove(key);refreshPrivateState(owner);}});
    }

    void directory(String query,String after,java.util.function.Consumer<JSONObject> success,java.util.function.Consumer<String> failure){
        final GatewayApi owner=api;if(owner==null){failure.accept(getString(R.string.connect_first));return;}
        io.execute(()->{try{JSONObject page=owner.directory(query);main.post(()->{if(api==owner)success.accept(page);});}catch(Exception e){main.post(()->{if(api==owner)failure.accept(getString(R.string.directory_failed));});}});
    }
    void incomingPage(String after,java.util.function.Consumer<JSONObject> success,java.util.function.Consumer<String> failure){
        if(!online()){failure.accept(getString(R.string.connect_first));return;}
        success.accept(Json.obj("lines",Json.array(snapshot,"lines"),"next_after",""));
    }
    void messageHistory(String line,String mode,String peer,String before,long expectedEpoch,java.util.function.Consumer<JSONObject> success,java.util.function.Consumer<String> failure){
        messagePageRequest("/v1/messages?page=true&line_id="+CallPlan.encode(line)+"&transport="+mode+"&peer="+CallPlan.encode(peer)+"&limit=50&before="+CallPlan.encode(before),expectedEpoch,success,failure);
    }
    void callHistory(long expectedEpoch,java.util.function.Consumer<JSONObject> success,java.util.function.Consumer<String> failure){
        messagePageRequest("/v1/calls?limit=100",expectedEpoch,success,failure);
    }
    void messageConversations(long expectedEpoch,java.util.function.Consumer<JSONObject> success,java.util.function.Consumer<String> failure){messagePageRequest("/v1/messages/conversations?all=true",expectedEpoch,success,failure);}
    private void messagePageRequest(String path,long expectedEpoch,java.util.function.Consumer<JSONObject> success,java.util.function.Consumer<String> failure){
        final GatewayApi owner=api;if(owner==null||configurationEpoch!=expectedEpoch){failure.accept(getString(R.string.history_account_changed));return;}
        io.execute(()->{try{JSONObject page=owner.json("GET",path,null);main.post(()->{if(api==owner&&configurationEpoch==expectedEpoch)success.accept(page);else failure.accept(getString(R.string.history_account_changed));});}catch(Exception e){main.post(()->failure.accept(getString(api==owner&&configurationEpoch==expectedEpoch?R.string.history_failed:R.string.history_account_changed)));}});
    }
    private boolean seenMessages;
    private void notifyEvents(){
        JSONArray lines=Json.array(snapshot,"lines");
        for(int i=0;i<lines.length();i++){
            JSONObject line=lines.optJSONObject(i);if(line==null)continue;
            JSONObject event=Json.object(line,"incoming");
            announce("call:"+event.optString("call_id"),!event.optString("call_id").isEmpty(),true);
        }
        JSONArray cellular=Json.array(snapshot,"cellular_calls");
        for(int i=0;i<cellular.length();i++){
            JSONObject event=cellular.optJSONObject(i);if(event==null)continue;
            announce("cell:"+event.optString("incoming_event_id"),event.optBoolean("actionable"),true);
        }
        JSONArray messages=Json.array(snapshot,"messages");
        synchronized(announced){
            for(int i=messages.length()-1;i>=0;i--){
                JSONObject message=messages.optJSONObject(i);if(message==null)continue;
                String id=message.optString("event_id",message.optString("id"));if(id.isEmpty())continue;
                if(!seenMessages)announced.add("sms:"+id);
                else announce("sms:"+id,"received".equals(message.optString("kind")),false);
            }
            seenMessages=true;
            while(announced.size()>512)announced.remove(announced.iterator().next());
        }
    }
    void syncMessages(){requestMessageSync(true);}
    private void requestMessageSync(boolean manual){
        if(!manual)return; // Live updates already arrive through the existing mobile snapshot.
        final GatewayApi owner=api;if(!canQueryMessages()||owner==null)return;
        io.execute(()->{try{
            JSONObject result=owner.json("GET","/v1/messages?limit=50",null);
            main.post(()->{if(api!=owner||!available)return;try{
                JSONObject next=new JSONObject(snapshot.toString());next.put("messages",Json.array(result,"messages"));snapshot=next;notifyEvents();changed();
            }catch(JSONException failure){notice=UiText.of(R.string.history_failed);changed();}});
        }catch(Exception failure){main.post(()->{if(api==owner){notice=UiText.of(R.string.history_failed);changed();}});}});
    }
    private void requireMessageOwner(JSONObject current,GatewayApi owner){
        if(destroyed||!available||api!=owner||owner.token.isEmpty()||!owner.token.equals(current.optString("token")))throw new IllegalStateException("Account changed");
    }
    private PendingIntent eventIntent(String id,boolean call){
        Intent intent=new Intent(this,MainActivity.class).setAction(getPackageName()+".OPEN."+Json.sha(id.getBytes(java.nio.charset.StandardCharsets.UTF_8))).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP);
        if(call)intent.putExtra("incoming_event",id);else intent.putExtra("open_messages",true);
        return PendingIntent.getActivity(this,2,intent,PendingIntent.FLAG_UPDATE_CURRENT|PendingIntent.FLAG_IMMUTABLE);
    }
    private void announce(String id,boolean actionable,boolean isCall){
        if(Build.VERSION.SDK_INT>=33&&checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS)!=android.content.pm.PackageManager.PERMISSION_GRANTED)return;
        synchronized(announced){if(!actionable||!announced.add(id))return;}
        Notification n=new Notification.Builder(this,"incoming").setSmallIcon(R.drawable.ic_agent)
            .setContentTitle(getString(isCall?R.string.incoming_call:R.string.incoming_sms)).setContentText(getString(R.string.tap_open))
            .setContentIntent(eventIntent(id,isCall)).setAutoCancel(true).setOnlyAlertOnce(true).setTimeoutAfter(isCall?60000:600000)
            .setCategory(isCall?Notification.CATEGORY_CALL:Notification.CATEGORY_MESSAGE).setVisibility(Notification.VISIBILITY_PRIVATE).build();
        getSystemService(NotificationManager.class).notify(id,3,n);
    }
    private void networkChanged(){if(!available)return;Link o=observer,a=agent;if(o!=null)o.networkChanged();if(a!=null)a.networkChanged();RemoteCall c=call;if(c!=null&&c.audio!=null)c.audio.networkChanged();}
    private void closeLinks(){if(loginTimer!=null)loginTimer.cancel(false);if(observer!=null)observer.close();if(agent!=null)agent.close();observer=agent=null;online=false;readerLinkOnline=false;}
    private void stopConnections(){
        invalidateCall();
        available=false;configurationEpoch++;configurationLoaded=true;
        if(healthTask!=null)healthTask.cancel(false);
        readerEpoch++;scanOwner.set(null);lastReaders=recoveringReaders();lastReaderAt=0;closeLinks();
        ReaderHub old=hub;hub=null;if(old!=null)readerIO.execute(old::close);
        if(api!=null){api.close();api=null;}
        readerStatus=UiText.of(sharing?R.string.sharing_paused:R.string.sharing_off);notice=UiText.EMPTY;
    }
    void pause(){
        if(accountBusy()||call!=null){notice=UiText.of(R.string.account_busy);changed();return;}
        stopConnections();final long expected=configurationEpoch;intentSaving=true;connection=UiText.of(R.string.saving_pause);changed();
        ConfigStore.intent(()->{try{JSONObject saved=store.update(current->current.put("available",false));main.post(()->{if(destroyed||configurationEpoch!=expected)return;config=saved;intentSaving=false;connection=UiText.of(R.string.paused);stopForeground(STOP_FOREGROUND_REMOVE);foreground=false;stopSelf();changed();});}
            catch(Exception e){main.post(()->{if(destroyed||configurationEpoch!=expected)return;intentSaving=false;notice=UiText.of(R.string.pause_save_failed);changed();});}});
    }
    void logout(Runnable complete){
        if(accountBusy()){notice=UiText.of(R.string.account_busy);changed();return;}
        stopConnections();call=null;sharing=false;sharingIntentEpoch++;final long expected=configurationEpoch;intentSaving=true;
        ConfigStore.intent(()->{try{JSONObject saved=store.signOut();main.post(()->{if(destroyed||configurationEpoch!=expected)return;config=saved;intentSaving=false;snapshot=Json.obj("lines",new JSONArray(),"incoming_lines",new JSONArray(),"messages",new JSONArray(),"cellular_calls",new JSONArray());synchronized(announced){announced.clear();}connection=UiText.of(R.string.paused);notice=UiText.of(R.string.signed_out_preserved);getSystemService(NotificationManager.class).cancelAll();stopForeground(STOP_FOREGROUND_REMOVE);foreground=false;stopSelf();changed();complete.run();});}
            catch(Exception e){main.post(()->{if(destroyed||configurationEpoch!=expected)return;intentSaving=false;notice=UiText.of(R.string.signout_failed);changed();});}});
    }
    @Override public void onDestroy(){destroyed=true;invalidateCall();closeLinks();if(call!=null)releaseWake(call);if(api!=null)api.close();try{connectivity.unregisterNetworkCallback(networkCallback);unregisterReceiver(usbReceiver);}catch(Exception ignored){}ReaderHub old=hub;if(old!=null)readerIO.execute(old::close);loop.shutdownNow();io.shutdownNow();controlIO.shutdownNow();readerIO.shutdown();super.onDestroy();}
}
