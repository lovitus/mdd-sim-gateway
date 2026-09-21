package com.lovitus.mddagent;
import okhttp3.*;
import org.json.*;
import java.util.concurrent.*;
import android.os.SystemClock;
/** Serialized connection ownership. Old callbacks cannot resurrect a replaced socket. */
final class Link implements AutoCloseable {
    interface Events {void message(JSONObject o);void state(String text,boolean connected);}
    private final GatewayApi api;private final ScheduledExecutorService loop;private final Events events;private final String path,agentID,agentToken,process;
    private final Retry retry=new Retry();private volatile WebSocket socket;private volatile long epoch,opened,lastMessage;private volatile boolean stopped,terminal,acknowledged;
    private ScheduledFuture<?> pending,deadline;private long sequence;private String revision="";
    Link(GatewayApi api,ScheduledExecutorService loop,String path,String agentID,String agentToken,String process,Events events){this.api=api;this.loop=loop;this.path=path;this.agentID=agentID;this.agentToken=agentToken;this.process=process;this.events=events;}
    void connect(){loop.execute(this::open);}
    private void open(){
        if(stopped||terminal||socket!=null)return;if(pending!=null)pending.cancel(false);final long mine=++epoch;
        Request.Builder request=api.request(path);
        if(!agentID.isEmpty())request.removeHeader("X-MDD-Session").removeHeader("Cookie").header("Authorization","Bearer "+agentToken).header("X-MDD-Agent-ID",agentID);
        opened=lastMessage=SystemClock.elapsedRealtime();acknowledged=false;sequence=0;revision="";events.state("Connecting",false);
        socket=api.http.newWebSocket(request.build(),new WebSocketListener(){
            public void onOpen(WebSocket ws,Response response){post(()->{if(!current(mine,ws))return;if(!agentID.isEmpty())ws.send(Json.obj("kind","hello","hello",Json.obj("schema_version",1,"agent_id",agentID,"process_generation",process)).toString());});}
            public void onMessage(WebSocket ws,String text){post(()->{if(!current(mine,ws))return;try{
                if(text.length()>2*1024*1024)throw new IllegalArgumentException();JSONObject o=new JSONObject(text);lastMessage=SystemClock.elapsedRealtime();
                if(!agentID.isEmpty()&&!acknowledged){if(!o.optString("kind").equals("hello_ack"))throw new IllegalArgumentException();acknowledged=true;events.state("Reader link online",true);}
                else if(agentID.isEmpty()){String type=o.optString("type");if(!type.equals("mobile.snapshot")&&!type.equals("mobile.heartbeat"))throw new IllegalArgumentException();if(o.optInt("schema_version")!=1)throw new IllegalArgumentException();if(!acknowledged&& !type.equals("mobile.snapshot"))throw new IllegalArgumentException();acknowledged=true;events.state("Online",true);}
                if(lastMessage-opened>30000)retry.healthy();events.message(o);
            }catch(Exception invalid){terminal=true;failed(mine,"Unsupported gateway message",false);}});}
            public void onClosing(WebSocket ws,int code,String reason){ws.close(code,null);}
            public void onClosed(WebSocket ws,int code,String reason){post(()->{if(!current(mine,ws))return;if(code==4401||code==1008)terminal=true;failed(mine,terminal?"Sign in / reader enrollment required":"Connection interrupted",!terminal);});}
            public void onFailure(WebSocket ws,Throwable error,Response response){post(()->{if(!current(mine,ws))return;int code=response==null?0:response.code();terminal=code==401||code==403||code==404||error instanceof javax.net.ssl.SSLException;failed(mine,code==404?"Update Core for native mobile support":terminal?"Authentication or TLS verification failed":"Waiting for network",!terminal);});}
        });
        if(deadline!=null)deadline.cancel(false);
        deadline=loop.schedule(()->{if(epoch==mine&&!acknowledged)failed(mine,"Handshake timed out",true);},15,TimeUnit.SECONDS);
    }
    private void post(Runnable r){try{loop.execute(r);}catch(RejectedExecutionException ignored){}}
    private boolean current(long mine,WebSocket ws){return !stopped&&epoch==mine&&socket==ws;}
    private void failed(long mine,String text,boolean recover){if(epoch!=mine||stopped)return;epoch++;WebSocket old=socket;socket=null;acknowledged=false;if(old!=null)old.cancel();if(deadline!=null)deadline.cancel(false);events.state(text,false);if(recover&&!terminal){long wait=retry.next();pending=loop.schedule(this::open,wait,TimeUnit.MILLISECONDS);}}
    void networkChanged(){post(()->{if(stopped||terminal)return;if(pending!=null)pending.cancel(false);epoch++;if(socket!=null)socket.cancel();socket=null;acknowledged=false;events.state("Network changed; reconnecting",false);pending=loop.schedule(this::open,400,TimeUnit.MILLISECONDS);});}
    void health(JSONObject topology){post(()->{if(stopped||!acknowledged||socket==null)return;
        String next=Json.sha(topology.toString().getBytes(java.nio.charset.StandardCharsets.UTF_8));JSONObject health=Json.obj("schema_version",1,"sequence",++sequence,"topology_revision",next);
        if(!next.equals(revision)){try{health.put("topology",topology);}catch(JSONException ignored){return;}}revision=next;
        if(socket.queueSize()>65536||!socket.send(Json.obj("kind","health","health",health).toString()))failed(epoch,"Reader link backpressure",true);
    });}
    long generation(){return epoch;}
    void respond(long expected,String requestID,JSONObject result){post(()->{if(epoch==expected&&acknowledged&&socket!=null)socket.send(Json.obj("kind","aka_response","request_id",requestID,"aka_response",result).toString());});}
    void checkFreshness(){post(()->{if(agentID.isEmpty()&&acknowledged&&SystemClock.elapsedRealtime()-lastMessage>65000)failed(epoch,"Gateway heartbeat missed",true);});}
    public void close(){stopped=true;post(()->{epoch++;if(pending!=null)pending.cancel(false);if(deadline!=null)deadline.cancel(false);if(socket!=null)socket.cancel();socket=null;});}
}
