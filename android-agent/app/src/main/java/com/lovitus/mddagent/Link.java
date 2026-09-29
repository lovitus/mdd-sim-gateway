package com.lovitus.mddagent;
import okhttp3.*;
import org.json.*;
import java.util.concurrent.*;
import android.os.SystemClock;
/** Serialized connection ownership. Old callbacks cannot resurrect a replaced socket. */
final class Link implements AutoCloseable {
    interface Events {void message(JSONObject o);void state(int label,boolean connected);default void authenticationRequired(){}}
    private final GatewayApi api;private final ScheduledExecutorService loop;private final Events events;private final String path,agentID,agentToken,process;
    private final Retry retry=new Retry();private volatile WebSocket socket;private volatile long epoch,opened,lastMessage;private volatile boolean stopped,terminal,acknowledged;private int retryState;
    private ScheduledFuture<?> pending,deadline;private long sequence;private String revision="";
    private volatile String diagnostic="";
    Link(GatewayApi api,ScheduledExecutorService loop,String path,String agentID,String agentToken,String process,Events events){this.api=api;this.loop=loop;this.path=path;this.agentID=agentID;this.agentToken=agentToken;this.process=process;this.events=events;}
    void connect(){loop.execute(this::open);}
    private void open(){
        if(stopped||terminal||socket!=null)return;if(pending!=null)pending.cancel(false);pending=null;final long mine=++epoch;
        Request.Builder request=api.request(path);
        if(!agentID.isEmpty())request.removeHeader("X-MDD-Session").removeHeader("Cookie").header("Authorization","Bearer "+agentToken).header("X-MDD-Agent-ID",agentID);
        opened=lastMessage=SystemClock.elapsedRealtime();acknowledged=false;sequence=0;revision="";if(retryState==0)events.state(R.string.link_connecting,false);else events.state(retryState,false);
        socket=api.http.newWebSocket(request.build(),new WebSocketListener(){
            public void onOpen(WebSocket ws,Response response){if(stopped){ws.cancel();return;}post(()->{if(!current(mine,ws)){ws.cancel();return;}if(!agentID.isEmpty())ws.send(Json.obj("kind","hello","hello",Json.obj("schema_version",1,"agent_id",agentID,"process_generation",process)).toString());});}
            public void onMessage(WebSocket ws,String text){post(()->{if(!current(mine,ws))return;try{
                if(text.length()>2*1024*1024)throw new IllegalArgumentException();JSONObject o=new JSONObject(text);lastMessage=SystemClock.elapsedRealtime();
                if(!agentID.isEmpty()&&!acknowledged){if(!o.optString("kind").equals("hello_ack"))throw new IllegalArgumentException();acknowledged=true;retryState=0;events.state(R.string.link_reader_online,true);}
                else if(agentID.isEmpty()){String type=o.optString("type");if(!type.equals("mobile.snapshot")&&!type.equals("mobile.heartbeat"))throw new IllegalArgumentException();if(o.optInt("schema_version")!=1)throw new IllegalArgumentException();if(!acknowledged&& !type.equals("mobile.snapshot"))throw new IllegalArgumentException();acknowledged=true;retryState=0;events.state(R.string.link_online,true);}
                if(lastMessage-opened>30000)retry.healthy();events.message(o);
            }catch(Exception invalid){diagnostic="invalid_protocol_message";failed(mine,R.string.link_schema,true);}});}
            public void onClosing(WebSocket ws,int code,String reason){ws.close(code,null);}
            public void onClosed(WebSocket ws,int code,String reason){post(()->{if(!current(mine,ws))return;diagnostic="ws_close_"+code;switch(reason){case "invalid hello":case "invalid health":case "invalid response":case "Agent admission changed":diagnostic+="_"+reason.replace(' ','_');break;default:break;}if(code==4401||code==1008)terminal=true;failed(mine,terminal?R.string.link_auth_required:R.string.link_interrupted,!terminal);if(code==4401&&agentID.isEmpty())events.authenticationRequired();});}
            public void onFailure(WebSocket ws,Throwable error,Response response){post(()->{if(!current(mine,ws))return;int code=response==null?0:response.code();boolean identityFailure=identityFailure(error);diagnostic="http_"+code+"_"+error.getClass().getSimpleName();terminal=code==401||code==403||identityFailure;failed(mine,code==404?R.string.link_upgrade:terminal?R.string.link_auth_tls:R.string.link_wait_network,!terminal);if(code==401&&agentID.isEmpty())events.authenticationRequired();});}
        });
        if(stopped){socket.cancel();socket=null;return;}
        if(deadline!=null)deadline.cancel(false);
        deadline=loop.schedule(()->{if(epoch==mine&&!acknowledged)failed(mine,R.string.link_handshake_timeout,true);},15,TimeUnit.SECONDS);
    }
    // Same certificate-vs-transport distinction as OkHttp's retry policy; no HTTP retries enabled.
    static boolean identityFailure(Throwable error){
        for(Throwable cause=error;cause!=null;cause=cause.getCause())
            if(cause instanceof java.security.cert.CertificateException||cause instanceof javax.net.ssl.SSLPeerUnverifiedException)return true;
        return false;
    }
    private void post(Runnable r){try{loop.execute(r);}catch(RejectedExecutionException ignored){}}
    private boolean current(long mine,WebSocket ws){return !stopped&&epoch==mine&&socket==ws;}
    private void failed(long mine,int label,boolean recover){if(epoch!=mine||stopped)return;epoch++;WebSocket old=socket;socket=null;acknowledged=false;if(old!=null)old.cancel();if(deadline!=null)deadline.cancel(false);retryState=label;events.state(label,false);if(recover&&!terminal){long wait=retry.next();pending=loop.schedule(this::open,wait,TimeUnit.MILLISECONDS);}}
    void networkChanged(){post(()->{
        if(stopped||terminal)return;
        // Coalesce network storms without cancelling an already-budgeted attempt.
        if(socket==null&&pending!=null&&!pending.isDone())return;
        epoch++;if(socket!=null)socket.cancel();socket=null;acknowledged=false;
        if(deadline!=null)deadline.cancel(false);
        retryState=R.string.link_network_changed;events.state(retryState,false);
        pending=loop.schedule(this::open,retry.next(),TimeUnit.MILLISECONDS);
    });}
    void health(JSONObject topology){post(()->{if(stopped||!acknowledged||socket==null)return;
        String next=Json.sha(topology.toString().getBytes(java.nio.charset.StandardCharsets.UTF_8));JSONObject health=Json.obj("schema_version",1,"sequence",++sequence,"topology_revision",next);
        if(!next.equals(revision)){try{health.put("topology",topology);}catch(JSONException ignored){return;}}revision=next;
        if(socket.queueSize()>65536||!socket.send(Json.obj("kind","health","health",health).toString()))failed(epoch,R.string.link_backpressure,true);
    });}
    long generation(){return epoch;}
    String diagnostic(){return diagnostic;}
    void sessionRenewed(){post(()->{if(stopped)return;terminal=false;retryState=0;open();});}
    void respond(long expected,String requestID,JSONObject result){post(()->{if(epoch==expected&&acknowledged&&socket!=null)socket.send(Json.obj("kind","aka_response","request_id",requestID,"aka_response",result).toString());});}
    void checkFreshness(){post(()->{if(agentID.isEmpty()&&acknowledged&&SystemClock.elapsedRealtime()-lastMessage>65000)failed(epoch,R.string.link_heartbeat_missed,true);});}
    public void close(){stopped=true;WebSocket active=socket;if(active!=null)active.cancel();post(()->{epoch++;if(pending!=null)pending.cancel(false);if(deadline!=null)deadline.cancel(false);if(socket!=null)socket.cancel();socket=null;});}
}
