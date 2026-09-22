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
    private final AtomicBoolean ending=new AtomicBoolean(),checking=new AtomicBoolean();
    android.os.PowerManager.WakeLock wake;
    volatile NativeAudio audio;
    volatile String state="准备通话",session="",phase="PREPARING";
    volatile boolean submitted,ended;
    private boolean retired;
    RemoteCall(AgentService s,GatewayApi a,CallPlan p,ExecutorService executor){
        service=s;api=a;plan=p;io=executor;store=new ConfigStore(s);binding=CallRecovery.bind(a);
    }
    RemoteCall(AgentService s,GatewayApi a,JSONObject saved,ExecutorService executor)throws Exception{
        service=s;api=a;plan=new CallPlan(saved);io=executor;store=new ConfigStore(s);binding=saved.getString("binding");
        phase=saved.getString("phase");session=saved.optString("session_id");
        submitted=!phase.equals("PREPARING");state="待核对的通话；麦克风未开启";
    }
    private void persist(String next,boolean create){
        store.update(config->{
            JSONObject old=config.optJSONObject("pending_call");
            if(create&&old!=null||!create&&(old==null||!plan.operation.equals(old.optString("operation_id"))))
                throw new IllegalStateException("Call record ownership changed");
            JSONObject row=plan.record();row.put("binding",binding).put("phase",next).put("session_id",session);
            row.put("revision",old==null?1:old.optLong("revision")+1);config.put("pending_call",row);
        });
    }
    void start(){io.execute(()->{try{
        persist("PREPARING",true);if(ended){finishPreparation();return;}
        audio=new NativeAudio(service,api,service.loop,new NativeAudio.Events(){
            public void state(String text){if(!ended){state=text;service.changed();}}
            public void ended(String reason){synchronized(RemoteCall.this){if(submitted)ended=true;}state="音频已停止；请核对远端通话";service.callAudioEnded(RemoteCall.this);}
        });
        if(ended){audio.close();finishPreparation();return;}
        JSONObject lease=api.json("POST",plan.leases(),plan.lease(),this);session=lease.getString("session_id");
        persist("PREPARING",false);if(ended){finishPreparation();return;}
        audio.prepare(lease,plan.id).get(22,TimeUnit.SECONDS);
        persist("START_MAY_HAVE_RUN",false);
        synchronized(this){phase="START_MAY_HAVE_RUN";if(!CallRecovery.mayDispatch(phase,ended)){finishPreparationAsync();return;}submitted=true;}
        state="正在呼叫，请勿重复提交";service.changed();
        JSONObject result=api.json("POST",plan.startPath(),plan.start(session),this);
        // Never persist late ACTIVE: the durable dispatch marker already preserves uncertainty.
        synchronized(this){if(!ended&&!retired){phase="ACTIVE";audio.markActive();state=result.optString("code","通话进行中");}}
    }catch(Exception e){
        if(audio!=null)audio.close();service.microphoneFinished(this);
        if(!submitted)finishPreparation();else if(!ending.get())state="通话结果未知；请核对或挂断原通话";
    }finally{service.changed();}});}
    private void finishPreparationAsync(){io.execute(this::finishPreparation);}
    private void finishPreparation(){
        if(audio!=null)audio.close();service.microphoneFinished(this);
        if(!release()){state="未发起通话；准备资源清理未确认，请重试核对";service.changed();return;}
        retire("未发起通话");
    }
    private boolean authorized(){return binding.equals(CallRecovery.bind(api));}
    void reconcile(){if(!checking.compareAndSet(false,true))return;io.execute(()->{try{
        if(!authorized()){state="登录身份已变化；请在网关核对原通话，不能接管";return;}
        if(phase.equals("PREPARING")&&!submitted){finishPreparation();return;}
        if(phase.equals("TERMINAL")){retire("通话已结束");return;}
        JSONObject status=api.json("GET",plan.mode.equals("cellular")?plan.prefix()+"status":"/v1/lines/"+CallPlan.encode(plan.line)+"/vowifi/status",null);
        if(plan.mode.equals("cellular")){
            JSONArray rows=Json.array(status,"sessions");boolean found=false;
            for(int i=0;i<rows.length();i++){JSONObject row=rows.optJSONObject(i);if(row!=null&&session.equals(row.optString("session_id"))&&plan.id.equals(row.optString("call_id"))){found=true;state="原通话状态："+row.optString("phase")+"；可请求挂断，麦克风未开启";break;}}
            if(!found)state="网关已无原会话证据；需要人工核对，不能判定已结束";
        }else{
            JSONObject active=status.optJSONObject("active_call");
            state=active!=null&&plan.id.equals(active.optString("call_id"))?"原通话仍活动；可挂断，麦克风未开启":"未找到原活动通话；需要网关核对，不能判定已结束";
        }
    }catch(Exception e){state="暂时无法核对原通话；记录已保留";}finally{checking.set(false);service.changed();}});}
    void hangup(){
        if(!authorized()){state="登录身份不匹配；请在网关核对原通话";service.changed();return;}
        if(!ending.compareAndSet(false,true))return;
        synchronized(this){ended=true;}
        api.cancel(this);if(audio!=null)audio.close();service.microphoneFinished(this);
        io.execute(()->{try{
            if(!submitted){state="取消准备中";return;}
            try{persist("ENDING",false);}catch(Exception ignored){/* Known safety action must remain available. */}
            if(session.isEmpty()){state="原媒体身份缺失；需要网关核对";return;}
            JSONObject result=api.json("POST",plan.prefix()+(plan.mode.equals("cellular")?"hangup":"end"),plan.end(session));
            if(CallRecovery.terminal(plan,session,result)){phase="TERMINAL";release();retire("通话已在远端结束");}
            else state="声音已停止，远端结束尚未确认；请核对状态";
        }catch(Exception e){state="声音已停止，挂断结果未知；请核对状态";}finally{ending.set(false);service.changed();}});
    }
    private void retire(String message){
        try{
            persist("TERMINAL",false);
            store.update(config->{JSONObject row=config.optJSONObject("pending_call");if(row==null||!plan.operation.equals(row.optString("operation_id")))throw new IllegalStateException("Call record replaced");config.remove("pending_call");});
            synchronized(this){retired=true;ended=true;phase="TERMINAL";}state=message;service.notice=message;service.clearCall(this);
        }catch(Exception e){state=message+"，但恢复状态保存失败；记录保留";}
    }
    boolean release(){if(session.isEmpty())return true;try{api.json("DELETE",plan.leases(),Json.obj("session_id",session));return true;}catch(Exception e){return false;}}
    static String safe(Exception e){Throwable c=e instanceof ExecutionException?e.getCause():e;String s=c==null?"Unavailable":c.getMessage();return s==null?"Unavailable":s.substring(0,Math.min(180,s.length()));}
    void dtmf(String signal){if(ended||audio==null||!audio.active||!signal.matches("[0-9*#A-D]"))return;io.execute(()->{try{JSONObject b=Json.obj("operation_id",Json.id(),"signal",signal);if(plan.mode.equals("cellular"))b.put("session_id",session);else b.put("call_id",plan.id).put("duration_ms",160);api.json("POST",plan.prefix()+"dtmf",b);}catch(Exception e){state="DTMF 未确认，未重复发送";service.changed();}});}
}
