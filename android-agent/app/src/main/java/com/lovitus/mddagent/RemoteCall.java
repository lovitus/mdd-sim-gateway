package com.lovitus.mddagent;
import org.json.*;
import java.util.concurrent.*;
import java.util.concurrent.atomic.AtomicBoolean;
final class RemoteCall {
    final CallPlan plan;private final AgentService service;private final GatewayApi api;private final ExecutorService io;
    android.os.PowerManager.WakeLock wake;
    volatile NativeAudio audio;volatile String state="Preparing audio",session="";volatile boolean submitted,ended;private final AtomicBoolean ending=new AtomicBoolean();
    RemoteCall(AgentService s,GatewayApi api,CallPlan p,ExecutorService io){service=s;this.api=api;plan=p;this.io=io;}
    void start(){io.execute(()->{try{
        if(ended)return;
        audio=new NativeAudio(service,api,service.loop,new NativeAudio.Events(){public void state(String text){state=text;service.changed();}public void ended(String reason){state=reason;ended=true;service.callAudioEnded(RemoteCall.this);if(reason.equals("Call ended"))io.execute(()->{release();service.clearCall(RemoteCall.this);});}});
        if(ended){audio.close();return;}
        JSONObject lease=api.json("POST",plan.leases(),plan.lease(),this);session=lease.getString("session_id");
        if(ended){release();return;}
        audio.prepare(lease,plan.id).get(22,TimeUnit.SECONDS);if(ended){release();return;}
        synchronized(RemoteCall.this){if(ended){release();return;}submitted=true;}state="Calling — do not repeat";service.changed();
        JSONObject result=api.json("POST",plan.startPath(),plan.start(session),this);
        if(!ended){audio.markActive();state=result.optString("code","Call active");service.changed();}
    }catch(Exception e){if(ending.get())return;state=submitted?"Call outcome unknown; use Hang up or reconcile history":"Audio/call preparation failed: "+safe(e);if(audio!=null)audio.close();if(!submitted){ended=true;release();service.notice=state;service.clearCall(this);}service.changed();service.microphoneFinished(this);}});}
    static String safe(Exception e){Throwable c=e instanceof ExecutionException?e.getCause():e;String s=c==null?"Unavailable":c.getMessage();return s==null?"Unavailable":s.substring(0,Math.min(180,s.length()));}
    synchronized void hangup(){if(!ending.compareAndSet(false,true))return;ended=true;api.cancel(this);if(audio!=null)audio.close();service.microphoneFinished(this);io.execute(()->{try{if(submitted&&!session.isEmpty())api.json("POST",plan.prefix()+(plan.mode.equals("cellular")?"hangup":"end"),plan.end(session));release();service.clearCall(this);}catch(Exception e){if(e instanceof GatewayApi.Failure&&((GatewayApi.Failure)e).status==404){release();service.clearCall(this);return;}state="Hangup outcome unknown. Server guard remains active.";ending.set(false);service.changed();}});}
    void release(){if(!session.isEmpty())try{api.json("DELETE",plan.leases(),Json.obj("session_id",session));}catch(Exception ignored){}}
    void dtmf(String signal){if(ended||audio==null||!audio.active||!signal.matches("[0-9*#A-D]"))return;io.execute(()->{try{JSONObject b=Json.obj("operation_id",Json.id(),"signal",signal);if(plan.mode.equals("cellular"))b.put("session_id",session);else b.put("call_id",plan.id).put("duration_ms",160);api.json("POST",plan.prefix()+"dtmf",b);}catch(Exception e){state="DTMF not confirmed; not repeated";service.changed();}});}
}
