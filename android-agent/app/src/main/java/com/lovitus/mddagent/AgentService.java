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
    private final LinkedHashSet<String> announced=new LinkedHashSet<>();
    private final java.util.concurrent.atomic.AtomicReference<ReaderHub> scanOwner=new java.util.concurrent.atomic.AtomicReference<>();
    private volatile long readerEpoch;
    private final String process=Json.id();
    private ConnectivityManager connectivity;private ConnectivityManager.NetworkCallback networkCallback;
    private ConfigStore store;private volatile JSONObject config;private volatile GatewayApi api;private volatile Link observer,agent;private volatile ReaderHub hub;
    private volatile boolean destroyed,available,sharing,online;private boolean foreground;
    volatile JSONObject snapshot=Json.obj("lines",new JSONArray(),"messages",new JSONArray(),"cellular_calls",new JSONArray());
    volatile UiText connection=UiText.of(R.string.paused),readerStatus=UiText.of(R.string.sharing_off),notice=UiText.EMPTY;volatile RemoteCall call;
    private final AtomicBoolean smsPending=new AtomicBoolean(),enrollmentPending=new AtomicBoolean();
    private GatewayApi messageSyncOwner;private boolean messageSyncRequested,messageSyncStopped,messageSyncTerminal;private int messageSyncFailures;
    private ScheduledFuture<?> messageSyncTimer;private final Retry messageRetry=new Retry();
    private final Set<String> messageChecks=ConcurrentHashMap.newKeySet();
    private volatile String messageWatermark="";
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
            main.post(()->{if(!available||destroyed||!sharing)return;publishReaderHealth();refreshReaders();});
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
        if(messageSyncTimer!=null)messageSyncTimer.cancel(false);messageSyncTimer=null;messageSyncFailures=0;messageSyncStopped=false;messageSyncTerminal=false;messageRetry.healthy();
        closeLinks();snapshot=Json.obj("lines",new JSONArray(),"messages",new JSONArray(),"cellular_calls",new JSONArray());if(api!=null){api.close();api=null;}
        JSONObject pending=config.optJSONObject("pending_call");
        if(pending==null)call=null;
        if(config.optString("token").isEmpty()){connection=UiText.of(R.string.login_required);changed();return;}
        api=new GatewayApi(new Endpoint(config.getString("server"),config.optString("pin")),config.getString("token"),config.getString("csrf"));
        if(pending!=null){call=new RemoteCall(this,api,pending,io);call.reconcile();}
        final GatewayApi connectionOwner=api;
        observer=new Link(api,loop,"/v1/mobile/ws","","",process,new Link.Events(){
            public void state(int label,boolean yes){if(api!=connectionOwner)return;boolean recovered=yes&&!online;connection=UiText.of(label);online=yes;if(recovered){main.post(()->{if(api!=connectionOwner)return;if(messageSyncStopped&&!messageSyncTerminal){messageSyncStopped=false;messageSyncFailures=0;messageRetry.healthy();}requestMessageSync(false);});RemoteCall c=call;if(c!=null&&(c.audio==null||c.audio.closed))c.reconcile();}changed();}
            public void message(JSONObject message){if(api!=connectionOwner)return;if(message.optString("type").equals("mobile.snapshot")){snapshot=Json.object(message,"data");notifyEvents();changed();}}
        });observer.connect();
        if(sharing&&!config.optString("agent_token").isEmpty())startReaderLink();
        else if(sharing){sharing=false;readerStatus=UiText.of(R.string.enroll_readers);}
        changed();
    }
    private void startReaderLink(){
        if(agent!=null){agent.close();agent=null;}
        if(hub==null||hub.closed)hub=new ReaderHub(this);
        readerObservation=null;
        final long epoch=++readerEpoch;
        agent=new Link(api,loop,"/v1/agent/ws",config.optString("agent_id"),config.optString("agent_token"),process,new Link.Events(){
            public void state(int label,boolean connected){main.post(()->{if(epoch!=readerEpoch||!sharing||destroyed)return;readerStatus=UiText.of(label);changed();if(connected){publishReaderHealth();refreshReaders();}});}
            public void message(JSONObject message){main.post(()->{if(epoch!=readerEpoch||!sharing||destroyed)return;if(message.optString("kind").equals("aka_request")){
                final Link owner=agent;final ReaderHub device=hub;if(owner==null||device==null)return;final long generation=owner.generation();
                readerIO.execute(()->{if(epoch!=readerEpoch||device!=hub||!sharing)return;JSONObject answer=device.authenticate(Json.object(message,"aka_request"));owner.respond(generation,message.optString("request_id"),answer);});
            }});}
        });agent.connect();refreshReaders();
    }
    void refreshReaders(){refreshReaders(false);}
    void refreshReaderMetadata(){refreshReaders(true);}
    private void refreshReaders(boolean explicitMetadata){
        if(Looper.myLooper()!=Looper.getMainLooper()){main.post(()->refreshReaders(explicitMetadata));return;}
        final ReaderHub owner=hub;final long epoch=readerEpoch;
        if(destroyed||!available||!sharing||owner==null||!scanOwner.compareAndSet(null,owner))return;
        try{readerIO.execute(()->{
            try{
                owner.scan(explicitMetadata);
                ReaderObservation observed=new ReaderObservation(owner,epoch,SystemClock.elapsedRealtime(),owner.topology());
                UiText diagnostic=owner.diagnostic;
                main.post(()->publishReaderObservation(observed,diagnostic));
            }finally{main.post(()->scanOwner.compareAndSet(owner,null));}
        });}catch(RejectedExecutionException stopped){scanOwner.compareAndSet(owner,null);}
    }
    // Sharing, connection replacement and publication all commit on the main owner.
    // No hardware I/O or lock is held while publishing, and old callbacks cannot
    // pass a check on one thread then publish onto a replacement Link on another.
    void publishReaderObservation(ReaderObservation observed,UiText diagnostic){
        if(Looper.myLooper()!=Looper.getMainLooper())throw new IllegalStateException("Reader publication outside owner");
        if(destroyed||!available||!sharing||!observed.current(hub,readerEpoch,SystemClock.elapsedRealtime()))return;
        readerObservation=observed;readerStatus=diagnostic;publishReaderHealth();changed();
    }
    private void publishReaderHealth(){
        Link link=agent;if(link!=null)link.health(readers());
    }
    JSONObject readers(){
        ReaderObservation observed=readerObservation;
        return sharing&&observed!=null&&observed.current(hub,readerEpoch,SystemClock.elapsedRealtime())?observed.topology():recoveringReaders();
    }
    private static JSONObject recoveringReaders(){return Json.obj("reader_condition","recovering","reader_detail","Fresh reader observation required","readers",new JSONArray(),"modem_condition","disabled");}
    private volatile ReaderObservation readerObservation;
    void shareReaders(boolean enabled){if(call!=null){notice=UiText.of(R.string.sharing_call_busy);changed();return;}
        if(!enabled){
            final long expected=++sharingIntentEpoch;readerEpoch++;scanOwner.set(null);sharing=false;readerObservation=null;
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
    void changed(){main.post(()->{if(destroyed)return;for(Runnable l:listeners)l.run();if(foreground&&(Build.VERSION.SDK_INT<33||checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS)==android.content.pm.PackageManager.PERMISSION_GRANTED))getSystemService(NotificationManager.class).notify(1,notification());});}
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
        final GatewayApi owner=api;if(!canQueryMessages()||owner==null){notice=UiText.of(R.string.connect_first);changed();return;}final String key=scope+"\n"+operation;if(!messageChecks.add(key))return;changed();
        io.execute(()->{boolean remoteConfirmed=false;try{
            String account=owner.authenticatedScope();if(!account.equals(scope))throw new IllegalStateException("Account changed");
            JSONObject original=MessageJournal.find(store.load(),scope,operation),payload=MessageJournal.receipt(original);
            if(!original.optString("gateway_origin").equals(owner.endpoint.origin)||!original.optString("gateway_pin").equals(owner.endpoint.fingerprint))throw new IllegalStateException("Gateway binding changed");
            String path="/v1/lines/"+CallPlan.encode(original.getString("line_id"))+(original.getString("transport").equals("cellular")?"/cellular/messages":"/vowifi/messages/receipt");
            JSONObject result=owner.json("POST",path,payload);
            remoteConfirmed=MessageJournal.confirmed(original,result);
            store.update(current->{if(api!=owner||!owner.token.equals(current.optString("token")))throw new IllegalStateException("Account changed");MessageJournal.response(current,scope,operation,result);});
            if(api==owner)notice=UiText.of(remoteConfirmed?R.string.message_confirmed:R.string.message_still_unknown);
        }catch(Exception failure){if(api==owner)notice=UiText.of(remoteConfirmed?R.string.message_confirmed_save_failed:R.string.message_retained_unknown);}
        finally{messageChecks.remove(key);refreshPrivateState(owner);}});
    }

    void directory(String query,String after,java.util.function.Consumer<JSONObject> success,java.util.function.Consumer<String> failure){
        final GatewayApi owner=api;if(owner==null){failure.accept(getString(R.string.connect_first));return;}
        io.execute(()->{try{JSONObject page=owner.json("GET","/v1/mobile/lines?q="+CallPlan.encode(query)+"&after="+CallPlan.encode(after),null);main.post(()->{if(api==owner)success.accept(page);});}catch(Exception e){main.post(()->{if(api==owner)failure.accept(getString(R.string.directory_failed));});}});
    }
    void incomingPage(String after,java.util.function.Consumer<JSONObject> success,java.util.function.Consumer<String> failure){
        final GatewayApi owner=api;if(owner==null){failure.accept(getString(R.string.connect_first));return;}
        io.execute(()->{try{JSONObject page=owner.json("GET","/v1/mobile/incoming?after="+CallPlan.encode(after),null);main.post(()->{if(api==owner)success.accept(page);});}catch(Exception e){main.post(()->{if(api==owner)failure.accept(getString(R.string.incoming_read_failed));});}});
    }
    void messageHistory(String line,String mode,String peer,String before,long expectedEpoch,java.util.function.Consumer<JSONObject> success,java.util.function.Consumer<String> failure){
        messagePageRequest("/v1/messages?page=true&line_id="+CallPlan.encode(line)+"&transport="+mode+"&peer="+CallPlan.encode(peer)+"&limit=50&before="+CallPlan.encode(before),expectedEpoch,success,failure);
    }
    void messageConversations(long expectedEpoch,java.util.function.Consumer<JSONObject> success,java.util.function.Consumer<String> failure){messagePageRequest("/v1/messages/conversations?all=true",expectedEpoch,success,failure);}
    private void messagePageRequest(String path,long expectedEpoch,java.util.function.Consumer<JSONObject> success,java.util.function.Consumer<String> failure){
        final GatewayApi owner=api;if(owner==null||configurationEpoch!=expectedEpoch){failure.accept(getString(R.string.history_account_changed));return;}
        io.execute(()->{try{JSONObject page=owner.json("GET",path,null);main.post(()->{if(api==owner&&configurationEpoch==expectedEpoch)success.accept(page);else failure.accept(getString(R.string.history_account_changed));});}catch(Exception e){main.post(()->failure.accept(getString(api==owner&&configurationEpoch==expectedEpoch?R.string.history_failed:R.string.history_account_changed)));}});
    }
    private void notifyEvents(){JSONArray lines=Json.array(snapshot,snapshot.has("incoming_lines")?"incoming_lines":"lines");for(int i=0;i<lines.length();i++){JSONObject in=Json.object(lines.optJSONObject(i),"incoming");announce("call:"+in.optString("call_id"),!in.optString("call_id").isEmpty(),true);}
        JSONArray cell=Json.array(snapshot,"cellular_calls");for(int i=0;i<cell.length();i++){JSONObject c=cell.optJSONObject(i);if(c!=null)announce("cell:"+c.optString("incoming_event_id"),c.optBoolean("actionable"),true);}
        JSONArray messages=Json.array(snapshot,"messages");String watermark=Json.sha(messages.toString().getBytes(java.nio.charset.StandardCharsets.UTF_8));if(!watermark.equals(messageWatermark)){messageWatermark=watermark;requestMessageSync(false);}
        synchronized(announced){while(announced.size()>512)announced.remove(announced.iterator().next());}
        if(snapshot.optBoolean("incoming_more"))announce("all",true,true);
    }

    void syncMessages(){if(!canQueryMessages()){notice=UiText.of(R.string.connect_first);changed();return;}requestMessageSync(true);}
    private void requestMessageSync(boolean manual){main.post(()->{
        if(!available||destroyed||api==null)return;
        if(manual){messageSyncFailures=0;messageSyncStopped=false;messageSyncTerminal=false;messageRetry.healthy();if(messageSyncTimer!=null)messageSyncTimer.cancel(false);messageSyncTimer=null;}
        if(messageSyncStopped)return;
        if(messageSyncOwner!=null){messageSyncRequested=true;return;}
        if(messageSyncTimer!=null&&!messageSyncTimer.isDone())return;
        startMessageSync();
    });}
    private void startMessageSync(){
        final GatewayApi owner=api;if(!available||destroyed||owner==null||messageSyncOwner!=null||messageSyncStopped)return;
        messageSyncOwner=owner;messageSyncRequested=false;messageSyncTimer=null;
        io.execute(()->{boolean more=false;Exception failure=null;try{
            String scope=owner.authenticatedScope();
            publishMessageSummary(owner,scope,false);
            for(int batch=0;batch<5;batch++){
                if(api!=owner||!available)return;
                JSONObject stored=store.load();
                JSONArray notifications=Json.array(MessageSync.state(stored,scope),"pending");
                if(notifications.length()>0){
                    String disposition=publishMessageSummary(owner,scope,true);
                    store.update(current->{requireMessageOwner(current,owner);MessageSync.acknowledged(current,scope,notifications,disposition);});
                }
                String cursor=MessageSync.state(stored,scope).optString("cursor");
                JSONObject page=owner.json("GET","/v1/messages?sync=true&limit=100&after="+CallPlan.encode(cursor),null);
                if(api!=owner||!available)return;
                JSONObject committed=store.update(current->{requireMessageOwner(current,owner);MessageSync.accept(current,scope,cursor,page);current.put("account_scope",scope);});
                if(page.optBoolean("gap")&&api==owner)notice=UiText.of(R.string.message_sync_gap);
                // One additional iteration drains the durable notification outbox, even on the last page.
                more=page.optBoolean("more")||Json.array(MessageSync.state(committed,scope),"pending").length()>0;
                if(!more)break;
            }
        }catch(Exception e){failure=e;}
        finally{final boolean next=more;final Exception error=failure;main.post(()->{
            if(messageSyncOwner!=owner)return;messageSyncOwner=null;
            if(destroyed||!available)return;
            if(api!=owner){messageSyncFailures=0;messageSyncStopped=false;messageSyncTerminal=false;messageRetry.healthy();startMessageSync();return;}
            refreshPrivateState(owner);
            if(error!=null){notice=UiText.of(R.string.message_sync_failed);changed();messageSyncTerminal=TransportFailures.terminalRead(error);
                if(!messageSyncTerminal&&++messageSyncFailures<=6)messageSyncTimer=loop.schedule(()->main.post(this::startMessageSync),messageRetry.next(),TimeUnit.MILLISECONDS);else messageSyncStopped=true;
            }else{messageSyncFailures=0;messageRetry.healthy();if(messageSyncRequested)startMessageSync();else if(next)messageSyncTimer=loop.schedule(()->main.post(this::startMessageSync),30,TimeUnit.SECONDS);}
        });}});
    }
    private void requireMessageOwner(JSONObject current,GatewayApi owner){if(destroyed||!available||api!=owner||owner.token.isEmpty()||!owner.token.equals(current.optString("token")))throw new IllegalStateException("Account changed");}
    private String publishMessageSummary(GatewayApi owner,String scope,boolean publish)throws Exception{
        CompletableFuture<String> result=new CompletableFuture<>();main.post(()->{try{
            if(destroyed||!available||api!=owner)throw new IllegalStateException("Account changed");
            NotificationManager manager=getSystemService(NotificationManager.class);String tag="sms-summary:"+Json.sha(scope.getBytes(java.nio.charset.StandardCharsets.UTF_8));
            for(android.service.notification.StatusBarNotification row:manager.getActiveNotifications())if(row.getId()==2&&!tag.equals(row.getTag()))manager.cancel(row.getTag(),2);
            if(!publish){result.complete("unchanged");return;}
            NotificationChannel channel=manager.getNotificationChannel("incoming");
            if(!manager.areNotificationsEnabled()||channel==null||channel.getImportance()==NotificationManager.IMPORTANCE_NONE||(Build.VERSION.SDK_INT>=33&&checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS)!=android.content.pm.PackageManager.PERMISSION_GRANTED)){result.complete("suppressed_by_permission");return;}
            Notification notification=new Notification.Builder(this,"incoming").setSmallIcon(R.drawable.ic_agent)
                .setContentTitle(getString(R.string.incoming_sms)).setContentText(getString(R.string.tap_open))
                .setContentIntent(eventIntent("sms:"+tag,false)).setAutoCancel(true).setOnlyAlertOnce(true).setTimeoutAfter(600000)
                .setCategory(Notification.CATEGORY_MESSAGE).setVisibility(Notification.VISIBILITY_PRIVATE).build();
            manager.notify(tag,2,notification);result.complete("submitted_to_android");
        }catch(Exception failure){result.completeExceptionally(failure);}});return result.get(5,TimeUnit.SECONDS);
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
    private void closeLinks(){readerEpoch++;readerObservation=null;if(observer!=null)observer.close();if(agent!=null)agent.close();observer=agent=null;online=false;}
    private void stopConnections(){
        invalidateCall();
        available=false;configurationEpoch++;configurationLoaded=true;
        if(healthTask!=null)healthTask.cancel(false);
        if(messageSyncTimer!=null){messageSyncTimer.cancel(false);messageSyncTimer=null;}messageSyncRequested=false;
        readerEpoch++;scanOwner.set(null);readerObservation=null;closeLinks();
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
    void prepareStorageReset(){
        if(accountBusy()||call!=null)throw new IllegalStateException(getString(R.string.storage_reset_busy));
        stopConnections();sharing=false;sharingIntentEpoch++;enrollmentPending.set(false);
        stopForeground(STOP_FOREGROUND_REMOVE);foreground=false;stopSelf();
        config=new JSONObject();snapshot=Json.obj("lines",new JSONArray(),"messages",new JSONArray());
        getSystemService(NotificationManager.class).cancelAll();changed();
    }
    void logout(Runnable complete){
        if(accountBusy()){notice=UiText.of(R.string.account_busy);changed();return;}
        stopConnections();call=null;sharing=false;sharingIntentEpoch++;final long expected=configurationEpoch;intentSaving=true;
        ConfigStore.intent(()->{try{JSONObject saved=store.signOut();main.post(()->{if(destroyed||configurationEpoch!=expected)return;config=saved;intentSaving=false;snapshot=Json.obj("lines",new JSONArray(),"incoming_lines",new JSONArray(),"messages",new JSONArray(),"cellular_calls",new JSONArray());synchronized(announced){announced.clear();}connection=UiText.of(R.string.paused);notice=UiText.of(R.string.signed_out_preserved);getSystemService(NotificationManager.class).cancelAll();stopForeground(STOP_FOREGROUND_REMOVE);foreground=false;stopSelf();changed();complete.run();});}
            catch(Exception e){main.post(()->{if(destroyed||configurationEpoch!=expected)return;intentSaving=false;notice=UiText.of(R.string.signout_failed);changed();});}});
    }
    @Override public void onDestroy(){destroyed=true;invalidateCall();closeLinks();if(call!=null)releaseWake(call);if(api!=null)api.close();try{connectivity.unregisterNetworkCallback(networkCallback);unregisterReceiver(usbReceiver);}catch(Exception ignored){}ReaderHub old=hub;if(old!=null)readerIO.execute(old::close);loop.shutdownNow();io.shutdownNow();controlIO.shutdownNow();readerIO.shutdown();super.onDestroy();}
}
