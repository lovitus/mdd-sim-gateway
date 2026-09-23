package com.lovitus.mddagent;

import org.json.*;
import java.util.concurrent.*;
import java.util.concurrent.atomic.AtomicBoolean;

final class RemoteCall {
    final CallPlan plan;
    private final AgentService service;
    private final GatewayApi api;
    private final ExecutorService io;
    private final ConfigStore store;
    private final String binding;
    private final String origin,pin;
    private final AtomicBoolean ending=new AtomicBoolean(),checking=new AtomicBoolean();
    private int reconcileAttempts;
    private ScheduledFuture<?> reconcileTimer;
    android.os.PowerManager.WakeLock wake;
    volatile NativeAudio audio;
    volatile UiText state=UiText.of(R.string.call_preparing);
    volatile String session="",phase="PREPARING";
    private volatile String releasedSession="";
    volatile boolean submitted,ended;
    private boolean retired,retiring;
    private volatile boolean invalidated;
    private volatile boolean starting;
    RemoteCall(AgentService s,GatewayApi a,CallPlan p,ExecutorService executor){
        service=s;api=a;plan=p;io=executor;store=new ConfigStore(s);binding=CallRecovery.bind(a);origin=a.endpoint.origin;pin=a.endpoint.fingerprint;starting=true;
    }
    RemoteCall(AgentService s,GatewayApi a,JSONObject saved,ExecutorService executor)throws Exception{
        service=s;api=a;plan=new CallPlan(saved);io=executor;store=new ConfigStore(s);binding=saved.getString("binding");origin=saved.optString("gateway_origin");pin=saved.optString("gateway_pin");
        phase=saved.getString("phase");session=saved.optString("session_id");
        submitted=!phase.equals("PREPARING");state=UiText.of(R.string.call_pending);
    }
    boolean ownsRecord(){return !invalidated&&service.ownsCall(this);}
    boolean busy(){return starting||audio!=null&&!audio.closed;}
    void invalidate(){
        invalidated=true;ended=true;
        synchronized(this){if(reconcileTimer!=null)reconcileTimer.cancel(false);}
        api.cancel(this);NativeAudio current=audio;if(current!=null)current.close();service.microphoneFinished(this);
    }
    private void requireOwner(){if(!ownsRecord())throw new IllegalStateException("Call owner was replaced");}
    private void persist(String next,boolean create){
        store.update(config->{
            requireOwner();
            JSONObject old=config.optJSONObject("pending_call");
            if(create&&old!=null||!create&&(old==null||!plan.operation.equals(old.optString("operation_id"))))
                throw new IllegalStateException("Call record ownership changed");
            if(old!=null&&("TERMINAL".equals(old.optString("phase"))&&!next.equals("TERMINAL")||"ENDING".equals(old.optString("phase"))&&(next.equals("PREPARING")||next.equals("START_MAY_HAVE_RUN"))))throw new IllegalStateException("Stale call transition");
            JSONObject row=plan.record();row.put("binding",binding).put("phase",next).put("session_id",session).put("gateway_origin",origin).put("gateway_pin",pin);
            row.put("revision",old==null?1:old.optLong("revision")+1);config.put("pending_call",row);
        });
    }
    void start(){io.execute(()->{try{
        persist("PREPARING",true);if(ended){finishPreparation();return;}
        JSONObject fresh=api.exactLine(plan.line,plan.card);if(plan.incoming==null)GatewayApi.requireReady(fresh,plan.mode+"_call");
        requireOwner();if(ended){finishPreparation();return;}
        audio=new NativeAudio(service,api,service.loop,new NativeAudio.Events(){
            public void state(int label){if(!ended&&ownsRecord())service.changed();}
            public void ended(int reason){if(!ownsRecord())return;synchronized(RemoteCall.this){ended=true;}api.cancel(RemoteCall.this);state=UiText.of(R.string.call_audio_stopped);service.callAudioEnded(RemoteCall.this);}
        });
        if(ended){audio.close();finishPreparation();return;}
        requireOwner();
        JSONObject lease=api.json("POST",plan.leases(),plan.lease(),this);session=lease.getString("session_id");
        persist("PREPARING",false);if(ended){finishPreparation();return;}
        audio.prepare(lease,plan.id).get(22,TimeUnit.SECONDS);
        persist("START_MAY_HAVE_RUN",false);
        synchronized(this){phase="START_MAY_HAVE_RUN";if(!ownsRecord()||!CallRecovery.mayDispatch(phase,ended)||audio.closed){ended=true;finishPreparationAsync();return;}submitted=true;}
        state=UiText.of(R.string.call_dispatching);service.changed();
        JSONObject result=api.json("POST",plan.startPath(),plan.start(session),this);
        // Never persist late ACTIVE: the durable dispatch marker already preserves uncertainty.
        boolean activate;synchronized(this){activate=ownsRecord()&&!ended&&!retired;if(activate){phase="ACTIVE";state=UiText.of(R.string.call_request_accepted);}}
        if(activate)audio.markActive();
    }catch(Exception e){
        if(audio!=null)audio.close();service.microphoneFinished(this);
        if(!submitted)finishPreparation();else if(ownsRecord()&&!ending.get()){state=UiText.of(R.string.call_result_unknown);service.reconcileSoon(this);}
    }finally{starting=false;service.changed();}});}
    private void finishPreparationAsync(){io.execute(this::finishPreparation);}
    private void finishPreparation(){
        retire(UiText.of(R.string.call_not_started));
    }
    private boolean authorized(){return binding.equals(CallRecovery.bind(api))||!plan.recoveryKey.isEmpty()&&origin.equals(api.endpoint.origin)&&pin.equals(api.endpoint.fingerprint);}
    private boolean recoveryTerminal(JSONObject result){return CallRecovery.recoveryTerminal(result)&&plan.id.equals(result.optString("call_id"))&&plan.operation.equals(result.optString("operation_id"))&&(session.isEmpty()||session.equals(result.optString("session_id")));}
    void reconcile(){reconcile(true);}
    private void reconcile(boolean reset){if(!ownsRecord()||!checking.compareAndSet(false,true))return;
        synchronized(this){if(retired){checking.set(false);return;}if(reset){reconcileAttempts=0;if(reconcileTimer!=null)reconcileTimer.cancel(false);}reconcileAttempts++;}
        io.execute(()->{boolean retry=true;try{
        if(!ownsRecord()){retry=false;return;}
        if(!authorized()){retry=false;state=UiText.of(R.string.call_owner_changed);return;}
        if(phase.equals("PREPARING")&&!submitted){finishPreparation();return;}
        if(phase.equals("TERMINAL")){retire(UiText.of(R.string.call_ended));return;}
        if(!plan.recoveryKey.isEmpty()){
            JSONObject result=api.json("POST",plan.prefix()+"recovery",plan.recovery("status"));
            if(!ownsRecord()){retry=false;return;}
            if(recoveryTerminal(result)){session=result.getString("session_id");phase="TERMINAL";retire(UiText.of(CallRecovery.terminalMessage(result)));}
            else{state=UiText.of(R.string.call_remote_state,UiLabels.unconfirmedCallState(result.optString("state")));String reason=result.optString("reason");if(!reason.isEmpty())state=UiText.of(R.string.call_reason,state,reason);}
            return;
        }
        JSONObject status=api.json("GET",plan.mode.equals("cellular")?plan.prefix()+"status":"/v1/lines/"+CallPlan.encode(plan.line)+"/vowifi/status",null);
        if(!ownsRecord()){retry=false;return;}
        if(plan.mode.equals("cellular")){
            JSONArray rows=Json.array(status,"sessions");boolean found=false;
            for(int i=0;i<rows.length();i++){JSONObject row=rows.optJSONObject(i);if(row!=null&&session.equals(row.optString("session_id"))&&plan.id.equals(row.optString("call_id"))){found=true;state=UiText.of(R.string.call_remote_state,UiLabels.unconfirmedCallState(row.optString("phase")));break;}}
            if(!found)state=UiText.of(R.string.call_no_evidence);
        }else{
            JSONObject active=status.optJSONObject("active_call");
            state=UiText.of(active!=null&&plan.id.equals(active.optString("call_id"))?R.string.call_still_active:R.string.call_no_active_evidence);
        }
    }catch(Exception e){state=UiText.of(R.string.call_check_failed);if(e instanceof GatewayApi.Failure){int status=((GatewayApi.Failure)e).status;if(status==401||status==403){retry=false;state=UiText.of(R.string.call_login_required);}else if(status==404){retry=false;state=UiText.of(R.string.call_evidence_unavailable);}}}
        finally{checking.set(false);service.changed();synchronized(this){if(retry&&ownsRecord()&&!retired&&!phase.equals("TERMINAL")&&(audio==null||audio.closed)&&reconcileAttempts<6){try{reconcileTimer=service.loop.schedule(()->reconcile(false),Math.min(30,1L<<reconcileAttempts),TimeUnit.SECONDS);}catch(RejectedExecutionException ignored){}}}}});}
    void hangup(){
        if(!service.ownsCall(this)){if(audio!=null)audio.close();service.microphoneFinished(this);return;}
        if(!authorized()){state=UiText.of(R.string.call_owner_changed);service.changed();return;}
        if(!ending.compareAndSet(false,true))return;
        synchronized(this){ended=true;}
        api.cancel(this);if(audio!=null)audio.close();service.microphoneFinished(this);
        service.controlIO.execute(()->{try{
            if(!service.ownsCall(this))return;
            if(!submitted){state=UiText.of(R.string.call_cancel_preparing);return;}
            // The stable end-operation ID is already durable. Never put a disk
            // wait in front of the user's existing authorized safety action.
            io.execute(()->{try{persist("ENDING",false);}catch(Exception ignored){}});
            if(!plan.recoveryKey.isEmpty()){
                JSONObject result=api.json("POST",plan.prefix()+"recovery",plan.recovery("end"));
                if(!service.ownsCall(this))return;
                if(recoveryTerminal(result)){session=result.getString("session_id");phase="TERMINAL";retire(UiText.of(CallRecovery.terminalMessage(result)));}
                else state=UiText.of(R.string.call_end_unconfirmed);
                return;
            }
            if(session.isEmpty()){state=UiText.of(R.string.call_missing_media);return;}
            JSONObject result=api.json("POST",plan.prefix()+(plan.mode.equals("cellular")?"hangup":"end"),plan.end(session));
            if(!service.ownsCall(this))return;
            if(CallRecovery.terminal(plan,session,result)){phase="TERMINAL";retire(UiText.of(R.string.call_remote_ended));}
            else state=UiText.of(R.string.call_end_unconfirmed);
        }catch(Exception e){state=UiText.of(R.string.call_end_unknown);}finally{ending.set(false);service.changed();service.reconcileSoon(this);}});
    }
    private void retire(UiText message){
        if(!ownsRecord()){
            if(audio!=null)audio.close();service.microphoneFinished(this);
            if(!submitted)release();
            if(service.ownsCall(this)){state=UiText.of(R.string.call_wait_new_owner,message);service.changed();}return;
        }
        synchronized(this){if(retired||retiring)return;retiring=true;ended=true;}
        if(audio!=null)audio.close();service.microphoneFinished(this);
        state=UiText.of(R.string.call_saving_recovery,message);service.changed();
        try{
            if(!submitted&&!release()){state=UiText.of(R.string.call_prepare_cleanup_unknown);service.changed();return;}
            persist("TERMINAL",false);
            if(!ownsRecord())return;
            if(submitted)release();
            store.update(config->{requireOwner();JSONObject row=config.optJSONObject("pending_call");if(row==null||!plan.operation.equals(row.optString("operation_id")))throw new IllegalStateException("Call record replaced");config.remove("pending_call");});
            synchronized(this){retired=true;ended=true;phase="TERMINAL";if(reconcileTimer!=null)reconcileTimer.cancel(false);}state=message;service.clearCall(this,message);
        }catch(Exception e){state=UiText.of(R.string.call_save_failed,message);}finally{synchronized(this){retiring=false;}}
    }
    boolean release(){String current=session;if(current.isEmpty()||current.equals(releasedSession))return true;try{api.json("DELETE",plan.leases(),Json.obj("session_id",current));releasedSession=current;return true;}catch(Exception e){return false;}}
    static String safe(Exception e){Throwable c=e instanceof ExecutionException?e.getCause():e;String s=c==null?"Unavailable":c.getMessage();return s==null?"Unavailable":s.substring(0,Math.min(180,s.length()));}
    void dtmf(String signal){if(!ownsRecord()||ended||audio==null||!audio.active||!signal.matches("[0-9*#A-D]"))return;io.execute(()->{try{if(!ownsRecord()||ended||audio==null||audio.closed)return;JSONObject b=Json.obj("operation_id",Json.id(),"signal",signal);if(plan.mode.equals("cellular"))b.put("session_id",session);else b.put("call_id",plan.id).put("duration_ms",160);api.json("POST",plan.prefix()+"dtmf",b);}catch(Exception e){state=UiText.of(R.string.dtmf_unconfirmed);service.changed();}});}
}
